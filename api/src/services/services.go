/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"strings"

	"github.com/spf13/viper"

	rlog "api/src/utils/log"
)

var logger = rlog.MustGetLogger("services")

// Init initializes the services package, creating system admin user and org
func Init() {
	AdminInit()
	RebuildAlarmRulesOnStartup()
	// CLAPI_INTERNAL_URL 默认拼装必须在 goroutine 启动前完成，
	// 否则 capture 分支拿到空 URL 直接报错；不依赖 DNS，做纯字符串拼接。
	fillClapiInternalURL()
	// 异步初始化 DNS + S3：先重建 hosts 文件并注册 S3/clapi 域名，再 InitS3
	// 顺序必须是 "DNS 先就绪 → InitS3 才能解析 S3_ENDPOINT"，否则 BucketExists
	// NXDOMAIN 触发静默降级，整个 S3 feature 假性禁用
	go func() {
		RebuildDnsHostsFile()
		ApplyDnsUpstream()
		registerS3ServiceDns()
		// 到这里 images.cloudland.internal / clapi.cloudland.internal 已经可解析
		InitS3()
	}()
}

// registerS3ServiceDns 把 MinIO/clapi 的内部域名注册到 hyper-hosts，供 compute node 解析
// 仅对 `.cloudland.internal` 这类内部域名做注册；外部 S3 域名（如 s3.amazonaws.com）走上游 DNS
func registerS3ServiceDns() {
	vip := viper.GetString("management_vip")
	if vip == "" {
		logger.Info("DNS: management_vip not set, skip S3/clapi hostname registration")
		return
	}
	// MinIO 域名（仅内置 MinIO 模式）
	if s3Host := ParseS3Host(); s3Host != "" && strings.HasSuffix(s3Host, ".cloudland.internal") {
		RegisterHostInDns(s3Host, vip)
	}
	// clapi 域名（capture 转发路径）
	if clapiHost := viper.GetString("clapi.hostname"); clapiHost != "" {
		RegisterHostInDns(clapiHost, vip)
	}
}

// fillClapiInternalURL 若未显式配置 CLAPI_INTERNAL_URL，则基于 CLAPI_HOSTNAME 拼装默认值
// 格式：http://<clapi_hostname>:8255/api/v1（capture 上传端点的基地址）
func fillClapiInternalURL() {
	if viper.GetString("clapi.internal_url") != "" {
		return
	}
	host := viper.GetString("clapi.hostname")
	if host == "" {
		return
	}
	viper.Set("clapi.internal_url", "http://"+host+":8255/api/v1")
}
