/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The copies of an image as an admin drives them, against PostgreSQL with the fake cland (shared-storage-design.md
// §9.6): preheat into a pool, the progress the host of the import reports, the host chosen by its running imports,
// removing a copy no boot disk uses and refusing one in use or still importing.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

func TestImageStorageAdminPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	stamp := time.Now().UnixNano() % 1000000
	pool, _ := f.createPool(fmt.Sprintf("isa-%d", stamp), 100, func(hostid int32, p string) string { return readyCheck(p, 100*gib, 0) })
	image := &model.Image{Name: fmt.Sprintf("isa-img-%d", stamp), Format: "qcow2", Status: "available", BootLoader: "bios", Size: 10 << 30}
	must(t, db.Create(image).Error)
	other := &model.Image{Name: fmt.Sprintf("isa-other-%d", stamp), Format: "qcow2", Status: "available", BootLoader: "bios"}
	must(t, db.Create(other).Error)
	vols := []*model.Volume{}
	t.Cleanup(func() {
		db.Unscoped().Where("storage_pool_id = ?", pool.ID).Delete(&model.ImageStorage{})
		for _, v := range vols {
			db.Unscoped().Delete(v)
		}
		db.Unscoped().Delete(image)
		db.Unscoped().Delete(other)
	})
	copyOf := func(id int64) *model.ImageStorage {
		t.Helper()
		is := &model.ImageStorage{}
		must(t, db.Unscoped().Take(is, id).Error)
		return is
	}
	f.cland.take()

	// Not a shared pool, a rescue image, an image not available: refused
	local := &model.StoragePool{Name: "builtin-like", Driver: model.StorageDriverLocal}
	_, err := PreheatImage(ctx, image, []*model.StoragePool{local})
	wantCodeMsg(t, err, ErrInvalidParameter, "not shared")
	pending := &model.Image{Name: "pending", Status: "downloading"}
	_, err = PreheatImage(ctx, pending, []*model.StoragePool{pool})
	wantCodeMsg(t, err, ErrImageNotAvailable, "downloading")

	// Preheat: the copy is imported by a host of the pool, nothing waits for it
	copies, err := PreheatImage(ctx, image, []*model.StoragePool{pool})
	must(t, err)
	if len(copies) != 1 || copies[0].Status != model.ImageStorageSyncing || copies[0].Hostid == 0 {
		t.Fatalf("preheated copies %+v", copies)
	}
	is := copies[0]
	imp := f.oneCmd("import", "import_image_shared.sh")
	if imp.hostid != is.Hostid || imp.input["image_size"] != float64(10<<30) {
		t.Fatalf("import command %+v", imp)
	}
	// Preheated again meanwhile: the import running is left as it is
	if again, err := PreheatImage(ctx, image, []*model.StoragePool{pool}); err != nil || again[0].ID != is.ID {
		t.Fatalf("second preheat %+v %v", again, err)
	}
	if cmds := f.cland.take(); len(cmds) != 0 {
		t.Fatalf("imported twice: %v", cmds)
	}

	// The progress of the import, from its host only, while it runs
	elsewhere := h[0]
	if elsewhere == is.Hostid {
		elsewhere = h[1]
	}
	must(t, HandleImageStorageProgress(ctx, is.Hostid, is.ID, ImageStoragePhaseDownload, 37))
	must(t, HandleImageStorageProgress(ctx, elsewhere, is.ID, ImageStoragePhaseWrite, 90))
	if c := copyOf(is.ID); c.Phase != ImageStoragePhaseDownload || c.Progress != 37 {
		t.Fatalf("progress %s %d", c.Phase, c.Progress)
	}
	if err := HandleImageStorageProgress(ctx, is.Hostid, is.ID, "bake", 1); err == nil {
		t.Fatal("an unknown phase was taken")
	}
	must(t, HandleImageStorageProgress(ctx, is.Hostid, is.ID, ImageStoragePhaseWrite, 140))
	if c := copyOf(is.ID); c.Phase != ImageStoragePhaseWrite || c.Progress != 100 {
		t.Fatalf("progress clamped %s %d", c.Phase, c.Progress)
	}

	// A second image goes to the host with fewer imports running
	copies, err = PreheatImage(ctx, other, []*model.StoragePool{pool})
	must(t, err)
	if copies[0].Hostid == is.Hostid {
		t.Fatalf("second import on the busy host %d", is.Hostid)
	}
	f.cland.take()
	views, err := ImageStorageCopies(ctx, image)
	must(t, err)
	if len(views) != 1 || views[0].Pool.ID != pool.ID || views[0].Copy.Progress != 100 || views[0].Host == "" {
		t.Fatalf("copies of the image %+v", views)
	}

	// While it imports, it is not removed
	err = DropImageStorage(ctx, image, pool)
	wantCodeMsg(t, err, ErrImageCopyInUse, "being imported")
	must(t, HandleImageStorageStatus(ctx, is.Hostid, is.ID, model.ImageStorageSynced, "-"))
	// A boot disk is cloned from it: refused
	vol := &model.Volume{Name: fmt.Sprintf("isa-boot-%d", stamp), Owner: 1, Booting: true, StoragePoolID: pool.ID, Size: 10,
		Status: model.VolumeStatusAttached, BaseImageStorageID: is.ID}
	must(t, db.Create(vol).Error)
	vols = append(vols, vol)
	err = DropImageStorage(ctx, image, pool)
	wantCodeMsg(t, err, ErrImageCopyInUse, "1 boot disks")
	// Not any more: the copy goes, its host removes it
	must(t, db.Model(&model.Volume{}).Where("id = ?", vol.ID).Update("base_image_storage_id", 0).Error)
	must(t, DropImageStorage(ctx, image, pool))
	del := f.oneCmd("remove", "delete_image_shared.sh")
	if del.args != fmt.Sprintf("'%d'", is.ID) || copyOf(is.ID).Status != model.ImageStorageDeleting {
		t.Fatalf("removal %+v copy %+v", del, copyOf(is.ID))
	}
	// Removing it again while it goes is fine; a pool without a copy says so
	must(t, DropImageStorage(ctx, image, pool))
	must(t, HandleImageStorageStatus(ctx, copyOf(is.ID).Hostid, is.ID, "deleted", "-"))
	err = DropImageStorage(ctx, image, pool)
	wantCodeMsg(t, err, ErrImageCopyNotFound, "has no copy")
	// The failed copy of the other image goes at once, nothing to remove in the pool
	o := &model.ImageStorage{}
	must(t, db.Where("image_id = ?", other.ID).Take(o).Error)
	must(t, HandleImageStorageStatus(ctx, o.Hostid, o.ID, model.ImageStorageError, "no space"))
	must(t, DropImageStorage(ctx, other, pool))
	if err := db.Where("image_id = ?", other.ID).Take(&model.ImageStorage{}).Error; err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("failed copy kept: %v", err)
	}
	if cmds := f.cland.take(); len(cmds) != 0 {
		t.Fatalf("a removal was sent for a copy never put in place: %v", cmds)
	}
}
