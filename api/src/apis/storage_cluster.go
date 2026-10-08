/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var storageClusterAPI = &StorageClusterAPI{}
var storageClusterAdmin = services.StorageClusters

// StorageClusterAPI serves storage clusters and their tasks (shared-storage-design.md §13.1). Every route is for
// system admins.
type StorageClusterAPI struct{}

type StorageNodePayload struct {
	Hypervisor string `json:"hypervisor" binding:"required,uuid"`
	// Roles of the kind (GET /storage_backends); the backend refuses ones it does not have
	Roles []string `json:"roles" binding:"omitempty,max=8,dive,min=1,max=16"`
}

type StorageDiskPayload struct {
	Hypervisor string `json:"hypervisor" binding:"required,uuid"`
	DiskID     string `json:"disk_id" binding:"required,max=256"`
	Media      string `json:"media" binding:"omitempty,oneof=ssd hdd nvme"`
	Wipe       bool   `json:"wipe"`
}

type StoragePrecheckPayload struct {
	// A kind from GET /storage_backends
	Kind  string                `json:"kind" binding:"required,max=16"`
	Nodes []*StorageNodePayload `json:"nodes" binding:"required,min=1,max=64,dive"`
	Disks []*StorageDiskPayload `json:"disks" binding:"omitempty,max=512,dive"`
	// Parameters of the kind, checked by its backend; every kind takes "test" (the single host test layout)
	Params           json.RawMessage `json:"params" swaggertype:"object"`
	AllowUnsupported bool            `json:"allow_unsupported"`
}

// StorageBackendResponse is what the forms need to know about a kind of storage cluster (shared-storage-design.md §4.5)
type StorageBackendResponse struct {
	Kind string `json:"kind"`
	// Roles a host may have, in the order forms show them
	Roles []string `json:"roles"`
	// Role of the hosts that give disks; empty when the kind takes no disks
	DiskRole string `json:"disk_role,omitempty"`
	// Suggested roles of one more host: default_roles always, plus each of default_once_roles while no host has it
	DefaultRoles     []string                 `json:"default_roles"`
	DefaultOnceRoles []string                 `json:"default_once_roles"`
	Systems          []services.StorageOSRule `json:"systems"`
	KernelModule     bool                     `json:"kernel_module"`
	Containers       bool                     `json:"containers"`
	// Installs from an uploaded package (GET /storage_packages) rather than from the distribution
	Package bool `json:"package"`
	// Operations of the kind; the interface offers only these
	Capabilities *services.StorageCapabilities `json:"capabilities"`
	// Driver of the CloudLand pools on clusters of the kind
	PoolDriver string `json:"pool_driver"`
}

type StorageBackendListResponse struct {
	StorageBackends []*StorageBackendResponse `json:"storage_backends"`
}

type StorageClusterCreatePayload struct {
	StoragePrecheckPayload
	Name        string `json:"name" binding:"required,max=63"`
	Description string `json:"description" binding:"omitempty,max=256"`
	// UUID of a verified package whose license is accepted, for kinds that install from one (gpfs)
	Package string `json:"package_id" binding:"omitempty,uuid"`
}

type StorageClusterDeletePayload struct {
	// The name of the cluster, typed to confirm: its disks are wiped
	ConfirmName string `json:"confirm_name"`
	// Remove the storage software from the hosts too
	PurgePackages bool `json:"purge_packages"`
}

type StorageSelftestPayload struct {
	Hypervisors     []string `json:"hypervisors" binding:"required,min=1,max=64,dive,uuid"`
	Steps           int      `json:"steps" binding:"required,min=1,max=10"`
	SleepSec        int      `json:"sleep_sec" binding:"omitempty,min=0,max=3600"`
	FailHypervisors []string `json:"fail_hypervisors" binding:"omitempty,max=64,dive,uuid"`
	FailAttempts    int      `json:"fail_attempts" binding:"omitempty,min=0,max=10"`
	FailStep        int      `json:"fail_step" binding:"omitempty,min=0,max=10"`
	Lock            string   `json:"lock" binding:"omitempty,max=32"`
	TimeoutSec      int      `json:"timeout_sec" binding:"omitempty,min=0,max=86400"`
}

type StorageRunResponse struct {
	ID         int64              `json:"id"`
	Hypervisor *ResourceReference `json:"hypervisor"`
	Hostid     int32              `json:"hostid"`
	Attempt    int32              `json:"attempt"`
	Status     string             `json:"status"`
	Dispatches int32              `json:"dispatches"`
	Progress   int32              `json:"progress"`
	Message    string             `json:"message"`
	LogTail    string             `json:"log_tail"`
	Result     json.RawMessage    `json:"result,omitempty" swaggertype:"object"`
	StartedAt  string             `json:"started_at"`
	UpdatedAt  string             `json:"updated_at"`
}

type StorageStepResponse struct {
	Seq        int32                 `json:"seq"`
	Name       string                `json:"name"`
	Scope      string                `json:"scope"`
	Status     string                `json:"status"`
	TimeoutSec int32                 `json:"timeout_sec"`
	StartedAt  string                `json:"started_at,omitempty"`
	FinishedAt string                `json:"finished_at,omitempty"`
	Runs       []*StorageRunResponse `json:"runs"`
}

type StorageTaskResponse struct {
	ID      string             `json:"id"`
	Cluster *ResourceReference `json:"cluster,omitempty"`
	Kind    string             `json:"kind"`
	// Kind of storage the task works on (gpfs, ceph); empty for the tasks every kind shares
	Backend     string                 `json:"backend,omitempty"`
	Status      string                 `json:"status"`
	CurrentStep int32                  `json:"current_step"`
	Creator     string                 `json:"creator"`
	Message     string                 `json:"message"`
	CreatedAt   string                 `json:"created_at"`
	FinishedAt  string                 `json:"finished_at,omitempty"`
	Steps       []*StorageStepResponse `json:"steps,omitempty"`
}

