/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The storage task engine (shared-storage-design.md §6.2). A task is one operation on a storage cluster, split into
// steps; a step runs a script under scripts/kvm/storage on some hosts (or on one admin host). Every script starts its
// work in the background with async_exec, so a long step never holds the serial command queue of cloudlet, and
// reports back with
//
//	|:-COMMAND-:| storage_task_run '<run ID>' '<status>' '<progress>' '<base64 json>'
//
// when it ends (sent by the next heartbeat) and whenever stc_poll.sh asks how it is doing. The host keeps a job
// directory per run, so stc_poll.sh can always tell whether a job runs, ended (and send its result again), was
// interrupted, or never arrived (and the command is sent again). Every change of a task is made with its row locked,
// so two clapi instances, the callbacks and the worker loop can all move the same task.

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
	"api/src/utils/tracing"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	storageScriptDir = "/opt/cloudland/scripts/backend/storage"
	// The worker loop polls the runs that have not reported for this long
	storagePollEvery = 15 * time.Second
	// A poll waits in the serial command queue of the host, behind whatever runs there (an image download, a
	// migration); it is not sent again while one is out, unless that one is this old
	storagePollOutstanding = 2 * time.Minute
	// A run its host has no trace of: the command is still queued behind a long command on the host, or it never
	// arrived. It is sent again until this long after it was started, then fails. A job that vanished (the host
	// restarted) is told apart by the host and fails at once
	storageRunMissingAfter  = 30 * time.Minute
	storageRunMaxDispatches = 10
	// A run on a host that stopped reporting fails after this long
	storageRunOfflineAfter = 5 * time.Minute
	// Upper bound of the log tail kept per run
	storageLogTailMax = 64 << 10
	// Status a host gives for a run it has no trace of (stc_poll.sh)
	storageRunMissing = "missing"
	// The command reaches the node in an environment variable, which holds at most 128 KiB
	storageInputMax = 120 << 10

	// Slots of a cluster a task kind takes (§6.2.1)
	storageSlotNone       = ""
	storageSlotStructural = "structural"
	storageSlotPool       = "pool"

	// Key of the PostgreSQL advisory lock that picks the clapi running the background loops
	storageLeaderKey = 0x434c5354 // "CLST"
)

// StorageStepPlan is a step of a task as planned when the task is created
type StorageStepPlan struct {
	Name    string
	Scope   string  // nodes | admin
	Hostids []int32 // hosts of a nodes step; candidate hosts of an admin step, in order of preference
	Timeout time.Duration
}

// storageStepDef tells the engine how to run a step
type storageStepDef struct {
	// Script under scripts/kvm/storage; it gets the run ID as its argument and the input as JSON on stdin
	Script string
	// Input builds the input of the script for the run on hostid; it may read the results of earlier steps
	Input func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error)
	// Done runs in the transaction that marks the step succeeded; an error fails the step
	Done func(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error
	// RetryFrom names an earlier step a retry must start from, when this step uses what that one found out right
	// before it (device names resolved just before disks are written)
	RetryFrom string
	// OnlineOnly: a nodes step that runs on the hosts online when it starts and skips the others, for work a host
	// that is away catches up on later by itself (the pool lists, §9.2)
	OnlineOnly bool
	// Control: a step clapi does itself instead of a script on the hosts (moving the instances off a host before it
	// is upgraded). Called outside the transactions of the engine when the step starts, at each move of the task and
	// each round of the worker, until it is done; it must do nothing twice. An error fails the step, so does the
	// timeout of the step. Script and Input are not used
	Control func(ctx context.Context, task *model.StorageTask, step *model.StorageTaskStep) (done bool, err error)
}

