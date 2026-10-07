/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Changing a managed cluster (shared-storage-design.md §7.5, §8.5): adding hosts and disks, removing a disk or a host
// (also a host that is gone for good), rebalancing. What every kind shares lives here: the request checks, the
// records of what joins or leaves, the role rules of the kind applied to the cluster as it will be. The steps come
// from the backend; an operation the kind does not support is refused (StorageBackend.Capabilities).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

const (
	StorageTaskAddNodes   = "add_nodes"
	StorageTaskAddDisks   = "add_disks"
	StorageTaskRemoveDisk = "remove_disk"
	StorageTaskRemoveNode = "remove_node"
	StorageTaskRebalance  = "rebalance"
)

// StorageClusterExpand adds hosts (AddNodes, each with its disks or none) or disks of members (AddDisks)
type StorageClusterExpand struct {
	Nodes            []*StorageNodePlan
	Disks            []*StorageDiskPlan
	AllowUnsupported bool
}

// storageClusterForChange loads a managed, ready cluster with its hosts and disks for an operation of the kind
func storageClusterForChange(ctx context.Context, uuid string, allowed func(*StorageCapabilities) bool) (cluster *model.StorageCluster,
	nodes []*model.StorageClusterNode, disks []*model.StorageClusterDisk, backend StorageBackend, err error) {
	if cluster, nodes, disks, err = StorageClusters.Get(ctx, uuid); err != nil {
		return
	}
	if backend, err = storageBackendOf(cluster.Kind); err != nil {
		return
	}
	if !allowed(storageClusterCapabilities(backend, cluster)) {
		err = planError("%s clusters (%s) do not support this operation yet", cluster.Kind, cluster.Layout)
		return
	}
	if cluster.Mode != model.StorageModeManaged {
		err = planError("Storage cluster %s is imported: CloudLand does not change it", cluster.Name)
		return
	}
	if cluster.Status != model.StorageClusterReady {
		err = NewCLError(ErrStorageClusterBusy, fmt.Sprintf("Storage cluster %s is %s, not ready", cluster.Name, cluster.Status), nil)
		return
	}
	if cluster.ActiveTask != 0 {
		err = NewCLError(ErrStorageClusterBusy, "A task still holds the cluster: wait for it, or retry or abort it", nil)
	}
	return
}

// layoutAfter is the cluster as it will be, as the role rules of the kind read it
func layoutAfter(nodes []*model.StorageClusterNode, disks []*model.StorageClusterDisk, addNodes []*StorageNodePlan, addDisks []*StorageDiskPlan,
	dropHost int32, dropDisk int64) ([]*StorageNodePlan, []*StorageDiskPlan) {
	planNodes := []*StorageNodePlan{}
	planDisks := []*StorageDiskPlan{}
	left := map[int32]int{}
	for _, d := range disks {
		if d.Hostid == dropHost || d.ID == dropDisk {
			continue
		}
		left[d.Hostid]++
		planDisks = append(planDisks, &StorageDiskPlan{Hostid: d.Hostid, DiskID: d.DiskID, Media: d.Media})
	}
	for _, d := range addDisks {
		left[d.Hostid]++
		planDisks = append(planDisks, d)
	}
	for _, n := range nodes {
		if n.Hostid == dropHost {
			continue
		}
		roles := strings.Split(n.Roles, ",")
		planNodes = append(planNodes, &StorageNodePlan{Hostid: n.Hostid, Roles: roles})
	}
	planNodes = append(planNodes, addNodes...)
	return planNodes, planDisks
}

// withDiskRole gives the disk role of the kind to the hosts left without it that have disks in the plan, and takes it
// from the hosts left without disks: what a cluster becomes when disks come and go
func withDiskRole(nodes []*StorageNodePlan, disks []*StorageDiskPlan, role string) {
	if role == "" {
		return
	}
	count := map[int32]int{}
	for _, d := range disks {
		count[d.Hostid]++
	}
	for _, n := range nodes {
		has := hasRole(n.Roles, role)
		switch {
		case count[n.Hostid] > 0 && !has:
			n.Roles = append(n.Roles, role)
		case count[n.Hostid] == 0 && has:
			roles := []string{}
			for _, r := range n.Roles {
				if r != role {
					roles = append(roles, r)
				}
			}
			if len(roles) == 0 {
				roles = []string{model.StorageRoleClient}
			}
			n.Roles = roles
		}
	}
}