type StorageTaskListResponse struct {
	Offset int                    `json:"offset"`
	Total  int                    `json:"total"`
	Limit  int                    `json:"limit"`
	Tasks  []*StorageTaskResponse `json:"tasks"`
}

type StorageClusterNodeResponse struct {
	Hypervisor *ResourceReference `json:"hypervisor"`
	Roles      []string           `json:"roles"`
	// Kind specific attributes, e.g. the failure group of a GPFS host
	Attrs         json.RawMessage `json:"attrs,omitempty" swaggertype:"object"`
	Status        string          `json:"status"`
	State         string          `json:"state"`
	Reason        string          `json:"reason,omitempty"`
	ReservedMemMB int32           `json:"reserved_mem_mb"`
	// When the health watchdog last saw the host in the storage software's report
	CheckedAt string `json:"checked_at,omitempty"`
}

type StorageClusterDiskResponse struct {
	// UUID of the disk in the cluster, for removing it
	ID         string             `json:"id"`
	Hypervisor *ResourceReference `json:"hypervisor"`
	DiskID     string             `json:"disk_id"`
	Serial     string             `json:"serial"`
	Role       string             `json:"role"`
	Name       string             `json:"name"`
	Media      string             `json:"media"`
	SizeBytes  int64              `json:"size_bytes"`
	// Kind specific attributes, e.g. the usage and storage pool of a GPFS NSD
	Attrs  json.RawMessage `json:"attrs,omitempty" swaggertype:"object"`
	Status string          `json:"status"`
	Reason string          `json:"reason,omitempty"`
	// What the storage software reports for the disk (gpfs availability, ceph up/down) and when it was seen
	State     string `json:"state,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
}

// StorageClusterHealthResponse is the last health report of a cluster and the alarms it raised (§14)
type StorageClusterHealthResponse struct {
	CheckedAt string   `json:"checked_at,omitempty"`
	Summary   string   `json:"summary,omitempty"`
	Error     string   `json:"error,omitempty"`
	Messages  []string `json:"messages,omitempty"`
	Flags     []string `json:"flags,omitempty"`
	// Capacity of the whole cluster as its software reports it (ceph df)
	CapacityBytes int64 `json:"capacity_bytes,omitempty"`
	FreeBytes     int64 `json:"free_bytes,omitempty"`
	// The host that checked
	Hypervisor *ResourceReference             `json:"hypervisor,omitempty"`
	Alarms     []*StorageClusterAlarmResponse `json:"alarms"`
}

type StorageClusterAlarmResponse struct {
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Since    string `json:"since"`
}

type StorageClusterResponse struct {
	*ResourceReference
	Kind   string `json:"kind"`
	Mode   string `json:"mode"`
	Layout string `json:"layout,omitempty"`
	// What the cluster supports: the operations of its kind, fewer in some layouts (gpfs erasure code); the
	// interface offers only these
	Capabilities *services.StorageCapabilities `json:"capabilities,omitempty"`
	// What the layout is made of, the keys the kind's own (gpfs ece: code, no_slot_map, recovery_group, vdisk_set,
	// node_class; shared-storage-design.md §7.9); absent when the layout has nothing to show
	LayoutInfo map[string]interface{} `json:"layout_info,omitempty"`
	// Made in the test layout of its kind: one copy of the data (shared-storage-design.md §6.3)
	TestLayout bool   `json:"test_layout,omitempty"`
	Status     string `json:"status"`
	Health     string `json:"health"`
	// Detail only: the last health report and the alarms it raised
	HealthInfo *StorageClusterHealthResponse `json:"health_info,omitempty"`
	Version    string                        `json:"version,omitempty"`
	// The storage software's own id of the cluster: GPFS cluster name and id, Ceph fsid
	ClusterRef  string `json:"cluster_ref,omitempty"`
	Unsupported bool   `json:"unsupported"`
	// Tasks holding the slots of the cluster (§6.2.1): structural changes, pool changes
	ActiveTask     string                        `json:"active_task,omitempty"`
	ActivePoolTask string                        `json:"active_pool_task,omitempty"`
	Description    string                        `json:"description,omitempty"`
	Nodes          []*StorageClusterNodeResponse `json:"nodes,omitempty"`
	Disks          []*StorageClusterDiskResponse `json:"disks,omitempty"`
	// Detail only: the file systems (kinds with that layer) and the CloudLand pools of the cluster
	Filesystems []*StorageFilesystemResponse  `json:"filesystems,omitempty"`
	Pools       []*StorageClusterPoolResponse `json:"pools,omitempty"`
	// Detail only: the zones whose hosts join as clients on their own and the hosts on their way in
	AutoJoin *StorageAutoJoinResponse `json:"auto_join,omitempty"`
	// Counts and capacity, in the list and the detail
	NodeCount     int   `json:"node_count"`
	DiskCount     int   `json:"disk_count"`
	PoolCount     int   `json:"pool_count"`
	CapacityBytes int64 `json:"capacity_bytes"`
	FreeBytes     int64 `json:"free_bytes"`
	// Sum of the volumes in the pools of the cluster
	AllocatedBytes int64 `json:"allocated_bytes"`
}

type StorageFilesystemResponse struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	MountPoint    string `json:"mount_point"`
	BlockSize     string `json:"block_size"`
	DataReplicas  int32  `json:"data_replicas"`
	MetaReplicas  int32  `json:"meta_replicas"`
	Status        string `json:"status"`
	CapacityBytes int64  `json:"capacity_bytes"`
	FreeBytes     int64  `json:"free_bytes"`
	CapacityAt    string `json:"capacity_at,omitempty"`
}

type StorageClusterPoolResponse struct {
	*ResourceReference
	Driver        string `json:"driver"`
	Status        string `json:"status"`
	Media         string `json:"media"`
	MountPath     string `json:"mount_path,omitempty"`
	QuotaBytes    int64  `json:"quota_bytes"`
	CapacityBytes int64  `json:"capacity_bytes"`
	UsedBytes     int64  `json:"used_bytes"`
	// Sum of the volumes of the pool
	AllocatedBytes int64 `json:"allocated_bytes"`
	// json read by the driver: the gpfs fileset, file system and storage pool
	DriverParams json.RawMessage `json:"driver_params,omitempty" swaggertype:"object"`
}

type StorageClusterListResponse struct {
	Offset          int                       `json:"offset"`
	Total           int                       `json:"total"`
	Limit           int                       `json:"limit"`
	StorageClusters []*StorageClusterResponse `json:"storage_clusters"`
}

// hyperRefs resolves host ids to references once per response
type hyperRefs struct {
	ctx   context.Context
	cache map[int32]*ResourceReference
}

func (h *hyperRefs) get(hostid int32) *ResourceReference {
	if ref, ok := h.cache[hostid]; ok {
		return ref
	}
	ref := &ResourceReference{}
	if hyper, err := hyperAdmin.GetHyperByHostid(h.ctx, hostid); err == nil {
		ref = &ResourceReference{ID: hyper.UUID, Name: hyper.Hostname}
	}
	h.cache[hostid] = ref
	return ref
}

func storageTaskResponse(task *model.StorageTask, clusters map[int64]*ResourceReference) *StorageTaskResponse {
	resp := &StorageTaskResponse{ID: task.UUID, Kind: task.Kind, Backend: task.Backend, Status: task.Status, CurrentStep: task.CurrentStep,
		Creator: task.CreatorName, Message: task.Message, CreatedAt: task.CreatedAt.Format(TimeStringForMat), FinishedAt: formatTimePtr(task.FinishedAt)}
	if task.ClusterID > 0 && clusters != nil {
		resp.Cluster = clusters[task.ClusterID]
	}
	return resp
}

func (v *StorageClusterAPI) clusterRefs(ctx context.Context, tasks []*model.StorageTask) map[int64]*ResourceReference {
	ids := []int64{}
	for _, t := range tasks {
		if t.ClusterID > 0 {
			ids = append(ids, t.ClusterID)
		}
	}
	refs := map[int64]*ResourceReference{}
	for id, c := range storageClusterAdmin.ClustersByID(ctx, ids) {
		refs[id] = &ResourceReference{ID: c.UUID, Name: c.Name}
	}
	return refs
}

func (v *StorageClusterAPI) resolveHosts(c *gin.Context, uuids []string) ([]int32, bool) {
	ids := []int32{}
	for _, u := range uuids {
		hyper, err := hyperAdmin.GetHyperByUUID(c.Request.Context(), u)
		if err != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid hypervisor "+u, err)
			return nil, false
		}
		ids = append(ids, hyper.Hostid)
	}
	return ids, true
}

// @Summary check hosts and disks for a storage cluster
// @Description run the precheck of a storage cluster plan on its hosts (shared-storage-design.md §6.5); nothing is installed. Returns the precheck task
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   message  body  StoragePrecheckPayload  true  "Plan to check"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/precheck [post]
func (v *StorageClusterAPI) Precheck(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &StoragePrecheckPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	plan, ok := v.plan(c, payload)
	if !ok {
		return
	}
	task, err := storageClusterAdmin.Precheck(ctx, plan)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to start the precheck", err)
		return
	}
	c.JSON(http.StatusAccepted, storageTaskResponse(task, nil))
}

// plan turns the hosts and disks of a request, named by UUID, into a plan of host ids
func (v *StorageClusterAPI) plan(c *gin.Context, payload *StoragePrecheckPayload) (*services.StoragePlan, bool) {
	plan := &services.StoragePlan{Kind: payload.Kind, AllowUnsupported: payload.AllowUnsupported, Params: payload.Params}
	byUUID := map[string]int32{}
	for _, n := range payload.Nodes {
		ids, ok := v.resolveHosts(c, []string{n.Hypervisor})
		if !ok {
			return nil, false
		}
		byUUID[n.Hypervisor] = ids[0]
		plan.Nodes = append(plan.Nodes, &services.StorageNodePlan{Hostid: ids[0], Roles: n.Roles})
	}
	for _, d := range payload.Disks {
		hostid, ok := byUUID[d.Hypervisor]
		if !ok {
			ErrorResponse(c, http.StatusBadRequest, "Disk "+d.DiskID+" is on a host that is not in the request", nil)
			return nil, false
		}
		plan.Disks = append(plan.Disks, &services.StorageDiskPlan{Hostid: hostid, DiskID: d.DiskID, Media: d.Media, Wipe: d.Wipe})
	}
	return plan, true
}

// @Summary create a managed storage cluster
// @Description records the cluster with its hosts and disks and starts its deployment (shared-storage-design.md §7.2); follow the task in active_task. The disks are claimed at once
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   message  body  StorageClusterCreatePayload  true  "Cluster"
// @Success 202 {object} StorageClusterResponse
// @Router /storage_clusters [post]
func (v *StorageClusterAPI) Create(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &StorageClusterCreatePayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	plan, ok := v.plan(c, &payload.StoragePrecheckPayload)
	if !ok {
		return
	}
	cluster, _, err := storageClusterAdmin.Create(ctx, &services.StorageClusterCreate{Plan: plan, Name: payload.Name,
		Description: payload.Description, PackageUUID: payload.Package})
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to create the storage cluster", err)
		return
	}
	cluster, nodes, disks, err := storageClusterAdmin.Get(ctx, cluster.UUID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage cluster", err)
		return
	}
	c.JSON(http.StatusAccepted, storageClusterResponse(ctx, cluster, nodes, disks))
}

// @Summary delete a storage cluster
// @Description tears the storage software down, wipes the disks of the cluster and cleans every host, then removes the records (shared-storage-design.md §7.6). Refused while the cluster has storage pools or a task holds it
// @tags StorageCluster
// @Produce json
// @Param   id              path   string  true   "Cluster UUID"
// @Param   confirm_name    query  string  true   "The name of the cluster, typed to confirm: its disks are wiped"
// @Param   purge_packages  query  bool    false  "Remove the storage software from the hosts too"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id} [delete]
func (v *StorageClusterAPI) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	// The gateway drops the body of DELETE requests: the confirmation comes as query parameters (a body is still
	// read when clapi is called directly)
	payload := &StorageClusterDeletePayload{ConfirmName: c.Query("confirm_name"), PurgePackages: c.Query("purge_packages") == "true"}
	if payload.ConfirmName == "" {
		_ = c.ShouldBindJSON(payload)
	}
	task, err := storageClusterAdmin.Delete(ctx, c.Param("id"), &services.StorageClusterDelete{ConfirmName: payload.ConfirmName,
		PurgePackages: payload.PurgePackages})
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to delete the storage cluster", err)
		return
	}
	c.JSON(http.StatusAccepted, storageTaskResponse(task, v.clusterRefs(ctx, []*model.StorageTask{task})))
}

// @Summary run a selftest task
// @Description run a task whose steps only sleep, report progress and fail on demand, to check the storage task path to the hosts (shared-storage-design.md §16 S1)
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   message  body  StorageSelftestPayload  true  "Selftest"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_tasks/selftest [post]
func (v *StorageClusterAPI) Selftest(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &StorageSelftestPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	hosts, ok := v.resolveHosts(c, payload.Hypervisors)
	if !ok {
		return
	}
	fails, ok := v.resolveHosts(c, payload.FailHypervisors)
	if !ok {
		return
	}
	task, err := storageClusterAdmin.Selftest(ctx, &services.StorageSelftest{Hostids: hosts, Steps: payload.Steps, SleepSec: payload.SleepSec,
		FailHostids: fails, FailAttempts: payload.FailAttempts, FailStep: payload.FailStep, Lock: payload.Lock, Timeout: payload.TimeoutSec})
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to start the selftest", err)
		return
	}
	c.JSON(http.StatusAccepted, storageTaskResponse(task, nil))
}

// @Summary list storage tasks
// @Description cluster_id is the UUID of a cluster; 0 lists the tasks not tied to a cluster (precheck, selftest); without it every task is listed
// @tags StorageCluster
// @Produce json
// @Param   cluster_id  query  string  false  "Cluster UUID, or 0"
// @Param   status      query  string  false  "running, failed, aborting, succeeded or aborted"
// @Success 200 {object} StorageTaskListResponse
// @Router /storage_tasks [get]
func (v *StorageClusterAPI) ListTasks(c *gin.Context) {
	ctx := c.Request.Context()
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if offset < 0 || limit < 0 || limit > 500 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset or limit", nil)
		return
	}
	clusterID := int64(-1)
	if ref := c.Query("cluster_id"); ref == "0" {
		clusterID = 0
	} else if ref != "" {
		cluster, _, _, err := storageClusterAdmin.Get(ctx, ref)
		if err != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid storage cluster", err)
			return
		}
		clusterID = cluster.ID
	}
	status := c.Query("status")
	if status != "" && !strings.Contains(" running failed aborting succeeded aborted ", " "+status+" ") {
		ErrorResponse(c, http.StatusBadRequest, "Invalid status", nil)
		return
	}
	total, tasks, err := storageClusterAdmin.ListTasks(ctx, clusterID, status, int64(offset), int64(limit))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list storage tasks", err)
		return
	}
	refs := v.clusterRefs(ctx, tasks)
	resp := &StorageTaskListResponse{Offset: offset, Total: int(total), Limit: len(tasks), Tasks: []*StorageTaskResponse{}}
	for _, t := range tasks {
		resp.Tasks = append(resp.Tasks, storageTaskResponse(t, refs))
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary get a storage task
// @Description a task with its steps and every run of every step, with the log tail each host sent
// @tags StorageCluster
// @Produce json
// @Param   id  path  string  true  "Task UUID"
// @Success 200 {object} StorageTaskResponse
// @Router /storage_tasks/{id} [get]
func (v *StorageClusterAPI) GetTask(c *gin.Context) {
	ctx := c.Request.Context()
	view, err := storageClusterAdmin.GetTask(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage task", err)
		return
	}
	resp := storageTaskResponse(view.Task, v.clusterRefs(ctx, []*model.StorageTask{view.Task}))
	hosts := &hyperRefs{ctx: ctx, cache: map[int32]*ResourceReference{}}
	resp.Steps = []*StorageStepResponse{}
	for _, sv := range view.Steps {
		step := &StorageStepResponse{Seq: sv.Step.Seq, Name: sv.Step.Name, Scope: sv.Step.Scope, Status: sv.Step.Status,
			TimeoutSec: sv.Step.TimeoutSec, StartedAt: formatTimePtr(sv.Step.StartedAt), FinishedAt: formatTimePtr(sv.Step.FinishedAt),
			Runs: []*StorageRunResponse{}}
		for _, r := range sv.Runs {
			run := &StorageRunResponse{ID: r.ID, Hypervisor: hosts.get(r.Hostid), Hostid: r.Hostid, Attempt: r.Attempt, Status: r.Status,
				Dispatches: r.Dispatches, Progress: r.Progress, Message: r.Message, LogTail: r.LogTail,
				StartedAt: r.StartedAt.Format(TimeStringForMat), UpdatedAt: r.UpdatedAt.Format(TimeStringForMat)}
			if r.Result != "" {
				run.Result = json.RawMessage(r.Result)
			}
			step.Runs = append(step.Runs, run)
		}
		resp.Steps = append(resp.Steps, step)
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary retry a failed storage task
// @Description run the failed step again on the hosts it failed on (or from the step it depends on)
// @tags StorageCluster
// @Produce json
// @Param   id  path  string  true  "Task UUID"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_tasks/{id}/retry [post]
func (v *StorageClusterAPI) RetryTask(c *gin.Context) {
	v.taskAction(c, services.RetryStorageTask)
}

// @Summary abort a storage task
// @Description stop a running or failed task; jobs still running on hosts are killed first, then the task ends and frees its cluster
// @tags StorageCluster
// @Produce json
// @Param   id  path  string  true  "Task UUID"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_tasks/{id}/abort [post]
func (v *StorageClusterAPI) AbortTask(c *gin.Context) {
	v.taskAction(c, services.AbortStorageTask)
}

func (v *StorageClusterAPI) taskAction(c *gin.Context, action func(context.Context, int64) error) {
	ctx := c.Request.Context()
	taskID, err := storageClusterAdmin.TaskIDByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage task", err)
		return
	}
	if err = action(ctx, taskID); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to change the storage task", err)
		return
	}
	view, err := storageClusterAdmin.GetTask(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage task", err)
		return
	}
	c.JSON(http.StatusAccepted, storageTaskResponse(view.Task, v.clusterRefs(ctx, []*model.StorageTask{view.Task})))
}

func storageClusterResponse(ctx context.Context, cluster *model.StorageCluster, nodes []*model.StorageClusterNode, disks []*model.StorageClusterDisk) *StorageClusterResponse {
	resp := &StorageClusterResponse{
		ResourceReference: &ResourceReference{ID: cluster.UUID, Name: cluster.Name,
			CreatedAt: cluster.CreatedAt.Format(TimeStringForMat), UpdatedAt: cluster.UpdatedAt.Format(TimeStringForMat)},
		Kind: cluster.Kind, Mode: cluster.Mode, Layout: cluster.Layout, Status: cluster.Status, Health: cluster.Health,
		Version: cluster.Version, ClusterRef: cluster.ClusterRef, Unsupported: cluster.Unsupported, Description: cluster.Description,
		Capabilities: services.StorageClusterCapabilities(cluster), LayoutInfo: services.StorageClusterLayoutInfo(cluster),
		TestLayout: services.StorageClusterTestLayout(cluster),
	}
	if cluster.ActiveTask > 0 || cluster.ActivePoolTask > 0 {
		uuids := storageClusterAdmin.TaskUUIDs(ctx, []int64{cluster.ActiveTask, cluster.ActivePoolTask})
		resp.ActiveTask, resp.ActivePoolTask = uuids[cluster.ActiveTask], uuids[cluster.ActivePoolTask]
	}
	hosts := &hyperRefs{ctx: ctx, cache: map[int32]*ResourceReference{}}
	for _, n := range nodes {
		roles := []string{}
		if n.Roles != "" {
			roles = strings.Split(n.Roles, ",")
		}
		resp.Nodes = append(resp.Nodes, &StorageClusterNodeResponse{Hypervisor: hosts.get(n.Hostid), Roles: roles, Attrs: rawAttrs(n.Attrs),
			Status: n.Status, State: n.State, Reason: n.Reason, ReservedMemMB: n.ReservedMemMB, CheckedAt: formatTimePtr(n.CheckedAt)})
	}
	for _, d := range disks {
		resp.Disks = append(resp.Disks, &StorageClusterDiskResponse{ID: d.UUID, Hypervisor: hosts.get(d.Hostid), DiskID: d.DiskID, Serial: d.Serial,
			Role: d.Role, Name: d.Name, Media: d.Media, SizeBytes: d.SizeBytes, Attrs: rawAttrs(d.Attrs), Status: d.Status, Reason: d.Reason,
			State: d.State, CheckedAt: formatTimePtr(d.CheckedAt)})
	}
	return resp
}

// storageClusterHealth is the health report kept with a cluster, for its detail
func storageClusterHealth(ctx context.Context, cluster *model.StorageCluster) *StorageClusterHealthResponse {
	info := services.ParseStorageHealthInfo(cluster.HealthInfo)
	h := &StorageClusterHealthResponse{CheckedAt: formatTimePtr(cluster.HealthAt), Summary: info.Summary, Error: info.Error,
		Messages: info.Messages, Flags: info.Flags, Alarms: []*StorageClusterAlarmResponse{}}
	if info.Capacity != nil {
		h.CapacityBytes, h.FreeBytes = info.Capacity.Total, info.Capacity.Free
	}
	if info.Hostid > 0 {
		h.Hypervisor = (&hyperRefs{ctx: ctx, cache: map[int32]*ResourceReference{}}).get(info.Hostid)
	}
	for _, a := range info.Firing {
		h.Alarms = append(h.Alarms, &StorageClusterAlarmResponse{Name: a.Name, Severity: a.Severity, Summary: a.Summary, Since: a.Since.Format(TimeStringForMat)})
	}
	sort.Slice(h.Alarms, func(i, j int) bool {
		if h.Alarms[i].Severity != h.Alarms[j].Severity {
			return h.Alarms[i].Severity == "critical"
		}
		return h.Alarms[i].Since < h.Alarms[j].Since
	})
	return h
}

// storageClusterSummarize adds the counts and capacity of the clusters of a response
func storageClusterSummarize(ctx context.Context, resps map[int64]*StorageClusterResponse) {
	ids := []int64{}
	for id := range resps {
		ids = append(ids, id)
	}
	for id, s := range storageClusterAdmin.Summaries(ctx, ids) {
		if r := resps[id]; r != nil {
			r.NodeCount, r.DiskCount, r.PoolCount, r.CapacityBytes, r.FreeBytes = s.Nodes, s.Disks, s.Pools, s.CapacityBytes, s.FreeBytes
			r.AllocatedBytes = s.AllocatedBytes
		}
	}
}

// storageClusterDetail adds the file systems, pools and health report of a cluster to its response
func storageClusterDetail(ctx context.Context, cluster *model.StorageCluster, resp *StorageClusterResponse) {
	resp.HealthInfo = storageClusterHealth(ctx, cluster)
	resp.AutoJoin = storageAutoJoin(ctx, cluster)
	for _, f := range storageClusterAdmin.Filesystems(ctx, cluster.ID) {
		resp.Filesystems = append(resp.Filesystems, &StorageFilesystemResponse{ID: f.UUID, Name: f.Name, MountPoint: f.MountPoint,
			BlockSize: f.BlockSize, DataReplicas: f.DataReplicas, MetaReplicas: f.MetaReplicas, Status: f.Status,
			CapacityBytes: f.CapacityBytes, FreeBytes: f.FreeBytes, CapacityAt: formatTimePtr(f.CapacityAt)})
	}
	allocated := storageClusterAdmin.PoolAllocations(ctx, cluster.ID)
	for _, p := range storageClusterAdmin.Pools(ctx, cluster.ID) {
		resp.Pools = append(resp.Pools, &StorageClusterPoolResponse{ResourceReference: &ResourceReference{ID: p.UUID, Name: p.Name},
			Driver: p.Driver, Status: p.Status, Media: p.Media, MountPath: p.MountPath, QuotaBytes: p.QuotaBytes,
			CapacityBytes: p.CapacityBytes, UsedBytes: p.UsedBytes, AllocatedBytes: allocated[p.ID], DriverParams: rawAttrs(p.DriverParams)})
	}
	storageClusterSummarize(ctx, map[int64]*StorageClusterResponse{cluster.ID: resp})
}

// rawAttrs passes the kind specific attributes of a row on as they are; anything not a json object is left out
func rawAttrs(attrs string) json.RawMessage {
	if !strings.HasPrefix(strings.TrimSpace(attrs), "{") || !json.Valid([]byte(attrs)) {
		return nil
	}
	return json.RawMessage(attrs)
}

// @Summary list the kinds of storage clusters
// @Description the kinds CloudLand can deploy or import, with the roles, disk role, suggested roles and host requirements of each (shared-storage-design.md §4.5)
// @tags StorageCluster
// @Produce json
// @Success 200 {object} StorageBackendListResponse
// @Router /storage_backends [get]
func (v *StorageClusterAPI) ListBackends(c *gin.Context) {
	backends, err := storageClusterAdmin.ListBackends(c.Request.Context())
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list storage kinds", err)
		return
	}
	resp := &StorageBackendListResponse{StorageBackends: []*StorageBackendResponse{}}
	for _, b := range backends {
		base, once := b.DefaultRoles()
		req := b.Requirements()
		resp.StorageBackends = append(resp.StorageBackends, &StorageBackendResponse{Kind: b.Kind(), Roles: b.Roles(), DiskRole: b.DiskRole(),
			DefaultRoles: base, DefaultOnceRoles: once, Systems: req.Systems, KernelModule: req.KernelModule, Containers: req.Containers,
			Package: req.Package, Capabilities: b.Capabilities(), PoolDriver: b.PoolDriver()})
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary list storage clusters
// @tags StorageCluster
// @Produce json
// @Success 200 {object} StorageClusterListResponse
// @Router /storage_clusters [get]
func (v *StorageClusterAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if offset < 0 || limit < 0 || limit > 500 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset or limit", nil)
		return
	}
	total, clusters, err := storageClusterAdmin.List(ctx, int64(offset), int64(limit))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list storage clusters", err)
		return
	}
	resp := &StorageClusterListResponse{Offset: offset, Total: int(total), Limit: len(clusters), StorageClusters: []*StorageClusterResponse{}}
	byID := map[int64]*StorageClusterResponse{}
	for _, cluster := range clusters {
		r := storageClusterResponse(ctx, cluster, nil, nil)
		byID[cluster.ID] = r
		resp.StorageClusters = append(resp.StorageClusters, r)
	}
	storageClusterSummarize(ctx, byID)
	c.JSON(http.StatusOK, resp)
}

// @Summary get a storage cluster
// @tags StorageCluster
// @Produce json
// @Param   id  path  string  true  "Cluster UUID"
// @Success 200 {object} StorageClusterResponse
// @Router /storage_clusters/{id} [get]
func (v *StorageClusterAPI) Get(c *gin.Context) {
	ctx := c.Request.Context()
	cluster, nodes, disks, err := storageClusterAdmin.Get(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage cluster", err)
		return
	}
	resp := storageClusterResponse(ctx, cluster, nodes, disks)
	storageClusterDetail(ctx, cluster, resp)
	c.JSON(http.StatusOK, resp)
}

// StorageExpandPayload adds hosts with their disks (POST .../nodes) or disks of members (POST .../disks)
type StorageExpandPayload struct {
	Nodes            []*StorageNodePayload `json:"nodes" binding:"omitempty,max=64,dive"`
	Disks            []*StorageDiskPayload `json:"disks" binding:"omitempty,max=512,dive"`
	AllowUnsupported bool                  `json:"allow_unsupported"`
	// The file system the disks go into (POST .../disks); the first one of the cluster when empty
	Filesystem string `json:"filesystem" binding:"omitempty,max=32"`
}

type StorageRebalancePayload struct {
	// Name of the file system; the first one of the cluster when empty
	Filesystem string `json:"filesystem" binding:"omitempty,max=32"`
}

// expandPlan turns the hosts and disks of an expansion into host ids: the disks may be on the hosts of the request or
// on members of the cluster
func (v *StorageClusterAPI) expandPlan(c *gin.Context, payload *StorageExpandPayload) (*services.StorageClusterExpand, bool) {
	req := &services.StorageClusterExpand{AllowUnsupported: payload.AllowUnsupported, Filesystem: payload.Filesystem}
	hosts := map[string]int32{}
	resolve := func(uuid string) (int32, bool) {
		if id, ok := hosts[uuid]; ok {
			return id, true
		}
		ids, ok := v.resolveHosts(c, []string{uuid})
		if !ok {
			return 0, false
		}
		hosts[uuid] = ids[0]
		return ids[0], true
	}
	for _, n := range payload.Nodes {
		id, ok := resolve(n.Hypervisor)
		if !ok {
			return nil, false
		}
		req.Nodes = append(req.Nodes, &services.StorageNodePlan{Hostid: id, Roles: n.Roles})
	}
	for _, d := range payload.Disks {
		id, ok := resolve(d.Hypervisor)
		if !ok {
			return nil, false
		}
		req.Disks = append(req.Disks, &services.StorageDiskPlan{Hostid: id, DiskID: d.DiskID, Media: d.Media, Wipe: d.Wipe})
	}
	return req, true
}

func (v *StorageClusterAPI) taskStarted(c *gin.Context, task *model.StorageTask, err error, what string) {
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, what, err)
		return
	}
	c.JSON(http.StatusAccepted, storageTaskResponse(task, v.clusterRefs(c.Request.Context(), []*model.StorageTask{task})))
}

// @Summary add hosts to a storage cluster
// @Description hosts join a managed cluster with their disks, or none (shared-storage-design.md §7.5): prechecked, installed and added; their disks go into the file system. Rebalancing is a separate task
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   id       path  string                true  "Cluster UUID"
// @Param   message  body  StorageExpandPayload  true  "Hosts and their disks"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/nodes [post]
func (v *StorageClusterAPI) AddNodes(c *gin.Context) {
	payload := &StorageExpandPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	req, ok := v.expandPlan(c, payload)
	if !ok {
		return
	}
	task, err := storageClusterAdmin.AddNodes(c.Request.Context(), c.Param("id"), req)
	v.taskStarted(c, task, err, "Failed to add hosts to the storage cluster")
}

// @Summary remove a host from a storage cluster
// @Description the disks of the host leave first (their data moves to the other disks), then the host leaves and is cleaned. offline=true removes a host that is gone for good: nothing runs on it, its disks are dropped without moving their data; confirm must be its name (shared-storage-design.md §7.5)
// @tags StorageCluster
// @Produce json
// @Param   id              path   string  true   "Cluster UUID"
// @Param   hypervisor      path   string  true   "Hypervisor UUID"
// @Param   offline         query  bool    false  "The host is gone for good"
// @Param   confirm         query  string  false  "The host name, for an offline removal"
// @Param   purge_packages  query  bool    false  "Remove the storage software from the host too"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/nodes/{hypervisor} [delete]
func (v *StorageClusterAPI) RemoveNode(c *gin.Context) {
	ids, ok := v.resolveHosts(c, []string{c.Param("hypervisor")})
	if !ok {
		return
	}
	task, err := storageClusterAdmin.RemoveNode(c.Request.Context(), c.Param("id"), &services.StorageNodeRemove{Hostid: ids[0],
		Offline: c.Query("offline") == "true", Confirm: c.Query("confirm"), PurgePackages: c.Query("purge_packages") == "true"})
	v.taskStarted(c, task, err, "Failed to remove the host from the storage cluster")
}

// @Summary add disks to a storage cluster
// @Description disks of member hosts join the cluster: they become NSDs of its file system (shared-storage-design.md §7.5). Rebalancing is a separate task
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   id       path  string                true  "Cluster UUID"
// @Param   message  body  StorageExpandPayload  true  "Disks (nodes must be empty)"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/disks [post]
func (v *StorageClusterAPI) AddDisks(c *gin.Context) {
	payload := &StorageExpandPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	req, ok := v.expandPlan(c, payload)
	if !ok {
		return
	}
	task, err := storageClusterAdmin.AddDisks(c.Request.Context(), c.Param("id"), req)
	v.taskStarted(c, task, err, "Failed to add disks to the storage cluster")
}

// @Summary remove a disk from a storage cluster
// @Description the data of the disk moves to the other disks, then it is wiped and given back to its host (shared-storage-design.md §7.5). Refused when the cluster would have too few failure groups for its replicas
// @tags StorageCluster
// @Produce json
// @Param   id       path  string  true  "Cluster UUID"
// @Param   disk_id  path  string  true  "Disk UUID (the id of a disk of the cluster)"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/disks/{disk_id} [delete]
func (v *StorageClusterAPI) RemoveDisk(c *gin.Context) {
	task, err := storageClusterAdmin.RemoveDisk(c.Request.Context(), c.Param("id"), c.Param("disk_id"))
	v.taskStarted(c, task, err, "Failed to remove the disk from the storage cluster")
}

// @Summary rebalance a file system of a storage cluster
// @Description spread the data over all disks (gpfs mmrestripefs -b): long and I/O heavy, best started at a quiet time
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   id       path  string                   true  "Cluster UUID"
// @Param   message  body  StorageRebalancePayload  true  "File system"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/rebalance [post]
func (v *StorageClusterAPI) Rebalance(c *gin.Context) {
	payload := &StorageRebalancePayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	task, err := storageClusterAdmin.Rebalance(c.Request.Context(), c.Param("id"), payload.Filesystem)
	v.taskStarted(c, task, err, "Failed to start the rebalance")
}

type StorageRotateKeysPayload struct {
	// Renew the SSH key the admin hosts log in to the members with
	SSH bool `json:"ssh"`
	// Renew the key of the client user (ceph). Running instances keep their sessions with the old key and take the
	// new one when they start again or migrate
	Client bool `json:"client"`
}

// @Summary rotate the keys of a storage cluster
// @Description renew the SSH key of a managed cluster and, for Ceph, the key of its client user, without a moment when a host or QEMU is left without a valid key (shared-storage-design.md §6.6, §8.4). Every member must be online. Nothing named: both. The new client key becomes the key of the user the first time a host uses it, the old one is refused for new sessions from then on; running instances keep their sessions and take the new key when they start again or migrate
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   id       path  string                    true   "Cluster UUID"
// @Param   message  body  StorageRotateKeysPayload  false  "What to rotate"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/rotate_keys [post]
func (v *StorageClusterAPI) RotateKeys(c *gin.Context) {
	payload := &StorageRotateKeysPayload{}
	if err := c.ShouldBindJSON(payload); err != nil && c.Request.ContentLength > 0 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	task, err := storageClusterAdmin.RotateKeys(c.Request.Context(), c.Param("id"), &services.StorageRotateKeys{SSH: payload.SSH, Client: payload.Client})
	v.taskStarted(c, task, err, "Failed to start the rotation of the keys")
}

type StorageUpgradePayload struct {
	// gpfs: UUID of the package of the new release (verified, its license accepted)
	Package string `json:"package" binding:"omitempty,uuid"`
	// gpfs: once every host runs the new release, raise the cluster and its file systems to it. Irreversible: hosts
	// of an older release can not join afterwards
	Finalize bool `json:"finalize"`
	// ceph: the image of the daemons; empty: the one of the release the hosts install from their distribution
	Image string `json:"image" binding:"omitempty,max=200"`
}

// @Summary upgrade a storage cluster
// @Description roll a managed cluster to a new release, one host after the other (shared-storage-design.md §7.7, §8.7). gpfs: name the package of the new release; each host has the instances that use the cluster moved off (as maintenance does), GPFS stopped, upgraded and started, and its disks brought back before the next one; then, separately, finalize to raise the cluster and its file systems to the release (irreversible). ceph: the hosts install the release their distribution has and cephadm upgrades the daemons; no instance moves (QEMU takes the new client library when it starts again or migrates). Every member must be online
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   id       path  string                 true   "Cluster UUID"
// @Param   message  body  StorageUpgradePayload  false  "What to upgrade to"
// @Success 202 {object} StorageTaskResponse
// @Router /storage_clusters/{id}/upgrade [post]
func (v *StorageClusterAPI) Upgrade(c *gin.Context) {
	payload := &StorageUpgradePayload{}
	if err := c.ShouldBindJSON(payload); err != nil && c.Request.ContentLength > 0 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	task, err := storageClusterAdmin.Upgrade(c.Request.Context(), c.Param("id"),
		&services.StorageUpgrade{PackageUUID: payload.Package, Finalize: payload.Finalize, Image: payload.Image})
	v.taskStarted(c, task, err, "Failed to start the upgrade")
}

type StorageClusterImportPayload struct {
	// A kind from GET /storage_backends whose capabilities include external
	Kind        string `json:"kind" binding:"required,max=16"`
	Name        string `json:"name" binding:"required,max=63"`
	Description string `json:"description" binding:"omitempty,max=256"`
	// The hosts that use the cluster: members of it already
	Hypervisors []string `json:"hypervisors" binding:"required,min=1,max=64,dive,uuid"`
	// What the kind must know to use the cluster (gpfs: fs_name, mount_point)
	Params json.RawMessage `json:"params" swaggertype:"object"`
}

// @Summary import a storage cluster
// @Description record a cluster its own admins run and that the given hosts are members of already (shared-storage-design.md §7.8): every host is checked, nothing of the cluster is changed. Pools are then registered on directories that exist (POST /storage_pools with the cluster and params.path)
// @tags StorageCluster
// @Accept  json
// @Produce json
// @Param   message  body  StorageClusterImportPayload  true  "Cluster"
// @Success 202 {object} StorageClusterResponse
// @Router /storage_clusters/import [post]
func (v *StorageClusterAPI) Import(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &StorageClusterImportPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	ids, ok := v.resolveHosts(c, payload.Hypervisors)
	if !ok {
		return
	}
	cluster, _, err := storageClusterAdmin.Import(ctx, &services.StorageClusterImport{Kind: payload.Kind, Name: payload.Name,
		Description: payload.Description, Hostids: ids, Params: payload.Params})
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to import the storage cluster", err)
		return
	}
	cluster, nodes, disks, err := storageClusterAdmin.Get(ctx, cluster.UUID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage cluster", err)
		return
	}
	c.JSON(http.StatusAccepted, storageClusterResponse(ctx, cluster, nodes, disks))
}
