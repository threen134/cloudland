package services

import (
	"context"
	log "github.com/sirupsen/logrus"

	"cpgateway/src/dbs"
	"cpgateway/src/model"
)

// ProvisionRegion pushes the orgs (deleted ones first, then the live ones), system settings (forced) and
// notification channels to a region. Used after region creation, manual bring-online and heartbeat recovery.
// The orgs go first: channels are pushed with the UUID of their org and are only bound to it once the
// region knows that org.
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

	SyncAllOrgsToRegion(ctx, db, &region)
	log.WithContext(ctx).Infof("Provisioned orgs to region '%s'", regionName)
	PushSettingsToRegion(ctx, db, &region)
	log.WithContext(ctx).Infof("Provisioned system settings to region '%s'", regionName)
	PushAllChannelsToRegion(ctx, db, &region)
	log.WithContext(ctx).Infof("Provisioned notification channels to region '%s'", regionName)
}