// expand records the hosts and disks an expansion brings and starts its task
func (a *StorageClusterAdmin) expand(ctx context.Context, uuid string, task string, req *StorageClusterExpand) (*model.StorageTask, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	cluster, nodes, disks, backend, err := storageClusterForChange(ctx, uuid, func(c *StorageCapabilities) bool {
		if task == StorageTaskAddNodes {
			return c.AddNodes
		}
		return c.AddDisks
	})
	if err != nil {
		return nil, err
	}
	params, err := backend.ParseParams([]byte(cluster.Params))
	if err != nil {
		return nil, err
	}
	members := map[int32]*model.StorageClusterNode{}
	for _, n := range nodes {
		members[n.Hostid] = n
	}
	// The hosts the request touches, as checkStoragePlan reads them: the new ones, or the members getting disks
	planNodes := []*StorageNodePlan{}
	switch task {
	case StorageTaskAddNodes:
		if len(req.Nodes) == 0 {
			return nil, planError("No host to add")
		}
		for _, n := range req.Nodes {
			if members[n.Hostid] != nil {
				return nil, planError("%s is in the cluster already", hostName(dbs.DBContext(ctx), n.Hostid))
			}
			planNodes = append(planNodes, n)
		}
		for _, d := range req.Disks {
			found := false
			for _, n := range req.Nodes {
				found = found || n.Hostid == d.Hostid
			}
			if !found {
				return nil, planError("Disk %s is not on a host being added", d.DiskID)
			}
		}
	case StorageTaskAddDisks:
		if len(req.Disks) == 0 {
			return nil, planError("No disk to add")
		}
		seen := map[int32]bool{}
		for _, d := range req.Disks {
			m := members[d.Hostid]
			if m == nil {
				return nil, planError("Disk %s is on a host that is not in the cluster", d.DiskID)
			}
			if !seen[d.Hostid] {
				seen[d.Hostid] = true
				roles := strings.Split(m.Roles, ",")
				if dr := backend.DiskRole(); dr != "" && !hasRole(roles, dr) {
					roles = append(roles, dr)
				}
				planNodes = append(planNodes, &StorageNodePlan{Hostid: d.Hostid, Roles: roles})
			}
		}
	}
	db := dbs.DBContext(ctx)
	plan := &StoragePlan{Kind: cluster.Kind, Nodes: planNodes, Disks: req.Disks, Params: []byte(cluster.Params),
		AllowUnsupported: req.AllowUnsupported || cluster.Unsupported, ClusterID: cluster.ID}
	_, scanned, err := checkStoragePlan(db, plan)
	if err != nil {
		return nil, err
	}
	for _, d := range req.Disks {
		if d.Media == "" {
			d.Media = scanned[storageDiskKey(d.Hostid, d.DiskID)].Media
		}
	}
	// The role rules of the kind hold for the cluster as it will be
	var addNodes []*StorageNodePlan
	if task == StorageTaskAddNodes {
		addNodes = req.Nodes
	}
	afterNodes, afterDisks := layoutAfter(nodes, disks, addNodes, req.Disks, -1, 0)
	withDiskRole(afterNodes, afterDisks, backend.DiskRole())
	if err = backend.CheckLayout(afterNodes, afterDisks, params); err != nil {
		return nil, err
	}
	nodeAttrs, diskAttrs := backend.InitialAttrs(nodes, disks, planNodes, req.Disks)
	disksOn := map[int32]int{}
	for _, d := range disks {
		disksOn[d.Hostid]++
	}
	for _, d := range req.Disks {
		disksOn[d.Hostid]++
	}
	newNodes := []*model.StorageClusterNode{}
	changedRoles := map[int32]string{}
	for _, n := range planNodes {
		if m := members[n.Hostid]; m != nil {
			if roles := strings.Join(n.Roles, ","); roles != m.Roles {
				changedRoles[n.Hostid] = roles
			}
			continue
		}
		newNodes = append(newNodes, &model.StorageClusterNode{ClusterID: cluster.ID, Hostid: n.Hostid, Roles: strings.Join(n.Roles, ","),
			Attrs: jsonAttrs(nodeAttrs[n.Hostid]), Status: model.StorageNodeJoining, ReservedMemMB: backend.ReserveMB(n.Roles, disksOn[n.Hostid], params)})
	}
	newDisks := []*model.StorageClusterDisk{}
	for _, d := range req.Disks {
		hd := scanned[storageDiskKey(d.Hostid, d.DiskID)]
		id := diskIdentity(hd, d.Wipe)
		newDisks = append(newDisks, &model.StorageClusterDisk{ClusterID: cluster.ID, Hostid: d.Hostid, DiskID: d.DiskID, Serial: id.Serial, WWN: id.WWN,
			SizeBytes: hd.SizeBytes, Role: backend.DiskRole(), Media: d.Media, Attrs: jsonAttrs(diskAttrs[storageDiskKey(d.Hostid, d.DiskID)]),
			Status: model.StorageDiskClaiming})
	}
	// The plan sees the cluster with what joins: the hosts and disks are recorded with the task
	planned := append(append([]*model.StorageClusterNode{}, nodes...), newNodes...)
	for _, n := range planned {
		if roles, ok := changedRoles[n.Hostid]; ok {
			n.Roles = roles
		}
	}
	steps, err := backend.TaskPlan(task, cluster, &StorageTaskScope{Nodes: planned, Disks: append(append([]*model.StorageClusterDisk{}, disks...), newDisks...)})
	if err != nil {
		return nil, err
	}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: task, Plan: steps,
		Params: map[string]interface{}{"wipe": wipeList(req.Disks)},
		Prepare: func(tx *gorm.DB) (int64, error) {
			for _, n := range newNodes {
				if err := tx.Create(n).Error; err != nil {
					return 0, NewCLError(ErrStorageInvalidPlan, fmt.Sprintf("Host %d joins the cluster already", n.Hostid), err)
				}
			}
			for h, roles := range changedRoles {
				if err := tx.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND hostid = ?", cluster.ID, h).Update("roles", roles).Error; err != nil {
					return 0, err
				}
			}
			for _, d := range newDisks {
				if err := tx.Create(d).Error; err != nil {
					return 0, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s of host %d is claimed already", d.DiskID, d.Hostid), err)
				}
			}
			return cluster.ID, storageRefreshReserve(tx, cluster)
		}})
}

