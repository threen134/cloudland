/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"api/src/model"

	"github.com/golang-jwt/jwt/v4"
)

type TokenClaim struct {
	OrgID      int64
	OrgRole    model.OrgRole
	InstanceID int    `json:"instanceID"`
	Secret     string `json:"secret"`
	jwt.RegisteredClaims
}
