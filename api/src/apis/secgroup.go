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
	"strings"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var secgroupAPI = &SecgroupAPI{}
var secgroupAdmin = &services.SecgroupAdmin{}

type SecgroupAPI struct{}

type SecurityGroupResponse struct {
	*ResourceReference
	IsDefault        bool               `json:"is_default"`
	Description      string             `json:"description,omitempty"`
	VPC              *ResourceReference `json:"vpc,omitempty"`
	TargetInterfaces []*TargetInterface `json:"target_interfaces,omitempty"`
	SecurityRules    []*SecruleResponse `json:"security_rules,omitempty"`
}

type SecurityGroupListResponse struct {
	Offset         int                      `json:"offset"`
	Total          int                      `json:"total"`
	Limit          int                      `json:"limit"`
	SecurityGroups []*SecurityGroupResponse `json:"security_groups"`
}

type SecurityGroupPayload struct {
	Name        string         `json:"name" binding:"required,min=2,max=32"`
	Description string         `json:"description" binding:"omitempty,max=256"`
	VPC         *BaseReference `json:"vpc" binding:"omitempty"`
	IsDefault   bool           `json:"is_default" binding:"omitempty"`
}

// PATCH 是部分更新：三个字段都用指针，才分得清「没传」和「传了空值 / false」。
// 原先 Name 是 required，详情页只改描述时不带 name，必定 400；IsDefault 是 bool，
// 不传等于 false，于是对任何默认安全组（系统默认组、组织默认组、VPC native 组）
// 做任何修改都会被判成「想取消默认」而 400。
type SecurityGroupPatchPayload struct {
	Name        *string `json:"name" binding:"omitempty,min=2,max=32"`
	Description *string `json:"description" binding:"omitempty,max=256"`
	IsDefault   *bool   `json:"is_default"`
}

// @Summary get a secgroup
// @Description get a secgroup
// @tags Security Group
// @Accept  json
// @Produce json
// @Success 200 {object} SecurityGroupResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /security_groups/{id} [get]
func (v *SecgroupAPI) Get(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	logger.Ctx(ctx).Debugf("Get secgroup %s", uuID)
	secgroup, err := secgroupAdmin.GetSecgroupByUUID(ctx, uuID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get secgroup %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid security group query", err)
		return
	}
	secgroupResp, err := v.getSecgroupResponse(ctx, secgroup)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	logger.Ctx(ctx).Debugf("Get secgroup successfully, %s, %+v", uuID, secgroupResp)
	c.JSON(http.StatusOK, secgroupResp)
}

// @Summary patch a secgroup
// @Description patch a secgroup
// @tags Security Group
// @Accept  json
// @Produce json
// @Param   message	body   SecurityGroupPatchPayload  true   "Secgroup patch payload"
// @Success 200 {object} SecurityGroupResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /security_groups/{id} [patch]
func (v *SecgroupAPI) Patch(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	logger.Ctx(ctx).Debugf("Patch secgroup %s", uuID)
	secgroup, err := secgroupAdmin.GetSecgroupByUUID(ctx, uuID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get secgroup %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid security group query", err)
		return
	}
	payload := &SecurityGroupPatchPayload{}
	err = c.ShouldBindJSON(payload)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to bind json, %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	logger.Ctx(ctx).Debugf("Patching secgroup %s with %+v", uuID, payload)
	// 只有显式传 is_default=false 才是「想取消默认」，不传就是没打算动它
	if payload.IsDefault != nil && !*payload.IsDefault && secgroup.IsDefault {
		logger.Ctx(ctx).Errorf("Not allowed to patch default security group to false")
		ErrorResponse(c, http.StatusBadRequest, "Not allowed to patch default security group to false", err)
		return
	}
	err = secgroupAdmin.Update(ctx, secgroup, payload.Name, payload.Description, payload.IsDefault)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to patch secgroup %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Patch security group failed", err)
		return
	}
	secgroupResp, err := v.getSecgroupResponse(ctx, secgroup)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	logger.Ctx(ctx).Debugf("Patch secgroup successfully, %s, %+v", uuID, secgroupResp)
	c.JSON(http.StatusOK, secgroupResp)
}

// @Summary delete a secgroup
// @Description delete a secgroup
// @tags Security Group
// @Accept  json
// @Produce json
// @Success 204
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /security_groups/{id} [delete]
func (v *SecgroupAPI) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	logger.Ctx(ctx).Debugf("Delete secgroup %s", uuID)
	secgroup, err := secgroupAdmin.GetSecgroupByUUID(ctx, uuID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get secgroup %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query", err)
		return
	}
	err = secgroupAdmin.Delete(ctx, secgroup)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to delete secgroup %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Not able to delete", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// @Summary create a secgroup
// @Description create a secgroup
// @tags Security Group
// @Accept  json
// @Produce json
// @Param   message	body   SecurityGroupPayload  true   "Secgroup create payload"
// @Success 200 {object} SecurityGroupResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /security_groups [post]
func (v *SecgroupAPI) Create(c *gin.Context) {
	logger.Ctx(c).Debugf("Create secgroup")
	ctx := c.Request.Context()
	payload := &SecurityGroupPayload{}
	err := c.ShouldBindJSON(payload)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to bind json, %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	logger.Ctx(ctx).Debugf("Creating secgroup with %+v", payload)
	var router *model.Router
	if payload.VPC != nil {
		router, err = routerAdmin.GetRouter(ctx, payload.VPC)
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to get vpc %+v, %+v", payload.VPC, err)
			ErrorResponse(c, http.StatusBadRequest, "Failed to get vpc", err)
			return
		}
	}
	// 用户创建的安全组不预置对全网开放的 SSH/RDP（withLoginRules=false），即使勾选了默认
	secgroup, err := secgroupAdmin.Create(ctx, payload.Name, payload.Description, payload.IsDefault, false, router)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to create secgroup %+v, %+v", payload, err)
		ErrorResponse(c, http.StatusBadRequest, "Not able to create", err)
		return
	}
	secgroupResp, err := v.getSecgroupResponse(ctx, secgroup)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	logger.Ctx(ctx).Debugf("Create secgroup successfully, %+v", secgroupResp)
	c.JSON(http.StatusOK, secgroupResp)
}

