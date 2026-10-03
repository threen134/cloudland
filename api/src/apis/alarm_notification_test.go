/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

// postJSON runs handler on a POST with body, as a system admin acting in organization 5
func postJSON(handler gin.HandlerFunc, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	membership := &common.MemberShip{UserID: 1, UserName: "tester", OrgID: 5, OrgRole: model.OrgAdmin, SystemRole: model.SystemAdmin}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req.WithContext(membership.SetContext(context.Background()))
	handler(c)
	return w
}

// POST /node-alarm-rules answered 500 for a mistake in the request (TC-10 ALM-01: an unknown rule type, config
// keys the templates do not use, a value a template needs). These are the caller's: 400, before anything is
// written.
func TestCreateNodeAlarmRuleBadConfigIs400(t *testing.T) {
	defer services.UseLocalRuleTemplates(filepath.Join("..", "..", "..", "deploy", "roles", "monitor", "templates"))()
	cases := []struct {
		name, body, want string
	}{
		{"unknown rule type", `{"rule_type":"foo","name":"r","config":{}}`, "unsupported rule type"},
		{"config key the template does not use", `{"rule_type":"ip_block","name":"r","config":{"foo":1}}`, "config keys not used"},
		{"config key clapi sets itself", `{"rule_type":"ip_block","name":"r","config":{"owner":"9"}}`, "config keys not used"},
		{"value the template needs", `{"rule_type":"hypervisor_vcpu","name":"r","config":{"for_duration":"10m"}}`, "config is missing values"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := postJSON(alarmAPI.CreateNodeAlarmRule, c.body)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.want) {
				t.Fatalf("got %d %s, want 400 with %q", w.Code, w.Body.String(), c.want)
			}
		})
	}
}

// Channels are pushed with the owner's organization UUID: a push without one (CPGateway's organization ID, as
// before, means nothing in a region) is refused rather than stored with no owner or a wrong one
func TestSyncChannelRequiresOrgUUID(t *testing.T) {
	cases := []struct {
		name, body string
	}{
		{"upsert with org_id only", `{"action":"upsert","channel":{"uuid":"c1","org_id":3,"name":"n","type":"webhook","config":{},"enabled":true}}`},
		{"upsert without uuid", `{"action":"upsert","channel":{"org_uuid":"o1","name":"n","type":"webhook","config":{}}}`},
		{"bulk with one channel lacking org_uuid", `{"action":"bulk_sync","channels":[{"uuid":"c1","org_uuid":"o1"},{"uuid":"c2","org_id":2}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := postJSON(notificationAPI.SyncChannel, c.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d %s, want 400", w.Code, w.Body.String())
			}
		})
	}
}

// The payload a CPGateway push becomes: owner by UUID, enabled as sent (false included)
func TestChannelPayloadToModel(t *testing.T) {
	var p channelPayload
	if err := json.Unmarshal([]byte(`{"uuid":"c1","org_uuid":"o1","name":"n","type":"webhook","config":{"url":"https://x"},"enabled":false}`), &p); err != nil {
		t.Fatal(err)
	}
	ch, err := p.toModel()
	if err != nil {
		t.Fatal(err)
	}
	if ch.UUID != "c1" || ch.OrgUUID != "o1" || ch.OrgID != 0 || ch.Enabled || ch.Config != `{"url":"https://x"}` {
		t.Fatalf("unexpected channel %+v", ch)
	}
}
