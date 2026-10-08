package services

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"

	"cpgateway/src/dbs"
	"cpgateway/src/model"
	"cpgateway/src/tracing"
)

func configInt(key string, def int) int {
	if v := viper.GetInt(key); v > 0 {
		return v
	}
	return def
}

// StartHeartbeat launches the background region heartbeat loop.
func StartHeartbeat() {
	go heartbeatLoop()
}

func heartbeatLoop() {
	log.Info("Starting Region heartbeat background task...")
	time.Sleep(5 * time.Second)

	for {
		func() {
			// 每轮心跳是一个后台任务 trace（按 TRACING_BACKGROUND_SAMPLE_RATIO 采样），推送到 Region 的请求挂在其下
			ctx, span := tracing.StartBackground(context.Background(), "region.heartbeat")
			defer span.End()
			defer func() {
				if r := recover(); r != nil {
					log.WithContext(ctx).Errorf("Error in heartbeat loop: %v", r)
				}
			}()
			checkAllRegions(ctx)
		}()
		time.Sleep(time.Duration(configInt("heartbeat.interval", 60)) * time.Second)
	}
}

func checkAllRegions(ctx context.Context) {
	var regions []model.Region
	if err := dbs.DBContext(ctx).Where("maintenance_mode = ?", false).Find(&regions).Error; err != nil {
		log.WithContext(ctx).Errorf("Error in heartbeat loop: %v", err)
		return
	}
	if len(regions) == 0 {
		return
	}
	forEachRegion(regions, func(r *model.Region) { checkRegionHealth(ctx, r) })
}

// checkRegionHealth probes {endpoint}/api/v1/version and updates availability.
// One success brings a region online; OFFLINE_THRESHOLD consecutive failures take it offline.
func checkRegionHealth(ctx context.Context, region *model.Region) {
	if region.MaintenanceMode {
		return
	}

	timeout := configInt("heartbeat.timeout", 5)
	threshold := configInt("heartbeat.offline_threshold", 3)
	healthURL := strings.TrimRight(region.InternalEndpoint, "/") + "/api/v1/version"
	start := time.Now().UTC()

	success := false
	errorMsg := ""
	var resp *http.Response
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err == nil {
		resp, err = InsecureClient(time.Duration(timeout) * time.Second).Do(req)
	}
	if err != nil {
		errorMsg = fmt.Sprintf("Network error: %v", err)
	} else {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			success = true
		} else {
			errorMsg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
	}

	updates := map[string]interface{}{"last_check_at": start}
	wasUnavailable := !region.IsAvailable
	if success {
		if wasUnavailable {
			log.WithContext(ctx).Infof("Region '%s' heartbeats recovered. Marking as available.", region.Name)
		}
		updates["is_available"] = true
		updates["fail_count"] = 0
		updates["status_message"] = "Healthy"
	} else {
		failCount := region.FailCount + 1
		updates["fail_count"] = failCount
		updates["status_message"] = errorMsg
		if failCount >= threshold {
			if region.IsAvailable {
				log.WithContext(ctx).Warnf("Region '%s' failed %d times. Marking as unavailable. Reason: %s", region.Name, failCount, errorMsg)
			}
			updates["is_available"] = false
		} else {
			log.WithContext(ctx).Debugf("Region '%s' check failed (%d/%d): %s", region.Name, failCount, threshold, errorMsg)
		}
	}

	if err := dbs.DBContext(ctx).Model(&model.Region{}).Where("id = ?", region.ID).Updates(updates).Error; err != nil {
		log.WithContext(ctx).Errorf("Heartbeat: failed to update region '%s': %v", region.Name, err)
		return
	}

	// Cold-start compensation: the region may have missed changes while offline.
	if success && wasUnavailable {
		go ProvisionRegion(context.WithoutCancel(ctx), region.ID, region.Name)
	}
}
