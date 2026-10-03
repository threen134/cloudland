/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"strings"
	"time"

	. "api/src/common"
	"api/src/model"

	jwt "github.com/golang-jwt/jwt/v4"
)

// Host console: a root shell on a hypervisor for system admins, relayed like a serial console.
// Settings are mirrored from cpgateway; the defaults and range must match its HOST_CONSOLE_* settings.
const (
	HostConsoleEnabledSetting     = "HOST_CONSOLE_ENABLED"
	HostConsoleIdleMinutesSetting = "HOST_CONSOLE_IDLE_MINUTES"
	DefaultHostConsoleIdleMinutes = 15
	minHostConsoleIdleMinutes     = 5
	maxHostConsoleIdleMinutes     = 240
	// The console page connects right after it gets the token, and the token is used only once
	hostConsoleTokenDuration = 2 * time.Minute
)

// HostConsoleEnabled reports whether system admins may open host consoles (off unless enabled)
func HostConsoleEnabled() bool {
	return strings.EqualFold(GetMirrorSetting(HostConsoleEnabledSetting), "true")
}

// HostConsoleIdleSeconds is how long a host console session may pass without traffic before it is closed
func HostConsoleIdleSeconds() int {
	minutes := GetMirrorSettingInt(HostConsoleIdleMinutesSetting, DefaultHostConsoleIdleMinutes)
	if minutes < minHostConsoleIdleMinutes || minutes > maxHostConsoleIdleMinutes {
		minutes = DefaultHostConsoleIdleMinutes
	}
	return minutes * 60
}

// MakeHostToken issues a single-use console token for a root shell on the hypervisor. rows and cols are the
// terminal size the shell starts with (0 leaves the default).
func MakeHostToken(ctx context.Context, hyper *model.Hyper, rows, cols int) (token string, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckSystemPermission() {
		return "", NewCLError(ErrPermissionDenied, "Only system admins can open a host console", nil)
	}
	if !HostConsoleEnabled() {
		return "", NewCLError(ErrPermissionDenied, "Host console is disabled in system settings", nil)
	}
	secret := RandomStr()
	claim := TokenClaim{
		OrgID:       memberShip.OrgID,
		OrgRole:     memberShip.OrgRole,
		Secret:      secret,
		ConsoleType: ConsoleTypeHost,
		HostID:      hyper.Hostid,
		Operator:    memberShip.UserName,
		Rows:        rows,
		Cols:        cols,
	}
	claim.RegisteredClaims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(hostConsoleTokenDuration))
	ctx, db := GetContextDB(ctx)
	console := &model.Console{
		HostID:     hyper.Hostid,
		Type:       ConsoleTypeHost,
		HashSecret: ConsoleSecretHash(secret),
	}
	// One pending token per hypervisor: opening another console invalidates a token not used yet
	err = db.Where("host_id = ? AND type = ?", hyper.Hostid, ConsoleTypeHost).Assign(console).FirstOrCreate(&model.Console{}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to make host console record ", err)
		return "", NewCLError(ErrConsoleCreateFailed, "Failed to make console record", err)
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claim).SignedString(SignedSeret)
}
