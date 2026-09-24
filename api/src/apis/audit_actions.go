/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	. "api/src/common"
	"api/src/model"

	"github.com/gin-gonic/gin"
)

// auditRoute 描述一个改动型接口对应的用户可见动作
type auditRoute struct {
	Resource string // 资源类型
	Action   string // 默认动作名，接口可用 SetAuditAction 按请求体细化
	Param    string // 路径中标识资源的参数，空表示集合操作（如创建）
	ByName   bool   // Param 是资源名而不是 UUID（zone、flavor）
}

const auditActionKey = "audit.action"

// auditRoutes 以「方法 + 路由模板（去掉 /api/v1）」为键。
// 不在表里的接口仍写审计日志，只是 action 为空、不出现在组织动态中
// （例如打开控制台、地址备注、告警规则、内部调整接口）
var auditRoutes = map[string]auditRoute{
	"POST /zones":         {"zone", "zone.create", "", false},
	"DELETE /zones/:name": {"zone", "zone.delete", "name", true},
	"PATCH /zones/:name":  {"zone", "zone.update", "name", true},

	"POST /hypers":                {"hyper", "hyper.create", "", false},
	"DELETE /hypers/:uuid":        {"hyper", "hyper.delete", "uuid", false},
	"PATCH /hypers/:uuid":         {"hyper", "hyper.update", "uuid", false},
	"POST /hypers/:uuid/maintain": {"hyper", "hyper.maintain", "uuid", false},
	"POST /hypers/:uuid/console":  {"hyper", "hyper.console", "uuid", false},

	"POST /migrations": {"migration", "migration.create", "", false},

	"POST /vpcs":       {"vpc", "vpc.create", "", false},
	"DELETE /vpcs/:id": {"vpc", "vpc.delete", "id", false},
	"PATCH /vpcs/:id":  {"vpc", "vpc.update", "id", false},

	"POST /subnets":       {"subnet", "subnet.create", "", false},
	"DELETE /subnets/:id": {"subnet", "subnet.delete", "id", false},
	"PATCH /subnets/:id":  {"subnet", "subnet.update", "id", false},

	"POST /security_groups":                      {"security_group", "security_group.create", "", false},
	"DELETE /security_groups/:id":                {"security_group", "security_group.delete", "id", false},
	"PATCH /security_groups/:id":                 {"security_group", "security_group.update", "id", false},
	"POST /security_groups/:id/rules":            {"security_group", "security_group.rule_create", "id", false},
	"DELETE /security_groups/:id/rules/:rule_id": {"security_group", "security_group.rule_delete", "id", false},
	"PATCH /security_groups/:id/rules/:rule_id":  {"security_group", "security_group.rule_update", "id", false},

	"POST /load_balancers":                                                   {"load_balancer", "load_balancer.create", "", false},
	"DELETE /load_balancers/:id":                                             {"load_balancer", "load_balancer.delete", "id", false},
	"PATCH /load_balancers/:id":                                              {"load_balancer", "load_balancer.update", "id", false},
	"POST /load_balancers/:id/floating_ips":                                  {"load_balancer", "load_balancer.fip_attach", "id", false},
	"DELETE /load_balancers/:id/floating_ips/:floating_ip_id":                {"load_balancer", "load_balancer.fip_detach", "id", false},
	"POST /load_balancers/:id/listeners":                                     {"load_balancer", "load_balancer.listener_create", "id", false},
	"DELETE /load_balancers/:id/listeners/:listener_id":                      {"load_balancer", "load_balancer.listener_delete", "id", false},
	"PATCH /load_balancers/:id/listeners/:listener_id":                       {"load_balancer", "load_balancer.listener_update", "id", false},
	"POST /load_balancers/:id/listeners/:listener_id/backends":               {"load_balancer", "load_balancer.backend_create", "id", false},
	"DELETE /load_balancers/:id/listeners/:listener_id/backends/:backend_id": {"load_balancer", "load_balancer.backend_delete", "id", false},
	"PATCH /load_balancers/:id/listeners/:listener_id/backends/:backend_id":  {"load_balancer", "load_balancer.backend_update", "id", false},

	"POST /vpn_gateways":                                        {"vpn_gateway", "vpn_gateway.create", "", false},
	"DELETE /vpn_gateways/:id":                                  {"vpn_gateway", "vpn_gateway.delete", "id", false},
	"PATCH /vpn_gateways/:id":                                   {"vpn_gateway", "vpn_gateway.update", "id", false},
	"POST /vpn_gateways/:id/connections":                        {"vpn_gateway", "vpn_gateway.connection_create", "id", false},
	"DELETE /vpn_gateways/:id/connections/:conn_id":             {"vpn_gateway", "vpn_gateway.connection_delete", "id", false},
	"PATCH /vpn_gateways/:id/connections/:conn_id":              {"vpn_gateway", "vpn_gateway.connection_update", "id", false},
	"POST /vpn_gateways/:id/connections/:conn_id/restart":       {"vpn_gateway", "vpn_gateway.connection_restart", "id", false},
	"POST /vpn_gateways/:id/clients":                            {"vpn_gateway", "vpn_gateway.client_create", "id", false},
	"DELETE /vpn_gateways/:id/clients/:client_id":               {"vpn_gateway", "vpn_gateway.client_delete", "id", false},
	"PATCH /vpn_gateways/:id/clients/:client_id":                {"vpn_gateway", "vpn_gateway.client_update", "id", false},

	"POST /floating_ips":             {"floating_ip", "floating_ip.create", "", false},
	"DELETE /floating_ips/:id":       {"floating_ip", "floating_ip.delete", "id", false},
	"PATCH /floating_ips/:id":        {"floating_ip", "floating_ip.update", "id", false},
	"POST /floating_ips/site_attach": {"floating_ip", "floating_ip.site_attach", "", false},
	"POST /floating_ips/site_detach": {"floating_ip", "floating_ip.site_detach", "", false},

	"POST /keys":       {"key", "key.create", "", false},
	"DELETE /keys/:id": {"key", "key.delete", "id", false},

	"POST /flavors":         {"flavor", "flavor.create", "", false},
	"DELETE /flavors/:name": {"flavor", "flavor.delete", "name", true},

	"POST /images":       {"image", "image.create", "", false},
	"DELETE /images/:id": {"image", "image.delete", "id", false},
	"PATCH /images/:id":  {"image", "image.update", "id", false},

	"POST /volumes":                  {"volume", "volume.create", "", false},
	"DELETE /volumes/:id":            {"volume", "volume.delete", "id", false},
	"PATCH /volumes/:id":             {"volume", "volume.update", "id", false},
	"POST /volumes/:id/resize":       {"volume", "volume.resize", "id", false},
	"POST /volumes/:id/force_detach": {"volume", "volume.force_detach", "id", false},

	"POST /storage_pools":                     {"storage_pool", "storage_pool.create", "", false},
	"PATCH /storage_pools/:id":                {"storage_pool", "storage_pool.update", "id", false},
	"DELETE /storage_pools/:id":               {"storage_pool", "storage_pool.delete", "id", false},
	"POST /storage_pools/:id/orphans/abandon": {"storage_pool", "storage_pool.orphans_abandon", "id", false},

	"POST /hypers/:uuid/disks/scan":                          {"hyper", "hyper.disks_scan", "uuid", false},
	"PATCH /hypers/:uuid/disks/:id":                          {"hyper", "hyper.disk_update", "uuid", false},
	"POST /hypers/:uuid/storage_pools":                       {"hyper", "hyper.storage_pool_create", "uuid", false},
	"POST /hypers/:uuid/storage_pools/adopt":                 {"hyper", "hyper.storage_pool_adopt", "uuid", false},
	"DELETE /hypers/:uuid/storage_pools/:pool_id":            {"hyper", "hyper.storage_pool_delete", "uuid", false},
	"POST /hypers/:uuid/storage_pools/:pool_id/extend":       {"hyper", "hyper.storage_pool_extend", "uuid", false},
	"POST /hypers/:uuid/storage_pools/:pool_id/replace_disk": {"hyper", "hyper.storage_pool_replace_disk", "uuid", false},
	"POST /hypers/:uuid/storage_pools/:pool_id/maintenance":  {"hyper", "hyper.storage_pool_maintenance", "uuid", false},
	"POST /hypers/:uuid/storage_pools/:pool_id/lost":         {"hyper", "hyper.storage_pool_lost", "uuid", false},
	"POST /hypers/:uuid/storage_pools/:pool_id/restore":      {"hyper", "hyper.storage_pool_restore", "uuid", false},

	"POST /instances":                                {"instance", "instance.create", "", false},
	"DELETE /instances/:id":                          {"instance", "instance.delete", "id", false},
	"PATCH /instances/:id":                           {"instance", "instance.update", "id", false},
	"POST /instances/:id/set_user_password":          {"instance", "instance.set_password", "id", false},
	"POST /instances/:id/reinstall":                  {"instance", "instance.reinstall", "id", false},
	"POST /instances/:id/resize":                     {"instance", "instance.resize", "id", false},
	"POST /instances/:id/rescue":                     {"instance", "instance.rescue", "id", false},
	"POST /instances/:id/end_rescue":                 {"instance", "instance.end_rescue", "id", false},
	"POST /instances/:id/interfaces":                 {"instance", "instance.interface_create", "id", false},
	"DELETE /instances/:id/interfaces/:interface_id": {"instance", "instance.interface_delete", "id", false},
	"PATCH /instances/:id/interfaces/:interface_id":  {"instance", "instance.interface_update", "id", false},
}

