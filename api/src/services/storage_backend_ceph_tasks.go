/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The tasks of the Ceph backend (shared-storage-design.md §8): deploying a managed cluster with cephadm (§8.2),
// deleting it (§8.6), importing one its admins run (§8.8), and changing it (§8.5). Inputs are built from the database
// when a step starts; what a step finds out that later steps need (the image and release, the mon addresses, the
// client key, the OSD ids) is recorded by its Done hook in the cluster and its disks.

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

// cephOrchLabels are the cephadm labels of a host with roles; a host with none (a client) is not a cephadm host
func cephOrchLabels(n *model.StorageClusterNode) []string {
	labels := []string{}
	for _, r := range []string{model.StorageRoleMon, model.StorageRoleMgr, model.StorageRoleOSD} {
		if n.HasRole(r) {
			labels = append(labels, r)
		}
	}
	if n.HasRole(model.StorageRoleAdmin) {
		labels = append(labels, "_admin")
	}
	return labels
}

// cephAdmins are the admin hosts a step of a task may run on: for a deployment the first admin only, the one that
// bootstraps and has the admin keyring until cephadm hands it to the others; afterwards any working admin
func cephAdmins(task string, nodes []*model.StorageClusterNode) []int32 {
	switch task {
	case StorageTaskDeploy:
		admins := nodeHostids(nodes, model.StorageRoleAdmin)
		if len(admins) > 0 {
			return admins[:1]
		}
		return nil
	case StorageTaskDeleteCluster:
		// A deployment stopped half way leaves its hosts joining: any admin host stops what may run
		return nodeHostids(nodes, model.StorageRoleAdmin)
	}
	return gpfsWorkingAdmins(nodes)
}

