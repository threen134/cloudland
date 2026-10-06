/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Changing a GPFS cluster (shared-storage-design.md §7.5): hosts join with the steps of a deployment and are added
// with mmaddnode, disks become NSDs and go into the file system with mmadddisk, a disk leaves with mmdeldisk (its
// data moves first) and is wiped, a host leaves with mmdelnode and is cleaned like a deleted cluster's. Inputs are
// built from the database when a step starts; what a task brings or takes away is marked by status (joining,
// claiming; leaving, removing).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

// gpfsHostsBy lists the hosts of a scope with a status (empty: any), and with a role (empty: any)
func gpfsHostsBy(nodes []*model.StorageClusterNode, status, role string) []int32 {
	ids := []int32{}
	for _, n := range nodes {
		if (status == "" || n.Status == status) && (role == "" || n.HasRole(role)) {
			ids = append(ids, n.Hostid)
		}
	}
	return ids
}

// gpfsDiskHosts lists the hosts that have disks with a status
func gpfsDiskHosts(disks []*model.StorageClusterDisk, status string) []int32 {
	ids := []int32{}
	seen := map[int32]bool{}
	for _, d := range disks {
		if d.Status == status && !seen[d.Hostid] {
			seen[d.Hostid] = true
			ids = append(ids, d.Hostid)
		}
	}
	return ids
}

// gpfsWorkingAdmins are the admin hosts that can run a cluster command now: members already, not leaving
func gpfsWorkingAdmins(nodes []*model.StorageClusterNode) []int32 {
	ids := []int32{}
	for _, n := range nodes {
		if n.HasRole(model.StorageRoleAdmin) && n.Status != model.StorageNodeJoining && n.Status != model.StorageNodeLeaving {
			ids = append(ids, n.Hostid)
		}
	}
	return ids
}

