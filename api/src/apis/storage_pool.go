/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var storagePoolAPI = &StoragePoolAPI{}
var storagePoolAdmin = &services.StoragePoolAdmin{}
var hyperStorage = services.HyperStorage

type StoragePoolAPI struct{}

type StoragePoolPayload struct {
	Name          string  `json:"name" binding:"required,min=2,max=64"`
	Media         string  `json:"media" binding:"omitempty,oneof=ssd hdd nvme"`
	FallbackGroup string  `json:"fallback_group" binding:"omitempty,max=64"`
	OverRatio     float64 `json:"over_ratio" binding:"omitempty,gte=0,lte=20"`
	IsDefault     bool    `json:"is_default"`
	Description   string  `json:"description" binding:"omitempty,max=256"`
}

type StoragePoolPatchPayload struct {
	Name          *string  `json:"name" binding:"omitempty,min=2,max=64"`
	Media         *string  `json:"media" binding:"omitempty"`
	FallbackGroup *string  `json:"fallback_group" binding:"omitempty,max=64"`
	OverRatio     *float64 `json:"over_ratio" binding:"omitempty,gte=0,lte=20"`
	Status        *string  `json:"status" binding:"omitempty,oneof=active disabled"`
	IsDefault     *bool    `json:"is_default" binding:"omitempty"`
	Description   *string  `json:"description" binding:"omitempty,max=256"`
}

type StoragePoolResponse struct {
	*ResourceReference
	Driver         string  `json:"driver"`
	Shared         bool    `json:"shared"`
	Builtin        bool    `json:"builtin"`
	Media          string  `json:"media"`
	FallbackGroup  string  `json:"fallback_group,omitempty"`
	Status         string  `json:"status,omitempty"`
	IsDefault      bool    `json:"is_default"`
	OverRatio      float64 `json:"over_ratio,omitempty"`
	MountPath      string  `json:"mount_path,omitempty"`
	Description    string  `json:"description,omitempty"`
	AvailableHosts int     `json:"available_hosts"`
	Hosts          int     `json:"hosts,omitempty"`
	CapacityBytes  int64   `json:"capacity_bytes,omitempty"`
	UsedBytes      int64   `json:"used_bytes,omitempty"`
	AllocatedBytes int64   `json:"allocated_bytes,omitempty"`
}

type StoragePoolListResponse struct {
	Offset       int                    `json:"offset"`
	Total        int                    `json:"total"`
	Limit        int                    `json:"limit"`
	StoragePools []*StoragePoolResponse `json:"storage_pools"`
}

// HostPoolResponse is a pool set up on a host
type HostPoolResponse struct {
	StoragePool       *ResourceReference     `json:"storage_pool"`
	Hypervisor        *ResourceReference     `json:"hypervisor"`
	Builtin           bool                   `json:"builtin"`
	Media             string                 `json:"media"`
	Status            string                 `json:"status"`
	Reason            string                 `json:"reason"`
	LastOp            string                 `json:"last_op"`
	ReportedStatus    string                 `json:"reported_status"`
	ReportedReason    string                 `json:"reported_reason"`
	Maintenance       bool                   `json:"maintenance"`
	Layout            string                 `json:"layout"`
	Devices           []*services.PoolDevice `json:"devices"`
	CapacityBytes     int64                  `json:"capacity_bytes"`
	UsedBytes         int64                  `json:"used_bytes"`
	AvailBytes        int64                  `json:"avail_bytes"`
	OwnBytes          int64                  `json:"own_bytes"`
	AllocatedBytes    int64                  `json:"allocated_bytes"`
	ReservedBytes     int64                  `json:"reserved_bytes"`
	UsageRatio        float64                `json:"usage_ratio"`
	SyncPercent       int32                  `json:"sync_percent"`
	VolumeCount       int64                  `json:"volume_count"`
	StorageFullPaused int64                  `json:"storage_full_paused"`
	CheckedAt         string                 `json:"checked_at,omitempty"`
	CapacityAt        string                 `json:"capacity_at,omitempty"`
	UsageAt           string                 `json:"usage_at,omitempty"`
	Usage             []*services.UsageEntry `json:"usage,omitempty"`
}

