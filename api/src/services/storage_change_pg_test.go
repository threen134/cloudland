/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Replacing a failed disk, changing the roles of a member and the hosts that join as clients on their own, against
// PostgreSQL with the fake cland (shared-storage-design.md §6.3, §7.5, §8.5, §13.1). On the GPFS cluster of the shared
// fixture; the Ceph steps are covered by TestStorageCephChangesPG.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"

	"github.com/spf13/viper"
)

func TestStorageReplaceDiskPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	disks := []*model.StorageClusterDisk{}
	db.Where("cluster_id = ?", f.cluster.ID).Order("hostid").Find(&disks)
	for _, d := range disks {
		must(t, db.Model(&model.StorageClusterDisk{}).Where("id = ?", d.ID).Update("name", fmt.Sprintf("cl%dh%dd1", f.cluster.ID, d.Hostid)).Error)
	}
	old := disks[1]
	oldName := fmt.Sprintf("cl%dh%dd1", f.cluster.ID, old.Hostid)
	must(t, db.Model(&model.StorageClusterDisk{}).Where("id = ?", old.ID).Update("attrs", `{"usage":"dataOnly","gpfs_pool":"data"}`).Error)
	scanned := func(hostid int32, id string) {
		db.Unscoped().Where("hostid = ? AND disk_id = ?", hostid, id).Delete(&model.HyperDisk{})
		must(t, db.Create(&model.HyperDisk{Hostid: hostid, DiskID: id, Name: "sdc", Serial: "S" + id, SizeBytes: 1 << 40, Media: "hdd",
			State: model.DiskFree, ScannedAt: time.Now()}).Error)
	}
	scanned(h[1], "wwn-rep-new")
	scanned(h[2], "wwn-rep-other")
	ok := func(sent []*stcSent, result string) {
		for _, s := range sent {
			must(t, f.report(s, model.StorageRunSucceeded, "", result))
		}
	}

	// Not before the health check saw the disk, not while it works, not with itself
	_, err := StorageClusters.ReplaceDisk(ctx, f.cluster.UUID, old.UUID, &StorageDiskPlan{DiskID: "wwn-rep-new"})
	wantCode(t, err, ErrStorageInvalidPlan, "has not seen")
	must(t, db.Model(&model.StorageClusterDisk{}).Where("id = ?", old.ID).Updates(map[string]interface{}{"state": "up", "checked_at": time.Now()}).Error)
	_, err = StorageClusters.ReplaceDisk(ctx, f.cluster.UUID, old.UUID, &StorageDiskPlan{DiskID: "wwn-rep-new"})
	wantCode(t, err, ErrStorageInvalidPlan, "the normal way")
	// Down, but seen too long ago: a fresh check first
	must(t, db.Model(&model.StorageClusterDisk{}).Where("id = ?", old.ID).Updates(map[string]interface{}{"state": "down", "checked_at": time.Now().Add(-20 * time.Minute)}).Error)
	_, err = StorageClusters.ReplaceDisk(ctx, f.cluster.UUID, old.UUID, &StorageDiskPlan{DiskID: "wwn-rep-new"})
	wantCode(t, err, ErrStorageInvalidPlan, "a stale state")
	must(t, db.Model(&model.StorageClusterDisk{}).Where("id = ?", old.ID).Update("checked_at", time.Now()).Error)
	_, err = StorageClusters.ReplaceDisk(ctx, f.cluster.UUID, old.UUID, &StorageDiskPlan{DiskID: old.DiskID})
	wantCode(t, err, ErrStorageInvalidPlan, "another disk")
	// A disk of another host is taken as the same host's: it is not scanned there
	_, err = StorageClusters.ReplaceDisk(ctx, f.cluster.UUID, old.UUID, &StorageDiskPlan{Hostid: h[2], DiskID: "wwn-rep-other"})
	if err == nil {
		t.Fatal("a disk of another host replaced the failed one")
	}

	task, err := StorageClusters.ReplaceDisk(ctx, f.cluster.UUID, old.UUID, &StorageDiskPlan{DiskID: "wwn-rep-new"})
	must(t, err)
	// The failed disk is not looked for on its host
	sent := f.expect("resolve", "stc_resolve_disks.sh", h[1])
	if ids := sent[0].input["disks"].([]interface{}); len(ids) != 1 || ids[0].(map[string]interface{})["id"] != "wwn-rep-new" {
		t.Fatalf("resolve input %v", sent[0].input)
	}
	ok(sent, `{"disks":[{"id":"wwn-rep-new","path":"/dev/sdc"}]}`)
	sent = f.expect("nsd", "gpfs_nsd.sh", h[0])
	nsd := sent[0].input["nsds"].([]interface{})[0].(map[string]interface{})
	newName := fmt.Sprintf("cl%dh%dd2", f.cluster.ID, h[1])
	if nsd["name"] != newName || nsd["usage"] != "dataOnly" || nsd["pool"] != "data" || nsd["failure_group"] != float64(2) {
		t.Fatalf("nsd input %v", nsd)
	}
	ok(sent, "{}")
	sent = f.expect("remove", "gpfs_fs.sh", h[0])
	if sent[0].input["action"] != "remove" || sent[0].input["damaged"] != true || sent[0].input["require_down"] != true ||
		fmt.Sprint(sent[0].input["nsds"]) != "["+oldName+"]" {
		t.Fatalf("remove input %v", sent[0].input)
	}
	ok(sent, "{}")
	sent = f.expect("add", "gpfs_fs.sh", h[0])
	if sent[0].input["action"] != "add" || len(sent[0].input["nsds"].([]interface{})) != 1 {
		t.Fatalf("add input %v", sent[0].input)
	}
	ok(sent, "{}")
	sent = f.expect("restore", "gpfs_fs.sh", h[0])
	if sent[0].input["action"] != "restore" || sent[0].input["fs_name"] != "fs1" {
		t.Fatalf("restore input %v", sent[0].input)
	}
	ok(sent, "{}")
	sent = f.expect("release", "stc_release_disks.sh", h[1])
	if sent[0].input["no_wipe"] != true || fmt.Sprint(sent[0].input["keep"]) != "[wwn-rep-new]" || len(sent[0].input["release"].([]interface{})) != 1 {
		t.Fatalf("release input %v", sent[0].input)
	}
	ok(sent, "{}")
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	if db.Take(&model.StorageClusterDisk{}, old.ID).Error == nil {
		t.Fatal("the failed disk is still claimed")
	}
	nd := &model.StorageClusterDisk{}
	must(t, db.Where("cluster_id = ? AND disk_id = ?", f.cluster.ID, "wwn-rep-new").Take(nd).Error)
	if nd.Status != model.StorageDiskActive || nd.Name != newName || nd.FsID != f.fs.ID || !strings.Contains(nd.Attrs, `"dataOnly"`) {
		t.Fatalf("new disk %+v", nd)
	}

	// Aborted half way: the new disk and the old one are marked failed
	must(t, db.Model(&model.StorageClusterDisk{}).Where("id = ?", disks[2].ID).Updates(map[string]interface{}{"state": "down", "checked_at": time.Now()}).Error)
	scanned(h[2], "wwn-rep-third")
	task, err = StorageClusters.ReplaceDisk(ctx, f.cluster.UUID, disks[2].UUID, &StorageDiskPlan{DiskID: "wwn-rep-third"})
	must(t, err)
	sent = f.expect("resolve", "stc_resolve_disks.sh", h[2])
	must(t, f.report(sent[0], model.StorageRunFailed, "the disk is not there", ""))
	f.wantTask(task.ID, model.StorageTaskFailed, "")
	must(t, AbortStorageTask(ctx, task.ID))
	f.wantTask(task.ID, model.StorageTaskAborted, "")
	d3, n3 := &model.StorageClusterDisk{}, &model.StorageClusterDisk{}
	must(t, db.Take(d3, disks[2].ID).Error)
	must(t, db.Where("cluster_id = ? AND disk_id = ?", f.cluster.ID, "wwn-rep-third").Take(n3).Error)
	if d3.Status != model.StorageDiskFailed || n3.Status != model.StorageDiskFailed {
		t.Fatalf("after the abort: old %s, new %s", d3.Status, n3.Status)
	}
}

func TestStorageChangeRolesPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	viper.Set("vpn.secret_key", "storage-gpfs-pg-test")
	if err := SecretStoreReady(); err != nil {
		t.Skipf("the credential store was set up without a key earlier in this test binary: %v", err)
	}
	pub, priv, err := newStorageSSHKey()
	must(t, err)
	must(t, db.Model(f.cluster).Updates(map[string]interface{}{"ssh_pub_key": pub, "ssh_priv_key": priv}).Error)
	for i, hh := range h {
		must(t, db.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND hostid = ?", f.cluster.ID, hh).
			Update("attrs", fmt.Sprintf(`{"failure_group":%d,"host_key":"ssh-ed25519 KEY%d","hostname":"h%d"}`, i+1, hh, hh)).Error)
	}
	node := func(hostid int32) *model.StorageClusterNode {
		n := &model.StorageClusterNode{}
		must(t, db.Where("cluster_id = ? AND hostid = ?", f.cluster.ID, hostid).Take(n).Error)
		return n
	}
	ok := func(sent []*stcSent) {
		for _, s := range sent {
			must(t, f.report(s, model.StorageRunSucceeded, "", "{}"))
		}
	}

	// Refused: nothing changes, an unknown role, an even number of quorum hosts, a third admin host
	_, err = StorageClusters.ChangeRoles(ctx, f.cluster.UUID, h[1], []string{"quorum"})
	wantCode(t, err, ErrStorageInvalidPlan, "do not change")
	_, err = StorageClusters.ChangeRoles(ctx, f.cluster.UUID, h[1], []string{"mon"})
	wantCode(t, err, ErrStorageInvalidPlan, "Unknown role")
	_, err = StorageClusters.ChangeRoles(ctx, f.cluster.UUID, h[2], []string{"client"})
	wantCode(t, err, ErrStorageInvalidPlan, "quorum")

	// h[1] becomes an admin host too: every member gets the trust again, the admin hosts the key
	task, err := StorageClusters.ChangeRoles(ctx, f.cluster.UUID, h[1], []string{"admin", "quorum"})
	must(t, err)
	if n := node(h[1]); n.Roles != "admin,quorum,nsd" {
		t.Fatalf("roles recorded with the task: %q", n.Roles)
	}
	sent := f.expect("trust", "stc_ssh_trust.sh", h...)
	admins := 0
	for _, s := range sent {
		if _, has := s.input["private_key"]; has {
			admins++
		}
	}
	if admins != 2 {
		t.Fatalf("hosts given the cluster key: %d", admins)
	}
	ok(sent)
	sent = f.expect("change roles", "gpfs_cluster.sh", h[0])
	if len(sent) != 1 || sent[0].input["action"] != "roles" || sent[0].input["ip"] != "10.93.0.2" || sent[0].input["quorum"] != true || sent[0].input["server"] != true {
		t.Fatalf("change roles input %v", sent)
	}
	ok(sent)
	ok(f.expect("finish", "stc_finish.sh", h[1]))
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")

	// Taken back, and aborted: the roles before come back
	task, err = StorageClusters.ChangeRoles(ctx, f.cluster.UUID, h[1], []string{"quorum"})
	must(t, err)
	sent = f.expect("trust", "stc_ssh_trust.sh", h...)
	must(t, f.report(sent[0], model.StorageRunFailed, "no", ""))
	ok(sent[1:])
	f.wantTask(task.ID, model.StorageTaskFailed, "")
	must(t, AbortStorageTask(ctx, task.ID))
	if n := node(h[1]); n.Roles != "admin,quorum,nsd" || !strings.Contains(n.Reason, "aborted") {
		t.Fatalf("roles after the abort: %+v", n)
	}
}

func TestStorageAutoJoinPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db := f.ctx, f.db
	h4 := stcHosts[3]
	zone := &model.Zone{Name: fmt.Sprintf("aj-%d", time.Now().UnixNano()%1000000)}
	must(t, db.Create(zone).Error)
	defer db.Unscoped().Delete(zone)
	must(t, db.Model(&model.Hyper{}).Where("hostid = ?", h4).Update("zone_id", zone.ID).Error)
	load := func() *model.StorageCluster {
		c := &model.StorageCluster{}
		must(t, db.Take(c, f.cluster.ID).Error)
		return c
	}
	pending := func() []*StoragePendingClient { return ParseStoragePendingClients(load().PendingClients) }

	// An unknown zone is refused; an imported cluster has no auto join
	bad := []string{"00000000-0000-0000-0000-000000000000"}
	_, err := StorageClusters.Update(ctx, f.cluster.UUID, &StorageClusterUpdate{AutoJoinZones: &bad})
	wantCode(t, err, ErrStorageInvalidPlan, "not found")
	zones := []string{zone.UUID}
	c, err := StorageClusters.Update(ctx, f.cluster.UUID, &StorageClusterUpdate{AutoJoinZones: &zones})
	must(t, err)
	if c.AutoJoinZones != fmt.Sprintf("[%d]", zone.ID) {
		t.Fatalf("zones: %q", c.AutoJoinZones)
	}

	// The round queues the host of the zone and starts its task at once, as a client
	maintainAutoJoin(ctx, load())
	p := pending()
	if len(p) != 1 || p[0].Hostid != h4 || p[0].Status != StoragePendingJoining || p[0].TaskID == 0 {
		raw, _ := json.Marshal(p)
		t.Fatalf("pending after the first round: %s", raw)
	}
	n4 := &model.StorageClusterNode{}
	must(t, db.Where("cluster_id = ? AND hostid = ?", f.cluster.ID, h4).Take(n4).Error)
	if n4.Roles != "client" || n4.Status != model.StorageNodeJoining {
		t.Fatalf("joining host %+v", n4)
	}
	// Its precheck fails (an unsupported system): the next round aborts the task, the one after marks the host
	sent := f.expect("precheck", "stc_precheck.sh", h4)
	must(t, f.report(sent[0], model.StorageRunFailed, "Ubuntu 26.04 is not supported by GPFS", ""))
	maintainAutoJoin(ctx, load())
	f.wantTask(p[0].TaskID, model.StorageTaskAborted, "")
	maintainAutoJoin(ctx, load())
	p = pending()
	if len(p) != 1 || p[0].Status != StoragePendingFailed || !strings.Contains(p[0].Reason, "not supported") {
		raw, _ := json.Marshal(p)
		t.Fatalf("pending after the failure: %s", raw)
	}
	// Left alone: no new task while it stays failed
	f.cland.take()
	maintainAutoJoin(ctx, load())
	if c := load(); c.ActiveTask != 0 {
		t.Fatal("a failed host was tried again")
	}
	// Retried while its member record from the aborted join is still there: kept, told to remove the host first
	_, err = StorageClusters.Update(ctx, f.cluster.UUID, &StorageClusterUpdate{RetryAutoJoin: true})
	must(t, err)
	if p := pending(); len(p) != 1 || p[0].Status != StoragePendingFailed || !strings.Contains(p[0].Reason, "remove the host") {
		t.Fatalf("retry with the member record there: %+v", p)
	}
	// Its error record removed by hand and retried: queued again
	db.Unscoped().Where("cluster_id = ? AND hostid = ?", f.cluster.ID, h4).Delete(&model.StorageClusterNode{})
	_, err = StorageClusters.Update(ctx, f.cluster.UUID, &StorageClusterUpdate{RetryAutoJoin: true})
	must(t, err)
	if len(pending()) != 0 {
		t.Fatal("a failed host stays after the retry")
	}
	maintainAutoJoin(ctx, load())
	p = pending()
	if len(p) != 1 || p[0].Status != StoragePendingJoining {
		t.Fatalf("pending after the retry: %+v", p)
	}
	// This time it joins: the entry goes once the task succeeded. The steps are those of any added host
	// (TestGPFSClusterChangesPG); here the task is finished by hand after its precheck
	f.expect("precheck again", "stc_precheck.sh", h4)
	task := f.task(p[0].TaskID)
	if task.Status != model.StorageTaskRunning || task.Kind != StorageTaskAddNodes {
		t.Fatalf("auto join task %s %s: %s", task.Kind, task.Status, task.Message)
	}
	must(t, db.Model(&model.StorageTask{}).Where("id = ?", task.ID).Update("status", model.StorageTaskSucceeded).Error)
	must(t, db.Model(&model.StorageCluster{}).Where("id = ?", f.cluster.ID).Update("active_task", 0).Error)
	maintainAutoJoin(ctx, load())
	if p := pending(); len(p) != 0 {
		t.Fatalf("pending after the join: %+v", p)
	}
	// A cluster another task took in the meantime is not the host's failure: it keeps waiting
	other := &model.StorageTask{ClusterID: f.cluster.ID, Kind: StorageTaskAddDisks, Status: model.StorageTaskRunning}
	must(t, db.Create(other).Error)
	defer db.Unscoped().Delete(other)
	db.Unscoped().Where("cluster_id = ? AND hostid = ?", f.cluster.ID, h4).Delete(&model.StorageClusterNode{})
	must(t, db.Model(&model.StorageCluster{}).Where("id = ?", f.cluster.ID).Update("pending_clients",
		fmt.Sprintf(`[{"hostid":%d,"status":"pending","since":"2026-10-04T00:00:00Z"}]`, h4)).Error)
	next := ParseStoragePendingClients(load().PendingClients)[0]
	// The round saw the slot free, then the other task took it before AddNodes
	must(t, db.Model(&model.StorageCluster{}).Where("id = ?", f.cluster.ID).Update("active_task", other.ID).Error)
	autoJoinStart(ctx, load(), next)
	if p := pending(); len(p) != 1 || p[0].Status != StoragePendingWaiting {
		t.Fatalf("a busy cluster: %+v", p)
	}
	must(t, db.Model(&model.StorageCluster{}).Where("id = ?", f.cluster.ID).Update("active_task", 0).Error)
	// Turned off: nothing waits
	none := []string{}
	_, err = StorageClusters.Update(ctx, f.cluster.UUID, &StorageClusterUpdate{AutoJoinZones: &none})
	must(t, err)
	if c := load(); c.AutoJoinZones != "[]" || c.PendingClients != "" {
		t.Fatalf("after turning it off: %q %q", c.AutoJoinZones, c.PendingClients)
	}
}
