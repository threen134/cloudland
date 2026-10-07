/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The tasks of the GPFS backend: deploying a managed replica cluster (shared-storage-design.md §7.2) and deleting
// one (§7.6). Inputs are built from the database when a step starts, so a retry uses what is recorded then.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	. "api/src/common"
	"api/src/model"

	"gorm.io/gorm"
)

const (
	gpfsMountRoot = "/gpfs"
	// Packages a replica cluster installs from the installer; the license package of the edition is added
	gpfsDebPrefixes = "gpfs.base_ gpfs.gpl_ gpfs.gskit_ gpfs.msg.en-us_ gpfs.docs_ gpfs.license."
)

func (gpfsBackend) DefaultLayout() string { return model.StorageLayoutReplica }

// InitialAttrs: a failure group per host with disks, numbered from 1 in the cluster (never the hostid, GPFS bounds
// them), a host that has one keeps it; with one kind of media every NSD holds data and metadata in the system pool,
// with SSD or NVMe and HDD the fast disks hold both in system and the HDDs only data in the data pool (§7.3). Disks
// added later follow the layout the cluster has: HDDs go to the data pool when there is one
func (gpfsBackend) InitialAttrs(existing []*model.StorageClusterNode, existingDisks []*model.StorageClusterDisk, nodes []*StorageNodePlan,
	disks []*StorageDiskPlan) (map[int32]map[string]interface{}, map[string]map[string]interface{}) {
	nodeAttrs := map[int32]map[string]interface{}{}
	groups := map[int32]bool{}
	maxGroup := 0
	for _, n := range existing {
		if fg, ok := nodeAttr(n, "failure_group").(float64); ok {
			groups[n.Hostid] = true
			if int(fg) > maxGroup {
				maxGroup = int(fg)
			}
		}
	}
	hosts := []int32{}
	seen := map[int32]bool{}
	fast, slow := false, false
	for _, d := range disks {
		if !seen[d.Hostid] && !groups[d.Hostid] {
			seen[d.Hostid] = true
			hosts = append(hosts, d.Hostid)
		}
		if d.Media == "hdd" {
			slow = true
		} else {
			fast = true
		}
	}
	dataPool := fast && slow
	if len(existing) > 0 {
		dataPool = false
		for _, d := range existingDisks {
			if diskAttr(d, "gpfs_pool") == gpfsPoolData {
				dataPool = true
			}
		}
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i] < hosts[j] })
	for i, h := range hosts {
		nodeAttrs[h] = map[string]interface{}{"failure_group": maxGroup + i + 1}
	}
	diskAttrs := map[string]map[string]interface{}{}
	for _, d := range disks {
		usage, pool := "dataAndMetadata", gpfsPoolSystem
		if dataPool && d.Media == "hdd" {
			usage, pool = "dataOnly", gpfsPoolData
		}
		diskAttrs[storageDiskKey(d.Hostid, d.DiskID)] = map[string]interface{}{"usage": usage, "gpfs_pool": pool}
	}
	return nodeAttrs, diskAttrs
}