type HostPoolListResponse struct {
	StoragePools     []*HostPoolResponse `json:"storage_pools"`
	PendingInstances []*BaseReference    `json:"pending_instances"`
}

type HostDiskResponse struct {
	ID            int64  `json:"id"`
	DiskID        string `json:"disk_id"`
	Name          string `json:"name"`
	Serial        string `json:"serial"`
	Model         string `json:"model"`
	SizeBytes     int64  `json:"size_bytes"`
	Transport     string `json:"transport"`
	Media         string `json:"media"`
	DetectedMedia string `json:"detected_media"`
	MediaSource   string `json:"media_source"`
	State         string `json:"state"`
	Detail        string `json:"detail"`
	PoolUUID      string `json:"pool_uuid,omitempty"`
	PoolName      string `json:"pool_name,omitempty"`
	OwnerHostid   int32  `json:"owner_hostid,omitempty"`
	OrphanCount   int64  `json:"orphan_count,omitempty"`
	ScannedAt     string `json:"scanned_at"`
}

type HostPoolPayload struct {
	StoragePool        *BaseReference `json:"storage_pool" binding:"required"`
	Layout             string         `json:"layout" binding:"required,oneof=single linear raid1"`
	Disks              []string       `json:"disks" binding:"required,min=1,max=32"`
	Wipe               bool           `json:"wipe"`
	AllowMediaMismatch bool           `json:"allow_media_mismatch"`
	DestroyPools       []string       `json:"destroy_pools" binding:"omitempty,max=8"`
	Confirm            string         `json:"confirm" binding:"required"`
}

type HostPoolExtendPayload struct {
	Disks              []string `json:"disks" binding:"required,min=1,max=32"`
	Wipe               bool     `json:"wipe"`
	AllowMediaMismatch bool     `json:"allow_media_mismatch"`
	Confirm            string   `json:"confirm" binding:"required"`
}

type HostPoolReplacePayload struct {
	FailedDisk         string `json:"failed_disk" binding:"required"`
	NewDisk            string `json:"new_disk" binding:"required"`
	Wipe               bool   `json:"wipe"`
	AllowMediaMismatch bool   `json:"allow_media_mismatch"`
	Confirm            string `json:"confirm" binding:"required"`
}

type ConfirmPayload struct {
	Confirm        string `json:"confirm" binding:"required"`
	NodeOfflineAck bool   `json:"node_offline_ack"`
}

type MaintenancePayload struct {
	Enable bool `json:"enable"`
}

type AdoptPayload struct {
	StoragePool *BaseReference `json:"storage_pool" binding:"required"`
	Confirm     string         `json:"confirm" binding:"required"`
}

type AbandonPayload struct {
	// Host id the volumes waiting for adoption still name; 0 is a valid host id
	OldHostid *int32 `json:"old_hostid" binding:"required,gte=0"` // a pointer, as "required" rejects 0 on a plain int
	Confirm   string `json:"confirm" binding:"required"`
}

type DiskMediaPayload struct {
	Media string `json:"media" binding:"omitempty,oneof=ssd hdd nvme"`
}

// poolResponse builds the response of a pool from a summary made beforehand: the list makes one for the whole page
func (v *StoragePoolAPI) poolResponse(ctx context.Context, pool *model.StoragePool, summary *services.PoolSummary) *StoragePoolResponse {
	if summary == nil {
		summary = &services.PoolSummary{}
	}
	resp := &StoragePoolResponse{
		ResourceReference: &ResourceReference{ID: pool.UUID, Name: pool.Name},
		Driver:            pool.Driver,
		Shared:            pool.Shared(),
		Builtin:           pool.Builtin,
		Media:             pool.Media,
		IsDefault:         pool.IsDefault,
		AvailableHosts:    summary.AvailableHosts,
	}
	if GetMemberShip(ctx).IsSystemAdmin() {
		resp.CreatedAt = pool.CreatedAt.Format(TimeStringForMat)
		resp.UpdatedAt = pool.UpdatedAt.Format(TimeStringForMat)
		resp.FallbackGroup = pool.FallbackGroup
		resp.Status = pool.Status
		resp.OverRatio = pool.OverRatio
		resp.MountPath = pool.Root()
		resp.Description = pool.Description
		resp.Hosts = summary.Hosts
		resp.CapacityBytes = summary.CapacityBytes
		resp.UsedBytes = summary.UsedBytes
		resp.AllocatedBytes = summary.AllocatedBytes
	}
	return resp
}

