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

var hyperAPI = &HyperAPI{}
var hyperAdmin = &routes.HyperAdmin{}

type HyperAPI struct{}

type HyperResponse struct {
	ID            int64   `json:"id"`
	UUID          string  `json:"uuid"`
	Hostid        int32   `json:"hostid"`
	Hostname      string  `json:"hostname"`
	Status        int32   `json:"status"`
	StatusName    string  `json:"status_name"`
	Parentid      int32   `json:"parentid"`
	Children      int32   `json:"children"`
	HostIP        string  `json:"host_ip"`
	RouteIP       string  `json:"route_ip"`
	VirtType      string  `json:"virt_type"`
	CpuOverRate   float32 `json:"cpu_over_rate"`
	MemOverRate   float32 `json:"mem_over_rate"`
	DiskOverRate  float32 `json:"disk_over_rate"`
	ZoneID        int64   `json:"zone_id"`
	ZoneName      string  `json:"zone_name"`
	Remark        string  `json:"remark"`
	Cpu           int64   `json:"cpu"`
	CpuTotal      int64   `json:"cpu_total"`
	Memory        int64   `json:"memory"`
	MemoryTotal   int64   `json:"memory_total"`
	Disk          int64   `json:"disk"`
	DiskTotal     int64   `json:"disk_total"`
	DeployCommand string  `json:"deploy_command,omitempty"`
}

type HyperListResponse struct {
	Offset int              `json:"offset"`
	Total  int              `json:"total"`
	Limit  int              `json:"limit"`
	Hypers []*HyperResponse `json:"hypers"`
}

type HyperPayload struct {
}

type HyperDeployPayload struct {
	IP            string `json:"ip" binding:"required"`
	Hostname      string `json:"hostname" binding:"required"`
	NetworkDevice string `json:"network_device"`
	VlanDevice    string `json:"vlan_device"`
	DNSServer     string `json:"dns_server"`
	Domain        string `json:"domain"`
	ZoneName      string `json:"zone_name"`
	VirtType      string `json:"virt_type"`
}

type HyperMaintainPayload struct {
	TargetHyper int32 `json:"target_hyper"`
	Migrate     bool  `json:"migrate"`
}

type HyperPatchPayload struct {
	Status       *int32   `json:"status" binding:"omitempty,min=0,max=1"`
	ZoneID       *int64   `json:"zone_id" binding:"omitempty,min=1"`
	CpuOverRate  *float32 `json:"cpu_over_rate" binding:"omitempty,min=1"`
	MemOverRate  *float32 `json:"mem_over_rate" binding:"omitempty,min=1"`
	DiskOverRate *float32 `json:"disk_over_rate" binding:"omitempty,min=1"`
	Remark       *string  `json:"remark"`
}

// @Summary get a hypervisor
// @Description get a hypervisor
// @tags Administration
// @Accept  json
// @Produce json
// @Router /hypers/{uuid} [get]
func (v *HyperAPI) Get(c *gin.Context) {
	uuid := c.Param("uuid")
	logger.Infof("API: Get hypervisor with uuid=%s", uuid)

	hyper, err := hyperAdmin.GetHyperByUUID(c.Request.Context(), uuid)
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Hypervisor not found", err)
		return
	}

	hyperResp := convertHyperToResponse(hyper)
	c.JSON(http.StatusOK, hyperResp)
}

