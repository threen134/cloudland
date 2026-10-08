/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

// The copies of an image in the shared pools (shared-storage-design.md §9.6): which pools have one and how far their
// imports got, importing one ahead of the first boot disk (preheat), removing one no boot disk is cloned from.
// System admins only: the copies take room of pools every organization shares.

import (
	"net/http"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

type ImageStorageCopyResponse struct {
	StoragePool *ResourceReference `json:"storage_pool"`
	// syncing | synced | error | deleting
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Of an import running: wait (for an import slot of the host), download, write; percent of that phase
	Phase    string `json:"phase,omitempty"`
	Progress int    `json:"progress"`
	// The host running the import or the removal
	Host string `json:"host,omitempty"`
	// Boot disks cloned from it; a copy with some can not be removed
	BootDisks int64  `json:"boot_disks"`
	UpdatedAt string `json:"updated_at"`
}

type ImageStorageCopiesResponse struct {
	Copies []*ImageStorageCopyResponse `json:"copies"`
}

type ImagePreheatPayload struct {
	StoragePools []*BaseReference `json:"storage_pools" binding:"required,min=1,max=16,dive"`
}

func imageStorageCopies(views []*services.ImageStorageView) *ImageStorageCopiesResponse {
	resp := &ImageStorageCopiesResponse{Copies: []*ImageStorageCopyResponse{}}
	for _, v := range views {
		resp.Copies = append(resp.Copies, &ImageStorageCopyResponse{StoragePool: &ResourceReference{ID: v.Pool.UUID, Name: v.Pool.Name},
			Status: v.Copy.Status, Reason: v.Copy.Reason, Phase: v.Copy.Phase, Progress: v.Copy.Progress, Host: v.Host,
			BootDisks: v.Refs, UpdatedAt: v.Copy.UpdatedAt.Format(TimeStringForMat)})
	}
	return resp
}

// @Summary list the copies of an image in the shared pools
// @Description the copy of the image in each shared pool it was imported into, with how far a running import got (shared-storage-design.md §9.6); system admins
// @tags Image
// @Produce json
// @Param   id  path  string  true  "Image UUID"
// @Success 200 {object} ImageStorageCopiesResponse
// @Router /images/{id}/storage_copies [get]
func (v *ImageAPI) ListCopies(c *gin.Context) {
	ctx := c.Request.Context()
	image, err := imageAdmin.GetImageByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid image query", err)
		return
	}
	views, err := services.ImageStorageCopies(ctx, image)
	if err != nil {
		ErrorResponse(c, http.StatusForbidden, "Failed to list the copies of the image", err)
		return
	}
	c.JSON(http.StatusOK, imageStorageCopies(views))
}

// @Summary import an image into shared pools ahead of time
// @Description preheat: import the image into the shared pools given, so the first boot disk made there does not wait for the copy (shared-storage-design.md §9.6). A pool with a copy synced or being imported is left as it is, a failed one is imported again; system admins
// @tags Image
// @Accept  json
// @Produce json
// @Param   id       path  string               true  "Image UUID"
// @Param   message  body  ImagePreheatPayload  true  "Pools"
// @Success 200 {object} ImageStorageCopiesResponse
// @Router /images/{id}/storage_copies [post]
func (v *ImageAPI) Preheat(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &ImagePreheatPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	image, err := imageAdmin.GetImageByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid image query", err)
		return
	}
	pools := []*model.StoragePool{}
	for _, ref := range payload.StoragePools {
		if ref == nil || (ref.ID == "" && ref.Name == "") {
			ErrorResponse(c, http.StatusBadRequest, "A storage pool is needed", nil)
			return
		}
		pool, perr := storagePoolAdmin.Resolve(ctx, ref)
		if perr != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", perr)
			return
		}
		pools = append(pools, pool)
	}
	if _, err = services.PreheatImage(ctx, image, pools); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to copy the image into the pools", err)
		return
	}
	views, err := services.ImageStorageCopies(ctx, image)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to list the copies of the image", err)
		return
	}
	c.JSON(http.StatusOK, imageStorageCopies(views))
}

// @Summary remove the copy of an image in a shared pool
// @Description removes the copy of the image in the pool when no boot disk is cloned from it (shared-storage-design.md §9.6); 409 while boot disks use it or its import runs. The copy goes once its host removed it; system admins
// @tags Image
// @Produce json
// @Param   id    path  string  true  "Image UUID"
// @Param   pool  path  string  true  "Storage pool UUID"
// @Success 204
// @Router /images/{id}/storage_copies/{pool} [delete]
func (v *ImageAPI) DropCopy(c *gin.Context) {
	ctx := c.Request.Context()
	image, err := imageAdmin.GetImageByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid image query", err)
		return
	}
	pool, err := storagePoolAdmin.Resolve(ctx, &BaseReference{ID: c.Param("pool")})
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", err)
		return
	}
	if err = services.DropImageStorage(ctx, image, pool); err != nil {
		status := http.StatusBadRequest
		if clErr, ok := err.(*CLError); ok && clErr.Code == ErrImageCopyInUse {
			status = http.StatusConflict
		}
		ErrorResponse(c, status, "Failed to remove the copy of the image", err)
		return
	}
	c.Status(http.StatusNoContent)
}
