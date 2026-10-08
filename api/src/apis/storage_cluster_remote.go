/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

// Remote mounts (shared-storage-design.md §7.11): a file system of one managed cluster mounted by the hosts of
// another cluster of the kind, under the same name and mount point, so the pools on it are usable there too

import (
	"net/http"

	. "api/src/common"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

// StorageRemoteMountResponse is a remote mount: the cluster whose file system it is, the cluster that mounts it
type StorageRemoteMountResponse struct {
	*ResourceReference
	Owner      *ResourceReference `json:"owner"`
	Access     *ResourceReference `json:"access"`
	Filesystem string             `json:"filesystem"`
	MountPoint string             `json:"mount_point"`
	// mounting | ready | unmounting | error
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt string `json:"created_at"`
}

type StorageRemoteMountListResponse struct {
	RemoteMounts []*StorageRemoteMountResponse `json:"remote_mounts"`
}

type StorageRemoteMountPayload struct {
	// File system of this cluster
	Filesystem string `json:"filesystem" binding:"required,max=32"`
	// The cluster whose hosts mount it (UUID)
	Cluster string `json:"cluster" binding:"required,uuid"`
}

// @Summary list the remote mounts of a storage cluster
// @Description the file systems of this cluster other clusters mount, and the file systems of others it mounts (shared-storage-design.md §7.11); system admins
// @tags StorageCluster
// @Produce json
// @Param   id  path  string  true  "Cluster UUID"
// @Success 200 {object} StorageRemoteMountListResponse
// @Router /storage_clusters/{id}/remote_mounts [get]
func (v *StorageClusterAPI) ListRemoteMounts(c *gin.Context) {
	views, err := storageClusterAdmin.RemoteMounts(c.Request.Context(), c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list the remote mounts", err)
		return
	}
	resp := &StorageRemoteMountListResponse{RemoteMounts: []*StorageRemoteMountResponse{}}
	for _, m := range views {
		resp.RemoteMounts = append(resp.RemoteMounts, storageRemoteMountResponse(m))
	}
	c.JSON(http.StatusOK, resp)
}

func storageRemoteMountResponse(m *services.StorageRemoteMountView) *StorageRemoteMountResponse {
	return &StorageRemoteMountResponse{
		ResourceReference: &ResourceReference{ID: m.Mount.UUID, Name: m.Mount.Name},
		Owner:             &ResourceReference{ID: m.Owner.UUID, Name: m.Owner.Name},
		Access:            &ResourceReference{ID: m.Access.UUID, Name: m.Access.Name},
		Filesystem:        m.Mount.Name,
		MountPoint:        m.Mount.MountPoint,
		Status:            m.Mount.Status,
		Reason:            m.Mount.Reason,
		CreatedAt:         m.Mount.CreatedAt.Format(TimeStringForMat),
	}
}

// @Summary let another storage cluster mount a file system
// @Description the hosts of another managed cluster of the kind mount a file system of this one under its name and mount point (GPFS multi-cluster: keys exchanged, the file system granted, mounted on every host there); the pools on it become usable on those hosts. A task of the cluster that mounts (shared-storage-design.md §7.11); system admins
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   id       path  string                     true  "Cluster UUID (whose file system it is)"
// @Param   message  body  StorageRemoteMountPayload  true  "The file system and the cluster that mounts it"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/remote_mounts [post]
func (v *StorageClusterAPI) CreateRemoteMount(c *gin.Context) {
	payload := &StorageRemoteMountPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	task, err := storageClusterAdmin.CreateRemoteMount(c.Request.Context(), c.Param("id"), payload.Filesystem, payload.Cluster)
	v.taskStarted(c, task, err, "Failed to mount the file system remotely")
}

// @Summary stop another storage cluster mounting a file system
// @Description the hosts of the other cluster unmount the file system and its grant goes; refused while instances on those hosts use volumes of the pools on it (shared-storage-design.md §7.11); system admins
// @tags StorageCluster
// @Produce json
// @Param   id        path  string  true  "Cluster UUID (whose file system it is)"
// @Param   mount_id  path  string  true  "Remote mount UUID"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/remote_mounts/{mount_id} [delete]
func (v *StorageClusterAPI) DeleteRemoteMount(c *gin.Context) {
	task, err := storageClusterAdmin.DeleteRemoteMount(c.Request.Context(), c.Param("id"), c.Param("mount_id"))
	if err != nil {
		status := http.StatusBadRequest
		if clErr, ok := err.(*CLError); ok && (clErr.Code == ErrStorageClusterBusy || clErr.Code == ErrStoragePoolInUse) {
			status = http.StatusConflict
		}
		ErrorResponse(c, status, "Failed to stop the remote mount", err)
		return
	}
	v.taskStarted(c, task, nil, "")
}
