/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/spf13/viper"

	rlog "api/src/utils/log"
)

var logger = rlog.MustGetLogger("services")

// s3ConfigErr 记录 S3 配置错误（重试无意义，需修改配置后重启 clapi），供镜像相关接口报出明确原因
var s3ConfigErr atomic.Pointer[error]

// Init initializes the services package, creating system admin user and org
func Init() {
	AdminInit()
	RebuildAlarmRulesOnStartup()
	StartVMRuleMappingReconciler()
	StartStorageMaintainer()
	StartVpnMasterWatchdog()
	StartTgwWatchdog()
	StartStorageTaskWorker()
	// CLAPI_INTERNAL_URL 默认拼装必须在 goroutine 启动前完成，
	// 否则 capture 分支拿到空 URL 直接报错；不依赖 DNS，做纯字符串拼接。
	fillClapiInternalURL()
	// DNS first, then S3: the rebuilt hosts file carries the S3 and clapi service names (serviceDnsEntries), and
	// InitS3 can only resolve S3_ENDPOINT once they are there; a BucketExists NXDOMAIN would leave S3 disabled
	go func() {
		RebuildDnsHostsFile()
		ApplyDnsUpstream()
		initS3WithRetry()
	}()
}

// initS3WithRetry 宿主机重启等场景下 MinIO/dnsmasq 可能晚于 clapi 就绪：失败后退避重试（上限 30s）直到成功。
// 未就绪期间 S3Enabled() 为 false，镜像上传/捕获直接报错，而不是改走 legacy 本地路径；
// 配置错误不重试，记录到 s3ConfigErr，接口据此提示检查配置而不是“稍后重试”
func initS3WithRetry() {
	const maxDelay = 30 * time.Second
	delay := 5 * time.Second
	for {
		err := InitS3()
		if err == nil {
			return
		}
		if errors.Is(err, errS3Config) {
			s3ConfigErr.Store(&err)
			logger.Errorf("S3 image store misconfigured, fix S3_ENDPOINT and restart clapi: %v", err)
			return
		}
		logger.Errorf("S3 image store not ready, retry in %s: %v", delay, err)
		time.Sleep(delay)
		delay = min(delay*2, maxDelay)
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
	// 8255 only serves HTTPS: a plain http:// URL made every capture upload fail with 400
	viper.Set("clapi.internal_url", "https://"+host+":8255/api/v1")
}
