/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The second code review of S4 (TC-22 regression points 21-): a reinstall waiting for the copy of its image keeps the
// instance as it is until it runs (power actions, rescue, reinstall, resize, migration refused by the waiter itself,
// whatever the status says); when the copy comes, a waiter that no longer fits its instance is dropped and its disk
// left as it was; the maintenance does not give up an import sent again since; waiters go for good; a launch that can
// not be sent fails its instance only while it is being created.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

func TestImageStorageWaitGuardPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	stamp := time.Now().UnixNano() % 1000000
	pool, _ := f.createPool(fmt.Sprintf("iswg-%d", stamp), 100, func(hostid int32, p string) string { return readyCheck(p, 100*gib, 0) })
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
	newImage := func(name string) *model.Image {
		img := &model.Image{Name: fmt.Sprintf("%s-%d", name, stamp), Format: "qcow2", Status: "available", BootLoader: "bios"}
		must(t, db.Create(img).Error)
		images = append(images, img)
		return img
	}
	newInstance := func(name string, img *model.Image, status model.InstanceStatus, volStatus model.VolumeStatus, size int32) (*model.Instance, *model.Volume) {
		inst := &model.Instance{Hostname: fmt.Sprintf("%s-%d", name, stamp), Status: status, Hyper: h[0], Owner: 1, ImageID: img.ID, Disk: size}
		must(t, db.Create(inst).Error)
		instances = append(instances, inst)
		vol := &model.Volume{Name: inst.Hostname + "-boot", Owner: 1, Booting: true, InstanceID: inst.ID, StoragePoolID: pool.ID, Size: size,
			Status: volStatus, Target: "vda"}
		must(t, db.Create(vol).Error)
		vol.Path = fmt.Sprintf("volumes/volume-%d.disk", vol.ID)
		must(t, db.Model(&model.Volume{}).Where("id = ?", vol.ID).Update("path", vol.Path).Error)
		inst.Volumes = []*model.Volume{vol}
		inst.Image = img
		return inst, vol
	}
	importing := func(img *model.Image) *model.ImageStorage {
		t.Helper()
		tx := db.Begin()
		is, cmd, err := prepareImageStorage(SetContextDB(ctx, tx), tx, pool, img)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		must(t, tx.Commit().Error)
		if cmd == nil {
			t.Fatalf("no import for %s", img.Name)
		}
		sendImageStorageImport(ctx, is, cmd)
		f.oneCmd("import "+img.Name, "import_image_shared.sh")
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
	waitersOf := func(instanceID int64) int64 {
		var n int64
		must(t, db.Unscoped().Model(&model.ImageStorageWaiter{}).Where("instance_id = ?", instanceID).Count(&n).Error)
		return n
	}
	reinstallsSent := func() (hosts []int32) {
		for _, c := range f.volumeCmds() {
			if c.script == "reinstall_vm.sh" {
				hosts = append(hosts, c.hostid)
			}
		}
		return
	}
	pw := "Passw0rd!"

	// A reinstall waits for the copy of its image; something set the instance running meanwhile (the status alone was
	// the guard before): every operation that would change it is refused by the waiter itself
	img1 := newImage("iswg-a")
	is1 := importing(img1)
	inst1, vol1 := newInstance("iswg-wait", img1, model.InstanceStatusReinstalling, model.VolumeStatus("reinstalling"), 12)
	must(t, waitForImageStorage(db, is1, model.ImageWaitReinstall, inst1.ID, vol1.ID, 8, fmt.Sprintf("inter=%d", h[0]), "/opt/cloudland/scripts/backend/reinstall_vm.sh 'x'"))
	must(t, db.Model(&model.Instance{}).Where("id = ?", inst1.ID).Update("status", model.InstanceStatusRunning).Error)
	inst1.Status = model.InstanceStatusRunning
	wantCode(t, instanceAdmin.Update(ctx, inst1, inst1.Hostname, PowerAction("stop"), int(inst1.Hyper)), ErrInstanceInvalidState, "power action while a reinstall waits")
	wantCode(t, instanceAdmin.Rescue(ctx, inst1, img1, pw), ErrInstanceInvalidState, "rescue while a reinstall waits")
	wantCode(t, instanceAdmin.Reinstall(ctx, inst1, img1, pw, nil, 1, 512, 12, 22), ErrInstanceInvalidState, "reinstall while a reinstall waits")
	wantCode(t, instanceAdmin.Resize(ctx, inst1, 1, 512), ErrInstanceInvalidState, "resize while a reinstall waits")
	wantCode(t, refuseWhileWaiting(db, inst1), ErrInstanceInvalidState, "migration while a reinstall waits")
	if cmds := f.volumeCmds(); len(cmds) != 0 {
		t.Fatalf("commands sent for an instance whose reinstall waits: %v", cmds)
	}
	// A rename is no change of what the reinstall does
	must(t, instanceAdmin.Update(ctx, inst1, inst1.Hostname+"x", PowerAction(""), int(inst1.Hyper)))

	// The copy comes: the waiter no longer fits (the instance is not reinstalling any more), it is dropped, nothing is
	// sent, its disk and the instance keep the old size, the waiter is gone for good
	must(t, HandleImageStorageStatus(ctx, is1.Hostid, is1.ID, model.ImageStorageSynced, "-"))
	if sent := reinstallsSent(); len(sent) != 0 {
		t.Fatalf("a reinstall that no longer fits was sent to %v", sent)
	}
	if v, i := volumeOf(vol1.ID), instanceOf(inst1.ID); v.Status != model.VolumeStatusAttached || v.Size != 8 || i.Disk != 8 || i.Status != model.InstanceStatusRunning {
		t.Fatalf("dropped reinstall: volume %s %d, instance %s disk %d", v.Status, v.Size, i.Status, i.Disk)
	}
	if n := waitersOf(inst1.ID); n != 0 {
		t.Fatalf("%d waiter rows left, also soft-deleted ones count", n)
	}

	// Two reinstalls wait for another copy: one instance was moved to another host meanwhile, the other was not. The
	// moved one is dropped (instance in error, disk as it was), the other goes to its host
	img2 := newImage("iswg-b")
	is2 := importing(img2)
	inst2, vol2 := newInstance("iswg-moved", img2, model.InstanceStatusReinstalling, model.VolumeStatus("reinstalling"), 20)
	inst3, vol3 := newInstance("iswg-stays", img2, model.InstanceStatusReinstalling, model.VolumeStatus("reinstalling"), 20)
	for _, x := range []struct {
		inst *model.Instance
		vol  *model.Volume
	}{{inst2, vol2}, {inst3, vol3}} {
		must(t, waitForImageStorage(db, is2, model.ImageWaitReinstall, x.inst.ID, x.vol.ID, 10, fmt.Sprintf("inter=%d", h[0]),
			fmt.Sprintf("/opt/cloudland/scripts/backend/reinstall_vm.sh '%d'", x.inst.ID)))
	}
	must(t, db.Model(&model.Instance{}).Where("id = ?", inst2.ID).Update("hyper", h[1]).Error)
	must(t, HandleImageStorageStatus(ctx, is2.Hostid, is2.ID, model.ImageStorageSynced, "-"))
	if sent := reinstallsSent(); len(sent) != 1 || sent[0] != h[0] {
		t.Fatalf("reinstalls sent to %v, want only the one still on host %d", sent, h[0])
	}
	if v, i := volumeOf(vol2.ID), instanceOf(inst2.ID); v.Status != model.VolumeStatusAttached || v.Size != 10 || i.Disk != 10 ||
		i.Status != "error" || !strings.Contains(i.Reason, "changed while its reinstall waited") {
		t.Fatalf("moved instance: volume %s %d, instance %s disk %d (%s)", v.Status, v.Size, i.Status, i.Disk, i.Reason)
	}
	if v := volumeOf(vol3.ID); v.Status != model.VolumeStatus("reinstalling") || v.Size != 20 {
		t.Fatalf("the reinstall sent left its disk %s %d", v.Status, v.Size)
	}

	// The maintenance gives up an import only when it was not sent again since it looked
	img3 := newImage("iswg-c")
	is3 := importing(img3)
	before := time.Now().Add(-imageStorageImportTimeout)
	must(t, handleImageStorageStatus(ctx, is3.Hostid, is3.ID, model.ImageStorageError, "late", &before))
	c := &model.ImageStorage{}
	must(t, db.Take(c, is3.ID).Error)
	if c.Status != model.ImageStorageSyncing {
		t.Fatalf("an import sent again was given up: %+v", c)
	}
	after := time.Now().Add(time.Minute)
	must(t, handleImageStorageStatus(ctx, is3.Hostid, is3.ID, model.ImageStorageError, "late", &after))
	must(t, db.Take(c, is3.ID).Error)
	if c.Status != model.ImageStorageError {
		t.Fatalf("a stale import was not given up: %+v", c)
	}

	// A launch that can not be sent fails its instance while it is being created, and only then
	inst5, vol5 := newInstance("iswg-launch", img3, model.InstanceStatusProvisioning, model.VolumeStatusPending, 10)
	inst6, vol6 := newInstance("iswg-other", img3, model.InstanceStatusRunning, model.VolumeStatusAttached, 10)
	f.cland.setFailing(true)
	sendImageStorageWaiters(ctx, []*model.ImageStorageWaiter{
		{Kind: model.ImageWaitLaunch, InstanceID: inst5.ID, VolumeID: vol5.ID, Control: fmt.Sprintf("inter=%d", h[0]), Command: "launch"},
		{Kind: model.ImageWaitLaunch, InstanceID: inst6.ID, VolumeID: vol6.ID, Control: fmt.Sprintf("inter=%d", h[0]), Command: "launch"},
	})
	f.cland.setFailing(false)
	if v, i := volumeOf(vol5.ID), instanceOf(inst5.ID); v.Status != model.VolumeStatusError || i.Status != "error" || !strings.Contains(i.Reason, "could not be sent") {
		t.Fatalf("launch not sent: volume %s, instance %s (%s)", v.Status, i.Status, i.Reason)
	}
	if v, i := volumeOf(vol6.ID), instanceOf(inst6.ID); v.Status != model.VolumeStatusAttached || i.Status != model.InstanceStatusRunning {
		t.Fatalf("a launch not sent changed an instance that is not being created: volume %s, instance %s", v.Status, i.Status)
	}
}