func (gpfsBackend) TaskPlan(task string, cluster *model.StorageCluster, scope *StorageTaskScope) ([]*StorageStepPlan, error) {
	nodes := scope.Nodes
	all := nodeHostids(nodes, "")
	admins := nodeHostids(nodes, model.StorageRoleAdmin)
	nsds := nodeHostids(nodes, model.StorageRoleNSD)
	step := func(name, scope string, hosts []int32, timeout time.Duration) *StorageStepPlan {
		return &StorageStepPlan{Name: name, Scope: scope, Hostids: hosts, Timeout: timeout}
	}
	n, a := model.StorageStepScopeNodes, model.StorageStepScopeAdmin
	switch task {
	case StorageTaskDeploy:
		if len(admins) == 0 || len(nsds) == 0 {
			return nil, planError("A GPFS cluster needs an admin host and an NSD host")
		}
		if cluster.Layout == model.StorageLayoutECE {
			return gpfsECEDeployPlan(all, admins, nsds), nil
		}
		return append(gpfsDeployPrefix(all, admins, nsds),
			step("create_nsd", a, admins, 15*time.Minute),
			step("create_fs", a, admins, 60*time.Minute),
			step("finish", n, all, 5*time.Minute),
		), nil
	case StorageTaskDeleteCluster:
		if cluster.Mode == model.StorageModeExternal {
			// Only what CloudLand put on the hosts goes; the cluster is not CloudLand's to tear down
			return []*StorageStepPlan{step("forget", n, all, 10*time.Minute)}, nil
		}
		if len(admins) == 0 {
			return nil, planError("The cluster has no admin host")
		}
		return []*StorageStepPlan{
			step("teardown", a, admins, 30*time.Minute),
			step("leave", n, all, 30*time.Minute),
		}, nil
	case StorageTaskImport:
		return []*StorageStepPlan{step("check_import", n, all, 10*time.Minute)}, nil
	case StorageTaskCreatePool, StorageTaskUpdatePool, StorageTaskDeletePool:
		if cluster.Mode == model.StorageModeExternal {
			// An imported cluster has no admin host of CloudLand's: any of its hosts registers the directory
			admins = all
		}
		if len(admins) == 0 {
			return nil, planError("The cluster has no admin host")
		}
		return gpfsPoolTaskPlan(task, cluster, all, admins)
	case StorageTaskAddNodes, StorageTaskAddDisks, StorageTaskRemoveDisk, StorageTaskRemoveNode, StorageTaskRebalance, StorageTaskReplaceDisk,
		StorageTaskChangeRoles, StorageTaskRotateKeys, StorageTaskUpgrade:
		return gpfsChangeTaskPlan(task, scope)
	}
	return nil, planError("GPFS clusters have no task %s", task)
}

func init() {
	deploySteps := map[string]*storageStepDef{
		"precheck":      gpfsPrecheckStep,
		"join":          {Script: "stc_join.sh", Input: gpfsJoinInput},
		"fetch_package": {Script: "stc_fetch.sh", Input: gpfsFetchInput},
		"install":       {Script: "gpfs_install.sh", Input: gpfsInstallInput},
		"build_gpl":     {Script: "gpfs_build_gpl.sh"},
		"ssh_trust":     {Script: "stc_ssh_trust.sh", Input: gpfsTrustInput},
		// Run after the trust again on a retry: a key or known_hosts written wrong is the usual reason it fails
		"create_cluster": {Script: "gpfs_cluster.sh", Input: gpfsCreateClusterInput, RetryFrom: "ssh_trust"},
		"start":          {Script: "gpfs_cluster.sh", Input: gpfsActionInput("start")},
		"resolve_disks":  {Script: "stc_resolve_disks.sh", Input: gpfsResolveInput},
		"create_nsd":     {Script: "gpfs_nsd.sh", Input: gpfsNSDInput, RetryFrom: "resolve_disks"},
		// A file system needs its NSDs: a retry makes sure they are there first (both steps check what exists)
		"create_fs": {Script: "gpfs_fs.sh", Input: gpfsFSInput, RetryFrom: "resolve_disks"},
		"finish":    {Script: "stc_finish.sh", Input: gpfsFinishInput},
	}
	// The erasure code layout takes the same task with its own steps after the resolved disks (§7.9)
	for name, def := range gpfsECESteps {
		deploySteps[name] = def
	}
	registerStorageTaskKind("gpfs:"+StorageTaskDeploy, &storageTaskKind{
		Slot:   storageSlotStructural,
		Steps:  deploySteps,
		Finish: gpfsDeployFinish,
	})
	registerStorageTaskKind("gpfs:"+StorageTaskDeleteCluster, &storageTaskKind{
		Slot: storageSlotStructural,
		Steps: map[string]*storageStepDef{
			"teardown": {Script: "gpfs_cluster.sh", Input: gpfsTeardownInput},
			"leave":    {Script: "stc_leave.sh", Input: gpfsLeaveInput},
			"forget":   storageForgetStep,
		},
		Finish: gpfsDeleteFinish,
	})
}

type storageStepInput = func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error)