// storageTaskKind is a kind of task: the slot it takes, the steps it may have and what to do when it ends
type storageTaskKind struct {
	Slot  string
	Steps map[string]*storageStepDef
	// Finish runs in the transaction that ends the task: succeeded, or aborted by an admin. It does not run for a
	// failed task, which keeps its slot until it is retried or aborted
	Finish func(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error
}

var storageTaskKinds = map[string]*storageTaskKind{}

// registerStorageTaskKind registers a task kind. A kind of one backend is registered as "<backend>:<kind>"
// (gpfs:deploy); the tasks every backend shares (precheck, selftest) without a backend
func registerStorageTaskKind(name string, kind *storageTaskKind) {
	storageTaskKinds[name] = kind
}

func storageTaskKindName(backend, kind string) string {
	if backend == "" {
		return kind
	}
	return backend + ":" + kind
}

// storageTaskKindOf returns the definition of the kind of a task, nil when nothing registered it
func storageTaskKindOf(task *model.StorageTask) *storageTaskKind {
	return storageTaskKinds[storageTaskKindName(task.Backend, task.Kind)]
}

// StorageRunPayload is the JSON a script sends back
type StorageRunPayload struct {
	Message string          `json:"message"`
	Log     string          `json:"log"`
	Result  json.RawMessage `json:"result"`
}

// storageDispatch is a command to send once the transaction that decided it is committed
type storageDispatch struct {
	RunID   int64
	Hostid  int32
	Command string
	// A run is failed when its command can not be sent; a kill is not
	Kill bool
}

func storageSlotColumn(slot string) string {
	switch slot {
	case storageSlotStructural:
		return "active_task"
	case storageSlotPool:
		return "active_pool_task"
	}
	return ""
}

func lockStorageTask(tx *gorm.DB, taskID int64) (task *model.StorageTask, err error) {
	task = &model.StorageTask{}
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(task, taskID).Error
	return
}

func storageTaskSteps(tx *gorm.DB, taskID int64) (steps []*model.StorageTaskStep, err error) {
	err = tx.Where("task_id = ?", taskID).Order("seq").Find(&steps).Error
	return
}

// currentStorageStep is the first step that has not succeeded
func currentStorageStep(steps []*model.StorageTaskStep) *model.StorageTaskStep {
	for _, s := range steps {
		if s.Status != model.StorageStepSucceeded {
			return s
		}
	}
	return nil
}

// latestStorageRuns returns the last attempt of a step on every host it ran on
func latestStorageRuns(tx *gorm.DB, stepID int64) (runs []*model.StorageTaskRun, err error) {
	all := []*model.StorageTaskRun{}
	if err = tx.Where("step_id = ?", stepID).Order("hostid, attempt").Find(&all).Error; err != nil {
		return
	}
	byHost := map[int32]*model.StorageTaskRun{}
	for _, r := range all {
		byHost[r.Hostid] = r
	}
	for _, r := range byHost {
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Hostid < runs[j].Hostid })
	return
}

// currentStepRuns returns the runs that decide a step: the last attempt on every host of a nodes step, the last run
// of an admin step (a retry may take another admin host, and the one that failed before no longer counts)
func currentStepRuns(tx *gorm.DB, step *model.StorageTaskStep) (runs []*model.StorageTaskRun, err error) {
	if step.Scope != model.StorageStepScopeAdmin {
		return latestStorageRuns(tx, step.ID)
	}
	err = tx.Where("step_id = ?", step.ID).Order("id DESC").Limit(1).Find(&runs).Error
	return
}

// liveStorageRuns returns the runs of a task that have not ended
func liveStorageRuns(tx *gorm.DB, taskID int64) (runs []*model.StorageTaskRun, err error) {
	err = tx.Where("status IN ? AND step_id IN (SELECT id FROM storage_task_steps WHERE task_id = ?)",
		[]string{model.StorageRunDispatched, model.StorageRunRunning}, taskID).Find(&runs).Error
	return
}

func parseHostids(raw string) (ids []int32) {
	_ = json.Unmarshal([]byte(raw), &ids)
	return
}

func storageRunTerminal(status string) bool {
	return status == model.StorageRunSucceeded || status == model.StorageRunFailed
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	// Start at a line, not in the middle of a UTF-8 sequence
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < 1024 {
		s = s[i+1:]
	}
	return s
}

// createStorageTask records a task of no particular backend with its steps and starts it
func createStorageTask(ctx context.Context, clusterID int64, kind string, params interface{}, plan []*StorageStepPlan) (task *model.StorageTask, err error) {
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: clusterID, Kind: kind, Params: params, Plan: plan})
}

// storageTaskSpec is a task to start
type storageTaskSpec struct {
	ClusterID int64
	Backend   string
	Kind      string
	Params    interface{}
	Plan      []*StorageStepPlan
	// Prepare runs first in the transaction that records the task and returns the cluster the task works on, for a
	// task that creates its cluster (a deployment): the cluster and the task that holds it appear together or not
	// at all
	Prepare func(tx *gorm.DB) (clusterID int64, err error)
}

