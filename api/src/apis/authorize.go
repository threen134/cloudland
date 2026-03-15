/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"net/http"
	"strconv"

	. "api/src/common"
	"api/src/model"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

// Authorize validates requests forwarded from CPGateway.
// CPGateway has already verified the JWT and set X-* headers.
// Cloudland only needs to:
// 1. Verify X-Forwarded-Secret (shared secret with CPGateway)
// 2. Build MemberShip from X-* headers
func Authorize() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. Verify shared secret from Control Plane Gateway (mandatory)
		expectedSecret := viper.GetString("cpgateway.secret")
		if expectedSecret == "" {
			ErrorResponse(c, http.StatusInternalServerError, "cpgateway.secret not configured", nil)
			c.Abort()
			return
		}
		secret := c.Request.Header.Get("X-Forwarded-Secret")
		if secret != expectedSecret {
			ErrorResponse(c, http.StatusForbidden, "Invalid forwarded secret", nil)
			c.Abort()
			return
		}

		// 2. Read identity from X-* headers (set by CPGateway Proxy)
		userIDStr := c.Request.Header.Get("X-User-ID")
		if userIDStr == "" {
			ErrorResponse(c, http.StatusUnauthorized, "Missing X-User-ID header", nil)
			c.Abort()
			return
		}

		uid, err := strconv.ParseInt(userIDStr, 10, 64)
		if err != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid X-User-ID value", err)
			c.Abort()
			return
		}

		orgIDStr := c.Request.Header.Get("X-Org-ID")
		var oid int64
		if orgIDStr != "" {
			oid, err = strconv.ParseInt(orgIDStr, 10, 64)
			if err != nil {
				ErrorResponse(c, http.StatusBadRequest, "Invalid X-Org-ID value", err)
				c.Abort()
				return
			}
		}

		orgRoleStr := c.Request.Header.Get("X-Org-Role")
		orgRole := model.OrgNone
		if orgRoleStr != "" {
			r, err := strconv.Atoi(orgRoleStr)
			if err == nil {
				orgRole = model.OrgRole(r)
			}
		}

		systemRoleStr := c.Request.Header.Get("X-System-Role")
		systemRole := model.SystemUser
		if systemRoleStr != "" {
			r, err := strconv.Atoi(systemRoleStr)
			if err == nil {
				systemRole = model.SystemRole(r)
			}
		}

		isOwner := c.Request.Header.Get("X-Is-Owner") == "true"
		userEmail := c.Request.Header.Get("X-User-Email")
		orgName := c.Request.Header.Get("X-Org-Name")

		// 3. Build MemberShip from headers (no DB query)
		memberShip := &MemberShip{
			UserID:     uid,
			UserEmail:  userEmail,
			SystemRole: systemRole,
			OrgID:      oid,
			OrgName:    orgName,
			OrgRole:    orgRole,
			IsOrgOwner: isOwner,
		}

		logger.Infof("MemberShip from headers: %v\n", memberShip)
		ctx := memberShip.SetContext(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
