/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Ceph against PostgreSQL with the fake cland standing in for the hosts (shared-storage-design.md §8): a managed
// deployment through every step (what each step sends, what the configuration step keeps of the client key, what
// the cluster records at the end), an RBD pool with a volume in it, changes, the deletion, and an imported cluster
// with a registered RBD pool. The node scripts are covered by the WSL test against a real single host Ceph.

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

const cephTestKey = "AQBz7Bpn0L2nGRAAyQp7Fq8m0n7rG3XbM0aFNQ=="

func cephPrecheckResult(osName string) func(s *stcSent) string {
	return func(s *stcSent) string {
		return fmt.Sprintf(`{"items":[],"facts":{"hostname":"h%d","os":%q,"host_key":"ssh-ed25519 KEY%d"}}`, s.hostid, osName, s.hostid)
	}
}

func TestStorageCephDeployPG(t *testing.T) {
	f := newStcFixture(t)
	ctx, db := f.ctx, f.db
	viper.Set("vpn.secret_key", "storage-ceph-pg-test")
	if err := SecretStoreReady(); err != nil {
		t.Skipf("the credential store was set up without a key earlier in this test binary: %v", err)
	}
	h := stcHosts
	for i, host := range h[:3] {
		must(t, db.Create(&model.HyperDisk{Hostid: host, DiskID: fmt.Sprintf("wwn-0x5000ceb%d", i), Name: "sdb", Serial: fmt.Sprintf("CSN%d", i),
			SizeBytes: 2 << 40, Media: "hdd", State: model.DiskFree, ScannedAt: time.Now()}).Error)
	}
	name := fmt.Sprintf("ceph-pg-%d", time.Now().UnixNano()%1000000)
	plan := func(params string) *StoragePlan {
		return &StoragePlan{Kind: "ceph", Params: json.RawMessage(params),
			Nodes: []*StorageNodePlan{{Hostid: h[0], Roles: []string{"admin", "mon", "mgr", "osd"}}, {Hostid: h[1], Roles: []string{"mon", "osd"}},
				{Hostid: h[2], Roles: []string{"mon", "osd"}}, {Hostid: h[3], Roles: []string{"client"}}},
			Disks: []*StorageDiskPlan{{Hostid: h[0], DiskID: "wwn-0x5000ceb0"}, {Hostid: h[1], DiskID: "wwn-0x5000ceb1"}, {Hostid: h[2], DiskID: "wwn-0x5000ceb2"}}}
	}
	_, _, err := StorageClusters.Create(ctx, &StorageClusterCreate{Name: name, Plan: plan(`{"replicas":2}`)})
	wantCode(t, err, ErrStorageInvalidPlan, "two replicas")
	_, _, err = StorageClusters.Create(ctx, &StorageClusterCreate{Name: name, Plan: plan(`{"image":"Quay.io/x y"}`)})
	wantCode(t, err, ErrStorageInvalidPlan, "bad image")
	_, _, err = StorageClusters.Create(ctx, &StorageClusterCreate{Name: name, PackageUUID: "x", Plan: plan(`{}`)})
	wantCode(t, err, ErrStorageInvalidPlan, "a package for ceph")

	cluster, task, err := StorageClusters.Create(ctx, &StorageClusterCreate{Name: name, Plan: plan(`{"osd_memory_target_mib":1024,"cluster_network":"10.94.0.0/24"}`)})
	must(t, err)
	defer func() {
		db.Unscoped().Where("cluster_id = ?", cluster.ID).Delete(&model.StorageClusterDisk{})
		db.Unscoped().Where("cluster_id = ?", cluster.ID).Delete(&model.StorageClusterNode{})
		db.Unscoped().Where("id = ?", cluster.ID).Delete(&model.StorageCluster{})
	}()
	_, nodes, _, err := StorageClusters.Get(ctx, cluster.UUID)
	must(t, err)
	reserved := map[int32]int32{}
	for _, n := range nodes {
		reserved[n.Hostid] = n.ReservedMemMB
	}
	// mon 2048 + mgr 1024 + one OSD (1024 + 512); a client reserves nothing
	if reserved[h[0]] != 4608 || reserved[h[1]] != 3584 || reserved[h[3]] != 0 {
		t.Fatalf("memory reservations %v", reserved)
	}

	succeed := func(sent []*stcSent, result func(s *stcSent) string) {
		for _, s := range sent {
			must(t, f.report(s, model.StorageRunSucceeded, "", result(s)))
		}
	}
	none := func(*stcSent) string { return "{}" }

	// The hosts must run one release: each installs the Ceph of its own distribution
	sent := f.expect("precheck", "stc_precheck.sh", h...)
	succeed(sent, func(s *stcSent) string {
		if s.hostid == h[3] {
			return cephPrecheckResult("ubuntu 26.04")(s)
		}
		return cephPrecheckResult("ubuntu 24.04")(s)
	})
	f.wantTask(task.ID, model.StorageTaskFailed, "different releases")
	must(t, RetryStorageTask(ctx, task.ID))
	succeed(f.expect("precheck again", "stc_precheck.sh", h...), cephPrecheckResult("ubuntu 24.04"))

	sent = f.expect("join", "stc_join.sh", h...)
	if sent[0].input["hold_kernel"] != false || sent[0].input["kind"] != "ceph" {
		t.Fatalf("join input %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("install", "ceph_install.sh", h...)
	for _, s := range sent {
		if s.input["orch"] != (s.hostid != h[3]) || s.input["image"] != "" {
			t.Fatalf("install input of host %d: %v", s.hostid, s.input)
		}
		// The cephadm hosts protect the daemons from the OOM killer, memory.low being the reservation of each daemon
		oom, _ := s.input["oom"].(map[string]interface{})
		if s.hostid == h[3] {
			if oom != nil {
				t.Fatalf("a client host gets OOM protection: %v", s.input)
			}
		} else if oom["adj"] != float64(-900) || oom["mon_bytes"] != float64(2048<<20) || oom["mgr_bytes"] != float64(1024<<20) ||
			oom["osd_bytes"] != float64((1024+512)<<20) {
			t.Fatalf("OOM protection of host %d: %v", s.hostid, oom)
		}
	}
	succeed(sent, func(s *stcSent) string {
		if s.hostid == h[3] {
			return `{"version":"19.2.3","image":""}`
		}
		return `{"version":"19.2.3","image":"quay.io/ceph/ceph:v19.2.3"}`
	})
	c := &model.StorageCluster{}
	must(t, db.Take(c, cluster.ID).Error)
	if info := cephInfoOf(c); info.Image != "quay.io/ceph/ceph:v19.2.3" || info.Version != "19.2.3" || c.Version != "19.2.3" || info.Fsid != c.UUID {
		t.Fatalf("after install %+v %+v", c, info)
	}
	// The admin host logs in to the others, and so does the orchestrator from the mgr host: here the same host
	sent = f.expect("trust", "stc_ssh_trust.sh", h...)
	for _, s := range sent {
		from, _ := s.input["from"].([]interface{})
		_, hasKey := s.input["private_key"]
		if len(from) != 1 || from[0] != "10.93.0.1" || hasKey != (s.hostid == h[0]) || s.input["kind"] != "ceph" {
			t.Fatalf("trust input of host %d: %v", s.hostid, s.input)
		}
	}
	succeed(sent, none)
	sent = f.expect("bootstrap", "ceph_cluster.sh", h[0])
	in := sent[0].input
	labels, _ := in["labels"].([]interface{})
	if in["action"] != "bootstrap" || in["fsid"] != cluster.UUID || in["mon_ip"] != "10.93.0.1" || in["hostname"] != fmt.Sprintf("h%d", h[0]) ||
		in["image"] != "quay.io/ceph/ceph:v19.2.3" || in["cluster_network"] != "10.94.0.0/24" || in["single_host"] != false ||
		len(labels) != 4 || labels[3] != "_admin" {
		t.Fatalf("bootstrap input %v", in)
	}
	succeed(sent, none)
	sent = f.expect("add hosts", "ceph_cluster.sh", h[0])
	hosts, _ := sent[0].input["hosts"].([]interface{})
	if sent[0].input["action"] != "add_hosts" || sent[0].input["mons"] != float64(3) || len(hosts) != 3 {
		t.Fatalf("add_hosts input %v", sent[0].input)
	}
	if h2 := hosts[2].(map[string]interface{}); h2["ip"] != "10.93.0.3" || h2["hostname"] != fmt.Sprintf("h%d", h[2]) ||
		len(h2["labels"].([]interface{})) != 2 {
		t.Fatalf("third host %v", h2)
	}
	succeed(sent, none)
	sent = f.expect("configure", "ceph_cluster.sh", h[0])
	if sent[0].input["action"] != "configure" || sent[0].input["replicas"] != float64(3) || sent[0].input["osd_memory_target"] != float64(1024<<20) ||
		sent[0].input["client_user"] != "cloudland" {
		t.Fatalf("configure input %v", sent[0].input)
	}
	configureRun := sent[0].run
	succeed(sent, func(*stcSent) string {
		return fmt.Sprintf(`{"client_key":%q,"mon_addrs":["10.93.0.1","10.93.0.2","10.93.0.3"]}`, cephTestKey)
	})
	must(t, db.Take(c, cluster.ID).Error)
	if !strings.HasPrefix(c.Secrets, "v1:") || strings.Contains(c.Secrets, cephTestKey) || len(cephInfoOf(c).MonAddrs) != 3 {
		t.Fatalf("after configure %+v", c)
	}
	if key, err := cephClientKey(c); err != nil || key != cephTestKey {
		t.Fatalf("client key %q %v", key, err)
	}
	if r := f.run(configureRun); strings.Contains(r.Result, cephTestKey) || !strings.Contains(r.Result, "mon_addrs") {
		t.Fatalf("the run still holds the client key: %s", r.Result)
	}
	sent = f.expect("resolve", "stc_resolve_disks.sh", h[:3]...)
	succeed(sent, func(s *stcSent) string {
		id := s.input["disks"].([]interface{})[0].(map[string]interface{})["id"]
		return fmt.Sprintf(`{"disks":[{"id":%q,"path":"/dev/sd%d","name":"sdb"}]}`, id, s.hostid)
	})
	sent = f.expect("osds", "ceph_cluster.sh", h[0])
	osds, _ := sent[0].input["osds"].([]interface{})
	if sent[0].input["action"] != "create_osds" || len(osds) != 3 {
		t.Fatalf("create_osds input %v", sent[0].input)
	}
	o1 := osds[1].(map[string]interface{})
	if o1["device"] != fmt.Sprintf("/dev/sd%d", h[1]) || o1["kname"] != "sdb" || o1["hostname"] != fmt.Sprintf("h%d", h[1]) || o1["media"] != "hdd" {
		t.Fatalf("second OSD %v", o1)
	}
	succeed(sent, func(s *stcSent) string {
		out := []string{}
		for i, o := range osds {
			out = append(out, fmt.Sprintf(`{"key":%q,"osd_id":%d}`, o.(map[string]interface{})["key"], i))
		}
		return `{"osds":[` + strings.Join(out, ",") + `]}`
	})
	sent = f.expect("clients", "ceph_client.sh", h...)
	in = sent[0].input
	if in["action"] != "setup" || in["fsid"] != cluster.UUID || in["client_key"] != cephTestKey || in["client_user"] != "cloudland" ||
		in["secret_uuid"] != cluster.UUID || len(in["mon_addrs"].([]interface{})) != 3 {
		t.Fatalf("client input %v", in)
	}
	succeed(sent, none)
	succeed(f.expect("finish", "stc_finish.sh", h...), none)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	must(t, db.Take(c, cluster.ID).Error)
	if c.Status != model.StorageClusterReady || c.ClusterRef != c.UUID || c.ActiveTask != 0 {
		t.Fatalf("deployed cluster %+v", c)
	}
	_, nodes, disks, _ := StorageClusters.Get(ctx, cluster.UUID)
	for _, d := range disks {
		if d.Status != model.StorageDiskActive || !strings.HasPrefix(d.Name, "osd.") || cephOsdID(d) < 0 {
			t.Fatalf("deployed disk %+v", d)
		}
	}
	for _, n := range nodes {
		if n.Status != model.StorageNodeActive || nodeAttr(n, "os") != "ubuntu 24.04" || nodeAttr(n, "hostname") != fmt.Sprintf("h%d", n.Hostid) {
			t.Fatalf("deployed node %+v", n)
		}
	}

	// An RBD pool: replicas on different hosts, the media rule
	_, _, err = storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name + "-p", ClusterUUID: c.UUID, Params: json.RawMessage(`{"inode_limit":1}`)})
	wantCode(t, err, ErrStorageInvalidPlan, "a GPFS parameter")
	_, _, err = storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name + "-p", ClusterUUID: c.UUID, Media: "ssd"})
	wantCode(t, err, ErrStorageInvalidPlan, "no ssd OSDs")
	pool, ptask, err := storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name + "-p", ClusterUUID: c.UUID, Media: "hdd"})
	must(t, err)
	defer func() {
		db.Unscoped().Where("storage_pool_id = ?", pool.ID).Delete(&model.Volume{})
		db.Unscoped().Where("pool_id = ?", pool.ID).Delete(&model.HyperStoragePool{})
		db.Unscoped().Where("id = ?", pool.ID).Delete(&model.StoragePool{})
	}()
	cephPool := "cl_" + pool.UUID[:8]
	p := &model.StoragePool{}
	must(t, db.Take(p, pool.ID).Error)
	if p.Driver != model.StorageDriverCephRBD || p.MountPath != "" || p.Replicas != 3 || p.CloneMode != "clone" ||
		stringParam(poolDriverParams(p), "ceph_pool") != cephPool || stringParam(poolDriverParams(p), "crush_rule") != "cl-hdd" {
		t.Fatalf("pool being made %+v", p)
	}
	if g := (rbdDriver{}).CapacityGroup(p); g != fmt.Sprintf("ceph/%d/cl-hdd", c.ID) {
		t.Fatalf("capacity group %q", g)
	}
	sent = f.expect("create_pool", "ceph_pool.sh", h[0])
	in = sent[0].input
	if in["action"] != "create" || in["ceph_pool"] != cephPool || in["crush_rule"] != "cl-hdd" || in["media"] != "hdd" || in["replicas"] != float64(3) ||
		in["fsid"] != c.UUID || in["quota_bytes"] != float64(0) {
		t.Fatalf("create_pool input %v", in)
	}
	succeed(sent, none)
	sent = f.expect("sync_pools", "stc_pools.sh", h...)
	entry := sent[0].input["pools"].([]interface{})[0].(map[string]interface{})
	if entry["driver"] != "ceph_rbd" || entry["ceph_pool"] != cephPool || entry["conf"] != "/etc/ceph/"+c.UUID+".conf" || entry["user"] != "cloudland" ||
		entry["secret_uuid"] != c.UUID || entry["cluster"] != c.UUID || entry["root"] != nil {
		t.Fatalf("pool entry %v", entry)
	}
	succeed(sent, func(s *stcSent) string { return readyCheck(pool.UUID, 300*gib, 0) })
	f.wantTask(ptask.ID, model.StorageTaskSucceeded, "")
	must(t, db.Take(p, pool.ID).Error)

	// A volume is an RBD image of the pool
	vol, err := volumeAdmin.Create(ctx, name+"-v", 20, p)
	must(t, err)
	sf := &sharedFixture{stcFixture: f}
	cmd := sf.oneCmd("create volume", "create_volume_shared.sh")
	if vol.Path != fmt.Sprintf("volume-%d", vol.ID) || cmd.input["image"] != vol.Path || cmd.input["path"] != nil || cmd.input["driver"] != "ceph_rbd" ||
		cmd.input["ceph_pool"] != cephPool || cmd.input["size_gb"] != float64(20) {
		t.Fatalf("volume %+v command %+v", vol, cmd.input)
	}
	must(t, HandleSharedVolumeCreated(ctx, cmd.hostid, vol.ID, "available", "-"))
	must(t, db.Take(vol, vol.ID).Error)
	deferred, err := volumeAdmin.Delete(ctx, vol)
	must(t, err)
	cmd = sf.oneCmd("delete volume", "delete_volume_shared.sh")
	if !deferred || cmd.input["image"] != vol.Path {
		t.Fatalf("delete %v %+v", deferred, cmd.input)
	}
	must(t, HandleClearVolume(ctx, cmd.hostid, vol.ID, "deleted", ""))
	qtask, err := storagePoolAdmin.UpdateSharedQuota(ctx, p, 50)
	must(t, err)
	sent = f.expect("quota", "ceph_pool.sh", h[0])
	if sent[0].input["action"] != "quota" || sent[0].input["quota_bytes"] != float64(50*gib) {
		t.Fatalf("quota input %v", sent[0].input)
	}
	succeed(sent, none)
	f.wantTask(qtask.ID, model.StorageTaskSucceeded, "")
	must(t, db.Take(p, pool.ID).Error)
	if g := (rbdDriver{}).CapacityGroup(p); g != "" || (rbdDriver{}).DriverArgs(p)["quota_bytes"] != int64(50*gib) {
		t.Fatalf("pool with a quota: group %q args %v", g, (rbdDriver{}).DriverArgs(p))
	}
	dtask, err := storagePoolAdmin.DeleteShared(ctx, p)
	must(t, err)
	succeed(f.expect("sync before deleting", "stc_pools.sh", h...), func(*stcSent) string { return `{"pools":0}` })
	sent = f.expect("delete_pool", "ceph_pool.sh", h[0])
	if sent[0].input["action"] != "delete" || sent[0].input["ceph_pool"] != cephPool {
		t.Fatalf("delete_pool input %v", sent[0].input)
	}
	succeed(sent, none)
	f.wantTask(dtask.ID, model.StorageTaskSucceeded, "")

	// Changes: a disk, then a host gone for good
	must(t, db.Create(&model.HyperDisk{Hostid: h[1], DiskID: "loop:/var/tmp/ceph-a.img", Name: "loop3", SizeBytes: 64 << 30, Media: "ssd",
		State: model.DiskFree, ScannedAt: time.Now()}).Error)
	atask, err := StorageClusters.AddDisks(ctx, c.UUID, &StorageClusterExpand{Disks: []*StorageDiskPlan{{Hostid: h[1], DiskID: "loop:/var/tmp/ceph-a.img"}}})
	must(t, err)
	sent = f.expect("resolve the new disk", "stc_resolve_disks.sh", h[1])
	succeed(sent, func(*stcSent) string {
		return `{"disks":[{"id":"wwn-0x5000ceb1","path":"/dev/sdx","name":"sdb"},{"id":"loop:/var/tmp/ceph-a.img","path":"/dev/clceph-x/osd","name":"loop3"}]}`
	})
	sent = f.expect("the new OSD", "ceph_cluster.sh", h[0])
	osds, _ = sent[0].input["osds"].([]interface{})
	if len(osds) != 1 || osds[0].(map[string]interface{})["device"] != "/dev/clceph-x/osd" || osds[0].(map[string]interface{})["kname"] != "loop3" ||
		osds[0].(map[string]interface{})["media"] != "ssd" {
		t.Fatalf("create_osds of the new disk %v", sent[0].input)
	}
	succeed(sent, func(*stcSent) string {
		return fmt.Sprintf(`{"osds":[{"key":%q,"osd_id":3}]}`, osds[0].(map[string]interface{})["key"])
	})
	// The host holds back the memory of one OSD more (mon 2048 + two OSDs of 1024 + 512), recorded and written on it
	reserveOf := func(hostid int32) int32 {
		n := &model.StorageClusterNode{}
		must(t, db.Where("cluster_id = ? AND hostid = ?", c.ID, hostid).Take(n).Error)
		return n.ReservedMemMB
	}
	sent = f.expect("finish of the new disk", "stc_finish.sh", h[1])
	if sent[0].input["reserve_mb"] != float64(5120) || reserveOf(h[1]) != 5120 {
		t.Fatalf("reservation with the new disk: finish input %v, recorded %d", sent[0].input, reserveOf(h[1]))
	}
	succeed(sent, none)
	f.wantTask(atask.ID, model.StorageTaskSucceeded, "")
	if reserveOf(h[1]) != 5120 || reserveOf(h[0]) != 4608 {
		t.Fatalf("reservations after the new disk: %d %d", reserveOf(h[1]), reserveOf(h[0]))
	}
	_, _, disks, _ = StorageClusters.Get(ctx, cluster.UUID)
	var added *model.StorageClusterDisk
	for _, d := range disks {
		if d.DiskID == "loop:/var/tmp/ceph-a.img" {
			added = d
		}
	}
	if added == nil || added.Status != model.StorageDiskActive || added.Name != "osd.3" {
		t.Fatalf("added disk %+v", added)
	}
	rtask, err := StorageClusters.RemoveDisk(ctx, c.UUID, added.UUID)
	must(t, err)
	sent = f.expect("remove the OSD", "ceph_cluster.sh", h[0])
	if sent[0].input["action"] != "remove_osds" || len(sent[0].input["osd_ids"].([]interface{})) != 1 || sent[0].input["osd_ids"].([]interface{})[0] != float64(3) {
		t.Fatalf("remove_osds input %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("release", "stc_release_disks.sh", h[1])
	if len(sent[0].input["keep"].([]interface{})) != 1 || len(sent[0].input["release"].([]interface{})) != 1 {
		t.Fatalf("release input %v", sent[0].input)
	}
	succeed(sent, none)
	// One OSD less on the host again
	sent = f.expect("finish of the removal", "stc_finish.sh", h[1])
	if sent[0].input["reserve_mb"] != float64(3584) {
		t.Fatalf("finish input after the removal %v", sent[0].input)
	}
	succeed(sent, none)
	f.wantTask(rtask.ID, model.StorageTaskSucceeded, "")
	if reserveOf(h[1]) != 3584 {
		t.Fatalf("reservation after the removal: %d", reserveOf(h[1]))
	}

	// The disk of osd.1 fails: a new disk of its host takes the id; the failed disk is not wiped
	_, _, disks, _ = StorageClusters.Get(ctx, cluster.UUID)
	var failed *model.StorageClusterDisk
	for _, d := range disks {
		if d.Hostid == h[1] {
			failed = d
		}
	}
	oldID := cephOsdID(failed)
	must(t, db.Model(&model.StorageClusterDisk{}).Where("id = ?", failed.ID).Updates(map[string]interface{}{"state": "down", "checked_at": time.Now()}).Error)
	must(t, db.Create(&model.HyperDisk{Hostid: h[1], DiskID: "wwn-0x5000cebn", Name: "sdc", Serial: "CSNN", SizeBytes: 2 << 40, Media: "hdd",
		State: model.DiskFree, ScannedAt: time.Now()}).Error)
	xtask, err := StorageClusters.ReplaceDisk(ctx, c.UUID, failed.UUID, &StorageDiskPlan{DiskID: "wwn-0x5000cebn"})
	must(t, err)
	sent = f.expect("resolve the new disk", "stc_resolve_disks.sh", h[1])
	if ids := sent[0].input["disks"].([]interface{}); len(ids) != 1 {
		t.Fatalf("resolve input %v", sent[0].input)
	}
	succeed(sent, func(*stcSent) string { return `{"disks":[{"id":"wwn-0x5000cebn","path":"/dev/sdc","name":"sdc"}]}` })
	sent = f.expect("replace the OSD", "ceph_cluster.sh", h[0])
	osds, _ = sent[0].input["osds"].([]interface{})
	if sent[0].input["action"] != "replace_osd" || sent[0].input["old_id"] != float64(oldID) || len(osds) != 1 ||
		osds[0].(map[string]interface{})["device"] != "/dev/sdc" {
		t.Fatalf("replace_osd input %v", sent[0].input)
	}
	succeed(sent, func(*stcSent) string {
		return fmt.Sprintf(`{"osds":[{"key":%q,"osd_id":%d}]}`, osds[0].(map[string]interface{})["key"], oldID)
	})
	sent = f.expect("release the failed disk", "stc_release_disks.sh", h[1])
	if sent[0].input["no_wipe"] != true {
		t.Fatalf("release input %v", sent[0].input)
	}
	succeed(sent, none)
	f.wantTask(xtask.ID, model.StorageTaskSucceeded, "")
	nd := &model.StorageClusterDisk{}
	must(t, db.Where("cluster_id = ? AND disk_id = ?", c.ID, "wwn-0x5000cebn").Take(nd).Error)
	if nd.Status != model.StorageDiskActive || nd.Name != fmt.Sprintf("osd.%d", oldID) || cephOsdID(nd) != oldID {
		t.Fatalf("new disk %+v", nd)
	}
	// A disk for a disk: as many OSDs as before
	if reserveOf(h[1]) != 3584 {
		t.Fatalf("reservation after the replacement: %d", reserveOf(h[1]))
	}

	// The mgr moves to h[1]: an even number of mons is refused; the labels follow the roles
	_, err = StorageClusters.ChangeRoles(ctx, c.UUID, h[1], []string{"osd"})
	wantCode(t, err, ErrStorageInvalidPlan, "even number of mons")
	gtask, err := StorageClusters.ChangeRoles(ctx, c.UUID, h[1], []string{"mon", "mgr"})
	must(t, err)
	succeed(f.expect("trust", "stc_ssh_trust.sh", h...), none)
	sent = f.expect("labels", "ceph_cluster.sh", h[0])
	if sent[0].input["action"] != "set_labels" || sent[0].input["hostname"] != fmt.Sprintf("h%d", h[1]) || sent[0].input["mons"] != float64(3) ||
		fmt.Sprint(sent[0].input["labels"]) != "[mon mgr osd]" {
		t.Fatalf("set_labels input %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("finish", "stc_finish.sh", h[1])
	if sent[0].input["reserve_mb"] != float64(4608) {
		t.Fatalf("finish input %v", sent[0].input)
	}
	succeed(sent, none)
	f.wantTask(gtask.ID, model.StorageTaskSucceeded, "")

	f.setHostStatus(h[3], 10)
	ntask, err := StorageClusters.RemoveNode(ctx, c.UUID, &StorageNodeRemove{Hostid: h[3], Offline: true, Confirm: "stc-h4"})
	must(t, err)
	sent = f.expect("remove the client", "ceph_cluster.sh", h[0])
	if sent[0].input["action"] != "remove_host" || sent[0].input["orch_host"] != false || sent[0].input["offline"] != true ||
		sent[0].input["hostname"] != fmt.Sprintf("h%d", h[3]) {
		t.Fatalf("remove_host input %v", sent[0].input)
	}
	succeed(sent, none)
	f.wantTask(ntask.ID, model.StorageTaskSucceeded, "")
	f.setHostStatus(h[3], 1)

	// Deletion: the orchestrator stops, the hosts remove the daemons and wipe the disks
	del, err := StorageClusters.Delete(ctx, c.UUID, &StorageClusterDelete{ConfirmName: name})
	must(t, err)
	sent = f.expect("teardown", "ceph_cluster.sh", h[0])
	if sent[0].input["action"] != "teardown" || sent[0].input["fsid"] != c.UUID {
		t.Fatalf("teardown input %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("leave", "stc_leave.sh", h[:3]...)
	for _, s := range sent {
		if s.input["kind"] != "ceph" || len(s.input["wipe"].([]interface{})) != 1 {
			t.Fatalf("leave input %v", s.input)
		}
	}
	succeed(sent, none)
	f.wantTask(del.ID, model.StorageTaskSucceeded, "")
	if db.Take(&model.StorageCluster{}, c.ID).Error == nil {
		t.Fatalf("the cluster is still there")
	}
}

func TestStorageCephImportPG(t *testing.T) {
	f := newStcFixture(t)
	ctx, db := f.ctx, f.db
	viper.Set("vpn.secret_key", "storage-ceph-pg-test")
	if err := SecretStoreReady(); err != nil {
		t.Skipf("the credential store was set up without a key earlier in this test binary: %v", err)
	}
	h := stcHosts[:2]
	fsid := "5b0e8a4e-1a2b-4c3d-8e9f-0a1b2c3d4e5f"
	name := fmt.Sprintf("ceph-ext-%d", time.Now().UnixNano()%1000000)
	params := func(mons, user, key string) []byte {
		return []byte(fmt.Sprintf(`{"fsid":%q,"mon_addrs":%s,"client_user":%q,"client_key":%q}`, fsid, mons, user, key))
	}
	_, _, err := StorageClusters.Import(ctx, &StorageClusterImport{Kind: "ceph", Name: name, Hostids: h, Params: params(`[]`, "rbd", cephTestKey)})
	wantCode(t, err, ErrStorageInvalidPlan, "no mon address")
	_, _, err = StorageClusters.Import(ctx, &StorageClusterImport{Kind: "ceph", Name: name, Hostids: h, Params: params(`["10.0.0.1"]`, "client.rbd", cephTestKey)})
	wantCode(t, err, ErrStorageInvalidPlan, "user with the client. prefix")
	_, _, err = StorageClusters.Import(ctx, &StorageClusterImport{Kind: "ceph", Name: name, Hostids: h, Params: params(`["10.0.0.1"]`, "rbd", "short")})
	wantCode(t, err, ErrStorageInvalidPlan, "bad key")
	cluster, task, err := StorageClusters.Import(ctx, &StorageClusterImport{Kind: "ceph", Name: name, Hostids: h,
		Params: params(`["10.0.0.1","10.0.0.2:3300"]`, "rbd", cephTestKey)})
	must(t, err)
	defer func() {
		db.Unscoped().Where("cluster_id = ?", cluster.ID).Delete(&model.StorageClusterNode{})
		db.Unscoped().Where("id = ?", cluster.ID).Delete(&model.StorageCluster{})
	}()
	c := &model.StorageCluster{}
	must(t, db.Take(c, cluster.ID).Error)
	if strings.Contains(c.Params, cephTestKey) || strings.Contains(c.Params, "client_key") || !strings.HasPrefix(c.Secrets, "v1:") {
		t.Fatalf("the key is kept in clear: params %s secrets %s", c.Params, c.Secrets)
	}
	sent := f.expect("check", "ceph_client.sh", h...)
	// The import holds no slot: while its check runs, deleting (forgetting) the cluster is refused
	_, err = StorageClusters.Delete(ctx, c.UUID, &StorageClusterDelete{ConfirmName: name})
	wantCode(t, err, ErrStorageClusterBusy, "delete while the import runs")
	in := sent[0].input
	mons, _ := in["mon_addrs"].([]interface{})
	if in["action"] != "import" || in["fsid"] != fsid || in["client_user"] != "rbd" || in["client_key"] != cephTestKey || in["secret_uuid"] != c.UUID ||
		len(mons) != 2 || mons[0] != "10.0.0.1:6789" || mons[1] != "10.0.0.2:3300" {
		t.Fatalf("check input %v", in)
	}
	for _, s := range sent {
		must(t, f.report(s, model.StorageRunSucceeded, "", `{"health":"HEALTH_OK","version":"19.2.3"}`))
	}
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	must(t, db.Take(c, cluster.ID).Error)
	if c.Status != model.StorageClusterReady || c.ClusterRef != fsid {
		t.Fatalf("imported cluster %+v", c)
	}
	_, err = StorageClusters.AddDisks(ctx, c.UUID, &StorageClusterExpand{Disks: []*StorageDiskPlan{{Hostid: h[0], DiskID: "x"}}})
	wantCode(t, err, ErrStorageInvalidPlan, "changing an imported cluster")

	// An existing RBD pool is registered, once
	_, _, err = storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name + "-p", ClusterUUID: c.UUID, Params: []byte(`{}`)})
	wantCode(t, err, ErrStorageInvalidPlan, "no RBD pool named")
	pool, ptask, err := storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name + "-p", ClusterUUID: c.UUID, Params: []byte(`{"ceph_pool":"vms"}`)})
	must(t, err)
	defer db.Unscoped().Where("pool_id = ?", pool.ID).Delete(&model.HyperStoragePool{})
	defer db.Unscoped().Where("id = ?", pool.ID).Delete(&model.StoragePool{})
	_, _, err = storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name + "-q", ClusterUUID: c.UUID, Params: []byte(`{"ceph_pool":"vms2"}`)})
	wantCode(t, err, ErrStorageClusterBusy, "pool slot held")
	sent = f.expect("register", "ceph_pool.sh", h[0])
	in = sent[0].input
	if in["action"] != "register" || in["ceph_pool"] != "vms" || in["conf"] != "/etc/ceph/"+c.UUID+".conf" || in["user"] != "rbd" {
		t.Fatalf("register input %v", in)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", "{}"))
	for _, s := range f.expect("sync", "stc_pools.sh", h...) {
		must(t, f.report(s, model.StorageRunSucceeded, "", readyCheck(pool.UUID, 1000*gib, 10*gib)))
	}
	f.wantTask(ptask.ID, model.StorageTaskSucceeded, "")
	p := &model.StoragePool{}
	must(t, db.Take(p, pool.ID).Error)
	if p.Status != model.StoragePoolActive || p.CloneMode != "copy" || (rbdDriver{}).CapacityGroup(p) != "" || !strings.Contains(p.DriverParams, `"external":true`) {
		t.Fatalf("registered pool %+v", p)
	}
	_, _, err = storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name + "-q", ClusterUUID: c.UUID, Params: []byte(`{"ceph_pool":"vms"}`)})
	wantCode(t, err, ErrStorageInvalidPlan, "the RBD pool is registered already")
	// _ is a LIKE wildcard: another RBD pool whose name matches vms through it is not taken for vms
	if err := (cephBackend{}).PoolSetup(db, c, &model.StoragePool{}, []byte(`{"ceph_pool":"v_s"}`)); err != nil {
		t.Fatalf("RBD pool v_s refused as taken: %v", err)
	}
	dtask, err := storagePoolAdmin.DeleteShared(ctx, p)
	must(t, err)
	for _, s := range f.expect("sync before unregistering", "stc_pools.sh", h...) {
		must(t, f.report(s, model.StorageRunSucceeded, "", `{"pools":0}`))
	}
	sent = f.expect("unregister", "ceph_pool.sh", h[0])
	if sent[0].input["action"] != "unregister" {
		t.Fatalf("unregister input %v", sent[0].input)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", "{}"))
	f.wantTask(dtask.ID, model.StorageTaskSucceeded, "")

	// Forgetting it removes the client part from the hosts, nothing else
	del, err := StorageClusters.Delete(ctx, c.UUID, &StorageClusterDelete{ConfirmName: name})
	must(t, err)
	sent = f.expect("forget", "stc_forget.sh", h...)
	if sent[0].input["kind"] != "ceph" {
		t.Fatalf("forget input %v", sent[0].input)
	}
	for _, s := range sent {
		must(t, f.report(s, model.StorageRunSucceeded, "", "{}"))
	}
	f.wantTask(del.ID, model.StorageTaskSucceeded, "")
}

