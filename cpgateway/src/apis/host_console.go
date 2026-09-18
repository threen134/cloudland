package apis

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"cpgateway/src/common"
)

// hostConsoleTemplate is the backend route that opens a root shell on a hypervisor. It is a system admin route
// that additionally asks for the caller's password, so a stolen session token alone cannot open a node shell.
const hostConsoleTemplate = "/hypers/{hostid}/console"

const (
	hostConsoleMaxFailures   = 5
	hostConsoleFailureWindow = 10 * time.Minute
)

var errHostConsoleRejected = errors.New("host console request rejected")

// hostConsoleFailures counts wrong passwords per user within hostConsoleFailureWindow. In memory: a restart or
// another gateway replica starts from zero, which only matters for guessing and still leaves login throttling.
var hostConsoleFailures = struct {
	sync.Mutex
	byUser map[int64][]time.Time
}{byUser: map[int64][]time.Time{}}

func recentHostConsoleFailures(userID int64, now time.Time) []time.Time {
	recent := hostConsoleFailures.byUser[userID][:0]
	for _, t := range hostConsoleFailures.byUser[userID] {
		if now.Sub(t) < hostConsoleFailureWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(hostConsoleFailures.byUser, userID)
		return nil
	}
	hostConsoleFailures.byUser[userID] = recent
	return recent
}

// reserveHostConsoleAttempt counts an attempt of a user and reports whether it may go on to the password check.
// The attempt is counted before the password is verified and dropped again once it turns out to be correct:
// counting only failures would let every request arriving during the ~100ms bcrypt read the same count and pass
// the limit together. An attempt that is already over the limit is not recorded, so retrying cannot push the
// window forward forever.
func reserveHostConsoleAttempt(userID int64, now time.Time) bool {
	hostConsoleFailures.Lock()
	defer hostConsoleFailures.Unlock()
	attempts := recentHostConsoleFailures(userID, now)
	if len(attempts) >= hostConsoleMaxFailures {
		return false
	}
	hostConsoleFailures.byUser[userID] = append(attempts, now)
	return true
}

// clearHostConsoleAttempts forgets the attempts of a user whose password was correct
func clearHostConsoleAttempts(userID int64) {
	hostConsoleFailures.Lock()
	defer hostConsoleFailures.Unlock()
	delete(hostConsoleFailures.byUser, userID)
}

type hostConsoleRequest struct {
	Password string `json:"password"`
	Rows     *int   `json:"rows,omitempty"`
	Cols     *int   `json:"cols,omitempty"`
}

// verifyHostConsolePassword checks the password in the request body against the current user and returns the
// body to forward without it. On failure the response is written and an error returned.
func verifyHostConsolePassword(c *gin.Context, body []byte) ([]byte, error) {
	user := currentUser(c)
	req := &hostConsoleRequest{}
	if len(body) == 0 || json.Unmarshal(body, req) != nil || req.Password == "" {
		common.AbortWithDetail(c, http.StatusBadRequest, "Password is required to open a host console")
		return nil, errHostConsoleRejected
	}

	if !reserveHostConsoleAttempt(user.ID, time.Now()) {
		common.AbortWithDetail(c, http.StatusTooManyRequests, "Too many incorrect passwords, try again later")
		return nil, errHostConsoleRejected
	}

	if !common.VerifyPassword(req.Password, user.HashedPassword) {
		log.WithContext(c.Request.Context()).Warnf("Host console for hypervisor %s refused: incorrect password of user %s", c.Param("hostid"), user.Username)
		common.AbortWithDetail(c, http.StatusForbidden, "Incorrect password")
		return nil, errHostConsoleRejected
	}

	clearHostConsoleAttempts(user.ID)
	forwarded, err := json.Marshal(struct {
		Rows *int `json:"rows,omitempty"`
		Cols *int `json:"cols,omitempty"`
	}{req.Rows, req.Cols})
	if err != nil {
		internalServerError(c, err)
		return nil, err
	}
	return forwarded, nil
}
