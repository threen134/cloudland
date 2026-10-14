/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Alignment 02 on PostgreSQL (CLAPI_TEST_DB_URI, see placement_pg_test.go): the batch lookup behind the metrics
// and rule-link endpoints, and the busy check repeated under the instance row lock.

import (
	"testing"

	. "api/src/common"
	"api/src/model"
)

func TestReadableInstanceIDsPG(t *testing.T) {
	_, db := pgContext(t, 1)
	org := p2Org(9301)
	mine := &model.Instance{Owner: org, Hostname: p2Name("ri-a"), Status: model.InstanceStatusRunning, Hyper: 9301}
	other := &model.Instance{Owner: org + 1, Hostname: p2Name("ri-b"), Status: model.InstanceStatusRunning, Hyper: 9301}
	must(t, db.Create(mine).Error)
	must(t, db.Create(other).Error)
	reader := asMember(org, model.OrgReader)

	ids, err := ReadableInstanceIDs(reader, []string{"", mine.UUID, " " + mine.UUID + " "})
	must(t, err)
	if len(ids) != 1 || ids[mine.UUID] != mine.ID {
		t.Fatalf("got %v", ids)
	}
	for name, uuids := range map[string][]string{
		"another organization": {mine.UUID, other.UUID},
		"missing":              {mine.UUID, "9e1d4c0a-0000-4000-8000-000000000000"},
	} {
		if _, err := ReadableInstanceIDs(reader, uuids); errCode(err) != ErrInstanceNotFound {
			t.Errorf("%s: got %v, want not found", name, err)
		}
	}
	// A system admin reads every organization's instances
	admin := (&MemberShip{UserID: 1, OrgID: org, OrgRole: model.OrgAdmin, SystemRole: model.SystemAdmin}).SetContext(reader)
	if ids, err = ReadableInstanceIDs(admin, []string{mine.UUID, other.UUID}); err != nil || len(ids) != 2 {
		t.Fatalf("admin: %v %v", ids, err)
	}
}

// Review #4: a request that loaded the instance before another one changed its status is refused under the lock
func TestBusyRecheckedUnderLockPG(t *testing.T) {
	cland := startFakeCland(t)
	_, db := pgContext(t, 1)
	org := p2Org(9302)
	inst := &model.Instance{Owner: org, Hostname: p2Name("lk"), Status: model.InstanceStatusRescuing, Hyper: 9302}
	must(t, db.Create(inst).Error)
	stale := *inst
	stale.Status = model.InstanceStatusShutoff // what a request loaded before the rescue started
	if err := instanceAdmin.Update(asMember(org, model.OrgWriter), &stale, stale.Hostname, "start", false); errCode(err) != ErrInstanceInvalidState {
		t.Fatalf("start on a stale copy: got %v, want invalid state", err)
	}
	if cmds := cland.take(); len(cmds) != 0 {
		t.Fatalf("commands sent: %v", cmds)
	}
}
