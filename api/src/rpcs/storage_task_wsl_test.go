/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

// The storage task path end to end in a WSL sandbox: the engine of clapi sends its commands to a fake cland that
// runs them as root in WSL the way cloudlet does (one queue per host, the command evaluated by bash), the callback
// lines go back through the parser and handler of clapi, and the async job output is collected like the heartbeat
// does. The real scripts of scripts/kvm/storage run, so this covers the command format, the job protocol, polling,
// kill and the callback encoding together. Needs PostgreSQL and WSL (as root: jq, rsync, util-linux) with the scripts
// of the checkout installed where a host has them, line ends converted:
//
//	src=/mnt/c/<checkout>/scripts; mkdir -p /opt/cloudland/{scripts,run,log,cache}
//	rsync -a --delete --exclude backend $src/ /opt/cloudland/scripts/; ln -sfn kvm /opt/cloudland/scripts/backend
//	touch /opt/cloudland/scripts/cloudrc.local; sed -i 's/\r$//' /opt/cloudland/scripts/cloudrc
//	find /opt/cloudland/scripts -name '*.sh' -exec sed -i 's/\r$//' {} +; chmod +x /opt/cloudland/scripts/kvm/{,storage/}*.sh
//
//	STORAGE_WSL_E2E=1 CLAPI_TEST_DB_URI="postgres://..." go test ./src/rpcs -run TestStorageTaskWSL -v
//
// Both test hosts are the same WSL instance, so they share host names and the lock of the selftest.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"api/src/cloudlet"
	"api/src/model"
	pb "api/src/proto/cloudlandpb"
	"api/src/services"

	. "api/src/common"

	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"gorm.io/gorm"
)

var wslRunRe = regexp.MustCompile(`storage_task_run '(\d+)'`)

// wslCland stands in for cland and the cloudlets of the test hosts
type wslCland struct {
	pb.UnimplementedClandServiceServer
	t      *testing.T
	db     *gorm.DB
	mu     sync.Mutex
	queues map[int32]chan string
	sent   []string
	// The host the output of an async job that is not a storage task run is taken from (WSL is one host for all)
	asyncHost int32
}

