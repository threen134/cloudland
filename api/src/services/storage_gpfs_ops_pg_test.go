/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Changing a GPFS cluster against PostgreSQL with the fake cland (shared-storage-design.md §7.5): disks and a host
// join, a disk and a host leave, the file system is rebalanced; the role rules refuse what would break the cluster.

import (
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

func TestGPFSClusterChangesPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
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
	pkg := &model.StoragePackage{Kind: "gpfs", Version: "6.0.0.2", FileName: "installer", SizeBytes: 1000, SHA256: strings.Repeat("b", 64),
		ObjectKey: "storage-packages/y/installer", PayloadLine: 680, Distros: `["ubuntu24"]`, Status: model.StoragePackageReady, AcceptedBy: "admin",
		Manifest: `{"gpfs.base_6.0.0-2_amd64.deb":"m1","gpfs.gpl_6.0.0-2_all.deb":"m2","gpfs.gskit_8.0.55-19.1_amd64.deb":"m3","gpfs.msg.en-us_6.0.0-2_all.deb":"m4","gpfs.license.ec_6.0.0-2_amd64.deb":"m5"}`}
	must(t, db.Create(pkg).Error)
	defer db.Unscoped().Delete(pkg)
	pub, priv, err := newStorageSSHKey()
	must(t, err)
	must(t, db.Model(f.cluster).Updates(map[string]interface{}{"package_id": pkg.ID, "ssh_pub_key": pub, "ssh_priv_key": priv}).Error)
	// As a deployment leaves them: names of the NSDs, host keys of the members
	disks := []*model.StorageClusterDisk{}
	db.Where("cluster_id = ?", f.cluster.ID).Order("hostid").Find(&disks)
	for _, d := range disks {
		must(t, db.Model(&model.StorageClusterDisk{}).Where("id = ?", d.ID).Update("name", fmt.Sprintf("cl%dh%dd1", f.cluster.ID, d.Hostid)).Error)
	}
	for i, hh := range h {
		must(t, db.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND hostid = ?", f.cluster.ID, hh).
			Update("attrs", fmt.Sprintf(`{"failure_group":%d,"host_key":"ssh-ed25519 KEY%d","hostname":"h%d"}`, i+1, hh, hh)).Error)
	}
	scanned := func(hostid int32, id, name string) {
		db.Unscoped().Where("hostid = ? AND disk_id = ?", hostid, id).Delete(&model.HyperDisk{})
		must(t, db.Create(&model.HyperDisk{Hostid: hostid, DiskID: id, Name: name, Serial: "S" + id, SizeBytes: 1 << 40, Media: "hdd",
			State: model.DiskFree, ScannedAt: time.Now()}).Error)
	}
	ok := func(*stcSent) string { return "{}" }
	succeed := func(sent []*stcSent, result func(*stcSent) string) {
		for _, s := range sent {
			must(t, f.report(s, model.StorageRunSucceeded, "", result(s)))
		}
	}
	diskByID := func(id string) *model.StorageClusterDisk {
		d := &model.StorageClusterDisk{}
		must(t, db.Where("cluster_id = ? AND disk_id = ?", f.cluster.ID, id).Take(d).Error)
		return d
	}

	// Three failure groups is the least: no disk of a host with one disk may go now
	_, err = StorageClusters.RemoveDisk(ctx, f.cluster.UUID, disks[2].UUID)
	wantCode(t, err, ErrStorageInvalidPlan, "fewer than three failure groups")
	// A pool, so the joining host gets its row
	pool, _ := f.createPool(fmt.Sprintf("spo-%d", time.Now().UnixNano()%1000000), 0, func(hostid int32, p string) string {
		return readyCheck(p, 10*gib, 0)
	})

	// A disk of a member joins
	scanned(h[1], "wwn-ops-a", "sdc")
	task, err := StorageClusters.AddDisks(ctx, f.cluster.UUID, &StorageClusterExpand{Disks: []*StorageDiskPlan{{Hostid: h[1], DiskID: "wwn-ops-a"}}})
	must(t, err)
	_, err = StorageClusters.AddDisks(ctx, f.cluster.UUID, &StorageClusterExpand{Disks: []*StorageDiskPlan{{Hostid: h[2], DiskID: "x"}}})
	wantCode(t, err, ErrStorageClusterBusy, "a change holds the cluster")
	sent := f.expect("resolve", "stc_resolve_disks.sh", h[1])
	if ids := sent[0].input["disks"].([]interface{}); len(ids) != 2 {
		t.Fatalf("resolve input %v", sent[0].input)
	}
	succeed(sent, func(*stcSent) string {
		return fmt.Sprintf(`{"disks":[{"id":"wwn-shared-1","path":"/dev/sdb"},{"id":"wwn-ops-a","path":"/dev/sdc"}]}`)
	})
	sent = f.expect("nsd", "gpfs_nsd.sh", h[0])
	nsds := sent[0].input["nsds"].([]interface{})
	newName := fmt.Sprintf("cl%dh%dd2", f.cluster.ID, h[1])
	if len(nsds) != 1 || nsds[0].(map[string]interface{})["name"] != newName || nsds[0].(map[string]interface{})["device"] != "/dev/sdc" ||
		nsds[0].(map[string]interface{})["failure_group"] != float64(2) {
		t.Fatalf("nsd input %v", sent[0].input)
	}
	succeed(sent, ok)
	sent = f.expect("add disks", "gpfs_fs.sh", h[0])
	if sent[0].input["action"] != "add" || sent[0].input["fs_name"] != "fs1" || len(sent[0].input["nsds"].([]interface{})) != 1 {
		t.Fatalf("add disks input %v", sent[0].input)
	}
	succeed(sent, ok)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	if d := diskByID("wwn-ops-a"); d.Status != model.StorageDiskActive || d.Name != newName || d.FsID != f.fs.ID {
		t.Fatalf("added disk %+v", d)
	}

	// A host joins with a disk; an instance there will use the pool
	scanned(stcHosts[3], "wwn-ops-b", "sdb")
	_, err = StorageClusters.AddNodes(ctx, f.cluster.UUID, &StorageClusterExpand{Nodes: []*StorageNodePlan{{Hostid: h[0], Roles: []string{"client"}}}})
	wantCode(t, err, ErrStorageInvalidPlan, "a member again")
	_, err = StorageClusters.AddNodes(ctx, f.cluster.UUID, &StorageClusterExpand{Nodes: []*StorageNodePlan{{Hostid: stcHosts[3], Roles: []string{"quorum"}}}})
	wantCode(t, err, ErrStorageInvalidPlan, "an even number of quorum hosts")
	task, err = StorageClusters.AddNodes(ctx, f.cluster.UUID, &StorageClusterExpand{Nodes: []*StorageNodePlan{{Hostid: stcHosts[3], Roles: []string{"nsd"}}},
		Disks: []*StorageDiskPlan{{Hostid: stcHosts[3], DiskID: "wwn-ops-b"}}})
	must(t, err)
	h4 := stcHosts[3]
	sent = f.expect("precheck", "stc_precheck.sh", h4)
	if peers := sent[0].input["peers"].([]interface{}); len(peers) != 3 || sent[0].input["cluster_uuid"] != f.cluster.UUID {
		t.Fatalf("precheck input %v", sent[0].input)
	}
	succeed(sent, func(s *stcSent) string {
		return `{"items":[],"facts":{"hostname":"h4","os":"ubuntu 24.04","host_key":"ssh-ed25519 KEYNEW"}}`
	})
	for _, script := range []string{"stc_join.sh", "stc_fetch.sh", "gpfs_install.sh", "gpfs_build_gpl.sh"} {
		succeed(f.expect(script, script, h4), ok)
	}
	sent = f.expect("trust", "stc_ssh_trust.sh", append(append([]int32{}, h...), h4)...)
	for _, s := range sent {
		if s.hostid != h[0] {
			continue
		}
		known := s.input["known_hosts"].([]interface{})
		if len(known) != 4 || !strings.Contains(fmt.Sprint(known), "KEY9302") || !strings.Contains(fmt.Sprint(known), "10.93.0.4,h4,stc-h4 ssh-ed25519 KEYNEW") {
			t.Fatalf("known hosts of the admin %v", known)
		}
	}
	succeed(sent, ok)
	sent = f.expect("add node", "gpfs_cluster.sh", h[0])
	if sent[0].input["action"] != "add" || fmt.Sprint(sent[0].input["nodes"]) != "[map[ip:10.93.0.4 manager:false quorum:false]]" ||
		fmt.Sprint(sent[0].input["server_license"]) != "[10.93.0.4]" {
		t.Fatalf("add node input %v", sent[0].input)
	}
	succeed(sent, ok)
	succeed(f.expect("resolve new host", "stc_resolve_disks.sh", h4), func(*stcSent) string {
		return `{"disks":[{"id":"wwn-ops-b","path":"/dev/sdb"}]}`
	})
	sent = f.expect("nsd of the new host", "gpfs_nsd.sh", h[0])
	nsd := sent[0].input["nsds"].([]interface{})[0].(map[string]interface{})
	if nsd["failure_group"] != float64(4) || nsd["name"] != fmt.Sprintf("cl%dh%dd1", f.cluster.ID, h4) || nsd["server"] != "10.93.0.4" {
		t.Fatalf("nsd of the new host %v", nsd)
	}
	succeed(sent, ok)
	succeed(f.expect("add disks of the new host", "gpfs_fs.sh", h[0]), ok)
	succeed(f.expect("finish", "stc_finish.sh", h4), ok)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	n4 := &model.StorageClusterNode{}
	must(t, db.Where("cluster_id = ? AND hostid = ?", f.cluster.ID, h4).Take(n4).Error)
	if n4.Status != model.StorageNodeActive || nodeAttr(n4, "host_key") != "ssh-ed25519 KEYNEW" || nodeAttr(n4, "failure_group") != float64(4) {
		t.Fatalf("joined host %+v", n4)
	}
	if r := f.row(h4, pool.ID); r.Status != model.HyperPoolUnavailable || !strings.Contains(r.Reason, "first report") {
		t.Fatalf("pool row of the joined host %+v", r)
	}

	// The disk added first leaves: its data moves, then its host wipes it and keeps the other one
	added := diskByID("wwn-ops-a")
	task, err = StorageClusters.RemoveDisk(ctx, f.cluster.UUID, added.UUID)
	must(t, err)
	sent = f.expect("remove disk", "gpfs_fs.sh", h[0])
	if sent[0].input["action"] != "remove" || fmt.Sprint(sent[0].input["nsds"]) != "["+newName+"]" || sent[0].input["damaged"] != false {
		t.Fatalf("remove disk input %v", sent[0].input)
	}
	succeed(sent, ok)
	sent = f.expect("release", "stc_release_disks.sh", h[1])
	if fmt.Sprint(sent[0].input["keep"]) != "[wwn-shared-1]" || len(sent[0].input["release"].([]interface{})) != 1 {
		t.Fatalf("release input %v", sent[0].input)
	}
	succeed(sent, ok)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	if db.Where("id = ?", added.ID).Take(&model.StorageClusterDisk{}).Error == nil {
		t.Fatalf("the removed disk is still claimed")
	}

	// The joined host leaves; not while an instance there uses a pool of the cluster
	inst := &model.Instance{Hostname: "spo-inst", Hyper: h4, Status: model.InstanceStatusRunning, Owner: 1}
	must(t, db.Create(inst).Error)
	defer db.Unscoped().Delete(inst)
	vol := &model.Volume{Name: "spo-v", Size: 1, Status: model.VolumeStatusAttached, InstanceID: inst.ID, StoragePoolID: pool.ID, Owner: 1, Path: "volumes/x.disk"}
	must(t, db.Create(vol).Error)
	_, err = StorageClusters.RemoveNode(ctx, f.cluster.UUID, &StorageNodeRemove{Hostid: h4})
	wantCode(t, err, ErrStoragePoolInUse, "an instance there uses the pool")
	db.Unscoped().Delete(vol)
	_, err = StorageClusters.RemoveNode(ctx, f.cluster.UUID, &StorageNodeRemove{Hostid: h4, Offline: true, Confirm: "stc-h4"})
	wantCode(t, err, ErrStorageInvalidPlan, "offline removal of an online host")
	_, err = StorageClusters.RemoveNode(ctx, f.cluster.UUID, &StorageNodeRemove{Hostid: h[0]})
	wantCode(t, err, ErrStorageInvalidPlan, "the only admin host")
	task, err = StorageClusters.RemoveNode(ctx, f.cluster.UUID, &StorageNodeRemove{Hostid: h4, PurgePackages: true})
	must(t, err)
	sent = f.expect("remove disks of the host", "gpfs_fs.sh", h[0])
	if fmt.Sprint(sent[0].input["nsds"]) != fmt.Sprintf("[cl%dh%dd1]", f.cluster.ID, h4) {
		t.Fatalf("remove disks of the host %v", sent[0].input)
	}
	succeed(sent, ok)
	sent = f.expect("remove node", "gpfs_cluster.sh", h[0])
	if sent[0].input["action"] != "remove" || sent[0].input["ip"] != "10.93.0.4" || sent[0].input["offline"] != false {
		t.Fatalf("remove node input %v", sent[0].input)
	}
	succeed(sent, ok)
	sent = f.expect("leave", "stc_leave.sh", h4)
	if sent[0].input["purge"] != true || len(sent[0].input["wipe"].([]interface{})) != 1 {
		t.Fatalf("leave input %v", sent[0].input)
	}
	succeed(sent, ok)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	var left int64
	db.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND hostid = ?", f.cluster.ID, h4).Count(&left)
	if left != 0 || db.Where("hostid = ? AND pool_id = ?", h4, pool.ID).Take(&model.HyperStoragePool{}).Error == nil {
		t.Fatalf("the host is still in the cluster (%d) or has its pool row", left)
	}

	// A host gone for good: only with its name typed, and only while the rules still hold without it
	f.setHostStatus(h[2], 10)
	_, err = StorageClusters.RemoveNode(ctx, f.cluster.UUID, &StorageNodeRemove{Hostid: h[2], Offline: true, Confirm: "nope"})
	wantCode(t, err, ErrStorageConfirmMismatch, "wrong host name")
	_, err = StorageClusters.RemoveNode(ctx, f.cluster.UUID, &StorageNodeRemove{Hostid: h[2], Offline: true, Confirm: "stc-h3"})
	wantCode(t, err, ErrStorageInvalidPlan, "two failure groups left")
	f.setHostStatus(h[2], 1)

	// Rebalance
	task, err = StorageClusters.Rebalance(ctx, f.cluster.UUID, "")
	must(t, err)
	sent = f.expect("rebalance", "gpfs_fs.sh", h[0])
	if sent[0].input["action"] != "rebalance" || sent[0].input["fs_name"] != "fs1" {
		t.Fatalf("rebalance input %v", sent[0].input)
	}
	succeed(sent, ok)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	_, err = StorageClusters.Rebalance(ctx, f.cluster.UUID, "fs9")
	wantCode(t, err, ErrStorageInvalidPlan, "no such file system")

	// Every minute an admin host starts the disks of a host that came back
	f.cland.take()
	maintainStorageClusters(ctx)
	found := false
	for _, rec := range f.cland.take() {
		if strings.HasPrefix(rec, fmt.Sprintf("inter=%d | ", h[0])) && strings.Contains(rec, "storage/gpfs_disks_up.sh '"+f.cluster.UUID+"'") &&
			strings.Contains(rec, `{"filesystems":["fs1"]}`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no disk check was sent to the admin host")
	}
}

// An imported GPFS cluster: its hosts are checked, a pool is registered on a directory that exists and removed again,
// the cluster is forgotten; nothing runs a GPFS command of CloudLand's on it
func TestGPFSImportPG(t *testing.T) {
	f := newStcFixture(t)
	ctx, db := f.ctx, f.db
	h := stcHosts[:2]
	name := fmt.Sprintf("gpfs-ext-%d", time.Now().UnixNano()%1000000)
	_, _, err := StorageClusters.Import(ctx, &StorageClusterImport{Kind: "gpfs", Name: name, Hostids: h, Params: []byte(`{}`)})
	wantCode(t, err, ErrStorageInvalidPlan, "no file system")
	_, _, err = StorageClusters.Import(ctx, &StorageClusterImport{Kind: "gpfs", Name: name, Hostids: h, Params: []byte(`{"fs_name":"ext1","mount_point":"/gpfs/a b"}`)})
	wantCode(t, err, ErrStorageInvalidPlan, "mount point with a space")
	cluster, task, err := StorageClusters.Import(ctx, &StorageClusterImport{Kind: "gpfs", Name: name, Hostids: h, Params: []byte(`{"fs_name":"ext1"}`)})
	must(t, err)
	defer func() {
		db.Unscoped().Where("cluster_id = ?", cluster.ID).Delete(&model.StorageFilesystem{})
		db.Unscoped().Where("cluster_id = ?", cluster.ID).Delete(&model.StorageClusterNode{})
		db.Unscoped().Where("id = ?", cluster.ID).Delete(&model.StorageCluster{})
	}()
	if cluster.Mode != model.StorageModeExternal || cluster.Status != model.StorageClusterDeploying || task.Kind != StorageTaskImport {
		t.Fatalf("imported cluster %+v task %+v", cluster, task)
	}
	sent := f.expect("check", "gpfs_import.sh", h...)
	if sent[0].input["fs_name"] != "ext1" || sent[0].input["mount_point"] != "/gpfs/ext1" {
		t.Fatalf("check input %v", sent[0].input)
	}
	for _, s := range sent {
		must(t, f.report(s, model.StorageRunSucceeded, "", `{"capacity_bytes":1000,"free_bytes":800}`))
	}
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	c := &model.StorageCluster{}
	must(t, db.Take(c, cluster.ID).Error)
	fs := &model.StorageFilesystem{}
	must(t, db.Where("cluster_id = ?", c.ID).Take(fs).Error)
	if c.Status != model.StorageClusterReady || fs.Name != "ext1" || fs.MountPoint != "/gpfs/ext1" || fs.CapacityBytes != 1000 {
		t.Fatalf("imported cluster %+v file system %+v", c, fs)
	}
	_, err = StorageClusters.AddDisks(ctx, c.UUID, &StorageClusterExpand{Disks: []*StorageDiskPlan{{Hostid: h[0], DiskID: "x"}}})
	wantCode(t, err, ErrStorageInvalidPlan, "changing an imported cluster")

	// A pool on a directory of the file system
	_, _, err = storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name + "-p", ClusterUUID: c.UUID, Params: []byte(`{"path":"/data/x"}`)})
	wantCode(t, err, ErrStorageInvalidPlan, "a directory outside the file system")
	_, _, err = storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name + "-p", ClusterUUID: c.UUID, QuotaGB: 10, Params: []byte(`{"path":"/gpfs/ext1/vols"}`)})
	wantCode(t, err, ErrStorageInvalidPlan, "a quota on an imported cluster")
	pool, ptask, err := storagePoolAdmin.CreateShared(ctx, &SharedPoolCreate{Name: name + "-p", ClusterUUID: c.UUID, Params: []byte(`{"path":"/gpfs/ext1/vols","fileset":"vols"}`)})
	must(t, err)
	defer db.Unscoped().Where("pool_id = ?", pool.ID).Delete(&model.HyperStoragePool{})
	defer db.Unscoped().Where("id = ?", pool.ID).Delete(&model.StoragePool{})
	sent = f.expect("register", "gpfs_pool.sh", h[0])
	if sent[0].input["action"] != "register" || sent[0].input["junction"] != "/gpfs/ext1/vols" || sent[0].input["mount_point"] != "/gpfs/ext1" {
		t.Fatalf("register input %v", sent[0].input)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"policy_installed":false}`))
	for _, s := range f.expect("sync", "stc_pools.sh", h...) {
		must(t, f.report(s, model.StorageRunSucceeded, "", readyCheck(pool.UUID, 1000, 200)))
	}
	f.wantTask(ptask.ID, model.StorageTaskSucceeded, "")
	p := &model.StoragePool{}
	must(t, db.Take(p, pool.ID).Error)
	if p.Status != model.StoragePoolActive || p.MountPath != "/gpfs/ext1/vols" || p.CloneMode != "copy" || !strings.Contains(p.DriverParams, `"external":true`) {
		t.Fatalf("registered pool %+v", p)
	}
	_, err = storagePoolAdmin.UpdateSharedQuota(ctx, p, 5)
	wantCode(t, err, ErrStorageInvalidPlan, "a quota on an imported cluster")
	dtask, err := storagePoolAdmin.DeleteShared(ctx, p)
	must(t, err)
	for _, s := range f.expect("sync before unregistering", "stc_pools.sh", h...) {
		must(t, f.report(s, model.StorageRunSucceeded, "", `{"pools":0}`))
	}
	sent = f.expect("unregister", "gpfs_pool.sh", h[0])
	if sent[0].input["action"] != "unregister" {
		t.Fatalf("unregister input %v", sent[0].input)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", "{}"))
	f.wantTask(dtask.ID, model.StorageTaskSucceeded, "")

	// Forgetting the cluster only cleans the hosts
	del, err := StorageClusters.Delete(ctx, c.UUID, &StorageClusterDelete{ConfirmName: name})
	must(t, err)
	for _, s := range f.expect("forget", "stc_forget.sh", h...) {
		must(t, f.report(s, model.StorageRunSucceeded, "", "{}"))
	}
	f.wantTask(del.ID, model.StorageTaskSucceeded, "")
	if db.Take(&model.StorageCluster{}, c.ID).Error == nil {
		t.Fatalf("the imported cluster is still there")
	}
}
