/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The storage task engine against PostgreSQL (CLAPI_TEST_DB_URI, see placement_pg_test.go) with the fake cland of
// p2_fix_test.go standing in for the hosts: the test reads the commands clapi sends and answers them the way
// stc_lib.sh does. Covered: steps in order, idempotent reports, failure and retry (also from an earlier step and on
// another admin host), lost commands sent again, commands that can not be sent, abort, timeouts, offline hosts,
// polling, the slots of a cluster, the leader lock and the precheck plan and input.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"

	"gorm.io/gorm"
)

var stcSentRe = regexp.MustCompile(`(?s)^inter=(-?\d+) \| /opt/cloudland/scripts/backend/storage/(\w+\.sh) '(\d+)'(?: <<'EOF'\n(.*)\nEOF)?$`)

type stcSent struct {
	hostid int32
	script string
	run    int64
	input  map[string]interface{}
}

type stcFixture struct {
	t     *testing.T
	ctx   context.Context
	db    *gorm.DB
	cland *fakeCland
	runs  map[int64]bool // runs of this test: the worker loop also sees what earlier test runs left behind
}

var stcHosts = []int32{9301, 9302, 9303, 9304}

func newStcFixture(t *testing.T) *stcFixture {
	ctx, db := pgContext(t, 1)
	f := &stcFixture{t: t, ctx: ctx, db: db, cland: startFakeCland(t), runs: map[int64]bool{}}
	must(t, db.Unscoped().Where("hostid IN ?", stcHosts).Delete(&model.Hyper{}).Error)
	must(t, db.Unscoped().Where("hostid IN ?", stcHosts).Delete(&model.HyperDisk{}).Error)
	must(t, db.Unscoped().Where("hostid IN ?", stcHosts).Delete(&model.StorageClusterDisk{}).Error)
	must(t, db.Unscoped().Where("hostid IN ?", stcHosts).Delete(&model.StorageClusterNode{}).Error)
	for i, h := range stcHosts {
		must(t, db.Create(&model.Hyper{Hostid: h, Hostname: fmt.Sprintf("stc-h%d", i+1), Status: 1, HostIP: fmt.Sprintf("10.93.0.%d", i+1)}).Error)
	}
	registerStcTestKinds()
	return f
}

func (f *stcFixture) setHostStatus(hostid int32, status int) {
	must(f.t, f.db.Model(&model.Hyper{}).Where("hostid = ?", hostid).Update("status", status).Error)
}

// sent returns the storage commands sent since the last call, only those of this test's runs once known
func (f *stcFixture) sent() (out []*stcSent) {
	f.t.Helper()
	for _, rec := range f.cland.take() {
		m := stcSentRe.FindStringSubmatch(rec)
		if m == nil {
			continue
		}
		hostid, _ := strconv.Atoi(m[1])
		run, _ := strconv.ParseInt(m[3], 10, 64)
		s := &stcSent{hostid: int32(hostid), script: m[2], run: run}
		if m[4] != "" {
			if err := json.Unmarshal([]byte(m[4]), &s.input); err != nil {
				f.t.Fatalf("bad input of run %d: %v", run, err)
			}
		}
		if s.script == "stc_poll.sh" && !f.runs[run] {
			continue
		}
		out = append(out, s)
	}
	return
}

// expect checks the commands sent since the last call: script and host of each, in any order
func (f *stcFixture) expect(what, script string, hostids ...int32) []*stcSent {
	f.t.Helper()
	sent := f.sent()
	got := map[int32]int{}
	for _, s := range sent {
		if s.script != script {
			f.t.Fatalf("%s: unexpected command %s to host %d", what, s.script, s.hostid)
		}
		got[s.hostid]++
		f.runs[s.run] = true
	}
	if len(sent) != len(hostids) {
		f.t.Fatalf("%s: want %s to %v, got %d commands %v", what, script, hostids, len(sent), got)
	}
	for _, h := range hostids {
		if got[h] != 1 {
			f.t.Fatalf("%s: want one %s to host %d, got %v", what, script, h, got)
		}
	}
	return sent
}

