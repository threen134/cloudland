/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The copies of an image in the shared pools as an admin sees and drives them (shared-storage-design.md §9.6): which
// pools have one and how far their imports got, importing one ahead of the first boot disk (preheat), removing one no
// boot disk is cloned from. The imports report their progress through the heartbeat of their host
// (image_storage_progress); a host runs a few imports at a time, the next ones wait there for a slot, and clapi sends a
// new import to the host of the pool with the fewest running.

import (
	"context"
	"errors"
	"fmt"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Phases of an import as its host reports them
const (
	ImageStoragePhaseWait     = "wait"     // waits for an import slot of the host
	ImageStoragePhaseDownload = "download" // fetched into the cache of the host
	ImageStoragePhaseWrite    = "write"    // written into the pool
)

// ImageStorageView is a copy of an image in a pool with its pool, for the detail of the image
type ImageStorageView struct {
	Copy *model.ImageStorage
	Pool *model.StoragePool
	// Boot disks cloned from it
	Refs int64
	// Hostname of the host running the import or the removal
	Host string
}

// ImageStorageCopies lists the copies of an image in the shared pools
func ImageStorageCopies(ctx context.Context, image *model.Image) (views []*ImageStorageView, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	copies := []*model.ImageStorage{}
	if err = db.Where("image_id = ?", image.ID).Order("id").Find(&copies).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query the copies of the image", err)
	}
	names, _ := hyperAdmin.GetHyperNames(ctx)
	for _, is := range copies {
		pool := &model.StoragePool{}
		if err = db.Unscoped().Take(pool, is.StoragePoolID).Error; err != nil {
			continue
		}
		refs, rerr := imageStorageRefs(db, is.ID)
		if rerr != nil {
			return nil, NewCLError(ErrSQLSyntaxError, "Failed to count the boot disks of a copy", rerr)
		}
		views = append(views, &ImageStorageView{Copy: is, Pool: pool, Refs: refs, Host: names[is.Hostid]})
	}
	return views, nil
}

