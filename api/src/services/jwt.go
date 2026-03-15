/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

// jwt.go is deprecated. Token issuance and verification are now handled by Middle.
// Cloudland receives identity via X-* headers set by Middle's proxy layer.
// This file is kept only for type definitions used by legacy code during migration.

package services

import (
	"fmt"

	"api/src/model"
)

// CustomClaims is deprecated. Kept for compile compatibility during migration.
type CustomClaims struct {
	UID string           `json:"uid,omitempty"`
	OID string           `json:"oid,omitempty"`
	SR  model.SystemRole `json:"sr"`
	OR  model.OrgRole    `json:"or"`
	ST  model.UserStatus `json:"st"`
}

// NewToken is deprecated. Token issuance is handled by Middle.
// Returns an error — callers should not rely on Cloudland-signed tokens.
func NewToken(u, o, uid, oid string, sysRole model.SystemRole, orgRole model.OrgRole, status model.UserStatus) (signed string, issueAt, expiresAt int64, err error) {
	err = fmt.Errorf("NewToken is deprecated: token issuance is handled by Middle")
	logger.Error("NewToken called — this function is deprecated. Token issuance should be handled by Middle.")
	return
}
