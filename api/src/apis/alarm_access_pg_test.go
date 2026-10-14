/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

// Alignment 04 O1, O2 on PostgreSQL (CLAPI_TEST_DB_URI): a VM rule group is changed only by its organization's
// writers, and only watches instances the caller can read. Every case here is refused before a link is written,
// matched_vms.json is touched or Prometheus is reloaded.

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/google/uuid"
)

func TestAlarmRuleGroupOwnershipPG(t *testing.T) {
	db := apiPGDB(t)
	suffix := time.Now().UnixNano() % 1000000
	orgA, orgB := 940000+suffix%10000*2, 940001+suffix%10000*2
	group := &model.RuleGroupV2{Name: fmt.Sprintf("pg_own_%d", suffix), Type: services.RuleTypeCPU, Owner: orgA, Enabled: true}
	group.UUID = uuid.New().String()
	group.RuleID = "alarm-cpu-pg-" + group.UUID
	if err := db.Create(group).Error; err != nil {
		t.Fatal(err)
	}
	instA := &model.Instance{Owner: orgA, Hostname: fmt.Sprintf("pg-own-a-%d", suffix), Status: model.InstanceStatusRunning}
	instB := &model.Instance{Owner: orgB, Hostname: fmt.Sprintf("pg-own-b-%d", suffix), Status: model.InstanceStatusRunning}
	for _, inst := range []*model.Instance{instA, instB} {
		if err := db.Create(inst).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		db.Unscoped().Where("group_uuid = ?", group.UUID).Delete(&model.VMRuleLink{})
		db.Unscoped().Delete(group)
		db.Unscoped().Delete(instA)
		db.Unscoped().Delete(instB)
	})
	member := func(org int64, role model.OrgRole) *common.MemberShip {
		return &common.MemberShip{UserID: 7, UserName: "pg", OrgID: org, OrgRole: role}
	}
	body := func(vm string) string {
		return fmt.Sprintf(`{"group_uuid":%q,"vm_links":[{"vm_uuid":%q}]}`, group.UUID, vm)
	}
	link := alarmAPI.LinkRuleToVMWithType("alarm")
	unlink := alarmAPI.UnlinkRuleFromVMWithType("alarm")

	for name, tc := range map[string]struct {
		h    func() int
		want int
	}{
		"another organization's instance": {func() int { return postAs(link, member(orgA, model.OrgWriter), body(instB.UUID)).Code }, http.StatusBadRequest},
		"another organization's group":    {func() int { return postAs(link, member(orgB, model.OrgWriter), body(instB.UUID)).Code }, http.StatusNotFound},
		"reader links":                    {func() int { return postAs(link, member(orgA, model.OrgReader), body(instA.UUID)).Code }, http.StatusForbidden},
		"another organization unlinks":    {func() int { return postAs(unlink, member(orgB, model.OrgWriter), body(instA.UUID)).Code }, http.StatusNotFound},
		"reader unlinks":                  {func() int { return postAs(unlink, member(orgA, model.OrgReader), body(instA.UUID)).Code }, http.StatusForbidden},
		"reader rebinds channels": {func() int {
			return postAs(notificationAPI.BindRuleChannels, member(orgA, model.OrgReader),
				fmt.Sprintf(`{"rule_group_uuid":%q,"channel_uuids":[]}`, group.UUID)).Code
		}, http.StatusForbidden},
	} {
		if got := tc.h(); got != tc.want {
			t.Errorf("%s: got %d, want %d", name, got, tc.want)
		}
	}
	var links int64
	db.Model(&model.VMRuleLink{}).Where("group_uuid = ?", group.UUID).Count(&links)
	if links != 0 {
		t.Fatalf("%d links written by refused requests", links)
	}
}