// gpfsPrecheckStep is the shared precheck run on the recorded cluster, plus: the package has packages for the
// Ubuntu release of every host
var gpfsPrecheckStep = &storageStepDef{
	Script: "stc_precheck.sh",
	Input: func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		cluster, nodes, disks, err := storageClusterOfTask(db, task)
		if err != nil {
			return nil, err
		}
		return precheckInput(db, clusterPrecheckPlan(db, cluster, nodes, disks, storageTaskWipe(task)), hostid)
	},
	Done: func(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
		if err := storagePrecheckStep.Done(ctx, tx, task, step, runs); err != nil {
			return err
		}
		cluster, _, _, err := storageClusterOfTask(tx, task)
		if err != nil {
			return err
		}
		pkg := &model.StoragePackage{}
		if err := tx.Take(pkg, cluster.PackageID).Error; err != nil {
			return fmt.Errorf("the package of the cluster is gone")
		}
		for _, r := range runs {
			result := &StoragePrecheckResult{}
			_ = json.Unmarshal([]byte(r.Result), result)
			osName, _ := result.Facts["os"].(string) // "ubuntu 24.04"
			fields := strings.Fields(osName)
			if len(fields) != 2 || !storagePackageDistro(pkg, fields[1]) {
				return fmt.Errorf("%s runs %s, for which package %s has no packages", hostName(tx, r.Hostid), osName, pkg.FileName)
			}
		}
		return nil
	},
}

func gpfsJoinInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	// The GPFS kernel module is built for the running kernel: hold the kernel packages, an admin upgrades them
	// after checking IBM's support matrix (§7.7, decision D5)
	return map[string]interface{}{"cluster_uuid": cluster.UUID, "kind": cluster.Kind, "hold_kernel": true}, nil
}

func gpfsClusterPackage(db *gorm.DB, task *model.StorageTask) (*model.StorageCluster, *model.StoragePackage, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, nil, err
	}
	// An upgrade installs the package of the new release; everything else the one of the cluster
	id := cluster.PackageID
	if task.Kind == StorageTaskUpgrade {
		if p := storageUpgradeOf(task); p.PackageID > 0 {
			id = p.PackageID
		}
	}
	pkg := &model.StoragePackage{}
	if err := db.Take(pkg, id).Error; err != nil {
		return nil, nil, fmt.Errorf("the package of the cluster is gone")
	}
	return cluster, pkg, nil
}

func gpfsFetchInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	_, pkg, err := gpfsClusterPackage(db, task)
	if err != nil {
		return nil, err
	}
	url, err := storagePackageURL(ctx, pkg)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"url": url, "sha256": pkg.SHA256, "size_bytes": pkg.SizeBytes, "name": pkg.FileName}, nil
}

func gpfsInstallInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, pkg, err := gpfsClusterPackage(db, task)
	if err != nil {
		return nil, err
	}
	manifest := map[string]string{}
	_ = json.Unmarshal([]byte(pkg.Manifest), &manifest)
	prefixes := gpfsDebPrefixes
	if cluster.Layout == model.StorageLayoutECE {
		prefixes += " " + gpfsECEDebPrefixes
	}
	debs := map[string]string{}
	for name, md5 := range manifest {
		// Release specific packages carry their release in the name (.U24.04); the ones here are common
		if strings.Contains(name, ".U2") || !strings.HasSuffix(name, ".deb") {
			continue
		}
		for _, prefix := range strings.Fields(prefixes) {
			if strings.HasPrefix(name, prefix) {
				debs[name] = md5
			}
		}
	}
	if len(debs) < 5 {
		return nil, fmt.Errorf("the manifest of the package lists only %d of the packages a cluster needs", len(debs))
	}
	if cluster.Layout == model.StorageLayoutECE {
		gnr := false
		for name := range debs {
			gnr = gnr || strings.HasPrefix(name, "gpfs.gnr_")
		}
		if !gnr {
			return nil, fmt.Errorf("package %s has no gpfs.gnr: the erasure code layout needs the Erasure Code Edition", pkg.FileName)
		}
	}
	return map[string]interface{}{"sha256": pkg.SHA256, "name": pkg.FileName, "payload_line": pkg.PayloadLine,
		"version": gpfsDebVersion(pkg.Version), "debs": debs}, nil
}

