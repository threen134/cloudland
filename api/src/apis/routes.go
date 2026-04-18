/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"context"
	"time"

	_ "api/docs/routes"
	. "api/src/common"
	"api/src/services"
	"api/src/utils/log"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

var logger = log.MustGetLogger("apis")

const defaultAlarmEventRetentionDays = 30

func startAlarmEventCleanup() {
	admin := &services.NotificationAdmin{}
	ticker := time.NewTicker(24 * time.Hour)
	go func() {
		// 等待服务完全启动后再执行首次清理
		time.Sleep(10 * time.Second)
		runAlarmEventCleanup(admin)
		for range ticker.C {
			runAlarmEventCleanup(admin)
		}
	}()
}

func runAlarmEventCleanup(admin *services.NotificationAdmin) {
	retentionDays := services.GetMirrorSettingInt("ALARM_EVENT_RETENTION_DAYS", defaultAlarmEventRetentionDays)
	if retentionDays < 1 {
		retentionDays = defaultAlarmEventRetentionDays
	}
	ctx := context.Background()
	ctx = SetContextDB(ctx, DB())
	deleted, err := admin.CleanupExpiredAlarmEvents(ctx, retentionDays)
	if err != nil {
		logger.Errorf("Failed to cleanup expired alarm events: %v", err)
		return
	}
	if deleted > 0 {
		logger.Infof("Cleaned up %d expired alarm events (older than %d days)", deleted, retentionDays)
	}
}

func Run() (err error) {
	logger.Info("Starting cloudland api daemon...")
	startAlarmEventCleanup()
	r := Register()
	cert := viper.GetString("rest.cert")
	key := viper.GetString("rest.key")
	listen := viper.GetString("rest.listen")
	if listen == "" {
		listen = ":8080" // Default port if not specified
		logger.Warningf("rest.listen not set, using default port %s", listen)
	}

	logger.Infof("Server configuration: cert=%s, key=%s, listen=%s", cert, key, listen)
	if cert != "" && key != "" {
		logger.Infof("Running HTTPS service on %s", listen)
		err = r.RunTLS(listen, cert, key)
	} else {
		logger.Infof("Running HTTP service on %s", listen)
		err = r.Run(listen)
	}

	if err != nil {
		logger.Errorf("Web server failed to start: %v", err)
	}
	return
}

