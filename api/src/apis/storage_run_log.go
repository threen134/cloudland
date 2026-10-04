/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"io"
	"net/http"
	"strconv"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

type StorageRunLogResponse struct {
	// requested: the host was asked for it, ask again in a moment; ready: content holds it (with a message: the host is
	// offline and this is the copy fetched at updated_at); error: see message
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	// Size of the log on the host; when larger than the content, only its end was kept
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// @Summary whole log of a storage task run
// @Description The callback of a run only brings the last 64 KiB of its log. The first call asks the host for the whole log and answers 202; call again until the answer is 200 with the log (its last 16 MiB). A run that is still running is fetched again when the copy is older than 30 s.
// @tags StorageCluster
// @Produce json
// @Param   id   path  string  true  "Task UUID"
// @Param   run  path  int     true  "Run ID"
// @Success 200 {object} StorageRunLogResponse
// @Success 202 {object} StorageRunLogResponse
// @Failure 404 {object} common.APIError "No such task or run"
// @Failure 409 {object} common.APIError "The host is offline, or the hosts can not upload"
// @Router /storage_tasks/{id}/runs/{run}/log [get]
func (v *StorageClusterAPI) RunLog(c *gin.Context) {
	runID, err := strconv.ParseInt(c.Param("run"), 10, 64)
	if err != nil || runID <= 0 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid run", err)
		return
	}
	log, err := storageClusterAdmin.RunLog(c.Request.Context(), c.Param("id"), runID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to get the log", err)
		return
	}
	resp := &StorageRunLogResponse{Status: log.Status, Message: log.Message, Size: log.Size, Content: log.Content,
		Truncated: log.Size > int64(len(log.Content))}
	if log.Status == model.StorageRunLogRequested {
		c.JSON(http.StatusAccepted, resp)
		return
	}
	resp.UpdatedAt = log.UpdatedAt.Format(TimeStringForMat)
	c.JSON(http.StatusOK, resp)
}

// UploadStorageRunLog takes the log of a run from its host (stc_upload_log.sh)
//
// Route:  POST /api/v1/internal/storage_runs/:id/log?expiry=<unix>&size=<bytes on the host>
// Header: X-Log-Token: hex(HMAC-SHA256(CAPTURE_UPLOAD_SECRET, "log|<run ID>|<expiry>"))
// Body:   the log, its last 16 MiB at most
//
// No JWT: the hosts have none. Outside the authorized group, like the capture upload
func (v *StorageClusterAPI) UploadRunLog(c *gin.Context) {
	runID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || runID <= 0 {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	expiry, err := strconv.ParseInt(c.Query("expiry"), 10, 64)
	if err != nil || expiry <= 0 {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !services.VerifyStorageRunLogToken(c.GetHeader("X-Log-Token"), runID, expiry) {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	size, _ := strconv.ParseInt(c.Query("size"), 10, 64)
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, services.StorageRunLogMax+1))
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if len(body) > services.StorageRunLogMax {
		c.AbortWithStatus(http.StatusRequestEntityTooLarge)
		return
	}
	ctx := c.Request.Context()
	if err := services.SaveStorageRunLog(ctx, runID, body, size); err != nil {
		logger.Ctx(ctx).Errorf("UploadRunLog: run %d: %v", runID, err)
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
