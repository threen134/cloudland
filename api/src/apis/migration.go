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
	migrationResp, err := v.getMigrationResponse(ctx, migration, hyperNames)
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
	logger.Ctx(ctx).Debugf("Creating migration with payload %+v", payload)
	migrations, err := migrationAdmin.Create(ctx, payload.Name, instances, payload.Force, targetHyper)
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
	for i, migration := range migrations {
		migrationsResp[i], err = v.getMigrationResponse(ctx, migration, hyperNames)
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
func (v *MigrationAPI) getMigrationResponse(_ context.Context, migration *model.Migration, hyperNames map[int32]string) (migrationResp *MigrationResponse, err error) {
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
	total, migrations, err := migrationAdmin.List(ctx, int64(offset), int64(limit), "-created_at", queryStr)
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
	for i, migration := range migrations {
		migrationListResp.Migrations[i], err = v.getMigrationResponse(ctx, migration, hyperNames)
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to create migration response %+v", err)
			ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
			return
		}
	}
	logger.Ctx(ctx).Debugf("List migrations success, response: %+v", migrationListResp)
	c.JSON(http.StatusOK, migrationListResp)
}