func gpfsChangeTaskPlan(task string, scope *StorageTaskScope) ([]*StorageStepPlan, error) {
	nodes := scope.Nodes
	admins := gpfsWorkingAdmins(nodes)
	if len(admins) == 0 {
		return nil, planError("No admin host of the cluster is left to run the change")
	}
	step := func(name, s string, hosts []int32, timeout time.Duration) *StorageStepPlan {
		return &StorageStepPlan{Name: name, Scope: s, Hostids: hosts, Timeout: timeout}
	}
	n, a := model.StorageStepScopeNodes, model.StorageStepScopeAdmin
	newHosts := gpfsHostsBy(nodes, model.StorageNodeJoining, "")
	diskHosts := gpfsDiskHosts(scope.Disks, model.StorageDiskClaiming)
	addDisks := func() []*StorageStepPlan {
		if len(diskHosts) == 0 {
			return nil
		}
		return []*StorageStepPlan{
			step("resolve_disks", n, diskHosts, 5*time.Minute),
			step("create_nsd", a, admins, 15*time.Minute),
			step("add_disks", a, admins, 60*time.Minute),
		}
	}
	plan := []*StorageStepPlan{}
	switch task {
	case StorageTaskAddNodes:
		if len(newHosts) == 0 {
			return nil, planError("No host joins the cluster")
		}
		plan = append(plan,
			step("precheck", n, newHosts, 10*time.Minute),
			step("join", n, newHosts, 10*time.Minute),
			step("fetch_package", n, newHosts, 30*time.Minute),
			step("install", n, newHosts, 30*time.Minute),
			step("build_gpl", n, newHosts, 15*time.Minute),
			// Every member: the admin hosts learn the new host keys, the new hosts let the admin hosts in
			step("ssh_trust", n, gpfsHostsBy(nodes, "", ""), 5*time.Minute),
			step("add_node", a, admins, 30*time.Minute))
		plan = append(plan, addDisks()...)
		plan = append(plan, step("finish", n, newHosts, 5*time.Minute))
	case StorageTaskAddDisks:
		if len(diskHosts) == 0 {
			return nil, planError("No disk joins the cluster")
		}
		plan = append(plan, addDisks()...)
	case StorageTaskRemoveDisk:
		hosts := gpfsDiskHosts(scope.Disks, model.StorageDiskRemoving)
		if len(hosts) == 0 {
			return nil, planError("No disk leaves the cluster")
		}
		plan = append(plan,
			step("remove_disks", a, admins, 24*time.Hour),
			step("release_disks", n, hosts, 30*time.Minute))
	case StorageTaskRemoveNode:
		leaving := gpfsHostsBy(nodes, model.StorageNodeLeaving, "")
		if len(leaving) != 1 {
			return nil, planError("One host leaves at a time")
		}
		if len(gpfsDiskHosts(scope.Disks, model.StorageDiskRemoving)) > 0 {
			plan = append(plan, step("remove_disks", a, admins, 24*time.Hour))
		}
		plan = append(plan, step("remove_node", a, admins, 30*time.Minute))
		if !scope.Offline {
			plan = append(plan, step("leave", n, leaving, 30*time.Minute))
		}
	case StorageTaskRebalance:
		plan = append(plan, step("rebalance", a, admins, 7*24*time.Hour))
	case StorageTaskReplaceDisk:
		// The new NSD first, so a disk that is no good stops the task before the failed one goes; then the failed NSD
		// is dropped (-p: it can not be read), the new one added and the replication restored
		old := gpfsDiskHosts(scope.Disks, model.StorageDiskRemoving)
		if len(diskHosts) != 1 || len(old) != 1 {
			return nil, planError("One disk replaces one disk")
		}
		plan = append(plan,
			step("resolve_disks", n, diskHosts, 5*time.Minute),
			step("create_nsd", a, admins, 15*time.Minute),
			step("remove_disks", a, admins, 60*time.Minute),
			step("add_disks", a, admins, 60*time.Minute),
			step("restore", a, admins, 7*24*time.Hour),
			step("release_disks", n, old, 10*time.Minute))
	case StorageTaskChangeRoles:
		// The admin hosts get the cluster key and the host keys (ssh_trust on every member), then the quorum changes
		run := changeRolesAdmins(admins, scope)
		if len(run) == 0 {
			return nil, planError("No admin host besides the one that changes is left to run the change")
		}
		plan = append(plan,
			step("ssh_trust", n, gpfsHostsBy(nodes, "", ""), 5*time.Minute),
			step("change_roles", a, run, 30*time.Minute),
			step("finish", n, gpfsChangedHost(scope), 5*time.Minute))
	case StorageTaskUpgrade:
		return gpfsUpgradePlan(scope, admins)
	case StorageTaskRotateKeys:
		// GPFS has no client key: the SSH key its admin commands log in with (§6.6)
		if !scope.RotateSSH {
			return nil, planError("GPFS clusters have no client key")
		}
		plan = storageRotateSSHSteps(gpfsHostsBy(nodes, "", ""))
	default:
		return nil, planError("GPFS clusters have no task %s", task)
	}
	return plan, nil
}