func hostPoolResponse(view *services.HyperPoolView) *HostPoolResponse {
	row := view.Row
	resp := &HostPoolResponse{
		StoragePool:       &ResourceReference{ID: view.Pool.UUID, Name: view.Pool.Name},
		Builtin:           view.Pool.Builtin,
		Media:             view.Pool.Media,
		Status:            row.Status,
		Reason:            row.Reason,
		LastOp:            row.LastOp,
		ReportedStatus:    row.ReportedStatus,
		ReportedReason:    row.ReportedReason,
		Maintenance:       row.Maintenance,
		Layout:            row.Layout,
		Devices:           view.Devices,
		CapacityBytes:     row.CapacityBytes,
		UsedBytes:         row.UsedBytes,
		AvailBytes:        row.AvailBytes,
		OwnBytes:          row.OwnBytes,
		AllocatedBytes:    view.AllocatedBytes,
		ReservedBytes:     view.ReservedBytes,
		UsageRatio:        row.UsageRatio(),
		SyncPercent:       row.SyncPercent,
		VolumeCount:       view.VolumeCount,
		StorageFullPaused: view.StorageFullPaused,
	}
	if view.Hyper != nil {
		resp.Hypervisor = &ResourceReference{ID: view.Hyper.UUID, Name: view.Hyper.Hostname}
	}
	if row.CheckedAt != nil {
		resp.CheckedAt = row.CheckedAt.Format(TimeStringForMat)
	}
	if row.CapacityAt != nil {
		resp.CapacityAt = row.CapacityAt.Format(TimeStringForMat)
	}
	if row.UsageAt != nil {
		resp.UsageAt = row.UsageAt.Format(TimeStringForMat)
		_ = json.Unmarshal([]byte(row.UsageReport), &resp.Usage)
	}
	return resp
}

// @Summary list storage pools
// @Description list storage pools. Members see the active pools with their media and how many hosts can use them; system admins see everything
// @tags StoragePool
// @Produce json
// @Success 200 {object} StoragePoolListResponse
// @Router /storage_pools [get]
func (v *StoragePoolAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if offset < 0 || limit < 0 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset or limit", nil)
		return
	}
	total, pools, err := storagePoolAdmin.List(ctx, int64(offset), int64(limit), c.DefaultQuery("order", "created_at"), c.DefaultQuery("query", ""))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list storage pools", err)
		return
	}
	resp := &StoragePoolListResponse{Offset: offset, Total: int(total), Limit: len(pools), StoragePools: []*StoragePoolResponse{}}
	summaries := hyperStorage.Summaries(ctx, pools)
	for _, pool := range pools {
		resp.StoragePools = append(resp.StoragePools, v.poolResponse(ctx, pool, summaries[pool.ID]))
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary get a storage pool
// @tags StoragePool
// @Produce json
// @Param   id  path  string  true  "Storage pool UUID"
// @Success 200 {object} StoragePoolResponse
// @Router /storage_pools/{id} [get]
func (v *StoragePoolAPI) Get(c *gin.Context) {
	ctx := c.Request.Context()
	pool, err := storagePoolAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", err)
		return
	}
	if pool.Status != model.StoragePoolActive && !GetMemberShip(ctx).IsSystemAdmin() {
		ErrorResponse(c, http.StatusNotFound, "Storage pool not found", NewCLError(ErrStoragePoolNotFound, "Storage pool not found", nil))
		return
	}
	c.JSON(http.StatusOK, v.poolResponse(ctx, pool, hyperStorage.Summary(ctx, pool)))
}

// @Summary create a storage pool
// @Description create a local storage pool. Its directory on every host is /opt/cloudland/pools/<uuid>; nothing is written on any host until the pool is set up there
// @tags StoragePool
// @Accept  json
// @Produce json
// @Param   message  body  StoragePoolPayload  true  "Storage pool"
// @Success 200 {object} StoragePoolResponse
// @Router /storage_pools [post]
func (v *StoragePoolAPI) Create(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &StoragePoolPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	pool, err := storagePoolAdmin.Create(ctx, payload.Name, payload.Media, payload.FallbackGroup, payload.OverRatio, payload.IsDefault, payload.Description)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to create storage pool", err)
		return
	}
	c.JSON(http.StatusOK, v.poolResponse(ctx, pool, hyperStorage.Summary(ctx, pool)))
}

