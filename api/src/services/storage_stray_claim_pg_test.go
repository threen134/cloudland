/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// What an aborted replacement leaves in an erasure code cluster (CLAPI_TEST_DB_URI, see placement_pg_test.go), where no
// disk leaves on its own (2026-10-12, work-x): the new disk's claim stayed failed and counted as a disk of its server
// for good, so every later replacement there failed the layout check; the old pdisk stayed failed once it worked again

import (
	"fmt"
	"testing"

	. "api/src/common"
	"api/src/model"
)

func TestStorageStrayClaimPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts

	// In the replica layout a failed disk without a name may be an NSD an aborted task made already: its removal
	// takes it out of the file system first, no stray shortcut
	looks := &model.StorageClusterDisk{ClusterID: f.cluster.ID, Hostid: h[1], DiskID: "wwn-0x6001405cccc", WWN: "6001405cccc", Role: "nsd",
		Media: "hdd", FsID: f.fs.ID, Status: model.StorageDiskFailed, Attrs: `{"usage":"dataAndMetadata","gpfs_pool":"system"}`}
	must(t, db.Create(looks).Error)
	if StorageDiskStray(f.cluster, looks) {
		t.Fatalf("a failed disk of the replica layout is taken for a stray claim")
	}
	task, err := StorageClusters.RemoveDisk(ctx, f.cluster.UUID, looks.UUID)
	must(t, err)
	removal := f.expect("the normal removal", "gpfs_fs.sh", h[0])
	must(t, AbortStorageTask(ctx, task.ID))
	f.expect("stop it", "stc_kill.sh", h[0])
	must(t, f.report(removal[0], model.StorageRunFailed, "stopped", ""))
	f.wantTask(task.ID, model.StorageTaskAborted, "")
	must(t, db.Unscoped().Delete(looks).Error)

	must(t, db.Model(f.cluster).Updates(map[string]interface{}{"layout": model.StorageLayoutECE,
		"params": `{"layout":"ece","no_slot_map":true}`}).Error)
	pdisk := &model.StorageClusterDisk{}
	must(t, db.Where("cluster_id = ? AND hostid = ?", f.cluster.ID, h[1]).Take(pdisk).Error)
	must(t, db.Model(pdisk).Updates(map[string]interface{}{"name": "n002p001", "state": "missing/drained", "status": model.StorageDiskFailed,
		"reason": "the removal was aborted; remove the disk again"}).Error)
	stray := &model.StorageClusterDisk{ClusterID: f.cluster.ID, Hostid: h[1], DiskID: "wwn-0x6001405aaaa", WWN: "6001405aaaa", Role: "nsd",
		Media: "hdd", Status: model.StorageDiskFailed, Attrs: "{}"}
	must(t, db.Create(stray).Error)
	// Another disk of the host being removed (an aborted removal) is not given back with the stray claim
	other := &model.StorageClusterDisk{ClusterID: f.cluster.ID, Hostid: h[1], DiskID: "wwn-0x6001405bbbb", WWN: "6001405bbbb", Role: "nsd",
		Media: "hdd", Name: "n002p009", State: "ok", Status: model.StorageDiskRemoving, Attrs: "{}"}
	must(t, db.Create(other).Error)
	status := func(d *model.StorageClusterDisk) *model.StorageClusterDisk {
		got := &model.StorageClusterDisk{}
		must(t, db.Unscoped().Take(got, d.ID).Error)
		return got
	}
	report := &StorageHealthReport{Health: "healthy", Nodes: []StorageHealthItem{}, Disks: []StorageHealthItem{{Name: "n002p001", State: "up"}}}
	if !StorageDiskStray(f.cluster, stray) || StorageDiskStray(f.cluster, pdisk) {
		t.Fatalf("stray flags: claim %v pdisk %v", StorageDiskStray(f.cluster, stray), StorageDiskStray(f.cluster, pdisk))
	}

	// The pdisk works again, but its server has a failed claim without a name: it may be the disk that took the name
	must(t, HandleStorageHealth(ctx, h[0], f.cluster.UUID, report))
	if s := status(pdisk).Status; s != model.StorageDiskFailed {
		t.Fatalf("the pdisk went %s with a stray claim on its server", s)
	}

	// A pdisk of the recovery group does not leave on its own
	_, err = StorageClusters.RemoveDisk(ctx, f.cluster.UUID, pdisk.UUID)
	wantCodeMsg(t, err, ErrStorageInvalidPlan, "gpfs clusters (ece) do not support this operation yet")

	// The stray claim goes: checked to be no pdisk on an admin host first, then that disk only is released on its
	// host (not the other one being removed there), and no nsddevices exit is written there
	task, err = StorageClusters.RemoveDisk(ctx, f.cluster.UUID, stray.UUID)
	must(t, err)
	sent := f.expect("check the stray claim", "gpfs_ece.sh", h[0])
	if in := sent[0].input; in["action"] != "claim_free" || in["recovery_group"] != fmt.Sprintf("cl%d_rg", f.cluster.ID) || in["wwn"] != "6001405aaaa" {
		t.Fatalf("claim_free input %v", in)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", "{}"))
	sent = f.expect("release the stray claim", "stc_release_disks.sh", h[1])
	in := sent[0].input
	release, _ := in["release"].([]interface{})
	if in["nsddevices"] != false || in["no_wipe"] != false || len(release) != 1 || release[0].(map[string]interface{})["id"] != "wwn-0x6001405aaaa" {
		t.Fatalf("release input %v", in)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", "{}"))
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	if got := status(stray); !got.DeletedAt.Valid {
		t.Fatalf("the stray claim is still recorded: %+v", got)
	}
	if got := status(other); got.DeletedAt.Valid || got.Status != model.StorageDiskRemoving {
		t.Fatalf("the other disk of the host went with the claim: %+v", got)
	}
	must(t, db.Unscoped().Delete(other).Error)

	// With the claim gone, the next health report makes the working pdisk active again
	must(t, HandleStorageHealth(ctx, h[0], f.cluster.UUID, report))
	if got := status(pdisk); got.Status != model.StorageDiskActive || got.Reason != "" || got.State != "up" {
		t.Fatalf("the working pdisk after the claim went: %s %q %s", got.Status, got.Reason, got.State)
	}

	// An abort after the replacement went through: the claim is the pdisk now. It takes the name and is active, the
	// record of the failed disk it replaced goes, its claim given back without wiping (the disk can not be read)
	must(t, db.Model(pdisk).Updates(map[string]interface{}{"status": model.StorageDiskFailed, "state": "up"}).Error)
	newer := &model.StorageClusterDisk{ClusterID: f.cluster.ID, Hostid: h[1], DiskID: "wwn-0x6001405dddd", WWN: "6001405dddd", Role: "nsd",
		Media: "hdd", Status: model.StorageDiskFailed, Attrs: "{}"}
	must(t, db.Create(newer).Error)
	task, err = StorageClusters.RemoveDisk(ctx, f.cluster.UUID, newer.UUID)
	must(t, err)
	sent = f.expect("check the claim", "gpfs_ece.sh", h[0])
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"pdisk":"n002p001"}`))
	sent = f.expect("release the replaced disk", "stc_release_disks.sh", h[1])
	in = sent[0].input
	release, _ = in["release"].([]interface{})
	if in["no_wipe"] != true || len(release) != 1 || release[0].(map[string]interface{})["id"] != pdisk.DiskID {
		t.Fatalf("release input when adopted %v", in)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", "{}"))
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	if got := status(newer); got.Name != "n002p001" || got.Status != model.StorageDiskActive || got.DeletedAt.Valid {
		t.Fatalf("the adopted claim %+v", got)
	}
	if got := status(pdisk); !got.DeletedAt.Valid {
		t.Fatalf("the record of the replaced disk is still there: %+v", got)
	}

	// A claim the check finds to be a pdisk whose record is a working disk: no aborted replacement leaves that, the
	// records stay as they are and the release fails
	odd := &model.StorageClusterDisk{ClusterID: f.cluster.ID, Hostid: h[1], DiskID: "wwn-0x6001405eeee", WWN: "6001405eeee", Role: "nsd",
		Media: "hdd", Status: model.StorageDiskFailed, Attrs: "{}"}
	must(t, db.Create(odd).Error)
	task, err = StorageClusters.RemoveDisk(ctx, f.cluster.UUID, odd.UUID)
	must(t, err)
	sent = f.expect("check the odd claim", "gpfs_ece.sh", h[0])
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"pdisk":"n002p001"}`))
	f.wantTask(task.ID, model.StorageTaskFailed, "which no failed record of this host has")
	f.expect("nothing released", "")
	if got := status(newer); got.Name != "n002p001" || got.Status != model.StorageDiskActive || got.DeletedAt.Valid {
		t.Fatalf("the working pdisk's record changed: %+v", got)
	}
	must(t, AbortStorageTask(ctx, task.ID))
	if got := status(odd); got.DeletedAt.Valid || got.Status != model.StorageDiskFailed {
		t.Fatalf("the odd claim after the abort: %+v", got)
	}
}
