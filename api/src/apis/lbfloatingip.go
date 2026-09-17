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
)

var lbFloatingIpAPI = &LBFloatingIpAPI{}

type LBFloatingIpAPI struct{}

// @Summary get a floating ip for load balancer
// @Description get a floating ip for load balancer
// @tags Load Balancer
// @Accept  json
// @Produce json
// @Success 200 {object} FloatingIpResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /load_balancers/{id}/floating_ips/{floating_ip_id} [get]
func (v *LBFloatingIpAPI) Get(c *gin.Context) {
	ctx := c.Request.Context()
	lbID := c.Param("id")
	logger.Ctx(ctx).Debugf("Get load balancer %s", lbID)
	loadBalancer, err := loadBalancerAdmin.GetLoadBalancerByUUID(ctx, lbID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get load balancer %s, %+v", lbID, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid load balancer query", err)
		return
	}
	fipID := c.Param("floating_ip_id")
	logger.Ctx(ctx).Debugf("Get floating ip %s", fipID)
	floatingIp, err := floatingIpAdmin.GetFloatingIpByUUID(ctx, fipID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get floating ip %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query", err)
		return
	}
	if floatingIp.LoadBalancerID != loadBalancer.ID {
		logger.Ctx(ctx).Error("Invalid query for load balancer floating ip")
		ErrorResponse(c, http.StatusBadRequest, "Invalid query", NewCLError(ErrInvalidParameter, "Invalid query for load balancer floating ip", nil))
		return
	}
	floatingIpResp, err := floatingIpAPI.getFloatingIpResponse(ctx, floatingIp)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, floatingIpResp)
}

// @Summary delete a floating ip for load balancer
// @Description delete a floating ip for load balancer
// @tags Load Balancer
// @Accept  json
// @Produce json
// @Success 200
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /load_balancers/{id}/floating_ips/{floating_ip_id} [delete]
func (v *LBFloatingIpAPI) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	lbID := c.Param("id")
	logger.Ctx(ctx).Debugf("Get load balancer %s", lbID)
	loadBalancer, err := loadBalancerAdmin.GetLoadBalancerByUUID(ctx, lbID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get load balancer %s, %+v", lbID, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid load balancer query", err)
		return
	}
	fipID := c.Param("floating_ip_id")
	logger.Ctx(ctx).Debugf("Delete floating ip %s", fipID)
	floatingIp, err := floatingIpAdmin.GetFloatingIpByUUID(ctx, fipID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get floating ip %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query", err)
		return
	}
	if floatingIp.LoadBalancerID != loadBalancer.ID {
		logger.Ctx(ctx).Error("Invalid delete for load balancer floating ip")
		ErrorResponse(c, http.StatusBadRequest, "Invalid delete", NewCLError(ErrInvalidParameter, "Invalid delete for load balancer floating ip", nil))
		return
	}
	err = floatingIpAdmin.Delete(ctx, floatingIp)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to delete floating ip %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Not able to delete", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// @Summary create a floating ip for load balancer