// @title CloudLand API
// @version 1.0
// @description APIs for CloudLand Functions
// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html
// @BasePath /api/v1
func Register() (r *gin.Engine) {
	r = gin.New()

	// 从网关、代理获取真实的 ClientIP
	r.ForwardedByClientIP = true
	r.SetTrustedProxies(nil)

	r.Use(gin.Recovery())
	r.Use(log.RequestID())
	r.Use(log.Logger())

	apiV1 := "/api/v1"
	v1 := r.Group(apiV1)
	v1.GET("/version", versionAPI.Get)
	v1.POST("/alerts/process", notificationAPI.ProcessAlertWebhookV2)
	v1.POST("/alerts/resource-adjustment", adjustAPI.ProcessResourceAdjustmentWebhook)

	// Prometheus http_sd_configs endpoint (无需认证，供 Prometheus 自动发现)
	v1.GET("/prometheus/sd/:exporter", prometheusSDAPI.GetTargets)

	// capture 镜像上传 (compute → clapi → MinIO)
	// 鉴权由 handler 内的 HMAC token 校验承担；compute 节点无 JWT，故不进 authGroup
	v1.POST("/internal/images/:id/upload", imageAPI.UploadCapture)

	authGroup := v1.Group("").Use(Authorize())
	{
		authGroup.GET("/zones", zoneAPI.List)
		authGroup.POST("/zones", zoneAPI.Create)
		authGroup.GET("/zones/:name", zoneAPI.Get)
		authGroup.DELETE("/zones/:name", zoneAPI.Delete)
		authGroup.PATCH("/zones/:name", zoneAPI.Patch)

		authGroup.GET("/hypers", hyperAPI.List)
		authGroup.POST("/hypers", hyperAPI.Deploy)
		authGroup.GET("/hypers/:uuid", hyperAPI.Get)
		authGroup.DELETE("/hypers/:uuid", hyperAPI.Delete)
		authGroup.PATCH("/hypers/:uuid", hyperAPI.Patch)
		authGroup.POST("/hypers/:uuid/maintain", hyperAPI.Maintain)

		authGroup.GET("/migrations", migrationAPI.List)
		authGroup.POST("/migrations", migrationAPI.Create)
		authGroup.GET("/migrations/:id", migrationAPI.Get)

		authGroup.GET("/vpcs", vpcAPI.List)
		authGroup.POST("/vpcs", vpcAPI.Create)
		authGroup.GET("/vpcs/:id", vpcAPI.Get)
		authGroup.DELETE("/vpcs/:id", vpcAPI.Delete)
		authGroup.PATCH("/vpcs/:id", vpcAPI.Patch)

		authGroup.GET("/dictionaries", dictionaryAPI.List)
		authGroup.POST("/dictionaries", dictionaryAPI.Create)
		authGroup.GET("/dictionaries/:id", dictionaryAPI.Get)
		authGroup.DELETE("/dictionaries/:id", dictionaryAPI.Delete)
		authGroup.PATCH("/dictionaries/:id", dictionaryAPI.Patch)

		authGroup.GET("/ip_groups", ipGroupAPI.List)
		authGroup.POST("/ip_groups", ipGroupAPI.Create)
		authGroup.GET("/ip_groups/:id", ipGroupAPI.Get)
		authGroup.DELETE("/ip_groups/:id", ipGroupAPI.Delete)
		authGroup.PATCH("/ip_groups/:id", ipGroupAPI.Patch)

		authGroup.GET("/subnets", subnetAPI.List)
		authGroup.POST("/subnets", subnetAPI.Create)
		authGroup.GET("/subnets/:id", subnetAPI.Get)
		authGroup.DELETE("/subnets/:id", subnetAPI.Delete)
		authGroup.PATCH("/subnets/:id", subnetAPI.Patch)

		authGroup.GET("/security_groups", secgroupAPI.List)
		authGroup.POST("/security_groups", secgroupAPI.Create)
		authGroup.GET("/security_groups/:id", secgroupAPI.Get)
		authGroup.DELETE("/security_groups/:id", secgroupAPI.Delete)
		authGroup.PATCH("/security_groups/:id", secgroupAPI.Patch)

		authGroup.GET("/security_groups/:id/rules", secruleAPI.List)
		authGroup.POST("/security_groups/:id/rules", secruleAPI.Create)
		authGroup.GET("/security_groups/:id/rules/:rule_id", secruleAPI.Get)
		authGroup.DELETE("/security_groups/:id/rules/:rule_id", secruleAPI.Delete)
		authGroup.PATCH("/security_groups/:id/rules/:rule_id", secruleAPI.Patch)

		authGroup.GET("/load_balancers", loadBalancerAPI.List)
		authGroup.POST("/load_balancers", loadBalancerAPI.Create)
		authGroup.GET("/load_balancers/:id", loadBalancerAPI.Get)
		authGroup.DELETE("/load_balancers/:id", loadBalancerAPI.Delete)
		authGroup.PATCH("/load_balancers/:id", loadBalancerAPI.Patch)

		authGroup.GET("/load_balancers/:id/floating_ips", lbFloatingIpAPI.List)
		authGroup.POST("/load_balancers/:id/floating_ips", lbFloatingIpAPI.Create)
		authGroup.GET("/load_balancers/:id/floating_ips/:floating_ip_id", lbFloatingIpAPI.Get)
		authGroup.DELETE("/load_balancers/:id/floating_ips/:floating_ip_id", lbFloatingIpAPI.Delete)

		authGroup.GET("/load_balancers/:id/listeners", listenerAPI.List)
		authGroup.POST("/load_balancers/:id/listeners", listenerAPI.Create)
		authGroup.GET("/load_balancers/:id/listeners/:listener_id", listenerAPI.Get)
		authGroup.DELETE("/load_balancers/:id/listeners/:listener_id", listenerAPI.Delete)

		authGroup.GET("/load_balancers/:id/listeners/:listener_id/backends", backendAPI.List)
		authGroup.POST("/load_balancers/:id/listeners/:listener_id/backends", backendAPI.Create)
		authGroup.GET("/load_balancers/:id/listeners/:listener_id/backends/:backend_id", backendAPI.Get)
		authGroup.DELETE("/load_balancers/:id/listeners/:listener_id/backends/:backend_id", backendAPI.Delete)

		authGroup.GET("/floating_ips", floatingIpAPI.List)
		authGroup.POST("/floating_ips", floatingIpAPI.Create)
		authGroup.GET("/floating_ips/:id", floatingIpAPI.Get)
		authGroup.DELETE("/floating_ips/:id", floatingIpAPI.Delete)
		authGroup.PATCH("/floating_ips/:id", floatingIpAPI.Patch)
		authGroup.POST("/floating_ips/site_attach", floatingIpAPI.SiteAttach)
		authGroup.POST("/floating_ips/site_detach", floatingIpAPI.SiteDetach)

		// Addresses
		authGroup.PATCH("/addresses/remark", addressAPI.Remark)
		authGroup.PATCH("/addresses/update-lock", addressAPI.UpdateLock)
		authGroup.GET("/addresses/:uuid", addressAPI.ListIpBySubnetUUID)

		authGroup.GET("/keys", keyAPI.List)
		authGroup.POST("/keys", keyAPI.Create)
		authGroup.GET("/keys/:id", keyAPI.Get)
		authGroup.DELETE("/keys/:id", keyAPI.Delete)

		authGroup.GET("/flavors", flavorAPI.List)
		authGroup.POST("/flavors", flavorAPI.Create)
		authGroup.GET("/flavors/:name", flavorAPI.Get)
		authGroup.DELETE("/flavors/:name", flavorAPI.Delete)

		authGroup.GET("/images", imageAPI.List)
		authGroup.POST("/images", imageAPI.Create)
		authGroup.GET("/images/:id", imageAPI.Get)
		authGroup.DELETE("/images/:id", imageAPI.Delete)
		authGroup.PATCH("/images/:id", imageAPI.Patch)
		authGroup.GET("/images/:id/storages", imageAPI.ListStorages)

		authGroup.GET("/volumes", volumeAPI.List)
		authGroup.POST("/volumes", volumeAPI.Create)
		authGroup.GET("/volumes/:id", volumeAPI.Get)
		authGroup.DELETE("/volumes/:id", volumeAPI.Delete)
		authGroup.PATCH("/volumes/:id", volumeAPI.Patch)
		authGroup.POST("/volumes/:id/resize", volumeAPI.Resize)
		authGroup.PUT("/volumes/:id/qos", volumeAPI.UpdateQos)

		authGroup.GET("/backups", volBackupAPI.List)
		authGroup.POST("/backups", volBackupAPI.Create)
		authGroup.GET("/backups/:id", volBackupAPI.Get)
		authGroup.DELETE("/backups/:id", volBackupAPI.Delete)
		authGroup.POST("/backups/:id/restore", volBackupAPI.Restore)

		authGroup.GET("/consistency_groups", consistencyGroupAPI.List)
		authGroup.POST("/consistency_groups", consistencyGroupAPI.Create)
		authGroup.GET("/consistency_groups/:id", consistencyGroupAPI.Get)
		authGroup.PATCH("/consistency_groups/:id", consistencyGroupAPI.Patch)
		authGroup.DELETE("/consistency_groups/:id", consistencyGroupAPI.Delete)
		authGroup.POST("/consistency_groups/:id/volumes", consistencyGroupAPI.AddVolumes)
		authGroup.DELETE("/consistency_groups/:id/volumes/:volume_id", consistencyGroupAPI.RemoveVolume)

		// CG Snapshots
		authGroup.GET("/consistency_groups/:id/snapshots", consistencyGroupAPI.ListSnapshots)
		authGroup.POST("/consistency_groups/:id/snapshots", consistencyGroupAPI.CreateSnapshot)
		authGroup.GET("/consistency_groups/:id/snapshots/:snap_id", consistencyGroupAPI.GetSnapshot)
		authGroup.DELETE("/consistency_groups/:id/snapshots/:snap_id", consistencyGroupAPI.DeleteSnapshot)
		authGroup.POST("/consistency_groups/:id/snapshots/:snap_id/restore", consistencyGroupAPI.RestoreSnapshot)

		authGroup.GET("/instances", instanceAPI.List)
		authGroup.POST("/instances", instanceAPI.Create)
		authGroup.GET("/instances/:id", instanceAPI.Get)
		authGroup.DELETE("/instances/:id", instanceAPI.Delete)
		authGroup.PATCH("/instances/:id", instanceAPI.Patch)
		authGroup.GET("/instances/rules", instanceAPI.GetInstanceRuleLinks)

		authGroup.POST("/instances/:id/set_user_password", instanceAPI.SetUserPassword)
		authGroup.POST("/instances/:id/console", consoleAPI.Create)
		authGroup.POST("/instances/:id/reinstall", instanceAPI.Reinstall)
		authGroup.POST("/instances/:id/resize", instanceAPI.Resize)
		authGroup.POST("/instances/:id/rescue", instanceAPI.Rescue)
		authGroup.POST("/instances/:id/end_rescue", instanceAPI.EndRescue)

		authGroup.GET("/instances/:id/interfaces", interfaceAPI.List)
		authGroup.POST("/instances/:id/interfaces", interfaceAPI.Create)
		authGroup.GET("/instances/:id/interfaces/:interface_id", interfaceAPI.Get)
		authGroup.DELETE("/instances/:id/interfaces/:interface_id", interfaceAPI.Delete)
		authGroup.PATCH("/instances/:id/interfaces/:interface_id", interfaceAPI.Patch)

		authGroup.GET("/tasks", taskAPI.List)
		authGroup.GET("/tasks/:id", taskAPI.Get)

		metricsGroup := v1.Group("/metrics").Use(Authorize())
		{
			metricsGroup.POST("/instances/cpu/his_data", monitorAPI.GetCPU)
			metricsGroup.POST("/instances/disk/his_data", monitorAPI.GetDisk)
			metricsGroup.POST("/instances/memory/his_data", monitorAPI.GetMemory)
			metricsGroup.POST("/instances/network/his_data", monitorAPI.GetNetwork)
			metricsGroup.POST("/instances/traffic/his_data", monitorAPI.GetTraffic)
			metricsGroup.POST("/instances/volume/his_data", monitorAPI.GetVolume)

			metricsGroup.POST("/hypers/cpu/his_data", monitorAPI.GetHyperCPU)
			metricsGroup.POST("/hypers/memory/his_data", monitorAPI.GetHyperMemory)

			metricsGroup.POST("/alarm/cpu/rules", alarmAPI.CreateCPURule)
			metricsGroup.GET("/alarm/cpu/rules", alarmAPI.GetCPURules)
			metricsGroup.GET("/alarm/active-rules", alarmAPI.GetActiveRules)
			metricsGroup.GET("/alarm/cpu/rule/:uuid", alarmAPI.GetCPURules)
			metricsGroup.DELETE("/alarm/cpu/rule/:uuid", alarmAPI.DeleteCPURule)

			metricsGroup.POST("/alarm/memory/rules", alarmAPI.CreateMemoryRule)
			metricsGroup.GET("/alarm/memory/rules", alarmAPI.GetMemoryRules)
			metricsGroup.GET("/alarm/memory/rule/:uuid", alarmAPI.GetMemoryRules)
			metricsGroup.DELETE("/alarm/memory/rule/:uuid", alarmAPI.DeleteMemoryRule)

			metricsGroup.POST("/alarm/bw/rules", alarmAPI.CreateBWRule)
			metricsGroup.GET("/alarm/bw/rules", alarmAPI.GetBWRules)
			metricsGroup.GET("/alarm/bw/rule/:uuid", alarmAPI.GetBWRules)
			metricsGroup.DELETE("/alarm/bw/rule/:uuid", alarmAPI.DeleteBWRules)

			// Add new endpoint for synchronizing VM rule mappings
			metricsGroup.POST("/alarm/sync-mappings", alarmAPI.SyncAllVMRuleMappings)

			metricsGroup.GET("/current-alarms", alarmAPI.GetCurrentAlarms)
			metricsGroup.GET("/history-alarms", alarmAPI.GetHistoryAlarm)
			metricsGroup.POST("/alarm/:id/enable", alarmAPI.ToggleRuleStatus("alarm", "enable"))
			metricsGroup.POST("/alarm/:id/disable", alarmAPI.ToggleRuleStatus("alarm", "disable"))
			metricsGroup.POST("/alarm/link", alarmAPI.LinkRuleToVMWithType("alarm"))
			metricsGroup.POST("/alarm/unlink", alarmAPI.UnlinkRuleFromVMWithType("alarm"))

			// Resource auto adjustment route
			metricsGroup.POST("/adjust/cpu/rules", adjustAPI.CreateCPUAdjustRule)
			metricsGroup.GET("/adjust/cpu/rules", adjustAPI.GetCPUAdjustRules)
			metricsGroup.GET("/adjust/cpu/rule/:uuid", adjustAPI.GetCPUAdjustRules)
			metricsGroup.PATCH("/adjust/cpu/rule/:uuid", adjustAPI.PatchCPUAdjustRule)
			metricsGroup.DELETE("/adjust/cpu/rule/:uuid", adjustAPI.DeleteCPUAdjustRule)

			// Bandwidth auto adjustment route
			metricsGroup.POST("/adjust/bw/rules", adjustAPI.CreateBWAdjustRule)
			metricsGroup.GET("/adjust/bw/rules", adjustAPI.GetBWAdjustRules)
			metricsGroup.GET("/adjust/bw/rule/:uuid", adjustAPI.GetBWAdjustRules)
			metricsGroup.PATCH("/adjust/bw/rule/:uuid", adjustAPI.PatchBWAdjustRule)
			metricsGroup.DELETE("/adjust/bw/rule/:uuid", adjustAPI.DeleteBWAdjustRule)

			// Enable/disable resource adjustment rules
			metricsGroup.POST("/adjust/:uuid/enable", alarmAPI.ToggleRuleStatus("adjust", "enable"))
			metricsGroup.POST("/adjust/:uuid/disable", alarmAPI.ToggleRuleStatus("adjust", "disable"))

			// VM adjust rule link management
			metricsGroup.POST("/adjust/link", alarmAPI.LinkRuleToVMWithType("adjust"))
			metricsGroup.DELETE("/adjust/unlink", alarmAPI.UnlinkRuleFromVMWithType("adjust"))

			// Batch get rules (supports both alarm and adjust rules)
			metricsGroup.POST("/rules/batch", alarmAPI.BatchGetRules)
		}

		authGroup.POST("/node-alarm-rules", alarmAPI.CreateNodeAlarmRule)
		authGroup.GET("/node-alarm-rules", alarmAPI.GetNodeAlarmRules)
		authGroup.DELETE("/node-alarm-rules/:uuid", alarmAPI.DeleteNodeAlarmRule)

		// OpenMeter API routes
		authGroup.GET("/openmeter/metrics", openMeterAPI.QueryOpenMeterMetrics)
		authGroup.GET("/openmeter/metrics/:instance_id/:subject", openMeterAPI.QueryInstanceMetricsBySubject)
		authGroup.GET("/openmeter/subjects", openMeterAPI.GetAvailableSubjects)

		// Unified rule links query (supports both alarm and adjust rules)
		authGroup.GET("/rules/links", adjustAPI.GetRuleLinks)

		// Bandwidth configuration metrics regeneration
		authGroup.POST("/adjust/regenerate-bandwidth-metrics", adjustAPI.RegenerateBandwidthConfigMetrics)

		// --- 通知渠道 & 告警事件 ---

		// 内部同步接口（CPGateway 推送通知渠道变更）
		authGroup.POST("/internal/notification-channels/sync", notificationAPI.SyncChannel)

		// 内部同步接口（CPGateway 推送系统设置）
		authGroup.POST("/internal/system-settings/sync", systemSettingAPI.SyncSystemSettings)

		// 运行时基础设施配置只读查询（CPGateway Infrastructure tab 透传）
		authGroup.GET("/internal/runtime-config", runtimeConfigAPI.GetRuntimeConfig)
		authGroup.POST("/internal/runtime-config/test-s3", runtimeConfigAPI.TestS3)

		// 内部同步接口（CPGateway 推送 org 记录，保持 organizations 表一致）
		authGroup.POST("/internal/orgs/sync", SyncOrg)

		// 内部告警事件查询（CPGateway 全局汇总用）
		authGroup.GET("/internal/alarm/events", notificationAPI.InternalListAlarmEvents)

		// 告警规则渠道绑定
		authGroup.POST("/alarm/rule-channels", notificationAPI.BindRuleChannels)
		authGroup.GET("/alarm/rule-channels/:uuid", notificationAPI.GetRuleChannels)

		// 告警事件查询
		authGroup.GET("/alarm/events", notificationAPI.ListAlarmEvents)
		authGroup.GET("/alarm/events/:event_uuid/delivery-logs", notificationAPI.GetAlarmDeliveryLogs)
	}

	r.GET("/swagger"+apiV1+"/*any", swaggerHandler())
	return
}