func (f *stcFixture) task(id int64) *model.StorageTask {
	f.t.Helper()
	task := &model.StorageTask{}
	must(f.t, f.db.Take(task, id).Error)
	return task
}

func (f *stcFixture) run(id int64) *model.StorageTaskRun {
	f.t.Helper()
	run := &model.StorageTaskRun{}
	must(f.t, f.db.Take(run, id).Error)
	return run
}

func (f *stcFixture) taskOfRun(runID int64) int64 {
	f.t.Helper()
	step := &model.StorageTaskStep{}
	must(f.t, f.db.Take(step, f.run(runID).StepID).Error)
	return step.TaskID
}

func (f *stcFixture) wantTask(id int64, status, msgPart string) *model.StorageTask {
	f.t.Helper()
	task := f.task(id)
	if task.Status != status || !strings.Contains(task.Message, msgPart) {
		f.t.Fatalf("task %d: want %s with %q, got %s with %q", id, status, msgPart, task.Status, task.Message)
	}
	return task
}

// report answers a run the way stc_lib.sh does
func (f *stcFixture) report(s *stcSent, status, message, result string) error {
	var payload *StorageRunPayload
	if status != storageRunMissing {
		payload = &StorageRunPayload{Message: message, Log: "log of run " + strconv.FormatInt(s.run, 10)}
		if result != "" {
			payload.Result = json.RawMessage(result)
		}
	}
	progress := int32(0)
	if status == model.StorageRunSucceeded {
		progress = 100
	}
	return HandleStorageTaskRun(f.ctx, s.hostid, s.run, status, progress, payload)
}

func (f *stcFixture) succeed(s *stcSent) {
	f.t.Helper()
	must(f.t, f.report(s, model.StorageRunSucceeded, "", fmt.Sprintf(`{"host":"h%d","step":%v}`, s.hostid, s.input["step"])))
}

func (f *stcFixture) backdate(runID int64, column string, ago time.Duration) {
	must(f.t, f.db.Model(&model.StorageTaskRun{}).Where("id = ?", runID).UpdateColumn(column, time.Now().Add(-ago)).Error)
}

func (f *stcFixture) cluster() *model.StorageCluster {
	c := &model.StorageCluster{Name: fmt.Sprintf("stc-pg-%d", time.Now().UnixNano()), Kind: model.StorageKindCeph, Mode: "managed", Status: "creating"}
	must(f.t, f.db.Create(c).Error)
	return c
}

var stcFinished = map[int64]bool{}

// stcFinishFailed: the tasks whose finish failed once (finish_fail_once fails the first time only)
var stcFinishFailed = map[int64]bool{}

func stcTestStep(retryFrom string) *storageStepDef {
	return &storageStepDef{Script: "stc_selftest.sh", RetryFrom: retryFrom,
		Input: func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
			return map[string]interface{}{"step": step.Seq}, nil
		}}
}

func registerStcTestKinds() {
	finish := func(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
		if succeeded && strings.Contains(task.Params, "finish_fail") {
			// Written before it fails: the savepoint the finish runs in must take it back
			if err := tx.Model(&model.StorageCluster{}).Where("id = ?", task.ClusterID).Update("status", "finish-partial").Error; err != nil {
				return err
			}
			if !strings.Contains(task.Params, "finish_fail_once") || !stcFinishFailed[task.ID] {
				stcFinishFailed[task.ID] = true
				return fmt.Errorf("finish refused")
			}
		}
		stcFinished[task.ID] = succeeded
		return nil
	}
	registerStorageTaskKind("pgslot", &storageTaskKind{Slot: storageSlotStructural, Finish: finish,
		Steps: map[string]*storageStepDef{"resolve": stcTestStep(""), "write": stcTestStep("resolve")}})
	registerStorageTaskKind("pgpool", &storageTaskKind{Slot: storageSlotPool, Finish: finish,
		Steps: map[string]*storageStepDef{"resolve": stcTestStep("")}})
	registerStorageTaskKind("pgadmin", &storageTaskKind{Steps: map[string]*storageStepDef{"admin": stcTestStep("")}})
	registerStorageTaskKind("pgbig", &storageTaskKind{Steps: map[string]*storageStepDef{"big": {Script: "stc_selftest.sh",
		Input: func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
			return map[string]string{"data": strings.Repeat("x", storageInputMax)}, nil
		}}}})
}

