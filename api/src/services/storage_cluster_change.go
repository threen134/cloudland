/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Replacing a failed disk and changing the roles of a member (shared-storage-design.md §7.5, §8.5, §13.1). Like the
// other changes, what every kind shares lives here (the checks, the records of what comes and goes, the role rules of
// the kind on the cluster as it will be) and the steps come from the backend.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

const (
	StorageTaskReplaceDisk = "replace_disk"
	StorageTaskChangeRoles = "change_roles"
)

// A disk is replaced as failed only on a state the health check saw this recently; the GPFS step looks again
// before it drops the disk
const storageReplaceFreshness = 10 * time.Minute

// storageTaskFinish is the Finish of a task kind
type storageTaskFinish = func(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error

// ReplaceDisk swaps a failed disk of a managed cluster for a new disk of the same host (§7.5 GPFS: the failed NSD is
// dropped with mmdeldisk -p, the new one added and the replication restored; §8.5 Ceph: the OSD is destroyed keeping
// its id, the new disk takes it). Only a disk the storage reports down: one that works is removed the normal way, so
// its data moves first. The failed disk is not wiped
func (a *StorageClusterAdmin) ReplaceDisk(ctx context.Context, uuid, diskUUID string, newDisk *StorageDiskPlan) (*model.StorageTask, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	cluster, nodes, disks, backend, err := storageClusterForChange(ctx, uuid, func(c *StorageCapabilities) bool { return c.ReplaceDisk })
	if err != nil {
		return nil, err
	}
	var old *model.StorageClusterDisk
	for _, d := range disks {
		if d.UUID == diskUUID {
			old = d
		}
	}
	if old == nil {
		return nil, NewCLError(ErrStorageDiskNotAllowed, "The disk is not in the cluster", nil)
	}
	db := dbs.DBContext(ctx)
	label := old.Name
	if label == "" {
		label = old.DiskID
	}
	if old.Status != model.StorageDiskActive && old.Status != model.StorageDiskFailed {
		return nil, planError("Disk %s is %s: only a disk in the cluster is replaced", label, old.Status)
	}
	switch {
	case old.State == "" || old.CheckedAt == nil:
		return nil, planError("The health check has not seen disk %s yet: wait a minute", label)
	case time.Since(*old.CheckedAt) > storageReplaceFreshness:
		return nil, planError("The health check last saw disk %s %s ago: wait for a fresh check", label, time.Since(*old.CheckedAt).Round(time.Minute))
	case old.State == "up":
		return nil, planError("Disk %s works (the storage reports it up): remove it the normal way, its data moves first, then add the new disk", label)
	}
	if newDisk == nil || newDisk.DiskID == "" {
		return nil, planError("No new disk is given")
	}
	newDisk.Hostid = old.Hostid
	if newDisk.DiskID == old.DiskID {
		return nil, planError("The new disk must be another disk than %s", label)
	}
	params, err := backend.ParseParams([]byte(cluster.Params))
	if err != nil {
		return nil, err
	}
	var member *model.StorageClusterNode
	for _, n := range nodes {
		if n.Hostid == old.Hostid {
			member = n
		}
	}
	if member == nil {
		return nil, planError("The host of disk %s is not in the cluster", label)
	}
	planNode := &StorageNodePlan{Hostid: member.Hostid, Roles: strings.Split(member.Roles, ",")}
	plan := &StoragePlan{Kind: cluster.Kind, Nodes: []*StorageNodePlan{planNode}, Disks: []*StorageDiskPlan{newDisk}, Params: []byte(cluster.Params),
		AllowUnsupported: cluster.Unsupported, ClusterID: cluster.ID}
	_, scanned, err := checkStoragePlan(db, plan)
	if err != nil {
		return nil, err
	}
	hd := scanned[storageDiskKey(newDisk.Hostid, newDisk.DiskID)]
	if newDisk.Media == "" {
		newDisk.Media = hd.Media
	}
	afterNodes, afterDisks := layoutAfter(nodes, disks, nil, []*StorageDiskPlan{newDisk}, -1, old.ID)
	withDiskRole(afterNodes, afterDisks, backend.DiskRole())
	if err = backend.CheckLayout(afterNodes, afterDisks, params); err != nil {
		return nil, err
	}
	// The new disk takes the attributes of the old one that the kind gives a disk (GPFS: usage and storage pool), not
	// what was found out about the old one (the id of its OSD)
	_, diskAttrs := backend.InitialAttrs(nodes, disks, []*StorageNodePlan{planNode}, []*StorageDiskPlan{newDisk})
	attrs := diskAttrs[storageDiskKey(newDisk.Hostid, newDisk.DiskID)]
	if attrs == nil {
		attrs = map[string]interface{}{}
	}
	oldAttrs := map[string]interface{}{}
	_ = json.Unmarshal([]byte(old.Attrs), &oldAttrs)
	for k := range attrs {
		if v, ok := oldAttrs[k]; ok {
			attrs[k] = v
		}
	}
	id := diskIdentity(hd, newDisk.Wipe)
	record := &model.StorageClusterDisk{ClusterID: cluster.ID, Hostid: newDisk.Hostid, DiskID: newDisk.DiskID, Serial: id.Serial, WWN: id.WWN,
		SizeBytes: hd.SizeBytes, Role: backend.DiskRole(), Media: newDisk.Media, Attrs: jsonAttrs(attrs), Status: model.StorageDiskClaiming}
	old.Status = model.StorageDiskRemoving
	steps, err := backend.TaskPlan(StorageTaskReplaceDisk, cluster, &StorageTaskScope{Nodes: nodes, Disks: append(append([]*model.StorageClusterDisk{}, disks...), record)})
	if err != nil {
		return nil, err
	}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskReplaceDisk, Plan: steps,
		Params: map[string]interface{}{"disk_id": old.ID, "damaged": true, "wipe": wipeList([]*StorageDiskPlan{newDisk})},
		Prepare: func(tx *gorm.DB) (int64, error) {
			if err := tx.Create(record).Error; err != nil {
				return 0, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s of host %d is claimed already", record.DiskID, record.Hostid), err)
			}
			if err := tx.Model(&model.StorageClusterDisk{}).Where("id = ?", old.ID).Update("status", model.StorageDiskRemoving).Error; err != nil {
				return 0, err
			}
			return cluster.ID, storageRefreshReserve(tx, cluster)
		}})
}

