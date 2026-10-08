/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Recovering the instances of a host that is down and the reconcile of the host when it comes back, against
// PostgreSQL with the fake cland (shared-storage-design.md §11): the grace period, instances with local disks left
// alone, the fence first (on another admin host), the launch with the existing disks once fenced, the launch report,
// the copy on the host removed by its reconcile and the fence lifted after that; fences that can not run, confirmed
// by an admin; failures; the reconcile decisions.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

var launchCmdRe = regexp.MustCompile(`(?s)^select=(\S+) cpu=\S+ memory=\S+ disk=0 network=0 \| /opt/cloudland/scripts/backend/launch_vm\.sh '(\d+)' .*<<'EOF'\n(\S+)\nEOF$`)

func TestEvacuatePG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	stamp := time.Now().UnixNano() % 1000000
	pool, _ := f.createPool(fmt.Sprintf("ev-%d", stamp), 100, func(hostid int32, p string) string { return readyCheck(p, 100*gib, 0) })
	image := &model.Image{Name: fmt.Sprintf("ev-img-%d", stamp), Format: "qcow2", Status: "available", BootLoader: "uefi"}
	must(t, db.Create(image).Error)
	down := h[1]
	insts := []*model.Instance{}
	t.Cleanup(func() {
		for _, inst := range insts {
			db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.Migration{})
			db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.Volume{})
			db.Unscoped().Delete(inst)
		}
		db.Unscoped().Where("hostid IN ?", h).Delete(&model.StorageFence{})
		db.Unscoped().Delete(image)
	})
	newInstance := func(name string, status model.InstanceStatus, shared bool) *model.Instance {
		inst := &model.Instance{Hostname: fmt.Sprintf("%s-%d", name, stamp), Status: status, Hyper: down, Owner: 1, ImageID: image.ID, Cpu: 1,
			Memory: 512, Disk: 10, NestedEnable: true}
		must(t, db.Create(inst).Error)
		insts = append(insts, inst)
		poolID := int64(0)
		if shared {
			poolID = pool.ID
		}
		boot := &model.Volume{Name: inst.Hostname + "-boot", Owner: 1, Booting: true, InstanceID: inst.ID, StoragePoolID: poolID, Size: 10,
			Status: model.VolumeStatusAttached, Target: "vda"}
		must(t, db.Create(boot).Error)
		must(t, db.Model(&model.Volume{}).Where("id = ?", boot.ID).Update("path", fmt.Sprintf("volumes/volume-%d.disk", boot.ID)).Error)
		return inst
	}
	shared := newInstance("ev-shared", model.InstanceStatusRunning, true)
	data := &model.Volume{Name: "ev-data", Owner: 1, InstanceID: shared.ID, StoragePoolID: pool.ID, Size: 5, Status: model.VolumeStatusAttached, Target: "vdb"}
	must(t, db.Create(data).Error)
	must(t, db.Model(&model.Volume{}).Where("id = ?", data.ID).Update("path", fmt.Sprintf("volumes/volume-%d.disk", data.ID)).Error)
	stopped := newInstance("ev-stopped", model.InstanceStatusShutoff, true)
	local := newInstance("ev-local", model.InstanceStatusRunning, false)
	f.cland.take()

	// The host is online: migrated, not evacuated. Then it goes offline from maintenance
	_, err := hyperAdmin.Evacuate(ctx, down, &EvacuateRequest{TargetHyper: -1})
	wantCode(t, err, ErrEvacuationRefused, "not offline")
	f.setHostStatus(down, 2)
	MarkHyperOffline(ctx, down)
	f.setHostStatus(down, int(model.HyperStatusOffline))
	MarkHyperOffline(ctx, down)
	hyper := &model.Hyper{}
	must(t, db.Where("hostid = ?", down).Take(hyper).Error)
	if hyper.OfflineAt == nil || hyper.OfflinePrior == nil || *hyper.OfflinePrior != 2 {
		t.Fatalf("offline not recorded: %+v", hyper)
	}
	_, err = hyperAdmin.Evacuate(ctx, down, &EvacuateRequest{TargetHyper: -1})
	wantCode(t, err, ErrEvacuationRefused, "offline for")
	must(t, db.Model(&model.Hyper{}).Where("hostid = ?", down).Update("offline_at", time.Now().Add(-10*time.Minute)).Error)

	// Evacuate: the instance with a local disk stays, the two others wait for the fence, sent to the admin host
	results, err := hyperAdmin.Evacuate(ctx, down, &EvacuateRequest{TargetHyper: -1})
	must(t, err)
	byID := map[string]*EvacuateResult{}
	for _, r := range results {
		byID[r.InstanceUUID] = r
	}
	if r := byID[local.UUID]; r == nil || r.Status != migrationStatusNotDoing || !strings.Contains(r.Reason, "local storage pool") {
		t.Fatalf("local instance: %+v", r)
	}
	for _, inst := range []*model.Instance{shared, stopped} {
		if r := byID[inst.UUID]; r == nil || r.Status != migrationStatusFencing || r.Migration == "" {
			t.Fatalf("shared instance %s: %+v", inst.Hostname, r)
		}
	}
	fence := f.expect("fence", "gpfs_cluster.sh", h[0])
	if fence[0].input["action"] != "fence" || fence[0].input["ip"] != "10.93.0.2" {
		t.Fatalf("fence input %v", fence[0].input)
	}
	// Asked again meanwhile: nothing new, and nothing launched before the fence is done
	advanceEvacuations(ctx)
	if cmds := f.cland.take(); len(cmds) != 0 {
		t.Fatalf("sent before the fence: %v", cmds)
	}
	got := &model.Instance{}
	must(t, db.Take(got, shared.ID).Error)
	if got.Status != model.InstanceStatusMigrating {
		t.Fatalf("instance status while fencing: %s", got.Status)
	}
	must(t, f.report(fence[0], model.StorageRunSucceeded, "", `{"node":"stc-h2"}`))
	fr, _ := liveStorageFence(db, f.cluster.ID, down)
	if fr == nil || fr.Status != model.StorageFenceFenced || fr.Method != model.StorageFenceExpel || fr.Target != "stc-h2" {
		t.Fatalf("fence after its task: %+v", fr)
	}

	// Fenced: each instance is launched with its disks as they are, on the other hosts that reach the pool
	advanceEvacuations(ctx)
	launches := map[string]map[string]interface{}{}
	controls := map[string]string{}
	for _, rec := range f.cland.take() {
		m := launchCmdRe.FindStringSubmatch(rec)
		if m == nil {
			t.Fatalf("unexpected command %q", rec)
		}
		raw, _ := base64.StdEncoding.DecodeString(m[3])
		md := map[string]interface{}{}
		must(t, json.Unmarshal(raw, &md))
		launches[m[2]], controls[m[2]] = md, m[1]
	}
	if len(launches) != 2 {
		t.Fatalf("launches %v", launches)
	}
	md := launches[fmt.Sprint(shared.ID)]
	want := fmt.Sprintf("group-zone-%d:%d,%d", shared.ZoneID, h[0], h[2])
	if controls[fmt.Sprint(shared.ID)] != want {
		t.Fatalf("hosts of the launch %s, want %s", controls[fmt.Sprint(shared.ID)], want)
	}
	bd, _ := md["boot_disk"].(map[string]interface{})
	dd, _ := md["data_disks"].([]interface{})
	ev, _ := md["evacuate"].(map[string]interface{})
	if bd["existing"] != true || bd["driver"] != "gpfs" || !strings.HasSuffix(fmt.Sprint(bd["nvram"]), fmt.Sprintf("nvram/inst-%d_VARS.fd", shared.ID)) ||
		len(dd) != 1 || dd[0].(map[string]interface{})["device"] != "vdb" || ev["start"] != true {
		t.Fatalf("metadata of the shared instance: boot %v data %v evacuate %v", bd, dd, ev)
	}
	if e2, _ := launches[fmt.Sprint(stopped.ID)]["evacuate"].(map[string]interface{}); e2["start"] != false {
		t.Fatalf("a shut off instance is started: %v", e2)
	}

	// The source can not report the launch; the target can
	if _, err = EvacuationLaunched(ctx, shared.ID, down, "running", ""); err == nil {
		t.Fatal("the source reported the evacuation")
	}
	m, err := EvacuationLaunched(ctx, shared.ID, h[2], "running", "")
	must(t, err)
	must(t, db.Take(got, shared.ID).Error)
	if m == nil || m.Status != migrationStatusCompleted || got.Hyper != h[2] || got.Status != model.InstanceStatusRunning {
		t.Fatalf("after the launch: migration %+v instance %+v", m, got)
	}
	// The other one fails on its host: it stays the down host's, as it was
	_, err = EvacuationLaunched(ctx, stopped.ID, h[0], "error", "boot disk not found")
	must(t, err)
	gs := &model.Instance{}
	must(t, db.Take(gs, stopped.ID).Error)
	failed := &model.Migration{}
	must(t, db.Where("instance_id = ?", stopped.ID).Take(failed).Error)
	if gs.Hyper != down || gs.Status != model.InstanceStatusShutoff || failed.Status != migrationStatusFailed || !strings.Contains(failed.Message, "not found") {
		t.Fatalf("failed evacuation: %+v %+v", gs, failed)
	}

	// The down host's reports about the evacuated instance are not believed until it removed its copy
	if !EvacuatedFrom(ctx, shared.ID, down) || EvacuatedFrom(ctx, shared.ID, h[2]) || EvacuatedFrom(ctx, stopped.ID, down) {
		t.Fatal("guard of the evacuation source")
	}
	// The host is held: offline
	checkHostUnfence(ctx, down)
	if cmds := f.cland.take(); len(cmds) != 0 {
		t.Fatalf("unfenced while offline: %v", cmds)
	}

	// It comes back and reports its domains: the evacuated one goes, the two others are its own
	f.setHostStatus(down, 1)
	f.cland.take()
	must(t, ReconcileNode(ctx, down, "boot-1", "boot", []ReconcileDomain{{ID: shared.ID, State: "shut_off"}, {ID: stopped.ID, State: "shut_off"},
		{ID: local.ID, State: "shut_off"}, {ID: 999999999, State: "running"}}))
	cmds := f.cland.take()
	if len(cmds) != 1 || !strings.HasPrefix(cmds[0], fmt.Sprintf("inter=%d | /opt/cloudland/scripts/backend/node_reconcile.sh", down)) {
		t.Fatalf("reconcile commands %v", cmds)
	}
	body := cmds[0][strings.Index(cmds[0], "{") : strings.LastIndex(cmds[0], "}")+1]
	answer := struct {
		Boot  string           `json:"boot"`
		Start []int64          `json:"start"`
		Stale []reconcileStale `json:"stale"`
	}{}
	must(t, json.Unmarshal([]byte(body), &answer))
	if answer.Boot != "boot-1" || fmt.Sprint(answer.Start) != fmt.Sprint([]int64{stopped.ID, local.ID}) || len(answer.Stale) != 2 ||
		answer.Stale[0].ID != shared.ID || answer.Stale[1].ID != 999999999 {
		t.Fatalf("reconcile answer %+v", answer)
	}
	// Reconciled, but the copy is not removed yet: still fenced
	must(t, NodeReconciled(ctx, down, "boot-1", "boot"))
	if cmds := f.cland.take(); len(cmds) != 0 {
		t.Fatalf("unfenced with a copy left: %v", cmds)
	}
	evs, err := StaleInstanceCleared(ctx, down, shared.ID, true)
	must(t, err)
	if len(evs) != 1 || evs[0].ID != m.ID {
		t.Fatalf("evacuations cleaned %+v", evs)
	}
	if EvacuatedFrom(ctx, shared.ID, down) {
		t.Fatal("still guarded after the copy went")
	}
	// Nothing holds it, but it came back just now: its boot work goes first (storageUnfenceSettle)
	if cmds := f.cland.take(); len(cmds) != 0 {
		t.Fatalf("unfenced right after the reconcile: %v", cmds)
	}
	must(t, db.Model(&model.StorageFence{}).Where("hostid = ?", down).Update("created_at", time.Now().Add(-storageUnfenceSettle-2*time.Minute)).Error)
	must(t, db.Model(&model.Hyper{}).Where("hostid = ?", down).Update("reconciled_at", time.Now().Add(-storageUnfenceSettle-time.Minute)).Error)
	checkHostUnfence(ctx, down)
	unfence := f.expect("unfence", "gpfs_cluster.sh", h[0])
	if unfence[0].input["action"] != "unfence" || unfence[0].input["ip"] != "10.93.0.2" {
		t.Fatalf("unfence input %v", unfence[0].input)
	}
	must(t, f.report(unfence[0], model.StorageRunSucceeded, "", `{"node":"stc-h2"}`))
	if fr, _ := liveStorageFence(db, f.cluster.ID, down); fr != nil {
		t.Fatalf("fence left after it was lifted: %+v", fr)
	}

	// The only admin host is the one down: no fence without a confirmation; confirmed, it is recorded, nothing runs
	f.setHostStatus(h[0], int(model.HyperStatusOffline))
	_, err = ensureStorageFence(ctx, f.cluster, h[0], "")
	wantCode(t, err, ErrStorageFenceUnavailable, "only admin host")
	fc, err := ensureStorageFence(ctx, f.cluster, h[0], "admin")
	must(t, err)
	if fc.Status != model.StorageFenceConfirmed || fc.Method != model.StorageFenceByAdmin || fc.ConfirmedBy != "admin" {
		t.Fatalf("confirmed fence %+v", fc)
	}
	if cmds := f.cland.take(); len(cmds) != 0 {
		t.Fatalf("a confirmed fence ran %v", cmds)
	}
	// Back and reconciled: a confirmation only goes
	f.setHostStatus(h[0], 1)
	must(t, NodeReconciled(ctx, h[0], "boot-2", "boot"))
	if fr, _ := liveStorageFence(db, f.cluster.ID, h[0]); fr != nil {
		t.Fatalf("confirmed fence left: %+v", fr)
	}

	// cland finds no host with the resources: the evacuation fails, the instance is the down host's again
	f.setHostStatus(h[2], int(model.HyperStatusOffline))
	must(t, db.Model(&model.Hyper{}).Where("hostid = ?", h[2]).Update("offline_at", time.Now().Add(-time.Hour)).Error)
	results, err = hyperAdmin.Evacuate(ctx, h[2], &EvacuateRequest{TargetHyper: -1, ConfirmFenced: true, Instances: []int64{shared.ID}})
	must(t, err)
	if len(results) != 1 || results[0].Status == migrationStatusNotDoing {
		t.Fatalf("second evacuation %+v", results)
	}
	second := f.expect("fence of the second host", "gpfs_cluster.sh", h[0])
	if FailEvacuationOf(ctx, shared.ID, "x") {
		t.Fatal("an evacuation waiting for its fence was failed as a refused launch")
	}
	must(t, f.report(second[0], model.StorageRunSucceeded, "", `{"node":"stc-h3"}`))
	advanceEvacuations(ctx)
	if cmds := f.cland.take(); len(cmds) != 1 || launchCmdRe.FindStringSubmatch(cmds[0]) == nil {
		t.Fatalf("second launch %v", cmds)
	}
	if !FailEvacuationOf(ctx, shared.ID, "no host has the resources") || FailEvacuationOf(ctx, stopped.ID, "x") {
		t.Fatal("failing the evacuation of a launch cland refused")
	}
	must(t, db.Take(got, shared.ID).Error)
	if got.Hyper != h[2] || got.Status != model.InstanceStatusRunning || !strings.Contains(got.Reason, "resources") {
		t.Fatalf("instance after the refused launch %+v", got)
	}
}