func (cephBackend) TaskPlan(task string, cluster *model.StorageCluster, scope *StorageTaskScope) ([]*StorageStepPlan, error) {
	nodes := scope.Nodes
	all := nodeHostids(nodes, "")
	admins := cephAdmins(task, nodes)
	step := func(name, s string, hosts []int32, timeout time.Duration) *StorageStepPlan {
		return &StorageStepPlan{Name: name, Scope: s, Hostids: hosts, Timeout: timeout}
	}
	n, a := model.StorageStepScopeNodes, model.StorageStepScopeAdmin
	if cluster.Mode == model.StorageModeExternal {
		switch task {
		case StorageTaskImport:
			return []*StorageStepPlan{step("check_import", n, all, 10*time.Minute)}, nil
		case StorageTaskDeleteCluster:
			return []*StorageStepPlan{step("forget", n, all, 10*time.Minute)}, nil
		case StorageTaskCreatePool, StorageTaskDeletePool:
			return cephPoolTaskPlan(task, cluster, all, all)
		}
		return nil, planError("An imported Ceph cluster has no task %s", task)
	}
	if len(admins) == 0 {
		return nil, planError("The cluster has no admin host")
	}
	osdHosts := gpfsDiskHosts(scope.Disks, model.StorageDiskClaiming)
	switch task {
	case StorageTaskDeploy:
		plan := []*StorageStepPlan{
			step("precheck", n, all, 10*time.Minute),
			step("join", n, all, 10*time.Minute),
			step("install", n, all, 45*time.Minute),
			step("ssh_trust", n, all, 5*time.Minute),
			step("bootstrap", a, admins, 30*time.Minute),
			step("add_hosts", a, admins, 30*time.Minute),
			step("configure", a, admins, 10*time.Minute),
		}
		if len(osdHosts) > 0 {
			plan = append(plan, step("resolve_disks", n, osdHosts, 5*time.Minute), step("create_osds", a, admins, 60*time.Minute))
		}
		return append(plan, step("client_setup", n, all, 10*time.Minute), step("finish", n, all, 5*time.Minute)), nil
	case StorageTaskDeleteCluster:
		return []*StorageStepPlan{step("teardown", a, admins, 10*time.Minute), step("leave", n, all, 30*time.Minute)}, nil
	case StorageTaskCreatePool, StorageTaskUpdatePool, StorageTaskDeletePool:
		return cephPoolTaskPlan(task, cluster, all, admins)
	case StorageTaskAddNodes:
		joining := gpfsHostsBy(nodes, model.StorageNodeJoining, "")
		if len(joining) == 0 {
			return nil, planError("No host joins the cluster")
		}
		plan := []*StorageStepPlan{
			step("precheck", n, joining, 10*time.Minute),
			step("join", n, joining, 10*time.Minute),
			step("install", n, joining, 45*time.Minute),
			// Every member: the new hosts let the admin and mgr hosts in
			step("ssh_trust", n, all, 5*time.Minute),
			step("add_hosts", a, admins, 30*time.Minute),
		}
		if len(osdHosts) > 0 {
			plan = append(plan, step("resolve_disks", n, osdHosts, 5*time.Minute), step("create_osds", a, admins, 60*time.Minute))
		}
		return append(plan, step("client_setup", n, joining, 10*time.Minute), step("finish", n, joining, 5*time.Minute)), nil
	case StorageTaskAddDisks:
		if len(osdHosts) == 0 {
			return nil, planError("No disk joins the cluster")
		}
		return []*StorageStepPlan{step("resolve_disks", n, osdHosts, 5*time.Minute), step("create_osds", a, admins, 60*time.Minute)}, nil
	case StorageTaskRemoveDisk:
		hosts := gpfsDiskHosts(scope.Disks, model.StorageDiskRemoving)
		if len(hosts) == 0 {
			return nil, planError("No disk leaves the cluster")
		}
		// Ceph moves the data of an OSD before it goes (ceph orch osd rm), which takes as long as it takes
		return []*StorageStepPlan{step("remove_osds", a, admins, 7*24*time.Hour), step("release_disks", n, hosts, 30*time.Minute)}, nil
	case StorageTaskReplaceDisk:
		old := gpfsDiskHosts(scope.Disks, model.StorageDiskRemoving)
		if len(osdHosts) != 1 || len(old) != 1 || osdHosts[0] != old[0] {
			return nil, planError("One disk replaces one disk of the same host")
		}
		// The OSD of the failed disk is destroyed keeping its id, the new disk takes it; the failed disk is not wiped
		return []*StorageStepPlan{
			step("resolve_disks", n, osdHosts, 5*time.Minute),
			step("replace_osd", a, admins, 60*time.Minute),
			step("release_disks", n, old, 10*time.Minute),
		}, nil
	case StorageTaskRotateKeys:
		return cephRotatePlan(scope, all, admins), nil
	case StorageTaskUpgrade:
		// The hosts install the new release of their distribution (librbd too, which QEMU loads when it starts),
		// then cephadm upgrades the daemons one after the other; no instance has to move
		return []*StorageStepPlan{step("install", n, all, 45*time.Minute), step("upgrade", a, admins, 12*time.Hour)}, nil
	case StorageTaskChangeRoles:
		run := changeRolesAdmins(admins, scope)
		if len(run) == 0 {
			return nil, planError("No admin host besides the one that changes is left to run the change")
		}
		plan := []*StorageStepPlan{}
		// A client installed for the client only gets what a cephadm host needs first (cephadm, the image, the user
		// of the daemons, the order of the units at shutdown) when it gets its first daemon role
		var changed *model.StorageClusterNode
		for _, nd := range nodes {
			if nd.Hostid == scope.Changed {
				changed = nd
			}
		}
		before := &model.StorageClusterNode{Roles: strings.Join(scope.ChangedFrom, ",")}
		if changed != nil && len(cephOrchLabels(before)) == 0 && len(cephOrchLabels(changed)) > 0 {
			plan = append(plan, step("install", n, []int32{scope.Changed}, 45*time.Minute))
		}
		// The mgr and admin hosts may log in to every host (ssh_trust on every member), then the labels change and
		// cephadm places the mons and mgrs by them
		return append(plan,
			step("ssh_trust", n, all, 5*time.Minute),
			step("set_labels", a, run, 30*time.Minute),
			step("finish", n, []int32{scope.Changed}, 5*time.Minute)), nil
	case StorageTaskRemoveNode:
		leaving := gpfsHostsBy(nodes, model.StorageNodeLeaving, "")
		if len(leaving) != 1 {
			return nil, planError("One host leaves at a time")
		}
		plan := []*StorageStepPlan{step("remove_host", a, admins, 7*24*time.Hour)}
		if !scope.Offline {
			plan = append(plan, step("leave", n, leaving, 30*time.Minute))
		}
		return plan, nil
	}
	return nil, planError("Ceph clusters have no task %s", task)
}