// AddNodes adds hosts to a managed cluster, each with its disks or none (§7.5): they are prechecked, get the software
// and join; disks are added to the file system (gpfs)
func (a *StorageClusterAdmin) AddNodes(ctx context.Context, uuid string, req *StorageClusterExpand) (*model.StorageTask, error) {
	return a.expand(ctx, uuid, StorageTaskAddNodes, req)
}

// AddDisks adds disks of members to a managed cluster (§7.5). A member without the disk role gets it
func (a *StorageClusterAdmin) AddDisks(ctx context.Context, uuid string, req *StorageClusterExpand) (*model.StorageTask, error) {
	if len(req.Nodes) > 0 {
		return nil, planError("Hosts are added with their disks by AddNodes")
	}
	return a.expand(ctx, uuid, StorageTaskAddDisks, req)
}

// RemoveDisk takes a disk out of a managed cluster (§7.5): its data moves to the other disks first, then it is
// wiped. Refused when the cluster would break its role rules (failure groups, replicas)
func (a *StorageClusterAdmin) RemoveDisk(ctx context.Context, uuid, diskUUID string) (*model.StorageTask, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	cluster, nodes, disks, backend, err := storageClusterForChange(ctx, uuid, func(c *StorageCapabilities) bool { return c.RemoveDisk })
	if err != nil {
		return nil, err
	}
	var disk *model.StorageClusterDisk
	for _, d := range disks {
		if d.UUID == diskUUID {
			disk = d
		}
	}
	if disk == nil {
		return nil, NewCLError(ErrStorageDiskNotAllowed, "The disk is not in the cluster", nil)
	}
	params, err := backend.ParseParams([]byte(cluster.Params))
	if err != nil {
		return nil, err
	}
	afterNodes, afterDisks := layoutAfter(nodes, disks, nil, nil, -1, disk.ID)
	withDiskRole(afterNodes, afterDisks, backend.DiskRole())
	if err = backend.CheckLayout(afterNodes, afterDisks, params); err != nil {
		return nil, planError("Without this disk: %s", planMessage(err))
	}
	disk.Status = model.StorageDiskRemoving
	steps, err := backend.TaskPlan(StorageTaskRemoveDisk, cluster, &StorageTaskScope{Nodes: nodes, Disks: disks})
	if err != nil {
		return nil, err
	}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskRemoveDisk, Plan: steps,
		Params: map[string]interface{}{"disk_id": disk.ID},
		Prepare: func(tx *gorm.DB) (int64, error) {
			if err := tx.Model(&model.StorageClusterDisk{}).Where("id = ?", disk.ID).Update("status", model.StorageDiskRemoving).Error; err != nil {
				return 0, err
			}
			return cluster.ID, storageRefreshReserve(tx, cluster)
		}})
}

