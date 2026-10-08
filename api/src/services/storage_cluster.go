/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Storage clusters (shared-storage-design.md): the plan checks shared by every task that adds hosts or disks, the
// precheck and selftest tasks, and the read side of clusters and tasks.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

var StorageClusters = &StorageClusterAdmin{}

type StorageClusterAdmin struct{}

const (
	storagePrecheckTimeout = 10 * time.Minute
	storageSelftestMaxSec  = 3600
)

// StorageNodePlan is a host and its roles in a request
type StorageNodePlan struct {
	Hostid int32
	Roles  []string
}

// StorageDiskPlan is a disk claimed in a request
type StorageDiskPlan struct {
	Hostid int32
	DiskID string
	Media  string
	Wipe   bool
}

// StoragePlan is what a precheck, a deployment or an expansion asks for
type StoragePlan struct {
	Kind  string
	Nodes []*StorageNodePlan
	Disks []*StorageDiskPlan
	// Params of the kind as the admin gave them (storage_clusters.params), read by StorageBackend.ParseParams
	Params           json.RawMessage
	AllowUnsupported bool
	// The cluster the hosts join, 0 for a new cluster
	ClusterID int64
	// The role rules of the kind do not apply: the hosts of an imported cluster are its clients
	SkipLayout bool
}

func hasRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

func planError(format string, args ...interface{}) error {
	return NewCLError(ErrStorageInvalidPlan, fmt.Sprintf(format, args...), nil)
}

// planMessage is the message of a plan error without its code, for wrapping it in another one
func planMessage(err error) string {
	var clErr *CLError
	if errors.As(err, &clErr) {
		return clErr.Message
	}
	return err.Error()
}

// checkStoragePlan validates the hosts and disks of a plan against the database: hosts exist and are online, are
// not taken by another cluster where that is not allowed (§4.1), disks are in a fresh scan, free and not claimed
// storageHostConflict tells why a host may not hold roles in a cluster while it is in another cluster of the same
// kind; whether it may is up to the backend (§4.1). Empty when it may
func storageHostConflict(db *gorm.DB, backend StorageBackend, kind string, clusterID int64, hostid int32, roles []string) (string, error) {
	// The leave it missed stops GPFS on it and wipes the disks the deleted cluster claimed: it must run first
	if c := storagePendingCleanupOf(db, hostid); c != nil {
		return fmt.Sprintf("still has to clean up storage cluster %s, deleted while it was offline: it does so by itself shortly after it is online", c.ClusterName), nil
	}
	others := []*model.StorageClusterNode{}
	err := db.Joins("JOIN storage_clusters c ON c.id = storage_cluster_nodes.cluster_id AND c.deleted_at IS NULL").
		Where("storage_cluster_nodes.hostid = ? AND c.kind = ? AND storage_cluster_nodes.cluster_id <> ?", hostid, kind, clusterID).Find(&others).Error
	if err != nil {
		return "", NewCLError(ErrSQLSyntaxError, "Failed to query the storage clusters of the hosts", err)
	}
	for _, o := range others {
		if why := backend.HostConflict(roles, strings.Split(o.Roles, ",")); why != "" {
			return why, nil
		}
	}
	return "", nil
}