// The reconcile decisions (shared-storage-design.md §11.4)
func TestReconcilePlanPG(t *testing.T) {
	f := newSharedFixture(t)
	db, h := f.db, f.hosts
	stamp := time.Now().UnixNano() % 1000000
	pool, _ := f.createPool(fmt.Sprintf("rc-%d", stamp), 100, func(hostid int32, p string) string { return readyCheck(p, 100*gib, 0) })
	insts := []*model.Instance{}
	t.Cleanup(func() {
		for _, inst := range insts {
			db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.Migration{})
			db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.Volume{})
			db.Unscoped().Delete(inst)
		}
	})
	mk := func(name string, hyper int32, status model.InstanceStatus, poolID int64) *model.Instance {
		inst := &model.Instance{Hostname: fmt.Sprintf("%s-%d", name, stamp), Status: status, Hyper: hyper, Owner: 1, RouterID: 7}
		must(t, db.Create(inst).Error)
		insts = append(insts, inst)
		must(t, db.Create(&model.Volume{Name: inst.Hostname, Owner: 1, Booting: true, InstanceID: inst.ID, StoragePoolID: poolID, Size: 1,
			Status: model.VolumeStatusAttached}).Error)
		return inst
	}
	own := mk("rc-own", h[0], model.InstanceStatusRunning, pool.ID)
	elsewhereShared := mk("rc-shared", h[1], model.InstanceStatusRunning, pool.ID)
	elsewhereLocal := mk("rc-local", h[1], model.InstanceStatusRunning, 0)
	migrating := mk("rc-migrating", h[1], model.InstanceStatusMigrating, pool.ID)
	deletingOld := mk("rc-deleting", h[0], model.InstanceStatusDeleting, pool.ID)
	deletingNew := mk("rc-deleting-new", h[0], model.InstanceStatusDeleting, pool.ID)
	deleted := mk("rc-deleted", h[0], model.InstanceStatusRunning, pool.ID)
	must(t, db.Delete(deleted).Error)
	must(t, db.Model(&model.Instance{}).Where("id = ?", deletingOld.ID).UpdateColumn("updated_at", time.Now().Add(-time.Hour)).Error)
	// An evacuation still waiting for its fence leaves the source's copy alone
	waiting := mk("rc-waiting", h[0], model.InstanceStatusMigrating, pool.ID)
	must(t, db.Create(&model.Migration{InstanceID: waiting.ID, Type: model.MigrationTypeEvacuate, SourceHyper: h[0], TargetHyper: -1, Status: migrationStatusFencing}).Error)

	doms := []ReconcileDomain{}
	for _, i := range []*model.Instance{own, elsewhereShared, elsewhereLocal, migrating, deletingOld, deletingNew, deleted, waiting} {
		doms = append(doms, ReconcileDomain{ID: i.ID})
	}
	plan, err := planReconcile(db, h[0], doms, time.Now())
	must(t, err)
	stale := []int64{}
	for _, s := range plan.Stale {
		stale = append(stale, s.ID)
		if s.Router != 7 {
			t.Fatalf("stale %d without its router", s.ID)
		}
	}
	resend := []int64{}
	for _, r := range plan.Resend {
		resend = append(resend, r.ID)
	}
	if fmt.Sprint(plan.Start) != fmt.Sprint([]int64{own.ID}) || fmt.Sprint(stale) != fmt.Sprint([]int64{elsewhereShared.ID, deleted.ID}) ||
		fmt.Sprint(plan.Leave) != fmt.Sprint([]int64{elsewhereLocal.ID, migrating.ID, deletingNew.ID, waiting.ID}) ||
		fmt.Sprint(resend) != fmt.Sprint([]int64{deletingOld.ID}) {
		t.Fatalf("plan: start %v stale %v leave %v resend %v", plan.Start, stale, plan.Leave, resend)
	}
	// The delete sent again keeps a shared boot disk for the clear_vm callback
	f.cland.take()
	must(t, resendInstanceDelete(f.ctx, deletingOld))
	cmds := f.cland.take()
	if len(cmds) != 1 || !strings.Contains(cmds[0], fmt.Sprintf("clear_vm.sh '%d' '7' '-' ''", deletingOld.ID)) {
		t.Fatalf("delete sent again %v", cmds)
	}
}