func init() {
	deploySteps := func() map[string]*storageStepDef {
		return map[string]*storageStepDef{
			"precheck":  cephPrecheckStep,
			"join":      {Script: "stc_join.sh", Input: cephJoinInput},
			"install":   {Script: "ceph_install.sh", Input: cephInstallInput, Done: cephInstallDone},
			"ssh_trust": {Script: "stc_ssh_trust.sh", Input: storageTrustInputFrom(model.StorageRoleAdmin, model.StorageRoleMgr)},
			// A retry starts from the trust: a key written wrong is the usual reason bootstrap can not log in
			"bootstrap":     {Script: "ceph_cluster.sh", Input: cephBootstrapInput, RetryFrom: "ssh_trust"},
			"add_hosts":     {Script: "ceph_cluster.sh", Input: cephAddHostsInput, RetryFrom: "ssh_trust"},
			"configure":     {Script: "ceph_cluster.sh", Input: cephConfigureInput, Done: cephConfigureDone},
			"resolve_disks": {Script: "stc_resolve_disks.sh", Input: gpfsResolveInput},
			"create_osds":   {Script: "ceph_cluster.sh", Input: cephCreateOsdsInput, Done: cephCreateOsdsDone, RetryFrom: "resolve_disks"},
			"client_setup":  {Script: "ceph_client.sh", Input: cephClientInput("setup"), Done: cephClientSetupDone},
			"finish":        {Script: "stc_finish.sh", Input: gpfsFinishInput},
		}
	}
	registerStorageTaskKind("ceph:"+StorageTaskDeploy, &storageTaskKind{Slot: storageSlotStructural, Steps: deploySteps(), Finish: cephDeployFinish})
	registerStorageTaskKind("ceph:"+StorageTaskAddNodes, &storageTaskKind{Slot: storageSlotStructural, Steps: deploySteps(), Finish: cephExpandFinish})
	registerStorageTaskKind("ceph:"+StorageTaskAddDisks, &storageTaskKind{Slot: storageSlotStructural, Steps: deploySteps(), Finish: cephExpandFinish})
	registerStorageTaskKind("ceph:"+StorageTaskDeleteCluster, &storageTaskKind{
		Slot: storageSlotStructural,
		Steps: map[string]*storageStepDef{
			"teardown": {Script: "ceph_cluster.sh", Input: cephActionInput("teardown")},
			"leave":    {Script: "stc_leave.sh", Input: gpfsLeaveInput},
			"forget":   storageForgetStep,
		},
		Finish: gpfsDeleteFinish,
	})
	registerStorageTaskKind("ceph:"+StorageTaskImport, &storageTaskKind{
		Slot:   storageSlotNone,
		Steps:  map[string]*storageStepDef{"check_import": {Script: "ceph_client.sh", Input: cephClientInput("import")}},
		Finish: cephImportFinish,
	})
	registerStorageTaskKind("ceph:"+StorageTaskRemoveDisk, &storageTaskKind{Slot: storageSlotStructural, Finish: gpfsRemoveDiskFinish,
		Steps: map[string]*storageStepDef{
			"remove_osds":   {Script: "ceph_cluster.sh", Input: cephRemoveOsdsInput},
			"release_disks": {Script: "stc_release_disks.sh", Input: gpfsReleaseDisksInput},
		}})
	registerStorageTaskKind("ceph:"+StorageTaskReplaceDisk, &storageTaskKind{Slot: storageSlotStructural, Finish: storageReplaceFinish(cephExpandFinish),
		Steps: map[string]*storageStepDef{
			"resolve_disks": {Script: "stc_resolve_disks.sh", Input: gpfsResolveInput},
			"replace_osd":   {Script: "ceph_cluster.sh", Input: cephReplaceOsdInput, Done: cephCreateOsdsDone, RetryFrom: "resolve_disks"},
			"release_disks": {Script: "stc_release_disks.sh", Input: storageReplaceReleaseInput},
		}})
	registerStorageTaskKind("ceph:"+StorageTaskChangeRoles, &storageTaskKind{Slot: storageSlotStructural, Finish: storageChangeRolesFinish,
		Steps: map[string]*storageStepDef{
			"ssh_trust":  {Script: "stc_ssh_trust.sh", Input: storageTrustInputFrom(model.StorageRoleAdmin, model.StorageRoleMgr)},
			"install":    {Script: "ceph_install.sh", Input: cephInstallInput, Done: cephInstallDone},
			"set_labels": {Script: "ceph_cluster.sh", Input: cephSetLabelsInput, RetryFrom: "ssh_trust"},
			"finish":     {Script: "stc_finish.sh", Input: gpfsFinishInput},
		}})
	registerStorageTaskKind("ceph:"+StorageTaskRemoveNode, &storageTaskKind{Slot: storageSlotStructural, Finish: gpfsRemoveNodeFinish,
		Steps: map[string]*storageStepDef{
			"remove_host": {Script: "ceph_cluster.sh", Input: cephRemoveHostInput},
			"leave":       {Script: "stc_leave.sh", Input: gpfsLeaveInput},
		}})
}