func (v *SecgroupAPI) getSecgroupResponse(ctx context.Context, secgroup *model.SecurityGroup) (secgroupResp *SecurityGroupResponse, err error) {
	owner := orgAdmin.GetOrgName(ctx, secgroup.Owner)
	secgroupResp = &SecurityGroupResponse{
		ResourceReference: &ResourceReference{
			ID:        secgroup.UUID,
			Name:      secgroup.Name,
			Owner:     owner,
			CreatedAt: secgroup.CreatedAt.Format(TimeStringForMat),
			UpdatedAt: secgroup.UpdatedAt.Format(TimeStringForMat),
		},
		IsDefault:   secgroup.IsDefault,
		Description: secgroup.Description,
	}
	if secgroup.Router != nil {
		secgroupResp.VPC = &ResourceReference{
			ID:   secgroup.Router.UUID,
			Name: secgroup.Router.Name,
		}
	}
	_, secrules, rulesErr := secruleAdmin.List(ctx, 0, -1, "-created_at", secgroup)
	if rulesErr != nil {
		logger.Ctx(ctx).Errorf("Failed to load rules for secgroup %s: %v", secgroup.UUID, rulesErr)
	} else {
		for _, rule := range secrules {
			ruleResp, _ := secruleAPI.getSecruleResponse(ctx, rule)
			if ruleResp != nil {
				secgroupResp.SecurityRules = append(secgroupResp.SecurityRules, ruleResp)
			}
		}
	}
	err = secgroupAdmin.GetSecgroupInterfaces(ctx, secgroup)
	if err != nil {
		return
	}
	for _, iface := range secgroup.Interfaces {
		targetIface := &TargetInterface{
			ResourceReference: &ResourceReference{
				ID:   iface.UUID,
				Name: iface.Name,
			},
		}
		if iface.Address != nil {
			targetIface.IpAddress = strings.Split(iface.Address.Address, "/")[0]
		}
		if iface.Instance > 0 {
			var instance *model.Instance
			instance, err = instanceAdmin.Get(ctx, iface.Instance)
			if err != nil {
				err = nil
				continue
			}
			owner := orgAdmin.GetOrgName(ctx, instance.Owner)
			targetIface.FromInstance = &InstanceInfo{
				ResourceReference: &ResourceReference{
					ID:    instance.UUID,
					Owner: owner,
				},
				Hostname: instance.Hostname,
			}
		}
		secgroupResp.TargetInterfaces = append(secgroupResp.TargetInterfaces, targetIface)
	}
	return
}

// @Summary list secgroups
// @Description list secgroups
// @tags Security Group
// @Accept  json
// @Produce json
// @Success 200 {object} SecurityGroupListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /security_groups [get]
func (v *SecgroupAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "50")
	queryStr := c.DefaultQuery("query", "")
	vpcID := strings.TrimSpace(c.DefaultQuery("vpc_id", ""))
	var routerID int64
	logger.Ctx(ctx).Debugf("List secgroups with offset %s, limit %s, query %s, vpc_id %s", offsetStr, limitStr, queryStr, vpcID)

	if vpcID != "" {
		logger.Ctx(ctx).Debugf("Filtering secgroups by VPC ID: %s", vpcID)
		var router *model.Router
		router, err := routerAdmin.GetRouterByUUID(ctx, vpcID)
		if err != nil {
			logger.Ctx(ctx).Errorf("Invalid query vpc_id: %s, %+v", vpcID, err)
			ErrorResponse(c, http.StatusBadRequest, "Invalid query router by vpc_id UUID: "+vpcID, err)
			return
		}

		logger.Ctx(ctx).Debugf("The router with vpc_id: %+v\n", router)
		logger.Ctx(ctx).Debugf("The router_id in vpc is: %d", router.ID)
		routerID = router.ID
	}

	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		logger.Ctx(ctx).Errorf("Invalid query offset: %s, %+v", offsetStr, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset: "+offsetStr, err)
		return
	}
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		logger.Ctx(ctx).Errorf("Invalid query limit: %s, %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query limit: "+limitStr, err)
		return
	}
	if offset < 0 || limit < 0 {
		errStr := "Invalid query offset or limit, cannot be negative"
		logger.Ctx(ctx).Errorf(errStr)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset or limit", errors.New(errStr))
		return
	}
	total, secgroups, err := secgroupAdmin.List(ctx, int64(offset), int64(limit), "-created_at", queryStr, routerID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to list secgroups, %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Failed to list secgroups", err)
		return
	}
	secgroupListResp := &SecurityGroupListResponse{
		Total:  int(total),
		Offset: offset,
		Limit:  len(secgroups),
	}
	secgroupListResp.SecurityGroups = make([]*SecurityGroupResponse, secgroupListResp.Limit)
	for i, secgroup := range secgroups {
		secgroupListResp.SecurityGroups[i], err = v.getSecgroupResponse(ctx, secgroup)
		if err != nil {
			ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
			return
		}
	}
	logger.Ctx(ctx).Debugf("List secgroups successfully, %+v", secgroupListResp)
	c.JSON(http.StatusOK, secgroupListResp)
}
