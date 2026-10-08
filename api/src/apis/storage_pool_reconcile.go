/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"context"
	"net/http"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

type StoragePoolReconcileResponse struct {
	// running: a host lists the pool; done: orphans holds the result; error: see error. Empty when never asked
	Status      string             `json:"status"`
	Hypervisor  *ResourceReference `json:"hypervisor,omitempty"`
	Error       string             `json:"error,omitempty"`
	Objects     int                `json:"objects"`
	Truncated   bool               `json:"truncated"`
	RequestedAt string             `json:"requested_at,omitempty"`
	CheckedAt   string             `json:"checked_at,omitempty"`
	// What is in the pool with no record in CloudLand: kind volume, deleted_volume (its record is deleted), image,
	// nvram, temporary or other. Nothing is removed
	Orphans []*services.StorageOrphan `json:"orphans"`
}

func storagePoolReconcileResponse(ctx context.Context, r *model.StoragePoolReconcile) *StoragePoolReconcileResponse {
	resp := &StoragePoolReconcileResponse{Orphans: []*services.StorageOrphan{}}
	if r == nil {
		return resp
	}
	resp.Status, resp.Error, resp.Objects, resp.Truncated = r.Status, r.Error, r.Objects, r.Truncated
	resp.RequestedAt = r.RequestedAt.Format(TimeStringForMat)
	resp.CheckedAt = formatTimePtr(r.CheckedAt)
	resp.Orphans = services.ParseStorageOrphans(r.Orphans)
	hyper := &model.Hyper{}
	if dbs.DBContext(ctx).Where("hostid = ?", r.Hostid).Take(hyper).Error == nil {
		resp.Hypervisor = &ResourceReference{ID: hyper.UUID, Name: hyper.Hostname}
	}
	return resp
}

// @Summary list the orphans of a shared pool
// @Description asks a host that reaches the pool for its files (GPFS) or RBD images (Ceph); clapi then reports those it has no record of (shared-storage-design.md §16 S5). Nothing is removed. A request still waiting is not sent again; read the result with GET
// @tags StoragePool
// @Produce json
// @Param   id  path  string  true  "Pool UUID"
// @Success 202 {object} StoragePoolReconcileResponse
// @Router /storage_pools/{id}/reconcile [post]
func (v *StoragePoolAPI) Reconcile(c *gin.Context) {
	ctx := c.Request.Context()
	r, err := storagePoolAdmin.ReconcilePool(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to reconcile the storage pool", err)
		return
	}
	c.JSON(http.StatusAccepted, storagePoolReconcileResponse(ctx, r))
}

// @Summary the last orphan report of a shared pool
// @Tags StoragePool
// @Produce json
// @Param   id  path  string  true  "Pool UUID"
// @Success 200 {object} StoragePoolReconcileResponse
// @Router /storage_pools/{id}/reconcile [get]
func (v *StoragePoolAPI) GetReconcile(c *gin.Context) {
	ctx := c.Request.Context()
	r, err := storagePoolAdmin.PoolReconcile(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", err)
		return
	}
	c.JSON(http.StatusOK, storagePoolReconcileResponse(ctx, r))
}
