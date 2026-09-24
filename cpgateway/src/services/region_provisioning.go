package services

import (
	"context"
	log "github.com/sirupsen/logrus"

	"cpgateway/src/dbs"
	"cpgateway/src/model"
)

// ProvisionRegion pushes notification channels, system settings (forced) and all orgs
// to a region. Used after region creation, manual bring-online and heartbeat recovery.
func ProvisionRegion(ctx context.Context, regionID int64, regionName string) {
	defer func() {
		if r := recover(); r != nil {
			log.WithContext(ctx).Errorf("Failed to provision region '%s': %v", regionName, r)
		}
	}()

	db := dbs.DBContext(ctx)
	var region model.Region
	if err := db.Where("id = ?", regionID).First(&region).Error; err != nil {
		return
	}

	PushAllChannelsToRegion(ctx, db, &region)
	log.WithContext(ctx).Infof("Provisioned notification channels to region '%s'", regionName)
	PushSettingsToRegion(ctx, db, &region)
	log.WithContext(ctx).Infof("Provisioned system settings to region '%s'", regionName)
	SyncAllOrgsToRegion(ctx, db, &region)
	log.WithContext(ctx).Infof("Provisioned orgs to region '%s'", regionName)
}