// @Summary update a storage pool
// @tags StoragePool
// @Accept  json
// @Produce json
// @Param   id       path  string                   true  "Storage pool UUID"
// @Param   message  body  StoragePoolPatchPayload  true  "Fields to change"
// @Success 200 {object} StoragePoolResponse
// @Router /storage_pools/{id} [patch]
func (v *StoragePoolAPI) Patch(c *gin.Context) {
	ctx := c.Request.Context()
	pool, err := storagePoolAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", err)
		return
	}
	payload := &StoragePoolPatchPayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	err = storagePoolAdmin.Update(ctx, pool, &services.StoragePoolUpdate{
		Name: payload.Name, Media: payload.Media, FallbackGroup: payload.FallbackGroup, OverRatio: payload.OverRatio,
		Status: payload.Status, IsDefault: payload.IsDefault, Description: payload.Description,
	})
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to update storage pool", err)
		return
	}
	pool, _ = storagePoolAdmin.GetByUUID(ctx, pool.UUID)
	c.JSON(http.StatusOK, v.poolResponse(ctx, pool, hyperStorage.Summary(ctx, pool)))
}

// @Summary delete a storage pool
// @tags StoragePool
// @Param   id  path  string  true  "Storage pool UUID"
// @Success 204
// @Router /storage_pools/{id} [delete]
func (v *StoragePoolAPI) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	pool, err := storagePoolAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", err)
		return
	}
	if err = storagePoolAdmin.Delete(ctx, pool); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to delete storage pool", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// @Summary list the hosts of a storage pool
// @tags StoragePool
// @Produce json
// @Param   id  path  string  true  "Storage pool UUID"
// @Success 200 {object} HostPoolListResponse
// @Router /storage_pools/{id}/hypers [get]
func (v *StoragePoolAPI) ListHosts(c *gin.Context) {
	ctx := c.Request.Context()
	pool, err := storagePoolAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", err)
		return
	}
	views, err := hyperStorage.ListPoolHosts(ctx, pool)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list hosts", err)
		return
	}
	resp := &HostPoolListResponse{StoragePools: []*HostPoolResponse{}, PendingInstances: []*BaseReference{}}
	for _, view := range views {
		resp.StoragePools = append(resp.StoragePools, hostPoolResponse(view))
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary abandon the volumes of a pool that wait for adoption
// @Description the volumes of the pool left by a deleted host become lost and can then be deleted
// @tags StoragePool
// @Accept  json
// @Param   id       path  string          true  "Storage pool UUID"
// @Param   message  body  AbandonPayload  true  "Old host id and the pool name to confirm"
// @Success 200
// @Router /storage_pools/{id}/orphans/abandon [post]
func (v *StoragePoolAPI) AbandonOrphans(c *gin.Context) {
	ctx := c.Request.Context()
	pool, err := storagePoolAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", err)
		return
	}
	payload := &AbandonPayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	count, err := hyperStorage.AbandonOrphans(ctx, pool, *payload.OldHostid, payload.Confirm)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to abandon the volumes", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"volumes": count})
}

// ---- storage of a host ----

func (v *StoragePoolAPI) hyperOf(c *gin.Context) (*model.Hyper, bool) {
	hyper, err := hyperAdmin.GetHyperByUUID(c.Request.Context(), c.Param("uuid"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid hypervisor", err)
		return nil, false
	}
	return hyper, true
}

func (v *StoragePoolAPI) hostPoolOf(c *gin.Context) (*model.Hyper, *model.StoragePool, bool) {
	hyper, ok := v.hyperOf(c)
	if !ok {
		return nil, nil, false
	}
	pool, err := storagePoolAdmin.GetByUUID(c.Request.Context(), c.Param("pool_id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", err)
		return nil, nil, false
	}
	return hyper, pool, true
}

// @Summary scan the disks of a host
// @Description the host scans its disks in the background; read the result with GET /hypers/{uuid}/disks
// @tags HostStorage
// @Param   uuid  path  string  true  "Hypervisor UUID"
// @Success 202
// @Router /hypers/{uuid}/disks/scan [post]
func (v *StoragePoolAPI) ScanDisks(c *gin.Context) {
	hyper, ok := v.hyperOf(c)
	if !ok {
		return
	}
	if err := hyperStorage.ScanDisks(c.Request.Context(), hyper); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to scan disks", err)
		return
	}
	c.JSON(http.StatusAccepted, nil)
}

