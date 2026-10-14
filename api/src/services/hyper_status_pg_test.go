/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Alignment 04 O8 on PostgreSQL (CLAPI_TEST_DB_URI, see placement_pg_test.go): enabling or disabling a host goes
// by the status it is in.

import (
	"testing"

	. "api/src/common"
	"api/src/model"
)

func TestHyperEnableByStatusPG(t *testing.T) {
	cland := startFakeCland(t)
	ctx, db := pgContext(t, 1)
	admin := (&MemberShip{UserID: 1, OrgID: 1, OrgRole: model.OrgAdmin, SystemRole: model.SystemAdmin}).SetContext(ctx)
	newHyper := func(hostid int32, status int32) *model.Hyper {
		h := &model.Hyper{Hostid: hostid, Hostname: p2Name("hs"), Status: status, CpuOverRate: 1, MemOverRate: 1, DiskOverRate: 1}
		db.Unscoped().Where("hostid = ?", hostid).Delete(&model.Hyper{})
		must(t, db.Create(h).Error)
		t.Cleanup(func() { db.Unscoped().Delete(h) })
		return h
	}
	reload := func(h *model.Hyper) *model.Hyper {
		got := &model.Hyper{}
		must(t, db.Take(got, h.ID).Error)
		return got
	}

	// Deploying and deploy-failed hosts are not enabled: they would become schedulable and the deploy could not
	// be retried
	for _, status := range []int32{4, 5} {
		h := newHyper(9401+status, status)
		req := *h
		req.Status = 1
		if err := hyperAdmin.Update(admin, &req); errCode(err) != ErrHypervisorInvalidState {
			t.Fatalf("status %d: got %v, want invalid state", status, err)
		}
		if got := reload(h); got.Status != status {
			t.Fatalf("status %d changed to %d", status, got.Status)
		}
	}

	// An offline host keeps status 10 (evacuation and fencing go by it) and comes back in the chosen status
	h := newHyper(9411, model.HyperStatusOffline)
	req := *h
	req.Status = 0
	must(t, hyperAdmin.Update(admin, &req))
	got := reload(h)
	if got.Status != model.HyperStatusOffline || got.OfflinePrior == nil || *got.OfflinePrior != 0 {
		t.Fatalf("offline host: status %d, offline_prior %v", got.Status, got.OfflinePrior)
	}
	if cmds := cland.take(); len(cmds) != 0 {
		t.Fatalf("commands sent: %v", cmds)
	}
}