// cephPrecheckStep is the shared precheck, plus: every host runs the same Ubuntu release, so the cephadm and
// ceph-common it installs from its distribution are one Ceph release (§8.1)
var cephPrecheckStep = &storageStepDef{
	Script: "stc_precheck.sh",
	Input:  gpfsPrecheckStep.Input,
	Done: func(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
		if err := storagePrecheckStep.Done(ctx, tx, task, step, runs); err != nil {
			return err
		}
		releases := map[string][]string{}
		for _, r := range runs {
			result := &StoragePrecheckResult{}
			_ = json.Unmarshal([]byte(r.Result), result)
			osName, _ := result.Facts["os"].(string)
			releases[osName] = append(releases[osName], hostName(tx, r.Hostid))
		}
		// A host joining an existing cluster must run what the cluster's hosts run
		cluster, nodes, _, err := storageClusterOfTask(tx, task)
		if err != nil {
			return err
		}
		if facts, err := gpfsPrecheckFacts(tx, task); err == nil {
			for _, n := range nodes {
				if os, ok := nodeAttr(n, "os").(string); ok && os != "" && facts[n.Hostid] == nil {
					releases[os] = append(releases[os], hostName(tx, n.Hostid))
				}
			}
		}
		if len(releases) > 1 {
			parts := []string{}
			for os, hosts := range releases {
				parts = append(parts, fmt.Sprintf("%s on %s", os, strings.Join(hosts, ", ")))
			}
			sort.Strings(parts)
			return fmt.Errorf("the hosts of Ceph cluster %s run different releases (%s): each installs its own Ceph release from its distribution", cluster.Name,
				strings.Join(parts, "; "))
		}
		return nil
	},
}

func cephJoinInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	// The daemons run in containers: the kernel is not tied to the storage, nothing to hold
	return map[string]interface{}{"cluster_uuid": cluster.UUID, "kind": cluster.Kind, "hold_kernel": false}, nil
}

func cephParamsOf(cluster *model.StorageCluster) *cephParams {
	p := &cephParams{}
	_ = json.Unmarshal([]byte(cluster.Params), p)
	return p
}

// cephInstallInput: the packages from the distribution, then, on a cephadm host, the image of the daemons. A host
// joining later pulls the image the cluster runs. A client host only gets the client packages
func cephInstallInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	image := cephParamsOf(cluster).Image
	if info := cephInfoOf(cluster); info.Image != "" {
		image = info.Image
	}
	orch := false
	for _, n := range nodes {
		if n.Hostid == hostid {
			orch = len(cephOrchLabels(n)) > 0
		}
	}
	return map[string]interface{}{"cluster_uuid": cluster.UUID, "image": image, "orch": orch}, nil
}

// cephInstallDone records the release and the image the hosts installed: one release everywhere, one image on the
// cephadm hosts, or the step fails
func cephInstallDone(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
	cluster, _, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	info := cephInfoOf(cluster)
	version, image := info.Version, info.Image
	for _, r := range runs {
		res := struct {
			Version string `json:"version"`
			Image   string `json:"image"`
		}{}
		_ = json.Unmarshal([]byte(r.Result), &res)
		if res.Version == "" {
			return fmt.Errorf("%s reported no Ceph release", hostName(tx, r.Hostid))
		}
		if version != "" && res.Version != version {
			return fmt.Errorf("%s installed Ceph %s, the cluster runs %s", hostName(tx, r.Hostid), res.Version, version)
		}
		if res.Image != "" && image != "" && res.Image != image {
			return fmt.Errorf("%s pulled image %s, the cluster runs %s", hostName(tx, r.Hostid), res.Image, image)
		}
		version = res.Version
		if res.Image != "" {
			image = res.Image
		}
	}
	if image == "" {
		return fmt.Errorf("no cephadm host pulled the image of the daemons")
	}
	info.Version, info.Image = version, image
	return cephSaveInfo(tx, cluster, info, map[string]interface{}{"version": version})
}

// cephSaveInfo writes what the backend knows of a cluster to storage_clusters.attrs, with other columns
func cephSaveInfo(tx *gorm.DB, cluster *model.StorageCluster, info *cephClusterInfo, updates map[string]interface{}) error {
	b, _ := json.Marshal(info)
	if updates == nil {
		updates = map[string]interface{}{}
	}
	updates["attrs"] = string(b)
	cluster.Attrs = string(b)
	return tx.Model(&model.StorageCluster{}).Where("id = ?", cluster.ID).Updates(updates).Error
}

