package services

import (
	"encoding/json"
	"strings"

	"api/src/model"

	. "api/src/common"
)

// GetMirrorSetting 从本地系统设置镜像表中读取指定 key 的值。
// 值以 JSON 格式存储，返回反序列化后的字符串（若原始值为 JSON 字符串，则去掉外层引号）。
// 读取失败时返回空字符串，不 panic。
func GetMirrorSetting(key string) string {
	db := DB()
	var row model.SystemSettingMirror
	if err := db.Where("key = ?", key).First(&row).Error; err != nil {
		return ""
	}
	raw := strings.TrimSpace(row.Value)
	if raw == "" || raw == "null" {
		return ""
	}
	// JSON 字符串：去掉外层引号
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err == nil {
		return s
	}
	// 非 JSON 字符串类型（number、boolean、json object），直接返回原始值
	return raw
}

// GetMirrorSettingJSON 从镜像中读取 JSON 类型值，返回已解析的 interface{}。
func GetMirrorSettingJSON(key string) interface{} {
	db := DB()
	var row model.SystemSettingMirror
	if err := db.Where("key = ?", key).First(&row).Error; err != nil {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal([]byte(row.Value), &v); err != nil {
		return nil
	}
	return v
}