// wsl runs a script as root in WSL with NODE_ID set, giving it stdin, and returns its output. WSL ends the session
// of a command when it exits, with everything it left running in the background; on a host the async jobs outlive
// the command, so the command gets a session of its own
func wsl(hostid int32, script string) (string, error) {
	cmd := exec.Command("wsl.exe", "-u", "root", "--exec", "env", "NODE_ID="+strconv.Itoa(int(hostid)), "setsid", "--wait", "bash", "-c", `eval "$(cat)"`)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (w *wslCland) Execute(_ context.Context, req *pb.ExecuteRequest) (*pb.ExecuteReply, error) {
	hostid, err := strconv.Atoi(strings.TrimPrefix(req.Control, "inter="))
	if err != nil || !strings.HasPrefix(req.Control, "inter=") {
		return &pb.ExecuteReply{Status: "error: no target node"}, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent = append(w.sent, req.Command)
	q := w.queues[int32(hostid)]
	if q == nil {
		// cloudlet runs the commands of a host one after the other
		q = make(chan string, 256)
		w.queues[int32(hostid)] = q
		go func(hostid int32) {
			for command := range q {
				out, err := wsl(hostid, command)
				if err != nil {
					w.t.Logf("host %d: %v: %s", hostid, err, out)
				}
				w.callbacks(hostid, out)
			}
		}(int32(hostid))
	}
	q <- req.Command
	return &pb.ExecuteReply{Status: "ok"}, nil
}

// callbacks hands the callback lines of some output to clapi as reported by hostid; hostid 0 takes the host of the
// run (async job output collected by the heartbeat of whichever host ran it)
func (w *wslCland) callbacks(hostid int32, out string) {
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		command, isCallback := cloudlet.ParseCallbackLine(line)
		if !isCallback {
			if strings.TrimSpace(line) != "" {
				w.t.Logf("non-callback output: %s", line)
			}
			continue
		}
		from := hostid
		if from == 0 {
			m := wslRunRe.FindStringSubmatch(command)
			run := &model.StorageTaskRun{}
			if m == nil || w.db.Take(run, m[1]).Error != nil {
				if w.asyncHost == 0 {
					w.t.Logf("callback of an unknown run: %s", command)
					continue
				}
				from = w.asyncHost
			} else {
				from = run.Hostid
			}
		}
		cmd, args := DecodeCommand(command)
		handler := Get(cmd)
		if handler == nil {
			w.t.Errorf("callback without a handler: %s", command)
			continue
		}
		if _, err := handler(context.WithValue(context.Background(), "hostid", from), args); err != nil {
			w.t.Errorf("callback %s from host %d: %v", truncateLine(command), from, err)
		}
	}
}

func (w *wslCland) commands(script string) (n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.sent {
		if strings.Contains(c, "/"+script+" ") {
			n++
		}
	}
	return
}

func truncateLine(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

// wslHarness is the engine of clapi wired to WSL: the fake cland, the heartbeat that collects the async job output,
// the task worker, and a context of a system admin
type wslHarness struct {
	db   *gorm.DB
	fake *wslCland
	ctx  context.Context
}

func newWSLHarness(t *testing.T) *wslHarness {
	db := consoleTestDB(t)
	if out, err := wsl(0, "test -x /opt/cloudland/scripts/backend/storage/stc_selftest.sh && echo ok"); strings.TrimSpace(out) != "ok" {
		t.Fatalf("the storage scripts are not installed in WSL: %v %s", err, out)
	}
	// Keep WSL up between the commands; start from an empty async job directory
	keep := exec.Command("wsl.exe", "-u", "root", "--exec", "sleep", "7200")
	if err := keep.Start(); err == nil {
		t.Cleanup(func() { keep.Process.Kill() })
	}
	_, _ = wsl(0, "rm -f /opt/cloudland/run/async_job/*.done")
	// Runs left by earlier tests would be polled on the test hosts
	must(t, db.Model(&model.StorageTaskRun{}).Where("status IN ?", []string{model.StorageRunDispatched, model.StorageRunRunning}).
		Updates(map[string]interface{}{"status": model.StorageRunFailed, "message": "left by an earlier test"}).Error)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	fake := &wslCland{t: t, db: db, queues: map[int32]chan string{}}
	server := grpc.NewServer()
	pb.RegisterClandServiceServer(server, fake)
	go server.Serve(lis)
	t.Cleanup(server.Stop)
	viper.Set("cland.endpoint", lis.Addr().String())
	ctx := (&MemberShip{UserID: 1, UserName: "tester", OrgID: 1, OrgRole: model.OrgAdmin, SystemRole: model.SystemAdmin}).SetContext(context.Background())

	// The heartbeat: hand the output of finished async jobs to clapi
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Second):
			}
			out, _ := wsl(0, `cd /opt/cloudland/run/async_job 2>/dev/null || exit 0; for f in *.done; do [ -f "$f" ] || continue; cat "$f"; rm -f "$f"; done`)
			fake.callbacks(0, out)
		}
	}()
	services.StartStorageTaskWorker()
	return &wslHarness{db: db, fake: fake, ctx: ctx}
}