// StorageNodeRemove takes a host out of a cluster
type StorageNodeRemove struct {
	Hostid int32
	// The host is gone for good: nothing runs on it, its disks are dropped from the cluster without moving their
	// data (the other copies remain)
	Offline bool
	// The host name, typed to confirm an offline removal
	Confirm string
	// Remove the storage software from the host too
	PurgePackages bool
}

// RemoveNode takes a host out of a managed cluster (§7.5): its disks first (their data moves, unless offline), then
// the host leaves and is cleaned. Refused while instances on the host use pools of the cluster, or when the cluster
// would break its role rules
func (a *StorageClusterAdmin) RemoveNode(ctx context.Context, uuid string, req *StorageNodeRemove) (*model.StorageTask, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	cluster, nodes, disks, backend, err := storageClusterForChange(ctx, uuid, func(c *StorageCapabilities) bool { return c.RemoveNode })
	if err != nil {
		return nil, err
	}
	var node *model.StorageClusterNode
	for _, n := range nodes {
		if n.Hostid == req.Hostid {
			node = n
		}
	}
	db := dbs.DBContext(ctx)
	if node == nil {
		return nil, planError("%s is not in the cluster", hostName(db, req.Hostid))
	}
	hyper, online := hostOnline(db, req.Hostid)
	if req.Offline {
		if hyper == nil || req.Confirm != hyper.Hostname {
			return nil, NewCLError(ErrStorageConfirmMismatch, "Type the name of the host to confirm its removal", nil)
		}
		if online {
			return nil, planError("%s is online: remove it the normal way", hyper.Hostname)
		}
	} else if !online {
		return nil, planError("%s is offline: remove it as a host that is gone for good", hostName(db, req.Hostid))
	}
	var busy int64
	db.Model(&model.Volume{}).Joins("JOIN instances i ON i.id = volumes.instance_id AND i.deleted_at IS NULL").
		Where("i.hyper = ? AND volumes.storage_pool_id IN (SELECT id FROM storage_pools WHERE cluster_id = ?)", req.Hostid, cluster.ID).Count(&busy)
	if busy > 0 {
		return nil, NewCLError(ErrStoragePoolInUse, fmt.Sprintf("%d volumes of the cluster's pools are attached to instances on %s: move those instances first",
			busy, hostName(db, req.Hostid)), nil)
	}
	params, err := backend.ParseParams([]byte(cluster.Params))
	if err != nil {
		return nil, err
	}
	afterNodes, afterDisks := layoutAfter(nodes, disks, nil, nil, req.Hostid, 0)
	if err = backend.CheckLayout(afterNodes, afterDisks, params); err != nil {
		return nil, planError("Without %s: %s", hostName(db, req.Hostid), planMessage(err))
	}
	node.Status = model.StorageNodeLeaving
	for _, d := range disks {
		if d.Hostid == req.Hostid {
			d.Status = model.StorageDiskRemoving
		}
	}
	steps, err := backend.TaskPlan(StorageTaskRemoveNode, cluster, &StorageTaskScope{Nodes: nodes, Disks: disks, Offline: req.Offline})
	if err != nil {
		return nil, err
	}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskRemoveNode, Plan: steps,
		Params: map[string]interface{}{"hostid": req.Hostid, "offline": req.Offline, "purge_packages": req.PurgePackages},
		Prepare: func(tx *gorm.DB) (int64, error) {
			if err := tx.Model(&model.StorageClusterNode{}).Where("id = ?", node.ID).Update("status", model.StorageNodeLeaving).Error; err != nil {
				return 0, err
			}
			return cluster.ID, tx.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND hostid = ?", cluster.ID, req.Hostid).
				Update("status", model.StorageDiskRemoving).Error
		}})
}

