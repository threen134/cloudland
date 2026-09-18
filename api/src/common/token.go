/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"fmt"

	"api/src/model"

	"github.com/golang-jwt/jwt/v4"
	"golang.org/x/crypto/sha3"
)

// Console types, as understood by the console proxy (virtconsoleproxyd)
const (
	ConsoleTypeVNC    = "vnc"
	ConsoleTypeSerial = "serial"
	// ConsoleTypeHost is a root shell on a hypervisor. The proxy relays it like a serial console,
	// so it is only a token type and resolves to ConsoleTypeSerial
	ConsoleTypeHost = "host"
)

type TokenClaim struct {
	OrgID      int64
	OrgRole    model.OrgRole
	InstanceID int    `json:"instanceID"`
	Secret     string `json:"secret"`
	// Empty in tokens issued before serial consoles existed, which are VNC tokens
	ConsoleType string `json:"consoleType,omitempty"`
	// Host console only: the hypervisor, the name of the system admin who opened it (recorded on the node)
	// and the terminal size the shell starts with
	HostID   int32  `json:"hostID,omitempty"`
	Operator string `json:"operator,omitempty"`
	Rows     int    `json:"rows,omitempty"`
	Cols     int    `json:"cols,omitempty"`
	jwt.RegisteredClaims
}

// Type returns the console type of the token
func (c *TokenClaim) Type() string {
	if c.ConsoleType == "" {
		return ConsoleTypeVNC
	}
	return c.ConsoleType
}

// ConsoleSecretHash is the value stored for a console token's secret
func ConsoleSecretHash(secret string) string {
	tokenHash := make([]byte, 32)
	data := sha3.NewShake256()
	data.Write([]byte(secret))
	data.Read(tokenHash)
	return fmt.Sprintf("%x", tokenHash)
}
