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
	// 异步初始化 DNS：重建 hosts 文件 + 应用上游配置，不阻塞主启动流程
	go func() {
		RebuildDnsHostsFile()
		ApplyDnsUpstream()
	}()
}