func init() {
	expand := func() map[string]*storageStepDef {
		return map[string]*storageStepDef{
			"precheck":      gpfsPrecheckStep,
			"join":          {Script: "stc_join.sh", Input: gpfsJoinInput},
			"fetch_package": {Script: "stc_fetch.sh", Input: gpfsFetchInput},
			"install":       {Script: "gpfs_install.sh", Input: gpfsInstallInput},
			"build_gpl":     {Script: "gpfs_build_gpl.sh"},
			"ssh_trust":     {Script: "stc_ssh_trust.sh", Input: gpfsTrustInput},
			"add_node":      {Script: "gpfs_cluster.sh", Input: gpfsAddNodeInput, RetryFrom: "ssh_trust"},
			"resolve_disks": {Script: "stc_resolve_disks.sh", Input: gpfsResolveInput},
			"create_nsd":    {Script: "gpfs_nsd.sh", Input: gpfsNSDInput, RetryFrom: "resolve_disks"},
			"add_disks":     {Script: "gpfs_fs.sh", Input: gpfsAddDisksInput, RetryFrom: "resolve_disks"},
			"finish":        {Script: "stc_finish.sh", Input: gpfsFinishInput},
		}
	}
	registerStorageTaskKind("gpfs:"+StorageTaskAddNodes, &storageTaskKind{Slot: storageSlotStructural, Steps: expand(), Finish: gpfsExpandFinish})
	registerStorageTaskKind("gpfs:"+StorageTaskAddDisks, &storageTaskKind{Slot: storageSlotStructural, Steps: expand(), Finish: gpfsExpandFinish})
	registerStorageTaskKind("gpfs:"+StorageTaskRemoveDisk, &storageTaskKind{Slot: storageSlotStructural, Finish: gpfsRemoveDiskFinish,
		Steps: map[string]*storageStepDef{
			"remove_disks":  {Script: "gpfs_fs.sh", Input: gpfsRemoveDisksInput},
			"release_disks": {Script: "stc_release_disks.sh", Input: gpfsReleaseDisksInput},
		}})
	registerStorageTaskKind("gpfs:"+StorageTaskRemoveNode, &storageTaskKind{Slot: storageSlotStructural, Finish: gpfsRemoveNodeFinish,
		Steps: map[string]*storageStepDef{
			"remove_disks": {Script: "gpfs_fs.sh", Input: gpfsRemoveDisksInput},
			"remove_node":  {Script: "gpfs_cluster.sh", Input: gpfsRemoveNodeInput},
			"leave":        {Script: "stc_leave.sh", Input: gpfsLeaveInput},
		}})
	registerStorageTaskKind("gpfs:"+StorageTaskRebalance, &storageTaskKind{Slot: storageSlotStructural,
		Steps: map[string]*storageStepDef{"rebalance": {Script: "gpfs_fs.sh", Input: gpfsRebalanceInput}}})
	replace := expand()
	replace["remove_disks"] = &storageStepDef{Script: "gpfs_fs.sh", Input: gpfsRemoveDisksInput}
	replace["restore"] = &storageStepDef{Script: "gpfs_fs.sh", Input: gpfsRestoreInput}
	replace["release_disks"] = &storageStepDef{Script: "stc_release_disks.sh", Input: storageReplaceReleaseInput}
	registerStorageTaskKind("gpfs:"+StorageTaskReplaceDisk, &storageTaskKind{Slot: storageSlotStructural, Steps: replace,
		Finish: storageReplaceFinish(gpfsExpandFinish)})
	registerStorageTaskKind("gpfs:"+StorageTaskRotateKeys, &storageTaskKind{Slot: storageSlotStructural, Finish: storageRotateFinish,
		Steps: map[string]*storageStepDef{
			"trust_add": {Script: "stc_ssh_trust.sh", Input: storageRotateTrustInput("add", model.StorageRoleAdmin)},
			// The admin commands log in with the key file the switch writes: the record follows there
			"ssh_switch": {Script: "stc_ssh_trust.sh", Input: storageRotateTrustInput("switch", model.StorageRoleAdmin), Done: storageRotateRecordKey},
			"trust_drop": {Script: "stc_ssh_trust.sh", Input: storageRotateTrustInput("drop", model.StorageRoleAdmin)},
		}})
	registerStorageTaskKind("gpfs:"+StorageTaskChangeRoles, &storageTaskKind{Slot: storageSlotStructural, Finish: storageChangeRolesFinish,
		Steps: map[string]*storageStepDef{
			"ssh_trust":    {Script: "stc_ssh_trust.sh", Input: gpfsTrustInput},
			"change_roles": {Script: "gpfs_cluster.sh", Input: gpfsChangeRolesInput, RetryFrom: "ssh_trust"},
			"finish":       {Script: "stc_finish.sh", Input: gpfsFinishInput},
		}})
}