// @Summary list the disks of a host
// @tags HostStorage
// @Produce json
// @Param   uuid  path  string  true  "Hypervisor UUID"
// @Success 200 {array} HostDiskResponse
// @Router /hypers/{uuid}/disks [get]
func (v *StoragePoolAPI) ListDisks(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, ok := v.hyperOf(c)
	if !ok {
		return
	}
	disks, err := hyperStorage.ListDisks(ctx, hyper)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list disks", err)
		return
	}
	resp := []*HostDiskResponse{}
	for _, d := range disks {
		resp = append(resp, diskResponse(ctx, d))
	}
	c.JSON(http.StatusOK, gin.H{"disks": resp})
}

func diskResponse(ctx context.Context, d *model.HyperDisk) *HostDiskResponse {
	r := &HostDiskResponse{
		ID: d.ID, DiskID: d.DiskID, Name: d.Name, Serial: d.Serial, Model: d.DiskModel, SizeBytes: d.SizeBytes,
		Transport: d.Transport, Media: d.Media, DetectedMedia: d.DetectedMedia, MediaSource: d.MediaSource,
		State: d.State, Detail: d.Detail, PoolUUID: d.PoolUUID, OwnerHostid: d.OwnerHostid,
		ScannedAt: d.ScannedAt.Format(TimeStringForMat),
	}
	if d.PoolUUID != "" {
		if pool, err := services.PoolByRef(ctx, d.PoolUUID); err == nil {
			r.PoolName = pool.Name
		}
		if d.State == model.DiskCloudlandPool {
			r.OrphanCount = services.OrphanCount(ctx, d.PoolUUID, d.OwnerHostid)
		}
	}
	return r
}

// @Summary set the media of a disk
// @Description an empty media goes back to the detected one
// @tags HostStorage
// @Accept  json
// @Produce json
// @Param   uuid     path  string            true  "Hypervisor UUID"
// @Param   id       path  int               true  "Disk record id"
// @Param   message  body  DiskMediaPayload  true  "Media"
// @Success 200 {object} HostDiskResponse
// @Router /hypers/{uuid}/disks/{id} [patch]
func (v *StoragePoolAPI) PatchDisk(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, ok := v.hyperOf(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid disk id", err)
		return
	}
	payload := &DiskMediaPayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	disk, err := hyperStorage.SetDiskMedia(ctx, hyper, id, payload.Media)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to update disk", err)
		return
	}
	c.JSON(http.StatusOK, diskResponse(ctx, disk))
}

// @Summary list the storage pools of a host
// @tags HostStorage
// @Produce json
// @Param   uuid  path  string  true  "Hypervisor UUID"
// @Success 200 {object} HostPoolListResponse
// @Router /hypers/{uuid}/storage_pools [get]
func (v *StoragePoolAPI) ListHostPools(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, ok := v.hyperOf(c)
	if !ok {
		return
	}
	views, err := hyperStorage.ListHostPools(ctx, hyper)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list storage pools", err)
		return
	}
	resp := &HostPoolListResponse{StoragePools: []*HostPoolResponse{}, PendingInstances: []*BaseReference{}}
	for _, view := range views {
		resp.StoragePools = append(resp.StoragePools, hostPoolResponse(view))
	}
	for _, inst := range services.PendingInstances(ctx, hyper.Hostid) {
		resp.PendingInstances = append(resp.PendingInstances, &BaseReference{ID: inst.UUID, Name: inst.Hostname})
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary set a storage pool up on a host
// @Description formats the selected disks. Type the host name in confirm
// @tags HostStorage
// @Accept  json
// @Produce json
// @Param   uuid     path  string           true  "Hypervisor UUID"
// @Param   message  body  HostPoolPayload  true  "Pool, layout and disks"
// @Success 202
// @Router /hypers/{uuid}/storage_pools [post]
func (v *StoragePoolAPI) CreateHostPool(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, ok := v.hyperOf(c)
	if !ok {
		return
	}
	payload := &HostPoolPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	pool, err := storagePoolAdmin.Resolve(ctx, payload.StoragePool)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", err)
		return
	}
	_, warnings, err := hyperStorage.CreatePool(ctx, hyper, pool, payload.Layout, &services.PoolDiskRequest{
		Disks: payload.Disks, Wipe: payload.Wipe, AllowMediaMismatch: payload.AllowMediaMismatch,
		DestroyPools: payload.DestroyPools, Confirm: payload.Confirm,
	})
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to set the storage pool up", err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"raid1_pairs": warnings})
}

