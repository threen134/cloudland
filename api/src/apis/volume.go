/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

var volumeAPI = &VolumeAPI{}
var volumeAdmin = &services.VolumeAdmin{}

type VolumeAPI struct{}

type VolumePayload struct {
	Count int    `json:"count" binding:"omitempty,gte=1,lte=16"`
	Name  string `json:"name" binding:"required"`
	Size  int32  `json:"size" binding:"required"`
	// Storage pool of the volume; the default pool when left out
	StoragePool *BaseReference `json:"storage_pool" binding:"omitempty"`
}

// VolumePatchPayload renames and/or attaches a volume. Capacity changes go through POST /volumes/{id}/resize.
type VolumePatchPayload struct {
	Name string `json:"name" binding:"omitempty"`
	// Attach to this instance; null detaches, and leaving the field out keeps the attachment as it is
	Instance *BaseID `json:"instance" binding:"omitempty"`
}

type VolumeResponse struct {
	*ResourceReference
	Path        string             `json:"path"`
	Size        int32              `json:"size"`
	Format      string             `json:"format"`
	Status      string             `json:"status"`
	Reason      string             `json:"reason"`
	Target      string             `json:"target"`
	Href        string             `json:"href"`
	Booting     bool               `json:"booting"`
	Instance    *BaseReference     `json:"instance"`
	StoragePool *ResourceReference `json:"storage_pool"`
	// Host holding the file of the volume, for system admins; empty until the volume is attached the first time
	Hyper *ResourceReference `json:"hypervisor,omitempty"`
}

type VolumeInfoResponse struct {
	*ResourceReference
	Target string `json:"target"`
	// Size in GB; the instance detail page shows it next to each attached volume
	Size    int32 `json:"size"`
	Booting bool  `json:"booting"`
}

type VolumeListResponse struct {
	Offset  int               `json:"offset"`
	Total   int               `json:"total"`
	Limit   int               `json:"limit"`
	Volumes []*VolumeResponse `json:"volumes"`
}

type VolumeResizePayload struct {
	Size int32 `json:"size" binding:"required,gte=1"`
}

// @Summary get a volume
// @Description get a volume
// @tags Volume
// @Accept  json
// @Produce json
// @Param   id     path    string     true  "Volume UUID"
// @Success 200 {object} VolumeResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /volumes/{id} [get]
func (v *VolumeAPI) Get(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	logger.Ctx(ctx).Debugf("Get volume by uuid: %s", uuID)
	volume, err := volumeAdmin.GetVolumeByUUID(ctx, uuID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get volume by uuid: %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid volume query", err)
		return
	}
	volumeResp, err := v.getVolumeResponse(ctx, volume)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	logger.Ctx(ctx).Debugf("Got volume : %+v", volumeResp)
	c.JSON(http.StatusOK, volumeResp)
}

// @Summary patch a volume
// @Description patch a volume
// @tags Volume
// @Accept  json
// @Produce json
// @Param   message	body   VolumePatchPayload  true   "Volume patch payload"
// @Success 200 {object} VolumeResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /volumes/{id} [patch]
func (v *VolumeAPI) Patch(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	payload := &VolumePatchPayload{}
	// Keep the body: "instance": null (detach) and an omitted instance both bind to nil
	err := c.ShouldBindBodyWith(payload, binding.JSON)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to bind json: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	logger.Ctx(ctx).Debugf("Patching volume %s with %+v", uuID, payload)
	var fields map[string]json.RawMessage
	if raw, ok := c.Get(gin.BodyBytesKey); ok {
		_ = json.Unmarshal(raw.([]byte), &fields)
	}
	// nil keeps the attachment, 0 detaches, anything else attaches to that instance
	var instanceID *int64
	if _, set := fields["instance"]; set {
		id := int64(0)
		if payload.Instance != nil {
			SetAuditAction(c, "volume.attach")
			var instance *model.Instance
			instance, err = instanceAdmin.GetInstanceByUUID(ctx, payload.Instance.ID)
			if err != nil {
				logger.Ctx(ctx).Errorf("Failed to get instance, %+v", err)
				ErrorResponse(c, http.StatusBadRequest, "Failed to get instance", err)
				return
			}
			id = instance.ID
		} else {
			SetAuditAction(c, "volume.detach")
		}
		instanceID = &id
	}

	volume, err := volumeAdmin.UpdateByUUID(ctx, uuID, payload.Name, instanceID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to update volume %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Failed to update volume", err)
		return
	}
	if volume, err = volumeAdmin.GetVolumeByUUID(ctx, volume.UUID); err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to update volume response", err)
		return
	}
	volumeResp, err := v.getVolumeResponse(ctx, volume)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to update volume response", err)
		return
	}
	logger.Ctx(ctx).Debugf("Patch volume successfully, %s, %+v", uuID, volumeResp)
	c.JSON(http.StatusOK, volumeResp)
}