// startStorageTask records a task with its steps and starts it. A task of a kind with a slot takes that slot of
// its cluster: no other task of the same slot can start on the cluster until this one succeeds or is aborted
func startStorageTask(ctx context.Context, spec *storageTaskSpec) (task *model.StorageTask, err error) {
	clusterID, kind, params, plan := spec.ClusterID, spec.Kind, spec.Params, spec.Plan
	k := storageTaskKinds[storageTaskKindName(spec.Backend, kind)]
	if k == nil {
		return nil, NewCLError(ErrInvalidParameter, "Unknown storage task kind "+storageTaskKindName(spec.Backend, kind), nil)
	}
	if len(plan) == 0 {
		return nil, NewCLError(ErrInvalidParameter, "The task has no step", nil)
	}
	for _, p := range plan {
		if k.Steps[p.Name] == nil {
			return nil, NewCLError(ErrInvalidParameter, fmt.Sprintf("Task %s has no step %s", kind, p.Name), nil)
		}
		if len(p.Hostids) == 0 {
			return nil, NewCLError(ErrInvalidParameter, fmt.Sprintf("Step %s has no host", p.Name), nil)
		}
	}
	slotColumn := storageSlotColumn(k.Slot)
	if clusterID == 0 && slotColumn != "" && spec.Prepare == nil {
		return nil, NewCLError(ErrInvalidParameter, "Task "+kind+" needs a cluster", nil)
	}
	db := dbs.DBContext(ctx)
	err = db.Transaction(func(tx *gorm.DB) error {
		if spec.Prepare != nil {
			id, err := spec.Prepare(tx)
			if err != nil {
				return err
			}
			clusterID = id
		}
		// Encoded after Prepare: the parameters may name what it recorded (the id of a new pool)
		paramsJSON, err := json.Marshal(params)
		if err != nil {
			return NewCLError(ErrJSONMarshalFailed, "Failed to encode the task parameters", err)
		}
		if slotColumn != "" {
			cluster := &model.StorageCluster{}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(cluster, clusterID).Error; err != nil {
				return NewCLError(ErrStorageClusterNotFound, "Storage cluster not found", err)
			}
			holder := cluster.ActiveTask
			if k.Slot == storageSlotPool {
				holder = cluster.ActivePoolTask
			}
			if holder != 0 {
				return NewCLError(ErrStorageClusterBusy, fmt.Sprintf("Task %d is still running on this cluster, or waits to be retried or aborted", holder), nil)
			}
		}
		task = &model.StorageTask{ClusterID: clusterID, Kind: kind, Backend: spec.Backend, Status: model.StorageTaskRunning,
			Params: string(paramsJSON), CurrentStep: 1, CreatorName: GetMemberShip(ctx).UserName}
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		for i, p := range plan {
			hostids, _ := json.Marshal(p.Hostids)
			step := &model.StorageTaskStep{TaskID: task.ID, Seq: int32(i + 1), Name: p.Name, Scope: p.Scope, Hostids: string(hostids),
				Status: model.StorageStepPending, TimeoutSec: int32(p.Timeout / time.Second)}
			if err := tx.Create(step).Error; err != nil {
				return err
			}
		}
		if slotColumn != "" {
			return tx.Model(&model.StorageCluster{}).Where("id = ?", clusterID).Update(slotColumn, task.ID).Error
		}
		return nil
	})
	if err != nil {
		if _, ok := err.(*CLError); !ok {
			err = NewCLError(ErrSQLSyntaxError, "Failed to create the storage task", err)
		}
		return nil, err
	}
	advanceStorageTask(ctx, task.ID)
	return
}

// advanceStorageTask moves a task on as far as it can go now, sending the commands it decides on
func advanceStorageTask(ctx context.Context, taskID int64) {
	// Bounded: a step that ends at once (every run failed to build or to send) moves the task on again
	for i := 0; i < 64; i++ {
		sends, control, again, err := advanceStorageTaskOnce(ctx, taskID)
		if err == nil && control != 0 {
			again = runStorageControl(ctx, taskID, control)
		}
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to advance storage task %d: %v", taskID, err)
			return
		}
		if sendStorageCommands(ctx, sends) {
			again = true
		}
		if !again {
			return
		}
	}
}

func advanceStorageTaskOnce(ctx context.Context, taskID int64) (sends []*storageDispatch, control int64, again bool, err error) {
	db := dbs.DBContext(ctx)
	err = db.Transaction(func(tx *gorm.DB) error {
		task, err := lockStorageTask(tx, taskID)
		if err != nil {
			return err
		}
		kind := storageTaskKindOf(task)
		if task.Status == model.StorageTaskAborting {
			live, err := liveStorageRuns(tx, task.ID)
			if err != nil || len(live) > 0 {
				return err
			}
			return finishStorageTask(ctx, tx, task, kind, false)
		}
		if task.Status != model.StorageTaskRunning {
			return nil
		}
		if kind == nil {
			return failStorageTask(tx, task, "unknown task kind "+task.Kind)
		}
		steps, err := storageTaskSteps(tx, task.ID)
		if err != nil {
			return err
		}
		cur := currentStorageStep(steps)
		if cur == nil {
			return finishStorageTask(ctx, tx, task, kind, true)
		}
		if task.CurrentStep != cur.Seq {
			task.CurrentStep = cur.Seq
			if err := tx.Model(task).Update("current_step", cur.Seq).Error; err != nil {
				return err
			}
		}
		def := kind.Steps[cur.Name]
		if def == nil && cur.Status != model.StorageStepFailed {
			// A step this version no longer has (renamed or removed by an upgrade while the task waited)
			return failStorageStep(tx, task, cur, "the step is unknown to this version of clapi")
		}
		if def != nil && def.Control != nil && cur.Status != model.StorageStepFailed {
			// Done by clapi, after this transaction
			if cur.Status == model.StorageStepPending {
				now := time.Now()
				if err := tx.Model(cur).Updates(map[string]interface{}{"status": model.StorageStepRunning, "started_at": &now, "finished_at": nil}).Error; err != nil {
					return err
				}
			}
			control = cur.ID
			return nil
		}
		switch cur.Status {
		case model.StorageStepPending:
			hosts, msg := storageStepHosts(tx, cur, def)
			if len(hosts) == 0 {
				return failStorageStep(tx, task, cur, msg)
			}
			now := time.Now()
			if err := tx.Model(cur).Updates(map[string]interface{}{"status": model.StorageStepRunning, "started_at": &now, "finished_at": nil}).Error; err != nil {
				return err
			}
			sends, err = startStorageRuns(ctx, tx, task, cur, def, hosts)
			if err != nil {
				return err
			}
			again = len(sends) == 0
		case model.StorageStepRunning:
			runs, err := currentStepRuns(tx, cur)
			if err != nil {
				return err
			}
			if len(runs) == 0 {
				return failStorageStep(tx, task, cur, "the step has no run")
			}
			failed := []string{}
			for _, r := range runs {
				if !storageRunTerminal(r.Status) {
					return nil
				}
				if r.Status == model.StorageRunFailed {
					failed = append(failed, fmt.Sprintf("%s: %s", hostName(tx, r.Hostid), r.Message))
				}
			}
			if len(failed) > 0 {
				return failStorageStep(tx, task, cur, strings.Join(failed, "; "))
			}
			if def.Done != nil {
				if err := def.Done(ctx, tx, task, cur, runs); err != nil {
					return failStorageStep(tx, task, cur, err.Error())
				}
			}
			now := time.Now()
			if err := tx.Model(cur).Updates(map[string]interface{}{"status": model.StorageStepSucceeded, "finished_at": &now}).Error; err != nil {
				return err
			}
			again = true
		case model.StorageStepFailed:
			return failStorageTask(tx, task, "")
		}
		return nil
	})
	return
}