// gpfsAddNodeInput: the hosts joining, with their designation and license; mmaddnode skips the ones in already
func gpfsAddNodeInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	joining := []*model.StorageClusterNode{}
	ids := []int32{}
	for _, n := range nodes {
		if n.Status == model.StorageNodeJoining {
			joining = append(joining, n)
			ids = append(ids, n.Hostid)
		}
	}
	if len(joining) == 0 {
		return nil, fmt.Errorf("no host joins the cluster")
	}
	ips, err := storageHostIPs(db, ids)
	if err != nil {
		return nil, err
	}
	members := []map[string]interface{}{}
	servers, clients := []string{}, []string{}
	for _, n := range joining {
		quorum := n.HasRole(model.StorageRoleQuorum)
		members = append(members, map[string]interface{}{"ip": ips[n.Hostid], "quorum": quorum, "manager": quorum})
		if quorum || n.HasRole(model.StorageRoleNSD) || n.HasRole(model.StorageRoleAdmin) {
			servers = append(servers, ips[n.Hostid])
		} else {
			clients = append(clients, ips[n.Hostid])
		}
	}
	return map[string]interface{}{"action": "add", "cluster_uuid": cluster.UUID, "cluster_name": cluster.Name, "nodes": members,
		"server_license": servers, "client_license": clients}, nil
}

// gpfsClusterFs is the file system the disks of a change go into: the first of the cluster
func gpfsClusterFs(db *gorm.DB, cluster *model.StorageCluster) (*model.StorageFilesystem, error) {
	fs := &model.StorageFilesystem{}
	if err := db.Where("cluster_id = ?", cluster.ID).Order("id").Take(fs).Error; err != nil {
		return nil, fmt.Errorf("storage cluster %s has no file system", cluster.Name)
	}
	return fs, nil
}

// gpfsDiskStanzas describes disks for mmadddisk / mmcrfs: name, usage, failure group and storage pool of each
func gpfsDiskStanzas(cluster *model.StorageCluster, nodes []*model.StorageClusterNode, all, disks []*model.StorageClusterDisk) []map[string]interface{} {
	names := gpfsNSDNames(cluster, all)
	groups := map[int32]interface{}{}
	for _, n := range nodes {
		groups[n.Hostid] = nodeAttr(n, "failure_group")
	}
	out := []map[string]interface{}{}
	for _, d := range disks {
		out = append(out, map[string]interface{}{"name": names[d.ID], "usage": diskAttr(d, "usage"), "failure_group": groups[d.Hostid],
			"pool": diskAttr(d, "gpfs_pool")})
	}
	return out
}

func gpfsAddDisksInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	fs, err := gpfsClusterFs(db, cluster)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"action": "add", "cluster_uuid": cluster.UUID, "fs_name": fs.Name,
		"nsds": gpfsDiskStanzas(cluster, nodes, disks, gpfsNewDisks(disks))}, nil
}

// gpfsRemoveDisksInput: the NSDs leaving, dropped from the file system (their data moved first, unless the host is
// gone for good) and deleted
func gpfsRemoveDisksInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	fs, err := gpfsClusterFs(db, cluster)
	if err != nil {
		return nil, err
	}
	names := gpfsNSDNames(cluster, disks)
	nsds := []string{}
	for _, d := range disks {
		if d.Status == model.StorageDiskRemoving {
			nsds = append(nsds, names[d.ID])
		}
	}
	if len(nsds) == 0 {
		return nil, fmt.Errorf("no disk leaves the cluster")
	}
	// A replacement drops a disk as failed: the node looks once more that GPFS has it down before it does
	return map[string]interface{}{"action": "remove", "cluster_uuid": cluster.UUID, "fs_name": fs.Name, "nsds": nsds,
		"damaged": storageTaskBool(task, "offline") || storageTaskBool(task, "damaged"), "require_down": storageTaskBool(task, "damaged")}, nil
}

// gpfsReleaseDisksInput: on the host of a disk that left, the disks the cluster keeps there (the nsddevices exit
// lists only those) and the disk to wipe
func gpfsReleaseDisksInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	keep := []string{}
	release := []*storageDiskIdentity{}
	for _, d := range disks {
		if d.Hostid != hostid {
			continue
		}
		if d.Status == model.StorageDiskRemoving {
			release = append(release, storageClusterDiskIdentity(d, true))
		} else {
			keep = append(keep, d.DiskID)
		}
	}
	return map[string]interface{}{"cluster_uuid": cluster.UUID, "kind": cluster.Kind, "keep": keep, "release": release}, nil
}

func gpfsRemoveNodeInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	leaving := int32(storageTaskInt(task, "hostid"))
	ips, err := storageHostIPs(db, []int32{leaving})
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"action": "remove", "cluster_uuid": cluster.UUID, "ip": ips[leaving], "offline": storageTaskBool(task, "offline")}, nil
}

