/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
	"api/src/utils/tracing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var auditAPI = &AuditAPI{}

type AuditAPI struct{}

type AuditLogResponse struct {
	ID           string `json:"id"`
	Actor        string `json:"actor"`
	ActorUUID    string `json:"actor_uuid"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Status       int    `json:"status"`
	Latency      int64  `json:"latency_ms"`
	TraceID      string `json:"trace_id"`
	Detail       string `json:"detail,omitempty"`
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	ResourceName string `json:"resource_name"`
	CreatedAt    string `json:"created_at"`
}

type AuditLogListResponse struct {
	Offset int                 `json:"offset"`
	Total  int                 `json:"total"`
	Limit  int                 `json:"limit"`
	Logs   []*AuditLogResponse `json:"logs"`
}

// ActivityResponse 面向组织成员的操作动态：只含可展示的语义字段，不暴露路径、耗时、
// trace_id 与失败响应体（可能含内部错误、节点名等）
type ActivityResponse struct {
	ID           string `json:"id"`
	Actor        string `json:"actor"`
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	ResourceName string `json:"resource_name"`
	Success      bool   `json:"success"`
	CreatedAt    string `json:"created_at"`
}

type ActivityListResponse struct {
	Activities []*ActivityResponse `json:"activities"`
	// NextCursor 非空表示还有更早的记录，作为下一页的 cursor 参数传回
	NextCursor string `json:"next_cursor"`
}

const (
	auditDetailLimit = 1024
	// 成功响应只为提取资源 ID 与名称而截取，超出则放弃解析（被截断的 JSON 无法解析）
	auditBodyLimit = 64 * 1024
	// 动态不传时间范围时默认取最近 7 天；单次查询跨度上限 90 天，避免扫描整表
	activityDefaultSpan = 7 * 24 * time.Hour
	auditMaxSpan        = 90 * 24 * time.Hour
)

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
		route, known := lookupAuditRoute(c)
		resourceUUID, resourceName := "", ""
		if known && route.Param != "" {
			if route.ByName {
				resourceName = c.Param(route.Param)
			} else {
				resourceUUID = c.Param(route.Param)
				// 名称要在接口执行前查：删除后资源被软删、可能改名
				resourceName = lookupResourceName(c.Request.Context(), route.Resource, resourceUUID)
			}
		}
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
		body := writer.body.String()
		if known {
			entry.ResourceType = route.Resource
			entry.Action = route.Action
			if action := c.GetString(auditActionKey); action != "" {
				entry.Action = action
			}
			if entry.Status < 400 && !writer.truncated {
				// 集合操作（创建）取响应里的新资源；针对单个资源的操作只在响应描述的就是
				// 该资源时采用其名称（子资源接口如添加安全组规则，响应是规则而不是安全组）
				id, name := resourceFromResponse(body)
				if route.Param == "" && id != "" {
					resourceUUID = id
				}
				if name != "" && id != "" && id == resourceUUID {
					resourceName = name
				}
			}
			entry.ResourceUUID = resourceUUID
			entry.ResourceName = resourceName
		}
		// 只在失败时留响应体：成功的响应往往很大（整个资源），而且没有排查价值
		if entry.Status >= 400 {
			entry.Detail = body
		}
		fitAuditColumns(entry)
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

// auditBodyWriter 截取响应体开头一段，用于记录失败原因和提取资源信息
type auditBodyWriter struct {
	gin.ResponseWriter
	body      strings.Builder
	truncated bool
}

func (w *auditBodyWriter) Write(b []byte) (int, error) {
	remain := auditBodyLimit - w.body.Len()
	switch {
	case len(b) <= remain:
		w.body.Write(b)
	case remain > 0:
		w.body.Write(b[:remain])
		w.truncated = true
	default:
		w.truncated = true
	}
	return w.ResponseWriter.Write(b)
}

// parseTimeRange 解析 start / end（RFC3339，左闭右开）。defaultSpan 为 0 且两者都不传时不限时间；
// 否则 end 缺省为当前时间、start 缺省为 end 往前 defaultSpan。跨度不得超过 auditMaxSpan
func parseTimeRange(c *gin.Context, defaultSpan time.Duration) (start, end time.Time, err error) {
	startStr, endStr := c.Query("start"), c.Query("end")
	if startStr != "" {
		if start, err = time.Parse(time.RFC3339, startStr); err != nil {
			return start, end, fmt.Errorf("Invalid start, expect RFC3339 such as 2026-09-16T00:00:00Z")
		}
	}
	if endStr != "" {
		if end, err = time.Parse(time.RFC3339, endStr); err != nil {
			return start, end, fmt.Errorf("Invalid end, expect RFC3339 such as 2026-09-16T00:00:00Z")
		}
	}
	if startStr == "" && endStr == "" && defaultSpan == 0 {
		return
	}
	if end.IsZero() {
		end = time.Now()
	}
	if start.IsZero() {
		span := defaultSpan
		if span == 0 {
			span = auditMaxSpan
		}
		start = end.Add(-span)
	}
	if !start.Before(end) {
		return start, end, fmt.Errorf("Start must be earlier than end")
	}
	if end.Sub(start) > auditMaxSpan {
		return start, end, fmt.Errorf("Time range must not exceed %d days", int(auditMaxSpan.Hours()/24))
	}
	return
}

func applyTimeRange(query *gorm.DB, start, end time.Time) *gorm.DB {
	if !start.IsZero() {
		query = query.Where("created_at >= ?", start)
	}
	if !end.IsZero() {
		query = query.Where("created_at < ?", end)
	}
	return query
}

// @Summary list audit logs
// @Description 按时间倒序返回改动型操作的审计记录，仅系统管理员可见
// @tags Administration
// @Accept  json
// @Produce json
// @Param   offset         query  int     false  "offset"
// @Param   limit          query  int     false  "limit, default 50"
// @Param   actor          query  string  false  "username"
// @Param   actor_uuid     query  string  false  "user uuid"
// @Param   path           query  string  false  "request path contains"
// @Param   resource_type  query  string  false  "resource type, such as instance"
// @Param   resource_uuid  query  string  false  "resource uuid"
// @Param   start          query  string  false  "RFC3339, inclusive"
// @Param   end            query  string  false  "RFC3339, exclusive; range must not exceed 90 days"
// @Success 200 {object} AuditLogListResponse
// @Failure 400 {object} common.APIError "Bad request"
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
	start, end, err := parseTimeRange(c, 0)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, err.Error(), err)
		return
	}

	ctx = SetContextDB(ctx, DB())
	_, db := GetContextDB(ctx)
	query := applyTimeRange(db.Model(&model.AuditLog{}), start, end)
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
	if resourceType := c.Query("resource_type"); resourceType != "" {
		query = query.Where("resource_type = ?", resourceType)
	}
	if resourceUUID := c.Query("resource_uuid"); resourceUUID != "" {
		query = query.Where("resource_uuid = ?", resourceUUID)
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
			Action: l.Action, ResourceType: l.ResourceType, ResourceID: l.ResourceUUID, ResourceName: l.ResourceName,
			CreatedAt: l.CreatedAt.Format(TimeStringForMat),
		}
	}
	c.JSON(http.StatusOK, resp)
}

// 动态游标为 "<created_at 纳秒>:<id>" 的 base64url。用游标而不是 offset：
// 动态列表持续有新记录插入，offset 翻页会重复或漏掉条目
func encodeActivityCursor(l *model.AuditLog) string {
	raw := fmt.Sprintf("%d:%d", l.CreatedAt.UnixNano(), l.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeActivityCursor(cursor string) (createdAt time.Time, id int64, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		err = fmt.Errorf("malformed cursor")
		return
	}
	nano, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return
	}
	if id, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
		return
	}
	return time.Unix(0, nano), id, nil
}

// @Summary list activities of current organization
// @Description 当前组织在本区域的操作动态，按时间倒序、游标分页，组织内所有成员可见
// @tags Activity
// @Accept  json
// @Produce json
// @Param   limit          query  int     false  "default 20, max 100"
// @Param   cursor         query  string  false  "next_cursor from previous page"
// @Param   start          query  string  false  "RFC3339, inclusive; default 7 days before end"
// @Param   end            query  string  false  "RFC3339, exclusive; default now; range must not exceed 90 days"
// @Param   resource_type  query  string  false  "resource type, such as instance"
// @Param   resource_uuid  query  string  false  "resource uuid"
// @Success 200 {object} ActivityListResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /activities [get]
func (v *AuditAPI) Activities(c *gin.Context) {
	ctx := c.Request.Context()
	memberShip := GetMemberShip(ctx)
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil || limit < 1 || limit > 100 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query limit, must be 1-100", err)
		return
	}
	start, end, err := parseTimeRange(c, activityDefaultSpan)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, err.Error(), err)
		return
	}

	ctx = SetContextDB(ctx, DB())
	_, db := GetContextDB(ctx)
	// 组织范围只取自身份、不接受参数：成员不能查看其他组织的动态
	query := applyTimeRange(db.Model(&model.AuditLog{}), start, end).
		Where("org_id = ? AND action <> ''", memberShip.OrgID)
	if resourceType := c.Query("resource_type"); resourceType != "" {
		query = query.Where("resource_type = ?", resourceType)
	}
	if resourceUUID := c.Query("resource_uuid"); resourceUUID != "" {
		query = query.Where("resource_uuid = ?", resourceUUID)
	}
	if cursor := c.Query("cursor"); cursor != "" {
		createdAt, id, cErr := decodeActivityCursor(cursor)
		if cErr != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid cursor", cErr)
			return
		}
		query = query.Where("(created_at, id) < (?, ?)", createdAt, id)
	}
	logs := []*model.AuditLog{}
	// 多取一条判断是否还有下一页
	if err = query.Order("created_at desc, id desc").Limit(limit + 1).Find(&logs).Error; err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to query activities", err)
		return
	}
	resp := &ActivityListResponse{Activities: []*ActivityResponse{}}
	if len(logs) > limit {
		logs = logs[:limit]
		resp.NextCursor = encodeActivityCursor(logs[limit-1])
	}
	for _, l := range logs {
		resp.Activities = append(resp.Activities, &ActivityResponse{
			ID: l.UUID, Actor: l.Actor, Action: l.Action,
			ResourceType: l.ResourceType, ResourceID: l.ResourceUUID, ResourceName: l.ResourceName,
			Success:   l.Status < 400,
			CreatedAt: l.CreatedAt.Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, resp)
}