// runStorageControl does a step clapi does itself (storageStepDef.Control) and records how it went. It tells whether
// the step ended, so the task moves on
func runStorageControl(ctx context.Context, taskID, stepID int64) (ended bool) {
	db := dbs.DBContext(ctx)
	task := &model.StorageTask{}
	step := &model.StorageTaskStep{}
	if db.Take(task, taskID).Error != nil || db.Take(step, stepID).Error != nil || task.Status != model.StorageTaskRunning ||
		step.Status != model.StorageStepRunning {
		return false
	}
	kind := storageTaskKindOf(task)
	if kind == nil || kind.Steps[step.Name] == nil || kind.Steps[step.Name].Control == nil {
		return false
	}
	var done bool
	var cerr error
	if step.TimeoutSec > 0 && step.StartedAt != nil && time.Since(*step.StartedAt) > time.Duration(step.TimeoutSec)*time.Second {
		cerr = fmt.Errorf("timed out after %s", time.Duration(step.TimeoutSec)*time.Second)
	} else {
		done, cerr = kind.Steps[step.Name].Control(ctx, task, step)
	}
	if !done && cerr == nil {
		return false
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		task, err := lockStorageTask(tx, taskID)
		if err != nil {
			return err
		}
		if err = tx.Take(step, stepID).Error; err != nil || task.Status != model.StorageTaskRunning || step.Status != model.StorageStepRunning {
			return err
		}
		ended = true
		if cerr != nil {
			return failStorageStep(tx, task, step, cerr.Error())
		}
		now := time.Now()
		return tx.Model(step).Updates(map[string]interface{}{"status": model.StorageStepSucceeded, "finished_at": &now}).Error
	})
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to record step %d of storage task %d: %v", stepID, taskID, err)
		return false
	}
	return ended
}

// workStorageControls moves on the tasks whose current step clapi does itself: what it waits for (migrations) does
// not report to the task
func workStorageControls(ctx context.Context) {
	db := dbs.DBContext(ctx)
	steps := []*model.StorageTaskStep{}
	if err := db.Joins("JOIN storage_tasks ON storage_tasks.id = storage_task_steps.task_id").
		Where("storage_tasks.status = ? AND storage_task_steps.status = ? AND storage_task_steps.seq = storage_tasks.current_step",
			model.StorageTaskRunning, model.StorageStepRunning).Find(&steps).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to list the running storage steps: %v", err)
		return
	}
	for _, step := range steps {
		task := &model.StorageTask{}
		if db.Take(task, step.TaskID).Error != nil {
			continue
		}
		if kind := storageTaskKindOf(task); kind != nil && kind.Steps[step.Name] != nil && kind.Steps[step.Name].Control != nil {
			advanceStorageTask(ctx, task.ID)
		}
	}
}

// storageStepHosts are the hosts a step runs on now: every host of a nodes step (the online ones for a step that
// runs on those only), the first online host of an admin step
func storageStepHosts(tx *gorm.DB, step *model.StorageTaskStep, def *storageStepDef) (hosts []int32, msg string) {
	ids := parseHostids(step.Hostids)
	if step.Scope != model.StorageStepScopeAdmin {
		if def == nil || !def.OnlineOnly {
			return ids, "the step has no host"
		}
		for _, id := range ids {
			if _, online := hostOnline(tx, id); online {
				hosts = append(hosts, id)
			}
		}
		return hosts, "no host of the step is online"
	}
	for _, id := range ids {
		if _, online := hostOnline(tx, id); online {
			return []int32{id}, ""
		}
	}
	return nil, "no admin host of the cluster is online"
}

