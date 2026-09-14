package services

import (
	"context"
	"sync"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
)

// forEachRegion runs fn concurrently for every region and waits for all to finish.
func forEachRegion(regions []model.Region, fn func(r *model.Region)) {
	var wg sync.WaitGroup
	for i := range regions {
		wg.Add(1)
		go func(r *model.Region) {
			defer wg.Done()
			fn(r)
		}(&regions[i])
	}
	wg.Wait()
}

func orgSyncPayload(id int64, name, slug string) map[string]interface{} {
	return map[string]interface{}{"id": id, "name": name, "slug": slug}
}

// SyncOrgToAllRegions pushes one org ({id, name, slug}) to all sync target regions.
func SyncOrgToAllRegions(ctx context.Context, orgID int64, name, slug string) {
	regions := SyncTargetRegions(dbs.DBContext(ctx))
	if len(regions) == 0 {
		return
	}
	payload := orgSyncPayload(orgID, name, slug)
	forEachRegion(regions, func(r *model.Region) {
		if err := pushToRegion(ctx, r, "/internal/orgs/sync", payload); err != nil {
			log.WithContext(ctx).Errorf("Failed to sync org '%s' (id=%d) to region '%s': %v", name, orgID, r.Name, err)
		}
	})
}

// SyncAllOrgsToRegion pushes every non-deleted org to a single region.
func SyncAllOrgsToRegion(ctx context.Context, db *gorm.DB, region *model.Region) {
	var orgs []model.Organization
	if err := db.Find(&orgs).Error; err != nil {
		log.WithContext(ctx).Errorf("Failed to list orgs for region '%s': %v", region.Name, err)
		return
	}
	if len(orgs) == 0 {
		return
	}

	var wg sync.WaitGroup
	for i := range orgs {
		wg.Add(1)
		go func(o *model.Organization) {
			defer wg.Done()
			if err := pushToRegion(ctx, region, "/internal/orgs/sync", orgSyncPayload(o.ID, o.Name, o.Slug)); err != nil {
				log.WithContext(ctx).Errorf("Failed to sync org '%s' (id=%d) to new region '%s': %v", o.Name, o.ID, region.Name, err)
			}
		}(&orgs[i])
	}
	wg.Wait()
	log.WithContext(ctx).Infof("Org sync to new region '%s' completed: %d orgs", region.Name, len(orgs))
}
