/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

// More file systems on a managed cluster (shared-storage-design.md §7.3): one made on new disks of its members,
// one with no pool on it deleted with its disks

import (
	"net/http"

	. "api/src/common"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

type StorageFilesystemPayload struct {
	// Name of the new file system, mounted at /gpfs/<name>
	Name string `json:"name" binding:"required,max=32"`
	// 1M, 2M, 4M (default), 8M or 16M
	BlockSize string `json:"block_size" binding:"omitempty,max=4"`
	// Copies of the data, 2 by default (1 in the test layout); the metadata gets 3 once there are 3 failure groups
	DataReplicas int `json:"data_replicas" binding:"omitempty,min=1,max=3"`
	// New disks of member hosts, on at least 3 hosts (one failure group each) outside the test layout
	Disks            []*StorageDiskPayload `json:"disks" binding:"required,min=1,max=512,dive"`
	AllowUnsupported bool                  `json:"allow_unsupported"`
}

// @Summary make a file system on a storage cluster
// @Description a new file system on new disks of the members: NSDs made, the file system made and mounted on every member (shared-storage-design.md §7.3). Pools pick it by name (filesystem); system admins
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   id       path  string                    true  "Cluster UUID"
// @Param   message  body  StorageFilesystemPayload  true  "The file system and its disks"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/filesystems [post]
func (v *StorageClusterAPI) CreateFilesystem(c *gin.Context) {
	payload := &StorageFilesystemPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	req, ok := v.expandPlan(c, &StorageExpandPayload{Disks: payload.Disks})
	if !ok {
		return
	}
	task, err := storageClusterAdmin.CreateFilesystem(c.Request.Context(), c.Param("id"), &services.StorageFilesystemPlan{Name: payload.Name,
		BlockSize: payload.BlockSize, DataReplicas: payload.DataReplicas}, req.Disks, payload.AllowUnsupported)
	v.taskStarted(c, task, err, "Failed to make the file system")
}

// @Summary delete a file system of a storage cluster
// @Description unmounts and deletes a file system no storage pool is on, with its NSDs; its disks are wiped and given back to their hosts. The only file system of a cluster goes with the cluster (shared-storage-design.md §7.3); system admins
// @tags StorageCluster
// @Produce json
// @Param   id    path  string  true  "Cluster UUID"
// @Param   name  path  string  true  "File system name"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/filesystems/{name} [delete]
func (v *StorageClusterAPI) DeleteFilesystem(c *gin.Context) {
	task, err := storageClusterAdmin.DeleteFilesystem(c.Request.Context(), c.Param("id"), c.Param("name"))
	if err != nil {
		status := http.StatusBadRequest
		if clErr, ok := err.(*CLError); ok && clErr.Code == ErrStorageClusterBusy {
			status = http.StatusConflict
		}
		ErrorResponse(c, status, "Failed to delete the file system", err)
		return
	}
	v.taskStarted(c, task, nil, "")
}
