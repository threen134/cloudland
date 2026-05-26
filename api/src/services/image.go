/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
	"api/src/utils"
)

var ImageAdmin = &ImageAdminService{}

type ImageAdminService struct{}

func FileExist(filename string) bool {
	_, err := os.Lstat(filename)
	return !os.IsNotExist(err)
}
func (a *ImageAdminService) Create(ctx context.Context, osCode, name, osVersion, virtType, userName, url, architecture, bootLoader string, isRescue bool, instID int64, uuid string, rescueImage *model.Image, osFamily string) (image *model.Image, err error) {
	logger.Infof("ENTER ImageAdmin.Create: name=%s, osCode=%s, osVersion=%s", name, osCode, osVersion)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.Create: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.Create: success")
		}
	}()
	logger.Debugf("Creating image %s %s %s %s %s %s %s %s %t %d %s %s", osCode, name, osVersion, virtType, userName, url, architecture, bootLoader, isRescue, instID, uuid, osFamily)
	memberShip := GetMemberShip(ctx)
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	var instance *model.Instance
	if instID > 0 {
		instance = &model.Instance{Model: model.Model{ID: instID}}
		err = db.Preload("Image").Preload("Volumes").Take(instance).Error
		if err != nil {
			logger.Error("DB failed to query instance", err)
			return nil, NewCLError(ErrInstanceNotFound, "Instance not found", err)
		}
		if instance.Status != model.InstanceStatusShutoff {
			err = fmt.Errorf("instance [%s] is running, shut it down first before capturing", instance.Hostname)
			logger.Error(err)
			return nil, NewCLError(ErrInstanceInvalidState, "Instance is running", err)
		}
		image = instance.Image.Clone()
		image.Model = model.Model{Creater: memberShip.UserID}
		image.Owner = memberShip.OrgID
		image.Name = name
		image.Status = "creating"
		image.CaptureFromInstanceID = instance.ID
		image.CaptureFromInstance = instance
		image.OsFamily = osFamily
	} else {
		image = &model.Image{
			Model:        model.Model{Creater: memberShip.UserID},
			Owner:        memberShip.OrgID,
			OsVersion:    osVersion,
			VirtType:     virtType,
			UserName:     userName,
			Name:         name,
			OSCode:       osCode,
			Status:       "creating",
			Architecture: architecture,
			BootLoader:   bootLoader,
			OsFamily:     osFamily,
		}
		if uuid != "" {
			logger.Debugf("Creating image with UUID %s", uuid)
			image.UUID = uuid
		}
	}
	// Set visibility based on creator identity: SystemAdmin → public, others → private
	// Must be set after both branches (upload and capture) to override Clone() inheritance
	if memberShip.IsSystemAdmin() {
		image.Visibility = model.ImageVisibilityPublic
	} else {
		image.Visibility = model.ImageVisibilityPrivate
	}
	image.QAEnabled = true
	image.IsRescue = isRescue
	if rescueImage != nil {
		image.RescueImage = rescueImage.ID
	}
	image.StorageType = GetVolumeDriver()
	logger.Debugf("Creating image %+v", image)
	err = db.Create(image).Error
	if err != nil {
		logger.Error("DB create image failed, %v", err)
		return nil, NewCLError(ErrImageCreateFailed, "Failed to create image record", err)
	}

	// create default storage
	defaultPool := viper.GetString("volume.default_wds_pool_id")
	storageID := int64(0)
	if defaultPool != "" {
		storage := &model.ImageStorage{
			ImageID: image.ID,
			Image:   image,
			PoolID:  defaultPool,
			Status:  model.StorageStatusUnknown,
		}
		if err = db.Create(storage).Error; err != nil {
			logger.Error("Failed to create default image storage", err)
			return nil, NewCLError(ErrImageStorageCreateFailed, "Failed to create default image storage", err)
		}
		storageID = storage.ID
	}

	// create with default pool id
	prefix := strings.Split(image.UUID, "-")[0]

	// 非 WDS 且 S3 已启用：非 capture 走 Go 直传 MinIO；capture 仍经 SCI 但附带上传 URL
	// WDS 路径（defaultPool != ""）保持原 shell 行为
	if defaultPool == "" && S3Enabled() {
		if instID == 0 {
			// 普通上传：在独立 goroutine 里异步拉远端 URL → PutObject，立即返回
			imageID := image.ID
			uploadCtx, cancel := context.WithTimeout(context.Background(), S3UploadTimeout())
			go func() {
				defer cancel()
				a.uploadImageToS3(uploadCtx, imageID, url)
			}()
			return
		}
		// capture：派发给该 VM 所在 hyper 执行脚本，脚本完成后 POST 回 clapi
		bootVolumeUUID := ""
		if instance.Volumes != nil {
			for _, volume := range instance.Volumes {
				if volume.Booting {
					bootVolumeUUID = volume.GetOriginVolumeID()
					break
				}
			}
		}
		token, expiry := GenerateCaptureToken(image.ID)
		uploadURL := BuildCaptureUploadURL(image.ID, expiry)
		if uploadURL == "" {
			logger.Error("capture: clapi.internal_url 未配置，无法派发 capture 上传")
			err = NewCLError(ErrImageCreateFailed, "clapi internal URL not configured for capture", nil)
			return
		}
		control := fmt.Sprintf("inter=%d", instance.Hyper)
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/capture_image.sh '%d' '%s' '%d' '%s' '%d' '%s' '%s'",
			image.ID, prefix, instance.ID, bootVolumeUUID, storageID, uploadURL, token)
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Capture image command execution failed", err)
			return
		}
		return
	}

	// WDS 路径 / legacy 本地路径：保持原 shell 派发
	control := "inter="
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/create_image.sh '%d' '%s' '%s' '%d'", image.ID, prefix, url, storageID)
	if instID > 0 {
		bootVolumeUUID := ""
		if instance.Volumes != nil {
			for _, volume := range instance.Volumes {
				if volume.Booting {
					bootVolumeUUID = volume.GetOriginVolumeID()
					break
				}
			}
		}
		control = fmt.Sprintf("inter=%d", instance.Hyper)
		command = fmt.Sprintf("/opt/cloudland/scripts/backend/capture_image.sh '%d' '%s' '%d' '%s' '%d'", image.ID, prefix, instance.ID, bootVolumeUUID, storageID)
	}
	err = HyperExecute(ctx, control, command)
	if err != nil {
		logger.Error("Create image command execution failed", err)
		return
	}
	return
}