// gpfsPrecheckFacts reads the facts every host reported in the precheck: its SSH host key above all
func gpfsPrecheckFacts(db *gorm.DB, task *model.StorageTask) (map[int32]map[string]interface{}, error) {
	facts := map[int32]map[string]interface{}{}
	results, err := storageStepResults(db, task.ID, "precheck")
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// A task without a precheck (a change of roles): the members are remembered
		return facts, nil
	}
	if err != nil {
		return nil, err
	}
	for h, raw := range results {
		r := &StoragePrecheckResult{}
		if err := json.Unmarshal(raw, r); err == nil {
			facts[h] = r.Facts
		}
	}
	return facts, nil
}

// gpfsTrustInput: every member lets the admin hosts log in with the cluster key; the admin hosts get the private
// key and a known_hosts of the members, made from the host keys of the precheck (§6.6)
var gpfsTrustInput = storageTrustInputFrom(model.StorageRoleAdmin)

// storageTrustInputFrom builds the input of stc_ssh_trust.sh: every member lets the hosts with one of the roles log in
// with the cluster key (the admin hosts of GPFS; the admin and mgr hosts of Ceph, whose orchestrator logs in from the
// mgr); the admin hosts get the private key and a known_hosts of the members
func storageTrustInputFrom(roles ...string) storageStepInput {
	return func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		return storageTrustInput(db, task, hostid, roles)
	}
}

func storageTrustInput(db *gorm.DB, task *model.StorageTask, hostid int32, roles []string) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	return storageTrustInputKey(db, task, hostid, roles, cluster.SSHPubKey, cluster.SSHPrivKey)
}

// storageTrustInputKey is the input of stc_ssh_trust.sh for a given key pair (its private part encrypted): the
// cluster's, or the new one of a rotation
func storageTrustInputKey(db *gorm.DB, task *model.StorageTask, hostid int32, roles []string, publicKey, encryptedPrivate string) (map[string]interface{}, error) {
	cluster, nodes, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	ips, err := storageHostIPs(db, nodeHostids(nodes, ""))
	if err != nil {
		return nil, err
	}
	from := []string{}
	for _, n := range nodes {
		for _, r := range roles {
			if n.HasRole(r) {
				from = append(from, ips[n.Hostid])
				break
			}
		}
	}
	isAdmin := false
	for _, h := range nodeHostids(nodes, model.StorageRoleAdmin) {
		isAdmin = isAdmin || h == hostid
	}
	in := map[string]interface{}{"cluster_uuid": cluster.UUID, "kind": cluster.Kind, "public_key": publicKey, "from": from}
	if isAdmin {
		facts, err := gpfsPrecheckFacts(db, task)
		if err != nil {
			return nil, err
		}
		// GPFS turns the addresses of the node file back into host names and logs in by those, so every line
		// names the host by its address, the name it reported and the name CloudLand has for it
		known := []string{}
		for _, n := range nodes {
			// The hosts of this task reported their key in its precheck; the members from before are remembered
			hostFacts := facts[n.Hostid]
			if hostFacts == nil {
				hostFacts = map[string]interface{}{"host_key": nodeAttr(n, "host_key"), "hostname": nodeAttr(n, "hostname")}
			}
			key, _ := hostFacts["host_key"].(string)
			if key == "" {
				return nil, fmt.Errorf("%s reported no SSH host key in the precheck", hostName(db, n.Hostid))
			}
			names := []string{ips[n.Hostid]}
			for _, name := range []interface{}{hostFacts["hostname"], hostName(db, n.Hostid)} {
				if s, ok := name.(string); ok && s != "" && !hasRole(names, s) {
					names = append(names, s)
				}
			}
			known = append(known, strings.Join(names, ",")+" "+key)
		}
		priv, err := DecryptSecret(encryptedPrivate)
		if err != nil {
			return nil, fmt.Errorf("the cluster key can not be decrypted: %v", err)
		}
		in["private_key"] = priv
		in["known_hosts"] = known
	}
	return in, nil
}

func gpfsCreateClusterInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	ips, err := storageHostIPs(db, nodeHostids(nodes, ""))
	if err != nil {
		return nil, err
	}
	p := gpfsParamsOf(cluster)
	pagepool := p.PagepoolMiB
	// In the erasure code layout the pagepool of the servers is mmvdisk's to set for their node class; the cluster
	// default stays small for the other members
	if pagepool <= 0 || p.ece() {
		pagepool = gpfsDefaultPagepoolMiB
	}
	members := []map[string]interface{}{}
	servers, clients := []string{}, []string{}
	for _, n := range nodes {
		quorum := n.HasRole(model.StorageRoleQuorum)
		members = append(members, map[string]interface{}{"ip": ips[n.Hostid], "quorum": quorum, "manager": quorum})
		// A server license for the hosts that serve data or take part in the quorum, a client license otherwise
		if quorum || n.HasRole(model.StorageRoleNSD) || n.HasRole(model.StorageRoleAdmin) {
			servers = append(servers, ips[n.Hostid])
		} else {
			clients = append(clients, ips[n.Hostid])
		}
	}
	return map[string]interface{}{"action": "create", "cluster_uuid": cluster.UUID, "cluster_name": cluster.Name, "nodes": members,
		"server_license": servers, "client_license": clients, "pagepool_mib": pagepool}, nil
}

func gpfsActionInput(action string) storageStepInput {
	return func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		cluster, _, _, err := storageClusterOfTask(db, task)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"action": action, "cluster_uuid": cluster.UUID}, nil
	}
}

func gpfsResolveInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	wipe := map[string]bool{}
	for _, w := range storageTaskWipe(task) {
		wipe[w] = true
	}
	ids := []*storageDiskIdentity{}
	for _, d := range disks {
		// A disk on its way out is not looked for: the failed disk of a replacement can not be read
		if d.Hostid == hostid && d.Status != model.StorageDiskRemoving {
			ids = append(ids, storageClusterDiskIdentity(d, wipe[storageDiskKey(d.Hostid, d.DiskID)]))
		}
	}
	// The disks of the erasure code layout become pdisks of the recovery group, never NSDs: no nsddevices exit
	return map[string]interface{}{"cluster_uuid": cluster.UUID, "kind": cluster.Kind, "disks": ids,
		"nsddevices": cluster.Layout != model.StorageLayoutECE}, nil
}

// gpfsNSDName names the NSDs of a cluster: cl<cluster>h<hostid>d<n>, n counting the disks of the host from 1
func gpfsNSDNames(cluster *model.StorageCluster, disks []*model.StorageClusterDisk) map[int64]string {
	names := map[int64]string{}
	used := map[int32]int{}
	// A disk keeps the name it was given: a disk removed before it must not rename it
	for _, d := range disks {
		if d.Name != "" {
			names[d.ID] = d.Name
			var c, h, n int
			if _, err := fmt.Sscanf(d.Name, "cl%dh%dd%d", &c, &h, &n); err == nil && n > used[d.Hostid] {
				used[d.Hostid] = n
			}
		}
	}
	for _, d := range disks {
		if d.Name == "" {
			used[d.Hostid]++
			names[d.ID] = fmt.Sprintf("cl%dh%dd%d", cluster.ID, d.Hostid, used[d.Hostid])
		}
	}
	return names
}

// gpfsNewDisks are the disks a task brings into the cluster: claimed and not active yet
func gpfsNewDisks(disks []*model.StorageClusterDisk) []*model.StorageClusterDisk {
	out := []*model.StorageClusterDisk{}
	for _, d := range disks {
		if d.Status == model.StorageDiskClaiming {
			out = append(out, d)
		}
	}
	return out
}

func gpfsNSDInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	ips, err := storageHostIPs(db, nodeHostids(nodes, ""))
	if err != nil {
		return nil, err
	}
	resolved, err := storageStepResults(db, task.ID, "resolve_disks")
	if err != nil {
		return nil, err
	}
	devices := map[string]string{}
	for h, raw := range resolved {
		r := struct {
			Disks []struct {
				ID   string `json:"id"`
				Path string `json:"path"`
			} `json:"disks"`
		}{}
		_ = json.Unmarshal(raw, &r)
		for _, d := range r.Disks {
			devices[storageDiskKey(h, d.ID)] = d.Path
		}
	}
	groups := map[int32]int{}
	for _, n := range nodes {
		if fg, ok := nodeAttr(n, "failure_group").(float64); ok {
			groups[n.Hostid] = int(fg)
		}
	}
	names := gpfsNSDNames(cluster, disks)
	nsds := []map[string]interface{}{}
	for _, d := range gpfsNewDisks(disks) {
		dev := devices[storageDiskKey(d.Hostid, d.DiskID)]
		if dev == "" {
			return nil, fmt.Errorf("disk %s of %s was not resolved", d.DiskID, hostName(db, d.Hostid))
		}
		if groups[d.Hostid] == 0 {
			return nil, fmt.Errorf("%s has no failure group", hostName(db, d.Hostid))
		}
		nsds = append(nsds, map[string]interface{}{"name": names[d.ID], "device": dev, "server": ips[d.Hostid],
			"failure_group": groups[d.Hostid], "usage": diskAttr(d, "usage"), "pool": diskAttr(d, "gpfs_pool")})
	}
	return map[string]interface{}{"action": "create", "cluster_uuid": cluster.UUID, "nsds": nsds}, nil
}

// gpfsFileSystem is the first file system of a new cluster, from its parameters
func gpfsFileSystem(cluster *model.StorageCluster, nodes []*model.StorageClusterNode) (name, blockSize string, data, meta int) {
	p := gpfsParamsOf(cluster)
	name, blockSize = p.FsName, p.BlockSize
	if name == "" {
		name = "fs1"
	}
	if blockSize == "" {
		blockSize = "4M"
	}
	groups := 0
	for _, n := range nodes {
		if nodeAttr(n, "failure_group") != nil {
			groups++
		}
	}
	data = p.DataReplicas
	if data == 0 {
		data = 2
		if p.Test {
			data = 1
		}
	}
	// Metadata gets three copies once there are three failure groups, never fewer than the data
	meta = groups
	if meta > 3 {
		meta = 3
	}
	if meta < data {
		meta = data
	}
	return
}

func gpfsFSInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	name, blockSize, data, meta := gpfsFileSystem(cluster, nodes)
	names := gpfsNSDNames(cluster, disks)
	groups := map[int32]interface{}{}
	for _, n := range nodes {
		groups[n.Hostid] = nodeAttr(n, "failure_group")
	}
	// mmcrfs takes the usage, failure group and pool of every disk from its stanza, not from the NSD
	nsds := []map[string]interface{}{}
	for _, d := range gpfsNewDisks(disks) {
		nsds = append(nsds, map[string]interface{}{"name": names[d.ID], "usage": diskAttr(d, "usage"), "failure_group": groups[d.Hostid],
			"pool": diskAttr(d, "gpfs_pool")})
	}
	return map[string]interface{}{"action": "create", "cluster_uuid": cluster.UUID, "fs_name": name, "mount_point": gpfsMountRoot + "/" + name,
		"block_size": blockSize, "data_replicas": data, "meta_replicas": meta, "nsds": nsds}, nil
}

func gpfsFinishInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if n.Hostid == hostid {
			return map[string]interface{}{"cluster_uuid": cluster.UUID, "kind": cluster.Kind, "reserve_mb": n.ReservedMemMB}, nil
		}
	}
	return nil, fmt.Errorf("host %d is not in the cluster", hostid)
}

// gpfsDeployFinish records the deployed cluster: its id, the file system, the NSD names, every host and disk active
func gpfsDeployFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	cluster, nodes, disks, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	if !succeeded {
		return tx.Model(cluster).Update("status", model.StorageClusterError).Error
	}
	if cluster.Layout == model.StorageLayoutECE {
		return gpfsECEDeployFinish(ctx, tx, task, cluster, nodes, disks)
	}
	name, blockSize, data, meta := gpfsFileSystem(cluster, nodes)
	fs := &model.StorageFilesystem{ClusterID: cluster.ID, Name: name, MountPoint: gpfsMountRoot + "/" + name, BlockSize: blockSize,
		DataReplicas: int32(data), MetaReplicas: int32(meta), Status: "ready"}
	return gpfsDeployDone(ctx, tx, task, cluster, nodes, disks, &gpfsDeployLayout{fs: fs, fsStep: "create_fs", diskNames: gpfsNSDNames(cluster, disks)})
}

