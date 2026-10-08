/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// A GPFS cluster in the shared disk layout against PostgreSQL with the fake cland standing in for the hosts
// (shared-storage-design.md §7.10): a LUN claimed under every host that serves it becomes one NSD with them as its
// servers, wiped once; a host joins serving a LUN there and a new one, leaves again; a LUN leaves as a whole; the
// deletion wipes every LUN once. The node scripts only ran against stubs (WSL stc-test16.sh).

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/viper"
)

func TestStorageGPFSSANPG(t *testing.T) {
	f := newStcFixture(t)
	ctx := f.ctx
	viper.Set("vpn.secret_key", "storage-gpfs-pg-test")
	if err := SecretStoreReady(); err != nil {
		t.Skipf("the credential store was set up without a key earlier in this test binary: %v", err)
	}
	client, err := minio.New("127.0.0.1:9", &minio.Options{Creds: credentials.NewStaticV4("k", "s", ""), Region: "us-east-1"})
	must(t, err)
	oldClient, oldBucket := s3Client.Load(), s3Bucket
	s3Client.Store(client)
	s3Bucket = "images"
	defer func() { s3Client.Store(oldClient); s3Bucket = oldBucket }()

	h := stcHosts
	// LUN a seen by the first three hosts, b by the first two, c by the fourth only; a free local disk on the first
	lun := func(id string, devs map[int32]string) {
		for host, dev := range devs {
			must(t, f.db.Create(&model.HyperDisk{Hostid: host, DiskID: id, Name: dev, Serial: "", SizeBytes: 4 << 40, Media: "hdd",
				State: model.DiskShared, Detail: "may be a shared LUN (multipath)", ScannedAt: time.Now()}).Error)
		}
	}
	lun("wwn-0x6000aaa1", map[int32]string{h[0]: "mpatha", h[1]: "mpathb", h[2]: "mpatha"})
	lun("wwn-0x6000aaa2", map[int32]string{h[0]: "mpathc", h[1]: "mpathc"})
	lun("wwn-0x6000aaa3", map[int32]string{h[3]: "mpathz"})
	must(t, f.db.Create(&model.HyperDisk{Hostid: h[0], DiskID: "wwn-0x5000local", Name: "sdb", SizeBytes: 2 << 40, Media: "hdd",
		State: model.DiskFree, ScannedAt: time.Now()}).Error)
	defer f.db.Unscoped().Where("disk_id LIKE ? OR disk_id = ?", "wwn-0x6000aaa%", "wwn-0x5000local").Delete(&model.HyperDisk{})
	manifest, _ := json.Marshal(map[string]string{"gpfs.base_6.0.0-2_amd64.deb": "m1", "gpfs.gpl_6.0.0-2_all.deb": "m2",
		"gpfs.gskit_8.0.55-19.1_amd64.deb": "m3", "gpfs.msg.en-us_6.0.0-2_all.deb": "m4", "gpfs.license.dm_6.0.0-2_amd64.deb": "m5",
		"gpfs.docs_6.0.0-2_all.deb": "m6"})
	pkg := &model.StoragePackage{Kind: "gpfs", Version: "6.0.0.2", Edition: "data_management", FileName: "installer", SizeBytes: 1000,
		SHA256: strings.Repeat("c", 64), ObjectKey: "storage-packages/s/installer", PayloadLine: 680, Distros: `["ubuntu24"]`,
		Manifest: string(manifest), Status: model.StoragePackageReady, AcceptedBy: "admin"}
	must(t, f.db.Create(pkg).Error)
	defer f.db.Unscoped().Delete(pkg)

	name := fmt.Sprintf("san-pg-%d", time.Now().UnixNano()%1000000)
	plan := func(disks ...*StorageDiskPlan) *StorageClusterCreate {
		return &StorageClusterCreate{Name: name, PackageUUID: pkg.UUID, Plan: &StoragePlan{Kind: "gpfs",
			Nodes: []*StorageNodePlan{{Hostid: h[0], Roles: []string{"admin", "quorum", "nsd"}}, {Hostid: h[1], Roles: []string{"quorum", "nsd"}},
				{Hostid: h[2], Roles: []string{"quorum", "nsd"}}},
			Disks: disks, Params: json.RawMessage(`{"layout":"san"}`)}}
	}
	sanDisks := []*StorageDiskPlan{{Hostid: h[0], DiskID: "wwn-0x6000aaa1"}, {Hostid: h[1], DiskID: "wwn-0x6000aaa1"},
		{Hostid: h[2], DiskID: "wwn-0x6000aaa1"}, {Hostid: h[0], DiskID: "wwn-0x6000aaa2"}, {Hostid: h[1], DiskID: "wwn-0x6000aaa2"}}
	_, _, err = StorageClusters.Create(ctx, plan(append(sanDisks, &StorageDiskPlan{Hostid: h[0], DiskID: "wwn-0x5000local"})...))
	wantCode(t, err, ErrStorageDiskNotAllowed, "a local disk in the shared disk layout")
	// A LUN another cluster claimed on any host is taken
	other := &model.StorageClusterDisk{ClusterID: 999999, Hostid: h[3], DiskID: "wwn-0x6000aaa2", Status: model.StorageDiskActive}
	must(t, f.db.Create(other).Error)
	_, _, err = StorageClusters.Create(ctx, plan(sanDisks...))
	wantCode(t, err, ErrStorageDiskNotAllowed, "a LUN of another cluster")
	must(t, f.db.Unscoped().Delete(other).Error)

	cluster, task, err := StorageClusters.Create(ctx, plan(sanDisks...))
	must(t, err)
	defer f.db.Unscoped().Where("id = ?", cluster.ID).Delete(&model.StorageCluster{})
	if cluster.Layout != model.StorageLayoutSAN {
		t.Fatalf("cluster %+v", cluster)
	}
	succeed := func(sent []*stcSent, result func(s *stcSent) string) {
		for _, s := range sent {
			must(t, f.report(s, model.StorageRunSucceeded, "", result(s)))
		}
	}
	none := func(*stcSent) string { return "{}" }
	resolveByScan := func(s *stcSent) string {
		list := []string{}
		for _, d := range s.input["disks"].([]interface{}) {
			id := d.(map[string]interface{})["id"].(string)
			hd := &model.HyperDisk{}
			must(t, f.db.Where("hostid = ? AND disk_id = ?", s.hostid, id).Take(hd).Error)
			list = append(list, fmt.Sprintf(`{"id":%q,"path":"/dev/mapper/%s","name":"%s"}`, id, hd.Name, hd.Name))
		}
		return `{"disks":[` + strings.Join(list, ",") + `]}`
	}
	succeed(f.expect("precheck", "stc_precheck.sh", h[:3]...), func(s *stcSent) string {
		return fmt.Sprintf(`{"items":[],"facts":{"hostname":"h%d","os":"ubuntu 24.04","host_key":"ssh-ed25519 KEY%d"}}`, s.hostid, s.hostid)
	})
	succeed(f.expect("join", "stc_join.sh", h[:3]...), none)
	succeed(f.expect("fetch", "stc_fetch.sh", h[:3]...), none)
	succeed(f.expect("install", "gpfs_install.sh", h[:3]...), none)
	succeed(f.expect("build", "gpfs_build_gpl.sh", h[:3]...), none)
	succeed(f.expect("trust", "stc_ssh_trust.sh", h[:3]...), none)
	succeed(f.expect("create cluster", "gpfs_cluster.sh", h[0]), func(*stcSent) string {
		return fmt.Sprintf(`{"cluster_name":"%s.cloudland","cluster_id":"9999"}`, name)
	})
	succeed(f.expect("start", "gpfs_cluster.sh", h[0]), none)
	sent := f.expect("resolve", "stc_resolve_disks.sh", h[:3]...)
	for _, s := range sent {
		for _, d := range s.input["disks"].([]interface{}) {
			m := d.(map[string]interface{})
			// The lowest host of a LUN checks it; the others identity only
			if (s.hostid == h[0]) != (m["in_use"] != true) {
				t.Fatalf("resolve on %d: %v", s.hostid, m)
			}
		}
	}
	succeed(sent, resolveByScan)
	sent = f.expect("create nsd", "gpfs_nsd.sh", h[0])
	nsds := sent[0].input["nsds"].([]interface{})
	s1 := fmt.Sprintf("cl%ds1", cluster.ID)
	if len(nsds) != 2 {
		t.Fatalf("one NSD per LUN: %v", nsds)
	}
	first, second := nsds[0].(map[string]interface{}), nsds[1].(map[string]interface{})
	// LUN a (index 1) rotates to its second host first; LUN b (index 2) has two hosts, rotated back to the first
	if first["name"] != s1 || first["server"] != "10.93.0.2,10.93.0.3,10.93.0.1" || first["device"] != "/dev/mapper/mpathb" ||
		first["failure_group"] != float64(1) || second["server"] != "10.93.0.1,10.93.0.2" || second["device"] != "/dev/mapper/mpathc" {
		t.Fatalf("NSD stanzas %v", nsds)
	}
	succeed(sent, none)
	sent = f.expect("create fs", "gpfs_fs.sh", h[0])
	fsNSDs := sent[0].input["nsds"].([]interface{})
	if len(fsNSDs) != 2 || sent[0].input["data_replicas"] != float64(1) || sent[0].input["meta_replicas"] != float64(1) ||
		fsNSDs[0].(map[string]interface{})["failure_group"] != float64(1) {
		t.Fatalf("file system input: one stanza per LUN, one copy %v", sent[0].input)
	}
	succeed(sent, func(*stcSent) string { return `{"capacity_bytes":9000,"free_bytes":8000}` })
	succeed(f.expect("finish", "stc_finish.sh", h[:3]...), none)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	_, _, disks, _ := StorageClusters.Get(ctx, cluster.UUID)
	for _, d := range disks {
		want := s1
		if d.DiskID == "wwn-0x6000aaa2" {
			want = fmt.Sprintf("cl%ds2", cluster.ID)
		}
		if d.Name != want || d.Status != model.StorageDiskActive {
			t.Fatalf("disk %+v", d)
		}
	}

	// ---- a host joins serving LUN a and its own LUN c ----
	// A LUN joining an existing NSD needs the host to see it: its scan lists the LUN too
	must(t, f.db.Create(&model.HyperDisk{Hostid: h[3], DiskID: "wwn-0x6000aaa1", Name: "mpathq", SizeBytes: 4 << 40, Media: "hdd",
		State: model.DiskShared, ScannedAt: time.Now()}).Error)
	// Wiping a LUN the cluster uses is refused: it holds the file system's data (review 2026-10-07)
	_, err = StorageClusters.AddNodes(ctx, cluster.UUID, &StorageClusterExpand{Nodes: []*StorageNodePlan{{Hostid: h[3], Roles: []string{"nsd"}}},
		Disks: []*StorageDiskPlan{{Hostid: h[3], DiskID: "wwn-0x6000aaa1", Wipe: true}, {Hostid: h[3], DiskID: "wwn-0x6000aaa3"}}})
	wantCodeMsg(t, err, ErrStorageDiskNotAllowed, "holds data of this cluster already")
	add, err := StorageClusters.AddNodes(ctx, cluster.UUID, &StorageClusterExpand{Nodes: []*StorageNodePlan{{Hostid: h[3], Roles: []string{"nsd"}}},
		Disks: []*StorageDiskPlan{{Hostid: h[3], DiskID: "wwn-0x6000aaa1"}, {Hostid: h[3], DiskID: "wwn-0x6000aaa3"}}})
	must(t, err)
	for _, step := range []struct{ what, script string }{{"precheck", "stc_precheck.sh"}, {"join", "stc_join.sh"}, {"fetch", "stc_fetch.sh"},
		{"install", "gpfs_install.sh"}, {"build", "gpfs_build_gpl.sh"}} {
		succeed(f.expect("add "+step.what, step.script, h[3]), func(s *stcSent) string {
			if step.what == "precheck" {
				return fmt.Sprintf(`{"items":[],"facts":{"hostname":"h%d","os":"ubuntu 24.04","host_key":"ssh-ed25519 KEY%d"}}`, s.hostid, s.hostid)
			}
			return "{}"
		})
	}
	succeed(f.expect("add trust", "stc_ssh_trust.sh", h...), none)
	succeed(f.expect("add node", "gpfs_cluster.sh", h[0]), none)
	sent = f.expect("add resolve", "stc_resolve_disks.sh", h[3])
	for _, d := range sent[0].input["disks"].([]interface{}) {
		m := d.(map[string]interface{})
		// LUN a is in use: identity only, never wiped; LUN c is new and only this host serves it: checked here
		if (m["id"] == "wwn-0x6000aaa1") != (m["in_use"] == true) || m["wipe"] == true {
			t.Fatalf("add resolve: %v", m)
		}
	}
	succeed(sent, resolveByScan)
	sent = f.expect("add nsd", "gpfs_nsd.sh", h[0])
	nsds = sent[0].input["nsds"].([]interface{})
	if len(nsds) != 1 || nsds[0].(map[string]interface{})["name"] != fmt.Sprintf("cl%ds3", cluster.ID) || nsds[0].(map[string]interface{})["server"] != "10.93.0.4" {
		t.Fatalf("only the new LUN becomes an NSD: %v", nsds)
	}
	succeed(sent, none)
	sent = f.expect("san servers", "gpfs_nsd.sh", h[0])
	srv := sent[0].input["nsds"].([]interface{})
	if sent[0].input["action"] != "servers" || len(srv) != 1 || srv[0].(map[string]interface{})["name"] != s1 ||
		srv[0].(map[string]interface{})["servers"] != "10.93.0.2,10.93.0.3,10.93.0.4,10.93.0.1" {
		t.Fatalf("LUN a gains the new host as a server: %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("add disks", "gpfs_fs.sh", h[0])
	if fs := sent[0].input["nsds"].([]interface{}); len(fs) != 1 || fs[0].(map[string]interface{})["name"] != fmt.Sprintf("cl%ds3", cluster.ID) {
		t.Fatalf("only the new LUN goes into the file system: %v", sent[0].input)
	}
	succeed(sent, none)
	succeed(f.expect("add finish", "stc_finish.sh", h[3]), none)
	f.wantTask(add.ID, model.StorageTaskSucceeded, "")

	// ---- the host leaves again: LUN a keeps its other servers, LUN c leaves with its data moved ----
	rm, err := StorageClusters.RemoveNode(ctx, cluster.UUID, &StorageNodeRemove{Hostid: h[3]})
	must(t, err)
	sent = f.expect("drop server", "gpfs_nsd.sh", h[0])
	srv = sent[0].input["nsds"].([]interface{})
	if len(srv) != 1 || srv[0].(map[string]interface{})["servers"] != "10.93.0.2,10.93.0.3,10.93.0.1" {
		t.Fatalf("LUN a loses the host leaving: %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("remove exclusive", "gpfs_fs.sh", h[0])
	if removedNSDs(sent[0].input) != fmt.Sprintf("[cl%ds3]", cluster.ID) {
		t.Fatalf("only the LUN the host served alone leaves: %v", sent[0].input)
	}
	succeed(sent, none)
	succeed(f.expect("remove node", "gpfs_cluster.sh", h[0]), none)
	sent = f.expect("leave", "stc_leave.sh", h[3])
	for _, w := range sent[0].input["wipe"].([]interface{}) {
		m := w.(map[string]interface{})
		if (m["id"] == "wwn-0x6000aaa1") != (m["shared"] == true) {
			t.Fatalf("leave: LUN a stays in use (not wiped), LUN c is wiped: %v", sent[0].input)
		}
	}
	succeed(sent, none)
	f.wantTask(rm.ID, model.StorageTaskSucceeded, "")

	// ---- LUN b leaves as a whole: one NSD out, both records released, wiped once ----
	var b *model.StorageClusterDisk
	_, _, disks, _ = StorageClusters.Get(ctx, cluster.UUID)
	for _, d := range disks {
		if d.DiskID == "wwn-0x6000aaa2" && d.Hostid == h[1] {
			b = d
		}
	}
	rmd, err := StorageClusters.RemoveDisk(ctx, cluster.UUID, b.UUID)
	must(t, err)
	sent = f.expect("remove lun", "gpfs_fs.sh", h[0])
	if removedNSDs(sent[0].input) != fmt.Sprintf("[cl%ds2]", cluster.ID) {
		t.Fatalf("one NSD for the LUN: %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("release lun", "stc_release_disks.sh", h[0], h[1])
	for _, s := range sent {
		rel := s.input["release"].([]interface{})[0].(map[string]interface{})
		if (s.hostid == h[1]) != (rel["shared"] == true) {
			t.Fatalf("release on %d: the lowest host wipes %v", s.hostid, s.input)
		}
	}
	succeed(sent, none)
	f.wantTask(rmd.ID, model.StorageTaskSucceeded, "")
	var bLeft int64
	f.db.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND disk_id = ?", cluster.ID, "wwn-0x6000aaa2").Count(&bLeft)
	if bLeft != 0 {
		t.Fatal("both records of LUN b go")
	}

	// ---- the deletion: LUN a wiped once ----
	del, err := StorageClusters.Delete(ctx, cluster.UUID, &StorageClusterDelete{ConfirmName: name})
	must(t, err)
	succeed(f.expect("teardown", "gpfs_cluster.sh", h[0]), none)
	sent = f.expect("delete leave", "stc_leave.sh", h[:3]...)
	for _, s := range sent {
		for _, w := range s.input["wipe"].([]interface{}) {
			if m := w.(map[string]interface{}); (s.hostid == h[0]) != (m["shared"] != true) {
				t.Fatalf("delete leave on %d: %v", s.hostid, s.input)
			}
		}
	}
	succeed(sent, none)
	f.wantTask(del.ID, model.StorageTaskSucceeded, "")
}