// Rebalance spreads the data of a file system over all its disks again (gpfs mmrestripefs -b): a long, I/O heavy
// task, started by hand after disks were added
func (a *StorageClusterAdmin) Rebalance(ctx context.Context, uuid, filesystem string) (*model.StorageTask, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	cluster, nodes, _, backend, err := storageClusterForChange(ctx, uuid, func(c *StorageCapabilities) bool { return c.Rebalance })
	if err != nil {
		return nil, err
	}
	fs := &model.StorageFilesystem{}
	q := dbs.DBContext(ctx).Where("cluster_id = ?", cluster.ID)
	if filesystem != "" {
		q = q.Where("name = ?", filesystem)
	}
	if err = q.Order("id").Take(fs).Error; err != nil {
		return nil, planError("Storage cluster %s has no file system %s", cluster.Name, filesystem)
	}
	steps, err := backend.TaskPlan(StorageTaskRebalance, cluster, &StorageTaskScope{Nodes: nodes})
	if err != nil {
		return nil, err
	}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskRebalance, Plan: steps,
		Params: map[string]interface{}{"filesystem_id": fs.ID}})
}

// storageTaskInt reads a number of the parameters of a task
func storageTaskInt(task *model.StorageTask, key string) int64 {
	p := map[string]interface{}{}
	_ = json.Unmarshal([]byte(task.Params), &p)
	f, _ := p[key].(float64)
	return int64(f)
}

func storageTaskBool(task *model.StorageTask, key string) bool {
	p := map[string]interface{}{}
	_ = json.Unmarshal([]byte(task.Params), &p)
	b, _ := p[key].(bool)
	return b
}

// storageRefreshReserve recomputes what the daemons of a cluster hold back on each member (shared-storage-design.md
// §6.7.1) from its roles and its disks, a disk leaving (removing) not counted, and records it where it changed: with a
// kind whose daemons go with the disks (a Ceph OSD per disk), adding or removing a disk changes it. Run when such a
// task starts and when it ends; the task writes it on the hosts with its finish step. A failed claim is still
// counted: its daemon may run, and holding back too much is the safe side
func storageRefreshReserve(tx *gorm.DB, cluster *model.StorageCluster) error {
	backend, err := storageBackendOf(cluster.Kind)
	if err != nil {
		return err
	}
	params, err := backend.ParseParams([]byte(cluster.Params))
	if err != nil {
		return err
	}
	nodes := []*model.StorageClusterNode{}
	if err = tx.Where("cluster_id = ?", cluster.ID).Find(&nodes).Error; err != nil {
		return err
	}
	disks := []*model.StorageClusterDisk{}
	if err = tx.Where("cluster_id = ? AND status <> ?", cluster.ID, model.StorageDiskRemoving).Find(&disks).Error; err != nil {
		return err
	}
	on := map[int32]int{}
	for _, d := range disks {
		on[d.Hostid]++
	}
	for _, n := range nodes {
		mb := backend.ReserveMB(strings.Split(n.Roles, ","), on[n.Hostid], params)
		if mb == n.ReservedMemMB {
			continue
		}
		if err = tx.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND hostid = ?", cluster.ID, n.Hostid).
			Update("reserved_mem_mb", mb).Error; err != nil {
			return err
		}
	}
	return nil
}

// storageRefreshAfter: the finish of a task that changes the disks of a cluster, then what the daemons hold back on
// its hosts as the disks are now (added, removed, or failed when the task was aborted)
func storageRefreshAfter(finish storageTaskFinish) storageTaskFinish {
	return func(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
		if err := finish(ctx, tx, task, succeeded); err != nil {
			return err
		}
		cluster := &model.StorageCluster{}
		if err := tx.Take(cluster, task.ClusterID).Error; err != nil {
			return err
		}
		return storageRefreshReserve(tx, cluster)
	}
}