// gpfsDeployLayout is what a layout gives the end of a deployment: the file system (name, mount point, block size,
// replicas), the step whose result has its capacity, the names of the disks and attrs to add to the cluster
type gpfsDeployLayout struct {
	fs        *model.StorageFilesystem
	fsStep    string
	diskNames map[int64]string
	attrs     map[string]interface{}
}

// gpfsDeployDone is how every managed GPFS deployment ends, whatever its layout: the file system recorded (the row
// a run before made is taken, so the disks get its ID), the disks active under their names, the nodes active, the
// host keys remembered, the cluster ready with its reference, and the hosts scanned once the transaction is in
func gpfsDeployDone(ctx context.Context, tx *gorm.DB, task *model.StorageTask, cluster *model.StorageCluster,
	nodes []*model.StorageClusterNode, disks []*model.StorageClusterDisk, l *gpfsDeployLayout) error {
	ref := ""
	if res, err := storageStepResults(tx, task.ID, "create_cluster"); err == nil {
		for _, raw := range res {
			r := struct {
				ClusterName string `json:"cluster_name"`
				ClusterID   string `json:"cluster_id"`
			}{}
			if json.Unmarshal(raw, &r) == nil && r.ClusterID != "" {
				ref = r.ClusterName + " " + r.ClusterID
			}
		}
	}
	fs := l.fs
	if res, err := storageStepResults(tx, task.ID, l.fsStep); err == nil {
		for _, raw := range res {
			r := struct {
				CapacityBytes int64 `json:"capacity_bytes"`
				FreeBytes     int64 `json:"free_bytes"`
			}{}
			if json.Unmarshal(raw, &r) == nil && r.CapacityBytes > 0 {
				fs.CapacityBytes, fs.FreeBytes, fs.CapacityAt = r.CapacityBytes, r.FreeBytes, storageNow()
			}
		}
	}
	var existing int64
	tx.Model(&model.StorageFilesystem{}).Where("cluster_id = ? AND name = ?", cluster.ID, fs.Name).Count(&existing)
	if existing == 0 {
		if err := tx.Create(fs).Error; err != nil {
			return err
		}
	} else if err := tx.Where("cluster_id = ? AND name = ?", cluster.ID, fs.Name).Take(fs).Error; err != nil {
		return err
	}
	for _, d := range disks {
		if err := tx.Model(d).Updates(map[string]interface{}{"status": model.StorageDiskActive, "name": l.diskNames[d.ID], "fs_id": fs.ID}).Error; err != nil {
			return err
		}
	}
	if err := tx.Model(&model.StorageClusterNode{}).Where("cluster_id = ?", cluster.ID).Update("status", model.StorageNodeActive).Error; err != nil {
		return err
	}
	if err := gpfsRememberHosts(tx, task, nodes); err != nil {
		return err
	}
	updates := map[string]interface{}{"status": model.StorageClusterReady, "cluster_ref": ref}
	if len(l.attrs) > 0 {
		attrs := map[string]interface{}{}
		_ = json.Unmarshal([]byte(cluster.Attrs), &attrs)
		for k, v := range l.attrs {
			attrs[k] = v
		}
		updates["attrs"] = jsonAttrs(attrs)
	}
	if err := tx.Model(cluster).Updates(updates).Error; err != nil {
		return err
	}
	// The disks now show as GPFS disks: scan once the transaction is in
	go func(ctx context.Context, ids []int32) {
		time.Sleep(3 * time.Second)
		scanStorageHosts(ctx, ids)
	}(context.WithoutCancel(ctx), nodeHostids(nodes, ""))
	return nil
}