// @Summary delete a volume
// @Description delete a volume. A volume written on a host is deleted by the host first: the request returns 202 and the volume stays "deleting" until the host confirms.
// @tags Volume
// @Accept  json
// @Produce json
// @Success 202
// @Success 204
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /volumes/{id} [delete]
func (v *VolumeAPI) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	logger.Ctx(ctx).Debugf("Deleting volume %s", uuID)
	deferred, err := volumeAdmin.DeleteVolumeByUUID(ctx, uuID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to delete volume %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Failed to delete volume", err)
		return
	}
	logger.Ctx(ctx).Debugf("Deleted volume %s successfully (deferred %t)", uuID, deferred)
	if deferred {
		c.JSON(http.StatusAccepted, nil)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// @Summary detach a volume of a lost pool by force
// @Description remove a volume whose storage pool was declared lost from its instance, without touching the file
// @tags Volume
// @Accept  json
// @Produce json
// @Success 200 {object} VolumeResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /volumes/{id}/force_detach [post]
func (v *VolumeAPI) ForceDetach(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	volume, err := volumeAdmin.GetVolumeByUUID(ctx, uuID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid volume query", err)
		return
	}
	if err = volumeAdmin.ForceDetach(ctx, volume); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to detach the volume", err)
		return
	}
	if volume, err = volumeAdmin.GetVolumeByUUID(ctx, uuID); err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	volumeResp, err := v.getVolumeResponse(ctx, volume)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, volumeResp)
}

// @Summary create a volume
// @Description create a volume
// @tags Volume
// @Accept  json
// @Produce json
// @Param   message	body   VolumePayload  true   "Volume create payload"
// @Success 200 {object} VolumeResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /volumes [post]
func (v *VolumeAPI) Create(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &VolumePayload{}
	err := c.ShouldBindJSON(payload)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to bind json: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	logger.Ctx(ctx).Debugf("Creating volume with %+v", payload)
	pool, err := storagePoolAdmin.Resolve(ctx, payload.StoragePool)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", err)
		return
	}
	volume, err := volumeAdmin.Create(ctx, payload.Name, payload.Size, pool)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to create volume: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Failed to create volume", err)
		return
	}
	volumeResp, err := v.getVolumeResponse(ctx, volume)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to create volume response", err)
		return
	}
	logger.Ctx(ctx).Debugf("Created volume successfully, %+v", volumeResp)
	c.JSON(http.StatusOK, volumeResp)
}

