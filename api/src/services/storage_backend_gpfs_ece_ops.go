/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Changing a GPFS cluster in the erasure code layout (shared-storage-design.md §7.9): servers join a scale-out
// recovery group with their disks (mmvdisk recoverygroup add, which rebalances the stripes over them and then formats
// their log groups and extends the vdisk sets), leave it (the file systems and the group give up their share, the data
// moves to the other servers), every server gets new disks at once (recoverygroup resize, and a vdisk set for the new
// capacity), and a failed pdisk is replaced by a new disk of its server. Clients, roles, keys and the upgrade go the
// way of the replica layout, the recovery group servers suspended one at a time in it.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"api/src/model"

	"gorm.io/gorm"
)

// gpfsECEChangeSteps are the steps of the erasure code layout in the change tasks of gpfs, merged into them when they
// are registered (storage_backend_gpfs_ops.go)
var gpfsECEChangeSteps = map[string]map[string]*storageStepDef{
	StorageTaskAddNodes: {
		// The disk expression comes from the resolve_disks step right before: a retry starts there
		"ece_add_servers": {Script: "gpfs_ece.sh", Input: gpfsECEAddServersInput, RetryFrom: "resolve_disks"},
	},
	StorageTaskAddDisks: {
		"ece_resize": {Script: "gpfs_ece.sh", Input: gpfsECEResizeInput, RetryFrom: "resolve_disks"},
	},
	StorageTaskRemoveNode: {
		"ece_remove_server": {Script: "gpfs_ece.sh", Input: gpfsECERemoveServerInput},
	},
	StorageTaskReplaceDisk: {
		"ece_replace": {Script: "gpfs_ece.sh", Input: gpfsECEReplaceInput, RetryFrom: "resolve_disks"},
	},
}

// gpfsECEChangeTaskPlan: the change plans of an erasure code cluster; what does not touch the recovery group (roles,
// keys, the upgrade) is planned as for the replica layout
func gpfsECEChangeTaskPlan(task string, scope *StorageTaskScope) ([]*StorageStepPlan, error) {
	nodes := scope.Nodes
	admins := gpfsWorkingAdmins(nodes)
	if len(admins) == 0 {
		return nil, planError("No admin host of the cluster is left to run the change")
	}
	step := func(name, s string, hosts []int32, timeout time.Duration) *StorageStepPlan {
		return &StorageStepPlan{Name: name, Scope: s, Hostids: hosts, Timeout: timeout}
	}
	n, a := model.StorageStepScopeNodes, model.StorageStepScopeAdmin
	// Every server resolves its disks when the disk expression of the whole group is needed
	servers := []int32{}
	for _, node := range nodes {
		if node.HasRole(model.StorageRoleNSD) && node.Status != model.StorageNodeLeaving {
			servers = append(servers, node.Hostid)
		}
	}
	switch task {
	case StorageTaskAddNodes:
		newHosts := gpfsHostsBy(nodes, model.StorageNodeJoining, "")
		if len(newHosts) == 0 {
			return nil, planError("No host joins the cluster")
		}
		plan := []*StorageStepPlan{
			step("precheck", n, newHosts, 10*time.Minute),
			step("join", n, newHosts, 10*time.Minute),
			step("fetch_package", n, newHosts, 30*time.Minute),
			step("install", n, newHosts, 30*time.Minute),
			step("build_gpl", n, newHosts, 15*time.Minute),
			step("ssh_trust", n, gpfsHostsBy(nodes, "", ""), 5*time.Minute),
			step("add_node", a, admins, 30*time.Minute),
		}
		if len(gpfsHostsBy(nodes, model.StorageNodeJoining, model.StorageRoleNSD)) > 0 {
			// The rebalance over the new servers runs before their log groups are made: hours to days
			plan = append(plan,
				step("resolve_disks", n, servers, 5*time.Minute),
				step("ece_add_servers", a, admins, 7*24*time.Hour))
		}
		return append(plan, step("finish", n, newHosts, 5*time.Minute)), nil
	case StorageTaskAddDisks:
		if len(gpfsDiskHosts(scope.Disks, model.StorageDiskClaiming)) == 0 {
			return nil, planError("No disk joins the cluster")
		}
		return []*StorageStepPlan{
			step("resolve_disks", n, servers, 5*time.Minute),
			step("ece_resize", a, admins, 7*24*time.Hour),
		}, nil
	case StorageTaskRemoveNode:
		leaving := gpfsHostsBy(nodes, model.StorageNodeLeaving, "")
		if len(leaving) != 1 {
			return nil, planError("One host leaves at a time")
		}
		plan := []*StorageStepPlan{}
		if len(gpfsHostsBy(nodes, model.StorageNodeLeaving, model.StorageRoleNSD)) > 0 {
			// Its data moves to the other servers first
			plan = append(plan, step("ece_remove_server", a, admins, 7*24*time.Hour))
		}
		plan = append(plan, step("remove_node", a, admins, 30*time.Minute))
		if !scope.Offline {
			plan = append(plan, step("leave", n, leaving, 30*time.Minute))
		}
		return plan, nil
	case StorageTaskReplaceDisk:
		newHost := gpfsDiskHosts(scope.Disks, model.StorageDiskClaiming)
		old := gpfsDiskHosts(scope.Disks, model.StorageDiskRemoving)
		if len(newHost) != 1 || len(old) != 1 {
			return nil, planError("One disk replaces one disk")
		}
		if newHost[0] != old[0] {
			return nil, planError("A pdisk is replaced by a disk of its own server")
		}
		return []*StorageStepPlan{
			step("resolve_disks", n, newHost, 5*time.Minute),
			step("ece_replace", a, admins, 2*time.Hour),
			step("release_disks", n, old, 10*time.Minute),
		}, nil
	case StorageTaskRemoveDisk:
		return nil, planError("A disk leaves a recovery group only with its server, or replaced by a new disk")
	case StorageTaskRebalance:
		return nil, planError("A recovery group balances its stripes itself")
	}
	return gpfsChangeTaskPlan(task, scope)
}

