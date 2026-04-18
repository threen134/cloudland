/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"net/http"
	"strconv"

	"api/src/services"

	"github.com/gin-gonic/gin"
)

// UploadCapture 接收 compute 节点 capture_image.sh 流式上传的镜像数据，转发到 S3/MinIO
//
// Route:   POST /api/v1/internal/images/:id/upload
// Header:  X-Capture-Token: hex(HMAC-SHA256(SCI_SHARED_SECRET, "<image_id>|<expiry>"))
// Query:   expiry=<unix_ts>
// Body:    application/octet-stream 镜像字节流（clapi 用 minio-go multipart，上限 ~640GB）
//
// 鉴权：纯 HMAC，不经 JWT。必须挂在 authGroup 之外。
func (v *ImageAPI) UploadCapture(c *gin.Context) {
	imageID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || imageID <= 0 {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	expiry, err := strconv.ParseInt(c.Query("expiry"), 10, 64)
	if err != nil || expiry <= 0 {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	token := c.GetHeader("X-Capture-Token")
	if token == "" {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	if !services.VerifyCaptureToken(token, imageID, expiry) {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	if !services.S3Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "S3 not enabled"})
		return
	}

	image, err := services.ImageAdmin.GetImageByIDInternal(imageID)
	if err != nil {
		logger.Errorf("UploadCapture: image %d not found: %v", imageID, err)
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	ctx := c.Request.Context()
	objectName := services.S3ObjectNameFor(image)

	if err := services.S3PutObject(ctx, objectName, c.Request.Body); err != nil {
		logger.Errorf("UploadCapture: S3PutObject failed for image %d (%s): %v", imageID, objectName, err)
		services.ImageAdmin.MarkImageError(imageID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	info, err := services.S3DetectImage(ctx, objectName)
	if err != nil || info == nil {
		logger.Errorf("UploadCapture: detect format failed for image %d (%s): %v", imageID, objectName, err)
		services.ImageAdmin.MarkImageError(imageID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "detect format failed"})
		return
	}
	services.ImageAdmin.MarkImageAvailable(imageID, info.Format, info.VirtualSize)
	c.JSON(http.StatusOK, gin.H{"ok": true, "format": info.Format, "virtual_size": info.VirtualSize})
}
