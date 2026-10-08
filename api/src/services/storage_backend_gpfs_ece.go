/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The erasure code layout of GPFS (IBM Storage Scale Erasure Code Edition, shared-storage-design.md §7.9): after the
// cluster runs, the disks of the NSD hosts go to one recovery group (mmvdisk), the file system lives on a vdisk set
// with an erasure code. The steps are registered with the deploy task of gpfs (storage_backend_gpfs_tasks.go).

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

// The packages of the erasure code layout besides those of a replica cluster: the native RAID (gpfs.gnr*) and the
// ones the validation installed with them (§7.9)
const gpfsECEDebPrefixes = "gpfs.gnr gpfs.adv_ gpfs.crypto_ gpfs.compression_"

// gpfsECENames are the mmvdisk objects of a cluster: node class, recovery group, the first data vdisk set
func gpfsECENames(cluster *model.StorageCluster) (nodeClass, rg, vs string) {
	return fmt.Sprintf("cl%d_nc", cluster.ID), fmt.Sprintf("cl%d_rg", cluster.ID), fmt.Sprintf("cl%d_vs", cluster.ID)
}

// gpfsECEMetaSet is the metadata vdisk set of a mixed media cluster
func gpfsECEMetaSet(cluster *model.StorageCluster) string {
	return fmt.Sprintf("cl%d_vsm", cluster.ID)
}

// gpfsECEDeploySets are the vdisk sets a deployment makes: the data set on the only array, or on the HDDs with the
// metadata set on the solid state disks beside them (mixed media)
func gpfsECEDeploySets(cluster *model.StorageCluster, disks []*model.StorageClusterDisk) ([]map[string]interface{}, error) {
	media := []string{}
	for _, d := range disks {
		media = append(media, d.Media)
	}
	data, meta, err := gpfsECEArrays(media)
	if err != nil {
		return nil, err
	}
	p := gpfsParamsOf(cluster)
	_, _, vs := gpfsECENames(cluster)
	set := gpfsECESetFor(p, data, data, meta)
	set["vdisk_set"] = vs
	sets := []map[string]interface{}{set}
	if meta != "" {
		m := gpfsECESetFor(p, meta, data, meta)
		m["vdisk_set"] = gpfsECEMetaSet(cluster)
		sets = append(sets, m)
	}
	return sets, nil
}

func gpfsParamsOf(cluster *model.StorageCluster) *gpfsParams {
	p := &gpfsParams{}
	_ = json.Unmarshal([]byte(cluster.Params), p)
	return p
}

// gpfsECEDeployPlan: the shared start (gpfsDeployPrefix: the running cluster and the resolved disks), then the
// recovery group on them instead of NSDs
func gpfsECEDeployPlan(all, admins, servers []int32) []*StorageStepPlan {
	step := func(name, scope string, hosts []int32, timeout time.Duration) *StorageStepPlan {
		return &StorageStepPlan{Name: name, Scope: scope, Hostids: hosts, Timeout: timeout}
	}
	n, a := model.StorageStepScopeNodes, model.StorageStepScopeAdmin
	return append(gpfsDeployPrefix(all, admins, servers),
		step("ece_configure", a, admins, 45*time.Minute),
		step("ece_slots", a, admins, 45*time.Minute),
		// mmvdisk formats a log home vdisk per log group: 1.5-3.5 hours on the (emulated) HDDs of the validation (§7.9)
		step("ece_create_rg", a, admins, 8*time.Hour),
		step("ece_create_vs", a, admins, 30*time.Minute),
		step("ece_create_fs", a, admins, 60*time.Minute),
		step("finish", n, all, 5*time.Minute),
	)
}

// gpfsECESteps are the steps of the erasure code layout in the deploy task of gpfs. The disk expression comes from
// the resolve_disks step that ran right before, so a retry starts there
var gpfsECESteps = map[string]*storageStepDef{
	"ece_configure": {Script: "gpfs_ece.sh", Input: gpfsECEConfigureInput, RetryFrom: "resolve_disks"},
	"ece_slots":     {Script: "gpfs_ece.sh", Input: gpfsECESlotsInput},
	"ece_create_rg": {Script: "gpfs_ece.sh", Input: gpfsECECreateRGInput, RetryFrom: "resolve_disks"},
	"ece_create_vs": {Script: "gpfs_ece.sh", Input: gpfsECECreateVSInput},
	"ece_create_fs": {Script: "gpfs_ece.sh", Input: gpfsECECreateFSInput},
}