// cephHostFacts are the IP addresses and host names of the hosts of a task: the host name cephadm knows a host by is
// the one it reported in a precheck (now, or when it joined)
func cephHostFacts(db *gorm.DB, task *model.StorageTask, nodes []*model.StorageClusterNode) (ips map[int32]string, names map[int32]string, err error) {
	if ips, err = storageHostIPs(db, nodeHostids(nodes, "")); err != nil {
		return
	}
	facts, _ := gpfsPrecheckFacts(db, task)
	names = map[int32]string{}
	for _, n := range nodes {
		name, _ := nodeAttr(n, "hostname").(string)
		if f := facts[n.Hostid]; f != nil {
			if s, ok := f["hostname"].(string); ok && s != "" {
				name = s
			}
		}
		if name == "" {
			return nil, nil, fmt.Errorf("%s reported no host name in a precheck", hostName(db, n.Hostid))
		}
		names[n.Hostid] = name
	}
	return
}

func cephBootstrapInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	ips, names, err := cephHostFacts(db, task, nodes)
	if err != nil {
		return nil, err
	}
	var self *model.StorageClusterNode
	for _, n := range nodes {
		if n.Hostid == hostid {
			self = n
		}
	}
	if self == nil || !self.HasRole(model.StorageRoleMon) {
		return nil, fmt.Errorf("%s is not a mon host of the cluster", hostName(db, hostid))
	}
	p := cephParamsOf(cluster)
	info := cephInfoOf(cluster)
	orch := 0
	for _, n := range nodes {
		if len(cephOrchLabels(n)) > 0 {
			orch++
		}
	}
	return map[string]interface{}{"action": "bootstrap", "cluster_uuid": cluster.UUID, "fsid": info.Fsid, "mon_ip": ips[hostid],
		"hostname": names[hostid], "labels": cephOrchLabels(self), "image": info.Image, "cluster_network": p.ClusterNetwork,
		"single_host": orch == 1}, nil
}

// cephAddHostsInput: every cephadm host of the cluster with its labels (the ones there are skipped, labels are set
// again), and the daemons placed by label
func cephAddHostsInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	ips, names, err := cephHostFacts(db, task, nodes)
	if err != nil {
		return nil, err
	}
	hosts := []map[string]interface{}{}
	mons := 0
	for _, n := range nodes {
		labels := cephOrchLabels(n)
		if len(labels) == 0 || n.Status == model.StorageNodeLeaving {
			continue
		}
		if n.HasRole(model.StorageRoleMon) {
			mons++
		}
		hosts = append(hosts, map[string]interface{}{"hostname": names[n.Hostid], "ip": ips[n.Hostid], "labels": labels})
	}
	return map[string]interface{}{"action": "add_hosts", "cluster_uuid": cluster.UUID, "fsid": cephInfoOf(cluster).Fsid, "hosts": hosts, "mons": mons}, nil
}

func cephConfigureInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	p := cephParamsOf(cluster)
	return map[string]interface{}{"action": "configure", "cluster_uuid": cluster.UUID, "fsid": cephInfoOf(cluster).Fsid,
		"replicas": cephReplicas(p), "osd_memory_target": int64(cephOsdMemoryMiB(p)) << 20, "cluster_network": p.ClusterNetwork,
		"client_user": cephClientUser}, nil
}

// cephConfigureDone keeps the mon addresses with the cluster and its client key encrypted; the key is then dropped
// from the result of the run, which is stored in clear
func cephConfigureDone(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
	cluster, _, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	for _, r := range runs {
		res := map[string]interface{}{}
		if json.Unmarshal([]byte(r.Result), &res) != nil {
			continue
		}
		key, _ := res["client_key"].(string)
		mons := []string{}
		if list, ok := res["mon_addrs"].([]interface{}); ok {
			for _, m := range list {
				if s, ok := m.(string); ok {
					mons = append(mons, s)
				}
			}
		}
		if !cephKeyRe.MatchString(key) || len(mons) == 0 {
			return fmt.Errorf("the configuration of the cluster reported no client key or mon address")
		}
		secrets, err := storageClusterSecrets(cluster)
		if err != nil {
			return err
		}
		secrets["client_key"] = key
		enc, err := encryptStorageSecrets(secrets)
		if err != nil {
			return err
		}
		info := cephInfoOf(cluster)
		info.MonAddrs = mons
		if err := cephSaveInfo(tx, cluster, info, map[string]interface{}{"secrets": enc, "cluster_ref": info.Fsid}); err != nil {
			return err
		}
		delete(res, "client_key")
		redacted, _ := json.Marshal(res)
		if err := tx.Model(&model.StorageTaskRun{}).Where("id = ?", r.ID).Update("result", string(redacted)).Error; err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("the configuration step has no result")
}

// cephResolvedDisk is what the resolve_disks step found for a disk: the device the OSD goes on (a logical volume on a
// loop device) and the kernel name of the disk, which the OSD reports as its device
type cephResolvedDisk struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Name string `json:"name"`
}