// auditNameColumns 资源类型 -> 表名与名称列，用于在操作前快照资源名
var auditNameColumns = map[string][2]string{
	"instance":       {"instances", "hostname"},
	"hyper":          {"hypers", "hostname"},
	"vpc":            {"routers", "name"},
	"vpn_gateway":    {"vpn_gateways", "name"},
	"subnet":         {"subnets", "name"},
	"security_group": {"security_groups", "name"},
	"load_balancer":  {"load_balancers", "name"},
	"floating_ip":    {"floating_ips", "name"},
	"key":            {"keys", "name"},
	"image":          {"images", "name"},
	"volume":         {"volumes", "name"},
	"storage_pool":   {"storage_pools", "name"},
}

// SetAuditAction 供接口按请求体细化动作名，例如 PATCH /instances/:id 区分开机、关机与改名
func SetAuditAction(c *gin.Context, action string) {
	c.Set(auditActionKey, action)
}

func lookupAuditRoute(c *gin.Context) (route auditRoute, ok bool) {
	template := strings.TrimPrefix(c.FullPath(), "/api/v1")
	route, ok = auditRoutes[c.Request.Method+" "+template]
	return
}

// lookupResourceName 在接口执行前查资源名：删除后资源会被软删并可能改名，事后查不到原名
func lookupResourceName(ctx context.Context, resourceType, uuid string) string {
	column, ok := auditNameColumns[resourceType]
	if !ok || uuid == "" {
		return ""
	}
	_, db := GetContextDB(ctx)
	var names []string
	if err := db.Table(column[0]).Where("uuid = ?", uuid).Limit(1).Pluck(column[1], &names).Error; err != nil || len(names) == 0 {
		return ""
	}
	return names[0]
}

