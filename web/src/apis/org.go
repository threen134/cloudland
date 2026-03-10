/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	. "web/src/common"
	"web/src/model"
	"web/src/routes"

	"github.com/gin-gonic/gin"
)

var orgAPI = &OrgAPI{}
var orgAdmin = &routes.OrgAdmin{}

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

// @Summary get a org
// @Description get a org
// @tags Authorization
// @Accept  json
// @Produce json
// @Success 200 {object} OrgResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /orgs/{id} [get]
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
	logger.Debugf("Got org : %+v", orgResp)
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
		orgResp.Members = append(orgResp.Members, &MemberInfo{
			ResourceReference: &ResourceReference{
				ID:   member.UUID,
				Name: strconv.FormatInt(member.UserID, 10),
			},
			Role: member.OrgRole.String(),
		})
	}
	return
}

// @Summary patch a org
// @Description patch a org
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   message	body   map[string]string  true   "Org patch payload"
// @Success 200 {object} OrgResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /orgs/{id} [patch]
func (v *OrgAPI) Patch(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	payload := struct {
		Name string `json:"name" binding:"required"`
	}{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	org, err := orgAdmin.GetOrgByUUID(ctx, uuID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid org id", err)
		return
	}
	err = orgAdmin.RenameOrg(ctx, org.ID, payload.Name)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to rename org", err)
		return
	}
	orgResp, _ := v.getOrgResponse(ctx, org)
	orgResp.Name = payload.Name
	c.JSON(http.StatusOK, orgResp)
}

// @Summary delete a org
// @Summary delete a org
// @Description delete a org
// @tags Authorization
// @Accept  json
// @Produce json
// @Success 204
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /orgs/{id} [delete]
func (v *OrgAPI) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	logger.Debugf("Deleting org %s", uuID)
	org, err := orgAdmin.GetOrgByUUID(ctx, uuID)
	if err != nil {
		logger.Errorf("Failed to get org by uuid: %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query", err)
		return
	}
	err = orgAdmin.Delete(ctx, org)
	if err != nil {
		logger.Errorf("Failed to delete org %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Not able to delete", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// @Summary add a member to org
// @Description add a member to org
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   id       path      string  true   "Org UUID"
// @Param   message	body      map[string]interface{}  true   "Member add payload"
// @Success 200 {object} MemberInfo
// @Failure 400 {object} common.APIError "Bad request"
// @Router /orgs/{id}/members [post]
func (v *OrgAPI) AddMember(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	payload := struct {
		UserID  int64         `json:"user_id" binding:"required"`
		OrgRole model.OrgRole `json:"role" binding:"required"`
	}{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	// Validate org_role is in range 1-3 (OrgReader to OrgAdmin)
	if payload.OrgRole < model.OrgReader || payload.OrgRole > model.OrgAdmin {
		ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Invalid org role: %d. Must be between %d (Reader) and %d (Admin)", payload.OrgRole, model.OrgReader, model.OrgAdmin), nil)
		return
	}
	org, err := orgAdmin.GetOrgByUUID(ctx, uuID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid org id", err)
		return
	}
	member, err := orgAdmin.AddMember(ctx, org.ID, payload.UserID, payload.OrgRole)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to add member", err)
		return
	}
	c.JSON(http.StatusOK, &MemberInfo{
		ResourceReference: &ResourceReference{ID: member.UUID, Name: strconv.FormatInt(member.UserID, 10)},
		Role:              member.OrgRole.String(),
	})
}

// @Summary remove a member from org
// @Description remove a member from org
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   id       path      string  true   "Org UUID"
// @Param   user_id  path      int64   true   "User ID"
// @Success 204
// @Failure 400 {object} common.APIError "Bad request"
// @Router /orgs/{id}/members/{user_id} [delete]
func (v *OrgAPI) RemoveMember(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	userIDStr := c.Param("user_id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid user id", err)
		return
	}
	org, err := orgAdmin.GetOrgByUUID(ctx, uuID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid org id", err)
		return
	}
	err = orgAdmin.RemoveMember(ctx, org.ID, userID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to remove member", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// @Summary update member role
// @Description update member role
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   id       path      string  true   "Org UUID"
// @Param   user_id  path      int64   true   "User ID"
// @Param   message	body      map[string]interface{}  true   "Role update payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} common.APIError "Bad request"
// @Router /orgs/{id}/members/{user_id} [patch]
func (v *OrgAPI) UpdateMemberRole(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	userIDStr := c.Param("user_id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid user id", err)
		return
	}
	payload := struct {
		OrgRole model.OrgRole `json:"role" binding:"required"`
	}{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	org, err := orgAdmin.GetOrgByUUID(ctx, uuID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid org id", err)
		return
	}
	err = orgAdmin.UpdateMemberRole(ctx, org.ID, userID, payload.OrgRole)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to update member role", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// @Summary create a org
// @Description create a org
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   message	body   map[string]interface{}  true   "Org create payload"
// @Success 200 {object} OrgResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /orgs [post]
func (v *OrgAPI) Create(c *gin.Context) {
	ctx := c.Request.Context()
	payload := struct {
		Name        string `json:"name" binding:"required"`
		OwnerUserID int64  `json:"owner_user_id" binding:"required"`
		Slug        string `json:"slug"`
	}{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	org, err := orgAdmin.Create(ctx, payload.Name, payload.OwnerUserID, payload.Slug)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to create organization", err)
		return
	}
	orgResp, _ := v.getOrgResponse(ctx, org)
	c.JSON(http.StatusOK, orgResp)
}

// @Summary transfer org ownership
// @Description transfer org ownership to another member. Only SystemAdmin can call this.
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   id      path   string  true  "Org UUID"
// @Param   message body   map[string]interface{}  true  "Transfer payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 403 {object} common.APIError "Forbidden"
// @Router /orgs/{id}/transfer-owner [post]
func (v *OrgAPI) TransferOwner(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	payload := struct {
		NewOwnerUserID int64 `json:"new_owner_user_id" binding:"required"`
	}{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	org, err := orgAdmin.GetOrgByUUID(ctx, uuID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid org id", err)
		return
	}
	if err = orgAdmin.TransferOwner(ctx, org.ID, payload.NewOwnerUserID); err != nil {
		ErrorResponse(c, http.StatusForbidden, "Failed to transfer ownership", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// @Summary list orgs
// @Description list orgs
// @tags Authorization
// @Accept  json
// @Produce json
// @Success 200 {object} OrgListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /orgs [get]
func (v *OrgAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "50")
	queryStr := c.DefaultQuery("query", "")
	logger.Debugf("List users, offset:%s, limit:%s, query:%s", offsetStr, limitStr, queryStr)
	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		logger.Errorf("Invalid query offset: %s, %+v", offsetStr, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset: "+offsetStr, err)
		return
	}
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		logger.Errorf("Invalid query limit: %s, %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query limit: "+limitStr, err)
		return
	}
	if offset < 0 || limit < 0 {
		errStr := "Invalid query offset or limit, cannot be negative"
		logger.Errorf(errStr)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset or limit", errors.New(errStr))
		return
	}
	total, orgs, err := orgAdmin.List(ctx, int64(offset), int64(limit), "-created_at", queryStr)
	if err != nil {
		logger.Errorf("Failed to list orgs, %+v", err)
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
	logger.Debugf("List orgs successfully, %+v", orgListResp)
	c.JSON(http.StatusOK, orgListResp)
}