// PreheatImage imports an image into shared pools ahead of the first boot disk made there, so that one does not wait
// for the copy. A pool with a copy synced or being imported is left as it is; a failed copy is imported again
func PreheatImage(ctx context.Context, image *model.Image, pools []*model.StoragePool) (copies []*model.ImageStorage, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if image.Status != "available" {
		return nil, NewCLError(ErrImageNotAvailable, fmt.Sprintf("Image %s is %s", image.Name, image.Status), nil)
	}
	if image.IsRescue {
		return nil, NewCLError(ErrInvalidParameter, "A rescue image boots from the local cache, it has no copies in pools", nil)
	}
	for _, p := range pools {
		if !p.Shared() {
			return nil, NewCLError(ErrInvalidParameter, fmt.Sprintf("Storage pool %s is not shared: images are copied into shared pools only", p.Name), nil)
		}
	}
	db := dbs.DBContext(ctx)
	type sent struct {
		is  *model.ImageStorage
		cmd *volumeCommand
	}
	sends := []sent{}
	err = db.Transaction(func(tx *gorm.DB) error {
		txCtx := SetContextDB(ctx, tx)
		for _, p := range pools {
			is, cmd, perr := prepareImageStorage(txCtx, tx, p, image)
			if perr != nil {
				return perr
			}
			copies = append(copies, is)
			if cmd != nil {
				sends = append(sends, sent{is, cmd})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, s := range sends {
		sendImageStorageImport(ctx, s.is, s.cmd)
	}
	return copies, nil
}

// DropImageStorage removes the copy of an image in a pool that no boot disk is cloned from: preheated for nothing, or
// to make room. An import still running is not stopped: remove the copy once it is done
func DropImageStorage(ctx context.Context, image *model.Image, pool *model.StoragePool) (err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	var cmd *volumeCommand
	err = db.Transaction(func(tx *gorm.DB) error {
		is := &model.ImageStorage{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("image_id = ? AND storage_pool_id = ?", image.ID, pool.ID).Take(is).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return NewCLError(ErrImageCopyNotFound, fmt.Sprintf("Image %s has no copy in storage pool %s", image.Name, pool.Name), nil)
			}
			return NewCLError(ErrSQLSyntaxError, "Failed to query the copy of the image", err)
		}
		switch {
		case is.Status == model.ImageStorageDeleting:
			return nil
		case is.Status == model.ImageStorageSyncing && is.SentAt != nil && time.Since(*is.SentAt) < imageStorageImportTimeout:
			return NewCLError(ErrImageCopyInUse, fmt.Sprintf("The copy of image %s in storage pool %s is being imported, remove it once that is done", image.Name, pool.Name), nil)
		}
		var waiting int64
		if err := tx.Model(&model.ImageStorageWaiter{}).Where("image_storage_id = ?", is.ID).Count(&waiting).Error; err != nil {
			return err
		}
		refs, err := imageStorageRefs(tx, is.ID)
		if err != nil {
			return err
		}
		if refs > 0 || waiting > 0 {
			return NewCLError(ErrImageCopyInUse, fmt.Sprintf("%d boot disks are cloned from the copy of image %s in storage pool %s", refs+waiting, image.Name, pool.Name), nil)
		}
		if is.Status != model.ImageStorageSynced {
			// Never put in place, or failed: nothing to remove in the pool but a leftover, which the reconcile finds
			return tx.Delete(is).Error
		}
		if err := tx.Model(is).Updates(map[string]interface{}{"status": model.ImageStorageDeleting, "reason": ""}).Error; err != nil {
			return err
		}
		is.Status = model.ImageStorageDeleting
		cmd, err = imageStorageDeleteCommand(tx, is)
		// No host reaches the pool now: the maintenance sends the removal later
		if err != nil {
			logger.Ctx(ctx).Warningf("The removal of image copy %d waits: %v", is.ID, err)
		}
		return nil
	})
	if err != nil {
		return
	}
	sendVolumeCommand(ctx, cmd)
	return nil
}

// HandleImageStorageProgress takes the progress of an import its host reports: phase and percent of the phase. Only
// the host it was sent to, only while it runs
func HandleImageStorageProgress(ctx context.Context, hostid int32, id int64, phase string, percent int) error {
	switch phase {
	case ImageStoragePhaseWait, ImageStoragePhaseDownload, ImageStoragePhaseWrite:
	default:
		return fmt.Errorf("unknown import phase %q", phase)
	}
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	db := dbs.DBContext(ctx)
	// A report keeps the import alive: the timeout counts from the last one (sent_at), so time waiting for a slot of
	// the host or on a long download is not held against it; the host reports an unchanged phase every 10 minutes
	res := db.Model(&model.ImageStorage{}).Where("id = ? AND hostid = ? AND status = ?", id, hostid, model.ImageStorageSyncing).
		Updates(map[string]interface{}{"phase": phase, "progress": percent, "sent_at": time.Now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		logger.Ctx(ctx).Debugf("Progress of image copy %d from host %d ignored: not an import of that host", id, hostid)
	}
	return nil
}

// pickImportHost is the host of a pool that imports a copy: an online one that reaches the pool, the one with the
// fewest imports running so that a few large images do not queue on one host while the others are idle
func pickImportHost(tx *gorm.DB, pool *model.StoragePool) (int32, error) {
	rows := []*model.HyperStoragePool{}
	if err := tx.Where("pool_id = ? AND status = ?", pool.ID, model.HyperPoolReady).Order("checked_at DESC NULLS LAST, hostid").Find(&rows).Error; err != nil {
		return 0, NewCLError(ErrSQLSyntaxError, "Failed to query the hosts of the pool", err)
	}
	type load struct {
		Hostid int32
		N      int64
	}
	loads := []*load{}
	since := time.Now().Add(-imageStorageImportTimeout)
	if err := tx.Model(&model.ImageStorage{}).Select("hostid, count(*) AS n").Where("status = ? AND sent_at > ?", model.ImageStorageSyncing, since).
		Group("hostid").Scan(&loads).Error; err != nil {
		return 0, NewCLError(ErrSQLSyntaxError, "Failed to count the imports of the hosts", err)
	}
	running := map[int32]int64{}
	for _, l := range loads {
		running[l.Hostid] = l.N
	}
	best, bestN := int32(0), int64(-1)
	for _, r := range rows {
		if _, online := hostOnline(tx, r.Hostid); !online {
			continue
		}
		if n := running[r.Hostid]; bestN < 0 || n < bestN {
			best, bestN = r.Hostid, n
		}
	}
	if bestN < 0 {
		return 0, NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("No online host can reach storage pool %s now", pool.Name), nil)
	}
	return best, nil
}
