package services

import (
	"context"
	"encoding/json"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"cpgateway/src/dbs"
	"cpgateway/src/model"
)

// ParseChannelConfig decodes the stored JSON config into an object.
func ParseChannelConfig(raw string) map[string]interface{} {
	cfg := map[string]interface{}{}
	if raw != "" {
		json.Unmarshal([]byte(raw), &cfg)
	}
	return cfg
}

// channelSyncData is a channel as regions receive it. The owner goes by the organization's UUID: a region
// numbers its organizations itself, so this gateway's organization ID means nothing there. It used to be
// sent (org_id) and stored as is, which bound the channels of every organization but the first to the
// wrong organization in the region.
func channelSyncData(ch *model.NotificationChannel, orgUUID string) map[string]interface{} {
	return map[string]interface{}{
		"uuid":     ch.UUID,
		"org_uuid": orgUUID,
		"name":     ch.Name,
		"type":     ch.Type,
		"config":   ParseChannelConfig(ch.Config),
		"enabled":  ch.Enabled,
	}
}

// orgUUIDsByID maps the given organization IDs to their UUIDs. Deleted organizations are left out.
func orgUUIDsByID(db *gorm.DB, orgIDs []int64) (map[int64]string, error) {
	uuids := map[int64]string{}
	if len(orgIDs) == 0 {
		return uuids, nil
	}
	var orgs []model.Organization
	if err := db.Select("id", "uuid").Where("id IN ?", orgIDs).Find(&orgs).Error; err != nil {
		return nil, err
	}
	for _, o := range orgs {
		if o.UUID != "" {
			uuids[o.ID] = o.UUID
		}
	}
	return uuids, nil
}

// channelSyncList builds the bulk sync list. A channel whose organization is gone (deleted, so absent from
// orgUUIDs) is left out: regions drop what the list does not name, and nobody can use it any more.
func channelSyncList(channels []model.NotificationChannel, orgUUIDs map[int64]string) (items []map[string]interface{}, skipped []string) {
	items = make([]map[string]interface{}, 0, len(channels))
	for i := range channels {
		orgUUID, ok := orgUUIDs[channels[i].OrgID]
		if !ok {
			skipped = append(skipped, channels[i].UUID)
			continue
		}
		items = append(items, channelSyncData(&channels[i], orgUUID))
	}
	return items, skipped
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
	db := dbs.DBContext(ctx)
	var ch model.NotificationChannel
	if err := db.Where("uuid = ?", channelUUID).First(&ch).Error; err != nil {
		return
	}
	orgUUIDs, err := orgUUIDsByID(db, []int64{ch.OrgID})
	if err != nil {
		log.WithContext(ctx).Errorf("Failed to look up the organization of channel %s: %v", channelUUID, err)
		return
	}
	orgUUID, ok := orgUUIDs[ch.OrgID]
	if !ok {
		log.WithContext(ctx).Warnf("Channel %s not synced: its organization %d no longer exists", channelUUID, ch.OrgID)
		return
	}
	pushChannelPayloadToAllRegions(ctx, map[string]interface{}{
		"action":  "upsert",
		"channel": channelSyncData(&ch, orgUUID),
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
	if err := db.Find(&channels).Error; err != nil {
		// An empty list would make the region drop every channel it has
		log.WithContext(ctx).Errorf("Full channel sync to region '%s' skipped: failed to list channels: %v", region.Name, err)
		return
	}
	orgIDs := make([]int64, 0, len(channels))
	for i := range channels {
		orgIDs = append(orgIDs, channels[i].OrgID)
	}
	orgUUIDs, err := orgUUIDsByID(db, orgIDs)
	if err != nil {
		log.WithContext(ctx).Errorf("Full channel sync to region '%s' skipped: failed to look up organizations: %v", region.Name, err)
		return
	}
	items, skipped := channelSyncList(channels, orgUUIDs)
	if len(skipped) > 0 {
		log.WithContext(ctx).Warnf("Full channel sync to region '%s': %d channels of deleted organizations left out: %v", region.Name, len(skipped), skipped)
	}
	payload := map[string]interface{}{"action": "bulk_sync", "channels": items}
	if err := pushToRegion(ctx, region, "/internal/notification-channels/sync", payload); err != nil {
		log.WithContext(ctx).Errorf("Full channel sync to region '%s' failed: %v", region.Name, err)
		return
	}
	log.WithContext(ctx).Infof("Full channel sync to region '%s' completed: %d channels", region.Name, len(items))
}