type auditResourceBody struct {
	ID string `json:"id"`
	// 部分接口（hypers、flavors）的响应用 uuid 而不是 id
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
}

// resourceFromResponse 从成功的响应体中取资源 ID 与名称：创建类接口的新资源 ID 只出现在响应里，
// 改名后的新名称也以响应为准。批量创建返回数组，只有一个元素时才取
func resourceFromResponse(body string) (id, name string) {
	body = strings.TrimSpace(body)
	resource := &auditResourceBody{}
	switch {
	case strings.HasPrefix(body, "{"):
		if json.Unmarshal([]byte(body), resource) != nil {
			return
		}
	case strings.HasPrefix(body, "["):
		list := []*auditResourceBody{}
		if json.Unmarshal([]byte(body), &list) != nil || len(list) != 1 || list[0] == nil {
			return
		}
		resource = list[0]
	default:
		return
	}
	name = resource.Name
	if resource.Hostname != "" {
		name = resource.Hostname
	}
	id = resource.ID
	if id == "" {
		id = resource.UUID
	}
	return id, name
}

// truncateUTF8 去掉非法 UTF-8 字节后按完整字符截断到最多 maxBytes 字节。按字节直接切可能切在
// 多字节字符中间，路径解码后也可能含非法字节（如 %FF），Postgres 会以
// invalid byte sequence for encoding UTF8 拒绝整条插入，审计记录随之丢失
func truncateUTF8(s string, maxBytes int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// fitAuditColumns 把各字段截到列宽：超长的路径参数（如 100 字符的 ID）会让插入报
// value too long，而被拒绝的异常请求恰恰最需要留下记录
func fitAuditColumns(entry *model.AuditLog) {
	entry.Actor = truncateUTF8(entry.Actor, 255)
	entry.ActorUUID = truncateUTF8(entry.ActorUUID, 64)
	entry.Method = truncateUTF8(entry.Method, 8)
	entry.Path = truncateUTF8(entry.Path, 512)
	entry.TraceID = truncateUTF8(entry.TraceID, 64)
	entry.Detail = truncateUTF8(entry.Detail, auditDetailLimit)
	entry.Action = truncateUTF8(entry.Action, 64)
	entry.ResourceType = truncateUTF8(entry.ResourceType, 32)
	entry.ResourceUUID = truncateUTF8(entry.ResourceUUID, 64)
	entry.ResourceName = truncateUTF8(entry.ResourceName, 255)
}