// The names of the files of an image and a volume as QEMU tools read it, through the driver of its pool
func TestImageFileNames(t *testing.T) {
	img := &model.Image{Model: model.Model{ID: 7, UUID: "AB12CD34-5E6F-4A7B-8C9D-0E1F2A3B4C5D"}}
	if img.FileBase() != "image-7-ab12cd34" || img.FilePrefix() != "ab12cd34" || s3ObjectName(img) != "image-7-ab12cd34" {
		t.Fatalf("file names of an image with capitals: %s %s %s", img.FileBase(), img.FilePrefix(), s3ObjectName(img))
	}
	gpfs := &model.StoragePool{Driver: model.StorageDriverGPFS, MountPath: "/gpfs/fs1/cl_1"}
	if s := BootDiskSource(gpfs, &model.Volume{Path: "volumes/volume-3.disk"}); s != "/gpfs/fs1/cl_1/volumes/volume-3.disk" {
		t.Fatalf("GPFS boot disk source %s", s)
	}
	d, err := poolDriverOf(gpfs)
	if err != nil || d.ImageBaseRef(gpfs, img) != "images/image-7-ab12cd34.qcow2" {
		t.Fatalf("copy of an image in a GPFS pool: %v %s", err, d.ImageBaseRef(gpfs, img))
	}
}