// getImageByID 走全局 dbs.DB()，不依赖 request ctx（供 goroutine 使用）
func (a *ImageAdminService) getImageByID(id int64) (*model.Image, error) {
	img := &model.Image{}
	if err := dbs.DB().Where("id = ?", id).Take(img).Error; err != nil {
		return nil, err
	}
	return img, nil
}

func (a *ImageAdminService) updateImageState(id int64, state string) {
	if err := dbs.DB().Model(&model.Image{}).Where("id = ?", id).Update("status", state).Error; err != nil {
		logger.Errorf("updateImageState: failed id=%d state=%s: %v", id, state, err)
	}
}

func (a *ImageAdminService) updateImageComplete(id int64, format string, virtualSize uint64) {
	updates := map[string]interface{}{
		"status": "available",
		"format": format,
		"size":   int64(virtualSize),
	}
	if err := dbs.DB().Model(&model.Image{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		logger.Errorf("updateImageComplete: failed id=%d: %v", id, err)
	}
}

// uploadImageToS3 在 goroutine 中执行：拉远端 URL → 流式写入 S3 → 检测格式 → 更新 DB
// ctx 基于 context.Background 派生，由 S3UploadTimeout() 驱动取消
func (a *ImageAdminService) uploadImageToS3(ctx context.Context, imageID int64, url string) {
	image, err := a.getImageByID(imageID)
	if err != nil {
		logger.Errorf("S3 upload: failed to load image %d: %v", imageID, err)
		return
	}
	objectName := s3ObjectName(image)
	// 上传期间保持 image.Status = "creating"（DB 记录建立时已写入），
	// 完成后 updateImageComplete 转 "available"，失败走 updateImageState("error")

	// HTTP client 不设整体 Timeout（覆盖 ctx），只设建连/TLS/响应头超时
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logger.Errorf("S3 upload: bad url %s: %v", url, err)
		a.updateImageState(imageID, "error")
		return
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Errorf("S3 upload: failed to fetch %s: %v", url, err)
		a.updateImageState(imageID, "error")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Errorf("S3 upload: fetch %s returned HTTP %d", url, resp.StatusCode)
		a.updateImageState(imageID, "error")
		return
	}

	if err := S3PutObject(ctx, objectName, resp.Body); err != nil {
		// 残缺对象清理（未完成 multipart 由 bucket lifecycle 兜底）
		_ = S3RemoveObject(context.Background(), objectName)
		logger.Errorf("S3 upload: PutObject failed for image %d: %v", imageID, err)
		a.updateImageState(imageID, "error")
		return
	}

	info, err := S3DetectImage(ctx, objectName)
	if err != nil || info == nil || (info.Format != "qcow2" && info.Format != "raw") {
		// 检测失败：S3 里已落盘的对象必须清掉，否则会孤立（Delete 走 Status 门控也碰不到）
		_ = S3RemoveObject(context.Background(), objectName)
		logger.Errorf("S3 upload: format detection failed for image %d: %v", imageID, err)
		a.updateImageState(imageID, "error")
		return
	}
	a.updateImageComplete(imageID, info.Format, info.VirtualSize)
	logger.Infof("S3 upload: image %d (%s, %d bytes) uploaded as %s", imageID, info.Format, info.VirtualSize, objectName)

	// 上传期间用户可能已删除 image 记录 —— 复查一次，已删则清对象，避免 S3 孤儿
	if _, err := a.getImageByID(imageID); err != nil {
		logger.Warningf("S3 upload: image %d vanished during upload, removing S3 object %s", imageID, objectName)
		_ = S3RemoveObject(context.Background(), objectName)
	}
}

