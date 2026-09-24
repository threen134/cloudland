/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"context"
	"net/http"
	"strconv"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var migrationAPI = &MigrationAPI{}
var migrationAdmin = &services.MigrationAdmin{}

type MigrationAPI struct{}

type TaskResponse struct {
	Source  string `json:"source"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type MigrationResponse struct {
	*ResourceReference
	Instance    *InstanceInfo `json:"instance"`
	SourceHyper int32         `json:"source_hyper"`
	TargetHyper int32         `json:"target_hyper"`
	// 节点名称，便于界面直接展示；目标节点由调度器自选（-1）时为空
	SourceHyperName string `json:"source_hyper_name"`
	TargetHyperName string `json:"target_hyper_name"`
	// 发起迁移的用户名，创建时快照下来
	CreaterName string          `json:"creater_name"`
	CreaterUUID string          `json:"creater_uuid"`
	Force       bool            `json:"force"`
	Type        string          `json:"type"`
	Phases      []*TaskResponse `json:"phases"`
	Status      string          `json:"status"`
	// 迁移进度：百分比与已传输 / 总字节数（内存 + 本地磁盘合计），由源节点上报
	Progress    int32 `json:"progress"`
	Transferred int64 `json:"transferred"`
	Total       int64 `json:"total"`
	// Where every disk goes on the target, fixed once the target is known
	DiskPlan          []*DiskPlanResponse `json:"disk_plan"`
	AllowPoolFallback bool                `json:"allow_pool_fallback"`
	IgnoreCapacity    bool                `json:"ignore_capacity"`
}

// DiskPlanResponse is one disk of a migration plan
type DiskPlanResponse struct {
	Volume     *ResourceReference `json:"volume"`
	Device     string             `json:"device"`
	Booting    bool               `json:"booting"`
	SizeGB     int32              `json:"size_gb"`
	SourcePool *ResourceReference `json:"source_pool"`
	TargetPool *ResourceReference `json:"target_pool"`
	Auto       bool               `json:"auto"`
	Reason     string             `json:"reason,omitempty"`
}

type MigrationListResponse struct {
	Offset     int                  `json:"offset"`
	Total      int                  `json:"total"`
	Limit      int                  `json:"limit"`
	Migrations []*MigrationResponse `json:"migrations"`
}

type MigrationPayload struct {
	Name        string    `json:"name" binding:"required,min=2,max=32"`
	Instances   []*BaseID `json:"instances" binding:"required,gte=1"`
	Force       bool      `json:"force" binding:"omitempty"`
	TargetHyper *int32    `json:"target_hyper" binding:"omitempty,gte=0,lte=65535"`
	// Target pool per disk; needs target_hyper and a single instance
	Disks []*MigrationDiskPayload `json:"disks" binding:"omitempty,max=32,dive"`
	// Replace a pool the target lacks by one of the same fallback group (default true)
	AllowPoolFallback *bool `json:"allow_pool_fallback"`
	// Skip the capacity checks of the target, to evacuate a host when every other one is nearly full
	IgnoreCapacity bool `json:"ignore_capacity"`
}

type MigrationDiskPayload struct {
	Volume      *BaseReference `json:"volume" binding:"required"`
	StoragePool *BaseReference `json:"storage_pool" binding:"required"`
}

// @Summary get a migration
// @Description get a migration
// @tags Administration,Migration
// @Accept  json
// @Produce json
// @Success 200 {object} MigrationResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /migrations/{id} [get]
func (v *MigrationAPI) Get(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	logger.Ctx(ctx).Debugf("Get migration %s", uuID)
	migration, err := migrationAdmin.GetMigrationByUUID(ctx, uuID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to get migration %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid migration query", err)
		return
	}
	hyperNames, nErr := hyperAdmin.GetHyperNames(ctx)
	if nErr != nil {
		// 取不到节点名不影响迁移信息本身，名字留空即可
		logger.Ctx(ctx).Errorf("Failed to get hypervisor names: %+v", nErr)
		hyperNames = map[int32]string{}
	}
	migrationResp, err := v.getMigrationResponse(ctx, migration, hyperNames, loadDiskPlanNames(ctx, migration))
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to create migration response %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	logger.Ctx(ctx).Debugf("Get migration %s success, response: %+v", uuID, migrationResp)
	c.JSON(http.StatusOK, migrationResp)
}

// @Summary create a migration
// @Description create a migration
// @tags Administration,Migration
// @Accept  json
// @Produce json
// @Param   message	body   MigrationPayload  true   "Migration create payload"
// @Success 200 {array} MigrationResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /migrations [post]
func (v *MigrationAPI) Create(c *gin.Context) {
	logger.Ctx(c).Debugf("Create migration")
	ctx := c.Request.Context()
	payload := &MigrationPayload{}
	err := c.ShouldBindJSON(payload)
	if err != nil {
		logger.Ctx(ctx).Errorf("Invalid input JSON %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	var instances []*model.Instance
	for _, instRef := range payload.Instances {
		var instance *model.Instance
		instance, err = instanceAdmin.GetInstanceByUUID(ctx, instRef.ID)
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to get instance %s, %+v", instRef.ID, err)
			ErrorResponse(c, http.StatusBadRequest, "Invalid input, specified instance does not exist", err)
			return
		}
		instances = append(instances, instance)
	}
	targetHyper := int32(-1)
	if payload.TargetHyper != nil {
		targetHyper = *payload.TargetHyper
	}
	opts := &services.MigrationOptions{AllowPoolFallback: true, IgnoreCapacity: payload.IgnoreCapacity, Disks: map[int64]int64{}}
	if payload.AllowPoolFallback != nil {
		opts.AllowPoolFallback = *payload.AllowPoolFallback
	}
	if len(payload.Disks) > 0 && len(instances) != 1 {
		ErrorResponse(c, http.StatusBadRequest, "Target pools of disks can only be given for one instance", nil)
		return
	}
	for _, d := range payload.Disks {
		volume, verr := volumeAdmin.GetVolumeByUUID(ctx, d.Volume.ID)
		if verr != nil || volume.InstanceID != instances[0].ID {
			ErrorResponse(c, http.StatusBadRequest, "Invalid volume, it must be a disk of the instance", verr)
			return
		}
		pool, perr := storagePoolAdmin.Resolve(ctx, d.StoragePool)
		if perr != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid storage pool", perr)
			return
		}
		opts.Disks[volume.ID] = pool.ID
	}
	logger.Ctx(ctx).Debugf("Creating migration with payload %+v", payload)
	migrations, _, err := migrationAdmin.Create(ctx, payload.Name, instances, payload.Force, targetHyper, opts, false)
	if err != nil {
		logger.Ctx(ctx).Errorf("Not able to create migration %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Not able to create", err)
		return
	}
	hyperNames, nErr := hyperAdmin.GetHyperNames(ctx)
	if nErr != nil {
		logger.Ctx(ctx).Errorf("Failed to get hypervisor names: %+v", nErr)
		hyperNames = map[int32]string{}
	}
	migrationsResp := make([]*MigrationResponse, len(migrations))
	planNames := loadDiskPlanNames(ctx, migrations...)
	for i, migration := range migrations {
		migrationsResp[i], err = v.getMigrationResponse(ctx, migration, hyperNames, planNames)
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to create migration response %+v", err)
			ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
			return
		}
	}
	logger.Ctx(ctx).Debugf("Create migration success, response: %+v", migrationsResp)
	c.JSON(http.StatusOK, migrationsResp)
}

// hyperNames 为 hostid -> hostname 映射，由调用方一次查出后复用，避免逐条迁移查询节点表
func (v *MigrationAPI) getMigrationResponse(ctx context.Context, migration *model.Migration, hyperNames map[int32]string, planNames *diskPlanNames) (migrationResp *MigrationResponse, err error) {
	migrationResp = &MigrationResponse{
		ResourceReference: &ResourceReference{
			ID:        migration.UUID,
			Name:      migration.Name,
			CreatedAt: migration.CreatedAt.Format(TimeStringForMat),
			UpdatedAt: migration.UpdatedAt.Format(TimeStringForMat),
		},
		Force:       migration.Force,
		Type:        migration.Type,
		CreaterName: migration.CreaterName,
		CreaterUUID: migration.CreaterUUID,
		SourceHyper: migration.SourceHyper,
		TargetHyper: migration.TargetHyper,
		Status:      migration.Status,
		Progress:    migration.Progress,
		Transferred: migration.Transferred,
		Total:       migration.Total,
	}
	// 目标节点为 -1 表示尚未由调度器选出，此时没有名字可填
	migrationResp.SourceHyperName = hyperNames[migration.SourceHyper]
	migrationResp.TargetHyperName = hyperNames[migration.TargetHyper]
	if migration.Instance != nil {
		migrationResp.Instance = &InstanceInfo{
			ResourceReference: &ResourceReference{
				ID: migration.Instance.UUID,
			},
			Hostname: migration.Instance.Hostname,
		}
	}
	migrationResp.AllowPoolFallback = migration.AllowPoolFallback
	migrationResp.IgnoreCapacity = migration.IgnoreCapacity
	migrationResp.DiskPlan = v.diskPlanResponse(migration, planNames)
	migrationResp.Phases = make([]*TaskResponse, len(migration.Phases))
	for i, task := range migration.Phases {
		migrationResp.Phases[i] = &TaskResponse{
			Source:  string(task.Source),
			Name:    task.Name,
			Summary: task.Summary,
			Status:  string(task.Status),
			Message: task.Message,
		}
	}
	return
}

// @Summary list migrations
// @Description list migrations
// @tags Administration,Migration
// @Accept  json
// @Produce json
// @Success 200 {object} MigrationListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /migrations [get]
func (v *MigrationAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "50")
	orderStr := c.DefaultQuery("order", "-created_at")
	queryStr := c.DefaultQuery("query", "")
	logger.Ctx(ctx).Debugf("List migrations with offset %s, limit %s, query %s", offsetStr, limitStr, queryStr)
	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		logger.Ctx(ctx).Errorf("Invalid query offset %s, %+v", offsetStr, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset: "+offsetStr, err)
		return
	}
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		logger.Ctx(ctx).Errorf("Invalid query limit %s, %+v", limitStr, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query limit: "+limitStr, err)
		return
	}
	if offset < 0 || limit < 0 {
		logger.Ctx(ctx).Errorf("Invalid query offset or limit %d, %d", offset, limit)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset or limit", err)
		return
	}
	total, migrations, err := migrationAdmin.List(ctx, int64(offset), int64(limit), orderStr, queryStr)
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to list migrations %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Failed to list migrations", err)
		return
	}
	migrationListResp := &MigrationListResponse{
		Total:  int(total),
		Offset: offset,
		Limit:  len(migrations),
	}
	hyperNames, nErr := hyperAdmin.GetHyperNames(ctx)
	if nErr != nil {
		logger.Ctx(ctx).Errorf("Failed to get hypervisor names: %+v", nErr)
		hyperNames = map[int32]string{}
	}
	migrationListResp.Migrations = make([]*MigrationResponse, migrationListResp.Limit)
	planNames := loadDiskPlanNames(ctx, migrations...)
	for i, migration := range migrations {
		migrationListResp.Migrations[i], err = v.getMigrationResponse(ctx, migration, hyperNames, planNames)
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to create migration response %+v", err)
			ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
			return
		}
	}
	logger.Ctx(ctx).Debugf("List migrations success, response: %+v", migrationListResp)
	c.JSON(http.StatusOK, migrationListResp)
}

// @Summary list the hosts an instance can migrate to
// @Description for every host of the zone, whether each local disk can stay in its pool, the pool of its fallback group that would replace it, and all usable pools with their free space
// @tags Administration,Migration
// @Produce json
// @Param   id  path  string  true  "Instance UUID"
// @Success 200 {array} services.MigrationTarget
// @Router /instances/{id}/migration_targets [get]
func (v *MigrationAPI) Targets(c *gin.Context) {
	ctx := c.Request.Context()
	instance, err := instanceAdmin.GetInstanceByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid instance", err)
		return
	}
	targets, err := services.MigrationTargets(ctx, instance)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list migration targets", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"targets": targets})
}

// diskPlanNames holds the names of the volumes and pools the disk plans of some migrations refer to. Like
// hyperNames it is built once by the caller: the migration list is polled, and resolving every disk of every row
// on its own cost a few queries per disk. Migrations are for system admins only, so no org check is needed here.
type diskPlanNames struct {
	volumes map[int64]*ResourceReference
	pools   map[int64]*ResourceReference
}

func loadDiskPlanNames(ctx context.Context, migrations ...*model.Migration) *diskPlanNames {
	names := &diskPlanNames{volumes: map[int64]*ResourceReference{}, pools: map[int64]*ResourceReference{}}
	var volumeIDs, poolIDs []int64
	for _, migration := range migrations {
		for _, item := range services.MigrationPlan(migration) {
			volumeIDs = append(volumeIDs, item.VolumeID)
			poolIDs = append(poolIDs, item.SrcPoolID, item.DstPoolID)
		}
	}
	if len(volumeIDs) == 0 {
		return names
	}
	_, db := GetContextDB(ctx)
	volumes := []*model.Volume{}
	if err := db.Select("id, uuid, name").Where("id IN ?", volumeIDs).Find(&volumes).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to get the volumes of disk plans: %+v", err)
	}
	for _, volume := range volumes {
		names.volumes[volume.ID] = &ResourceReference{ID: volume.UUID, Name: volume.Name}
	}
	// Pools are deleted for good, so a pool removed since the migration shows no name
	_, db = GetContextDB(ctx)
	pools := []*model.StoragePool{}
	if err := db.Select("id, uuid, name").Where("id IN ?", poolIDs).Find(&pools).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to get the pools of disk plans: %+v", err)
	}
	for _, pool := range pools {
		names.pools[pool.ID] = &ResourceReference{ID: pool.UUID, Name: pool.Name}
	}
	return names
}

// volume and pool return a name, or an empty reference when the volume or pool is gone
func (n *diskPlanNames) volume(id int64) *ResourceReference {
	if ref, ok := n.volumes[id]; ok {
		return ref
	}
	return &ResourceReference{}
}

func (n *diskPlanNames) pool(id int64) *ResourceReference {
	if ref, ok := n.pools[id]; ok {
		return ref
	}
	return &ResourceReference{}
}

func (v *MigrationAPI) diskPlanResponse(migration *model.Migration, names *diskPlanNames) []*DiskPlanResponse {
	resp := []*DiskPlanResponse{}
	for _, item := range services.MigrationPlan(migration) {
		resp = append(resp, &DiskPlanResponse{Volume: names.volume(item.VolumeID), Device: item.Device,
			Booting: item.Booting, SizeGB: item.SizeGB, SourcePool: names.pool(item.SrcPoolID),
			TargetPool: names.pool(item.DstPoolID), Auto: item.Auto, Reason: item.Reason})
	}
	return resp
}
