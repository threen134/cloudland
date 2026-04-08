package apis

import (
	"net/http"

	"api/src/model"
	"api/src/services"

	. "api/src/common"

	"github.com/gin-gonic/gin"
)

// SystemSettingAPI 系统设置同步接口
type SystemSettingAPI struct{}

var systemSettingAPI = &SystemSettingAPI{}

// SyncSystemSettings 接收 CPGateway 推送的系统设置同步请求（全量替换 + 版本校验）
// @Summary Sync system settings
// @Description Internal endpoint for CPGateway to push system settings (full sync with version check)
// @Tags SystemSettings
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{} "Sync successful"
// @Failure 400 {object} map[string]interface{} "Bad request"
// @Failure 500 {object} map[string]interface{} "Internal server error"
// @Router /internal/system-settings/sync [post]
func (a *SystemSettingAPI) SyncSystemSettings(c *gin.Context) {
	var req struct {
		ConfigVersion int64                        `json:"config_version"`
		Force         bool                         `json:"force"`
		Settings      []model.SystemSettingMirror  `json:"settings" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	ctx, db := GetContextDB(ctx)

	// 乱序保护：只接受 version > local_version 的增量同步。
	// force=true 或 version==0 时跳过版本检查（Provisioning / 心跳恢复全量对账）。
	var localVer model.SystemSettingMirrorVersion
	db.Where("id = ?", 1).First(&localVer)
	if !req.Force && req.ConfigVersion > 0 && req.ConfigVersion <= localVer.Version {
		logger.Warningf("Received stale or duplicate system settings sync (version=%d <= local=%d), ignoring",
			req.ConfigVersion, localVer.Version)
		c.JSON(http.StatusOK, gin.H{"status": "ignored", "reason": "stale_or_duplicate_version"})
		return
	}

	// 全量替换：在事务内清空旧镜像后批量写入新数据
	tx := db.Begin()
	if err := tx.Exec("DELETE FROM system_setting_mirror").Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear settings mirror: " + err.Error()})
		return
	}

	// 批量插入（比循环单条 Save 效率高）
	if len(req.Settings) > 0 {
		if err := tx.Create(&req.Settings).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to insert settings: " + err.Error()})
			return
		}
	}

	// 更新本地版本号
	if localVer.ID == 0 {
		localVer.ID = 1
		localVer.Version = req.ConfigVersion
		tx.Create(&localVer)
	} else {
		tx.Model(&localVer).Update("version", req.ConfigVersion)
	}

	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed: " + err.Error()})
		return
	}

	logger.Infof("System settings synced: %d settings, version=%d", len(req.Settings), req.ConfigVersion)

	// 异步应用 DNS 上游配置（不阻塞 HTTP 响应）
	go services.ApplyDnsUpstream()

	c.JSON(http.StatusOK, gin.H{"status": "ok", "synced": len(req.Settings), "version": req.ConfigVersion})
}
