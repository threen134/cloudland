package services

import (
	"context"
	"sync"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"cpgateway/src/dbs"
	"cpgateway/src/model"
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

// 同步载荷以 UUID 为准：两侧组织表的自增主键各自独立，跨服务只能用全局唯一标识对应。
// 此前传的是自增 ID，区域侧强行拿它当主键插入，两边靠"都先建了一个 admin 组织"碰巧对齐
// org_type tells the region which org is the system org: the region links it to its own system org, every
// other org is matched by UUID only.
func orgSyncPayload(org *model.Organization) map[string]interface{} {
	return map[string]interface{}{"uuid": org.UUID, "name": org.Name, "slug": org.Slug, "org_type": int(org.OrgType)}
}

// The name only ends up on the tombstone a region keeps for an org it never received
func orgDeletePayload(org *model.Organization) map[string]interface{} {
	return map[string]interface{}{"uuid": org.UUID, "name": org.Name}
}

// SyncOrgToAllRegions pushes one org (uuid, name, slug, type) to all sync target regions. It is called after
// every change the regions need to know about: creation (by an admin or a self-registration), approval or
// activation (so an org whose earlier push failed gets another one) and renaming. The org is passed by value:
// callers run this in a goroutine. A failure is only logged; a region that was down gets every org again when
// it is provisioned.
func SyncOrgToAllRegions(ctx context.Context, org model.Organization) {
	regions := SyncTargetRegions(dbs.DBContext(ctx))
	if len(regions) == 0 {
		return
	}
	payload := orgSyncPayload(&org)
	forEachRegion(regions, func(r *model.Region) {
		if err := pushToRegion(ctx, r, "/internal/orgs/sync", payload); err != nil {
			log.WithContext(ctx).Errorf("Failed to sync org '%s' (uuid=%s) to region '%s': %v", org.Name, org.UUID, r.Name, err)
		}
	})
}

// DeleteOrgFromAllRegions tells all sync target regions that an org was deleted, so its UUID stops resolving
// there and its slug is free for a new org. A region answers 409 while the org still owns resources in it;
// the region is told again whenever it is provisioned.
func DeleteOrgFromAllRegions(ctx context.Context, org model.Organization) {
	regions := SyncTargetRegions(dbs.DBContext(ctx))
	if len(regions) == 0 {
		return
	}
	payload := orgDeletePayload(&org)
	forEachRegion(regions, func(r *model.Region) {
		if err := pushToRegion(ctx, r, "/internal/orgs/delete", payload); err != nil {
			log.WithContext(ctx).Errorf("Failed to delete org uuid=%s from region '%s': %v", org.UUID, r.Name, err)
		}
	})
}

// SyncAllOrgsToRegion reconciles the orgs of a single region: deleted orgs are removed first (so that their
// slugs are free before the live orgs that reuse them arrive), then every live org is pushed.
func SyncAllOrgsToRegion(ctx context.Context, db *gorm.DB, region *model.Region) {
	var deleted []model.Organization
	if err := db.Unscoped().Where("deleted_at IS NOT NULL").Find(&deleted).Error; err != nil {
		log.WithContext(ctx).Errorf("Failed to list deleted orgs for region '%s': %v", region.Name, err)
	}
	var orgs []model.Organization
	if err := db.Find(&orgs).Error; err != nil {
		log.WithContext(ctx).Errorf("Failed to list orgs for region '%s': %v", region.Name, err)
		return
	}

	pushAll := func(list []model.Organization, path string, payload func(*model.Organization) map[string]interface{}) {
		var wg sync.WaitGroup
		for i := range list {
			wg.Add(1)
			go func(o *model.Organization) {
				defer wg.Done()
				if err := pushToRegion(ctx, region, path, payload(o)); err != nil {
					log.WithContext(ctx).Errorf("Failed to push %s for org '%s' (id=%d) to region '%s': %v", path, o.Name, o.ID, region.Name, err)
				}
			}(&list[i])
		}
		wg.Wait()
	}
	pushAll(deleted, "/internal/orgs/delete", orgDeletePayload)
	pushAll(orgs, "/internal/orgs/sync", orgSyncPayload)
	log.WithContext(ctx).Infof("Org sync to region '%s' completed: %d orgs, %d deleted orgs", region.Name, len(orgs), len(deleted))
}