// startStorageRuns creates the runs of a step on some hosts and builds their commands. A run whose input can not be
// built fails at once
func startStorageRuns(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, def *storageStepDef,
	hosts []int32) (sends []*storageDispatch, err error) {
	now := time.Now()
	for _, h := range hosts {
		var attempt int32
		if err = tx.Model(&model.StorageTaskRun{}).Where("step_id = ? AND hostid = ?", step.ID, h).
			Select("COALESCE(MAX(attempt), 0)").Scan(&attempt).Error; err != nil {
			return nil, err
		}
		run := &model.StorageTaskRun{StepID: step.ID, Hostid: h, Attempt: attempt + 1, Status: model.StorageRunDispatched,
			Dispatches: 1, StartedAt: now, UpdatedAt: now}
		if err = tx.Create(run).Error; err != nil {
			return nil, err
		}
		command, buildErr := storageRunCommand(ctx, tx, task, step, def, run)
		if buildErr != nil {
			if err = tx.Model(run).Updates(map[string]interface{}{"status": model.StorageRunFailed, "message": truncate(buildErr.Error(), 1024)}).Error; err != nil {
				return nil, err
			}
			continue
		}
		sends = append(sends, &storageDispatch{RunID: run.ID, Hostid: h, Command: command})
	}
	return
}

func storageRunCommand(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, def *storageStepDef,
	run *model.StorageTaskRun) (string, error) {
	var input interface{} = map[string]interface{}{}
	if def.Input != nil {
		var err error
		if input, err = def.Input(ctx, db, task, step, run.Hostid); err != nil {
			return "", err
		}
	}
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	if len(body) > storageInputMax {
		return "", fmt.Errorf("the input of step %s is %d bytes, more than a node command can carry", step.Name, len(body))
	}
	return fmt.Sprintf("%s/%s '%d' <<'EOF'\n%s\nEOF", storageScriptDir, def.Script, run.ID, body), nil
}

// sendStorageCommands sends commands after the transaction that decided them is committed. A run whose command can
// not be sent fails; it reports whether one did, so the caller moves the task on again
func sendStorageCommands(ctx context.Context, sends []*storageDispatch) (failed bool) {
	db := dbs.DBContext(ctx)
	for _, s := range sends {
		err := HyperExecute(ctx, fmt.Sprintf("inter=%d", s.Hostid), s.Command)
		if err == nil || s.Kill {
			if err != nil {
				logger.Ctx(ctx).Warningf("Failed to stop storage run %d on host %d: %v", s.RunID, s.Hostid, err)
			}
			continue
		}
		db.Model(&model.StorageTaskRun{}).Where("id = ? AND status IN ?", s.RunID, []string{model.StorageRunDispatched, model.StorageRunRunning}).
			Updates(map[string]interface{}{"status": model.StorageRunFailed, "message": truncate("failed to send the command: "+err.Error(), 1024)})
		failed = true
	}
	return
}

func failStorageStep(tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, msg string) error {
	now := time.Now()
	if err := tx.Model(step).Updates(map[string]interface{}{"status": model.StorageStepFailed, "finished_at": &now}).Error; err != nil {
		return err
	}
	return failStorageTask(tx, task, fmt.Sprintf("%s: %s", step.Name, msg))
}

// failStorageTask stops a task at its current step. It keeps its slot, so nothing else of the kind runs on a
// half-done cluster before an admin retries or aborts the task
func failStorageTask(tx *gorm.DB, task *model.StorageTask, msg string) error {
	updates := map[string]interface{}{"status": model.StorageTaskFailed}
	if msg != "" {
		updates["message"] = truncate(msg, 1024)
	}
	return tx.Model(task).Updates(updates).Error
}

// finishStorageTask runs the finish of a task's kind and ends the task. The finish runs in a savepoint: when it fails
// none of its writes stay, and a task whose every step succeeded is left failed with no step to run, which a retry
// finishes again rather than an abort marking a working cluster or pool as broken
func finishStorageTask(ctx context.Context, tx *gorm.DB, task *model.StorageTask, kind *storageTaskKind, succeeded bool) error {
	if kind != nil && kind.Finish != nil {
		if err := tx.Transaction(func(sp *gorm.DB) error { return kind.Finish(ctx, sp, task, succeeded) }); err != nil {
			if succeeded {
				return failStorageTask(tx, task, "finishing: "+err.Error())
			}
			logger.Ctx(ctx).Errorf("Finishing aborted storage task %d: %v", task.ID, err)
		}
	}
	status := model.StorageTaskSucceeded
	if !succeeded {
		status = model.StorageTaskAborted
	}
	now := time.Now()
	if err := tx.Model(task).Updates(map[string]interface{}{"status": status, "finished_at": &now}).Error; err != nil {
		return err
	}
	if kind != nil && task.ClusterID > 0 {
		if column := storageSlotColumn(kind.Slot); column != "" {
			return tx.Model(&model.StorageCluster{}).Where("id = ? AND "+column+" = ?", task.ClusterID, task.ID).Update(column, 0).Error
		}
	}
	return nil
}