// @Summary list hypervisors
// @Description list hypervisors
// @tags Administration
// @Accept  json
// @Produce json
// @Param offset query int false "Offset for pagination"
// @Param limit query int false "Limit for pagination"
// @Param order query string false "Order by field"
// @Param q query string false "Search query"
// @Success 200 {object} HyperListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /hypers [get]
func (v *HyperAPI) List(c *gin.Context) {
	offset := c.Query("offset")
	limit := c.Query("limit")
	order := c.Query("order")
	query := c.Query("q")
	logger.Infof("Listing hypervisors via API: offset=%s, limit=%s, order=%s, q=%s", offset, limit, order, query)

	var offsetInt, limitInt int64
	var err error

	if offset != "" {
		offsetInt, err = strconv.ParseInt(offset, 10, 64)
		if err != nil {
			offsetInt = 0
		}
	}

	if limit != "" {
		limitInt, err = strconv.ParseInt(limit, 10, 64)
		if err != nil {
			limitInt = 16
		}
	} else {
		limitInt = 16
	}

	total, hypers, err := hyperAdmin.List(c.Request.Context(), offsetInt, limitInt, order, query)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}

	hyperResponses := make([]*HyperResponse, len(hypers))
	for i, hyper := range hypers {
		hyperResponses[i] = convertHyperToResponse(hyper)
	}

	hyperListResp := &HyperListResponse{
		Offset: int(offsetInt),
		Total:  int(total),
		Limit:  int(limitInt),
		Hypers: hyperResponses,
	}
	c.JSON(http.StatusOK, hyperListResp)
}

// @Summary update a hypervisor
// @Description update hypervisor status, zone, over-commit rates, and remark
// @tags Administration
// @Accept  json
// @Produce json
// @Router /hypers/{uuid} [patch]
func (v *HyperAPI) Patch(c *gin.Context) {
	uuid := c.Param("uuid")

	var payload HyperPatchPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		logger.Errorf("Failed to bind JSON for Hyper PATCH: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid payload", err)
		return
	}
	logger.Infof("Patching hypervisor %s with payload: %+v", uuid, payload)

	// Get existing hypervisor
	hyper, err := hyperAdmin.GetHyperByUUID(c.Request.Context(), uuid)
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Hypervisor not found", err)
		return
	}

	// Update only the fields provided in the payload
	if payload.Status != nil {
		hyper.Status = *payload.Status
	}
	if payload.ZoneID != nil {
		hyper.ZoneID = *payload.ZoneID
	}
	if payload.CpuOverRate != nil {
		hyper.CpuOverRate = *payload.CpuOverRate
	}
	if payload.MemOverRate != nil {
		hyper.MemOverRate = *payload.MemOverRate
	}
	if payload.DiskOverRate != nil {
		hyper.DiskOverRate = *payload.DiskOverRate
	}
	if payload.Remark != nil {
		hyper.Remark = *payload.Remark
	}

	// Update the hypervisor
	if err := hyperAdmin.Update(c.Request.Context(), hyper); err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}

	// Get updated hypervisor with Zone preloaded
	updatedHyper, err := hyperAdmin.GetHyperByUUID(c.Request.Context(), uuid)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}

	hyperResp := convertHyperToResponse(updatedHyper)
	c.JSON(http.StatusOK, hyperResp)
}

// @Summary deploy a new hypervisor
// @Description deploy a new compute node via SSH
// @tags Administration
// @Accept  json
// @Produce json
// @Param body body HyperDeployPayload true "Deploy payload"
// @Success 200 {object} HyperResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Failure 500 {object} common.APIError "Internal server error"
// @Router /hypers [post]
func (v *HyperAPI) Deploy(c *gin.Context) {
	var payload HyperDeployPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		logger.Errorf("Failed to bind JSON for Hyper Deploy: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid payload", err)
		return
	}
	logger.Infof("API: Deploy hypervisor with payload: %+v", payload)
	if payload.NetworkDevice == "" {
		payload.NetworkDevice = "eth0"
	}
	if payload.VlanDevice == "" {
		payload.VlanDevice = payload.NetworkDevice
	}
	if payload.DNSServer == "" {
		payload.DNSServer = "8.8.8.8"
	}
	if payload.Domain == "" {
		payload.Domain = "example.com"
	}
	if payload.ZoneName == "" {
		// payload.ZoneName = "zone0"
	}
	if payload.VirtType == "" {
		payload.VirtType = "kvm-x86_64"
	}

	hyper, deployCmd, err := hyperAdmin.Deploy(c.Request.Context(), payload.IP,
		payload.Hostname, payload.NetworkDevice, payload.VlanDevice, payload.DNSServer,
		payload.Domain, payload.ZoneName, payload.VirtType)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to deploy hypervisor", err)
		return
	}
	resp := convertHyperToResponse(hyper)
	resp.DeployCommand = deployCmd
	c.JSON(http.StatusOK, resp)
}

