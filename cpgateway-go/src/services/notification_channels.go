package services

import (
	"context"
	"encoding/json"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
)

// ParseChannelConfig decodes the stored JSON config into an object.
func ParseChannelConfig(raw string) map[string]interface{} {
	cfg := map[string]interface{}{}
	if raw != "" {
		json.Unmarshal([]byte(raw), &cfg)
	}
	return cfg
}

func channelSyncData(ch *model.NotificationChannel) map[string]interface{} {
	return map[string]interface{}{
		"uuid":    ch.UUID,
		"org_id":  ch.OrgID,
		"name":    ch.Name,
		"type":    ch.Type,
		"config":  ParseChannelConfig(ch.Config),
		"enabled": ch.Enabled,
	}
}

func pushChannelPayloadToAllRegions(ctx context.Context, payload map[string]interface{}) {
	regions := SyncTargetRegions(dbs.DBContext(ctx))
	if len(regions) == 0 {
		return
	}
	forEachRegion(regions, func(r *model.Region) {
		if err := pushToRegion(ctx, r, "/internal/notification-channels/sync", payload); err != nil {
			log.WithContext(ctx).Errorf("Failed to sync channel to region '%s': %v", r.Name, err)
		}
	})
}

// PushChannelUpsertToAllRegions reloads the channel and pushes an upsert.
func PushChannelUpsertToAllRegions(ctx context.Context, channelUUID string) {
	var ch model.NotificationChannel
	if err := dbs.DBContext(ctx).Where("uuid = ?", channelUUID).First(&ch).Error; err != nil {
		return
	}
	pushChannelPayloadToAllRegions(ctx, map[string]interface{}{
		"action":  "upsert",
		"channel": channelSyncData(&ch),
	})
}

func PushChannelDeleteToAllRegions(ctx context.Context, channelUUID string) {
	pushChannelPayloadToAllRegions(ctx, map[string]interface{}{
		"action":       "delete",
		"channel_uuid": channelUUID,
	})
}

// PushAllChannelsToRegion bulk-syncs all channels, including disabled ones:
// clapi's bulk_sync deletes channels missing from the list.
func PushAllChannelsToRegion(ctx context.Context, db *gorm.DB, region *model.Region) {
	var channels []model.NotificationChannel
	db.Find(&channels)

	items := make([]map[string]interface{}, 0, len(channels))
	for i := range channels {
		items = append(items, channelSyncData(&channels[i]))
	}
	payload := map[string]interface{}{"action": "bulk_sync", "channels": items}
	if err := pushToRegion(ctx, region, "/internal/notification-channels/sync", payload); err != nil {
		log.WithContext(ctx).Errorf("Full channel sync to region '%s' failed: %v", region.Name, err)
		return
	}
	log.WithContext(ctx).Infof("Full channel sync to region '%s' completed: %d channels", region.Name, len(channels))
}