// HandleStorageTaskRun takes a report of a run from the host it was sent to. Reports are idempotent: cland sends a
// callback again when clapi did not answer, the end of a run comes both from the heartbeat and from a poll, and a
// run that ended does not change any more
func HandleStorageTaskRun(ctx context.Context, hostid int32, runID int64, status string, progress int32, payload *StorageRunPayload) (err error) {
	switch status {
	case model.StorageRunRunning, model.StorageRunSucceeded, model.StorageRunFailed, storageRunMissing:
	default:
		return fmt.Errorf("unknown run status %q", status)
	}
	db := dbs.DBContext(ctx)
	var taskID int64
	var resend []*storageDispatch
	advance := false
	err = db.Transaction(func(tx *gorm.DB) error {
		run := &model.StorageTaskRun{}
		if err := tx.Take(run, runID).Error; err != nil {
			return fmt.Errorf("run %d not found: %w", runID, err)
		}
		// Only the host the run was sent to reports on it
		if run.Hostid != hostid {
			return fmt.Errorf("run %d belongs to host %d, not %d", runID, run.Hostid, hostid)
		}
		step := &model.StorageTaskStep{}
		if err := tx.Take(step, run.StepID).Error; err != nil {
			return err
		}
		// The task row is the lock of everything in the task
		task, err := lockStorageTask(tx, step.TaskID)
		if err != nil {
			return err
		}
		taskID = task.ID
		if err := tx.Take(run, runID).Error; err != nil {
			return err
		}
		if storageRunTerminal(run.Status) {
			return nil
		}
		now := time.Now()
		updates := map[string]interface{}{"updated_at": now}
		if status == storageRunMissing {
			if now.Sub(run.StartedAt) >= storageRunMissingAfter || run.Dispatches >= storageRunMaxDispatches || task.Status == model.StorageTaskAborting {
				updates["status"] = model.StorageRunFailed
				updates["message"] = "the command never reached the host"
			} else {
				// The command was lost on the way (or clapi stopped before sending it): send it again. Starting a
				// job is idempotent on the host, so a copy still queued there does not run it twice
				kind := storageTaskKindOf(task)
				if kind == nil || kind.Steps[step.Name] == nil {
					return nil
				}
				command, err := storageRunCommand(ctx, tx, task, step, kind.Steps[step.Name], run)
				if err != nil {
					updates["status"] = model.StorageRunFailed
					updates["message"] = truncate(err.Error(), 1024)
				} else {
					updates["dispatches"] = run.Dispatches + 1
					resend = append(resend, &storageDispatch{RunID: run.ID, Hostid: run.Hostid, Command: command})
				}
			}
		} else {
			updates["status"] = status
			updates["progress"] = progress
			if payload != nil {
				if payload.Message != "" {
					updates["message"] = truncate(payload.Message, 1024)
				}
				if payload.Log != "" {
					updates["log_tail"] = tailString(payload.Log, storageLogTailMax)
				}
				if len(payload.Result) > 0 && string(payload.Result) != "null" {
					updates["result"] = string(payload.Result)
				}
			}
		}
		if err := tx.Model(run).Updates(updates).Error; err != nil {
			return err
		}
		if s, ok := updates["status"].(string); ok && storageRunTerminal(s) {
			advance = true
		}
		return nil
	})
	if err != nil {
		return
	}
	if sendStorageCommands(ctx, resend) {
		advance = true
	}
	if advance {
		advanceStorageTask(ctx, taskID)
	}
	return
}