// gpfsRestoreInput: the files that lost a copy with the failed disk get it back (mmrestripefs -r)
func gpfsRestoreInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	fs, err := gpfsClusterFs(db, cluster)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"action": "restore", "cluster_uuid": cluster.UUID, "fs_name": fs.Name}, nil
}

// gpfsChangedHost is the host whose roles a change of roles changes
func gpfsChangedHost(scope *StorageTaskScope) []int32 {
	return []int32{scope.Changed}
}

// gpfsChangeRolesInput: the address of the host and its quorum (manager with it) designation as it will be; a server
// license once it is a quorum, NSD or admin host
func gpfsChangeRolesInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	changed := int32(storageTaskInt(task, "hostid"))
	var node *model.StorageClusterNode
	for _, n := range nodes {
		if n.Hostid == changed {
			node = n
		}
	}
	if node == nil {
		return nil, fmt.Errorf("host %d is not in the cluster", changed)
	}
	ips, err := storageHostIPs(db, []int32{changed})
	if err != nil {
		return nil, err
	}
	quorum := node.HasRole(model.StorageRoleQuorum)
	return map[string]interface{}{"action": "roles", "cluster_uuid": cluster.UUID, "ip": ips[changed], "quorum": quorum,
		"server": quorum || node.HasRole(model.StorageRoleNSD) || node.HasRole(model.StorageRoleAdmin)}, nil
}

func gpfsRebalanceInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	fs := &model.StorageFilesystem{}
	if err := db.Take(fs, storageTaskInt(task, "filesystem_id")).Error; err != nil {
		return nil, fmt.Errorf("the file system of the task is gone")
	}
	return map[string]interface{}{"action": "rebalance", "cluster_uuid": cluster.UUID, "fs_name": fs.Name}, nil
}

// gpfsExpandFinish: the hosts that joined and the disks that went in are active; the new hosts get the pool lists
func gpfsExpandFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	cluster, nodes, disks, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	if !succeeded {
		// Half joined hosts and half made NSDs stay visible: remove them, or retry the change as a new task
		tx.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND status = ?", cluster.ID, model.StorageNodeJoining).
			Updates(map[string]interface{}{"status": model.StorageNodeError, "reason": "the change was aborted"})
		return tx.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND status = ?", cluster.ID, model.StorageDiskClaiming).
			Updates(map[string]interface{}{"status": model.StorageDiskFailed, "reason": "the change was aborted"}).Error
	}
	fs, err := gpfsClusterFs(tx, cluster)
	if err != nil {
		return err
	}
	names := gpfsNSDNames(cluster, disks)
	scan := map[int32]bool{}
	for _, d := range gpfsNewDisks(disks) {
		scan[d.Hostid] = true
		if err := tx.Model(&model.StorageClusterDisk{}).Where("id = ?", d.ID).
			Updates(map[string]interface{}{"status": model.StorageDiskActive, "name": names[d.ID], "fs_id": fs.ID}).Error; err != nil {
			return err
		}
	}
	joined := []*model.StorageClusterNode{}
	for _, n := range nodes {
		if n.Status == model.StorageNodeJoining {
			joined = append(joined, n)
			scan[n.Hostid] = true
		}
	}
	if err := gpfsRememberHosts(tx, task, joined); err != nil {
		return err
	}
	if err := tx.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND status = ?", cluster.ID, model.StorageNodeJoining).
		Update("status", model.StorageNodeActive).Error; err != nil {
		return err
	}
	// A new host sees no pool until its first report; the next round of pool lists reaches it
	pools, err := sharedPoolsOfCluster(tx, cluster.ID)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, n := range joined {
		for _, p := range pools {
			if err := upsertSharedPoolRow(tx, n.Hostid, p, &SharedPoolReport{Pool: p.UUID, Status: model.HyperPoolUnavailable,
				Reason: "joined the cluster: waiting for its first report"}, now); err != nil {
				return err
			}
		}
	}
	hosts := []int32{}
	for h := range scan {
		hosts = append(hosts, h)
	}
	go func(ctx context.Context, ids []int32, sync bool) {
		time.Sleep(3 * time.Second)
		scanStorageHosts(ctx, ids)
		if sync {
			SyncSharedPools(ctx)
		}
	}(context.WithoutCancel(ctx), hosts, len(joined) > 0)
	return nil
}

