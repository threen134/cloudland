/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package common

import (
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
)

// type JSONTime time.Time

type BaseReference struct {
	ID   string `json:"id" binding:"omitempty,uuid"`
	Name string `json:"name" binding:"omitempty,min=2,max=36"`
}

type ResourceReference struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Owner string `json:"owner,omitempty"`
	// OwnerUUID identifies the owning org across services (org names are not unique); cpgateway uses it to
	// release quota only when the caller's org actually owns the deleted resource
	OwnerUUID string `json:"owner_uuid,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type BaseID struct {
	ID string `json:"id" binding:"required,uuid"`
}

type APIError struct {
	ErrorCode    int    `json:"error_code"`
	ErrorCodeStr string `json:"error_code_str,omitempty"`
	ErrorMessage string `json:"error_message"`
}

// isDatabaseError reports whether err wraps a database server error. Its text can carry SQL fragments and
// column values (error-based SQL injection reads data through it), so it only goes to the log
func isDatabaseError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr)
}

func ErrorResponse(c *gin.Context, code int, errorMsg string, err error) {
	logger.Ctx(c).Errorf("%s, %v\n", errorMsg, err)
	status := code
	if err != nil {
		var clErr *CLError
		if errors.As(err, &clErr) {
			// If business code has a specific mapping, override the default code
			if businessStatus := clErr.Code.ToHTTPStatus(); businessStatus != 500 {
				status = businessStatus
			}
			message := clErr.Error()
			if isDatabaseError(err) {
				message = fmt.Sprintf("Code %d: %s", clErr.Code, clErr.Message)
			}
			c.JSON(status, &APIError{
				ErrorCode:    int(clErr.Code),
				ErrorCodeStr: clErr.Code.String(),
				ErrorMessage: message,
			})
			return
		}
		if !isDatabaseError(err) {
			errorMsg = errorMsg + ": " + err.Error()
		}
	}
	c.JSON(status, &APIError{
		ErrorCode:    status,
		ErrorMessage: errorMsg,
	})
}

func (e *APIError) Error() string {
	return fmt.Sprintf("CLError: code=%d, message=%s", e.ErrorCode, e.ErrorMessage)
}

/*
type Marshaler interface {
    MarshalJSON() ([]byte, error)
}

func (t JSONTime)MarshalJSON() ([]byte, error) {
    //do your serializing here
    stamp := fmt.Sprintf("\"%s\"", time.Time(t).Format("Mon Jan _2"))
    return []byte(stamp), nil
}
*/