func (w *wslHarness) waitTask(t *testing.T, id int64, timeout time.Duration, statuses ...string) *model.StorageTask {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		task := &model.StorageTask{}
		must(t, w.db.Take(task, id).Error)
		for _, s := range statuses {
			if task.Status == s {
				return task
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %d: still %s (%s) after %s, want %v", id, task.Status, task.Message, timeout, statuses)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (w *wslHarness) taskRuns(t *testing.T, id int64) (runs []*model.StorageTaskRun) {
	must(t, w.db.Where("step_id IN (SELECT id FROM storage_task_steps WHERE task_id = ?)", id).Order("id").Find(&runs).Error)
	return
}

func TestStorageTaskWSL(t *testing.T) {
	if os.Getenv("STORAGE_WSL_E2E") == "" {
		t.Skip("STORAGE_WSL_E2E is not set: this test runs the storage scripts in WSL")
	}
	hw := newWSLHarness(t)
	db, fake, ctx := hw.db, hw.fake, hw.ctx
	h1, h2 := int32(9311), int32(9312)
	for i, h := range []int32{h1, h2} {
		must(t, db.Unscoped().Where("hostid = ?", h).Delete(&model.Hyper{}).Error)
		must(t, db.Create(&model.Hyper{Hostid: h, Hostname: fmt.Sprintf("wsl-h%d", i+1), Status: 1, HostIP: "127.0.0.1"}).Error)
	}
	waitTask, taskRuns := hw.waitTask, hw.taskRuns

	t.Run("steps, failure and retry", func(t *testing.T) {
		task, err := services.StorageClusters.Selftest(ctx, &services.StorageSelftest{Hostids: []int32{h1, h2}, Steps: 2, SleepSec: 2,
			FailHostids: []int32{h2}, FailAttempts: 1, FailStep: 1, Lock: "e2e"})
		must(t, err)
		failed := waitTask(t, task.ID, time.Minute, model.StorageTaskFailed, model.StorageTaskSucceeded)
		if failed.Status != model.StorageTaskFailed || !strings.Contains(failed.Message, "wsl-h2: failed on purpose (selftest)") {
			t.Fatalf("first attempt: %s %q", failed.Status, failed.Message)
		}
		must(t, services.RetryStorageTask(ctx, task.ID))
		done := waitTask(t, task.ID, 2*time.Minute, model.StorageTaskSucceeded, model.StorageTaskFailed)
		if done.Status != model.StorageTaskSucceeded {
			t.Fatalf("after retry: %s %q", done.Status, done.Message)
		}
		runs := taskRuns(t, task.ID)
		if len(runs) != 5 {
			t.Fatalf("want 2 + 1 retry + 2 runs, got %d", len(runs))
		}
		for _, r := range runs {
			if r.Status == model.StorageRunSucceeded && (!strings.Contains(r.Result, `"boot_id"`) || !strings.Contains(r.LogTail, "=== run")) {
				t.Fatalf("run %d: result %q log %q", r.ID, r.Result, r.LogTail)
			}
			if r.Status == model.StorageRunFailed && !strings.Contains(r.LogTail, "failed: failed on purpose") {
				t.Fatalf("failed run %d has no log: %q", r.ID, r.LogTail)
			}
		}
	})

	t.Run("polling a long run", func(t *testing.T) {
		task, err := services.StorageClusters.Selftest(ctx, &services.StorageSelftest{Hostids: []int32{h1}, Steps: 1, SleepSec: 45, Lock: "poll"})
		must(t, err)
		deadline := time.Now().Add(44 * time.Second)
		for {
			runs := taskRuns(t, task.ID)
			if len(runs) == 1 && runs[0].Status == model.StorageRunRunning && runs[0].Progress > 0 && strings.Contains(runs[0].Message, "slept") {
				t.Logf("poll answered: %d%% %s", runs[0].Progress, runs[0].Message)
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no poll answer while the run was going: %+v", runs)
			}
			time.Sleep(time.Second)
		}
		if fake.commands("stc_poll.sh") == 0 {
			t.Fatal("no poll was sent")
		}
		if done := waitTask(t, task.ID, time.Minute, model.StorageTaskSucceeded, model.StorageTaskFailed); done.Status != model.StorageTaskSucceeded {
			t.Fatalf("long run: %s %q", done.Status, done.Message)
		}
	})

	t.Run("abort kills the job", func(t *testing.T) {
		task, err := services.StorageClusters.Selftest(ctx, &services.StorageSelftest{Hostids: []int32{h1}, Steps: 1, SleepSec: 600, Lock: "abort"})
		must(t, err)
		run := taskRuns(t, task.ID)[0]
		alive := fmt.Sprintf(`pgrep -f -- "--stc-job %d( |$)" >/dev/null && echo alive || echo gone`, run.ID)
		deadline := time.Now().Add(20 * time.Second)
		for {
			if out, _ := wsl(0, alive); strings.TrimSpace(out) == "alive" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the job did not start")
			}
			time.Sleep(500 * time.Millisecond)
		}
		must(t, services.AbortStorageTask(ctx, task.ID))
		done := waitTask(t, task.ID, 30*time.Second, model.StorageTaskAborted)
		if out, _ := wsl(0, alive); strings.TrimSpace(out) != "gone" {
			t.Fatalf("the job of run %d is still running after the abort", run.ID)
		}
		if r := taskRuns(t, task.ID)[0]; r.Status != model.StorageRunFailed || !strings.Contains(r.Message, "aborted") {
			t.Fatalf("run after abort: %+v (task %q)", r, done.Message)
		}
	})

	t.Run("precheck", func(t *testing.T) {
		out, err := wsl(0, `set -e; mkdir -p /root/stc-disks; cd /root/stc-disks; [ -f e2e.img ] || truncate -s 1G e2e.img
d=$(losetup -j $PWD/e2e.img -nO NAME | head -1); [ -n "$d" ] || d=$(losetup -f --show $PWD/e2e.img); wipefs -a -q $d
echo "$(lsblk -dbno SIZE $d)"`)
		size, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
		if err != nil || perr != nil {
			t.Fatalf("loop disk: %v %s", err, out)
		}
		diskID := "loop:/root/stc-disks/e2e.img"
		must(t, db.Unscoped().Where("hostid IN ?", []int32{h1, h2}).Delete(&model.HyperDisk{}).Error)
		must(t, db.Create(&model.HyperDisk{Hostid: h1, DiskID: diskID, Name: "loop", SizeBytes: size, State: model.DiskFree, ScannedAt: time.Now()}).Error)
		// GPFS is installed in the sandbox (the GPFS spike), which makes every blank disk a possible NSD: hide it
		_, _ = wsl(0, "[ -x /usr/lpp/mmfs/bin/mmfsd ] && chmod 000 /usr/lpp/mmfs/bin/mmfsd && touch /tmp/stc-e2e-gpfs-hidden; true")
		defer wsl(0, "[ -f /tmp/stc-e2e-gpfs-hidden ] && chmod 500 /usr/lpp/mmfs/bin/mmfsd && rm -f /tmp/stc-e2e-gpfs-hidden; true")

		task, err := services.StorageClusters.Precheck(ctx, &services.StoragePlan{Kind: model.StorageKindCeph,
			Params: json.RawMessage(`{"test":true}`),
			Nodes:  []*services.StorageNodePlan{{Hostid: h1, Roles: []string{"admin", "mon", "mgr", "osd"}}, {Hostid: h2, Roles: []string{"client"}}},
			Disks:  []*services.StorageDiskPlan{{Hostid: h1, DiskID: diskID}}})
		must(t, err)
		done := waitTask(t, task.ID, 3*time.Minute, model.StorageTaskSucceeded, model.StorageTaskFailed)
		runs := taskRuns(t, task.ID)
		for _, r := range runs {
			t.Logf("host %d %s: %s", r.Hostid, r.Status, r.Result)
			if r.Status != model.StorageRunSucceeded {
				t.Fatalf("precheck on host %d: %s", r.Hostid, r.Message)
			}
		}
		if !strings.Contains(runs[0].Result, `"disk `+diskID+`","status":"ok"`) {
			t.Fatalf("the disk was not checked: %s", runs[0].Result)
		}
		// Both hosts are this WSL instance, so the check across hosts finds the same host name twice
		if done.Status != model.StorageTaskFailed || !strings.Contains(done.Message, "the same host name") {
			t.Fatalf("precheck task: %s %q", done.Status, done.Message)
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
