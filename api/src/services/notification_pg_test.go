/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Notification and alarm event tests that need PostgreSQL. Like placement_pg_test.go they run against the
// database given by CLAPI_TEST_DB_URI and are skipped without it.

import (
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"api/src/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// pgNotifOrg creates an organization with the given UUID (a new one when empty)
func pgNotifOrg(t *testing.T, db *gorm.DB, orgUUID string, orgType model.OrgType) *model.Organization {
	t.Helper()
	if orgUUID == "" {
		orgUUID = uuid.New().String()
	}
	org := &model.Organization{Model: model.Model{UUID: orgUUID}, Name: "pg-notif", Slug: fmt.Sprintf("pgn-%d", time.Now().UnixNano()),
		OrgType: orgType, OwnerUserID: 1}
	must(t, db.Create(org).Error)
	return org
}

func pgChannel(orgUUID string, enabled bool) *model.NotificationChannel {
	ch := &model.NotificationChannel{OrgUUID: orgUUID, Name: "pg-notif", Type: "webhook", Config: `{"url":"https://example.com"}`, Enabled: enabled}
	ch.UUID = uuid.New().String()
	return ch
}

func reloadChannel(t *testing.T, db *gorm.DB, channelUUID string) *model.NotificationChannel {
	t.Helper()
	ch := &model.NotificationChannel{}
	must(t, db.Where("uuid = ?", channelUUID).Take(ch).Error)
	return ch
}

// D9: a channel pushed as disabled was mirrored as enabled (gorm default:true), by upsert and by bulk sync
func TestPGChannelMirrorKeepsDisabled(t *testing.T) {
	ctx, db := pgContext(t, 1)
	admin := &NotificationAdmin{}
	org := pgNotifOrg(t, db, "", model.OrgTypeTeam)

	ch := pgChannel(org.UUID, false)
	must(t, admin.UpsertChannel(ctx, ch))
	if got := reloadChannel(t, db, ch.UUID); got.Enabled {
		t.Fatal("upsert: a channel created disabled is mirrored as enabled")
	}

	// Bulk sync: a new disabled channel next to a new enabled one (the first channel, left out, is dropped)
	bulk := pgChannel(org.UUID, false)
	must(t, admin.BulkSyncChannels(ctx, []model.NotificationChannel{*bulk, *pgChannel(org.UUID, true)}))
	if got := reloadChannel(t, db, bulk.UUID); got.Enabled {
		t.Fatal("bulk sync: a channel created disabled is mirrored as enabled")
	}
	channels, err := admin.EnabledChannelsOfOrg(ctx, org.ID)
	must(t, err)
	if len(channels) != 1 || channels[0].UUID == bulk.UUID {
		t.Fatalf("enabled channels of the organization: %v", channels)
	}
}

// D2: the owner of a mirrored channel is the local organization its UUID names, never CPGateway's own
// organization ID; a channel that arrives before its organization is bound once the organization is here
func TestPGChannelOwnerResolvedByOrgUUID(t *testing.T) {
	ctx, db := pgContext(t, 1)
	admin := &NotificationAdmin{}
	orgA := pgNotifOrg(t, db, "", model.OrgTypeTeam)
	orgB := pgNotifOrg(t, db, "", model.OrgTypeTeam)

	chA := pgChannel(orgA.UUID, true)
	must(t, admin.UpsertChannel(ctx, chA))
	if got := reloadChannel(t, db, chA.UUID); got.OrgID != orgA.ID || got.OrgUUID != orgA.UUID {
		t.Fatalf("channel of A mirrored with org_id %d / %s, want %d / %s", got.OrgID, got.OrgUUID, orgA.ID, orgA.UUID)
	}
	if err := admin.ValidateChannelOwnership(ctx, []string{chA.UUID}, orgA.ID); err != nil {
		t.Fatalf("A binding its own channel: %v", err)
	}
	for _, orgID := range []int64{orgB.ID, 0} {
		if err := admin.ValidateChannelOwnership(ctx, []string{chA.UUID}, orgID); !errors.Is(err, ErrChannelNotOwned) {
			t.Fatalf("organization %d binding A's channel: %v", orgID, err)
		}
	}
	if err := admin.ValidateChannelOwnership(ctx, []string{uuid.New().String()}, orgA.ID); !errors.Is(err, ErrChannelNotSynced) {
		t.Fatalf("unknown channel: %v", err)
	}
	if channels, err := admin.EnabledChannelsOfOrg(ctx, orgB.ID); err != nil || len(channels) != 0 {
		t.Fatalf("B must not get A's channels: %v %v", channels, err)
	}

	// The organization is not in this region yet: the channel is kept, owned by nobody
	lateUUID := uuid.New().String()
	late := pgChannel(lateUUID, true)
	must(t, admin.UpsertChannel(ctx, late))
	if got := reloadChannel(t, db, late.UUID); got.OrgID != 0 {
		t.Fatalf("channel of an unknown organization owned by %d", got.OrgID)
	}
	if channels, err := admin.EnabledChannelsOfOrg(ctx, 0); err != nil || len(channels) != 0 {
		t.Fatalf("organization 0 must own nothing: %v %v", channels, err)
	}
	// ... and bound to it once it arrives
	orgC := pgNotifOrg(t, db, lateUUID, model.OrgTypeTeam)
	if err := admin.ValidateChannelOwnership(ctx, []string{late.UUID}, orgC.ID); err != nil {
		t.Fatalf("late organization binding its channel: %v", err)
	}
	if got := reloadChannel(t, db, late.UUID); got.OrgID != orgC.ID {
		t.Fatalf("late channel owned by %d, want %d", got.OrgID, orgC.ID)
	}
}

// D12: the state an alarm event goes through, and the notification each report calls for
func TestPGAlarmEventLifecycle(t *testing.T) {
	ctx, db := pgContext(t, 1)
	admin := &NotificationAdmin{}
	labels := map[string]string{"alertname": "PgTest", "owner": "5", "rule_group": "rg", "severity": "warning", "summary": "s"}
	start := time.Now().Add(-time.Minute)

	// Seen for the first time already resolved: resolved, not firing
	first := fmt.Sprintf("pg-fp-%d-a", time.Now().UnixNano())
	event, notify, err := admin.UpsertAlarmEvent(ctx, first, labels, "resolved", start)
	must(t, err)
	if event.Status != "resolved" || event.ResolvedAt == nil || notify != "resolved" {
		t.Fatalf("first report resolved: status %s resolved_at %v notify %q", event.Status, event.ResolvedAt, notify)
	}

	fp := fmt.Sprintf("pg-fp-%d-b", time.Now().UnixNano())
	steps := []struct {
		status, wantStatus, wantNotify string
	}{
		{"firing", "firing", "firing_trigger"},
		{"firing", "firing", "repeat_remind"},
		{"resolved", "resolved", "resolved"},
		{"resolved", "resolved", ""},           // reported again: nothing to tell
		{"firing", "firing", "firing_trigger"}, // the same alert again: reopened
	}
	var resolvedAt *time.Time
	for i, s := range steps {
		event, notify, err := admin.UpsertAlarmEvent(ctx, fp, labels, s.status, start)
		must(t, err)
		stored := &model.AlarmEvent{}
		must(t, db.Where("fingerprint = ?", fp).Take(stored).Error)
		if event.Status != s.wantStatus || stored.Status != s.wantStatus || notify != s.wantNotify {
			t.Fatalf("step %d (%s): status %s / stored %s, notify %q; want %s, %q", i, s.status, event.Status, stored.Status, notify, s.wantStatus, s.wantNotify)
		}
		switch i {
		case 2:
			resolvedAt = stored.ResolvedAt
		case 3:
			if stored.ResolvedAt == nil || resolvedAt == nil || !stored.ResolvedAt.Equal(*resolvedAt) {
				t.Fatalf("a repeated resolved report moved resolved_at: %v -> %v", resolvedAt, stored.ResolvedAt)
			}
		case 4:
			if stored.ResolvedAt != nil {
				t.Fatalf("a reopened event keeps resolved_at %v", stored.ResolvedAt)
			}
		}
	}
	must(t, db.Unscoped().Where("fingerprint IN ?", []string{first, fp}).Delete(&model.AlarmEvent{}).Error)
}

// D12: an alert without an owner label (node alerts before their templates had one) belongs to the system
// organization instead of to nobody, new or already recorded
func TestPGOwnerlessAlarmEventGoesToSystemOrg(t *testing.T) {
	ctx, db := pgContext(t, 1)
	admin := &NotificationAdmin{}
	system := &model.Organization{}
	if err := db.Where("org_type = ?", model.OrgTypeSystem).Order("id").Take(system).Error; err != nil {
		system = pgNotifOrg(t, db, "", model.OrgTypeSystem)
	}
	platformOrgIDCache.Store(0)
	want := strconv.FormatInt(system.ID, 10)

	fp := fmt.Sprintf("pg-fp-%d-node", time.Now().UnixNano())
	event, _, err := admin.UpsertAlarmEvent(ctx, fp, map[string]string{"alertname": "ComputeNodeDown", "severity": "critical"}, "firing", time.Now())
	must(t, err)
	if event.Owner != want {
		t.Fatalf("ownerless alert recorded with owner %q, want %q", event.Owner, want)
	}

	// Recorded before: given the owner on its next report, or by the startup upgrade
	old := &model.AlarmEvent{Model: model.Model{UUID: uuid.New().String()}, Fingerprint: fp + "-old", AlertName: "ComputeNodeDown", Status: "firing",
		FiredAt: time.Now(), LastFiredAt: time.Now()}
	stale := &model.AlarmEvent{Model: model.Model{UUID: uuid.New().String()}, Fingerprint: fp + "-stale", AlertName: "ComputeNodeDown", Status: "resolved",
		FiredAt: time.Now(), LastFiredAt: time.Now()}
	must(t, db.Create(old).Error)
	must(t, db.Create(stale).Error)
	event, _, err = admin.UpsertAlarmEvent(ctx, old.Fingerprint, map[string]string{"alertname": "ComputeNodeDown"}, "firing", time.Now())
	must(t, err)
	stored := &model.AlarmEvent{}
	must(t, db.Where("fingerprint = ?", old.Fingerprint).Take(stored).Error)
	if event.Owner != want || stored.Owner != want {
		t.Fatalf("ownerless event not given to the system organization on its next report: %q / %q", event.Owner, stored.Owner)
	}
	must(t, model.AssignOwnerlessAlarmEvents(db))
	stored = &model.AlarmEvent{}
	must(t, db.Where("fingerprint = ?", stale.Fingerprint).Take(stored).Error)
	if stored.Owner != want {
		t.Fatalf("startup upgrade left an ownerless event with owner %q", stored.Owner)
	}
	must(t, db.Unscoped().Where("fingerprint LIKE ?", fp+"%").Delete(&model.AlarmEvent{}).Error)
}

// Only the owner of a VM alarm rule group (or a system admin) sees and changes its channel bindings; node alarm
// rules are system admins' only
func TestPGRuleGroupAccess(t *testing.T) {
	ctx, db := pgContext(t, 1)
	admin := &NotificationAdmin{}
	group := &model.RuleGroupV2{Name: fmt.Sprintf("pg_rg_%d", time.Now().UnixNano()), Type: RuleTypeCPU, Owner: 21, Enabled: true}
	group.UUID = uuid.New().String()
	group.RuleID = group.UUID
	must(t, db.Create(group).Error)
	node := &model.NodeAlarmRule{RuleType: fmt.Sprintf("pg_%d", time.Now().UnixNano()), Name: "n", Owner: "1", Enabled: true,
		Config: model.ConfigWrapper{RawMessage: []byte(`{}`)}}
	must(t, db.Create(node).Error)
	defer func() {
		db.Unscoped().Delete(group)
		db.Unscoped().Delete(node)
	}()

	cases := []struct {
		name     string
		ruleUUID string
		org      int64
		sysAdmin bool
		want     error
	}{
		{"owner", group.UUID, 21, false, nil},
		{"other organization", group.UUID, 22, false, ErrRuleGroupNotOwned},
		{"system admin, other organization", group.UUID, 22, true, nil},
		{"node rule, member", node.UUID, 21, false, ErrRuleGroupNotOwned},
		{"node rule, system admin", node.UUID, 21, true, nil},
		{"unknown", uuid.New().String(), 21, true, ErrRuleGroupNotFound},
	}
	for _, c := range cases {
		if err := admin.CheckRuleGroupAccess(ctx, c.ruleUUID, c.org, c.sysAdmin); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

// Saving a rule group's channel bindings again, or unbinding a channel and binding it back, failed with a
// duplicate key: bindings are soft deleted and the unique index covered the deleted rows
func TestPGRuleBindingsRebind(t *testing.T) {
	ctx, db := pgContext(t, 1)
	admin := &NotificationAdmin{}
	org := pgNotifOrg(t, db, "", model.OrgTypeTeam)
	ch := pgChannel(org.UUID, true)
	must(t, admin.UpsertChannel(ctx, ch))
	ruleGroup := uuid.New().String()

	live := func() int64 {
		var n int64
		must(t, db.Model(&model.AlarmNotificationBinding{}).Where("rule_group_uuid = ?", ruleGroup).Count(&n).Error)
		return n
	}
	for i, channels := range [][]string{{ch.UUID}, {ch.UUID}, {}, {ch.UUID}} {
		if err := admin.SetRuleBindings(ctx, ruleGroup, channels, org.ID); err != nil {
			t.Fatalf("save %d (%v): %v", i, channels, err)
		}
		if got := live(); got != int64(len(channels)) {
			t.Fatalf("save %d: %d live bindings, want %d", i, got, len(channels))
		}
	}
	// The same pair is still unique among live rows
	dup := &model.AlarmNotificationBinding{RuleGroupUUID: ruleGroup, ChannelUUID: ch.UUID, OrgID: org.ID}
	if err := db.Create(dup).Error; err == nil {
		t.Fatal("a second live binding of the same pair was accepted")
	}
}
