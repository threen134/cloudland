/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Creating and deleting managed storage clusters (shared-storage-design.md §7.2, §7.6, §8.2, §8.6). What is common
// to every kind lives here: checking the request, recording the cluster with its hosts and disks, and starting the
// task; the steps of the task come from the backend of the kind (§4.5.1).

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StorageTaskDeploy        = "deploy"
	StorageTaskDeleteCluster = "delete_cluster"
)

var storageClusterNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,62}$`)

// StorageClusterCreate is a request for a managed cluster
type StorageClusterCreate struct {
	Plan        *StoragePlan
	Name        string
	Description string
	// UUID of an uploaded package, for kinds that install from one
	PackageUUID string
}

func storageDiskKey(hostid int32, diskID string) string {
	return fmt.Sprintf("%d/%s", hostid, diskID)
}

func jsonAttrs(attrs map[string]interface{}) string {
	if len(attrs) == 0 {
		return ""
	}
	b, _ := json.Marshal(attrs)
	return string(b)
}

// Create records a managed cluster with its hosts and disks and starts its deployment
func (a *StorageClusterAdmin) Create(ctx context.Context, req *StorageClusterCreate) (cluster *model.StorageCluster, task *model.StorageTask, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	plan := req.Plan
	if !storageClusterNameRe.MatchString(req.Name) {
		return nil, nil, planError("The cluster name starts with a letter and holds at most 63 letters, digits and . _ -")
	}
	if len(req.Description) > 256 {
		return nil, nil, planError("The description is at most 256 characters")
	}
	backend, err := storageBackendOf(plan.Kind)
	if err != nil {
		return
	}
	// The cluster keeps its SSH key encrypted (§6.6)
	if err = SecretStoreReady(); err != nil {
		return nil, nil, NewCLError(ErrSecretUnavailable, "The credential key (VPN_SECRET_KEY) is not set, so the cluster key can not be stored", err)
	}
	params, err := backend.ParseParams(plan.Params)
	if err != nil {
		return
	}
	normalized, _ := json.Marshal(params)
	db := dbs.DBContext(ctx)
	plan.ClusterID = 0
	_, scanned, err := checkStoragePlan(db, plan)
	if err != nil {
		return
	}
	var pkg *model.StoragePackage
	if backend.Requirements().Package {
		if pkg, err = StoragePackages.get(ctx, req.PackageUUID); err != nil {
			return nil, nil, err
		}
		if pkg.Kind != plan.Kind || pkg.Status != model.StoragePackageReady {
			return nil, nil, NewCLError(ErrStoragePackageState, "The package is not a verified "+plan.Kind+" package", nil)
		}
		if pkg.AcceptedBy == "" {
			return nil, nil, NewCLError(ErrStoragePackageState, "Accept the license of the package first", nil)
		}
	} else if req.PackageUUID != "" {
		return nil, nil, planError("A %s cluster installs from the distribution and takes no package", plan.Kind)
	}
	pubKey, privKey, err := newStorageSSHKey()
	if err != nil {
		return nil, nil, NewCLError(ErrSecretUnavailable, "Failed to make the cluster key", err)
	}
	// The media of a disk is what the admin chose, else what the scan found
	for _, d := range plan.Disks {
		if d.Media == "" {
			d.Media = scanned[storageDiskKey(d.Hostid, d.DiskID)].Media
		}
	}
	nodeAttrs, diskAttrs := backend.InitialAttrs(nil, nil, plan.Nodes, plan.Disks)
	disksOn := map[int32]int{}
	for _, d := range plan.Disks {
		disksOn[d.Hostid]++
	}
	cluster = &model.StorageCluster{Name: req.Name, Kind: plan.Kind, Mode: model.StorageModeManaged, Layout: backend.DefaultLayout(),
		Status: model.StorageClusterDeploying, Health: model.StorageHealthUnknown, Params: string(normalized),
		Unsupported: plan.AllowUnsupported, SSHPubKey: pubKey, SSHPrivKey: privKey, Description: req.Description}
	if pkg != nil {
		cluster.PackageID, cluster.Version = pkg.ID, pkg.Version
	}
	nodes := []*model.StorageClusterNode{}
	for _, n := range plan.Nodes {
		nodes = append(nodes, &model.StorageClusterNode{Hostid: n.Hostid, Roles: strings.Join(n.Roles, ","), Attrs: jsonAttrs(nodeAttrs[n.Hostid]),
			Status: model.StorageNodeJoining, ReservedMemMB: backend.ReserveMB(n.Roles, disksOn[n.Hostid], params)})
	}
	disks := []*model.StorageClusterDisk{}
	for _, d := range plan.Disks {
		hd := scanned[storageDiskKey(d.Hostid, d.DiskID)]
		media := d.Media
		id := diskIdentity(hd, d.Wipe)
		disks = append(disks, &model.StorageClusterDisk{Hostid: d.Hostid, DiskID: d.DiskID, Serial: id.Serial, WWN: id.WWN, SizeBytes: hd.SizeBytes,
			Role: backend.DiskRole(), Media: media, Attrs: jsonAttrs(diskAttrs[storageDiskKey(d.Hostid, d.DiskID)]), Status: model.StorageDiskClaiming})
	}
	steps, err := backend.TaskPlan(StorageTaskDeploy, cluster, &StorageTaskScope{Nodes: nodes, Disks: disks})
	if err != nil {
		return nil, nil, err
	}
	task, err = startStorageTask(ctx, &storageTaskSpec{Backend: plan.Kind, Kind: StorageTaskDeploy, Plan: steps,
		Params: map[string]interface{}{"wipe": wipeList(plan.Disks)},
		Prepare: func(tx *gorm.DB) (int64, error) {
			var taken int64
			tx.Model(&model.StorageCluster{}).Where("name = ?", req.Name).Count(&taken)
			if taken > 0 {
				return 0, planError("A storage cluster named %s exists already", req.Name)
			}
			if err := tx.Create(cluster).Error; err != nil {
				return 0, err
			}
			for _, n := range nodes {
				n.ClusterID = cluster.ID
				if err := tx.Create(n).Error; err != nil {
					return 0, err
				}
			}
			for _, d := range disks {
				d.ClusterID = cluster.ID
				// The unique index (hostid, disk_id) refuses a disk another request claimed meanwhile
				if err := tx.Create(d).Error; err != nil {
					return 0, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s of host %d is claimed already", d.DiskID, d.Hostid), err)
				}
			}
			return cluster.ID, nil
		}})
	if err != nil {
		return nil, nil, err
	}
	return cluster, task, nil
}

func wipeList(disks []*StorageDiskPlan) []string {
	list := []string{}
	for _, d := range disks {
		if d.Wipe {
			list = append(list, storageDiskKey(d.Hostid, d.DiskID))
		}
	}
	return list
}

// StorageClusterDelete is a request to delete a cluster
type StorageClusterDelete struct {
	ConfirmName string
	// Wipe the disks of the cluster (always for a managed cluster: the disks hold its data and must not be taken
	// for a local pool with that on them)
	PurgePackages bool
}

// Delete starts the deletion of a cluster: the storage software is torn down, the disks are wiped and every host
// is cleaned, then the records go. It works on a half deployed cluster too (§7.6)
func (a *StorageClusterAdmin) Delete(ctx context.Context, uuid string, req *StorageClusterDelete) (task *model.StorageTask, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	cluster, nodes, _, err := a.Get(ctx, uuid)
	if err != nil {
		return
	}
	if req.ConfirmName != cluster.Name {
		return nil, NewCLError(ErrStorageConfirmMismatch, "Type the name of the cluster to confirm", nil)
	}
	backend, err := storageBackendOf(cluster.Kind)
	if err != nil {
		return
	}
	// A failed deployment holds the structural slot: abort it first, then delete
	if cluster.ActiveTask != 0 {
		return nil, NewCLError(ErrStorageClusterBusy, "A task still holds the cluster: abort it first", nil)
	}
	steps, err := backend.TaskPlan(StorageTaskDeleteCluster, cluster, &StorageTaskScope{Nodes: nodes})
	if err != nil {
		return
	}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskDeleteCluster, Plan: steps,
		Params: map[string]interface{}{"purge_packages": req.PurgePackages},
		Prepare: func(tx *gorm.DB) (int64, error) {
			// Checked under the lock of the cluster row, which a pool being created takes as well: no pool slips in
			// between the check and the deletion
			c := &model.StorageCluster{}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(c, cluster.ID).Error; err != nil {
				return 0, NewCLError(ErrStorageClusterNotFound, "Storage cluster not found", err)
			}
			var pools, imports int64
			if err := tx.Model(&model.StoragePool{}).Where("cluster_id = ?", c.ID).Count(&pools).Error; err != nil {
				return 0, err
			}
			if pools > 0 {
				return 0, NewCLError(ErrStoragePoolInUse, fmt.Sprintf("Delete the %d storage pool(s) of the cluster first", pools), nil)
			}
			// An import holds no slot. Its check may still run on the hosts, or run again on a retry, and would write
			// back the client configuration the deletion removes
			if err := tx.Model(&model.StorageTask{}).Where("cluster_id = ? AND kind = ? AND status IN ?", c.ID, StorageTaskImport,
				[]string{model.StorageTaskRunning, model.StorageTaskAborting, model.StorageTaskFailed}).Count(&imports).Error; err != nil {
				return 0, err
			}
			if imports > 0 {
				return 0, NewCLError(ErrStorageClusterBusy, "The import of the cluster is still running, or waits to be retried or aborted: let it end or abort it first", nil)
			}
			return c.ID, tx.Model(&model.StorageCluster{}).Where("id = ?", c.ID).Update("status", model.StorageClusterDeleting).Error
		}})
}

// ---- helpers for the step definitions of the backends ----

// storageClusterOfTask loads the cluster of a task with its hosts and disks
func storageClusterOfTask(db *gorm.DB, task *model.StorageTask) (cluster *model.StorageCluster, nodes []*model.StorageClusterNode, disks []*model.StorageClusterDisk, err error) {
	cluster = &model.StorageCluster{}
	if err = db.Take(cluster, task.ClusterID).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("task %d has no cluster: %v", task.ID, err)
	}
	if err = db.Where("cluster_id = ?", cluster.ID).Order("hostid").Find(&nodes).Error; err != nil {
		return
	}
	err = db.Where("cluster_id = ?", cluster.ID).Order("hostid, id").Find(&disks).Error
	return
}

// storageHostIPs returns the internal addresses of some hosts (hypers.host_ip), the names the storage software
// knows its members by
func storageHostIPs(db *gorm.DB, hostids []int32) (map[int32]string, error) {
	hypers := []*model.Hyper{}
	if err := db.Where("hostid IN ?", hostids).Find(&hypers).Error; err != nil {
		return nil, err
	}
	ips := map[int32]string{}
	for _, h := range hypers {
		ips[h.Hostid] = h.HostIP
	}
	for _, id := range hostids {
		if ips[id] == "" {
			return nil, fmt.Errorf("host %d has no internal address", id)
		}
	}
	return ips, nil
}

func nodeHostids(nodes []*model.StorageClusterNode, role string) []int32 {
	ids := []int32{}
	for _, n := range nodes {
		if role == "" || n.HasRole(role) {
			ids = append(ids, n.Hostid)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func nodeAttr(n *model.StorageClusterNode, key string) interface{} {
	attrs := map[string]interface{}{}
	_ = json.Unmarshal([]byte(n.Attrs), &attrs)
	return attrs[key]
}

func diskAttr(d *model.StorageClusterDisk, key string) string {
	attrs := map[string]interface{}{}
	_ = json.Unmarshal([]byte(d.Attrs), &attrs)
	s, _ := attrs[key].(string)
	return s
}

// clusterPrecheckPlan is the plan of a recorded cluster in the form the precheck step reads
func clusterPrecheckPlan(db *gorm.DB, cluster *model.StorageCluster, nodes []*model.StorageClusterNode, disks []*model.StorageClusterDisk, wipe []string) *storagePlanParams {
	p := &storagePlanParams{Kind: cluster.Kind, Params: json.RawMessage(cluster.Params), AllowUnsupported: cluster.Unsupported, ClusterID: cluster.ID}
	for _, n := range nodes {
		p.Nodes = append(p.Nodes, &storageNodeParams{Hostid: n.Hostid, Roles: strings.Split(n.Roles, ",")})
	}
	wiped := map[string]bool{}
	for _, w := range wipe {
		wiped[w] = true
	}
	for _, d := range disks {
		p.Disks = append(p.Disks, &storageDiskParams{Hostid: d.Hostid, DiskID: d.DiskID, Media: d.Media, Wipe: wiped[storageDiskKey(d.Hostid, d.DiskID)]})
	}
	return p
}

// storageTaskWipe is the list of disks a deployment may wipe (from the task parameters)
func storageTaskWipe(task *model.StorageTask) []string {
	p := struct {
		Wipe []string `json:"wipe"`
	}{}
	_ = json.Unmarshal([]byte(task.Params), &p)
	return p.Wipe
}

// storageClusterDiskIdentity is what a host checks on a claimed disk before writing it (§6.4)
func storageClusterDiskIdentity(d *model.StorageClusterDisk, wipe bool) *storageDiskIdentity {
	return &storageDiskIdentity{ID: d.DiskID, Serial: d.Serial, WWN: d.WWN, SizeBytes: d.SizeBytes, Wipe: wipe}
}

// scanStorageHosts asks the hosts of a cluster to scan their disks again, so the scan shows the disks the cluster
// claimed or gave back
func scanStorageHosts(ctx context.Context, hostids []int32) {
	for _, h := range hostids {
		if err := HyperExecute(ctx, fmt.Sprintf("inter=%d", h), "/opt/cloudland/scripts/backend/scan_host_disks.sh"); err != nil {
			logger.Ctx(ctx).Warningf("Failed to ask host %d to scan its disks: %v", h, err)
		}
	}
}

func storageNow() *time.Time {
	now := time.Now()
	return &now
}
