/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"api/src/model"

	"github.com/golang-jwt/jwt/v4"
)

// Console types, as understood by the console proxy (virtconsoleproxyd)
const (
	ConsoleTypeVNC    = "vnc"
	ConsoleTypeSerial = "serial"
)

type TokenClaim struct {
	OrgID      int64
	OrgRole    model.OrgRole
	InstanceID int    `json:"instanceID"`
	Secret     string `json:"secret"`
	// Empty in tokens issued before serial consoles existed, which are VNC tokens
	ConsoleType string `json:"consoleType,omitempty"`
	jwt.RegisteredClaims
}

// Type returns the console type of the token
func (c *TokenClaim) Type() string {
	if c.ConsoleType == "" {
		return ConsoleTypeVNC
	}
	return c.ConsoleType
}