// @Summary list volumes
// @Description list volumes
// @tags Volume
// @Accept  json
// @Produce json
// @Success 200 {object} VolumeListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /volumes [get]
func (v *VolumeAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "50")
	orderStr := c.DefaultQuery("order", "-created_at")
	nameStr := c.DefaultQuery("name", "")

	// type: all, data, boot
	// default data
	typeStr := c.DefaultQuery("type", "data")
	logger.Ctx(ctx).Debugf("List volumes, offset:%s, limit:%s, name:%s, type:%s", offsetStr, limitStr, nameStr, typeStr)
	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		logger.Ctx(ctx).Errorf("Invalid query offset: %s, %+v", offsetStr, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset: "+offsetStr, err)
		return
	}
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		logger.Ctx(ctx).Errorf("Invalid query limit: %s, %+v", limitStr, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query limit: "+limitStr, err)
		return
	}
	if offset < 0 || limit < 0 {
		errStr := "Invalid query offset or limit, cannot be negative"
		logger.Ctx(ctx).Errorf(errStr)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset or limit", errors.New(errStr))
		return
	}
	total, volumes, err := volumeAdmin.ListVolume(ctx, int64(offset), int64(limit), orderStr, nameStr, typeStr)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to list volumes, %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Failed to list volumes", err)
		return
	}
	volumeListResp := &VolumeListResponse{
		Offset: offset,
		Total:  int(total),
		Limit:  len(volumes),
	}
	volumeList := make([]*VolumeResponse, volumeListResp.Limit)
	for i, volume := range volumes {
		volumeResp, err := v.getVolumeResponse(ctx, volume)
		if err != nil {
			ErrorResponse(c, http.StatusInternalServerError, "Failed to list volume response", err)
			return
		}
		volumeList[i] = volumeResp
	}

	volumeListResp.Volumes = volumeList
	logger.Ctx(ctx).Debugf("List volumes successfully, %+v", volumeListResp)
	c.JSON(http.StatusOK, volumeListResp)
}

// @Summary resize a volume
// @Description resize a volume
// @tags Volume
// @Accept  json
// @Produce json
// @Param   message	body   VolumeResizePayload  true   "Volume resize payload"
// @Success 200
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /volumes/{id}/resize [post]
func (v *VolumeAPI) Resize(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	volume, err := volumeAdmin.GetVolumeByUUID(ctx, uuID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get volume by uuid: %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid volume query", err)
		return
	}
	payload := &VolumeResizePayload{}
	err = c.ShouldBindJSON(payload)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to bind json: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	logger.Ctx(ctx).Debugf("Resizing volume %s with size %d", uuID, payload.Size)
	err = volumeAdmin.Resize(ctx, volume, payload.Size)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to resize volume %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Failed to resize volume", err)
		return
	}
	c.JSON(http.StatusOK, nil)
}

func (v *VolumeAPI) getVolumeResponse(ctx context.Context, volume *model.Volume) (*VolumeResponse, error) {
	owner := orgAdmin.GetOrgName(ctx, volume.Owner)
	volumeResp := &VolumeResponse{
		ResourceReference: &ResourceReference{
			ID:        volume.UUID,
			Name:      volume.Name,
			Owner:     owner,
			OwnerUUID: orgAdmin.GetOrgUUID(ctx, volume.Owner),
			CreatedAt: volume.CreatedAt.Format(TimeStringForMat),
			UpdatedAt: volume.UpdatedAt.Format(TimeStringForMat),
		},
		Path:    volume.Path,
		Size:    volume.Size,
		Format:  volume.Format,
		Status:  volume.Status.String(),
		Reason:  volume.Reason,
		Target:  volume.Target,
		Href:    volume.Href,
		Booting: volume.Booting,
	}
	if volume.Instance != nil {
		volumeResp.Instance = &BaseReference{
			ID:   volume.Instance.UUID,
			Name: volume.Instance.Hostname,
		}
	}
	if pool, err := services.VolumePool(ctx, volume); err == nil {
		volumeResp.StoragePool = &ResourceReference{ID: pool.UUID, Name: pool.Name}
	}
	if volume.Hyper > 0 && GetMemberShip(ctx).IsSystemAdmin() {
		if hyper, err := hyperAdmin.GetHyperByHostid(ctx, volume.Hyper); err == nil {
			volumeResp.Hyper = &ResourceReference{ID: hyper.UUID, Name: hyper.Hostname}
		}
	}
	return volumeResp, nil
}
