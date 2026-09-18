/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"net/http"

	. "api/src/common"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var orgAdmin = &services.OrgAdmin{}

// SyncOrg handles POST /internal/orgs/sync
// Called by CPGateway to keep the local organizations table in sync.
func SyncOrg(c *gin.Context) {
	// 以 UUID 为准同步：两侧组织表的自增主键各自独立，不能拿控制面的 ID 当本地主键
	var req struct {
		UUID string `json:"uuid" binding:"required"`
		Name string `json:"name" binding:"required"`
		Slug string `json:"slug"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	ctx = SetContextDB(ctx, DB())
	if err := orgAdmin.UpsertOrgByUUID(ctx, req.UUID, req.Name, req.Slug); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
