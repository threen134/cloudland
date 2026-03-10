/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"net/http"
	"strconv"

	. "web/src/common"
	"web/src/model"
	"web/src/routes"

	"github.com/gin-gonic/gin"
)

const (
	TokenType = "bearer"
	AppName   = "Cloudland"
)

func Authorize() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := c.Request.Header.Get("Authorization")
		if tokenStr == "" {
			ErrorResponse(c, http.StatusUnauthorized, "Invalid Token", nil)
			c.Abort()
			return
		}
		tokenStr = tokenStr[len(TokenType)+1:]
		// Use expected audience as AppName (or set as needed)
		_, claims, err := routes.ParseToken(tokenStr)
		if err != nil {
			ErrorResponse(c, http.StatusUnauthorized, "Invalid Token", err)
			c.Abort()
			return
		}
		if claims.Issuer != AppName {
			ErrorResponse(c, http.StatusUnauthorized, "Invalid Token", nil)
			c.Abort()
			return
		}

		// Parse user ID and org ID from JWT claims
		uid, err := strconv.ParseInt(claims.UID, 10, 64)
		if err != nil {
			ErrorResponse(c, http.StatusUnauthorized, "Invalid user ID in token", err)
			c.Abort()
			return
		}
		var oid int64
		if claims.OID == "" {
			oid = 0
		} else {
			oid, err = strconv.ParseInt(claims.OID, 10, 64)
			if err != nil {
				ErrorResponse(c, http.StatusUnauthorized, "Invalid org ID in token", err)
				c.Abort()
				return
			}
		}

		// X-Resource-Org: only SystemAdmin can switch org context
		realOrgStr := c.Request.Header.Get("X-Resource-Org")
		if realOrgStr != "" {
			if claims.SR != model.SystemAdmin {
				ErrorResponse(c, http.StatusForbidden, "Not authorized to switch org context", nil)
				c.Abort()
				return
			}
			realOrgID, parseErr := strconv.ParseInt(realOrgStr, 10, 64)
			if parseErr != nil {
				ErrorResponse(c, http.StatusBadRequest, "Invalid X-Resource-Org value", parseErr)
				c.Abort()
				return
			}
			oid = realOrgID
		}

		// Build MemberShip from DB
		memberShip, err := GetDBMemberShip(uid, oid)
		if err != nil {
			// If user has no Org (Dormant), still allow through with empty membership
			if oid == 0 && claims.ST == model.UserDormant {
				userEmail := ""
				if len(claims.Audience) > 0 {
					userEmail = claims.Audience[0]
				}
				memberShip = &MemberShip{
					UserID:     uid,
					UserEmail:  userEmail,
					SystemRole: claims.SR,
					OrgID:      0,
					OrgRole:    model.OrgNone,
				}
			} else {
				ErrorResponse(c, http.StatusBadRequest, "Invalid resource user with org membership", err)
				c.Abort()
				return
			}
		}
		logger.Infof("MemberShip: %v\n", memberShip)
		ctx := memberShip.SetContext(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