// cephDiskPaths are the devices the disks of the hosts resolved to in the resolve_disks step of a task
func cephDiskPaths(db *gorm.DB, task *model.StorageTask) (map[string]*cephResolvedDisk, error) {
	resolved, err := storageStepResults(db, task.ID, "resolve_disks")
	if err != nil {
		return nil, err
	}
	devices := map[string]*cephResolvedDisk{}
	for h, raw := range resolved {
		r := struct {
			Disks []*cephResolvedDisk `json:"disks"`
		}{}
		_ = json.Unmarshal(raw, &r)
		for _, d := range r.Disks {
			devices[storageDiskKey(h, d.ID)] = d
		}
	}
	return devices, nil
}

// cephCreateOsdsInput: an OSD on every disk the task brings, on the device it resolved to, with the media it was
// claimed with as its device class
func cephCreateOsdsInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	_, names, err := cephHostFacts(db, task, nodes)
	if err != nil {
		return nil, err
	}
	devices, err := cephDiskPaths(db, task)
	if err != nil {
		return nil, err
	}
	osds := []map[string]interface{}{}
	for _, d := range gpfsNewDisks(disks) {
		key := storageDiskKey(d.Hostid, d.DiskID)
		dev := devices[key]
		if dev == nil || dev.Path == "" || dev.Name == "" {
			return nil, fmt.Errorf("disk %s of %s was not resolved", d.DiskID, hostName(db, d.Hostid))
		}
		osds = append(osds, map[string]interface{}{"key": key, "hostname": names[d.Hostid], "device": dev.Path, "kname": dev.Name,
			"media": d.Media})
	}
	return map[string]interface{}{"action": "create_osds", "cluster_uuid": cluster.UUID, "fsid": cephInfoOf(cluster).Fsid, "osds": osds}, nil
}

// cephCreateOsdsDone records the OSD id of every disk
func cephCreateOsdsDone(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
	_, _, disks, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	ids := map[string]int{}
	for _, r := range runs {
		res := struct {
			Osds []struct {
				Key string `json:"key"`
				ID  int    `json:"osd_id"`
			} `json:"osds"`
		}{}
		_ = json.Unmarshal([]byte(r.Result), &res)
		for _, o := range res.Osds {
			ids[o.Key] = o.ID
		}
	}
	for _, d := range gpfsNewDisks(disks) {
		id, ok := ids[storageDiskKey(d.Hostid, d.DiskID)]
		if !ok {
			return fmt.Errorf("no OSD was reported for disk %s of %s", d.DiskID, hostName(tx, d.Hostid))
		}
		attrs := map[string]interface{}{}
		_ = json.Unmarshal([]byte(d.Attrs), &attrs)
		attrs["osd_id"] = id
		if err := tx.Model(&model.StorageClusterDisk{}).Where("id = ?", d.ID).
			Updates(map[string]interface{}{"attrs": jsonAttrs(attrs), "name": fmt.Sprintf("osd.%d", id)}).Error; err != nil {
			return err
		}
	}
	return nil
}

// cephClientInput: the client configuration of a host (§8.4): the minimal configuration made from the fsid and the
// mon addresses, the keyring of the client user, and the libvirt secret holding its key. "import" also checks the
// host reaches the cluster with it
func cephClientInput(action string) storageStepInput {
	return func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		cluster, _, _, err := storageClusterOfTask(db, task)
		if err != nil {
			return nil, err
		}
		info := cephInfoOf(cluster)
		key, err := cephClientKey(cluster)
		if err != nil {
			return nil, err
		}
		in := map[string]interface{}{"action": action, "cluster_uuid": cluster.UUID, "fsid": info.Fsid, "mon_addrs": info.MonAddrs,
			"client_user": info.ClientUser, "client_key": key, "secret_uuid": info.SecretUUID}
		// A pending key left by an aborted rotation may have become the key (storageRotateFinish): the host takes it only
		// when the cluster refuses the recorded one, and says so (cephClientSetupDone)
		if action == "setup" {
			if secrets, err := storageClusterSecrets(cluster); err == nil && cephKeyRe.MatchString(secrets["client_key_pending"]) {
				in["client_key_alt"] = secrets["client_key_pending"]
			}
		}
		return in, nil
	}
}

