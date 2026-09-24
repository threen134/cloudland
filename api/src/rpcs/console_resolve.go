/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package rpcs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	jwt "github.com/golang-jwt/jwt/v4"
	"github.com/sethvargo/go-password/password"
	"gopkg.in/macaron.v1"
)

var (
	consoleAdmin = &ConsoleAdmin{}
)

type ConsoleAdmin struct{}

type ConsoleInfo struct {
	Type      string `json:"type"`
	Address   string `json:"address"`
	Insecure  bool   `json:"insecure"`
	TLSTunnel bool   `json:"tlsTunnel"`
	Password  string `json:"password"`
}

// ResolveToken validates a console token against its console record. A host console record is deleted once
// resolved, so its token opens a single session.
func ResolveToken(ctx context.Context, tokenString string) (*TokenClaim, error) {
	token, err := jwt.ParseWithClaims(tokenString, &TokenClaim{}, func(token *jwt.Token) (interface{}, error) {
		return SignedSeret, nil
	})
	if err != nil || token == nil {
		return nil, err
	}
	claims, ok := token.Claims.(*TokenClaim)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}
	console := &model.Console{}
	ctx, db := GetContextDB(ctx)
	query := db.Where("instance = ? AND type = ?", claims.InstanceID, claims.Type())
	if claims.Type() == ConsoleTypeHost {
		query = db.Where("host_id = ? AND type = ?", claims.HostID, ConsoleTypeHost)
	}
	if err = query.Take(console).Error; err != nil {
		return nil, err
	}
	if ConsoleSecretHash(claims.Secret) != console.HashSecret {
		return nil, errors.New("Secret can not pass validation")
	}
	if claims.Type() == ConsoleTypeHost {
		// Only the request that deletes the record may use the token
		result := db.Unscoped().Where("id = ? AND hash_secret = ?", console.ID, console.HashSecret).Delete(&model.Console{})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			return nil, errors.New("host console token already used")
		}
	}
	return claims, nil
}

func (a *ConsoleAdmin) ConsoleResolve(c *macaron.Context) {
	var err error
	ctx := c.Req.Context()
	db := DB()
	token := c.Params("token")
	logger.Ctx(ctx).Debug("Get JWT token", token)
	claims, err := ResolveToken(ctx, token)
	if err != nil {
		logger.Ctx(ctx).Error("Unable to resolve token", err)
		code := http.StatusUnauthorized
		c.Error(code, http.StatusText(code))
		return
	}
	if claims.Type() == ConsoleTypeHost {
		a.resolveHost(ctx, c, claims)
		return
	}
	instanceID, consoleType := claims.InstanceID, claims.Type()
	memberShip := &MemberShip{OrgID: claims.OrgID, OrgRole: claims.OrgRole}
	permit := memberShip.CheckOrgPermission(model.OrgWriter)
	if !permit {
		logger.Ctx(ctx).Error("Not authorized for this operation")
		code := http.StatusForbidden
		c.Error(code, http.StatusText(code))
		return
	}
	instance := &model.Instance{Model: model.Model{ID: int64(instanceID)}}
	err = db.Take(instance).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to get instance", err)
		code := http.StatusInternalServerError
		c.Error(code, http.StatusText(code))
		return
	}
	if consoleType == ConsoleTypeSerial {
		a.resolveSerial(ctx, c, instance)
		return
	}

	accessPass, err := password.Generate(8, 2, 0, false, false)
	if err != nil {
		logger.Ctx(ctx).Error("Failed to generate password")
		code := http.StatusInternalServerError
		c.Error(code, http.StatusText(code))
		return
	}
	// Query by column instead of reusing a struct as the condition: soft delete writes DeletedAt back into
	// the struct, and a later Where(vnc) would add deleted_at = <that time>, which never matches
	err = db.Where("instance_id = ?", instanceID).Delete(&model.Vnc{}).Error
	if err != nil {
		logger.Ctx(ctx).Error("VNC record deletion failed", err)
	}
	control := fmt.Sprintf("inter=%d", instance.Hyper)
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/set_vnc_passwd.sh '%d' '%s'", instance.ID, ShellEscape(accessPass))
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Ctx(ctx).Error("Set vnc password execution failed", err)
		c.JSON(http.StatusInternalServerError, &APIError{ErrorMessage: "Internal error"})
		return
	}

	// set_vnc_passwd.sh reports the VNC address through its callback, which recreates the record
	vnc := &model.Vnc{}
	for i := 0; i < 10; i++ {
		time.Sleep(time.Duration(i) * time.Second)
		vnc = &model.Vnc{}
		err = db.Where("instance_id = ?", instanceID).Take(vnc).Error
		if err == nil {
			logger.Ctx(ctx).Debugf("Got VNC record of instance %d after %d tries", instanceID, i+1)
			break
		}
	}
	if vnc.LocalAddress == "" {
		logger.Ctx(ctx).Error("Failed to get VNC record", err)
		c.JSON(http.StatusInternalServerError, &APIError{ErrorMessage: "Internal error"})
		return
	}
	address := fmt.Sprintf("%s:%d", vnc.LocalAddress, vnc.LocalPort)
	consoleInfo := &ConsoleInfo{
		Type:      "vnc",
		Address:   address,
		Insecure:  true,
		TLSTunnel: false,
		Password:  accessPass,
	}

	c.JSON(http.StatusOK, consoleInfo)
	return
}