func TestStorageTaskPG(t *testing.T) {
	f := newStcFixture(t)
	ctx := f.ctx
	h1, h2, h3, h4 := stcHosts[0], stcHosts[1], stcHosts[2], stcHosts[3]

	t.Run("steps in order", func(t *testing.T) {
		f.t = t
		task, err := StorageClusters.Selftest(ctx, &StorageSelftest{Hostids: []int32{h1, h2}, Steps: 2, SleepSec: 1})
		must(t, err)
		step1 := f.expect("step 1", "stc_selftest.sh", h1, h2)
		if step1[0].input["step"] != float64(1) || step1[0].input["fail"] != false {
			t.Fatalf("step 1 input %v", step1[0].input)
		}
		must(t, f.report(step1[0], model.StorageRunRunning, "slept 1", ""))
		if r := f.run(step1[0].run); r.Status != model.StorageRunRunning || r.LogTail == "" {
			t.Fatalf("running report not recorded: %+v", r)
		}
		f.succeed(step1[0])
		f.expect("one run of step 1 left", "")
		// The heartbeat and a poll both bring the end; a late report changes nothing
		f.succeed(step1[0])
		must(t, f.report(step1[0], model.StorageRunFailed, "late", ""))
		if r := f.run(step1[0].run); r.Status != model.StorageRunSucceeded || r.Progress != 100 {
			t.Fatalf("a report after the end changed run %d: %+v", r.ID, r)
		}
		if err := f.report(&stcSent{hostid: h3, run: step1[1].run}, model.StorageRunSucceeded, "", "{}"); err == nil {
			t.Fatal("a report from another host was taken")
		}
		f.succeed(step1[1])
		step2 := f.expect("step 2", "stc_selftest.sh", h1, h2)
		if step2[0].input["step"] != float64(2) {
			t.Fatalf("step 2 input %v", step2[0].input)
		}
		if f.task(task.ID).CurrentStep != 2 {
			t.Fatal("current step not moved on")
		}
		f.succeed(step2[0])
		f.succeed(step2[1])
		if done := f.wantTask(task.ID, model.StorageTaskSucceeded, ""); done.FinishedAt == nil {
			t.Fatal("finished_at not set")
		}
		view, err := StorageClusters.GetTask(ctx, task.UUID)
		must(t, err)
		if len(view.Steps) != 2 || len(view.Steps[1].Runs) != 2 || view.Steps[0].Step.Status != model.StorageStepSucceeded {
			t.Fatalf("task view %+v", view)
		}
	})

	t.Run("failure and retry", func(t *testing.T) {
		f.t = t
		task, err := StorageClusters.Selftest(ctx, &StorageSelftest{Hostids: []int32{h1, h2}, Steps: 1, FailHostids: []int32{h2}, FailAttempts: 1})
		must(t, err)
		sent := f.expect("first attempt", "stc_selftest.sh", h1, h2)
		for _, s := range sent {
			if want := s.hostid == h2; s.input["fail"] != want {
				t.Fatalf("host %d: fail=%v", s.hostid, s.input["fail"])
			}
		}
		f.succeed(sent[0])
		f.expect("waits for the other run", "")
		failed := sent[1]
		if failed.hostid != h2 {
			failed = sent[0]
		}
		must(t, f.report(failed, model.StorageRunFailed, "boom", ""))
		f.wantTask(task.ID, model.StorageTaskFailed, "stc-h2: boom")
		wantCode(t, RetryStorageTask(member(1, model.OrgAdmin).SetContext(context.Background()), task.ID), ErrPermissionDenied, "retry by a non-admin")
		must(t, RetryStorageTask(ctx, task.ID))
		retry := f.expect("retry only where it failed", "stc_selftest.sh", h2)
		if r := f.run(retry[0].run); r.Attempt != 2 || retry[0].input["fail"] != false {
			t.Fatalf("retry run %+v input %v", r, retry[0].input)
		}
		f.succeed(retry[0])
		f.wantTask(task.ID, model.StorageTaskSucceeded, "")
		wantCode(t, RetryStorageTask(ctx, task.ID), ErrStorageTaskState, "retry of a task that succeeded")
	})

	t.Run("lost command sent again", func(t *testing.T) {
		f.t = t
		task, err := StorageClusters.Selftest(ctx, &StorageSelftest{Hostids: []int32{h1}, Steps: 1})
		must(t, err)
		first := f.expect("dispatch", "stc_selftest.sh", h1)[0]
		must(t, f.report(first, storageRunMissing, "", ""))
		again := f.expect("sent again", "stc_selftest.sh", h1)[0]
		if again.run != first.run {
			t.Fatalf("a lost command must be sent again for the same run, got run %d for %d", again.run, first.run)
		}
		if r := f.run(first.run); r.Dispatches != 2 || r.Status != model.StorageRunDispatched {
			t.Fatalf("run after one resend %+v", r)
		}
		must(t, f.db.Model(&model.StorageTaskRun{}).Where("id = ?", first.run).Update("dispatches", storageRunMaxDispatches).Error)
		must(t, f.report(first, storageRunMissing, "", ""))
		f.expect("no more resends", "")
		f.wantTask(task.ID, model.StorageTaskFailed, "never reached the host")
	})

	t.Run("command that can not be sent", func(t *testing.T) {
		f.t = t
		f.cland.setFailing(true)
		task, err := StorageClusters.Selftest(ctx, &StorageSelftest{Hostids: []int32{h1, h2}, Steps: 1})
		f.cland.setFailing(false)
		must(t, err)
		f.wantTask(task.ID, model.StorageTaskFailed, "failed to send the command")
	})

	t.Run("input too big", func(t *testing.T) {
		f.t = t
		task, err := createStorageTask(ctx, 0, "pgbig", nil, []*StorageStepPlan{{Name: "big", Scope: model.StorageStepScopeNodes, Hostids: []int32{h1}}})
		must(t, err)
		f.expect("nothing sent", "")
		f.wantTask(task.ID, model.StorageTaskFailed, "more than a node command can carry")
	})

	t.Run("abort", func(t *testing.T) {
		f.t = t
		task, err := StorageClusters.Selftest(ctx, &StorageSelftest{Hostids: []int32{h1, h2}, Steps: 1, SleepSec: 600})
		must(t, err)
		sent := f.expect("dispatch", "stc_selftest.sh", h1, h2)
		must(t, AbortStorageTask(ctx, task.ID))
		f.expect("kills", "stc_kill.sh", h1, h2)
		f.wantTask(task.ID, model.StorageTaskAborting, "aborted by tester")
		must(t, f.report(sent[0], model.StorageRunFailed, "stopped because the task was aborted", ""))
		f.wantTask(task.ID, model.StorageTaskAborting, "")
		must(t, f.report(sent[1], model.StorageRunFailed, "stopped because the task was aborted", ""))
		if done := f.wantTask(task.ID, model.StorageTaskAborted, ""); done.FinishedAt == nil {
			t.Fatal("finished_at not set")
		}
		wantCode(t, AbortStorageTask(ctx, task.ID), ErrStorageTaskState, "abort of an aborted task")

		// A failed task has nothing running: it ends at once
		task, err = StorageClusters.Selftest(ctx, &StorageSelftest{Hostids: []int32{h1}, Steps: 1, FailHostids: []int32{h1}, FailAttempts: 1})
		must(t, err)
		s := f.expect("dispatch", "stc_selftest.sh", h1)[0]
		must(t, f.report(s, model.StorageRunFailed, "boom", ""))
		must(t, AbortStorageTask(ctx, task.ID))
		f.expect("no kill", "")
		f.wantTask(task.ID, model.StorageTaskAborted, "")
	})

	t.Run("worker: timeout, offline, polling", func(t *testing.T) {
		f.t = t
		timed, err := StorageClusters.Selftest(ctx, &StorageSelftest{Hostids: []int32{h1}, Steps: 1, SleepSec: 600, Timeout: 1})
		must(t, err)
		ts := f.expect("dispatch", "stc_selftest.sh", h1)[0]
		f.backdate(ts.run, "started_at", 10*time.Second)

		f.setHostStatus(h4, 10)
		offline, err := StorageClusters.Selftest(ctx, &StorageSelftest{Hostids: []int32{h4}, Steps: 1, SleepSec: 600})
		must(t, err)
		off := f.expect("dispatch", "stc_selftest.sh", h4)[0]
		f.backdate(off.run, "updated_at", storageRunOfflineAfter+time.Minute)

		polled, err := StorageClusters.Selftest(ctx, &StorageSelftest{Hostids: []int32{h2}, Steps: 1, SleepSec: 600})
		must(t, err)
		ps := f.expect("dispatch", "stc_selftest.sh", h2)[0]
		f.backdate(ps.run, "updated_at", storagePollEvery+5*time.Second)

		workStorageRuns(ctx)
		f.wantTask(timed.ID, model.StorageTaskFailed, "timed out after 1s")
		f.wantTask(offline.ID, model.StorageTaskFailed, "the host is offline")
		f.expect("poll", "stc_poll.sh", h2)
		workStorageRuns(ctx)
		f.expect("one poll out at a time", "")
		// The host answered the poll: the next one is due once the answer is old
		must(t, f.report(ps, model.StorageRunRunning, "slept 3", ""))
		f.backdate(ps.run, "polled_at", 40*time.Second)
		f.backdate(ps.run, "updated_at", 20*time.Second)
		workStorageRuns(ctx)
		f.expect("poll after the answer", "stc_poll.sh", h2)
		// No answer for long: asked again
		f.backdate(ps.run, "polled_at", storagePollOutstanding+time.Second)
		workStorageRuns(ctx)
		f.expect("poll again after no answer", "stc_poll.sh", h2)
		f.setHostStatus(h4, 1)
		must(t, AbortStorageTask(ctx, polled.ID))
		f.expect("kill", "stc_kill.sh", h2)
		must(t, f.report(ps, model.StorageRunFailed, "stopped", ""))
	})

	t.Run("admin step retried on another host", func(t *testing.T) {
		f.t = t
		f.setHostStatus(h4, 10)
		task, err := createStorageTask(ctx, 0, "pgadmin", nil, []*StorageStepPlan{{Name: "admin", Scope: model.StorageStepScopeAdmin, Hostids: []int32{h4, h1, h2}}})
		must(t, err)
		s := f.expect("first online admin host", "stc_selftest.sh", h1)[0]
		must(t, f.report(s, model.StorageRunFailed, "admin host broke", ""))
		f.wantTask(task.ID, model.StorageTaskFailed, "admin host broke")
		f.setHostStatus(h1, 10)
		must(t, RetryStorageTask(ctx, task.ID))
		s = f.expect("next admin host", "stc_selftest.sh", h2)[0]
		f.succeed(s)
		// The run that failed on the first admin host no longer counts
		f.wantTask(task.ID, model.StorageTaskSucceeded, "")
		f.setHostStatus(h1, 1)
		f.setHostStatus(h4, 1)
	})

	t.Run("slots and retry from an earlier step", func(t *testing.T) {
		f.t = t
		c := f.cluster()
		plan := []*StorageStepPlan{{Name: "resolve", Scope: model.StorageStepScopeNodes, Hostids: []int32{h1, h2}},
			{Name: "write", Scope: model.StorageStepScopeNodes, Hostids: []int32{h1, h2}}}
		task, err := createStorageTask(ctx, c.ID, "pgslot", map[string]string{"x": "y"}, plan)
		must(t, err)
		if got := (&model.StorageCluster{}); f.db.Take(got, c.ID).Error != nil || got.ActiveTask != task.ID {
			t.Fatalf("structural slot not taken: %+v", got)
		}
		_, err = createStorageTask(ctx, c.ID, "pgslot", nil, plan)
		wantCode(t, err, ErrStorageClusterBusy, "second structural task")
		_, err = createStorageTask(ctx, 0, "pgslot", nil, plan)
		wantCode(t, err, ErrInvalidParameter, "slot task without a cluster")
		pool, err := createStorageTask(ctx, c.ID, "pgpool", nil, plan[:1])
		must(t, err)
		// Both tasks started their first step on both hosts
		resolve := []*stcSent{}
		for _, s := range f.sent() {
			f.runs[s.run] = true
			if f.taskOfRun(s.run) == task.ID {
				resolve = append(resolve, s)
			} else {
				f.succeed(s)
			}
		}
		f.wantTask(pool.ID, model.StorageTaskSucceeded, "")
		if !stcFinished[pool.ID] {
			t.Fatal("finish of the pool task not called")
		}
		if len(resolve) != 2 {
			t.Fatalf("resolve runs %d", len(resolve))
		}
		f.succeed(resolve[0])
		f.succeed(resolve[1])
		write := f.expect("write", "stc_selftest.sh", h1, h2)
		f.succeed(write[0])
		must(t, f.report(write[1], model.StorageRunFailed, "disk changed", ""))
		f.wantTask(task.ID, model.StorageTaskFailed, "disk changed")
		if got := (&model.StorageCluster{}); f.db.Take(got, c.ID).Error != nil || got.ActiveTask != task.ID {
			t.Fatal("a failed task must keep its slot")
		}
		// write resolves device names right before it writes: a retry starts at resolve, on all hosts
		must(t, RetryStorageTask(ctx, task.ID))
		resolve = f.expect("resolve again", "stc_selftest.sh", h1, h2)
		for _, s := range resolve {
			if s.input["step"] != float64(1) || f.run(s.run).Attempt != 2 {
				t.Fatalf("retry did not start at resolve: %v attempt %d", s.input, f.run(s.run).Attempt)
			}
			f.succeed(s)
		}
		write = f.expect("write again", "stc_selftest.sh", h1, h2)
		f.succeed(write[0])
		f.succeed(write[1])
		f.wantTask(task.ID, model.StorageTaskSucceeded, "")
		if got := (&model.StorageCluster{}); f.db.Take(got, c.ID).Error != nil || got.ActiveTask != 0 || !stcFinished[task.ID] {
			t.Fatalf("slot not released or finish not called: %+v", got)
		}

		// A finish that fails keeps the task failed and the slot taken; abort releases it
		task, err = createStorageTask(ctx, c.ID, "pgslot", map[string]string{"x": "finish_fail"}, plan[:1])
		must(t, err)
		for _, s := range f.expect("resolve", "stc_selftest.sh", h1, h2) {
			f.succeed(s)
		}
		f.wantTask(task.ID, model.StorageTaskFailed, "finishing: finish refused")
		if got := (&model.StorageCluster{}); f.db.Take(got, c.ID).Error != nil || got.Status == "finish-partial" {
			t.Fatalf("what the failed finish wrote stayed: %+v", got)
		}
		must(t, AbortStorageTask(ctx, task.ID))
		f.wantTask(task.ID, model.StorageTaskAborted, "")
		if got := (&model.StorageCluster{}); f.db.Take(got, c.ID).Error != nil || got.ActiveTask != 0 {
			t.Fatal("abort did not release the slot")
		}
		if succeeded, called := stcFinished[task.ID]; !called || succeeded {
			t.Fatal("finish of an aborted task not called with succeeded=false")
		}

		// Every step done and the finish failed once: a retry runs the finish again, and no step
		task, err = createStorageTask(ctx, c.ID, "pgslot", map[string]string{"x": "finish_fail_once"}, plan[:1])
		must(t, err)
		for _, s := range f.expect("resolve", "stc_selftest.sh", h1, h2) {
			f.succeed(s)
		}
		f.wantTask(task.ID, model.StorageTaskFailed, "finishing: finish refused")
		must(t, RetryStorageTask(ctx, task.ID))
		f.expect("a retried finish sends nothing", "")
		f.wantTask(task.ID, model.StorageTaskSucceeded, "")
		if got := (&model.StorageCluster{}); f.db.Take(got, c.ID).Error != nil || got.ActiveTask != 0 || !stcFinished[task.ID] {
			t.Fatalf("the retried finish did not end the task: %+v", got)
		}

		// A step this version does not know (renamed by an upgrade while the task waited): the task fails, no panic
		task, err = createStorageTask(ctx, c.ID, "pgslot", map[string]string{"x": "y"}, plan)
		must(t, err)
		must(t, f.db.Model(&model.StorageTaskStep{}).Where("task_id = ? AND name = ?", task.ID, "write").Update("name", "gone").Error)
		for _, s := range f.expect("resolve", "stc_selftest.sh", h1, h2) {
			f.succeed(s)
		}
		f.wantTask(task.ID, model.StorageTaskFailed, "gone: the step is unknown to this version of clapi")
		wantCode(t, RetryStorageTask(ctx, task.ID), ErrStorageTaskState, "retry of an unknown step")
		must(t, AbortStorageTask(ctx, task.ID))
		f.wantTask(task.ID, model.StorageTaskAborted, "")
	})

	t.Run("leader lock", func(t *testing.T) {
		f.t = t
		sqlDB, err := f.db.DB()
		must(t, err)
		conn, err := sqlDB.Conn(ctx)
		must(t, err)
		defer conn.Close()
		_, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", storageLeaderKey)
		must(t, err)
		called := false
		storageLeader(ctx, func(context.Context) { called = true })
		if called {
			t.Fatal("ran without the leader lock")
		}
		_, err = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", storageLeaderKey)
		must(t, err)
		storageLeader(ctx, func(context.Context) { called = true })
		if !called {
			t.Fatal("did not run with the leader lock free")
		}
	})
}