// gpfsRemoveDiskFinish: the disk is out of the cluster and wiped; its claim goes
func gpfsRemoveDiskFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	disk := &model.StorageClusterDisk{}
	if err := tx.Take(disk, storageTaskInt(task, "disk_id")).Error; err != nil {
		return nil
	}
	if !succeeded {
		// Where the removal stopped is not known: the disk may be out of the file system already
		return tx.Model(&model.StorageClusterDisk{}).Where("id = ?", disk.ID).
			Updates(map[string]interface{}{"status": model.StorageDiskFailed, "reason": "the removal was aborted; remove the disk again"}).Error
	}
	if err := tx.Delete(&model.StorageClusterDisk{}, disk.ID).Error; err != nil {
		return err
	}
	go func(ctx context.Context, id int32) {
		time.Sleep(3 * time.Second)
		scanStorageHosts(ctx, []int32{id})
	}(context.WithoutCancel(ctx), disk.Hostid)
	return nil
}

// gpfsRemoveNodeFinish: the host and its disks are out of the cluster; it has no row of the cluster's pools any more
func gpfsRemoveNodeFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	cluster, _, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	hostid := int32(storageTaskInt(task, "hostid"))
	if !succeeded {
		return tx.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND hostid = ?", cluster.ID, hostid).
			Updates(map[string]interface{}{"status": model.StorageNodeError, "reason": "the removal was aborted; remove the host again"}).Error
	}
	if err := tx.Where("cluster_id = ? AND hostid = ?", cluster.ID, hostid).Delete(&model.StorageClusterDisk{}).Error; err != nil {
		return err
	}
	if err := tx.Where("cluster_id = ? AND hostid = ?", cluster.ID, hostid).Delete(&model.StorageClusterNode{}).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Where("hostid = ? AND pool_id IN (SELECT id FROM storage_pools WHERE cluster_id = ?)", hostid, cluster.ID).
		Delete(&model.HyperStoragePool{}).Error; err != nil {
		return err
	}
	if !storageTaskBool(task, "offline") {
		go func(ctx context.Context, id int32) {
			time.Sleep(3 * time.Second)
			scanStorageHosts(ctx, []int32{id})
		}(context.WithoutCancel(ctx), hostid)
	}
	// Its fences on the cluster go: once this transaction is committed, the node rows are gone
	go func(ctx context.Context, cid int64, id int32) {
		time.Sleep(3 * time.Second)
		liftRemovedHostFences(ctx, cid, id)
	}(context.WithoutCancel(ctx), cluster.ID, hostid)
	return nil
}

// Maintain starts the NSDs of a host that came back (§7.1): an admin host checks, and starts them once every node is
// active again. Imported clusters are their admins' business
func (gpfsBackend) Maintain(ctx context.Context, cluster *model.StorageCluster, nodes []*model.StorageClusterNode) {
	if cluster.Mode != model.StorageModeManaged {
		return
	}
	db := dbs.DBContext(ctx)
	admin := int32(-1)
	for _, h := range gpfsWorkingAdmins(nodes) {
		if _, online := hostOnline(db, h); online {
			admin = h
			break
		}
	}
	if admin < 0 {
		return
	}
	names := []string{}
	for _, f := range StorageClusters.Filesystems(ctx, cluster.ID) {
		names = append(names, f.Name)
	}
	if len(names) == 0 {
		return
	}
	body, _ := json.Marshal(map[string]interface{}{"filesystems": names})
	command := fmt.Sprintf("%s/gpfs_disks_up.sh '%s' <<'EOF'\n%s\nEOF", storageScriptDir, ShellEscape(cluster.UUID), body)
	if err := HyperExecute(ctx, fmt.Sprintf("inter=%d", admin), command); err != nil {
		logger.Ctx(ctx).Warningf("Failed to check the disks of storage cluster %s: %v", cluster.Name, err)
	}
}
