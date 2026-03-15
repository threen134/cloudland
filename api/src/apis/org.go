/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var orgAPI = &OrgAPI{}
var orgAdmin = &services.OrgAdmin{}

type OrgAPI struct{}

type MemberInfo struct {
	*ResourceReference
	Role string `json:"role"`
}

type OrgResponse struct {
	*ResourceReference
	Members []*MemberInfo `json:"members"`
}

type OrgListResponse struct {
	Offset int            `json:"offset"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Orgs   []*OrgResponse `json:"orgs"`
}

type OrgPayload struct {
}

type OrgPatchPayload struct {
}

// Get retrieves org details (read-only, kept for migration).
func (v *OrgAPI) Get(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	logger.Debugf("Get org by uuid: %s", uuID)
	org, err := orgAdmin.GetOrgByUUID(ctx, uuID)
	if err != nil {
		logger.Errorf("Failed to get org by uuid: %s", uuID)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query", err)
		return
	}
	orgResp, err := v.getOrgResponse(ctx, org)
	if err != nil {
		logger.Errorf("Failed to get org response: %s", uuID)
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, orgResp)
}

func (v *OrgAPI) getOrgResponse(ctx context.Context, org *model.Organization) (orgResp *OrgResponse, err error) {
	orgResp = &OrgResponse{
		ResourceReference: &ResourceReference{
			ID:   org.UUID,
			Name: org.Name,
		},
	}
	for _, member := range org.Members {
		memberName := strconv.FormatInt(member.UserID, 10)
		if member.User != nil && member.User.Email != "" {
			memberName = member.User.Email
		}
		orgResp.Members = append(orgResp.Members, &MemberInfo{
			ResourceReference: &ResourceReference{ID: member.UUID, Name: memberName},
			Role:              member.OrgRole.String(),
		})
	}
	return
}

// --- Write operations below are DEPRECATED. Org management is handled by Middle. ---

func deprecated501(c *gin.Context, endpoint string) {
	logger.Errorf("OrgAPI.%s called — this endpoint is deprecated. Org management is handled by Middle.", endpoint)
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":   "This endpoint is deprecated",
		"message": "Org management is now handled by Middle. Use /api/v1/orgs on the Middle API gateway.",
	})
}

// Patch is deprecated. Org management is now handled by Middle.
func (v *OrgAPI) Patch(c *gin.Context) { deprecated501(c, "Patch") }

// Delete is deprecated. Org management is now handled by Middle.
func (v *OrgAPI) Delete(c *gin.Context) { deprecated501(c, "Delete") }

// AddMember is deprecated. Org management is now handled by Middle.
func (v *OrgAPI) AddMember(c *gin.Context) { deprecated501(c, "AddMember") }

// RemoveMember is deprecated. Org management is now handled by Middle.
func (v *OrgAPI) RemoveMember(c *gin.Context) { deprecated501(c, "RemoveMember") }

// UpdateMemberRole is deprecated. Org management is now handled by Middle.
func (v *OrgAPI) UpdateMemberRole(c *gin.Context) { deprecated501(c, "UpdateMemberRole") }

// Create is deprecated. Org management is now handled by Middle.
func (v *OrgAPI) Create(c *gin.Context) { deprecated501(c, "Create") }

// TransferOwner is deprecated. Org management is now handled by Middle.
func (v *OrgAPI) TransferOwner(c *gin.Context) { deprecated501(c, "TransferOwner") }

// List retrieves org list (read-only, kept for migration).
func (v *OrgAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "50")
	queryStr := c.DefaultQuery("query", "")
	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset: "+offsetStr, err)
		return
	}
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query limit: "+limitStr, err)
		return
	}
	if offset < 0 || limit < 0 {
		errStr := "Invalid query offset or limit, cannot be negative"
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset or limit", errors.New(errStr))
		return
	}
	total, orgs, err := orgAdmin.List(ctx, int64(offset), int64(limit), "-created_at", queryStr)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list orgs", err)
		return
	}
	orgListResp := &OrgListResponse{
		Total:  int(total),
		Offset: offset,
		Limit:  len(orgs),
	}
	orgListResp.Orgs = make([]*OrgResponse, orgListResp.Limit)
	for i, org := range orgs {
		orgListResp.Orgs[i], err = v.getOrgResponse(ctx, org)
		if err != nil {
			ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
			return
		}
	}
	c.JSON(http.StatusOK, orgListResp)
}
