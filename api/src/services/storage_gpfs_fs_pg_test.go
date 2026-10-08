/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// More file systems on a managed GPFS cluster against PostgreSQL with the fake cland (shared-storage-design.md §7.3):
// one made on new disks (the checks, its NSDs, the file system, its record), a disk added into it by name, that disk
// removed from it (not from the first file system), a pool on it refusing its deletion, its deletion with its disks,
// and the last file system that does not go.

import (
	"fmt"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

func TestStorageGPFSFilesystemsPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	scanned := func(host int32, id string) {
		db.Unscoped().Where("hostid = ? AND disk_id = ?", host, id).Delete(&model.HyperDisk{})
		must(t, db.Create(&model.HyperDisk{Hostid: host, DiskID: id, Name: "sdc", Serial: "S" + id, SizeBytes: 1 << 40, Media: "hdd",
			State: model.DiskFree, ScannedAt: time.Now()}).Error)
	}
	newDisks := []*StorageDiskPlan{}
	for i, host := range h {
		id := fmt.Sprintf("wwn-fs2-%d", i)
		scanned(host, id)
		newDisks = append(newDisks, &StorageDiskPlan{Hostid: host, DiskID: id})
	}
	ok := func(*stcSent) string { return "{}" }
	succeed := func(sent []*stcSent, result func(*stcSent) string) {
		for _, s := range sent {
			must(t, f.report(s, model.StorageRunSucceeded, "", result(s)))
		}
	}
	// What resolve_disks reports on a host: every disk of the cluster there, the new ones on /dev/sdc
	resolved := func(s *stcSent) string {
		out := ""
		for _, d := range s.input["disks"].([]interface{}) {
			id := d.(map[string]interface{})["id"].(string)
			path := "/dev/sdb"
			if id != fmt.Sprintf("wwn-shared-%d", indexOf(h, s.hostid)) {
				path = "/dev/sdc"
			}
			if out != "" {
				out += ","
			}
			out += fmt.Sprintf(`{"id":%q,"path":%q}`, id, path)
		}
		return `{"disks":[` + out + `]}`
	}

	// The checks: a name GPFS takes, one not in use, disks on three hosts, a block size GPFS takes
	_, err := StorageClusters.CreateFilesystem(ctx, f.cluster.UUID, &StorageFilesystemPlan{Name: "2fs"}, newDisks, false)
	wantCodeMsg(t, err, ErrStorageInvalidPlan, "starts with a letter")
	_, err = StorageClusters.CreateFilesystem(ctx, f.cluster.UUID, &StorageFilesystemPlan{Name: "fs2"}, newDisks[:2], false)
	wantCodeMsg(t, err, ErrStorageInvalidPlan, "at least 3 hosts")
	_, err = StorageClusters.CreateFilesystem(ctx, f.cluster.UUID, &StorageFilesystemPlan{Name: "fs2", BlockSize: "3M"}, newDisks, false)
	wantCodeMsg(t, err, ErrStorageInvalidPlan, "block size")
	_, err = StorageClusters.CreateFilesystem(ctx, f.cluster.UUID, &StorageFilesystemPlan{Name: "fs1"}, newDisks, false)
	wantCodeMsg(t, err, ErrStorageInvalidPlan, "has a file system fs1 already")

	// fs2 on a new disk of every host: its record is made with the task, its disks are in it from the start
	task, err := StorageClusters.CreateFilesystem(ctx, f.cluster.UUID, &StorageFilesystemPlan{Name: "fs2", BlockSize: "1M"}, newDisks, false)
	must(t, err)
	fs2 := &model.StorageFilesystem{}
	must(t, db.Where("cluster_id = ? AND name = ?", f.cluster.ID, "fs2").Take(fs2).Error)
	if fs2.Status != "creating" || fs2.MountPoint != "/gpfs/fs2" || fs2.BlockSize != "1M" || fs2.DataReplicas != 2 || fs2.MetaReplicas != 3 ||
		storageTaskInt(f.task(task.ID), "filesystem_id") != fs2.ID {
		t.Fatalf("new file system %+v, task %s", fs2, f.task(task.ID).Params)
	}
	var claimed int64
	db.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND fs_id = ? AND status = ?", f.cluster.ID, fs2.ID, model.StorageDiskClaiming).Count(&claimed)
	if claimed != 3 {
		t.Fatalf("disks of fs2 being claimed: %d", claimed)
	}
	succeed(f.expect("resolve", "stc_resolve_disks.sh", h...), resolved)
	sent := f.expect("nsds", "gpfs_nsd.sh", h[0])
	if nsds := sent[0].input["nsds"].([]interface{}); len(nsds) != 3 ||
		nsds[0].(map[string]interface{})["name"] != fmt.Sprintf("cl%dh%dd2", f.cluster.ID, nsds0Host(nsds)) {
		t.Fatalf("nsd input %v", sent[0].input)
	}
	succeed(sent, ok)
	sent = f.expect("create fs2", "gpfs_fs.sh", h[0])
	in := sent[0].input
	if in["action"] != "create" || in["fs_name"] != "fs2" || in["mount_point"] != "/gpfs/fs2" || in["block_size"] != "1M" ||
		in["data_replicas"] != float64(2) || in["meta_replicas"] != float64(3) || len(in["nsds"].([]interface{})) != 3 {
		t.Fatalf("create_fs input %v", in)
	}
	succeed(sent, func(*stcSent) string { return `{"capacity_bytes":3298534883328,"free_bytes":3298000000000}` })
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	must(t, db.Take(fs2, fs2.ID).Error)
	if fs2.Status != "ready" || fs2.CapacityBytes != 3298534883328 {
		t.Fatalf("fs2 made %+v", fs2)
	}
	var active int64
	db.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND fs_id = ? AND status = ?", f.cluster.ID, fs2.ID, model.StorageDiskActive).Count(&active)
	if active != 3 {
		t.Fatalf("active disks of fs2: %d", active)
	}

	// A disk into fs2 by name
	scanned(h[1], "wwn-fs2x-1")
	_, err = StorageClusters.AddDisks(ctx, f.cluster.UUID, &StorageClusterExpand{Disks: []*StorageDiskPlan{{Hostid: h[1], DiskID: "wwn-fs2x-1"}}, Filesystem: "nofs"})
	wantCodeMsg(t, err, ErrStorageInvalidPlan, "no ready file system nofs")
	task, err = StorageClusters.AddDisks(ctx, f.cluster.UUID, &StorageClusterExpand{Disks: []*StorageDiskPlan{{Hostid: h[1], DiskID: "wwn-fs2x-1"}}, Filesystem: "fs2"})
	must(t, err)
	succeed(f.expect("resolve", "stc_resolve_disks.sh", h[1]), func(s *stcSent) string {
		return `{"disks":[{"id":"wwn-shared-1","path":"/dev/sdb"},{"id":"wwn-fs2-1","path":"/dev/sdc"},{"id":"wwn-fs2x-1","path":"/dev/sdd"}]}`
	})
	succeed(f.expect("nsd", "gpfs_nsd.sh", h[0]), ok)
	sent = f.expect("add into fs2", "gpfs_fs.sh", h[0])
	if sent[0].input["action"] != "add" || sent[0].input["fs_name"] != "fs2" {
		t.Fatalf("add disks input %v", sent[0].input)
	}
	succeed(sent, ok)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	extra := &model.StorageClusterDisk{}
	must(t, db.Where("cluster_id = ? AND disk_id = ?", f.cluster.ID, "wwn-fs2x-1").Take(extra).Error)
	if extra.FsID != fs2.ID || extra.Status != model.StorageDiskActive {
		t.Fatalf("disk added into fs2 %+v", extra)
	}
	// It leaves fs2, not fs1
	task, err = StorageClusters.RemoveDisk(ctx, f.cluster.UUID, extra.UUID)
	must(t, err)
	sent = f.expect("remove from fs2", "gpfs_fs.sh", h[0])
	if sent[0].input["action"] != "remove" || sent[0].input["fs_name"] != "fs2" || removedNSDs(sent[0].input) != "["+extra.Name+"]" {
		t.Fatalf("remove input %v", sent[0].input)
	}
	succeed(sent, ok)
	succeed(f.expect("release", "stc_release_disks.sh", h[1]), ok)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")

	// A host with disks in fs1 and fs2 leaves (its disks marked as a removal marks them): one group of NSDs for each
	// file system, not a failure for the disks being in two
	must(t, db.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND hostid = ?", f.cluster.ID, h[2]).
		Update("status", model.StorageDiskRemoving).Error)
	rmIn, err := gpfsRemoveDisksInput(ctx, db, &model.StorageTask{ClusterID: f.cluster.ID, Params: "{}"}, nil, h[0])
	must(t, db.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND hostid = ?", f.cluster.ID, h[2]).
		Update("status", model.StorageDiskActive).Error)
	must(t, err)
	groups := rmIn.(map[string]interface{})["groups"].([]map[string]interface{})
	if len(groups) != 2 || groups[0]["fs_name"] != "fs1" || groups[1]["fs_name"] != "fs2" ||
		len(groups[0]["nsds"].([]string)) != 1 || len(groups[1]["nsds"].([]string)) != 1 {
		t.Fatalf("remove input of a host in two file systems %v", rmIn)
	}

	// A pool on fs2 keeps it
	pool := &model.StoragePool{Name: fmt.Sprintf("fs2-pool-%d", time.Now().UnixNano()%1000000), Driver: model.StorageDriverGPFS, ClusterID: f.cluster.ID,
		FilesystemID: fs2.ID, Status: model.StoragePoolActive}
	must(t, db.Create(pool).Error)
	_, err = StorageClusters.DeleteFilesystem(ctx, f.cluster.UUID, "fs2")
	wantCodeMsg(t, err, ErrStorageInvalidPlan, "storage pools are on file system fs2")
	must(t, db.Unscoped().Delete(pool).Error)

	// Deleted with its disks; the disks of fs1 stay on their hosts
	task, err = StorageClusters.DeleteFilesystem(ctx, f.cluster.UUID, "fs2")
	must(t, err)
	must(t, db.Take(fs2, fs2.ID).Error)
	if fs2.Status != "deleting" {
		t.Fatalf("fs2 being deleted %+v", fs2)
	}
	sent = f.expect("delete fs2", "gpfs_fs.sh", h[0])
	if sent[0].input["action"] != "delete" || sent[0].input["fs_name"] != "fs2" || sent[0].input["mount_point"] != "/gpfs/fs2" ||
		len(sent[0].input["nsds"].([]interface{})) != 3 {
		t.Fatalf("delete_fs input %v", sent[0].input)
	}
	succeed(sent, ok)
	sent = f.expect("release", "stc_release_disks.sh", h...)
	for _, s := range sent {
		keep := s.input["keep"].([]interface{})
		if len(keep) != 1 || keep[0] != fmt.Sprintf("wwn-shared-%d", indexOf(h, s.hostid)) || len(s.input["release"].([]interface{})) != 1 {
			t.Fatalf("release input of host %d: %v", s.hostid, s.input)
		}
	}
	succeed(sent, ok)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	if db.Take(&model.StorageFilesystem{}, fs2.ID).Error == nil {
		t.Fatal("fs2 is still recorded")
	}
	var left int64
	db.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND fs_id = ?", f.cluster.ID, fs2.ID).Count(&left)
	if left != 0 {
		t.Fatalf("%d disks of fs2 left", left)
	}
	// The last file system goes with the cluster
	_, err = StorageClusters.DeleteFilesystem(ctx, f.cluster.UUID, "fs1")
	wantCodeMsg(t, err, ErrStorageInvalidPlan, "only file system")
}

func indexOf(list []int32, x int32) int {
	for i, v := range list {
		if v == x {
			return i
		}
	}
	return -1
}

// nsds0Host is the host whose NSD is listed first: its server address 10.93.0.<n> is host stcHosts[n-1]
func nsds0Host(nsds []interface{}) int32 {
	var n int
	fmt.Sscanf(nsds[0].(map[string]interface{})["server"].(string), "10.93.0.%d", &n)
	return stcHosts[n-1]
}

// removedNSDs is the NSDs a remove input of gpfs_fs.sh takes out, over all its file system groups, as fmt prints a list
func removedNSDs(input map[string]interface{}) string {
	all := []interface{}{}
	groups, _ := input["groups"].([]interface{})
	for _, g := range groups {
		nsds, _ := g.(map[string]interface{})["nsds"].([]interface{})
		all = append(all, nsds...)
	}
	return fmt.Sprint(all)
}