// GetImageByIDInternal 供内部 handler（如 UploadCapture）无鉴权按 ID 加载 image
// 不走 ctx，不做权限校验（调用方用 HMAC token 自证）
func (a *ImageAdminService) GetImageByIDInternal(id int64) (*model.Image, error) {
	return a.getImageByID(id)
}

// MarkImageError 把镜像状态置为 error（供异步/内部路径更新）
func (a *ImageAdminService) MarkImageError(id int64) {
	a.updateImageState(id, "error")
}

// MarkImageAvailable 把镜像状态置为 available 并写入 format/size
func (a *ImageAdminService) MarkImageAvailable(id int64, format string, virtualSize uint64) {
	a.updateImageComplete(id, format, virtualSize)
}

// S3ObjectNameFor 暴露 object 命名规则给其他包（如 apis）
func S3ObjectNameFor(image *model.Image) string {
	return s3ObjectName(image)
}

// BuildImageDownloadURLParam 供 launch/rescue/reinstall dispatch 生成 base64 编码的 presigned URL 参数
// S3 未启用或未就绪时返回空串
func BuildImageDownloadURLParam(ctx context.Context, image *model.Image) string {
	if !S3Enabled() || image == nil || image.Status != "available" {
		return ""
	}
	u, err := GenerateDownloadURL(ctx, image)
	if err != nil {
		logger.Errorf("BuildImageDownloadURLParam: presign failed for image %d: %v", image.ID, err)
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(u))
}