// cephClientSetupDone: a host that had to take the pending key of an aborted rotation shows it is the key now
func cephClientSetupDone(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
	took := false
	for _, r := range runs {
		res := map[string]interface{}{}
		if json.Unmarshal([]byte(r.Result), &res) == nil && res["key"] == "alt" {
			took = true
		}
	}
	if !took {
		return nil
	}
	cluster, _, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	secrets, err := storageClusterSecrets(cluster)
	if err != nil {
		return err
	}
	if !cephKeyRe.MatchString(secrets["client_key_pending"]) {
		return nil
	}
	logger.Ctx(ctx).Warningf("Storage cluster %d refuses its recorded client key: the pending key of an aborted rotation is the key", cluster.ID)
	secrets["client_key"] = secrets["client_key_pending"]
	delete(secrets, "client_key_pending")
	return storageClusterSaveSecrets(tx, cluster, secrets)
}

func cephActionInput(action string) storageStepInput {
	return func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		cluster, _, _, err := storageClusterOfTask(db, task)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"action": action, "cluster_uuid": cluster.UUID, "fsid": cephInfoOf(cluster).Fsid}, nil
	}
}

// cephOsdID is the OSD id recorded for a disk, -1 when it has none
func cephOsdID(d *model.StorageClusterDisk) int {
	attrs := map[string]interface{}{}
	_ = json.Unmarshal([]byte(d.Attrs), &attrs)
	if id, ok := attrs["osd_id"].(float64); ok {
		return int(id)
	}
	return -1
}

// cephRemoveOsdsInput: the OSDs leaving; their data moves to the others first, or, when their host is gone for good,
// they are purged and Ceph recovers the copies from the other hosts
func cephRemoveOsdsInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	ids := []int{}
	for _, d := range disks {
		if d.Status == model.StorageDiskRemoving {
			if id := cephOsdID(d); id >= 0 {
				ids = append(ids, id)
			}
		}
	}
	return map[string]interface{}{"action": "remove_osds", "cluster_uuid": cluster.UUID, "fsid": cephInfoOf(cluster).Fsid, "osd_ids": ids,
		"offline": storageTaskBool(task, "offline")}, nil
}

// cephReplaceOsdInput: the OSD of the failed disk and the new disk of the same host that takes its id
func cephReplaceOsdInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	in, err := cephCreateOsdsInput(ctx, db, task, step, hostid)
	if err != nil {
		return nil, err
	}
	_, _, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	old := -1
	for _, d := range disks {
		if d.Status == model.StorageDiskRemoving {
			old = cephOsdID(d)
		}
	}
	if old < 0 {
		return nil, fmt.Errorf("the failed disk has no OSD id")
	}
	m := in.(map[string]interface{})
	m["action"] = "replace_osd"
	m["old_id"] = old
	return m, nil
}

// cephSetLabelsInput: the labels the host gets for its roles as they will be (mon, mgr, osd, _admin), and how many
// mons the cluster then has
func cephSetLabelsInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	changed := int32(storageTaskInt(task, "hostid"))
	ips, names, err := cephHostFacts(db, task, nodes)
	if err != nil {
		return nil, err
	}
	labels := []string{}
	mons := 0
	for _, n := range nodes {
		if n.HasRole(model.StorageRoleMon) {
			mons++
		}
		if n.Hostid == changed {
			labels = cephOrchLabels(n)
		}
	}
	if names[changed] == "" {
		return nil, fmt.Errorf("host %d has no cephadm host name", changed)
	}
	return map[string]interface{}{"action": "set_labels", "cluster_uuid": cluster.UUID, "fsid": cephInfoOf(cluster).Fsid,
		"hostname": names[changed], "ip": ips[changed], "labels": labels, "mons": mons}, nil
}

// cephRemoveHostInput: the host leaving is drained by cephadm (its daemons and OSDs go, their data moves first) and
// removed; a host gone for good is removed at once and its OSDs purged
func cephRemoveHostInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, nodes, disks, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	leaving := int32(storageTaskInt(task, "hostid"))
	var node *model.StorageClusterNode
	for _, n := range nodes {
		if n.Hostid == leaving {
			node = n
		}
	}
	if node == nil {
		return nil, fmt.Errorf("host %d is not in the cluster", leaving)
	}
	name, _ := nodeAttr(node, "hostname").(string)
	if name == "" {
		return nil, fmt.Errorf("%s has no recorded host name", hostName(db, leaving))
	}
	ids := []int{}
	for _, d := range disks {
		if d.Hostid == leaving {
			if id := cephOsdID(d); id >= 0 {
				ids = append(ids, id)
			}
		}
	}
	return map[string]interface{}{"action": "remove_host", "cluster_uuid": cluster.UUID, "fsid": cephInfoOf(cluster).Fsid, "hostname": name,
		"orch_host": len(cephOrchLabels(node)) > 0, "osd_ids": ids, "offline": storageTaskBool(task, "offline")}, nil
}