func TestStoragePrecheckPG(t *testing.T) {
	f := newStcFixture(t)
	ctx := f.ctx
	h1, h2 := stcHosts[0], stcHosts[1]
	disk := func(hostid int32, id, state string, scanned time.Time) {
		must(t, f.db.Create(&model.HyperDisk{Hostid: hostid, DiskID: id, Name: "sdb", Serial: "SER-" + id, SizeBytes: 4 << 40,
			State: state, ScannedAt: scanned}).Error)
	}
	disk(h1, "wwn-0x5000aaaa", model.DiskFree, time.Now())
	disk(h1, "wwn-0x5000bbbb", model.DiskDirty, time.Now())
	disk(h1, "wwn-0x5000cccc", model.DiskFree, time.Now().Add(-48*time.Hour))
	disk(h1, "wwn-0x5000dddd", model.DiskFree, time.Now())
	c := f.cluster()
	must(t, f.db.Create(&model.StorageClusterDisk{ClusterID: c.ID, Hostid: h1, DiskID: "wwn-0x5000dddd", Role: model.StorageDiskRoleOSD, Status: model.StorageDiskActive}).Error)

	plan := func(disks ...*StorageDiskPlan) *StoragePlan {
		return &StoragePlan{Kind: model.StorageKindCeph, Params: json.RawMessage(`{"test":true}`), Disks: disks,
			Nodes: []*StorageNodePlan{{Hostid: h1, Roles: []string{"admin", "mon", "mgr", "osd"}}, {Hostid: h2, Roles: []string{"client"}}}}
	}
	_, err := StorageClusters.Precheck(member(1, model.OrgAdmin).SetContext(context.Background()), plan())
	wantCode(t, err, ErrPermissionDenied, "precheck by a non-admin")
	_, err = StorageClusters.Precheck(ctx, plan())
	wantCode(t, err, ErrStorageInvalidPlan, "OSD host without a disk")
	_, err = StorageClusters.Precheck(ctx, plan(&StorageDiskPlan{Hostid: h1, DiskID: "wwn-0x5000bbbb"}))
	wantCode(t, err, ErrStorageDiskNotAllowed, "dirty disk without wipe")
	_, err = StorageClusters.Precheck(ctx, plan(&StorageDiskPlan{Hostid: h1, DiskID: "wwn-0x5000cccc"}))
	wantCode(t, err, ErrStorageDiskNotAllowed, "old scan")
	_, err = StorageClusters.Precheck(ctx, plan(&StorageDiskPlan{Hostid: h1, DiskID: "wwn-0x5000dddd"}))
	wantCode(t, err, ErrStorageDiskNotAllowed, "claimed disk")
	_, err = StorageClusters.Precheck(ctx, plan(&StorageDiskPlan{Hostid: h2, DiskID: "wwn-0x5000aaaa"}))
	wantCode(t, err, ErrStorageInvalidPlan, "disk on a client host")
	bad := plan(&StorageDiskPlan{Hostid: h1, DiskID: "wwn-0x5000aaaa"})
	bad.Nodes[1].Roles = []string{"mon"}
	_, err = StorageClusters.Precheck(ctx, bad)
	wantCode(t, err, ErrStorageInvalidPlan, "two mons")
	f.setHostStatus(h2, 10)
	_, err = StorageClusters.Precheck(ctx, plan(&StorageDiskPlan{Hostid: h1, DiskID: "wwn-0x5000aaaa"}))
	wantCode(t, err, ErrStorageInvalidPlan, "offline host")
	f.setHostStatus(h2, 1)
	f.expect("nothing sent for refused plans", "")

	good := plan(&StorageDiskPlan{Hostid: h1, DiskID: "wwn-0x5000aaaa"}, &StorageDiskPlan{Hostid: h1, DiskID: "wwn-0x5000bbbb", Wipe: true})
	task, err := StorageClusters.Precheck(ctx, good)
	must(t, err)
	sent := f.expect("precheck", "stc_precheck.sh", h1, h2)
	var on1, on2 *stcSent
	for _, s := range sent {
		if s.hostid == h1 {
			on1 = s
		} else {
			on2 = s
		}
	}
	in := on1.input
	disks, _ := in["disks"].([]interface{})
	peers, _ := in["peers"].([]interface{})
	dirs, _ := in["data_dirs"].([]interface{})
	if in["kind"] != "ceph" || len(disks) != 2 || len(peers) != 1 || peers[0] != "10.93.0.2" || in["need_container"] != true ||
		in["need_headers"] != false || len(dirs) != 1 || in["reserve_mb"].(float64) <= 0 {
		t.Fatalf("precheck input of the mon host: %v", in)
	}
	d0 := disks[0].(map[string]interface{})
	if d0["id"] != "wwn-0x5000aaaa" || d0["wwn"] != "0x5000aaaa" || d0["serial"] != "SER-wwn-0x5000aaaa" || d0["size_bytes"].(float64) != 4<<40 {
		t.Fatalf("disk identity %v", d0)
	}
	if d1 := disks[1].(map[string]interface{}); d1["wipe"] != true {
		t.Fatalf("wipe not passed: %v", d1)
	}
	if d2, _ := on2.input["data_dirs"].([]interface{}); len(d2) != 0 || len(on2.input["disks"].([]interface{})) != 0 {
		t.Fatalf("client host input: %v", on2.input)
	}
	result := func(name string) string {
		return fmt.Sprintf(`{"items":[{"name":"os","status":"ok","detail":"ubuntu 26.04"}],"facts":{"hostname":%q}}`, name)
	}
	must(t, f.report(on1, model.StorageRunSucceeded, "", result("node-a")))
	must(t, f.report(on2, model.StorageRunSucceeded, "", result("node-a")))
	f.wantTask(task.ID, model.StorageTaskFailed, "the same host name node-a")

	task, err = StorageClusters.Precheck(ctx, good)
	must(t, err)
	for _, s := range f.expect("precheck again", "stc_precheck.sh", h1, h2) {
		must(t, f.report(s, model.StorageRunSucceeded, "", result(fmt.Sprintf("node-%d", s.hostid))))
	}
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	total, tasks, err := StorageClusters.ListTasks(ctx, 0, model.StorageTaskSucceeded, 0, 5)
	must(t, err)
	if total < 1 || tasks[0].ID != task.ID || tasks[0].Kind != "precheck" {
		t.Fatalf("task list %d %+v", total, tasks)
	}
}