func (a *ImageAdminService) GetImageByUUID(ctx context.Context, uuID string) (image *model.Image, err error) {
	logger.Infof("ENTER ImageAdmin.GetImageByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.GetImageByUUID: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.GetImageByUUID: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	image = &model.Image{}
	err = db.Where("uuid = ?", uuID).Take(image).Error
	if err != nil {
		logger.Error("Failed to query image, %v", err)
		return nil, NewCLError(ErrImageNotFound, "Image not found", err)
	}
	memberShip := GetMemberShip(ctx)
	// Public images are accessible to all authenticated users; private images require org ownership
	if image.Visibility != model.ImageVisibilityPublic {
		permit := memberShip.CheckResourceOrg(model.OrgReader, image.Owner)
		if !permit {
			logger.Error("Not authorized to get image")
			err = NewCLError(ErrPermissionDenied, "Not authorized to get image", nil)
			return
		}
	}
	return
}

// GetImageByName returns an image by name.
// Note: name is not globally unique. If multiple orgs have public images with the same name,
// the result is non-deterministic. Internal RPC callbacks may use this; external callers should prefer UUID.
func (a *ImageAdminService) GetImageByName(ctx context.Context, name string) (image *model.Image, err error) {
	logger.Infof("ENTER ImageAdmin.GetImageByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.GetImageByName: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.GetImageByName: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	image = &model.Image{}
	err = db.Where("name = ?", name).Take(image).Error
	if err != nil {
		logger.Error("Failed to query image, %v", err)
		return nil, NewCLError(ErrImageNotFound, "Image not found", err)
	}
	memberShip := GetMemberShip(ctx)
	if image.Visibility != model.ImageVisibilityPublic {
		permit := memberShip.CheckResourceOrg(model.OrgReader, image.Owner)
		if !permit {
			logger.Error("Not authorized to get image")
			err = NewCLError(ErrPermissionDenied, "Not authorized to get image", nil)
			return
		}
	}
	return
}

func (a *ImageAdminService) Get(ctx context.Context, id int64) (image *model.Image, err error) {
	logger.Infof("ENTER ImageAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.Get: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.Get: success")
		}
	}()
	if id <= 0 {
		err = NewCLError(ErrInvalidParameter, "Invalid image ID", nil)
		logger.Error(err)
		return
	}
	ctx, db := GetContextDB(ctx)
	image = &model.Image{Model: model.Model{ID: id}}
	err = db.Take(image).Error
	if err != nil {
		logger.Error("DB failed to query image, %v", err)
		return nil, NewCLError(ErrImageNotFound, "Image not found", err)
	}
	memberShip := GetMemberShip(ctx)
	if image.Visibility != model.ImageVisibilityPublic {
		permit := memberShip.CheckResourceOrg(model.OrgReader, image.Owner)
		if !permit {
			logger.Error("Not authorized to get image")
			err = NewCLError(ErrPermissionDenied, "Not authorized to get image", nil)
			return
		}
	}
	return
}

func (a *ImageAdminService) GetImage(ctx context.Context, reference *BaseReference) (image *model.Image, err error) {
	logger.Infof("ENTER ImageAdmin.GetImage: reference=%+v", reference)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.GetImage: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.GetImage: success")
		}
	}()
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		err = NewCLError(ErrInvalidParameter, "Image base reference must be provided with either uuid or name", nil)
		return
	}
	if reference.ID != "" {
		image, err = a.GetImageByUUID(ctx, reference.ID)
		return
	}
	if reference.Name != "" {
		image, err = a.GetImageByName(ctx, reference.Name)
		return
	}
	return
}

