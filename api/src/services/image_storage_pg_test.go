/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Boot disks in shared pools against PostgreSQL with the fake cland (shared-storage-design.md §9.6–§9.8): the copy of
// an image in a pool (one import, the commands waiting for it, a failed import, the retry), the boot disk made from it,
// its removal with the instance, the removal of the copy once the image is deleted and nothing is cloned from it, the
// pool refusing to go while an import runs and dropping its copies when it goes. The node scripts are covered by the
// WSL tests and on real hosts.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

func TestImageStoragePG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	stamp := time.Now().UnixNano() % 1000000
	pool, _ := f.createPool(fmt.Sprintf("is-%d", stamp), 100, func(hostid int32, p string) string { return readyCheck(p, 100*gib, 0) })
	root := pool.MountPath
	image := &model.Image{Name: fmt.Sprintf("is-img-%d", stamp), Format: "qcow2", Status: "available", BootLoader: "bios"}
	must(t, db.Create(image).Error)
	prefix := strings.Split(image.UUID, "-")[0]
	instances := []*model.Instance{}
	t.Cleanup(func() {
		db.Unscoped().Where("storage_pool_id = ?", pool.ID).Delete(&model.ImageStorage{})
		for _, inst := range instances {
			db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.ImageStorageWaiter{})
			db.Unscoped().Delete(inst)
		}
		db.Unscoped().Delete(image)
	})
	// An instance with its boot disk in the pool, as the creation records them before the launch
	newInstance := func(name string, status model.InstanceStatus, volStatus model.VolumeStatus, size int32) (*model.Instance, *model.Volume) {
		inst := &model.Instance{Hostname: fmt.Sprintf("%s-%d", name, stamp), Status: status, Hyper: -1, Owner: 1, ImageID: image.ID, Disk: size}
		must(t, db.Create(inst).Error)
		instances = append(instances, inst)
		vol := &model.Volume{Name: inst.Hostname + "-boot", Owner: 1, Booting: true, InstanceID: inst.ID, StoragePoolID: pool.ID, Size: size, Status: volStatus}
		must(t, db.Create(vol).Error)
		must(t, db.Model(&model.Volume{}).Where("id = ?", vol.ID).Update("path", fmt.Sprintf("volumes/volume-%d.disk", vol.ID)).Error)
		vol.Path = fmt.Sprintf("volumes/volume-%d.disk", vol.ID)
		return inst, vol
	}
	prepare := func() (*model.ImageStorage, *volumeCommand, error) {
		t.Helper()
		tx := db.Begin()
		is, cmd, err := prepareImageStorage(SetContextDB(ctx, tx), tx, pool, image)
		if err != nil {
			tx.Rollback()
			return nil, nil, err
		}
		must(t, tx.Commit().Error)
		return is, cmd, nil
	}
	volumeOf := func(id int64) *model.Volume {
		t.Helper()
		v := &model.Volume{}
		must(t, db.Take(v, id).Error)
		return v
	}
	copyOf := func(id int64) *model.ImageStorage {
		t.Helper()
		is := &model.ImageStorage{}
		must(t, db.Unscoped().Take(is, id).Error)
		return is
	}

	// The first use imports the copy: one host of the pool, in the background (import_image_shared.sh)
	is, cmd, err := prepare()
	must(t, err)
	base := fmt.Sprintf("images/image-%d-%s.qcow2", image.ID, prefix)
	if is.Status != model.ImageStorageSyncing || is.Path != base || is.SentAt == nil || cmd == nil {
		t.Fatalf("copy %+v command %+v", is, cmd)
	}
	sendImageStorageImport(ctx, is, cmd)
	imp := f.oneCmd("import", "import_image_shared.sh")
	if imp.hostid != is.Hostid || imp.args != fmt.Sprintf("'%d'", is.ID) || imp.input["base"] != root+"/"+base ||
		imp.input["image_name"] != fmt.Sprintf("image-%d-%s.qcow2", image.ID, prefix) || imp.input["image_storage_id"] != float64(is.ID) ||
		imp.input["driver"] != "gpfs" || imp.input["root"] != root || imp.input["clone_mode"] != pool.CloneMode {
		t.Fatalf("import command %+v", imp)
	}
	// Another request meanwhile waits for the same import
	is2, cmd2, err := prepare()
	must(t, err)
	if is2.ID != is.ID || cmd2 != nil {
		t.Fatalf("second request: copy %+v command %+v", is2, cmd2)
	}

	// Commands waiting for it: a launch, and a reinstall that grew the disk from 8 to 12 GB
	inst1, vol1 := newInstance("is-launch", model.InstanceStatusProvisioning, model.VolumeStatusPending, 10)
	inst2, vol2 := newInstance("is-reinstall", model.InstanceStatusReinstalling, model.VolumeStatus("reinstalling"), 12)
	launch := fmt.Sprintf("/opt/cloudland/scripts/backend/launch_vm.sh '%d' x <<'EOF'\nmd\nEOF", inst1.ID)
	must(t, waitForImageStorage(db, is, model.ImageWaitLaunch, inst1.ID, vol1.ID, 0, fmt.Sprintf("inter=%d", h[0]), launch))
	must(t, waitForImageStorage(db, is, model.ImageWaitReinstall, inst2.ID, vol2.ID, 8, fmt.Sprintf("inter=%d", h[1]), "/opt/cloudland/scripts/backend/reinstall_vm.sh 'x'"))
	// Only the host the import was sent to reports on it
	other := h[0]
	if other == is.Hostid {
		other = h[1]
	}
	if err := HandleImageStorageStatus(ctx, other, is.ID, model.ImageStorageSynced, "-"); err == nil {
		t.Fatal("a report from another host was taken")
	}
	// The import fails: the launch fails with its instance, the reinstall leaves the disk as it was
	must(t, HandleImageStorageStatus(ctx, is.Hostid, is.ID, model.ImageStorageError, "no space left"))
	if c := copyOf(is.ID); c.Status != model.ImageStorageError || !strings.Contains(c.Reason, "no space") {
		t.Fatalf("copy after the failed import %+v", c)
	}
	got1, got2 := &model.Instance{}, &model.Instance{}
	must(t, db.Take(got1, inst1.ID).Error)
	must(t, db.Take(got2, inst2.ID).Error)
	gv1, gv2 := &model.Volume{}, &model.Volume{}
	must(t, db.Take(gv1, vol1.ID).Error)
	must(t, db.Take(gv2, vol2.ID).Error)
	if got1.Status != "error" || gv1.Status != model.VolumeStatusError || !strings.Contains(got1.Reason, "no space") {
		t.Fatalf("launch after the failed import: %+v %+v", got1, gv1)
	}
	if got2.Status != "error" || gv2.Status != model.VolumeStatusAttached || gv2.Size != 8 {
		t.Fatalf("reinstall after the failed import: %+v %+v", got2, gv2)
	}
	var waiting int64
	must(t, db.Model(&model.ImageStorageWaiter{}).Where("image_storage_id = ?", is.ID).Count(&waiting).Error)
	if waiting != 0 {
		t.Fatalf("%d commands still wait for a failed copy", waiting)
	}
	if cmds := f.volumeCmds(); len(cmds) != 0 {
		t.Fatalf("commands sent for a failed copy: %v", cmds)
	}

	// The next use imports it again; once synced, the command that waited goes out
	is3, cmd3, err := prepare()
	must(t, err)
	if is3.ID != is.ID || cmd3 == nil || is3.Status != model.ImageStorageSyncing {
		t.Fatalf("retry: copy %+v command %+v", is3, cmd3)
	}
	sendImageStorageImport(ctx, is3, cmd3)
	f.oneCmd("import again", "import_image_shared.sh")
	inst3, vol3 := newInstance("is-launch2", model.InstanceStatusProvisioning, model.VolumeStatusPending, 10)
	launch3 := fmt.Sprintf("/opt/cloudland/scripts/backend/launch_vm.sh '%d' x <<'EOF'\nmd\nEOF", inst3.ID)
	must(t, waitForImageStorage(db, is3, model.ImageWaitLaunch, inst3.ID, vol3.ID, 0, fmt.Sprintf("inter=%d", h[2]), launch3))
	must(t, HandleImageStorageStatus(ctx, is3.Hostid, is3.ID, model.ImageStorageSynced, "-"))
	if l := f.oneCmd("launch after the copy", "launch_vm.sh"); l.hostid != h[2] || !strings.Contains(l.raw, fmt.Sprintf("'%d'", inst3.ID)) {
		t.Fatalf("launch sent %+v", l)
	}
	// A repeated report changes nothing and sends nothing
	must(t, HandleImageStorageStatus(ctx, is3.Hostid, is3.ID, model.ImageStorageSynced, "-"))
	if cmds := f.volumeCmds(); len(cmds) != 0 {
		t.Fatalf("a repeated report sent %v", cmds)
	}
	// Synced: a new boot disk clones from it right away
	is4, cmd4, err := prepare()
	must(t, err)
	if is4.Status != model.ImageStorageSynced || cmd4 != nil {
		t.Fatalf("synced copy %+v command %+v", is4, cmd4)
	}

	// What the launch gets about the boot disk
	bd, err := sharedBootDisk(pool, vol3, inst3.ID, is4)
	must(t, err)
	if bd["image_base"] != root+"/"+base || bd["image_storage_id"] != is4.ID || bd["path"] != fmt.Sprintf("%s/volumes/volume-%d.disk", root, vol3.ID) ||
		bd["nvram"] != fmt.Sprintf("%s/nvram/inst-%d_VARS.fd", root, inst3.ID) || bd["driver"] != "gpfs" || bd["volume_id"] != vol3.ID || bd["clone_mode"] != pool.CloneMode {
		t.Fatalf("boot disk %v", bd)
	}
	md, err := metadataWithBootDisk(`{"os_code":"linux","vlans":[]}`, bd)
	must(t, err)
	parsed := map[string]interface{}{}
	must(t, json.Unmarshal([]byte(md), &parsed))
	if parsed["os_code"] != "linux" || parsed["boot_disk"].(map[string]interface{})["image_storage_id"] != float64(is4.ID) {
		t.Fatalf("metadata %s", md)
	}

	// The boot disk reported made: only by a host of the pool, with a copy of its pool
	if err := HandleSharedBootCreated(ctx, stcHosts[3], vol3.ID, "attached", is4.ID, "-"); err == nil {
		t.Fatal("a boot disk reported by a host outside the pool was taken")
	}
	if err := HandleSharedBootCreated(ctx, h[2], vol3.ID, "attached", is4.ID+1000000, "-"); err == nil {
		t.Fatal("a boot disk naming a copy of no pool was taken")
	}
	must(t, HandleSharedBootCreated(ctx, h[2], vol3.ID, "attached", is4.ID, "-"))
	gv1 = volumeOf(vol3.ID)
	if gv1.Status != model.VolumeStatusAttached || gv1.BaseImageStorageID != is4.ID || gv1.Hyper != 0 {
		t.Fatalf("boot disk made %+v", gv1)
	}
	if n, _ := imageStorageRefs(db, is4.ID); n != 1 {
		t.Fatalf("clones of the copy: %d", n)
	}

	// The image is deleted while a boot disk is cloned from its copy: the copy stays, deleting
	tx := db.Begin()
	removals, err := deleteImageStorages(tx, image)
	must(t, err)
	must(t, tx.Commit().Error)
	if len(removals) != 0 || copyOf(is4.ID).Status != model.ImageStorageDeleting {
		t.Fatalf("removals %v copy %+v", removals, copyOf(is4.ID))
	}
	if _, _, err := prepare(); errCode(err) != ErrImageNotAvailable {
		t.Fatalf("a boot disk from a copy being removed: %v", err)
	}

	// The instance goes: its boot disk is removed by a host of the pool with its UEFI variables
	must(t, db.Model(&model.Volume{}).Where("id = ?", vol3.ID).Update("status", model.VolumeStatusDeleting).Error)
	must(t, db.Delete(inst3).Error)
	tx = db.Begin()
	send := SharedBootRemoval(SetContextDB(ctx, tx), inst3.ID)
	must(t, tx.Commit().Error)
	send()
	rm := f.oneCmd("boot disk removal", "delete_volume_shared.sh")
	if rm.input["path"] != fmt.Sprintf("%s/volumes/volume-%d.disk", root, vol3.ID) || rm.input["nvram"] != fmt.Sprintf("%s/nvram/inst-%d_VARS.fd", root, inst3.ID) {
		t.Fatalf("boot disk removal %+v", rm)
	}
	// Its removal confirmed, nothing is cloned from the copy any more: the copy is removed
	must(t, HandleClearVolume(ctx, rm.hostid, vol3.ID, "deleted", "-"))
	if db.Take(&model.Volume{}, vol3.ID).Error == nil {
		t.Fatal("the boot disk record is still there")
	}
	del := f.oneCmd("copy removal", "delete_image_shared.sh")
	if del.args != fmt.Sprintf("'%d'", is4.ID) || del.input["base"] != root+"/"+base {
		t.Fatalf("copy removal %+v", del)
	}
	// A failed removal is kept and sent again later; a confirmed one drops the record
	must(t, HandleImageStorageStatus(ctx, del.hostid, is4.ID, "delete_failed", "busy"))
	if c := copyOf(is4.ID); c.Status != model.ImageStorageDeleting || !strings.Contains(c.Reason, "busy") {
		t.Fatalf("copy after a failed removal %+v", c)
	}
	must(t, db.Model(&model.ImageStorage{}).Where("id = ?", is4.ID).Update("sent_at", time.Now().Add(-time.Hour)).Error)
	maintainImageStorages(ctx)
	del = f.oneCmd("copy removal sent again", "delete_image_shared.sh")
	must(t, HandleImageStorageStatus(ctx, del.hostid, is4.ID, "deleted", "-"))
	if c := copyOf(is4.ID); c.DeletedAt.Time.IsZero() {
		t.Fatalf("the copy record is still live %+v", c)
	}

	// A boot disk whose removal failed after its instance went: delete_failed, deleted again as a volume
	inst5, vol5 := newInstance("is-orphan", model.InstanceStatusRunning, model.VolumeStatusDeleting, 10)
	must(t, db.Delete(inst5).Error)
	must(t, HandleClearVolume(ctx, h[0], vol5.ID, "error", "the volume is still open"))
	gv1 = volumeOf(vol5.ID)
	if gv1.Status != model.VolumeStatusDeleteFailed {
		t.Fatalf("boot disk after a failed removal %+v", gv1)
	}
	deferred, err := volumeAdmin.Delete(ctx, gv1)
	must(t, err)
	if !deferred {
		t.Fatal("the removal of a shared boot disk is not deferred")
	}
	f.oneCmd("orphan boot disk removal", "delete_volume_shared.sh")
	// The boot disk of a live instance is not deleted as a volume
	_, vol6 := newInstance("is-live", model.InstanceStatusRunning, model.VolumeStatusDeleteFailed, 10)
	_, err = volumeAdmin.Delete(ctx, vol6)
	wantCode(t, err, ErrVolumeIsInUse, "boot disk of a live instance")

	// The hosts a boot disk in the pool can run on: the active hosts of the zone where the pool is ready
	zone := &model.Zone{Name: fmt.Sprintf("is-zone-%d", stamp)}
	must(t, db.Create(zone).Error)
	t.Cleanup(func() { db.Unscoped().Delete(zone) })
	must(t, db.Model(&model.Hyper{}).Where("hostid IN ?", h[:2]).Update("zone_id", zone.ID).Error)
	must(t, db.Model(&model.HyperStoragePool{}).Where("hostid = ? AND pool_id = ?", h[1], pool.ID).Update("status", model.HyperPoolUnavailable).Error)
	hosts, err := sharedPoolHostsOfZone(db, pool, zone.ID)
	must(t, err)
	if len(hosts) != 1 || hosts[0] != h[0] {
		t.Fatalf("hosts of the pool in the zone %v", hosts)
	}
	f.setHostStatus(h[0], 10)
	_, err = sharedPoolHostsOfZone(db, pool, zone.ID)
	wantCode(t, err, ErrStoragePoolUnavailable, "no host of the zone reaches the pool")
	f.setHostStatus(h[0], 1)
	must(t, db.Model(&model.HyperStoragePool{}).Where("hostid = ? AND pool_id = ?", h[1], pool.ID).Update("status", model.HyperPoolReady).Error)

	// The pool does not go while an image is copied into it; when it goes, its copies go with it
	db.Unscoped().Where("storage_pool_id = ?", pool.ID).Delete(&model.Volume{})
	image2 := &model.Image{Name: fmt.Sprintf("is-img2-%d", stamp), Format: "raw", Status: "available"}
	must(t, db.Create(image2).Error)
	t.Cleanup(func() { db.Unscoped().Delete(image2) })
	busy := &model.ImageStorage{ImageID: image2.ID, StoragePoolID: pool.ID, Path: "images/x.qcow2", Status: model.ImageStorageSyncing}
	must(t, db.Create(busy).Error)
	_, err = storagePoolAdmin.DeleteShared(ctx, f.pool(pool.ID))
	wantCode(t, err, ErrStoragePoolInUse, "pool with an import running")
	must(t, db.Model(busy).Update("status", model.ImageStorageSynced).Error)
	task, err := storagePoolAdmin.DeleteShared(ctx, f.pool(pool.ID))
	must(t, err)
	for _, s := range f.expect("sync_pools of the removal", "stc_pools.sh", h...) {
		must(t, f.report(s, model.StorageRunSucceeded, "", `{"pools":0}`))
	}
	sent := f.expect("delete_pool", "gpfs_pool.sh", h[0])
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"policy_installed":false}`))
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	var copies int64
	must(t, db.Model(&model.ImageStorage{}).Where("storage_pool_id = ?", pool.ID).Count(&copies).Error)
	if copies != 0 {
		t.Fatalf("%d copies of a deleted pool are left", copies)
	}
}

// The boot disk of an RBD pool as QEMU tools read it (capture, BootDiskSource), and the copy of an image in it
func TestImageStorageRBDRefs(t *testing.T) {
	pool := &model.StoragePool{Driver: model.StorageDriverCephRBD, DriverParams: `{"ceph_pool":"cl_1a2b3c4d","user":"cloudland","conf":"/etc/ceph/c.conf"}`}
	volume := &model.Volume{Path: "volume-12"}
	if s := BootDiskSource(pool, volume); s != "rbd:cl_1a2b3c4d/volume-12:id=cloudland:conf=/etc/ceph/c.conf" {
		t.Fatalf("RBD boot disk source %s", s)
	}
	image := &model.Image{Model: model.Model{ID: 3, UUID: "ab12cd34-0000-0000-0000-000000000000"}}
	d, err := poolDriverOf(pool)
	if err != nil {
		t.Fatal(err)
	}
	if ref := d.ImageBaseRef(pool, image); ref != "image-3-ab12cd34" {
		t.Fatalf("copy of an image in an RBD pool %s", ref)
	}
	local := &model.StoragePool{Driver: model.StorageDriverLocal, MountPath: "/opt/cloudland/pools/x"}
	if s := BootDiskSource(local, &model.Volume{Path: "volumes/volume-1.disk"}); s != "/opt/cloudland/pools/x/volumes/volume-1.disk" {
		t.Fatalf("local boot disk source %s", s)
	}
}
