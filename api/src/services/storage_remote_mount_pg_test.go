/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Remote mounts against PostgreSQL with the fake cland standing in for the hosts (shared-storage-design.md §7.11): a
// file system of one GPFS cluster mounted by the host of another (keys of both, the grant, the mount, each step on an
// admin host of its cluster), the owner's pools on it given to that host and its reports taken, an instance there
// fenced in its own cluster, the refusals while the mount is there, and the unmount. The node script only ran against
// stubs (WSL stc-test17.sh).

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

func TestStorageRemoteMountPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db := f.ctx, f.db
	owner := f.cluster
	h4 := stcHosts[3]
	access := &model.StorageCluster{Name: fmt.Sprintf("access-pg-%d", time.Now().UnixNano()%1000000), Kind: model.StorageKindGPFS,
		Mode: model.StorageModeManaged, Layout: model.StorageLayoutReplica, Status: model.StorageClusterReady, Health: model.StorageHealthUnknown}
	must(t, db.Create(access).Error)
	must(t, db.Create(&model.StorageClusterNode{ClusterID: access.ID, Hostid: h4, Roles: "admin,quorum,nsd", Status: model.StorageNodeActive,
		Attrs: `{"failure_group":1}`}).Error)
	own := &model.StorageFilesystem{ClusterID: access.ID, Name: "fs1", MountPoint: "/gpfs/fs1", Status: "ready"}
	must(t, db.Create(own).Error)
	pool := &model.StoragePool{Name: fmt.Sprintf("rm-pool-%d", time.Now().UnixNano()%100000), Driver: model.StorageDriverGPFS, ClusterID: owner.ID,
		FilesystemID: f.fs.ID, MountPath: "/gpfs/fs1/cl_rm", Status: model.StoragePoolActive}
	must(t, db.Create(pool).Error)
	t.Cleanup(func() {
		db.Unscoped().Where("owner_cluster_id = ? OR access_cluster_id = ?", owner.ID, access.ID).Delete(&model.StorageRemoteMount{})
		db.Unscoped().Where("cluster_id = ?", access.ID).Delete(&model.StorageFilesystem{})
		db.Unscoped().Where("cluster_id = ?", access.ID).Delete(&model.StorageClusterNode{})
		db.Unscoped().Where("pool_id = ?", pool.ID).Delete(&model.HyperStoragePool{})
		db.Unscoped().Delete(access)
	})

	_, err := StorageClusters.CreateRemoteMount(ctx, owner.UUID, "fs1", owner.UUID)
	wantCode(t, err, ErrStorageInvalidPlan, "its own file systems")
	_, err = StorageClusters.CreateRemoteMount(ctx, owner.UUID, "nofs", access.UUID)
	wantCode(t, err, ErrStorageInvalidPlan, "no such file system")
	// The accessing cluster has a file system of that name: the remote one keeps its name and mount point
	_, err = StorageClusters.CreateRemoteMount(ctx, owner.UUID, "fs1", access.UUID)
	wantCode(t, err, ErrStorageInvalidPlan, "a file system of the same name on the cluster that mounts")
	must(t, db.Unscoped().Delete(own).Error)

	task, err := StorageClusters.CreateRemoteMount(ctx, owner.UUID, "fs1", access.UUID)
	must(t, err)
	if task.ClusterID != access.ID || task.Kind != StorageTaskRemoteMount {
		t.Fatalf("a task of the cluster that mounts: %+v", task)
	}
	succeed := func(sent []*stcSent, result func(s *stcSent) string) {
		for _, s := range sent {
			must(t, f.report(s, model.StorageRunSucceeded, "", result(s)))
		}
	}
	none := func(*stcSent) string { return "{}" }
	key := func(name, k string) func(*stcSent) string {
		return func(*stcSent) string {
			return fmt.Sprintf(`{"cluster_name":%q,"public_key":%q}`, name, base64.StdEncoding.EncodeToString([]byte(k)))
		}
	}
	sent := f.expect("owner key", "gpfs_remote.sh", f.hosts[0])
	if sent[0].input["action"] != "key" || sent[0].input["cluster_uuid"] != owner.UUID {
		t.Fatalf("owner key input %v", sent[0].input)
	}
	succeed(sent, key("own.cl", "OWNERKEY"))
	sent = f.expect("access key", "gpfs_remote.sh", h4)
	if sent[0].input["cluster_uuid"] != access.UUID {
		t.Fatalf("access key input %v", sent[0].input)
	}
	succeed(sent, key("acc.cl", "ACCESSKEY"))
	sent = f.expect("grant", "gpfs_remote.sh", f.hosts[0])
	in := sent[0].input
	if in["action"] != "grant" || in["remote_name"] != "acc.cl" || in["remote_key"] != base64.StdEncoding.EncodeToString([]byte("ACCESSKEY")) ||
		in["filesystem"] != "fs1" || in["cluster_uuid"] != owner.UUID {
		t.Fatalf("grant input %v", in)
	}
	succeed(sent, none)
	sent = f.expect("mount", "gpfs_remote.sh", h4)
	in = sent[0].input
	if in["action"] != "mount" || in["remote_name"] != "own.cl" || fmt.Sprint(in["contacts"]) != "[10.93.0.1 10.93.0.2 10.93.0.3]" ||
		in["filesystem"] != "fs1" || in["mount_point"] != "/gpfs/fs1" || in["cluster_uuid"] != access.UUID {
		t.Fatalf("mount input %v", in)
	}
	succeed(sent, func(*stcSent) string { return `{"mounted_nodes":1}` })
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	views, err := StorageClusters.RemoteMounts(ctx, access.UUID)
	must(t, err)
	if len(views) != 1 || views[0].Mount.Status != StorageRemoteReady || views[0].Owner.ID != owner.ID {
		t.Fatalf("remote mounts %+v", views)
	}
	mount := views[0].Mount

	// The owner's pools on the file system go to the host that mounts it, under the owner's uuid
	f.cland.take()
	SyncSharedPools(ctx)
	found := false
	for _, rec := range f.cland.take() {
		// Marked remote: the host's metrics do not report the owner (its own cluster would be described under it)
		if strings.HasPrefix(rec, fmt.Sprintf("inter=%d |", h4)) && strings.Contains(rec, "sync_shared_pools.sh '"+owner.UUID+"' remote") && strings.Contains(rec, pool.UUID) {
			found = true
		}
	}
	if !found {
		t.Fatal("the host that mounts gets the owner's pools on the file system")
	}
	// Its report of the pool is taken (a host of no cluster of the pool is not)
	must(t, HandleSharedPoolStatus(ctx, h4, []*SharedPoolReport{{Pool: pool.UUID, Status: model.HyperPoolReady, Size: 100, Used: 1}}))
	var rows int64
	db.Model(&model.HyperStoragePool{}).Where("pool_id = ? AND hostid = ? AND status = ?", pool.ID, h4, model.HyperPoolReady).Count(&rows)
	if rows != 1 {
		t.Fatal("the report of the host that mounts is taken")
	}
	if access, ok := sharedPoolRemoteHost(db, pool, h4); !ok || access == 0 {
		t.Fatal("the host reaches the pool through the remote mount")
	}
	if _, ok := sharedPoolRemoteHost(db, pool, f.hosts[1]); ok {
		t.Fatal("a member of the owner is no remote host")
	}

	// The refusals while the mount is there
	_, err = StorageClusters.Delete(ctx, owner.UUID, &StorageClusterDelete{ConfirmName: owner.Name})
	wantCode(t, err, ErrStoragePoolInUse, "the owner of a remote mount (its pools are refused first)")
	vol := &model.Volume{Name: "rm-vol", StoragePoolID: pool.ID, Status: model.VolumeStatusAttached}
	inst := &model.Instance{Hostname: "rm-inst", Hyper: h4, Status: "running"}
	must(t, db.Create(inst).Error)
	vol.InstanceID = inst.ID
	must(t, db.Create(vol).Error)
	defer db.Unscoped().Delete(vol)
	defer db.Unscoped().Delete(inst)
	_, err = StorageClusters.DeleteRemoteMount(ctx, owner.UUID, mount.UUID)
	wantCode(t, err, ErrStoragePoolInUse, "a volume of the pool is attached to an instance on the host that mounts")
	// The instance on that host is fenced in the host's own cluster
	inst.Volumes = []*model.Volume{vol}
	_, clusters, err := evacueePools(ctx, inst)
	must(t, err)
	if len(clusters) != 1 || clusters[0] != access.ID {
		t.Fatalf("fenced in its own cluster: %v", clusters)
	}
	must(t, db.Unscoped().Delete(vol).Error)

	// A host leaving the cluster that mounts loses its rows of the owner's pools (its reports of them are not taken
	// any more, the rows would stay ready for good) and is given an empty list of the owner
	owners, err := remoteMountHostLeft(db, access.ID, h4)
	must(t, err)
	db.Model(&model.HyperStoragePool{}).Where("pool_id = ? AND hostid = ?", pool.ID, h4).Count(&rows)
	if rows != 0 || len(owners) != 1 || owners[0] != owner.UUID {
		t.Fatalf("a host leaving: rows %d, owners %v", rows, owners)
	}
	f.cland.take()
	clearRemotePoolLists(ctx, h4, owners)
	cleared := false
	for _, rec := range f.cland.take() {
		if strings.HasPrefix(rec, fmt.Sprintf("inter=%d |", h4)) && strings.Contains(rec, "sync_shared_pools.sh '"+owner.UUID+"' remote") && strings.Contains(rec, "\n[]\n") {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("the host that left gets an empty list of the owner")
	}

	// ---- the unmount ----
	un, err := StorageClusters.DeleteRemoteMount(ctx, owner.UUID, mount.UUID)
	must(t, err)
	succeed(f.expect("un owner key", "gpfs_remote.sh", f.hosts[0]), key("own.cl", "OWNERKEY"))
	succeed(f.expect("un access key", "gpfs_remote.sh", h4), key("acc.cl", "ACCESSKEY"))
	sent = f.expect("unmount", "gpfs_remote.sh", h4)
	if sent[0].input["action"] != "unmount" || sent[0].input["remote_name"] != "own.cl" || sent[0].input["forget_cluster"] != true {
		t.Fatalf("unmount input %v", sent[0].input)
	}
	succeed(sent, none)
	sent = f.expect("revoke", "gpfs_remote.sh", f.hosts[0])
	if sent[0].input["action"] != "revoke" || sent[0].input["remote_name"] != "acc.cl" || sent[0].input["forget_cluster"] != true {
		t.Fatalf("revoke input %v", sent[0].input)
	}
	succeed(sent, none)
	f.wantTask(un.ID, model.StorageTaskSucceeded, "")
	var left int64
	db.Model(&model.StorageRemoteMount{}).Where("id = ?", mount.ID).Count(&left)
	db.Model(&model.HyperStoragePool{}).Where("pool_id = ? AND hostid = ?", pool.ID, h4).Count(&rows)
	if left != 0 || rows != 0 {
		t.Fatalf("the mount and the host's row of the pool go: %d %d", left, rows)
	}
}
