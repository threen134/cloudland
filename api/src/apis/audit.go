/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"context"
	"net/http"
	"strconv"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
	"api/src/utils/tracing"

	"github.com/gin-gonic/gin"
)

var auditAPI = &AuditAPI{}

type AuditAPI struct{}

type AuditLogResponse struct {
	ID        string `json:"id"`
	Actor     string `json:"actor"`
	ActorUUID string `json:"actor_uuid"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	Latency   int64  `json:"latency_ms"`
	TraceID   string `json:"trace_id"`
	Detail    string `json:"detail,omitempty"`
	CreatedAt string `json:"created_at"`
}

type AuditLogListResponse struct {
	Offset int                 `json:"offset"`
	Total  int                 `json:"total"`
	Limit  int                 `json:"limit"`
	Logs   []*AuditLogResponse `json:"logs"`
}

const auditDetailLimit = 1024

// Audit 记录改动型请求。只记 POST/PUT/PATCH/DELETE：GET 量大且不改变状态，记下来
// 只会淹没真正需要追查的操作。失败的请求同样记录——"谁试图做什么但被拒绝"往往比成功
// 的操作更值得追查。
//
// 必须注册在 Authorize 之后：操作者身份来自 MemberShip。
func Audit() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			c.Next()
			return
		}
		start := time.Now()
		writer := &auditBodyWriter{ResponseWriter: c.Writer}
		c.Writer = writer
		c.Next()

		memberShip := GetMemberShip(c.Request.Context())
		entry := &model.AuditLog{
			Actor:     memberShip.UserName,
			ActorUUID: memberShip.UserUUID,
			ActorID:   memberShip.UserID,
			OrgID:     memberShip.OrgID,
			Method:    c.Request.Method,
			Path:      c.Request.URL.Path,
			Status:    c.Writer.Status(),
			Latency:   time.Since(start).Milliseconds(),
			TraceID:   c.Writer.Header().Get(tracing.TraceIDHeader),
		}
		// 只在失败时留响应体：成功的响应往往很大（整个资源），而且没有排查价值
		if entry.Status >= 400 {
			entry.Detail = writer.body
		}
		// 审计写失败不能影响请求本身，也不能被客户端断开连接取消
		go func() {
			ctx := SetContextDB(context.WithoutCancel(c.Request.Context()), dbs.DB())
			_, db := GetContextDB(ctx)
			if err := db.Create(entry).Error; err != nil {
				logger.Ctx(ctx).Errorf("Failed to write audit log for %s %s: %v", entry.Method, entry.Path, err)
			}
		}()
	}
}

// auditBodyWriter 截取响应体开头一段，用于记录失败原因
type auditBodyWriter struct {
	gin.ResponseWriter
	body string
}

func (w *auditBodyWriter) Write(b []byte) (int, error) {
	if len(w.body) < auditDetailLimit {
		remain := auditDetailLimit - len(w.body)
		if len(b) < remain {
			remain = len(b)
		}
		w.body += string(b[:remain])
	}
	return w.ResponseWriter.Write(b)
}

// @Summary list audit logs
// @Description 按时间倒序返回改动型操作的审计记录，仅系统管理员可见
// @tags Administration
// @Accept  json
// @Produce json
// @Success 200 {object} AuditLogListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /audit_logs [get]
func (v *AuditAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckSystemPermission() {
		ErrorResponse(c, http.StatusForbidden, "Not authorized for this operation", nil)
		return
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset", err)
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 0 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query limit", err)
		return
	}
	if limit == 0 {
		limit = 50
	}

	ctx = SetContextDB(ctx, DB())
	_, db := GetContextDB(ctx)
	query := db.Model(&model.AuditLog{})
	// actor / path 过滤：排查时最常见的两个入口——"某人做了什么""某个资源被谁动过"
	if actor := c.Query("actor"); actor != "" {
		query = query.Where("actor = ?", actor)
	}
	if actorUUID := c.Query("actor_uuid"); actorUUID != "" {
		query = query.Where("actor_uuid = ?", actorUUID)
	}
	if path := c.Query("path"); path != "" {
		query = query.Where("path LIKE ?", "%"+path+"%")
	}
	var total int64
	if err = query.Count(&total).Error; err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to count audit logs", err)
		return
	}
	logs := []*model.AuditLog{}
	if err = query.Order("created_at desc").Offset(offset).Limit(limit).Find(&logs).Error; err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to query audit logs", err)
		return
	}
	resp := &AuditLogListResponse{Offset: offset, Total: int(total), Limit: len(logs)}
	resp.Logs = make([]*AuditLogResponse, len(logs))
	for i, l := range logs {
		resp.Logs[i] = &AuditLogResponse{
			ID: l.UUID, Actor: l.Actor, ActorUUID: l.ActorUUID, Method: l.Method, Path: l.Path,
			Status: l.Status, Latency: l.Latency, TraceID: l.TraceID, Detail: l.Detail,
			CreatedAt: l.CreatedAt.Format(TimeStringForMat),
		}
	}
	c.JSON(http.StatusOK, resp)
}
