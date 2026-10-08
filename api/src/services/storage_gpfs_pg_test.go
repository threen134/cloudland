/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// A GPFS deployment and deletion against PostgreSQL with the fake cland standing in for three hosts
// (shared-storage-design.md §7.2, §7.6): what every step sends, what the cluster records at the end, and what the
// deletion gives back. The hosts' scripts are covered on real hosts; this checks the clapi side.

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
	"gorm.io/gorm"
)

func TestStorageGPFSDeployPG(t *testing.T) {
	f := newStcFixture(t)
	ctx := f.ctx
	viper.Set("vpn.secret_key", "storage-gpfs-pg-test")
	if err := SecretStoreReady(); err != nil {
		t.Skipf("the credential store was set up without a key earlier in this test binary: %v", err)
	}
	// Presigning needs no S3 server, only a client with a region
	client, err := minio.New("127.0.0.1:9", &minio.Options{Creds: credentials.NewStaticV4("k", "s", ""), Region: "us-east-1"})
	must(t, err)
	oldClient, oldBucket := s3Client.Load(), s3Bucket
	s3Client.Store(client)
	s3Bucket = "images"
	defer func() { s3Client.Store(oldClient); s3Bucket = oldBucket }()

	h := stcHosts[:3]
	for i, host := range h {
		must(t, f.db.Create(&model.HyperDisk{Hostid: host, DiskID: fmt.Sprintf("wwn-0x5000ccc%d", i), Name: "sdb", Serial: fmt.Sprintf("SN%d", i),
			SizeBytes: 2 << 40, Media: "hdd", State: model.DiskFree, ScannedAt: time.Now()}).Error)
	}
	manifest, _ := json.Marshal(map[string]string{"gpfs.base_6.0.0-2_amd64.deb": "m1", "gpfs.gpl_6.0.0-2_all.deb": "m2",
		"gpfs.gskit_8.0.55-19.1_amd64.deb": "m3", "gpfs.msg.en-us_6.0.0-2_all.deb": "m4", "gpfs.license.ec_6.0.0-2_amd64.deb": "m5",
		"gpfs.docs_6.0.0-2_all.deb": "m6", "gpfs.gui_6.0.0-2_all.deb": "m7", "gpfs.librdkafka_6.0.0-2.U24.04_amd64.deb": "m8"})
	pkg := &model.StoragePackage{Kind: "gpfs", Version: "6.0.0.2", Edition: "erasure_code", FileName: "installer", SizeBytes: 1000,
		SHA256: strings.Repeat("a", 64), ObjectKey: "storage-packages/x/installer", PayloadLine: 680, Distros: `["ubuntu22","ubuntu24"]`,
		Manifest: string(manifest), Status: model.StoragePackageReady}
	must(t, f.db.Create(pkg).Error)
	defer f.db.Unscoped().Delete(pkg)

	name := fmt.Sprintf("gpfs-pg-%d", time.Now().UnixNano()%1000000)
	req := func() *StorageClusterCreate {
		return &StorageClusterCreate{Name: name, PackageUUID: pkg.UUID, Plan: &StoragePlan{Kind: "gpfs",
			Nodes: []*StorageNodePlan{{Hostid: h[0], Roles: []string{"admin", "quorum", "nsd"}}, {Hostid: h[1], Roles: []string{"quorum", "nsd"}},
				{Hostid: h[2], Roles: []string{"quorum", "nsd"}}},
			Disks: []*StorageDiskPlan{{Hostid: h[0], DiskID: "wwn-0x5000ccc0"}, {Hostid: h[1], DiskID: "wwn-0x5000ccc1"}, {Hostid: h[2], DiskID: "wwn-0x5000ccc2"}}}}
	}
	_, _, err = StorageClusters.Create(ctx, req())
	wantCode(t, err, ErrStoragePackageState, "license not accepted")
	must(t, f.db.Model(pkg).Update("accepted_by", "admin").Error)
	bad := req()
	bad.Name = "1bad"
	_, _, err = StorageClusters.Create(ctx, bad)
	wantCode(t, err, ErrStorageInvalidPlan, "bad name")

	cluster, task, err := StorageClusters.Create(ctx, req())
	must(t, err)
	defer f.db.Unscoped().Where("id = ?", cluster.ID).Delete(&model.StorageCluster{})
	if task.Backend != "gpfs" || task.Kind != "deploy" || cluster.Status != model.StorageClusterDeploying || cluster.PackageID != pkg.ID ||
		cluster.Version != "6.0.0.2" || !strings.HasPrefix(cluster.SSHPrivKey, "v1:") || cluster.Layout != "replica" {
		t.Fatalf("cluster %+v task %+v", cluster, task)
	}
	c := &model.StorageCluster{}
	must(t, f.db.Take(c, cluster.ID).Error)
	if c.ActiveTask != task.ID {
		t.Fatalf("the deployment does not hold the cluster: %d", c.ActiveTask)
	}
	_, nodes, disks, err := StorageClusters.Get(ctx, cluster.UUID)
	must(t, err)
	for i, n := range nodes {
		if fg, _ := nodeAttr(n, "failure_group").(float64); int(fg) != i+1 || n.ReservedMemMB != 2048 || n.Status != model.StorageNodeJoining {
			t.Fatalf("node %d: %+v", i, n)
		}
	}
	for _, d := range disks {
		if diskAttr(d, "usage") != "dataAndMetadata" || diskAttr(d, "gpfs_pool") != "system" || d.Role != "nsd" || d.Status != model.StorageDiskClaiming || d.Serial == "" {
			t.Fatalf("disk %+v", d)
		}
	}
	again := req()
	again.Name = name + "b"
	_, _, err = StorageClusters.Create(ctx, again)
	wantCode(t, err, ErrStorageInvalidPlan, "hosts in another GPFS cluster")

	succeed := func(sent []*stcSent, result func(s *stcSent) string) {
		for _, s := range sent {
			must(t, f.report(s, model.StorageRunSucceeded, "", result(s)))
		}
	}
	none := func(*stcSent) string { return "{}" }
	precheckResult := func(osName string) func(*stcSent) string {
		return func(s *stcSent) string {
			return fmt.Sprintf(`{"items":[],"facts":{"hostname":"h%d","os":%q,"host_key":"ssh-ed25519 KEY%d"}}`, s.hostid, osName, s.hostid)
		}
	}

	sent := f.expect("precheck", "stc_precheck.sh", h...)
	// Local disks of the replica layout: none of them is taken for a shared LUN
	for _, s := range sent {
		for _, d := range s.input["disks"].([]interface{}) {
			if _, shared := d.(map[string]interface{})["shared"]; shared {
				t.Fatalf("precheck on %d: a local disk marked shared: %v", s.hostid, d)
			}
		}
	}
	succeed(sent, precheckResult("ubuntu 26.04"))
	f.wantTask(task.ID, model.StorageTaskFailed, "has no packages")
	must(t, RetryStorageTask(ctx, task.ID))
	succeed(f.expect("precheck again", "stc_precheck.sh", h...), precheckResult("ubuntu 24.04"))

	sent = f.expect("join", "stc_join.sh", h...)
	if sent[0].input["hold_kernel"] != true || sent[0].input["cluster_uuid"] != cluster.UUID {
		t.Fatalf("join input %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("fetch", "stc_fetch.sh", h...)
	if url, _ := sent[0].input["url"].(string); !strings.Contains(url, "/images/storage-packages/x/installer") || !strings.Contains(url, "X-Amz-Signature") {
		t.Fatalf("fetch url %v", sent[0].input["url"])
	}
	succeed(sent, none)
	sent = f.expect("install", "gpfs_install.sh", h...)
	debs, _ := sent[0].input["debs"].(map[string]interface{})
	if len(debs) != 6 || debs["gpfs.license.ec_6.0.0-2_amd64.deb"] != "m5" || debs["gpfs.gui_6.0.0-2_all.deb"] != nil || sent[0].input["version"] != "6.0.0-2" {
		t.Fatalf("install input %v", sent[0].input)
	}
	succeed(sent, none)
	succeed(f.expect("build", "gpfs_build_gpl.sh", h...), none)
	sent = f.expect("trust", "stc_ssh_trust.sh", h...)
	for _, s := range sent {
		from, _ := s.input["from"].([]interface{})
		_, hasKey := s.input["private_key"]
		known, _ := s.input["known_hosts"].([]interface{})
		if len(from) != 1 || from[0] != "10.93.0.1" || hasKey != (s.hostid == h[0]) || (s.hostid == h[0] && (len(known) != 3 || known[1] != "10.93.0.2,h9302,stc-h2 ssh-ed25519 KEY9302")) {
			t.Fatalf("trust input of host %d: %v", s.hostid, s.input)
		}
		if s.hostid == h[0] && !strings.Contains(s.input["private_key"].(string), "OPENSSH PRIVATE KEY") {
			t.Fatalf("private key not decrypted")
		}
	}
	succeed(sent, none)
	sent = f.expect("create cluster", "gpfs_cluster.sh", h[0])
	if sent[0].input["action"] != "create" || len(sent[0].input["server_license"].([]interface{})) != 3 || sent[0].input["cluster_name"] != name {
		t.Fatalf("create input %v", sent[0].input)
	}
	succeed(sent, func(*stcSent) string { return fmt.Sprintf(`{"cluster_name":"%s.cloudland","cluster_id":"1234"}`, name) })
	succeed(f.expect("start", "gpfs_cluster.sh", h[0]), none)
	sent = f.expect("resolve", "stc_resolve_disks.sh", h...)
	for _, s := range sent {
		ids, _ := s.input["disks"].([]interface{})
		if len(ids) != 1 || ids[0].(map[string]interface{})["serial"] == "" {
			t.Fatalf("resolve input %v", s.input)
		}
	}
	succeed(sent, func(s *stcSent) string {
		id := s.input["disks"].([]interface{})[0].(map[string]interface{})["id"]
		return fmt.Sprintf(`{"disks":[{"id":%q,"path":"/dev/sd%d","name":"sdb"}]}`, id, s.hostid)
	})
	sent = f.expect("nsd", "gpfs_nsd.sh", h[0])
	nsds, _ := sent[0].input["nsds"].([]interface{})
	n2 := nsds[1].(map[string]interface{})
	if len(nsds) != 3 || n2["name"] != fmt.Sprintf("cl%dh%dd1", cluster.ID, h[1]) || n2["device"] != fmt.Sprintf("/dev/sd%d", h[1]) ||
		n2["server"] != "10.93.0.2" || n2["failure_group"] != float64(2) || n2["usage"] != "dataAndMetadata" {
		t.Fatalf("nsd input %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("fs", "gpfs_fs.sh", h[0])
	if sent[0].input["fs_name"] != "fs1" || sent[0].input["data_replicas"] != float64(2) || sent[0].input["meta_replicas"] != float64(3) ||
		sent[0].input["block_size"] != "4M" || sent[0].input["mount_point"] != "/gpfs/fs1" ||
		sent[0].input["nsds"].([]interface{})[2].(map[string]interface{})["failure_group"] != float64(3) {
		t.Fatalf("fs input %v", sent[0].input)
	}
	succeed(sent, func(*stcSent) string { return `{"capacity_bytes":6000,"free_bytes":5000}` })
	sent = f.expect("finish", "stc_finish.sh", h...)
	if sent[0].input["reserve_mb"] != float64(2048) {
		t.Fatalf("finish input %v", sent[0].input)
	}
	succeed(sent, none)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	must(t, f.db.Take(c, cluster.ID).Error)
	if c.Status != model.StorageClusterReady || c.ClusterRef != name+".cloudland 1234" || c.ActiveTask != 0 {
		t.Fatalf("deployed cluster %+v", c)
	}
	fs := &model.StorageFilesystem{}
	must(t, f.db.Where("cluster_id = ?", c.ID).Take(fs).Error)
	_, nodes, disks, _ = StorageClusters.Get(ctx, cluster.UUID)
	for _, d := range disks {
		if d.Status != model.StorageDiskActive || d.FsID != fs.ID || !strings.HasPrefix(d.Name, fmt.Sprintf("cl%dh", c.ID)) {
			t.Fatalf("deployed disk %+v", d)
		}
	}
	if fs.CapacityBytes != 6000 || nodes[0].Status != model.StorageNodeActive {
		t.Fatalf("file system %+v node %+v", fs, nodes[0])
	}
	// The finish again (a retry of it): the file system row is there already, the disks keep its ID (they got 0)
	doneTask := &model.StorageTask{}
	must(t, f.db.Take(doneTask, task.ID).Error)
	must(t, f.db.Transaction(func(tx *gorm.DB) error { return gpfsDeployFinish(ctx, tx, doneTask, true) }))
	_, _, disks, _ = StorageClusters.Get(ctx, cluster.UUID)
	for _, d := range disks {
		if d.FsID != fs.ID {
			t.Fatalf("disk after the finish ran again %+v, file system %d", d, fs.ID)
		}
	}

	_, err = StorageClusters.Delete(ctx, cluster.UUID, &StorageClusterDelete{ConfirmName: "nope"})
	wantCode(t, err, ErrStorageConfirmMismatch, "wrong confirmation")
	del, err := StorageClusters.Delete(ctx, cluster.UUID, &StorageClusterDelete{ConfirmName: name, PurgePackages: true})
	must(t, err)
	sent = f.expect("teardown", "gpfs_cluster.sh", h[0])
	if sent[0].input["action"] != "teardown" || len(sent[0].input["nsds"].([]interface{})) != 3 || sent[0].input["filesystems"].([]interface{})[0] != "fs1" {
		t.Fatalf("teardown input %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("leave", "stc_leave.sh", h...)
	for _, s := range sent {
		if s.input["purge"] != true || len(s.input["wipe"].([]interface{})) != 1 {
			t.Fatalf("leave input %v", s.input)
		}
	}
	succeed(sent, none)
	f.wantTask(del.ID, model.StorageTaskSucceeded, "")
	var left int64
	f.db.Model(&model.StorageClusterDisk{}).Where("cluster_id = ?", c.ID).Count(&left)
	if f.db.Take(&model.StorageCluster{}, c.ID).Error == nil || left != 0 {
		t.Fatalf("the cluster or its disks are still there (%d disks)", left)
	}
	// The disks are free for another cluster now
	again.Name = name + "c"
	c2, _, err := StorageClusters.Create(ctx, again)
	must(t, err)
	f.db.Unscoped().Where("cluster_id = ?", c2.ID).Delete(&model.StorageClusterDisk{})
	f.db.Unscoped().Where("cluster_id = ?", c2.ID).Delete(&model.StorageClusterNode{})
	f.db.Unscoped().Where("id = ?", c2.ID).Delete(&model.StorageCluster{})
}
