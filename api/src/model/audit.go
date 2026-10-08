/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"api/src/dbs"

	"gorm.io/gorm"
)

// AuditLog 记录一次改动型操作：谁、在哪个组织、做了什么、结果如何。
// 资源表上的 creater 之类的列只能回答"谁创建了这条记录"，答不了"谁删的""谁把节点置为维护"
// ——那些操作不留下新记录，事后只能翻日志，而日志会滚动（2026-09-16 就因此查不到
// 是谁在 06:44 让 work-03 进入了维护模式）。审计表是追加写的，不随日志滚动丢失。
//
// 操作者存用户名而非 ID：本服务没有用户表、也不与 cpgateway 同步，ID 无法反查；
// 用户名不可更改、不可复用，作为当时事实的快照是准确的。
type AuditLog struct {
	Model
	// Actor 发起操作的用户名，来自 cpgateway 的 X-User-Name
	Actor string `gorm:"type:varchar(255);index"`
	// ActorUUID 是账号的全局唯一标识，与用户名快照互补：快照保证历史显示不失真，
	// UUID 为将来按身份精确查询或对接用户同步留路
	ActorUUID string `gorm:"type:varchar(64);index"`
	ActorID   int64
	// OrgID 本区域的组织 ID（已由 X-Org-UUID 解析）
	OrgID int64 `gorm:"index"`
	// Method / Path 为原始请求，Path 已包含路径参数，足以定位操作对象
	Method string `gorm:"type:varchar(8)"`
	Path   string `gorm:"type:varchar(512)"`
	// Status 为 HTTP 状态码，失败的尝试同样要留痕（谁试图做什么但被拒绝）
	Status  int
	Latency int64 // 毫秒
	// TraceID 关联到完整调用链，便于从一条审计记录跳到 Grafana 看全过程
	TraceID string `gorm:"type:varchar(64);index"`
	// Detail 失败时记录响应体片段，成功时为空
	Detail string `gorm:"type:varchar(1024)"`
	// Action 为语义化的动作名（如 instance.stop），由路由模板映射得出，个别接口按请求体细化。
	// 原始的 Method + Path 区分不了开机和关机（同是 PATCH /instances/:id），也无法直接展示给用户；
	// 为空表示该接口不属于用户可见的操作（如打开控制台），不出现在动态里
	Action       string `gorm:"type:varchar(64);not null;default:''"`
	ResourceType string `gorm:"type:varchar(32);not null;default:''"`
	ResourceUUID string `gorm:"type:varchar(64);not null;default:''"`
	// ResourceName 为操作当时的名称快照：资源删除后会被改名或不可查，事后无法补回
	ResourceName string `gorm:"type:varchar(255);not null;default:''"`
}

func init() {
	dbs.AutoMigrate(&AuditLog{})
	dbs.AutoUpgrade("001_audit_log_created_at", func(db *gorm.DB) error {
		// 审计查询基本都是"按时间倒序看最近的操作"
		return db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs (created_at DESC)`).Error
	})
	dbs.AutoUpgrade("002_audit_log_org_activity", func(db *gorm.DB) error {
		// 组织动态：按组织、时间倒序分页，只含有语义动作的记录
		return db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_logs_org_activity ON audit_logs (org_id, created_at DESC, id DESC) WHERE action <> ''`).Error
	})
	dbs.AutoUpgrade("003_audit_log_resource", func(db *gorm.DB) error {
		// 资源详情页的操作记录
		return db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_logs_resource ON audit_logs (resource_uuid, created_at DESC) WHERE resource_uuid <> ''`).Error
	})
}