func (a *ImageAdminService) Delete(ctx context.Context, image *model.Image) (err error) {
	logger.Infof("ENTER ImageAdmin.Delete: id=%d, uuid=%s", image.ID, image.UUID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.Delete: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckResourceOrg(model.OrgWriter, image.Owner)
	if !permit {
		logger.Error("Not authorized to delete image")
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete image", nil)
		return
	}
	refCount := 0
	err = db.Model(&model.Instance{}).Where("image_id = ?", image.ID).Count(&refCount).Error
	if err != nil {
		logger.Error("Failed to count the number of instances using the image", err)
		return NewCLError(ErrSQLSyntaxError, "Failed to count instances using the image", err)
	}
	if refCount > 0 {
		logger.Error("Image can not be deleted if there are instances using it")
		err = NewCLError(ErrImageInUse, "The image can not be deleted if there are instances using it", nil)
		return
	}
	prefix := strings.Split(image.UUID, "-")[0]
	control := "inter=0"
	total, storages, _ := imageStorageAdmin.List(0, -1, "", image, "")

	// S3 对象清理不受 Status 门控：覆盖 creating/error 状态下已落盘对象的孤儿场景
	// （uploadImageToS3 detect-fail、UploadCapture 检测失败、goroutine-delete 竞态等）
	// RemoveObject 对不存在对象幂等，空跑无害
	if total == 0 && S3Enabled() {
		if rmErr := S3RemoveObject(ctx, s3ObjectName(image)); rmErr != nil {
			logger.Errorf("S3 delete: RemoveObject failed for image %d: %v", image.ID, rmErr)
			// 不阻塞 DB 记录删除 —— S3 侧可由 lifecycle 规则兜底
		}
	}

	if image.Status == "available" {
		if total > 0 {
			// WDS 路径：通过 SCI 派发 clear_image.sh 清理各 storage pool 的卷
			for _, storage := range storages {
				if storage.Status != model.StorageStatusSynced {
					continue
				}
				command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_image.sh '%d' '%s' '%s' '%s'", image.ID, prefix, image.Format, storage.VolumeID)
				err = HyperExecute(ctx, control, command)
				if err != nil {
					logger.Error("Clear image storage command execution failed", err)
					return
				}
			}
		} else if !S3Enabled() {
			// legacy 本地模式：走原 SCI + shell（S3 分支已在上面处理）
			command := fmt.Sprintf("/opt/cloudland/scripts/backend/clear_image.sh '%d' '%s' '%s' %s'", image.ID, prefix, image.Format, "")
			err = HyperExecute(ctx, control, command)
			if err != nil {
				logger.Error("Clear image command execution failed", err)
				return
			}
		}
	}
	if err = db.Delete(image).Error; err != nil {
		return NewCLError(ErrImageDeleteFailed, "Failed to delete image record", err)
	}
	if err = db.Where("image_id = ?", image.ID).Delete(&model.ImageStorage{}).Error; err != nil {
		return NewCLError(ErrImageStorageDeleteFailed, "Failed to delete image storage records", err)
	}
	return
}

func (a *ImageAdminService) List(ctx context.Context, offset, limit int64, order, query, visibility string) (total int64, images []*model.Image, err error) {
	logger.Infof("ENTER ImageAdmin.List: offset=%d, limit=%d, order=%s, query=%s, visibility=%s", offset, limit, order, query, visibility)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.List: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.List: success")
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	if limit == 0 {
		limit = 16
	}

	if order == "" {
		order = "created_at"
	}

	// Build base query with parameterized name search (fix SQL injection)
	baseQuery := db.Model(&model.Image{})
	if query != "" {
		baseQuery = baseQuery.Where("name LIKE ?", "%"+query+"%")
	}

	// Apply owner/visibility filtering
	if !memberShip.IsSystemAdmin() {
		// Normal user: see own org images + public images, with optional visibility filter
		switch visibility {
		case model.ImageVisibilityPublic:
			baseQuery = baseQuery.Where("visibility = ?", model.ImageVisibilityPublic)
		case model.ImageVisibilityPrivate:
			baseQuery = baseQuery.Where("owner = ? AND visibility = ?", memberShip.OrgID, model.ImageVisibilityPrivate)
		default:
			// "all" or empty: own + public
			baseQuery = baseQuery.Where("owner = ? OR visibility = ?", memberShip.OrgID, model.ImageVisibilityPublic)
		}
	} else {
		// SystemAdmin: optional visibility filter, no owner restriction
		switch visibility {
		case model.ImageVisibilityPublic:
			baseQuery = baseQuery.Where("visibility = ?", model.ImageVisibilityPublic)
		case model.ImageVisibilityPrivate:
			baseQuery = baseQuery.Where("visibility = ?", model.ImageVisibilityPrivate)
		}
	}

	images = []*model.Image{}
	if err = baseQuery.Count(&total).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to count images", err)
	}
	sortedQuery := dbs.Sortby(baseQuery.Offset(offset).Limit(limit), order)
	if err = sortedQuery.Find(&images).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to find images", err)
	}

	return
}

