/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The fixes after the code review of S4 (TC-22 regression points 8–20) against PostgreSQL with the fake cland: the
// copy name in lower case, an image not deleted while its copy is imported, a copy a host did not find is imported
// again, the maintenance puts off copies still cloned from, a boot disk whose removal failed earlier goes with its
// instance, a reinstall that could not be sent leaves the disk as it was, a resized boot disk resizes its instance, a
// waiting reinstall refuses reinstall / rescue / resize, and a default shared pool the zone does not reach leaves the
// boot disk to the builtin pool.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

func TestImageStorageReviewPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	stamp := time.Now().UnixNano() % 1000000
	pool, _ := f.createPool(fmt.Sprintf("isr-%d", stamp), 100, func(hostid int32, p string) string { return readyCheck(p, 100*gib, 0) })
	images := []*model.Image{}
	instances := []*model.Instance{}
	t.Cleanup(func() {
		db.Unscoped().Where("storage_pool_id = ?", pool.ID).Delete(&model.ImageStorage{})
		for _, inst := range instances {
			db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.ImageStorageWaiter{})
			db.Unscoped().Delete(inst)
		}
		for _, img := range images {
			db.Unscoped().Delete(img)
		}
	})
	newImage := func(name, uuid string) *model.Image {
		img := &model.Image{Model: model.Model{UUID: uuid}, Name: fmt.Sprintf("%s-%d", name, stamp), Format: "qcow2", Status: "available", BootLoader: "bios"}
		must(t, db.Create(img).Error)
		images = append(images, img)
		return img
	}
	image := newImage("isr-img", "")
	newInstance := func(name string, status model.InstanceStatus, volStatus model.VolumeStatus, size int32) (*model.Instance, *model.Volume) {
		inst := &model.Instance{Hostname: fmt.Sprintf("%s-%d", name, stamp), Status: status, Hyper: h[0], Owner: 1, ImageID: image.ID, Disk: size}
		must(t, db.Create(inst).Error)
		instances = append(instances, inst)
		vol := &model.Volume{Name: inst.Hostname + "-boot", Owner: 1, Booting: true, InstanceID: inst.ID, StoragePoolID: pool.ID, Size: size,
			Status: volStatus, Target: "vda"}
		must(t, db.Create(vol).Error)
		vol.Path = fmt.Sprintf("volumes/volume-%d.disk", vol.ID)
		must(t, db.Model(&model.Volume{}).Where("id = ?", vol.ID).Update("path", vol.Path).Error)
		return inst, vol
	}
	prepare := func(img *model.Image) (*model.ImageStorage, *volumeCommand) {
		t.Helper()
		tx := db.Begin()
		is, cmd, err := prepareImageStorage(SetContextDB(ctx, tx), tx, pool, img)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		must(t, tx.Commit().Error)
		return is, cmd
	}
	copyOf := func(id int64) *model.ImageStorage {
		t.Helper()
		is := &model.ImageStorage{}
		must(t, db.Unscoped().Take(is, id).Error)
		return is
	}
	volumeOf := func(id int64) *model.Volume {
		t.Helper()
		v := &model.Volume{}
		must(t, db.Unscoped().Take(v, id).Error)
		return v
	}
	instanceOf := func(id int64) *model.Instance {
		t.Helper()
		i := &model.Instance{}
		must(t, db.Unscoped().Take(i, id).Error)
		return i
	}
	sentFor := func(script, args string) int {
		n := 0
		for _, c := range f.volumeCmds() {
			if c.script == script && c.args == args {
				n++
			}
		}
		return n
	}

	// 5: an image whose UUID has capitals: every file of it is named in lower case (model.Image.FileBase), the copy as
	// the image in the cache of the host (the node scripts check [0-9a-f])
	upper := newImage("isr-upper", "AB12CD34-5E6F-4A7B-8C9D-0E1F2A3B4C5D")
	isU, cmdU := prepare(upper)
	if isU.Path != fmt.Sprintf("images/image-%d-ab12cd34.qcow2", upper.ID) || cmdU == nil {
		t.Fatalf("copy of an image with capitals %+v", isU)
	}
	sendImageStorageImport(ctx, isU, cmdU)
	imp := f.oneCmd("import of an image with capitals", "import_image_shared.sh")
	if imp.input["image_name"] != fmt.Sprintf("image-%d-ab12cd34.qcow2", upper.ID) || imp.input["base"] != pool.MountPath+"/"+isU.Path {
		t.Fatalf("import of an image with capitals %+v", imp.input)
	}

	// 2: the image is not deleted while its copy is imported (the copy would be put in place after its removal);
	// an import past its time does not hold it
	tx := db.Begin()
	_, err := deleteImageStorages(tx, upper)
	tx.Rollback()
	wantCode(t, err, ErrImageInUse, "image whose copy is imported")
	must(t, db.Model(&model.ImageStorage{}).Where("id = ?", isU.ID).Update("sent_at", time.Now().Add(-imageStorageImportTimeout-time.Minute)).Error)
	tx = db.Begin()
	removals, err := deleteImageStorages(tx, upper)
	must(t, err)
	must(t, tx.Commit().Error)
	if c := copyOf(isU.ID); c.Status != model.ImageStorageDeleting || len(removals) != 1 {
		t.Fatalf("image with an import past its time: copy %+v, %d removals", c, len(removals))
	}

	// 11: a host did not find the synced copy: the boot disk and its instance fail, the copy too, and the next boot
	// disk imports it again
	is, cmd := prepare(image)
	sendImageStorageImport(ctx, is, cmd)
	f.oneCmd("import", "import_image_shared.sh")
	must(t, HandleImageStorageStatus(ctx, is.Hostid, is.ID, model.ImageStorageSynced, "-"))
	inst1, vol1 := newInstance("isr-nocopy", model.InstanceStatusProvisioning, model.VolumeStatusPending, 10)
	must(t, HandleSharedBootCreated(ctx, h[0], vol1.ID, "nocopy", is.ID, "the copy of the image is not in pool"))
	if c := copyOf(is.ID); c.Status != model.ImageStorageError || !strings.Contains(c.Reason, "not found in the pool") {
		t.Fatalf("copy a host did not find %+v", c)
	}
	if v, i := volumeOf(vol1.ID), instanceOf(inst1.ID); v.Status != model.VolumeStatusError || i.Status != "error" {
		t.Fatalf("boot disk without its copy: %+v %+v", v, i)
	}
	is2, cmd2 := prepare(image)
	if is2.ID != is.ID || is2.Status != model.ImageStorageSyncing || cmd2 == nil {
		t.Fatalf("next use of a copy a host did not find: %+v %+v", is2, cmd2)
	}
	sendImageStorageImport(ctx, is2, cmd2)
	f.oneCmd("import again", "import_image_shared.sh")
	must(t, HandleImageStorageStatus(ctx, is2.Hostid, is2.ID, model.ImageStorageSynced, "-"))
	// A plain error leaves the copy alone
	_, vol1b := newInstance("isr-error", model.InstanceStatusProvisioning, model.VolumeStatusPending, 10)
	must(t, HandleSharedBootCreated(ctx, h[0], vol1b.ID, "error", 0, "no space left"))
	if c := copyOf(is.ID); c.Status != model.ImageStorageSynced {
		t.Fatalf("copy after another failure %+v", c)
	}

	// 13: a copy being removed that boot disks are still cloned from: the maintenance puts off its next look
	_, vol2 := newInstance("isr-clone", model.InstanceStatusRunning, model.VolumeStatusAttached, 10)
	must(t, db.Model(&model.Volume{}).Where("id = ?", vol2.ID).Update("base_image_storage_id", is.ID).Error)
	must(t, db.Model(&model.ImageStorage{}).Where("id = ?", is.ID).Updates(map[string]interface{}{
		"status": model.ImageStorageDeleting, "sent_at": time.Now().Add(-time.Hour)}).Error)
	f.volumeCmds()
	maintainImageStorages(ctx)
	if c := copyOf(is.ID); c.SentAt == nil || time.Since(*c.SentAt) > time.Minute {
		t.Fatalf("copy still cloned from, not put off %+v", c)
	}
	if n := sentFor("delete_image_shared.sh", fmt.Sprintf("'%d'", is.ID)); n != 0 {
		t.Fatalf("removal of a copy still cloned from sent %d times", n)
	}
	// Handled since (by another clapi) is skipped by the maintenance; a released one goes at once
	must(t, db.Model(&model.Volume{}).Where("id = ?", vol2.ID).Update("base_image_storage_id", 0).Error)
	before := time.Now().Add(-imageStorageDeleteTimeout)
	releaseImageStorageIf(ctx, is.ID, &before)
	if n := sentFor("delete_image_shared.sh", fmt.Sprintf("'%d'", is.ID)); n != 0 {
		t.Fatalf("the maintenance sent the removal of a copy handled since (%d)", n)
	}
	releaseImageStorage(ctx, is.ID)
	del := f.oneCmd("copy removal", "delete_image_shared.sh")
	must(t, HandleImageStorageStatus(ctx, del.hostid, is.ID, "deleted", "-"))

	// 8: a boot disk whose removal failed before the clear_vm of its instance came is removed with the instance
	inst3, vol3 := newInstance("isr-remove", model.InstanceStatusRunning, model.VolumeStatusDeleteFailed, 10)
	must(t, db.Delete(inst3).Error)
	tx = db.Begin()
	send := SharedBootRemoval(SetContextDB(ctx, tx), inst3.ID)
	must(t, tx.Commit().Error)
	if send == nil {
		t.Fatal("no removal for a boot disk in delete_failed")
	}
	if v := volumeOf(vol3.ID); v.Status != model.VolumeStatusDeleting {
		t.Fatalf("boot disk before its removal %+v", v)
	}
	send()
	f.oneCmd("removal of a boot disk in delete_failed", "delete_volume_shared.sh")

	// 9: deleted again as a volume while cland is away: it stays delete_failed, not available
	must(t, db.Model(&model.Volume{}).Where("id = ?", vol3.ID).Update("status", model.VolumeStatusDeleteFailed).Error)
	f.cland.setFailing(true)
	_, err = volumeAdmin.Delete(ctx, volumeOf(vol3.ID))
	f.cland.setFailing(false)
	if v := volumeOf(vol3.ID); err == nil || v.Status != model.VolumeStatusDeleteFailed {
		t.Fatalf("boot disk whose removal could not be sent: %v %+v", err, v)
	}

	// 6: a reinstall that waited and can not be sent leaves the disk as it was, like a failed import
	inst4, vol4 := newInstance("isr-reinstall", model.InstanceStatusReinstalling, model.VolumeStatus("reinstalling"), 12)
	f.cland.setFailing(true)
	sendImageStorageWaiters(ctx, []*model.ImageStorageWaiter{{Kind: model.ImageWaitReinstall, InstanceID: inst4.ID, VolumeID: vol4.ID,
		PriorSize: 8, Control: fmt.Sprintf("inter=%d", h[0]), Command: "/opt/cloudland/scripts/backend/reinstall_vm.sh 'x'"}})
	f.cland.setFailing(false)
	if v, i := volumeOf(vol4.ID), instanceOf(inst4.ID); v.Status != model.VolumeStatusAttached || v.Size != 8 || i.Status != "error" || i.Disk != 8 {
		t.Fatalf("reinstall that could not be sent: %+v %+v", v, i)
	}

	// 10: the control of a waiting command is not cut at 1024 characters (a select= of many hosts)
	is5, cmd5 := prepare(image)
	if cmd5 == nil {
		t.Fatal("no import for a removed copy")
	}
	control := "select=" + strings.Repeat("9", 1500)
	inst5, vol5 := newInstance("isr-wait", model.InstanceStatusProvisioning, model.VolumeStatusPending, 10)
	must(t, waitForImageStorage(db, is5, model.ImageWaitLaunch, inst5.ID, vol5.ID, 0, control, "launch"))
	w := &model.ImageStorageWaiter{}
	must(t, db.Where("instance_id = ?", inst5.ID).Take(w).Error)
	if w.Control != control {
		t.Fatalf("control of a waiting command cut to %d characters", len(w.Control))
	}
	db.Unscoped().Where("instance_id = ?", inst5.ID).Delete(&model.ImageStorageWaiter{})

	// 1: the resize of a boot disk in a shared pool resizes its instance too
	inst6, vol6 := newInstance("isr-resize", model.InstanceStatusRunning, model.VolumeStatusAttached, 10)
	must(t, volumeAdmin.Resize(ctx, volumeOf(vol6.ID), 15))
	if v, i := volumeOf(vol6.ID), instanceOf(inst6.ID); v.Size != 15 || v.Status != model.VolumeStatusResizing || i.Disk != 15 {
		t.Fatalf("resized boot disk %+v, instance disk %d", v, i.Disk)
	}
	f.oneCmd("resize of a shared boot disk", "resize_volume_shared.sh")

	// 4: a reinstall waiting for its image refuses another reinstall, a rescue and a resize
	waiting := &model.Instance{Model: model.Model{ID: inst4.ID}, Status: model.InstanceStatusReinstalling, Owner: 1,
		Volumes: []*model.Volume{{Booting: true, Size: 10}}, Image: image}
	wantCode(t, instanceAdmin.Reinstall(ctx, waiting, image, "Passw0rd!", nil, 1, 512, 10, 22), ErrInstanceInvalidState, "reinstall while reinstalling")
	wantCode(t, instanceAdmin.Rescue(ctx, waiting, image, "Passw0rd!"), ErrInstanceInvalidState, "rescue while reinstalling")
	wantCode(t, instanceAdmin.Resize(ctx, waiting, 1, 512), ErrInstanceInvalidState, "resize while reinstalling")

	// 7: the default pool is shared: an instance in a zone (or on a host) that does not reach it gets its boot disk in
	// the builtin pool; a pool named outright stays as named
	builtin, err := BuiltinPool(ctx)
	must(t, err)
	must(t, storagePoolAdmin.setDefault(db, f.pool(pool.ID)))
	zone := &model.Zone{Name: fmt.Sprintf("isr-zone-%d", stamp)}
	empty := &model.Zone{Name: fmt.Sprintf("isr-empty-%d", stamp)}
	must(t, db.Create(zone).Error)
	must(t, db.Create(empty).Error)
	t.Cleanup(func() { db.Unscoped().Delete(zone); db.Unscoped().Delete(empty) })
	must(t, db.Model(&model.Hyper{}).Where("hostid IN ?", h[:2]).Update("zone_id", zone.ID).Error)
	resolve := func(ref *BaseReference, zoneID int64, hostid int) *model.StoragePool {
		t.Helper()
		p, err := storagePoolAdmin.ResolveBoot(ctx, ref, zoneID, hostid)
		must(t, err)
		return p
	}
	if p := resolve(nil, zone.ID, -1); p.ID != pool.ID {
		t.Fatalf("zone reaching the default pool got pool %s", p.Name)
	}
	if p := resolve(nil, zone.ID, int(h[1])); p.ID != pool.ID {
		t.Fatalf("host reaching the default pool got pool %s", p.Name)
	}
	if p := resolve(&BaseReference{}, empty.ID, -1); p.ID != builtin.ID {
		t.Fatalf("zone without the default pool got pool %s", p.Name)
	}
	if p := resolve(nil, zone.ID, int(h[2])); p.ID != builtin.ID {
		t.Fatalf("host outside the zone got pool %s", p.Name)
	}
	if p := resolve(&BaseReference{Name: pool.Name}, empty.ID, -1); p.ID != pool.ID {
		t.Fatalf("pool named outright became %s", p.Name)
	}
	must(t, storagePoolAdmin.setDefault(db, builtin))
}
