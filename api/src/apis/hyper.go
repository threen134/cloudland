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
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var hyperAPI = &HyperAPI{}
var hyperAdmin = &services.HyperAdmin{}

type HyperAPI struct{}

type HyperResponse struct {
	UUID string `json:"uuid"`
	// 节点编号：迁移接口的 target_hyper、实例的 hyper 字段用的都是它
	Hostid        int32   `json:"hostid"`
	Hostname      string  `json:"hostname"`
	Status        int32   `json:"status"`
	StatusName    string  `json:"status_name"`
	Children      int32   `json:"children"`
	HostIP        string  `json:"host_ip"`
	RouteIP       string  `json:"route_ip"`
	VirtType      string  `json:"virt_type"`
	CpuOverRate   float32 `json:"cpu_over_rate"`
	MemOverRate   float32 `json:"mem_over_rate"`
	DiskOverRate  float32 `json:"disk_over_rate"`
	ZoneName      string  `json:"zone_name"`
	Remark        string  `json:"remark"`
	InstanceCount int64   `json:"instance_count"`
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
	IP                string `json:"ip" binding:"required"`
	Hostname          string `json:"hostname" binding:"required"`
	NetworkDevice     string `json:"network_device"`
	VlanDevice        string `json:"vlan_device"`
	PrivateVlanDevice string `json:"private_vlan_device"`
	DNSServer         string `json:"dns_server"`
	Domain            string `json:"domain"`
	ZoneName          string `json:"zone_name"`
	VirtType          string `json:"virt_type"`
}

type HyperMaintainPayload struct {
	// 用指针而非值类型：值类型时客户端漏传 target_hyper 会得到零值 0，而 0 不是合法 hostid，
	// 会被当作"迁往 hostid 0"从而报 HypervisorNotFound。约定 nil / -1 表示由调度器自选
	TargetHyper *int32 `json:"target_hyper" binding:"omitempty,gte=-1,lte=65535"`
	Migrate     bool   `json:"migrate"`
}

type HyperPatchPayload struct {
	Status *int32 `json:"status" binding:"omitempty,min=0,max=1"`
	// 可用区的 UUID。接口对外暴露的 id 一律是 UUID，此前这里收的是数据库自增 ID，
	// 而 GET /zones 只返回 UUID，界面上改可用区必定 400
	ZoneID       *string  `json:"zone_id" binding:"omitempty,uuid"`
	CpuOverRate  *float32 `json:"cpu_over_rate" binding:"omitempty,min=1"`
	MemOverRate  *float32 `json:"mem_over_rate" binding:"omitempty,min=1"`
	DiskOverRate *float32 `json:"disk_over_rate" binding:"omitempty,min=1"`
	Remark       *string  `json:"remark"`
}

// @Summary get a hypervisor
// @Description get a hypervisor
// @tags Administration,Hypervisor
// @Accept  json
// @Produce json
// @Router /hypers/{uuid} [get]
func (v *HyperAPI) Get(c *gin.Context) {
	uuid := c.Param("uuid")
	logger.Ctx(c).Infof("API: Get hypervisor with uuid=%s", uuid)

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
// @tags Administration,Hypervisor
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
	logger.Ctx(c).Infof("Listing hypervisors via API: offset=%s, limit=%s, order=%s, q=%s", offset, limit, order, query)

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

	// 每个节点上的虚拟机数量：一次分组统计，避免按节点逐个查询
	instanceCounts, cErr := hyperAdmin.GetInstanceCounts(c.Request.Context())
	if cErr != nil {
		// 统计失败不影响节点列表本身，数量按 0 返回并记录
		logger.Ctx(c).Errorf("Failed to count instances per hypervisor: %+v", cErr)
		instanceCounts = map[int32]int64{}
	}
	hyperResponses := make([]*HyperResponse, len(hypers))
	for i, hyper := range hypers {
		hyperResponses[i] = convertHyperToResponse(hyper)
		hyperResponses[i].InstanceCount = instanceCounts[hyper.Hostid]
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
// @tags Administration,Hypervisor
// @Accept  json
// @Produce json
// @Router /hypers/{uuid} [patch]
func (v *HyperAPI) Patch(c *gin.Context) {
	uuid := c.Param("uuid")

	var payload HyperPatchPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		logger.Ctx(c).Errorf("Failed to bind JSON for Hyper PATCH: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid payload", err)
		return
	}
	logger.Ctx(c).Infof("Patching hypervisor %s with payload: %+v", uuid, payload)
	if payload.Status != nil {
		// 启用（含退出维护）与禁用是需要单独呈现的节点状态变化，其余字段归为修改配置
		if *payload.Status == 1 {
			SetAuditAction(c, "hyper.enable")
		} else {
			SetAuditAction(c, "hyper.disable")
		}
	}

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
		zone, zoneErr := zoneAdmin.GetZoneByUUID(c.Request.Context(), *payload.ZoneID)
		if zoneErr != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid zone", zoneErr)
			return
		}
		hyper.ZoneID = zone.ID
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
// @tags Administration,Hypervisor
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
		logger.Ctx(c).Errorf("Failed to bind JSON for Hyper Deploy: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid payload", err)
		return
	}
	logger.Ctx(c).Infof("API: Deploy hypervisor with payload: %+v", payload)
	if payload.NetworkDevice == "" {
		payload.NetworkDevice = "eth0"
	}
	if payload.VlanDevice == "" {
		payload.VlanDevice = payload.NetworkDevice
	}
	if payload.PrivateVlanDevice == "" {
		payload.PrivateVlanDevice = payload.VlanDevice
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
		payload.Hostname, payload.NetworkDevice, payload.VlanDevice, payload.PrivateVlanDevice,
		payload.DNSServer, payload.Domain, payload.ZoneName, payload.VirtType)
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
// @tags Administration,Hypervisor
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
		logger.Ctx(c).Warningf("Failed to bind JSON for Hyper Maintain, using default (migrate=true): %+v", err)
		payload.TargetHyper = nil
		payload.Migrate = true
	}
	targetHyper := int32(-1) // -1：由调度器自选目标节点
	if payload.TargetHyper != nil {
		targetHyper = *payload.TargetHyper
	}
	logger.Ctx(c).Infof("Maintenance requested via API for hypervisor %s: migrate=%v, target=%d", uuid, payload.Migrate, targetHyper)
	hyper, err := hyperAdmin.GetHyperByUUID(c.Request.Context(), uuid)
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Hypervisor not found", err)
		return
	}
	if err := hyperAdmin.Maintain(c.Request.Context(), hyper.Hostid, payload.Migrate, targetHyper); err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to maintain hypervisor", err)
		return
	}
	c.JSON(http.StatusOK, map[string]string{"result": "success"})
}

// @Summary delete a hypervisor
// @Description remove a hypervisor record from database
// @tags Administration,Hypervisor
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
	logger.Ctx(c).Infof("Deletion requested via API for hypervisor: uuid=%s", uuid)
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
		UUID:         hyper.UUID,
		Hostid:       hyper.Hostid,
		Hostname:     hyper.Hostname,
		Status:       hyper.Status,
		StatusName:   hyper.GetStatus(),
		Children:     hyper.Children,
		HostIP:       hyper.HostIP,
		RouteIP:      hyper.RouteIP,
		VirtType:     hyper.VirtType,
		CpuOverRate:  hyper.CpuOverRate,
		MemOverRate:  hyper.MemOverRate,
		DiskOverRate: hyper.DiskOverRate,
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
