/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"context"
	"net/http"
	"sort"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

// StorageAutoJoinResponse is where the hosts that join a cluster as clients on their own come from, and how far each
// got (shared-storage-design.md §6.3)
type StorageAutoJoinResponse struct {
	Zones   []*ResourceReference            `json:"zones"`
	Pending []*StoragePendingClientResponse `json:"pending"`
}

type StoragePendingClientResponse struct {
	Hypervisor *ResourceReference `json:"hypervisor"`
	// pending: waits for the cluster to be free; joining: its task runs; failed: left alone, see reason
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Task   string `json:"task,omitempty"`
	Since  string `json:"since"`
}

// storageAutoJoin describes the auto join of a cluster for its detail
func storageAutoJoin(ctx context.Context, cluster *model.StorageCluster) *StorageAutoJoinResponse {
	resp := &StorageAutoJoinResponse{Zones: []*ResourceReference{}, Pending: []*StoragePendingClientResponse{}}
	db := dbs.DBContext(ctx)
	if ids := services.ParseStorageZones(cluster.AutoJoinZones); len(ids) > 0 {
		zones := []*model.Zone{}
		db.Where("id IN ?", ids).Order("name").Find(&zones)
		for _, z := range zones {
			resp.Zones = append(resp.Zones, &ResourceReference{ID: z.UUID, Name: z.Name})
		}
	}
	list := services.ParseStoragePendingClients(cluster.PendingClients)
	if len(list) == 0 {
		return resp
	}
	hostids, taskIDs := []int32{}, []int64{}
	for _, p := range list {
		hostids = append(hostids, p.Hostid)
		if p.TaskID != 0 {
			taskIDs = append(taskIDs, p.TaskID)
		}
	}
	hypers := []*model.Hyper{}
	db.Where("hostid IN ?", hostids).Find(&hypers)
	byID := map[int32]*model.Hyper{}
	for _, h := range hypers {
		byID[h.Hostid] = h
	}
	tasks := storageClusterAdmin.TaskUUIDs(ctx, taskIDs)
	for _, p := range list {
		ref := &ResourceReference{}
		if h := byID[p.Hostid]; h != nil {
			ref = &ResourceReference{ID: h.UUID, Name: h.Hostname}
		}
		resp.Pending = append(resp.Pending, &StoragePendingClientResponse{Hypervisor: ref, Status: p.Status, Reason: p.Reason,
			Task: tasks[p.TaskID], Since: p.Since.Format(TimeStringForMat)})
	}
	sort.SliceStable(resp.Pending, func(i, j int) bool { return resp.Pending[i].Hypervisor.Name < resp.Pending[j].Hypervisor.Name })
	return resp
}

type StorageClusterPatchPayload struct {
	Description *string `json:"description" binding:"omitempty,max=256"`
	// Zones whose hosts join the cluster as clients on their own; [] turns it off (managed clusters only)
	AutoJoinZones *[]string `json:"auto_join_zones" binding:"omitempty,max=32,dive,uuid"`
	// Forget the hosts that failed to join on their own, so they are tried again
	RetryAutoJoin bool `json:"retry_auto_join"`
}

// @Summary change a storage cluster
// @Description the description, and the zones whose hosts join the cluster as clients on their own (shared-storage-design.md §6.3): every online host of those zones not in the cluster is queued and joins with a task of its own once the cluster is free; one that fails is left alone and listed until retry_auto_join
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   id       path  string                      true  "Cluster UUID"
// @Param   message  body  StorageClusterPatchPayload  true  "Changes"
// @Success 200 {object} StorageClusterResponse
// @Router /storage_clusters/{id} [patch]
func (v *StorageClusterAPI) Patch(c *gin.Context) {
	payload := &StorageClusterPatchPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	ctx := c.Request.Context()
	if _, err := storageClusterAdmin.Update(ctx, c.Param("id"), &services.StorageClusterUpdate{Description: payload.Description,
		AutoJoinZones: payload.AutoJoinZones, RetryAutoJoin: payload.RetryAutoJoin}); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to change the storage cluster", err)
		return
	}
	cluster, nodes, disks, err := storageClusterAdmin.Get(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage cluster", err)
		return
	}
	resp := storageClusterResponse(ctx, cluster, nodes, disks)
	storageClusterDetail(ctx, cluster, resp)
	c.JSON(http.StatusOK, resp)
}

type StorageReplaceDiskPayload struct {
	// The new disk: a scanned disk of the same host (hyper_disks.disk_id)
	DiskID string `json:"disk_id" binding:"required,max=256"`
	Media  string `json:"media" binding:"omitempty,oneof=ssd hdd nvme"`
	Wipe   bool   `json:"wipe"`
}

// @Summary replace a failed disk of a storage cluster
// @Description a disk the storage reports down is swapped for a new disk of the same host (shared-storage-design.md §7.5, §8.5): GPFS drops the failed NSD (mmdeldisk -p), adds the new one and restores the replication; Ceph destroys the OSD keeping its id and makes the new disk take it. The failed disk is not wiped. A disk that works is removed the normal way instead
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   id       path  string                     true  "Cluster UUID"
// @Param   disk_id  path  string                     true  "UUID of the failed disk in the cluster"
// @Param   message  body  StorageReplaceDiskPayload  true  "The new disk"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/disks/{disk_id}/replace [post]
func (v *StorageClusterAPI) ReplaceDisk(c *gin.Context) {
	payload := &StorageReplaceDiskPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	task, err := storageClusterAdmin.ReplaceDisk(c.Request.Context(), c.Param("id"), c.Param("disk_id"),
		&services.StorageDiskPlan{DiskID: payload.DiskID, Media: payload.Media, Wipe: payload.Wipe})
	v.taskStarted(c, task, err, "Failed to replace the disk")
}

type StorageChangeRolesPayload struct {
	// The roles as they will be; the disk role follows the disks of the host
	Roles []string `json:"roles" binding:"required,min=1,max=8,dive,max=16"`
}

// @Summary change the roles of a member of a storage cluster
// @Description GPFS quorum (with manager) and admin hosts, Ceph mon, mgr and admin placement (shared-storage-design.md §13.1), checked like a new cluster; the disk role follows the disks of the host
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   id          path  string                     true  "Cluster UUID"
// @Param   hypervisor  path  string                     true  "Hypervisor UUID"
// @Param   message     body  StorageChangeRolesPayload  true  "Roles"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/nodes/{hypervisor} [patch]
func (v *StorageClusterAPI) ChangeRoles(c *gin.Context) {
	payload := &StorageChangeRolesPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	ids, ok := v.resolveHosts(c, []string{c.Param("hypervisor")})
	if !ok {
		return
	}
	task, err := storageClusterAdmin.ChangeRoles(c.Request.Context(), c.Param("id"), ids[0], payload.Roles)
	v.taskStarted(c, task, err, "Failed to change the roles")
}