// A deployment aborted half way leaves its hosts joining: deleting the cluster still runs the teardown on its admin
// host and the leave on every host
func TestStorageCephAbortedDeletePG(t *testing.T) {
	f := newStcFixture(t)
	ctx, db := f.ctx, f.db
	viper.Set("vpn.secret_key", "storage-ceph-pg-test")
	if err := SecretStoreReady(); err != nil {
		t.Skipf("the credential store was set up without a key earlier in this test binary: %v", err)
	}
	h := stcHosts[:3]
	for i, host := range h {
		must(t, db.Create(&model.HyperDisk{Hostid: host, DiskID: fmt.Sprintf("wwn-0x5000cec%d", i), Name: "sdb", Serial: fmt.Sprintf("CAB%d", i),
			SizeBytes: 2 << 40, Media: "hdd", State: model.DiskFree, ScannedAt: time.Now()}).Error)
	}
	name := fmt.Sprintf("ceph-abort-%d", time.Now().UnixNano()%1000000)
	cluster, task, err := StorageClusters.Create(ctx, &StorageClusterCreate{Name: name, Plan: &StoragePlan{Kind: "ceph", Params: json.RawMessage(`{}`),
		Nodes: []*StorageNodePlan{{Hostid: h[0], Roles: []string{"admin", "mon", "mgr", "osd"}}, {Hostid: h[1], Roles: []string{"mon", "osd"}},
			{Hostid: h[2], Roles: []string{"mon", "osd"}}},
		Disks: []*StorageDiskPlan{{Hostid: h[0], DiskID: "wwn-0x5000cec0"}, {Hostid: h[1], DiskID: "wwn-0x5000cec1"}, {Hostid: h[2], DiskID: "wwn-0x5000cec2"}}}})
	must(t, err)
	defer func() {
		db.Unscoped().Where("cluster_id = ?", cluster.ID).Delete(&model.StorageClusterDisk{})
		db.Unscoped().Where("cluster_id = ?", cluster.ID).Delete(&model.StorageClusterNode{})
		db.Unscoped().Where("id = ?", cluster.ID).Delete(&model.StorageCluster{})
	}()
	for _, s := range f.expect("precheck", "stc_precheck.sh", h...) {
		must(t, f.report(s, model.StorageRunFailed, "bootstrap failed", ""))
	}
	f.wantTask(task.ID, model.StorageTaskFailed, "")
	must(t, AbortStorageTask(ctx, task.ID))
	f.wantTask(task.ID, model.StorageTaskAborted, "")
	del, err := StorageClusters.Delete(ctx, cluster.UUID, &StorageClusterDelete{ConfirmName: name})
	must(t, err)
	sent := f.expect("teardown", "ceph_cluster.sh", h[0])
	if sent[0].input["action"] != "teardown" {
		t.Fatalf("teardown input %v", sent[0].input)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", "{}"))
	for _, s := range f.expect("leave", "stc_leave.sh", h...) {
		must(t, f.report(s, model.StorageRunSucceeded, "", "{}"))
	}
	f.wantTask(del.ID, model.StorageTaskSucceeded, "")
	if db.Take(&model.StorageCluster{}, cluster.ID).Error == nil {
		t.Fatalf("the cluster is still there")
	}
}
