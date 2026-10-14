/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

// Alignment 04 (test-items/alignment/04-告警控制台部署与计算节点.md): refusals that come before any database,
// Prometheus or file access.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"api/src/common"
	"api/src/model"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

// O4: the Alertmanager webhooks want the shared token, and nothing gets in while none is configured
func TestAlertWebhookNeedsToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/hook", alertWebhookAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	call := func(auth string) int {
		req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(`{}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	defer viper.Set("alert.webhook_token", "")
	viper.Set("alert.webhook_token", "")
	for _, auth := range []string{"", "Bearer ", "Bearer x"} {
		if code := call(auth); code != http.StatusUnauthorized {
			t.Errorf("no token configured, %q: got %d", auth, code)
		}
	}
	viper.Set("alert.webhook_token", "s3cret")
	for _, auth := range []string{"", "s3cret", "Bearer s3cre", "Bearer s3cret ", "Basic s3cret"} {
		if code := call(auth); code != http.StatusUnauthorized {
			t.Errorf("%q: got %d, want 401", auth, code)
		}
	}
	if code := call("Bearer s3cret"); code != http.StatusNoContent {
		t.Errorf("right token: got %d", code)
	}
}

// O2, O3: readers do not create rules; region_id must not be able to break the rule file
func TestAlarmRuleCreateRefusals(t *testing.T) {
	reader := &common.MemberShip{UserID: 2, UserName: "r", OrgID: 5, OrgRole: model.OrgReader}
	writer := &common.MemberShip{UserID: 3, UserName: "w", OrgID: 5, OrgRole: model.OrgWriter}
	cpu := func(region string) string {
		return `{"name":"cpu_rule","region_id":` + quote(region) + `,"rules":[{"name":"x","limit":80,"rule":"gt","duration":5,"level":"warning"}]}`
	}
	bw := func(region string) string {
		return `{"name":"bw_rule","region_id":` + quote(region) + `,"rules":[{"direction":"in","limit":80,"duration":5,"level":"warning"}]}`
	}
	creates := map[string]struct {
		h    gin.HandlerFunc
		body func(string) string
	}{"cpu": {alarmAPI.CreateCPURule, cpu}, "memory": {alarmAPI.CreateMemoryRule, cpu}, "bandwidth": {alarmAPI.CreateBWRule, bw}}
	for name, c := range creates {
		if w := postAs(c.h, reader, c.body("r1")); w.Code != http.StatusForbidden {
			t.Errorf("%s: reader got %d, want 403", name, w.Code)
		}
		for _, region := range []string{"r1\"\n      owner: \"9", `r1"`, "a b", strings.Repeat("a", 65)} {
			if w := postAs(c.h, writer, c.body(region)); w.Code != http.StatusBadRequest {
				t.Errorf("%s: region %q got %d, want 400", name, region, w.Code)
			}
		}
	}
	for _, ok := range []string{"", "49fce7f0-0ae3-49e1-8ffb-02c816b26bb1", "us-east_1.a"} {
		if !ruleRegionRe.MatchString(ok) {
			t.Errorf("region %q refused", ok)
		}
	}
}

// O5, O6: Prometheus' whole rule set, the node alarm rules and the hypervisor list are for system admins, in
// clapi too and not only in the gateway whitelist
func TestAdminOnlyAlarmAndHyperEndpoints(t *testing.T) {
	orgAdmin := &common.MemberShip{UserID: 2, UserName: "m", OrgID: 5, OrgRole: model.OrgAdmin}
	for name, h := range map[string]gin.HandlerFunc{
		"active rules":           alarmAPI.GetActiveRules,
		"create node alarm rule": alarmAPI.CreateNodeAlarmRule,
		"update node alarm rule": alarmAPI.UpdateNodeAlarmRule,
		"list node alarm rules":  alarmAPI.GetNodeAlarmRules,
		"delete node alarm rule": alarmAPI.DeleteNodeAlarmRule,
		"list hypervisors":       hyperAPI.List,
		"get hypervisor":         hyperAPI.Get,
	} {
		if w := postAs(h, orgAdmin, `{}`); w.Code != http.StatusForbidden {
			t.Errorf("%s: org admin got %d, want 403", name, w.Code)
		}
	}
}