// RetryStorageTask runs the failed step of a task again on the hosts it failed on. Hosts it succeeded on are not
// asked again, so every step script must check first whether its work is already done
func RetryStorageTask(ctx context.Context, taskID int64) (err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	var sends []*storageDispatch
	db := dbs.DBContext(ctx)
	err = db.Transaction(func(tx *gorm.DB) error {
		task, err := lockStorageTask(tx, taskID)
		if err != nil {
			return NewCLError(ErrStorageTaskNotFound, "Storage task not found", err)
		}
		if task.Status != model.StorageTaskFailed {
			return NewCLError(ErrStorageTaskState, "Only a failed task can be retried", nil)
		}
		kind := storageTaskKindOf(task)
		if kind == nil {
			return NewCLError(ErrStorageTaskState, "Unknown task kind "+task.Kind, nil)
		}
		steps, err := storageTaskSteps(tx, task.ID)
		if err != nil {
			return err
		}
		cur := currentStorageStep(steps)
		if cur == nil {
			// Every step succeeded and finishing failed: the task finishes again when it moves on
			return tx.Model(task).Updates(map[string]interface{}{"status": model.StorageTaskRunning, "message": ""}).Error
		}
		def := kind.Steps[cur.Name]
		if def == nil {
			return NewCLError(ErrStorageTaskState, "The step "+cur.Name+" is unknown to this version", nil)
		}
		if def.Control != nil {
			// Done by clapi again, from the start: its timeout too
			if err := tx.Model(cur).Updates(map[string]interface{}{"status": model.StorageStepPending, "started_at": nil, "finished_at": nil}).Error; err != nil {
				return err
			}
			return tx.Model(task).Updates(map[string]interface{}{"status": model.StorageTaskRunning, "message": ""}).Error
		}
		if def.RetryFrom != "" {
			var from *model.StorageTaskStep
			for _, s := range steps {
				if s.Seq < cur.Seq && s.Name == def.RetryFrom {
					from = s
				}
			}
			if from != nil {
				// Run the earlier step again on all of its hosts, then everything after it
				if err := tx.Model(&model.StorageTaskStep{}).Where("task_id = ? AND seq >= ? AND seq <= ?", task.ID, from.Seq, cur.Seq).
					Updates(map[string]interface{}{"status": model.StorageStepPending, "started_at": nil, "finished_at": nil}).Error; err != nil {
					return err
				}
				return tx.Model(task).Updates(map[string]interface{}{"status": model.StorageTaskRunning, "message": ""}).Error
			}
		}
		var hosts []int32
		if cur.Status == model.StorageStepPending || cur.Scope == model.StorageStepScopeAdmin {
			// The admin host that failed may be the problem: take whichever admin host is online now
			hosts, _ = storageStepHosts(tx, cur, def)
		} else {
			runs, err := latestStorageRuns(tx, cur.ID)
			if err != nil {
				return err
			}
			for _, r := range runs {
				if r.Status == model.StorageRunFailed {
					hosts = append(hosts, r.Hostid)
				}
			}
			if len(hosts) == 0 {
				// Every run succeeded and the check across the hosts (Done) failed, e.g. two hosts with one
				// name: what it checked may have changed on any of them, so they all run again
				hosts, _ = storageStepHosts(tx, cur, def)
			}
		}
		if len(hosts) == 0 {
			return NewCLError(ErrStorageTaskState, "No host to run the step on: "+cur.Name, nil)
		}
		now := time.Now()
		if err := tx.Model(cur).Updates(map[string]interface{}{"status": model.StorageStepRunning, "finished_at": nil, "started_at": &now}).Error; err != nil {
			return err
		}
		if err := tx.Model(task).Updates(map[string]interface{}{"status": model.StorageTaskRunning, "message": ""}).Error; err != nil {
			return err
		}
		sends, err = startStorageRuns(ctx, tx, task, cur, def, hosts)
		return err
	})
	if err != nil {
		return
	}
	sendStorageCommands(ctx, sends)
	advanceStorageTask(ctx, taskID)
	return
}

// AbortStorageTask stops a running or failed task. Jobs still going on hosts are killed first; the task ends, and
// frees its slot, once every run has ended (or its host has been offline too long)
func AbortStorageTask(ctx context.Context, taskID int64) (err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	var kills []*storageDispatch
	db := dbs.DBContext(ctx)
	err = db.Transaction(func(tx *gorm.DB) error {
		task, err := lockStorageTask(tx, taskID)
		if err != nil {
			return NewCLError(ErrStorageTaskNotFound, "Storage task not found", err)
		}
		if task.Status != model.StorageTaskRunning && task.Status != model.StorageTaskFailed {
			return NewCLError(ErrStorageTaskState, "The task is not running", nil)
		}
		live, err := liveStorageRuns(tx, task.ID)
		if err != nil {
			return err
		}
		if len(live) == 0 {
			return finishStorageTask(ctx, tx, task, storageTaskKindOf(task), false)
		}
		for _, r := range live {
			kills = append(kills, &storageDispatch{RunID: r.ID, Hostid: r.Hostid, Kill: true,
				Command: fmt.Sprintf("%s/stc_kill.sh '%d'", storageScriptDir, r.ID)})
		}
		return tx.Model(task).Updates(map[string]interface{}{"status": model.StorageTaskAborting, "message": "aborted by " + GetMemberShip(ctx).UserName}).Error
	})
	if err != nil {
		return
	}
	sendStorageCommands(ctx, kills)
	return
}

