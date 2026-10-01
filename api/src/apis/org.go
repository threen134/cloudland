/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"net/http"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var orgAdmin = &services.OrgAdmin{}

// SyncOrg handles POST /internal/orgs/sync
// Called by CPGateway to keep the local organizations table in sync.
func SyncOrg(c *gin.Context) {
	// Synced by UUID: the auto-increment keys of the two org tables are independent, the control plane's ID
	// must never be used as the local primary key
	var req struct {
		UUID    string `json:"uuid" binding:"required"`
		Name    string `json:"name" binding:"required"`
		Slug    string `json:"slug"`
		OrgType int    `json:"org_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	ctx = SetContextDB(ctx, DB())
	id, err := orgAdmin.UpsertOrgByUUID(ctx, req.UUID, req.Name, req.Slug, model.OrgType(req.OrgType))
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to sync organization", err)
		return
	}
	if id == 0 {
		// The org was deleted in this region: a stale push, nothing was recreated
		c.JSON(http.StatusOK, gin.H{"status": "deleted"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// DeleteOrg handles POST /internal/orgs/delete
// Called by CPGateway after an org is deleted in the control plane: the UUID stops resolving in this region.
// An org that still owns resources here is kept and 409 is returned.
func DeleteOrg(c *gin.Context) {
	var req struct {
		UUID string `json:"uuid" binding:"required"`
		// Only kept on the tombstone left for an org this region never saw
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := SetContextDB(c.Request.Context(), DB())
	if err := orgAdmin.DeleteOrgByUUID(ctx, req.UUID, req.Name); err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to delete organization", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