func checkStoragePlan(db *gorm.DB, plan *StoragePlan) (hypers map[int32]*model.Hyper, disks map[string]*model.HyperDisk, err error) {
	backend, err := storageBackendOf(plan.Kind)
	if err != nil {
		return nil, nil, err
	}
	params, err := backend.ParseParams(plan.Params)
	if err != nil {
		return nil, nil, err
	}
	known := backend.Roles()
	if len(plan.Nodes) == 0 {
		return nil, nil, planError("No host is given")
	}
	hypers = map[int32]*model.Hyper{}
	roles := map[int32][]string{}
	for _, node := range plan.Nodes {
		if _, dup := hypers[node.Hostid]; dup {
			return nil, nil, planError("Host %d is given twice", node.Hostid)
		}
		hyper, online := hostOnline(db, node.Hostid)
		if hyper == nil {
			return nil, nil, NewCLError(ErrHypervisorNotFound, fmt.Sprintf("Host %d not found", node.Hostid), nil)
		}
		if !online {
			return nil, nil, planError("%s is offline", hyper.Hostname)
		}
		if len(node.Roles) == 0 {
			node.Roles = []string{model.StorageRoleClient}
		}
		for _, r := range node.Roles {
			if !hasRole(known, r) {
				return nil, nil, planError("Unknown role %q for a %s cluster", r, plan.Kind)
			}
		}
		hypers[node.Hostid] = hyper
		roles[node.Hostid] = node.Roles
		why, err := storageHostConflict(db, backend, plan.Kind, plan.ClusterID, node.Hostid, node.Roles)
		if err != nil {
			return nil, nil, err
		}
		if why != "" {
			return nil, nil, planError("%s %s", hyper.Hostname, why)
		}
	}
	if plan.ClusterID == 0 && !plan.SkipLayout {
		if err = backend.CheckLayout(plan.Nodes, plan.Disks, params); err != nil {
			return nil, nil, err
		}
	}
	takesShared := storageTakesShared(backend, params)
	diskRole := backend.DiskRole()
	if diskRole == "" && len(plan.Disks) > 0 {
		return nil, nil, planError("A %s cluster takes no disks", plan.Kind)
	}
	disks = map[string]*model.HyperDisk{}
	for _, d := range plan.Disks {
		key := fmt.Sprintf("%d/%s", d.Hostid, d.DiskID)
		if _, dup := disks[key]; dup {
			return nil, nil, planError("Disk %s is given twice", d.DiskID)
		}
		hyper := hypers[d.Hostid]
		if hyper == nil {
			return nil, nil, planError("Disk %s is on a host that is not in the request", d.DiskID)
		}
		if !hasRole(roles[d.Hostid], diskRole) {
			return nil, nil, planError("Disks of %s need the %s role", hyper.Hostname, diskRole)
		}
		disk := &model.HyperDisk{}
		if err = db.Where("hostid = ? AND disk_id = ?", d.Hostid, d.DiskID).Take(disk).Error; err != nil {
			return nil, nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s is not in the scan result of %s; scan the disks first", d.DiskID, hyper.Hostname), nil)
		}
		if time.Since(disk.ScannedAt) > diskScanValidity {
			return nil, nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("The scan result of disk %s is too old; scan the disks of %s again", disk.Name, hyper.Hostname), nil)
		}
		// The shared disk layout takes shared LUNs only; a LUN another cluster claimed on any host is taken
		if takesShared {
			if disk.State != model.DiskShared {
				return nil, nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s of %s is no shared LUN (%s): the shared disk layout takes LUNs of a SAN only",
					disk.Name, hyper.Hostname, disk.State), nil)
			}
			other := &model.StorageClusterDisk{}
			if db.Where("disk_id = ? AND cluster_id <> ?", d.DiskID, plan.ClusterID).Take(other).Error == nil {
				return nil, nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("LUN %s belongs to storage cluster %d", d.DiskID, other.ClusterID), nil)
			}
			// A LUN the cluster uses already (served by other hosts) holds its data: a new server of it never wipes it
			if d.Wipe && plan.ClusterID > 0 {
				used := &model.StorageClusterDisk{}
				if db.Where("disk_id = ? AND cluster_id = ? AND status <> ?", d.DiskID, plan.ClusterID, model.StorageDiskClaiming).Take(used).Error == nil {
					return nil, nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("LUN %s holds data of this cluster already: it can not be wiped", d.DiskID), nil)
				}
			}
			if claimed, _ := storageDiskClaim(db, d.Hostid, d.DiskID); claimed != nil {
				return nil, nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s of %s belongs to storage cluster %d", disk.Name, hyper.Hostname, claimed.ClusterID), nil)
			}
			disks[key] = disk
			continue
		}
		switch disk.State {
		case model.DiskFree:
		case model.DiskDirty:
			if !d.Wipe {
				return nil, nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s of %s has data on it; choose to wipe it", disk.Name, hyper.Hostname), nil)
			}
		default:
			return nil, nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s of %s can not be used: %s (%s)", disk.Name, hyper.Hostname, disk.State, disk.Detail), nil)
		}
		if claimed, _ := storageDiskClaim(db, d.Hostid, d.DiskID); claimed != nil {
			return nil, nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s of %s belongs to storage cluster %d", disk.Name, hyper.Hostname, claimed.ClusterID), nil)
		}
		disks[key] = disk
	}
	return
}