func (a *ImageAdminService) Update(ctx context.Context, image *model.Image, osCode, name, osVersion, userName string, pools []string, osFamily, uuid string, public *bool) (err error) {
	logger.Infof("ENTER ImageAdmin.Update: id=%d, name=%s, osCode=%s", image.ID, name, osCode)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT ImageAdmin.Update: error=%v", err)
		} else {
			logger.Info("EXIT ImageAdmin.Update: success")
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	memberShip := GetMemberShip(ctx)
	permit := memberShip.CheckSystemPermission()
	if !permit {
		logger.Error("Not authorized to update image")
		err = NewCLError(ErrPermissionDenied, "Not authorized to update image", nil)
		return
	}
	if osCode != "" {
		image.OSCode = osCode
	}
	if name != "" {
		image.Name = name
	}
	if osVersion != "" {
		image.OsVersion = osVersion
	}
	if userName != "" {
		image.UserName = userName
	}

	if uuid != "" {
		if !utils.IsUUID(uuid) {
			logger.Error("Invalid UUID format")
			err = NewCLError(ErrInvalidParameter, "Invalid UUID format", nil)
			return
		}
		image.UUID = uuid
	}

	if osFamily != "" {
		image.OsFamily = osFamily
	}

	if public != nil {
		if *public {
			image.Visibility = model.ImageVisibilityPublic
		} else {
			image.Visibility = model.ImageVisibilityPrivate
		}
	}

	if image.Status != "available" {
		logger.Error("Image status is not available, cannot update")
		err = NewCLError(ErrImageNotAvailable, "Image status is not available, cannot update", nil)
		return
	}

	err = db.Model(&model.Image{}).Where("id = ?", image.ID).Updates(map[string]interface{}{
		"name": image.Name, "os_version": image.OsVersion, "user_name": image.UserName,
		"uuid": image.UUID, "os_family": image.OsFamily, "visibility": image.Visibility,
	}).Error
	if err != nil {
		logger.Error("Failed to save image", err)
		return NewCLError(ErrImageUpdateFailed, "Failed to save image", err)
	}

	driver := GetVolumeDriver()
	if driver == "local" {
		return
	}

	defaultPoolID := viper.GetString("volume.default_wds_pool_id")
	storages, err := imageStorageAdmin.InitStorages(ctx, image, pools)
	if err != nil {
		logger.Error("Failed to initialize image storages", err)
		return
	}

	logger.Debugf("Image %s storages: %+v", image.UUID, storages)

	sourceVolumeID := ""
	for _, storage := range storages {
		if storage.PoolID == defaultPoolID {
			sourceVolumeID = storage.VolumeID
			break
		}
	}
	if sourceVolumeID == "" {
		logger.Error("Source volume ID not found for image")
		return NewCLError(ErrImageStorageNotFound, "Source volume ID not found for image", err)
	}
	for _, storage := range storages {
		// ignore already synced or syncing storages
		if storage.Status == model.StorageStatusSynced || storage.Status == model.StorageStatusSyncing {
			logger.Debugf("Image %s storage %s is already synced or syncing, skipping", image.UUID, storage.PoolID)
			continue
		}
		prefix := strings.Split(image.UUID, "-")[0]
		control := "inter="
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/clone_image.sh '%d' '%s' '%s' '%d' '%s'", image.ID, prefix, storage.PoolID, storage.ID, sourceVolumeID)
		if storage.PoolID == defaultPoolID {
			command = fmt.Sprintf("/opt/cloudland/scripts/backend/sync_image_info.sh '%d' '%s' '%s' '%d'", image.ID, prefix, storage.PoolID, storage.ID)
		}
		storage.Status = model.StorageStatusSyncing
		if err = db.Model(&model.ImageStorage{}).Where("id = ?", storage.ID).Updates(map[string]interface{}{"status": storage.Status}).Error; err != nil {
			logger.Error("Failed to update image storage status", err)
			return NewCLError(ErrImageStorageUpdateFailed, "Failed to update image storage status", err)
		}
		err = HyperExecute(ctx, control, command)
		if err != nil {
			logger.Error("Sync remote info command execution failed", err)
		}
	}
	return
}