// @Description create a floating ip for load balancer
// @tags Load Balancer
// @Accept  json
// @Produce json
// @Param   message	body   FloatingIpPayload  true   "Floating ip create payload"
// @Success 200 {object} FloatingIpResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /load_balancers/{id}/floating_ips [post]
func (v *LBFloatingIpAPI) Create(c *gin.Context) {
	logger.Ctx(c).Debugf("Creating floating ip")
	ctx := c.Request.Context()
	lbID := c.Param("id")
	loadBalancer, err := loadBalancerAdmin.GetLoadBalancerByUUID(ctx, lbID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get load balancer %s, %+v", lbID, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid load balancer query", err)
		return
	}
	payload := &FloatingIpPayload{}
	err = c.ShouldBindJSON(payload)
	if err != nil {
		logger.Ctx(ctx).Errorf("Invalid input JSON %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	logger.Ctx(ctx).Debugf("Creating floating ip with %+v", payload)
	activationCount := payload.ActivationCount
	if activationCount == 0 {
		activationCount = 1
	}

	var publicSubnets []*model.Subnet
	if payload.PublicSubnets != nil {
		for _, subnetRef := range payload.PublicSubnets {
			subnet, err := subnetAdmin.GetSubnet(ctx, subnetRef)
			if err != nil {
				logger.Ctx(ctx).Errorf("Failed to get public subnet %+v", err)
				ErrorResponse(c, http.StatusBadRequest, "Failed to get public subnet", err)
				return
			}
			publicSubnets = append(publicSubnets, subnet)
		}
	} else {
		if payload.PublicSubnet != nil {
			subnet, err := subnetAdmin.GetSubnet(ctx, payload.PublicSubnet)
			if err != nil {
				logger.Ctx(ctx).Errorf("Failed to get public subnet %+v", err)
				ErrorResponse(c, http.StatusBadRequest, "Failed to get public subnet", err)
				return
			}
			publicSubnets = append(publicSubnets, subnet)
		} else {
			publicSubnets = make([]*model.Subnet, 0)
		}
	}

	var group *model.IpGroup
	if payload.Group != nil {
		group, err = ipGroupAdmin.GetIpGroupByUUID(ctx, payload.Group.ID)
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to get ip group %+v", err)
			ErrorResponse(c, http.StatusBadRequest, "Failed to get ip group", err)
			return
		}
	}

	logger.Ctx(ctx).Debugf("publicSubnets: %v, publicIp: %s, name: %s, inbound: %d, outbound: %d, activationCount: %d, group: %v", publicSubnets, payload.PublicIp, payload.Name, payload.Inbound, payload.Outbound, activationCount, group)
	floatingIps, err := floatingIpAdmin.Create(ctx, nil, publicSubnets, payload.PublicIp, payload.Name, payload.Inbound, payload.Outbound, activationCount, nil, group, loadBalancer)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to create floating ip %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Failed to create floating ip", err)
		return
	}
	floatingIpResp := make([]*FloatingIpResponse, 0, len(floatingIps))
	for _, fip := range floatingIps {
		resp, err := floatingIpAPI.getFloatingIpResponse(ctx, fip)
		if err != nil {
			ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
			return
		}
		floatingIpResp = append(floatingIpResp, resp)
	}
	logger.Ctx(ctx).Debugf("Created floating ips %+v", floatingIpResp)
	c.JSON(http.StatusOK, floatingIpResp)
}

// @Summary list floating ips for load balancer
// @Description list floating ips for load balancer
// @tags Load Balancer
// @Accept  json
// @Produce json
// @Success 200 {object} FloatingIpListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /load_balancers/{id}/floating_ips [get]
func (v *LBFloatingIpAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	lbID := c.Param("id")
	loadBalancer, err := loadBalancerAdmin.GetLoadBalancerByUUID(ctx, lbID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get load balancer %s, %+v", lbID, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid load balancer query", err)
		return
	}
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "50")
	queryStr := c.DefaultQuery("query", "")
	logger.Ctx(ctx).Debugf("List floating ips with offset %s, limit %s, query %s", offsetStr, limitStr, queryStr)
	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		logger.Ctx(ctx).Errorf("Invalid query offset %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset: "+offsetStr, err)
		return
	}
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		logger.Ctx(ctx).Errorf("Invalid query limit %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query limit: "+limitStr, err)
		return
	}
	if offset < 0 || limit < 0 {
		logger.Ctx(ctx).Errorf("Invalid query offset or limit %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset or limit", err)
		return
	}
	total, floatingIps, err := floatingIpAdmin.List(ctx, int64(offset), int64(limit), "-created_at", queryStr, "load_balancer_id = ?", loadBalancer.ID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to list floatingIps %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Failed to list floatingIps", err)
		return
	}
	floatingIpListResp := &FloatingIpListResponse{
		Total:  int(total),
		Offset: offset,
		Limit:  len(floatingIps),
	}
	floatingIpListResp.FloatingIps = make([]*FloatingIpResponse, floatingIpListResp.Limit)
	for i, floatingIp := range floatingIps {
		floatingIpListResp.FloatingIps[i], err = floatingIpAPI.getFloatingIpResponse(ctx, floatingIp)
		if err != nil {
			ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
			return
		}
	}
	c.JSON(http.StatusOK, floatingIpListResp)
}