// storageDiskClaim returns the storage cluster claim of a disk, nil when it has none
func storageDiskClaim(db *gorm.DB, hostid int32, diskID string) (*model.StorageClusterDisk, error) {
	claim := &model.StorageClusterDisk{}
	err := db.Where("hostid = ? AND disk_id = ?", hostid, diskID).Take(claim).Error
	if err != nil {
		return nil, err
	}
	return claim, nil
}

// storageDiskIdentity is what the hosts check on a disk right before writing it (§6.4)
type storageDiskIdentity struct {
	ID        string `json:"id"`
	Serial    string `json:"serial"`
	WWN       string `json:"wwn,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	Wipe      bool   `json:"wipe,omitempty"`
	// In the cluster already: it holds data, only its identity is checked
	InUse bool `json:"in_use,omitempty"`
	// A shared LUN another host of the cluster wipes or keeps serving: released or left without wiping it. In the
	// input of the precheck: a shared LUN is expected (the shared disk layout), so the host does not take it for a
	// local disk it can not use
	Shared bool `json:"shared,omitempty"`
}

// storageSharedDiskTaker is a backend some of whose layouts claim shared LUNs (gpfs san), and only those
type storageSharedDiskTaker interface {
	TakesSharedDisks(params interface{}) bool
}

// storageDiskSiblingsOf is a backend whose disks may be one device seen by several hosts (gpfs san): what leaves
// with a disk
type storageDiskSiblingsOf interface {
	DiskSiblings(cluster *model.StorageCluster, disk *model.StorageClusterDisk, disks []*model.StorageClusterDisk) []*model.StorageClusterDisk
}

// storageTakesShared: whether the plan of a kind claims shared LUNs
func storageTakesShared(backend StorageBackend, params interface{}) bool {
	t, ok := backend.(storageSharedDiskTaker)
	return ok && t.TakesSharedDisks(params)
}

func diskIdentity(disk *model.HyperDisk, wipe bool) *storageDiskIdentity {
	id := &storageDiskIdentity{ID: disk.DiskID, Serial: disk.Serial, SizeBytes: disk.SizeBytes, Wipe: wipe}
	if strings.HasPrefix(disk.DiskID, "wwn-") {
		id.WWN = strings.TrimPrefix(disk.DiskID, "wwn-")
	}
	return id
}

// ---- precheck ----

// storagePrecheckInput is the input of stc_precheck.sh on one host
type storagePrecheckInput struct {
	Kind             string                 `json:"kind"`
	ClusterUUID      string                 `json:"cluster_uuid,omitempty"`
	Roles            []string               `json:"roles"`
	Disks            []*storageDiskIdentity `json:"disks"`
	Peers            []string               `json:"peers"`
	Ports            []int                  `json:"ports"`
	Support          []StorageOSRule        `json:"support"`
	AllowUnsupported bool                   `json:"allow_unsupported"`
	NeedHeaders      bool                   `json:"need_headers"`
	NeedContainer    bool                   `json:"need_container"`
	DataDirs         []storageDataDir       `json:"data_dirs"`
	ReserveMB        int32                  `json:"reserve_mb"`
	// What the backend checks besides for the roles of the host (backend_readiness of the node side; gpfs: a
	// recovery group server of the erasure code layout)
	Readiness map[string]interface{} `json:"readiness,omitempty"`
}

// storageReadinessProvider is a backend with checks of its own for a host and its roles, run by the precheck
type storageReadinessProvider interface {
	ReadinessInput(roles []string, params interface{}) map[string]interface{}
}

// StoragePrecheckResult is what stc_precheck.sh reports for a host
type StoragePrecheckResult struct {
	Items []*StoragePrecheckItem `json:"items"`
	Facts map[string]interface{} `json:"facts"`
}

type StoragePrecheckItem struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail"`
}

type storagePrecheckParams struct {
	Plan *storagePlanParams `json:"plan"`
}

// storagePlanParams is a plan as kept in storage_tasks.params
type storagePlanParams struct {
	Kind             string               `json:"kind"`
	Nodes            []*storageNodeParams `json:"nodes"`
	Disks            []*storageDiskParams `json:"disks"`
	Params           json.RawMessage      `json:"params,omitempty"`
	AllowUnsupported bool                 `json:"allow_unsupported"`
	ClusterID        int64                `json:"cluster_id,omitempty"`
}

type storageNodeParams struct {
	Hostid int32    `json:"hostid"`
	Roles  []string `json:"roles"`
}