// gpfsResolvedNames are the kernel names of the claimed disks each host resolved: hostid -> disk id -> name
func gpfsResolvedNames(db *gorm.DB, task *model.StorageTask) (map[int32]map[string]string, error) {
	resolved, err := storageStepResults(db, task.ID, "resolve_disks")
	if err != nil {
		return nil, err
	}
	names := map[int32]map[string]string{}
	for h, raw := range resolved {
		r := struct {
			Disks []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"disks"`
		}{}
		_ = json.Unmarshal(raw, &r)
		names[h] = map[string]string{}
		for _, d := range r.Disks {
			names[h][d.ID] = d.Name
		}
	}
	return names, nil
}

// gpfsECEDiskExpr is the disk list of mmvdisk: the claimed disks of every server by kernel name, "<ip>:sdb,sdc;..."
// (mmvdisk takes the address of a node for its name), so the recovery group never takes another disk of a host; with
// the addresses of the servers in the same order (by hostid)
func gpfsECEDiskExpr(db *gorm.DB, task *model.StorageTask, nodes []*model.StorageClusterNode,
	disks []*model.StorageClusterDisk) (expr string, serverIPs []string, err error) {
	names, err := gpfsResolvedNames(db, task)
	if err != nil {
		return "", nil, err
	}
	servers := nodeHostids(nodes, model.StorageRoleNSD)
	ips, err := storageHostIPs(db, servers)
	if err != nil {
		return "", nil, err
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i] < servers[j] })
	parts := []string{}
	for _, h := range servers {
		list := []string{}
		for _, d := range disks {
			if d.Hostid != h {
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
		serverIPs = append(serverIPs, ips[h])
	}
	return strings.Join(parts, ";"), serverIPs, nil
}

func gpfsECEConfigureInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	expr, list, err := gpfsECEDiskExpr(db, task, nodes, disks)
	if err != nil {
		return nil, err
	}
	nc, _, _ := gpfsECENames(cluster)
	p := gpfsParamsOf(cluster)
	pagepool := p.PagepoolMiB
	if pagepool < gpfsECEMinPagepoolMiB {
		pagepool = gpfsECEMinPagepoolMiB
	}
	return map[string]interface{}{"action": "configure", "cluster_uuid": cluster.UUID, "node_class": nc, "servers": list,
		"disk_expr": expr, "pagepool_bytes": int64(pagepool) << 20}, nil
}

func gpfsECESlotsInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	nc, _, _ := gpfsECENames(cluster)
	return gpfsECESlotInput(gpfsParamsOf(cluster), map[string]interface{}{"action": "slots", "cluster_uuid": cluster.UUID, "node_class": nc}), nil
}

func gpfsECECreateRGInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	expr, _, err := gpfsECEDiskExpr(db, task, nodes, disks)
	if err != nil {
		return nil, err
	}
	nc, rg, _ := gpfsECENames(cluster)
	return map[string]interface{}{"action": "create_rg", "cluster_uuid": cluster.UUID, "recovery_group": rg, "node_class": nc, "disk_expr": expr}, nil
}

func gpfsECECreateVSInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	_, rg, _ := gpfsECENames(cluster)
	sets, err := gpfsECEDeploySets(cluster, disks)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"action": "create_vs", "cluster_uuid": cluster.UUID, "recovery_group": rg, "sets": sets}, nil
}

// gpfsECEFsName is the file system of an erasure code cluster
func gpfsECEFsName(cluster *model.StorageCluster) string {
	if name := gpfsParamsOf(cluster).FsName; name != "" {
		return name
	}
	return "fs1"
}

func gpfsECECreateFSInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	sets, err := gpfsECEDeploySets(cluster, disks)
	if err != nil {
		return nil, err
	}
	names, dataPool := []string{}, ""
	for _, s := range sets {
		names = append(names, s["vdisk_set"].(string))
		if s["storage_pool"] == "data" {
			dataPool = "data"
		}
	}
	name := gpfsECEFsName(cluster)
	return map[string]interface{}{"action": "create_fs", "cluster_uuid": cluster.UUID, "fs_name": name, "vdisk_sets": names,
		"data_pool": dataPool, "mount_point": gpfsMountRoot + "/" + name}, nil
}

// gpfsECEPdisk is a pdisk of the recovery group as ece_create_rg reports it
type gpfsECEPdisk struct {
	Name   string `json:"name"`
	IP     string `json:"ip"`
	Device string `json:"device"`
	WWN    string `json:"wwn"`
	State  string `json:"state"`
}

// gpfsWWNKey writes a WWN the one way: GPFS says naa.5000C500..., lsblk 0x5000c500..., udev wwn-0x5000c500...
func gpfsWWNKey(wwn string) string {
	w := strings.ToLower(strings.TrimSpace(wwn))
	for _, prefix := range []string{"wwn-", "naa.", "0x"} {
		w = strings.TrimPrefix(w, prefix)
	}
	return w
}

// gpfsECEDeployFinish records a deployed erasure code cluster: the file system, the mmvdisk objects, every disk by
// the name of its pdisk (matched by WWN, else by host and kernel name: the health of the disks comes by that name),
// every host and disk active (gpfsDeployDone)
func gpfsECEDeployFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, cluster *model.StorageCluster,
	nodes []*model.StorageClusterNode, disks []*model.StorageClusterDisk) error {
	p := gpfsParamsOf(cluster)
	name := gpfsECEFsName(cluster)
	fs := &model.StorageFilesystem{ClusterID: cluster.ID, Name: name, MountPoint: gpfsMountRoot + "/" + name, BlockSize: p.BlockSize,
		DataReplicas: 1, MetaReplicas: 1, Status: "ready"}
	pdisks := []*gpfsECEPdisk{}
	if res, err := storageStepResults(tx, task.ID, "ece_create_rg"); err == nil {
		for _, raw := range res {
			r := struct {
				Pdisks []*gpfsECEPdisk `json:"pdisks"`
			}{}
			_ = json.Unmarshal(raw, &r)
			pdisks = append(pdisks, r.Pdisks...)
		}
	}
	names, err := gpfsResolvedNames(tx, task)
	if err != nil {
		return err
	}
	ips, err := storageHostIPs(tx, nodeHostids(nodes, ""))
	if err != nil {
		return err
	}
	pdiskOf, err := gpfsECEPdiskNames(disks, pdisks, ips, names)
	if err != nil {
		return err
	}
	nc, rg, vs := gpfsECENames(cluster)
	sets, err := gpfsECEDeploySets(cluster, disks)
	if err != nil {
		return err
	}
	setNames := []string{}
	for _, s := range sets {
		setNames = append(setNames, s["vdisk_set"].(string))
	}
	return gpfsDeployDone(ctx, tx, task, cluster, nodes, disks, &gpfsDeployLayout{fs: fs, fsStep: "ece_create_fs", diskNames: pdiskOf,
		attrs: map[string]interface{}{"node_class": nc, "recovery_group": rg, "vdisk_set": vs, "vdisk_sets": setNames, "ece_code": p.ECECode}})
}

// gpfsECEPdiskNames names every disk of the cluster by its pdisk: by WWN (recorded when the disk was claimed), else by
// host address and the kernel name the disk had when it was resolved right before the recovery group was made. A disk
// without its pdisk, or two disks on one pdisk, fail the deployment rather than leave the health unmatched
func gpfsECEPdiskNames(disks []*model.StorageClusterDisk, pdisks []*gpfsECEPdisk, ips map[int32]string,
	resolved map[int32]map[string]string) (map[int64]string, error) {
	byDevice, byWWN := map[string]string{}, map[string]string{}
	for _, pd := range pdisks {
		if pd.Name == "" {
			continue
		}
		byDevice[pd.IP+"/"+pd.Device] = pd.Name
		if w := gpfsWWNKey(pd.WWN); w != "" {
			byWWN[w] = pd.Name
		}
	}
	names, taken := map[int64]string{}, map[string]bool{}
	for _, d := range disks {
		pdisk := ""
		if w := gpfsWWNKey(d.WWN); w != "" {
			pdisk = byWWN[w]
		}
		if pdisk == "" {
			pdisk = byDevice[ips[d.Hostid]+"/"+resolved[d.Hostid][d.DiskID]]
		}
		if pdisk == "" {
			return nil, fmt.Errorf("disk %s of host %d is not a pdisk of the recovery group", d.DiskID, d.Hostid)
		}
		if taken[pdisk] {
			return nil, fmt.Errorf("pdisk %s matches two disks of the cluster", pdisk)
		}
		taken[pdisk] = true
		names[d.ID] = pdisk
	}
	return names, nil
}

// gpfsECETeardown is the erasure code part of the teardown input: gpfs_cluster.sh deletes it in mmvdisk's order
// before the cluster itself (every vdisk set of the recovery group, found there, then the group and the node class);
// the disks are pdisks, not NSDs
func gpfsECETeardown(cluster *model.StorageCluster) map[string]interface{} {
	nc, rg, _ := gpfsECENames(cluster)
	return map[string]interface{}{"recovery_group": rg, "node_class": nc}
}

// HealthInput: the health of an erasure code cluster reads the pdisks of its recovery group (backend_health in
// scripts/kvm/storage/backends/gpfs.sh); they carry the names of the cluster's disks
func (gpfsBackend) HealthInput(cluster *model.StorageCluster) map[string]interface{} {
	if cluster.Layout != model.StorageLayoutECE {
		return nil
	}
	_, rg, _ := gpfsECENames(cluster)
	return map[string]interface{}{"recovery_group": rg}
}