// StartStorageTaskWorker polls the runs that have not reported for a while, and fails the runs that ran out of
// time or whose host went away. Only the clapi holding the leader lock does it
func StartStorageTaskWorker() {
	go func() {
		time.Sleep(20 * time.Second)
		for round := 0; ; round++ {
			ctx, span := tracing.StartBackground(context.Background(), "storage.tasks")
			storageLeader(ctx, func(ctx context.Context) {
				workStorageRuns(ctx)
				workStorageControls(ctx)
				// Evacuations wait for the fences of their host, fences for their tasks (shared-storage-design.md §11)
				maintainStorageFences(ctx)
				advanceEvacuations(ctx)
				// The clusters' own rounds: every 4 rounds (a minute)
				if round%4 == 0 {
					maintainStorageClusters(ctx)
				}
				// Package clean-up deals in hours, the pool lists are a safety net: every 20 rounds (5 minutes)
				if round%20 == 0 {
					maintainStoragePackages(ctx)
					SyncSharedPools(ctx)
				}
			})
			span.End()
			time.Sleep(storagePollEvery)
		}
	}()
}

// storageLeader runs fn when this clapi gets the leader lock of the storage background loops. With two clapi
// instances only one polls the hosts; the other skips the round
func storageLeader(ctx context.Context, fn func(ctx context.Context)) {
	db := dbs.DBContext(ctx)
	if db.Dialector.Name() != "postgres" {
		fn(ctx)
		return
	}
	sqlDB, err := db.DB()
	if err != nil {
		return
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		logger.Ctx(ctx).Warningf("No connection for the storage leader lock: %v", err)
		return
	}
	defer conn.Close()
	leader := false
	if err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", storageLeaderKey).Scan(&leader); err != nil || !leader {
		return
	}
	defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", storageLeaderKey)
	fn(ctx)
}

func workStorageRuns(ctx context.Context) {
	db := dbs.DBContext(ctx)
	runs := []*model.StorageTaskRun{}
	if err := db.Where("status IN ?", []string{model.StorageRunDispatched, model.StorageRunRunning}).Find(&runs).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to list the storage runs: %v", err)
		return
	}
	now := time.Now()
	for _, run := range runs {
		step := &model.StorageTaskStep{}
		if err := db.Take(step, run.StepID).Error; err != nil {
			continue
		}
		reason := ""
		if step.TimeoutSec > 0 && now.Sub(run.StartedAt) > time.Duration(step.TimeoutSec)*time.Second {
			reason = fmt.Sprintf("timed out after %s", time.Duration(step.TimeoutSec)*time.Second)
		} else if _, online := hostOnline(db, run.Hostid); !online && now.Sub(run.UpdatedAt) > storageRunOfflineAfter {
			reason = "the host is offline"
		}
		if reason != "" {
			failStorageRun(ctx, run.ID, reason)
			continue
		}
		if now.Sub(run.UpdatedAt) < storagePollEvery {
			continue
		}
		// One poll out at a time per run: the poll waits in the serial command queue of the host
		claim := db.Model(&model.StorageTaskRun{}).
			Where("id = ? AND (polled_at IS NULL OR polled_at < ? OR polled_at < updated_at)", run.ID, now.Add(-storagePollOutstanding)).
			UpdateColumn("polled_at", now)
		if claim.Error != nil || claim.RowsAffected == 0 {
			continue
		}
		if err := HyperExecute(ctx, fmt.Sprintf("inter=%d", run.Hostid), fmt.Sprintf("%s/stc_poll.sh '%d'", storageScriptDir, run.ID)); err != nil {
			logger.Ctx(ctx).Warningf("Failed to poll storage run %d on host %d: %v", run.ID, run.Hostid, err)
		}
	}
}

// failStorageRun ends a run that will not report, then moves its task on
func failStorageRun(ctx context.Context, runID int64, reason string) {
	db := dbs.DBContext(ctx)
	var taskID int64
	changed := false
	err := db.Transaction(func(tx *gorm.DB) error {
		run := &model.StorageTaskRun{}
		if err := tx.Take(run, runID).Error; err != nil {
			return err
		}
		step := &model.StorageTaskStep{}
		if err := tx.Take(step, run.StepID).Error; err != nil {
			return err
		}
		task, err := lockStorageTask(tx, step.TaskID)
		if err != nil {
			return err
		}
		taskID = task.ID
		if err := tx.Take(run, runID).Error; err != nil || storageRunTerminal(run.Status) {
			return err
		}
		changed = true
		return tx.Model(run).Updates(map[string]interface{}{"status": model.StorageRunFailed, "message": reason, "updated_at": time.Now()}).Error
	})
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to end storage run %d: %v", runID, err)
		return
	}
	if changed {
		advanceStorageTask(ctx, taskID)
	}
}

// storageStepResults returns the results of the last successful runs of the last step of a name, by host
func storageStepResults(db *gorm.DB, taskID int64, stepName string) (results map[int32]json.RawMessage, err error) {
	results = map[int32]json.RawMessage{}
	step := &model.StorageTaskStep{}
	if err = db.Where("task_id = ? AND name = ?", taskID, stepName).Order("seq DESC").Take(step).Error; err != nil {
		return
	}
	runs, err := currentStepRuns(db, step)
	if err != nil {
		return
	}
	for _, r := range runs {
		if r.Status == model.StorageRunSucceeded && r.Result != "" {
			results[r.Hostid] = json.RawMessage(r.Result)
		}
	}
	return
}