type storageDiskParams struct {
	Hostid int32  `json:"hostid"`
	DiskID string `json:"disk_id"`
	Media  string `json:"media,omitempty"`
	Wipe   bool   `json:"wipe,omitempty"`
}

func planToParams(plan *StoragePlan) *storagePlanParams {
	p := &storagePlanParams{Kind: plan.Kind, Params: plan.Params, AllowUnsupported: plan.AllowUnsupported, ClusterID: plan.ClusterID}
	for _, n := range plan.Nodes {
		p.Nodes = append(p.Nodes, &storageNodeParams{Hostid: n.Hostid, Roles: n.Roles})
	}
	for _, d := range plan.Disks {
		p.Disks = append(p.Disks, &storageDiskParams{Hostid: d.Hostid, DiskID: d.DiskID, Media: d.Media, Wipe: d.Wipe})
	}
	return p
}

func taskPlan(task *model.StorageTask) (*storagePlanParams, error) {
	params := &storagePrecheckParams{}
	if err := json.Unmarshal([]byte(task.Params), params); err != nil || params.Plan == nil {
		return nil, fmt.Errorf("task %d has no plan", task.ID)
	}
	return params.Plan, nil
}

// precheckInput builds the input of stc_precheck.sh for a host of a plan
func precheckInput(db *gorm.DB, plan *storagePlanParams, hostid int32) (*storagePrecheckInput, error) {
	backend, err := storageBackendOf(plan.Kind)
	if err != nil {
		return nil, err
	}
	params, err := backend.ParseParams(plan.Params)
	if err != nil {
		return nil, err
	}
	req := backend.Requirements()
	in := &storagePrecheckInput{Kind: plan.Kind, Ports: req.PeerPorts, Support: req.Systems, AllowUnsupported: plan.AllowUnsupported,
		NeedHeaders: req.KernelModule, NeedContainer: req.Containers, Disks: []*storageDiskIdentity{}, Peers: []string{}}
	if plan.ClusterID > 0 {
		cluster := &model.StorageCluster{}
		if err := db.Take(cluster, plan.ClusterID).Error; err == nil {
			in.ClusterUUID = cluster.UUID
		}
	}
	found := false
	for _, n := range plan.Nodes {
		if n.Hostid == hostid {
			in.Roles = n.Roles
			found = true
			continue
		}
		hyper := &model.Hyper{}
		if err := db.Where("hostid = ?", n.Hostid).Take(hyper).Error; err == nil && hyper.HostIP != "" {
			in.Peers = append(in.Peers, hyper.HostIP)
		}
	}
	if !found {
		return nil, fmt.Errorf("host %d is not in the plan", hostid)
	}
	diskCount := 0
	takesShared := storageTakesShared(backend, params)
	for _, d := range plan.Disks {
		if d.Hostid != hostid {
			continue
		}
		disk := &model.HyperDisk{}
		if err := db.Where("hostid = ? AND disk_id = ?", hostid, d.DiskID).Take(disk).Error; err != nil {
			return nil, fmt.Errorf("disk %s is not in the scan result of host %d", d.DiskID, hostid)
		}
		id := diskIdentity(disk, d.Wipe)
		id.Shared = takesShared
		in.Disks = append(in.Disks, id)
		diskCount++
	}
	for _, dir := range req.DataDirs {
		if len(dir.Roles) > 0 {
			match := false
			for _, r := range dir.Roles {
				if hasRole(in.Roles, r) {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		in.DataDirs = append(in.DataDirs, dir)
	}
	in.ReserveMB = backend.ReserveMB(in.Roles, diskCount, params)
	if rp, ok := backend.(storageReadinessProvider); ok {
		in.Readiness = rp.ReadinessInput(in.Roles, params)
	}
	return in, nil
}

var storagePrecheckStep = &storageStepDef{
	Script: "stc_precheck.sh",
	Input: func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		plan, err := taskPlan(task)
		if err != nil {
			return nil, err
		}
		return precheckInput(db, plan, hostid)
	},
	// Checks across the hosts: every host must report the address CloudLand knows it by, and host names must
	// differ (GPFS and cephadm name the members by them)
	Done: func(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
		names := map[string]int32{}
		for _, r := range runs {
			result := &StoragePrecheckResult{}
			if err := json.Unmarshal([]byte(r.Result), result); err != nil {
				return fmt.Errorf("host %d sent no precheck result", r.Hostid)
			}
			name, _ := result.Facts["hostname"].(string)
			if other, dup := names[name]; dup && name != "" {
				return fmt.Errorf("hosts %d and %d have the same host name %s", other, r.Hostid, name)
			}
			names[name] = r.Hostid
		}
		return nil
	},
}

func init() {
	registerStorageTaskKind("precheck", &storageTaskKind{
		Slot:  storageSlotNone,
		Steps: map[string]*storageStepDef{"precheck": storagePrecheckStep},
	})
	registerStorageTaskKind("selftest", &storageTaskKind{
		Slot:  storageSlotNone,
		Steps: map[string]*storageStepDef{"selftest": storageSelftestStep},
	})
}

// Precheck checks a plan without creating anything: a task of kind precheck runs stc_precheck.sh on every host
func (a *StorageClusterAdmin) Precheck(ctx context.Context, plan *StoragePlan) (task *model.StorageTask, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if _, _, err = checkStoragePlan(dbs.DBContext(ctx), plan); err != nil {
		return
	}
	hostids := []int32{}
	for _, n := range plan.Nodes {
		hostids = append(hostids, n.Hostid)
	}
	sort.Slice(hostids, func(i, j int) bool { return hostids[i] < hostids[j] })
	return createStorageTask(ctx, 0, "precheck", &storagePrecheckParams{Plan: planToParams(plan)},
		[]*StorageStepPlan{{Name: "precheck", Scope: model.StorageStepScopeNodes, Hostids: hostids, Timeout: storagePrecheckTimeout}})
}

// ---- selftest ----

// StorageSelftest drives stc_selftest.sh to check the task engine on real hosts (§16 S1)
type StorageSelftest struct {
	Hostids []int32 `json:"hostids"`
	Steps   int     `json:"steps"`
	// How long every run sleeps, and how often it reports progress meanwhile
	SleepSec int `json:"sleep_sec"`
	// Runs on these hosts fail on their first FailAttempts attempts of step FailStep (from 1; 0 = of every step)
	FailHostids  []int32 `json:"fail_hostids"`
	FailAttempts int     `json:"fail_attempts"`
	FailStep     int     `json:"fail_step"`
	// Runs of the same lock name never run at the same time on a host, as jobs of one cluster
	Lock    string `json:"lock"`
	Timeout int    `json:"timeout_sec"`
}

var storageSelftestStep = &storageStepDef{
	Script: "stc_selftest.sh",
	Input: func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		st := &StorageSelftest{}
		if err := json.Unmarshal([]byte(task.Params), st); err != nil {
			return nil, err
		}
		var attempt int32
		db.Model(&model.StorageTaskRun{}).Where("step_id = ? AND hostid = ?", step.ID, hostid).Select("COALESCE(MAX(attempt), 0)").Scan(&attempt)
		fail := false
		for _, h := range st.FailHostids {
			if h == hostid && int(attempt) <= st.FailAttempts && (st.FailStep == 0 || st.FailStep == int(step.Seq)) {
				fail = true
			}
		}
		return map[string]interface{}{"sleep_sec": st.SleepSec, "fail": fail, "lock": st.Lock, "step": step.Seq, "attempt": attempt}, nil
	},
	Done: func(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
		// Later steps can read what earlier ones returned
		for _, r := range runs {
			out := map[string]interface{}{}
			if err := json.Unmarshal([]byte(r.Result), &out); err != nil || out["host"] == nil {
				return fmt.Errorf("host %d returned no result", r.Hostid)
			}
		}
		return nil
	},
}

// Selftest starts a selftest task
func (a *StorageClusterAdmin) Selftest(ctx context.Context, st *StorageSelftest) (task *model.StorageTask, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if len(st.Hostids) == 0 || st.Steps < 1 || st.Steps > 10 || st.SleepSec < 0 || st.SleepSec > storageSelftestMaxSec {
		return nil, planError("A selftest needs hosts, 1-10 steps and a sleep of 0-%d seconds", storageSelftestMaxSec)
	}
	if st.Lock == "" {
		st.Lock = "selftest"
	}
	if strings.Trim(st.Lock, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		return nil, planError("The lock name may only hold lower case letters, digits and dashes")
	}
	db := dbs.DBContext(ctx)
	for _, h := range st.Hostids {
		if hyper, _ := hostOnline(db, h); hyper == nil {
			return nil, NewCLError(ErrHypervisorNotFound, fmt.Sprintf("Host %d not found", h), nil)
		}
	}
	plan := []*StorageStepPlan{}
	for i := 0; i < st.Steps; i++ {
		plan = append(plan, &StorageStepPlan{Name: "selftest", Scope: model.StorageStepScopeNodes, Hostids: st.Hostids,
			Timeout: time.Duration(st.Timeout) * time.Second})
	}
	return createStorageTask(ctx, 0, "selftest", st, plan)
}

// ---- read side ----

// StorageTaskView is a task with its steps and the runs of every step, newest attempt last
type StorageTaskView struct {
	Task  *model.StorageTask
	Steps []*StorageStepView
}

type StorageStepView struct {
	Step *model.StorageTaskStep
	Runs []*model.StorageTaskRun
}

func (a *StorageClusterAdmin) ListTasks(ctx context.Context, clusterID int64, status string, offset, limit int64) (total int64, tasks []*model.StorageTask, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	q := db.Model(&model.StorageTask{})
	if clusterID >= 0 {
		q = q.Where("cluster_id = ?", clusterID)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if err = q.Count(&total).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to count storage tasks", err)
	}
	tasks = []*model.StorageTask{}
	err = q.Order("id DESC").Offset(int(offset)).Limit(int(limit)).Find(&tasks).Error
	return
}

func (a *StorageClusterAdmin) GetTask(ctx context.Context, uuid string) (view *StorageTaskView, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	task := &model.StorageTask{}
	if err = db.Where("uuid = ?", uuid).Take(task).Error; err != nil {
		return nil, NewCLError(ErrStorageTaskNotFound, "Storage task not found", err)
	}
	view = &StorageTaskView{Task: task}
	steps, err := storageTaskSteps(db, task.ID)
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		runs := []*model.StorageTaskRun{}
		db.Where("step_id = ?", s.ID).Order("hostid, attempt").Find(&runs)
		view.Steps = append(view.Steps, &StorageStepView{Step: s, Runs: runs})
	}
	return
}