// @Summary add disks to a storage pool of a host
// @tags HostStorage
// @Accept  json
// @Produce json
// @Param   uuid     path  string                 true  "Hypervisor UUID"
// @Param   pool_id  path  string                 true  "Storage pool UUID"
// @Param   message  body  HostPoolExtendPayload  true  "Disks"
// @Success 202
// @Router /hypers/{uuid}/storage_pools/{pool_id}/extend [post]
func (v *StoragePoolAPI) ExtendHostPool(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, pool, ok := v.hostPoolOf(c)
	if !ok {
		return
	}
	payload := &HostPoolExtendPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	warnings, err := hyperStorage.ExtendPool(ctx, hyper, pool, &services.PoolDiskRequest{
		Disks: payload.Disks, Wipe: payload.Wipe, AllowMediaMismatch: payload.AllowMediaMismatch, Confirm: payload.Confirm,
	})
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to extend the storage pool", err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"raid1_pairs": warnings})
}

// @Summary replace a failed disk of a RAID1 pool
// @tags HostStorage
// @Accept  json
// @Param   uuid     path  string                  true  "Hypervisor UUID"
// @Param   pool_id  path  string                  true  "Storage pool UUID"
// @Param   message  body  HostPoolReplacePayload  true  "Failed and new disk"
// @Success 202
// @Router /hypers/{uuid}/storage_pools/{pool_id}/replace_disk [post]
func (v *StoragePoolAPI) ReplaceDisk(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, pool, ok := v.hostPoolOf(c)
	if !ok {
		return
	}
	payload := &HostPoolReplacePayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	err := hyperStorage.ReplaceDisk(ctx, hyper, pool, payload.FailedDisk, &services.PoolDiskRequest{
		Disks: []string{payload.NewDisk}, Wipe: payload.Wipe, AllowMediaMismatch: payload.AllowMediaMismatch, Confirm: payload.Confirm,
	})
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to replace the disk", err)
		return
	}
	c.JSON(http.StatusAccepted, nil)
}

// @Summary remove a storage pool from a host
// @Description force deletes leftover files of an empty pool; a lost pool is only unmounted and its data kept
// @tags HostStorage
// @Accept  json
// @Param   uuid     path   string          true   "Hypervisor UUID"
// @Param   pool_id  path   string          true   "Storage pool UUID"
// @Param   force    query  bool            false  "Delete leftover files"
// @Param   confirm  query  string          true   "Host name to confirm"
// @Success 202
// @Router /hypers/{uuid}/storage_pools/{pool_id} [delete]
func (v *StoragePoolAPI) DeleteHostPool(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, pool, ok := v.hostPoolOf(c)
	if !ok {
		return
	}
	// The gateway drops the body of DELETE requests: the confirmation comes as a query parameter
	payload := &ConfirmPayload{Confirm: c.Query("confirm")}
	if payload.Confirm == "" {
		_ = c.ShouldBindJSON(payload)
	}
	if err := hyperStorage.RemovePool(ctx, hyper, pool, payload.Confirm, c.Query("force") == "true"); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to remove the storage pool", err)
		return
	}
	c.JSON(http.StatusAccepted, nil)
}

