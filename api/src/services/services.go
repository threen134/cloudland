/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	rlog "api/src/utils/log"
)

var logger = rlog.MustGetLogger("services")

// Init initializes the services package, creating system admin user and org
func Init() {
	AdminInit()
	RebuildAlarmRulesOnStartup()
	// 启动时从 DB 重建 hyper-hosts，确保 dnsmasq 能解析所有已注册节点
	RebuildDnsHostsFile()
	// 启动时应用一次 DNS 上游配置，确保 dnsmasq 与数据库镜像一致
	ApplyDnsUpstream()
}