// TaskUUIDs returns the UUIDs of some tasks by ID
func (a *StorageClusterAdmin) TaskUUIDs(ctx context.Context, ids []int64) map[int64]string {
	uuids := map[int64]string{}
	if len(ids) == 0 {
		return uuids
	}
	tasks := []*model.StorageTask{}
	dbs.DBContext(ctx).Select("id", "uuid").Where("id IN ?", ids).Find(&tasks)
	for _, t := range tasks {
		uuids[t.ID] = t.UUID
	}
	return uuids
}

// ClustersByID returns some clusters by ID, deleted ones included (old tasks keep their cluster)
func (a *StorageClusterAdmin) ClustersByID(ctx context.Context, ids []int64) map[int64]*model.StorageCluster {
	clusters := map[int64]*model.StorageCluster{}
	if len(ids) == 0 {
		return clusters
	}
	list := []*model.StorageCluster{}
	dbs.DBContext(ctx).Unscoped().Where("id IN ?", ids).Find(&list)
	for _, c := range list {
		clusters[c.ID] = c
	}
	return clusters
}

func (a *StorageClusterAdmin) TaskIDByUUID(ctx context.Context, uuid string) (int64, error) {
	task := &model.StorageTask{}
	if err := dbs.DBContext(ctx).Where("uuid = ?", uuid).Take(task).Error; err != nil {
		return 0, NewCLError(ErrStorageTaskNotFound, "Storage task not found", err)
	}
	return task.ID, nil
}