// @Summary put a storage pool of a host in or out of maintenance
// @tags HostStorage
// @Accept  json
// @Param   uuid     path  string              true  "Hypervisor UUID"
// @Param   pool_id  path  string              true  "Storage pool UUID"
// @Param   message  body  MaintenancePayload  true  "Enable or disable"
// @Success 200
// @Router /hypers/{uuid}/storage_pools/{pool_id}/maintenance [post]
func (v *StoragePoolAPI) Maintenance(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, pool, ok := v.hostPoolOf(c)
	if !ok {
		return
	}
	payload := &MaintenancePayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	if err := hyperStorage.SetMaintenance(ctx, hyper, pool, payload.Enable); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to change maintenance", err)
		return
	}
	c.JSON(http.StatusOK, nil)
}

// @Summary declare a storage pool of a host lost
// @tags HostStorage
// @Accept  json
// @Param   uuid     path  string          true  "Hypervisor UUID"
// @Param   pool_id  path  string          true  "Storage pool UUID"
// @Param   message  body  ConfirmPayload  true  "Host name to confirm; node_offline_ack when the host is offline"
// @Success 200
// @Router /hypers/{uuid}/storage_pools/{pool_id}/lost [post]
func (v *StoragePoolAPI) DeclareLost(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, pool, ok := v.hostPoolOf(c)
	if !ok {
		return
	}
	payload := &ConfirmPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	count, err := hyperStorage.DeclareLost(ctx, hyper, pool, payload.Confirm, payload.NodeOfflineAck)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to declare the pool lost", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"volumes": count})
}

// @Summary restore a storage pool declared lost
// @Description only when the host reports the pool healthy again; volumes whose file is found become usable
// @tags HostStorage
// @Param   uuid     path  string  true  "Hypervisor UUID"
// @Param   pool_id  path  string  true  "Storage pool UUID"
// @Success 200
// @Router /hypers/{uuid}/storage_pools/{pool_id}/restore [post]
func (v *StoragePoolAPI) Restore(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, pool, ok := v.hostPoolOf(c)
	if !ok {
		return
	}
	count, err := hyperStorage.Restore(ctx, hyper, pool)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to restore the storage pool", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"volumes": count})
}

// @Summary adopt a storage pool found on the disks of a host
// @tags HostStorage
// @Accept  json
// @Param   uuid     path  string        true  "Hypervisor UUID"
// @Param   message  body  AdoptPayload  true  "Pool and host name to confirm"
// @Success 202
// @Router /hypers/{uuid}/storage_pools/adopt [post]
func (v *StoragePoolAPI) Adopt(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, ok := v.hyperOf(c)
	if !ok {
		return
	}
	payload := &AdoptPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	pool, err := storagePoolAdmin.Resolve(ctx, payload.StoragePool)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", err)
		return
	}
	if err = hyperStorage.Adopt(ctx, hyper, pool, payload.Confirm); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to adopt the storage pool", err)
		return
	}
	c.JSON(http.StatusAccepted, nil)
}

// @Summary list the largest files of a storage pool on a host
// @Description POST asks the host for a fresh report, GET returns the last one inside the pool listing
// @tags HostStorage
// @Param   uuid     path  string  true  "Hypervisor UUID"
// @Param   pool_id  path  string  true  "Storage pool UUID"
// @Success 202
// @Router /hypers/{uuid}/storage_pools/{pool_id}/usage [post]
func (v *StoragePoolAPI) ScanUsage(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, pool, ok := v.hostPoolOf(c)
	if !ok {
		return
	}
	if err := hyperStorage.ScanUsage(ctx, hyper, pool); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to scan the usage", err)
		return
	}
	c.JSON(http.StatusAccepted, nil)
}

// @Summary read the last usage report of a storage pool on a host
// @tags HostStorage
// @Produce json
// @Param   uuid     path  string  true  "Hypervisor UUID"
// @Param   pool_id  path  string  true  "Storage pool UUID"
// @Success 200
// @Router /hypers/{uuid}/storage_pools/{pool_id}/usage [get]
func (v *StoragePoolAPI) GetUsage(c *gin.Context) {
	ctx := c.Request.Context()
	hyper, pool, ok := v.hostPoolOf(c)
	if !ok {
		return
	}
	entries, at, err := hyperStorage.HostPoolUsage(ctx, hyper, pool)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to read the usage", err)
		return
	}
	resp := gin.H{"usage": entries}
	if at != nil {
		resp["usage_at"] = at.Format(TimeStringForMat)
	}
	c.JSON(http.StatusOK, resp)
}