// gpfsECEDiskExprOf is the disk list of mmvdisk for some servers: their disks in the cluster (not those on their way
// out) by kernel name, "<ip>:sdb,sdc;...", with the addresses of the servers in the same order (by hostid)
func gpfsECEDiskExprOf(db *gorm.DB, task *model.StorageTask, servers []int32, disks []*model.StorageClusterDisk) (string, []string, error) {
	names, err := gpfsResolvedNames(db, task)
	if err != nil {
		return "", nil, err
	}
	ips, err := storageHostIPs(db, servers)
	if err != nil {
		return "", nil, err
	}
	sorted := append([]int32{}, servers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	parts, addrs := []string{}, []string{}
	for _, h := range sorted {
		list := []string{}
		for _, d := range disks {
			if d.Hostid != h || d.Status == model.StorageDiskRemoving || d.Status == model.StorageDiskFailed {
				continue
			}
			name := names[h][d.DiskID]
			if name == "" {
				return "", nil, fmt.Errorf("disk %s of %s was not resolved", d.DiskID, hostName(db, h))
			}
			list = append(list, name)
		}
		if len(list) == 0 {
			return "", nil, fmt.Errorf("%s has no disks for the recovery group", hostName(db, h))
		}
		sort.Strings(list)
		parts = append(parts, ips[h]+":"+strings.Join(list, ","))
		addrs = append(addrs, ips[h])
	}
	return strings.Join(parts, ";"), addrs, nil
}

// gpfsECEServers are the recovery group servers of a cluster as a change leaves them, and those joining with it
func gpfsECEServers(nodes []*model.StorageClusterNode) (all, joining []int32) {
	for _, n := range nodes {
		if !n.HasRole(model.StorageRoleNSD) || n.Status == model.StorageNodeLeaving {
			continue
		}
		all = append(all, n.Hostid)
		if n.Status == model.StorageNodeJoining {
			joining = append(joining, n.Hostid)
		}
	}
	return all, joining
}

// gpfsECESlotInput: what the slot step needs of the parameters of a cluster
func gpfsECESlotInput(p *gpfsParams, in map[string]interface{}) map[string]interface{} {
	in["no_slot_map"] = p.NoSlotMap
	if p.SlotMode != "" {
		in["slot_mode"], in["slot_range"] = p.SlotMode, p.SlotRange
	}
	return in
}

func gpfsECEAddServersInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	all, joining := gpfsECEServers(nodes)
	full, _, err := gpfsECEDiskExprOf(db, task, all, disks)
	if err != nil {
		return nil, err
	}
	expr, ips, err := gpfsECEDiskExprOf(db, task, joining, disks)
	if err != nil {
		return nil, err
	}
	nc, rg, _ := gpfsECENames(cluster)
	return gpfsECESlotInput(gpfsParamsOf(cluster), map[string]interface{}{"action": "add_servers", "cluster_uuid": cluster.UUID,
		"node_class": nc, "recovery_group": rg, "servers": ips, "disk_expr": expr, "full_expr": full, "total_servers": len(all)}), nil
}

func gpfsECERemoveServerInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	leaving := int32(storageTaskInt(task, "hostid"))
	ips, err := storageHostIPs(db, []int32{leaving})
	if err != nil {
		return nil, err
	}
	nc, rg, _ := gpfsECENames(cluster)
	return map[string]interface{}{"action": "remove_server", "cluster_uuid": cluster.UUID, "node_class": nc, "recovery_group": rg,
		"ip": ips[leaving]}, nil
}

// gpfsECEReplaceInput: the pdisk of the failed disk, and the new disk of the same server by its kernel name as the
// resolve step found it; with a slot map mmvdisk finds the new disk in the slot of the old one itself
func gpfsECEReplaceInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	var old, fresh *model.StorageClusterDisk
	for _, d := range disks {
		switch d.Status {
		case model.StorageDiskRemoving:
			old = d
		case model.StorageDiskClaiming:
			fresh = d
		}
	}
	if old == nil || fresh == nil {
		return nil, fmt.Errorf("the replacement has no failed disk or no new disk")
	}
	if old.Name == "" {
		return nil, fmt.Errorf("disk %s has no pdisk name", old.DiskID)
	}
	names, err := gpfsResolvedNames(db, task)
	if err != nil {
		return nil, err
	}
	device := names[fresh.Hostid][fresh.DiskID]
	if device == "" {
		return nil, fmt.Errorf("disk %s was not resolved", fresh.DiskID)
	}
	ips, err := storageHostIPs(db, []int32{fresh.Hostid})
	if err != nil {
		return nil, err
	}
	_, rg, _ := gpfsECENames(cluster)
	return map[string]interface{}{"action": "replace", "cluster_uuid": cluster.UUID, "recovery_group": rg, "pdisk": old.Name,
		"ip": ips[fresh.Hostid], "device": device, "wwn": fresh.WWN, "slot_map": !gpfsParamsOf(cluster).NoSlotMap}, nil
}

// gpfsECESetsOf are the vdisk sets of a cluster as recorded: the data sets in the order they were made, then the
// metadata set of a mixed media cluster
func gpfsECESetsOf(cluster *model.StorageCluster) []string {
	attrs := struct {
		VdiskSets []string `json:"vdisk_sets"`
		VdiskSet  string   `json:"vdisk_set"`
	}{}
	_ = json.Unmarshal([]byte(cluster.Attrs), &attrs)
	if len(attrs.VdiskSets) > 0 {
		return attrs.VdiskSets
	}
	if attrs.VdiskSet != "" {
		return []string{attrs.VdiskSet}
	}
	return nil
}

// gpfsECENextSet names the vdisk set the capacity of new disks goes to: cl<id>_vs<n>, n one more than the sets so far
func gpfsECENextSet(cluster *model.StorageCluster) string {
	return fmt.Sprintf("cl%d_vs%d", cluster.ID, len(gpfsECESetsOf(cluster))+1)
}

// gpfsECESetFor: the vdisk set new capacity of a media takes: the code, block size and share of the metadata set on the
// solid state array of a mixed media cluster, else of the data set
func gpfsECESetFor(p *gpfsParams, media string, arraysData, arraysMeta string) map[string]interface{} {
	set := map[string]interface{}{"code": p.ECECode, "block_size": p.BlockSize, "set_size_pct": p.ECESetSize}
	if arraysMeta == "" {
		return set
	}
	if gpfsECEMedia(media) == arraysMeta {
		code, bs, pct := gpfsECEMeta(p)
		return map[string]interface{}{"code": code, "block_size": bs, "set_size_pct": pct, "da_type": arraysMeta,
			"nsd_usage": "metadataOnly", "storage_pool": "system"}
	}
	set["da_type"], set["nsd_usage"], set["storage_pool"] = arraysData, "dataOnly", "data"
	return set
}

func gpfsECEResizeInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	all, _ := gpfsECEServers(nodes)
	expr, _, err := gpfsECEDiskExprOf(db, task, all, disks)
	if err != nil {
		return nil, err
	}
	media := []string{}
	newMedia := map[string]bool{}
	for _, d := range disks {
		media = append(media, d.Media)
		if d.Status == model.StorageDiskClaiming {
			newMedia[gpfsECEMedia(d.Media)] = true
		}
	}
	if len(newMedia) != 1 {
		return nil, fmt.Errorf("the new disks of a recovery group are of one media at a time")
	}
	data, meta, err := gpfsECEArrays(media)
	if err != nil {
		return nil, err
	}
	var m string
	for k := range newMedia {
		m = k
	}
	_, rg, _ := gpfsECENames(cluster)
	in := gpfsECESetFor(gpfsParamsOf(cluster), m, data, meta)
	in["action"], in["cluster_uuid"], in["recovery_group"], in["disk_expr"] = "resize", cluster.UUID, rg, expr
	in["vdisk_set"], in["fs_name"] = gpfsECENextSet(cluster), gpfsECEFsName(cluster)
	return in, nil
}

// gpfsECERecordNewSet records the vdisk set a resize made in the attributes of the cluster
func gpfsECERecordNewSet(tx *gorm.DB, task *model.StorageTask, cluster *model.StorageCluster) error {
	res, err := storageStepResults(tx, task.ID, "ece_resize")
	if err != nil || len(res) == 0 {
		return nil
	}
	attrs := map[string]interface{}{}
	_ = json.Unmarshal([]byte(cluster.Attrs), &attrs)
	sets := gpfsECESetsOf(cluster)
	next := gpfsECENextSet(cluster)
	for _, s := range sets {
		if s == next {
			return nil
		}
	}
	attrs["vdisk_sets"] = append(sets, next)
	return tx.Model(&model.StorageCluster{}).Where("id = ?", cluster.ID).Update("attrs", jsonAttrs(attrs)).Error
}

// gpfsECEChangePdisks are the pdisks the step of an erasure code change reported last (add_servers, resize, replace)
func gpfsECEChangePdisks(tx *gorm.DB, task *model.StorageTask) ([]*gpfsECEPdisk, error) {
	for _, name := range []string{"ece_add_servers", "ece_resize", "ece_replace"} {
		res, err := storageStepResults(tx, task.ID, name)
		if err != nil || len(res) == 0 {
			continue
		}
		pdisks := []*gpfsECEPdisk{}
		for _, raw := range res {
			r := struct {
				Pdisks []*gpfsECEPdisk `json:"pdisks"`
			}{}
			_ = json.Unmarshal(raw, &r)
			pdisks = append(pdisks, r.Pdisks...)
		}
		return pdisks, nil
	}
	return nil, fmt.Errorf("no step of the change reported the pdisks")
}

// gpfsECEExpandNames names the disks a change brought into the recovery group by their pdisks, and gives the file
// system they went into; a replaced pdisk keeps its name, so the new disk takes the name of the failed one
func gpfsECEExpandNames(tx *gorm.DB, task *model.StorageTask, cluster *model.StorageCluster, nodes []*model.StorageClusterNode,
	fresh []*model.StorageClusterDisk) (map[int64]string, *model.StorageFilesystem, error) {
	fs, err := gpfsClusterFs(tx, cluster)
	if err != nil {
		return nil, nil, err
	}
	if len(fresh) == 0 {
		return map[int64]string{}, fs, nil
	}
	pdisks, err := gpfsECEChangePdisks(tx, task)
	if err != nil {
		return nil, nil, err
	}
	// The pdisk being drained under a temporary name (n001p005#0010) is the old disk, never the new one
	working := []*gpfsECEPdisk{}
	for _, pd := range pdisks {
		if !strings.Contains(pd.Name, "#") {
			working = append(working, pd)
		}
	}
	names, err := gpfsResolvedNames(tx, task)
	if err != nil {
		return nil, nil, err
	}
	ips, err := storageHostIPs(tx, nodeHostids(nodes, ""))
	if err != nil {
		return nil, nil, err
	}
	pdiskOf, err := gpfsECEPdiskNames(fresh, working, ips, names)
	return pdiskOf, fs, err
}