func (a *StorageClusterAdmin) List(ctx context.Context, offset, limit int64) (total int64, clusters []*model.StorageCluster, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	if err = db.Model(&model.StorageCluster{}).Count(&total).Error; err != nil {
		return 0, nil, NewCLError(ErrSQLSyntaxError, "Failed to count storage clusters", err)
	}
	clusters = []*model.StorageCluster{}
	err = db.Order("id").Offset(int(offset)).Limit(int(limit)).Find(&clusters).Error
	return
}

func (a *StorageClusterAdmin) Get(ctx context.Context, uuid string) (cluster *model.StorageCluster, nodes []*model.StorageClusterNode, disks []*model.StorageClusterDisk, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	cluster = &model.StorageCluster{}
	if err = db.Where("uuid = ?", uuid).Take(cluster).Error; err != nil {
		return nil, nil, nil, NewCLError(ErrStorageClusterNotFound, "Storage cluster not found", err)
	}
	db.Where("cluster_id = ?", cluster.ID).Order("hostid").Find(&nodes)
	db.Where("cluster_id = ?", cluster.ID).Order("hostid, disk_id").Find(&disks)
	return
}

// StorageClusterSummary counts what a cluster holds, for the cluster list
type StorageClusterSummary struct {
	Nodes         int
	Disks         int
	Pools         int
	CapacityBytes int64
	FreeBytes     int64
	// Promised to the volumes of the pools of the cluster, the same sum as the admission of a shared pool
	AllocatedBytes int64
}