// @Summary maintain a hypervisor
// @Description start maintenance for a hypervisor, optionally migrating all instances
// @tags Administration
// @Accept json
// @Produce json
// @Param uuid path string true "Hypervisor UUID"
// @Param body body HyperMaintainPayload false "Maintenance options"
// @Success 200 {object} map[string]string
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Failure 500 {object} common.APIError "Internal server error"
// @Router /hypers/{uuid}/maintain [post]
func (v *HyperAPI) Maintain(c *gin.Context) {
	uuid := c.Param("uuid")
	var payload HyperMaintainPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		logger.Warningf("Failed to bind JSON for Hyper Maintain, using default (migrate=true): %+v", err)
		payload.TargetHyper = -1
		payload.Migrate = true
	}
	logger.Infof("Maintenance requested via API for hypervisor %s: migrate=%v, target=%d", uuid, payload.Migrate, payload.TargetHyper)
	hyper, err := hyperAdmin.GetHyperByUUID(c.Request.Context(), uuid)
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Hypervisor not found", err)
		return
	}
	if err := hyperAdmin.Maintain(c.Request.Context(), hyper.Hostid, payload.Migrate, payload.TargetHyper); err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to maintain hypervisor", err)
		return
	}
	c.JSON(http.StatusOK, map[string]string{"result": "success"})
}

// @Summary delete a hypervisor
// @Description remove a hypervisor record from database
// @tags Administration
// @Accept json
// @Produce json
// @Param uuid path string true "Hypervisor UUID"
// @Success 204 "No content"
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Failure 404 {object} common.APIError "Not found"
// @Failure 500 {object} common.APIError "Internal server error"
// @Router /hypers/{uuid} [delete]
func (v *HyperAPI) Delete(c *gin.Context) {
	uuid := c.Param("uuid")
	logger.Infof("Deletion requested via API for hypervisor: uuid=%s", uuid)
	hyper, err := hyperAdmin.GetHyperByUUID(c.Request.Context(), uuid)
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Hypervisor not found", err)
		return
	}
	if err := hyperAdmin.Delete(c.Request.Context(), hyper.Hostid); err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to delete hypervisor", err)
		return
	}
	c.Status(http.StatusNoContent)
}

// convertHyperToResponse converts a model.Hyper to HyperResponse
func convertHyperToResponse(hyper *model.Hyper) *HyperResponse {
	resp := &HyperResponse{
		ID:           hyper.ID,
		UUID:         hyper.UUID,
		Hostid:       hyper.Hostid,
		Hostname:     hyper.Hostname,
		Status:       hyper.Status,
		StatusName:   hyper.GetStatus(),
		Parentid:     hyper.Parentid,
		Children:     hyper.Children,
		HostIP:       hyper.HostIP,
		RouteIP:      hyper.RouteIP,
		VirtType:     hyper.VirtType,
		CpuOverRate:  hyper.CpuOverRate,
		MemOverRate:  hyper.MemOverRate,
		DiskOverRate: hyper.DiskOverRate,
		ZoneID:       hyper.ZoneID,
		Remark:       hyper.Remark,
	}

	if hyper.Zone != nil {
		resp.ZoneName = hyper.Zone.Name
	}

	if hyper.Resource != nil {
		resp.Cpu = hyper.Resource.Cpu
		resp.CpuTotal = hyper.Resource.CpuTotal
		resp.Memory = hyper.Resource.Memory / 1024                       // Convert KB to MB
		resp.MemoryTotal = hyper.Resource.MemoryTotal / 1024             // Convert KB to MB
		resp.Disk = hyper.Resource.Disk / (1024 * 1024 * 1024)           // Convert B to GB
		resp.DiskTotal = hyper.Resource.DiskTotal / (1024 * 1024 * 1024) // Convert B to GB
	}

	return resp
}