// storageReplaceFinish: the failed disk's claim goes and the new disk is active (done), or the new disk is marked
// failed and the old one too, as nothing tells how far the replacement went (aborted)
func storageReplaceFinish(expandFinish storageTaskFinish) storageTaskFinish {
	return func(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
		// The new disk first: its NSD name is counted with the failed disk still there, as when it was made
		if err := expandFinish(ctx, tx, task, succeeded); err != nil {
			return err
		}
		return gpfsRemoveDiskFinish(ctx, tx, task, succeeded)
	}
}

// storageReplaceReleaseInput: the failed disk is given back without wiping it (it can not be read), and the kind is
// told the disks the cluster keeps on the host
func storageReplaceReleaseInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	in, err := gpfsReleaseDisksInput(ctx, db, task, step, hostid)
	if err != nil {
		return nil, err
	}
	m := in.(map[string]interface{})
	m["no_wipe"] = true
	return m, nil
}

// ChangeRoles changes the roles of a member of a managed cluster (§13.1): GPFS quorum (with manager) and admin hosts
// (mmchnode), Ceph mon, mgr and admin placement (the cephadm labels). The disk role follows the disks of the host and
// is not changed here. Checked like a new cluster (§6.3)
func (a *StorageClusterAdmin) ChangeRoles(ctx context.Context, uuid string, hostid int32, roles []string) (*model.StorageTask, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	cluster, nodes, disks, backend, err := storageClusterForChange(ctx, uuid, func(c *StorageCapabilities) bool { return c.ChangeRoles })
	if err != nil {
		return nil, err
	}
	db := dbs.DBContext(ctx)
	var node *model.StorageClusterNode
	for _, n := range nodes {
		if n.Hostid == hostid {
			node = n
		}
	}
	if node == nil {
		return nil, planError("%s is not in the cluster", hostName(db, hostid))
	}
	if node.Status != model.StorageNodeActive {
		return nil, planError("%s is %s: only an active member changes its roles", hostName(db, hostid), node.Status)
	}
	if _, online := hostOnline(db, hostid); !online {
		return nil, planError("%s is offline", hostName(db, hostid))
	}
	known := backend.Roles()
	want := []string{}
	for _, r := range roles {
		if !hasRole(known, r) {
			return nil, planError("Unknown role %q for a %s cluster", r, cluster.Kind)
		}
		if !hasRole(want, r) {
			want = append(want, r)
		}
	}
	// The disk role follows the disks of the host; a host with daemon roles is no mere client
	planned := &StorageNodePlan{Hostid: hostid, Roles: want}
	afterNodes, afterDisks := layoutAfter(nodes, disks, nil, nil, -1, 0)
	for i, n := range afterNodes {
		if n.Hostid == hostid {
			afterNodes[i] = planned
		}
	}
	withDiskRole(afterNodes, afterDisks, backend.DiskRole())
	if len(planned.Roles) > 1 || storageDaemonRoles(planned.Roles) {
		without := []string{}
		for _, r := range planned.Roles {
			if r != model.StorageRoleClient {
				without = append(without, r)
			}
		}
		if len(without) > 0 {
			planned.Roles = without
		}
	}
	if len(planned.Roles) == 0 {
		planned.Roles = []string{model.StorageRoleClient}
	}
	old := strings.Split(node.Roles, ",")
	if sameRoles(old, planned.Roles) {
		return nil, planError("The roles of %s do not change", hostName(db, hostid))
	}
	params, err := backend.ParseParams([]byte(cluster.Params))
	if err != nil {
		return nil, err
	}
	if err = backend.CheckLayout(afterNodes, afterDisks, params); err != nil {
		return nil, err
	}
	// Whether the host may hold its new roles while it is in another cluster of the kind (§4.1)
	why, err := storageHostConflict(db, backend, cluster.Kind, cluster.ID, hostid, planned.Roles)
	if err != nil {
		return nil, err
	}
	if why != "" {
		return nil, planError("%s %s", hostName(db, hostid), why)
	}
	disksOn := 0
	for _, d := range disks {
		if d.Hostid == hostid {
			disksOn++
		}
	}
	newRoles := strings.Join(planned.Roles, ",")
	reserve := backend.ReserveMB(planned.Roles, disksOn, params)
	oldReserve := node.ReservedMemMB
	node.Roles = newRoles
	node.ReservedMemMB = reserve
	steps, err := backend.TaskPlan(StorageTaskChangeRoles, cluster, &StorageTaskScope{Nodes: nodes, Disks: disks, Changed: hostid, ChangedFrom: old})
	if err != nil {
		return nil, err
	}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskChangeRoles, Plan: steps,
		Params: map[string]interface{}{"hostid": hostid, "old_roles": strings.Join(old, ","), "old_reserve_mb": oldReserve,
			"roles": newRoles},
		Prepare: func(tx *gorm.DB) (int64, error) {
			return cluster.ID, tx.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND hostid = ?", cluster.ID, hostid).
				Updates(map[string]interface{}{"roles": newRoles, "reserved_mem_mb": reserve}).Error
		}})
}