// cephDeployFinish records the deployed cluster: its hosts and OSDs active, the host names cephadm knows them by
func cephDeployFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	cluster, nodes, disks, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	if !succeeded {
		return tx.Model(cluster).Update("status", model.StorageClusterError).Error
	}
	for _, d := range disks {
		if err := tx.Model(&model.StorageClusterDisk{}).Where("id = ?", d.ID).Update("status", model.StorageDiskActive).Error; err != nil {
			return err
		}
	}
	if err := cephRememberHosts(tx, task, nodes); err != nil {
		return err
	}
	if err := tx.Model(&model.StorageClusterNode{}).Where("cluster_id = ?", cluster.ID).Update("status", model.StorageNodeActive).Error; err != nil {
		return err
	}
	if err := tx.Model(cluster).Updates(map[string]interface{}{"status": model.StorageClusterReady, "cluster_ref": cephInfoOf(cluster).Fsid}).Error; err != nil {
		return err
	}
	// The disks now show as Ceph OSDs: scan once the transaction is in
	go func(ctx context.Context, ids []int32) {
		time.Sleep(3 * time.Second)
		scanStorageHosts(ctx, ids)
	}(context.WithoutCancel(ctx), nodeHostids(nodes, ""))
	return nil
}

// cephRememberHosts keeps what the hosts reported in the precheck that later tasks need: the SSH host key and the
// host name (gpfsRememberHosts), and the release they run
func cephRememberHosts(tx *gorm.DB, task *model.StorageTask, nodes []*model.StorageClusterNode) error {
	if err := gpfsRememberHosts(tx, task, nodes); err != nil {
		return err
	}
	facts, err := gpfsPrecheckFacts(tx, task)
	if err != nil {
		return nil
	}
	for _, n := range nodes {
		f := facts[n.Hostid]
		os, _ := f["os"].(string)
		if os == "" {
			continue
		}
		fresh := &model.StorageClusterNode{}
		if err := tx.Take(fresh, n.ID).Error; err != nil {
			return err
		}
		attrs := map[string]interface{}{}
		_ = json.Unmarshal([]byte(fresh.Attrs), &attrs)
		attrs["os"] = os
		if err := tx.Model(&model.StorageClusterNode{}).Where("id = ?", n.ID).Update("attrs", jsonAttrs(attrs)).Error; err != nil {
			return err
		}
	}
	return nil
}

// cephExpandFinish: the hosts that joined and the OSDs made are active; the new hosts get the pool lists
func cephExpandFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	cluster, nodes, disks, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	if !succeeded {
		tx.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND status = ?", cluster.ID, model.StorageNodeJoining).
			Updates(map[string]interface{}{"status": model.StorageNodeError, "reason": "the change was aborted"})
		return tx.Model(&model.StorageClusterDisk{}).Where("cluster_id = ? AND status = ?", cluster.ID, model.StorageDiskClaiming).
			Updates(map[string]interface{}{"status": model.StorageDiskFailed, "reason": "the change was aborted"}).Error
	}
	scan := map[int32]bool{}
	for _, d := range gpfsNewDisks(disks) {
		scan[d.Hostid] = true
		if err := tx.Model(&model.StorageClusterDisk{}).Where("id = ?", d.ID).Update("status", model.StorageDiskActive).Error; err != nil {
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
	if err := cephRememberHosts(tx, task, joined); err != nil {
		return err
	}
	if err := tx.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND status = ?", cluster.ID, model.StorageNodeJoining).
		Update("status", model.StorageNodeActive).Error; err != nil {
		return err
	}
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

// cephImportFinish: the hosts that reached the cluster use it; its fsid is its reference
func cephImportFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	cluster, _, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	if err := storageImportFinish(ctx, tx, task, succeeded, "", ""); err != nil {
		return err
	}
	if !succeeded {
		return nil
	}
	return tx.Model(&model.StorageCluster{}).Where("id = ?", cluster.ID).Update("cluster_ref", cephInfoOf(cluster).Fsid).Error
}