// consoleProxySource is the address the console proxy's connections to compute nodes come from. The proxy
// resolves tokens through the clapi on its own host, and in an HA control plane that host's address differs
// from MANAGEMENT_VIP (a secondary address that outgoing connections do not use), so the node has to allow it
// explicitly. Empty when unknown; the node still allows the controller endpoint.
func consoleProxySource() string {
	ip := net.ParseIP(strings.TrimSpace(os.Getenv("INTERNAL_IP")))
	if ip == nil || ip.To4() == nil || ip.IsLoopback() {
		return ""
	}
	return ip.String()
}

// resolveSerial asks the instance's hypervisor to expose the serial console pty on a one-time TCP port
// (start_serial_console.sh) and returns that address. The console proxy relays the raw bytes between the
// websocket and the port; the node only accepts the connection from the control plane.
func (a *ConsoleAdmin) resolveSerial(ctx context.Context, c *macaron.Context, instance *model.Instance) {
	a.relayConsole(ctx, c, instance.Hyper, "instance_id = ?", instance.ID, func(session string) string {
		return fmt.Sprintf("/opt/cloudland/scripts/backend/start_serial_console.sh '%d' '%s' '%s'",
			instance.ID, ShellEscape(session), ShellEscape(consoleProxySource()))
	})
}

// consoleOperatorPattern matches what start_host_console.sh accepts as the operator name
var consoleOperatorPattern = regexp.MustCompile(`[^A-Za-z0-9._@-]`)

// resolveHost asks the hypervisor for a root shell on a one-time TCP port (start_host_console.sh). The token was
// issued to a system admin; the setting is checked again because it may have been turned off since.
func (a *ConsoleAdmin) resolveHost(ctx context.Context, c *macaron.Context, claims *TokenClaim) {
	if !services.HostConsoleEnabled() {
		logger.Ctx(ctx).Error("Host console is disabled")
		code := http.StatusForbidden
		c.Error(code, http.StatusText(code))
		return
	}
	operator := consoleOperatorPattern.ReplaceAllString(claims.Operator, "_")
	if len(operator) > 64 {
		operator = operator[:64]
	}
	if operator == "" {
		operator = "unknown"
	}
	logger.Ctx(ctx).Infof("Opening host console on hypervisor %d for %s", claims.HostID, claims.Operator)
	a.relayConsole(ctx, c, claims.HostID, "host_id = ? AND instance_id = 0", claims.HostID, func(session string) string {
		return fmt.Sprintf("/opt/cloudland/scripts/backend/start_host_console.sh '%s' '%s' '%d' '%s' '%d' '%d'",
			ShellEscape(session), ShellEscape(operator), services.HostConsoleIdleSeconds(),
			ShellEscape(consoleProxySource()), claims.Rows, claims.Cols)
	})
}

// relayConsole runs the command built for a new session on the hypervisor, waits for the callback that records
// the listening address of that session (matched by owner, a condition with one argument) and returns the
// address for the console proxy to relay as a serial console.
func (a *ConsoleAdmin) relayConsole(ctx context.Context, c *macaron.Context, hostID int32, owner string, ownerArg interface{}, command func(session string) string) {
	db := DB()
	// Records left by abandoned requests (any console, including deleted instances); recent ones may belong to a
	// request still waiting for its callback
	err := db.Unscoped().Where("created_at < ?", time.Now().Add(-10*time.Minute)).Delete(&model.SerialConsole{}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Serial console record cleanup failed", err)
	}
	// Each request waits for the record of its own session: two consoles opened at the same time must not
	// pick up each other's port
	sessionBytes := make([]byte, 16)
	if _, err = rand.Read(sessionBytes); err != nil {
		logger.Ctx(ctx).Error("Failed to generate console session", err)
		c.JSON(http.StatusInternalServerError, &APIError{ErrorMessage: "Internal error"})
		return
	}
	session := hex.EncodeToString(sessionBytes)
	if err = HyperExecute(ctx, fmt.Sprintf("inter=%d", hostID), command(session)); err != nil {
		logger.Ctx(ctx).Error("Start console execution failed", err)
		c.JSON(http.StatusInternalServerError, &APIError{ErrorMessage: "Internal error"})
		return
	}
	// The script's callback creates the record once the port is listening
	serial := &model.SerialConsole{}
	for i := 0; i < 40; i++ {
		time.Sleep(500 * time.Millisecond)
		serial = &model.SerialConsole{}
		if err = db.Where(owner, ownerArg).Where("session = ?", session).Take(serial).Error; err == nil {
			break
		}
	}
	if serial.LocalAddress == "" {
		logger.Ctx(ctx).Error("Failed to get console address record", err)
		c.JSON(http.StatusInternalServerError, &APIError{ErrorMessage: "Internal error"})
		return
	}
	if err = db.Unscoped().Delete(serial).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to delete used console address record", err)
	}
	c.JSON(http.StatusOK, &ConsoleInfo{
		Type:     ConsoleTypeSerial,
		Address:  fmt.Sprintf("%s:%d", serial.LocalAddress, serial.LocalPort),
		Insecure: true,
	})
}