func sameRoles(a, b []string) bool {
	x := append([]string{}, a...)
	y := append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, ",") == strings.Join(y, ",")
}

// storageChangeRolesFinish: the new roles stay (done), or the old ones come back (aborted: the change may have been
// made in part, the next change or the health check shows it)
func storageChangeRolesFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	if succeeded {
		return nil
	}
	p := map[string]interface{}{}
	_ = json.Unmarshal([]byte(task.Params), &p)
	roles, _ := p["old_roles"].(string)
	if roles == "" {
		return nil
	}
	update := map[string]interface{}{"roles": roles, "reason": "the change of roles was aborted"}
	if mb, ok := p["old_reserve_mb"].(float64); ok {
		update["reserved_mem_mb"] = int32(mb)
	}
	return tx.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND hostid = ?", task.ClusterID, storageTaskInt(task, "hostid")).
		Updates(update).Error
}

// changeRolesAdmins are the admin hosts a change of roles runs its cluster commands on: not the host that changes,
// which may not hold the admin key yet (Ceph hands the admin keyring to a host some time after it gets _admin) or
// may lose it while the step runs; that host only when it is the one admin before and after
func changeRolesAdmins(admins []int32, scope *StorageTaskScope) []int32 {
	out := []int32{}
	for _, h := range admins {
		if h != scope.Changed {
			out = append(out, h)
		}
	}
	if len(out) == 0 && hasRole(scope.ChangedFrom, model.StorageRoleAdmin) {
		for _, h := range admins {
			if h == scope.Changed {
				out = append(out, h)
			}
		}
	}
	return out
}

// storageRolesChanged tells which roles a change of roles adds and takes away
func storageRolesChanged(task *model.StorageTask) (added, removed []string) {
	p := map[string]interface{}{}
	_ = json.Unmarshal([]byte(task.Params), &p)
	oldRoles, _ := p["old_roles"].(string)
	newRoles, _ := p["roles"].(string)
	o, n := strings.Split(oldRoles, ","), strings.Split(newRoles, ",")
	for _, r := range n {
		if r != "" && !hasRole(o, r) {
			added = append(added, r)
		}
	}
	for _, r := range o {
		if r != "" && !hasRole(n, r) {
			removed = append(removed, r)
		}
	}
	return
}