// Summaries counts the hosts, disks and pools and sums the file system capacity of some clusters with one query each
func (a *StorageClusterAdmin) Summaries(ctx context.Context, ids []int64) map[int64]*StorageClusterSummary {
	db := dbs.DBContext(ctx)
	summaries := map[int64]*StorageClusterSummary{}
	for _, id := range ids {
		summaries[id] = &StorageClusterSummary{}
	}
	if len(ids) == 0 {
		return summaries
	}
	type countRow struct {
		ClusterID int64
		N         int
	}
	count := func(m interface{}, apply func(s *StorageClusterSummary, n int)) {
		rows := []countRow{}
		db.Model(m).Select("cluster_id, COUNT(*) AS n").Where("cluster_id IN ?", ids).Group("cluster_id").Scan(&rows)
		for _, r := range rows {
			if s := summaries[r.ClusterID]; s != nil {
				apply(s, r.N)
			}
		}
	}
	count(&model.StorageClusterNode{}, func(s *StorageClusterSummary, n int) { s.Nodes = n })
	count(&model.StorageClusterDisk{}, func(s *StorageClusterSummary, n int) { s.Disks = n })
	count(&model.StoragePool{}, func(s *StorageClusterSummary, n int) { s.Pools = n })
	fss := []*model.StorageFilesystem{}
	db.Where("cluster_id IN ?", ids).Find(&fss)
	for _, f := range fss {
		if s := summaries[f.ClusterID]; s != nil {
			s.CapacityBytes += f.CapacityBytes
			s.FreeBytes += f.FreeBytes
		}
	}
	allocated := []countRow{}
	db.Table("volumes").Select("storage_pools.cluster_id, COALESCE(SUM(volumes.size), 0) AS n").
		Joins("JOIN storage_pools ON storage_pools.id = volumes.storage_pool_id").
		Where("storage_pools.cluster_id IN ? AND volumes.deleted_at IS NULL", ids).Group("storage_pools.cluster_id").Scan(&allocated)
	for _, r := range allocated {
		if s := summaries[r.ClusterID]; s != nil {
			s.AllocatedBytes = int64(r.N) * gib
		}
	}
	return summaries
}

// Filesystems lists the file systems of a cluster
func (a *StorageClusterAdmin) Filesystems(ctx context.Context, clusterID int64) (fss []*model.StorageFilesystem) {
	dbs.DBContext(ctx).Where("cluster_id = ?", clusterID).Order("id").Find(&fss)
	return
}

// Pools lists the CloudLand pools of a cluster
func (a *StorageClusterAdmin) Pools(ctx context.Context, clusterID int64) (pools []*model.StoragePool) {
	dbs.DBContext(ctx).Where("cluster_id = ?", clusterID).Order("id").Find(&pools)
	return
}

// PoolAllocations sums the volumes of each pool of a cluster in bytes, by pool id
func (a *StorageClusterAdmin) PoolAllocations(ctx context.Context, clusterID int64) map[int64]int64 {
	rows := []struct {
		StoragePoolID int64
		N             int64
	}{}
	dbs.DBContext(ctx).Table("volumes").Select("volumes.storage_pool_id, COALESCE(SUM(volumes.size), 0) AS n").
		Joins("JOIN storage_pools ON storage_pools.id = volumes.storage_pool_id").
		Where("storage_pools.cluster_id = ? AND volumes.deleted_at IS NULL", clusterID).Group("volumes.storage_pool_id").Scan(&rows)
	allocated := map[int64]int64{}
	for _, r := range rows {
		allocated[r.StoragePoolID] = r.N * gib
	}
	return allocated
}