// gpfsRememberHosts keeps the SSH host key and name every host of a task reported in its precheck, for the
// known_hosts of the admin hosts when hosts join later
func gpfsRememberHosts(tx *gorm.DB, task *model.StorageTask, nodes []*model.StorageClusterNode) error {
	facts, err := gpfsPrecheckFacts(tx, task)
	if err != nil {
		return nil
	}
	for _, n := range nodes {
		f := facts[n.Hostid]
		if f == nil {
			continue
		}
		attrs := map[string]interface{}{}
		_ = json.Unmarshal([]byte(n.Attrs), &attrs)
		for _, k := range []string{"host_key", "hostname"} {
			if v, ok := f[k].(string); ok && v != "" {
				attrs[k] = v
			}
		}
		if err := tx.Model(&model.StorageClusterNode{}).Where("id = ?", n.ID).Update("attrs", jsonAttrs(attrs)).Error; err != nil {
			return err
		}
	}
	return nil
}

func gpfsTeardownInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	fss := []*model.StorageFilesystem{}
	db.Where("cluster_id = ?", cluster.ID).Find(&fss)
	fsNames := []string{}
	for _, f := range fss {
		fsNames = append(fsNames, f.Name)
	}
	in := map[string]interface{}{"action": "teardown", "cluster_uuid": cluster.UUID, "cluster_name": cluster.Name, "filesystems": fsNames}
	if cluster.Layout == model.StorageLayoutECE {
		// The disks are pdisks of the recovery group, which goes with the erasure code layer
		in["nsds"], in["ece"] = []string{}, gpfsECETeardown(cluster)
		return in, nil
	}
	names := gpfsNSDNames(cluster, disks)
	nsds := []string{}
	for _, d := range disks {
		nsds = append(nsds, names[d.ID])
	}
	in["nsds"] = nsds
	return in, nil
}

func gpfsLeaveInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	p := struct {
		Purge bool `json:"purge_packages"`
	}{}
	_ = json.Unmarshal([]byte(task.Params), &p)
	wipe := []*storageDiskIdentity{}
	for _, d := range disks {
		if d.Hostid == hostid {
			wipe = append(wipe, storageClusterDiskIdentity(d, true))
		}
	}
	return map[string]interface{}{"cluster_uuid": cluster.UUID, "kind": cluster.Kind, "wipe": wipe, "purge": p.Purge}, nil
}

// gpfsDeleteFinish removes the records of a deleted cluster; its disks were wiped and show free after a scan
func gpfsDeleteFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	cluster, nodes, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	if !succeeded {
		return tx.Model(cluster).Update("status", model.StorageClusterError).Error
	}
	// The fences go with the cluster: nothing is left to keep a host off
	for _, m := range []interface{}{&model.StorageFilesystem{}, &model.StorageClusterDisk{}, &model.StorageClusterNode{}, &model.StorageFence{}} {
		if err := tx.Where("cluster_id = ?", cluster.ID).Delete(m).Error; err != nil {
			return err
		}
	}
	if err := tx.Model(cluster).Updates(map[string]interface{}{"active_task": 0, "status": model.StorageClusterDeleting}).Error; err != nil {
		return err
	}
	if err := tx.Delete(cluster).Error; err != nil {
		return err
	}
	go func(ctx context.Context, ids []int32) {
		time.Sleep(3 * time.Second)
		scanStorageHosts(ctx, ids)
	}(context.WithoutCancel(ctx), nodeHostids(nodes, ""))
	return nil
}

// gpfsDeployPrefix is how a managed GPFS cluster starts, whatever its layout: the packages on every host, the cluster
// made and started, the disks of the NSD hosts resolved. The layout adds its own steps after it, and finish
func gpfsDeployPrefix(all, admins, nsds []int32) []*StorageStepPlan {
	step := func(name, scope string, hosts []int32, timeout time.Duration) *StorageStepPlan {
		return &StorageStepPlan{Name: name, Scope: scope, Hostids: hosts, Timeout: timeout}
	}
	n, a := model.StorageStepScopeNodes, model.StorageStepScopeAdmin
	return []*StorageStepPlan{
		step("precheck", n, all, 10*time.Minute),
		step("join", n, all, 10*time.Minute),
		step("fetch_package", n, all, 30*time.Minute),
		step("install", n, all, 30*time.Minute),
		step("build_gpl", n, all, 15*time.Minute),
		step("ssh_trust", n, all, 5*time.Minute),
		step("create_cluster", a, admins, 15*time.Minute),
		step("start", a, admins, 15*time.Minute),
		step("resolve_disks", n, nsds, 5*time.Minute),
	}
}
