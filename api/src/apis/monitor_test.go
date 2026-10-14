package apis

// Alignment 02 V3 (TC-13 SEC-16): the metrics endpoints refuse values that would change the PromQL query, and
// host metrics are for system admins only. These refusals come before any database or Prometheus call.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"api/src/common"
	"api/src/model"

	"github.com/gin-gonic/gin"
)

func postAs(handler gin.HandlerFunc, m *common.MemberShip, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req.WithContext(m.SetContext(context.Background()))
	handler(c)
	return w
}

func TestHostMetricsNeedSystemAdmin(t *testing.T) {
	member := &common.MemberShip{UserID: 2, UserName: "m", OrgID: 5, OrgRole: model.OrgAdmin}
	body := `{"start":"1","end":"2","step":"60","hostname":["work-01"]}`
	for name, h := range map[string]gin.HandlerFunc{"cpu": monitorAPI.GetHyperCPU, "memory": monitorAPI.GetHyperMemory} {
		if w := postAs(h, member, body); w.Code != http.StatusForbidden {
			t.Errorf("%s: org admin got %d, want 403", name, w.Code)
		}
	}
}

func TestMetricLabelsRefuseQuerySyntax(t *testing.T) {
	admin := &common.MemberShip{UserID: 1, UserName: "a", OrgID: 5, OrgRole: model.OrgAdmin, SystemRole: model.SystemAdmin}
	for _, host := range []string{`work-01"}) or vector(1) #`, `.*`, `a|b`, ``, strings.Repeat("a", 65)} {
		body := `{"start":"1","end":"2","step":"60","hostname":[` + quote(host) + `]}`
		if w := postAs(monitorAPI.GetHyperCPU, admin, body); w.Code != http.StatusBadRequest {
			t.Errorf("hostname %q: got %d, want 400", host, w.Code)
		}
	}
	// Device names are checked before the instances are looked up
	bad := quote(`vda"}[5m]) or up{job=~".+`)
	if w := postAs(monitorAPI.GetDisk, admin, `{"start":"1","end":"2","step":"60","id":["x"],"disk":[`+bad+`]}`); w.Code != http.StatusBadRequest {
		t.Errorf("disk: got %d, want 400", w.Code)
	}
	if w := postAs(monitorAPI.GetTraffic, admin, `{"start":"1","end":"2","step":"60","id":["x"],"network":[`+bad+`]}`); w.Code != http.StatusBadRequest {
		t.Errorf("network: got %d, want 400", w.Code)
	}
	for _, ok := range []string{"work-01", "vda", "tap1a2b3c4d", "eth0.756"} {
		if !metricLabelRe.MatchString(ok) {
			t.Errorf("%q refused", ok)
		}
	}
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// A '.' in a name matches only a '.': "work.0." must not select work-01 and work-02
func TestPromRegexAlternationEscapesDots(t *testing.T) {
	got := promRegexAlternation([]string{"work.01", "vda", "eth0.756"})
	want := `work\\.01|vda|eth0\\.756`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
