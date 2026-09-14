package apis

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"

	"cpgateway-go/src/common"
	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
	"cpgateway-go/src/services"
	"cpgateway-go/src/tracing"
)

func writeTestKeys(t *testing.T) {
	dir := t.TempDir()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	priv, _ := x509.MarshalPKCS8PrivateKey(key)
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	privPath, pubPath := filepath.Join(dir, "private.pem"), filepath.Join(dir, "public.pem")
	os.WriteFile(privPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), 0600)
	os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}), 0600)
	viper.Set("auth.rsa_private_key_path", privPath)
	viper.Set("auth.rsa_public_key_path", pubPath)
}

type testClient struct {
	t *testing.T
	r *gin.Engine
}

func (c testClient) do(method, path, token string, body interface{}) (int, interface{}) {
	var raw []byte
	contentType := "application/json"
	switch b := body.(type) {
	case nil:
	case url.Values:
		raw = []byte(b.Encode())
		contentType = "application/x-www-form-urlencoded"
	default:
		raw, _ = json.Marshal(b)
	}
	req, _ := http.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	c.r.ServeHTTP(w, req)
	var out interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (c testClient) expect(method, path, token string, body interface{}, status int) map[string]interface{} {
	c.t.Helper()
	code, out := c.do(method, path, token, body)
	if code != status {
		c.t.Fatalf("%s %s: expected %d, got %d: %v", method, path, status, code, out)
	}
	m, _ := out.(map[string]interface{})
	return m
}

func (c testClient) expectList(method, path, token string, status int) []interface{} {
	c.t.Helper()
	code, out := c.do(method, path, token, nil)
	if code != status {
		c.t.Fatalf("%s %s: expected %d, got %d: %v", method, path, status, code, out)
	}
	l, _ := out.([]interface{})
	return l
}

// TestPythonParityFlows exercises the main CPGateway flows against in-memory SQLite and a fake
// region backend, asserting the behaviour and API contract of the Python implementation.
func TestPythonParityFlows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	writeTestKeys(t)
	flushTracing := tracing.Init(context.Background(), "test", "dev")
	defer flushTracing()
	viper.Set("auth.secret_key", "test-secret")
	viper.Set("superuser.email", "admin@example.com")
	viper.Set("superuser.username", "admin")
	viper.Set("superuser.password", "changeme")

	var mu sync.Mutex
	var seen []string
	var traced []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" org="+r.Header.Get("X-Org-ID"))
		if (r.Method == "POST" && r.URL.Path == "/api/v1/instances") || r.Method == "DELETE" {
			traced = append(traced, r.Method+" "+r.URL.Path+" traceparent="+r.Header.Get("traceparent"))
		}
		mu.Unlock()
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/api/v1/internal/"):
			w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && p == "/api/v1/instances/abc":
			w.Write([]byte(`{"cpu":2,"memory":2048,"disk":10}`))
		case r.Method == "POST" && p == "/api/v1/instances":
			w.WriteHeader(201)
			w.Write([]byte(`{"id":"abc"}`))
		case r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			w.WriteHeader(500) // list endpoints fail so consumption sync never overwrites test data
		}
	}))
	defer backend.Close()

	dbs.DB()
	services.Init()
	c := testClient{t, Routes()}
	db := dbs.DB()

	// Registration, validation, activation.
	reg := map[string]interface{}{"email": "u1@example.com", "username": "u1", "password": "password123", "org_name": "Team One", "org_slug": "team-one"}
	user := c.expect("POST", "/api/v1/auth/register", "", reg, 201)["user"].(map[string]interface{})
	if user["status"] != "dormant" || user["is_active"] != false {
		t.Fatalf("unexpected registered user: %v", user)
	}
	reg["email"], reg["username"] = "u2@example.com", "u2"
	if d := c.expect("POST", "/api/v1/auth/register", "", reg, 400)["detail"]; d != "Organization slug 'team-one' already exists" {
		t.Fatalf("slug conflict detail: %v", d)
	}
	reg["org_slug"] = "Bad_Slug"
	c.expect("POST", "/api/v1/auth/register", "", reg, 422)

	form := url.Values{"username": {"u1"}, "password": {"password123"}}
	if d := c.expect("POST", "/api/v1/auth/token/form", "", form, 401)["detail"]; d != "User account is not active" {
		t.Fatalf("inactive login detail: %v", d)
	}
	actToken, _ := common.CreateActivationToken(user["uuid"].(string))
	c.expect("GET", "/api/v1/auth/activate?token="+actToken, "", nil, 200)
	if m := c.expect("GET", "/api/v1/auth/activate?token="+actToken, "", nil, 200)["message"]; m != "Account already activated" {
		t.Fatalf("activation idempotency: %v", m)
	}
	c.expect("POST", "/api/v1/auth/token/form", "", url.Values{"username": {"u1"}, "password": {"wrong"}}, 401)

	// Admin registers a region pointing at the fake backend and brings it online.
	adminTok := c.expect("POST", "/api/v1/auth/token/form", "", url.Values{"username": {"admin"}, "password": {"changeme"}}, 200)["access_token"].(string)
	if list := c.expectList("GET", "/api/v1/regions", "", 200); len(list) != 0 {
		t.Fatalf("region list should be public and empty: %v", list)
	}
	region := c.expect("POST", "/api/v1/regions", adminTok, map[string]interface{}{"name": "r1", "internal_endpoint": backend.URL}, 201)
	if region["internal_secret"] == "" || region["is_available"] != false {
		t.Fatalf("unexpected region: %v", region)
	}
	regionUUID := region["uuid"].(string)
	c.expect("PATCH", "/api/v1/regions/"+regionUUID, adminTok, map[string]interface{}{"is_available": true}, 200)

	login := c.expect("POST", "/api/v1/auth/token", "", map[string]interface{}{"username": "u1", "password": "password123"}, 200)
	userTok, orgUUID := login["access_token"].(string), login["org_uuid"].(string)
	if login["region"] != regionUUID {
		t.Fatalf("login region: %v", login)
	}
	me := c.expect("GET", "/api/v1/auth/me", userTok, nil, 200)
	if me["is_superuser"] != false || me["status"] != "active" || me["current_org_uuid"] != orgUUID {
		t.Fatalf("unexpected /me: %v", me)
	}
	if orgs := c.expectList("GET", "/api/v1/auth/me/orgs", userTok, 200); len(orgs) != 1 || orgs[0].(map[string]interface{})["is_current"] != true {
		t.Fatalf("unexpected /me/orgs: %v", orgs)
	}
	if orgs := c.expectList("GET", "/api/v1/auth/me/orgs", adminTok, 200); len(orgs) != 2 {
		t.Fatalf("admin should see all orgs: %v", orgs)
	}

	// SystemAdmin-only operations.
	for _, p := range []struct{ method, path string }{
		{"PATCH", "/api/v1/orgs/" + orgUUID + "/status"},
		{"POST", "/api/v1/orgs/" + orgUUID + "/transfer-owner"},
		{"DELETE", "/api/v1/orgs/" + orgUUID},
		{"GET", "/api/v1/hypers"},
	} {
		if code, body := c.do(p.method, p.path, userTok, map[string]interface{}{"status": 2, "new_owner_uuid": "x"}); code != 403 {
			t.Fatalf("%s %s should be 403, got %d %v", p.method, p.path, code, body)
		}
	}
	c.expect("POST", "/api/v1/orgs", userTok, map[string]interface{}{"name": "x", "slug": "x-org"}, 403)

	// Invitation flow; member operations are keyed by user uuid.
	inv := c.expect("POST", "/api/v1/orgs/"+orgUUID+"/invitations", userTok, map[string]interface{}{"email": "inv@example.com", "org_role": 2}, 201)
	if inv["status"].(float64) != 0 || inv["inviter_email"] != "u1@example.com" {
		t.Fatalf("unexpected invitation: %v", inv)
	}
	if members := c.expectList("GET", "/api/v1/orgs/"+orgUUID+"/members", userTok, 200); len(members) != 2 {
		t.Fatalf("members should include the pending invitation: %v", members)
	}
	var member model.Member
	db.Where("uuid = ?", inv["uuid"]).First(&member)
	info := c.expect("GET", "/api/v1/auth/invitation/info?token="+url.QueryEscape(*member.InvitationToken), "", nil, 200)
	if info["is_existing_user"] != false || info["org_name"] != "Team One" {
		t.Fatalf("unexpected invitation info: %v", info)
	}
	acc := c.expect("POST", "/api/v1/auth/invitation/accept", "", map[string]interface{}{"token": *member.InvitationToken, "username": "invitee", "password": "password123"}, 200)
	if acc["is_new_user"] != true {
		t.Fatalf("unexpected accept: %v", acc)
	}
	var invitee model.User
	db.Where("username = ?", "invitee").First(&invitee)
	c.expect("PATCH", "/api/v1/orgs/"+orgUUID+"/members/"+invitee.UUID, userTok, map[string]interface{}{"org_role": 1}, 200)

	// PUT /users/:uuid (web "edit user"): role is the org role in the caller's current org,
	// username/email changes are SystemAdmin-only and unchanged values are ignored.
	for _, m := range c.expectList("GET", "/api/v1/orgs/"+orgUUID+"/members", userTok, 200) {
		if mm := m.(map[string]interface{}); mm["user_uuid"] == invitee.UUID && mm["username"] != "invitee" {
			t.Fatalf("member list should expose the real username: %v", mm)
		}
	}
	userPath := "/api/v1/users/" + invitee.UUID
	c.expect("PUT", userPath, userTok, map[string]interface{}{"username": "invitee", "email": "inv@example.com", "role": "admin"}, 200)
	var membership model.Member
	db.Where("user_id = ? AND org_id = ?", invitee.ID, member.OrgID).First(&membership)
	if membership.OrgRole != model.OrgRoleAdmin {
		t.Fatalf("org role should be admin, got %d", membership.OrgRole)
	}
	c.expect("PUT", userPath, userTok, map[string]interface{}{"username": "renamed"}, 403)
	c.expect("PUT", "/api/v1/users/"+user["uuid"].(string), userTok, map[string]interface{}{"role": "owner"}, 200)
	c.expect("PUT", userPath, adminTok, map[string]interface{}{"username": "u1"}, 400)
	c.expect("PUT", userPath, adminTok, map[string]interface{}{"username": "invitee2", "email": "inv2@example.com"}, 200)
	var renamed model.User
	db.Where("id = ?", invitee.ID).First(&renamed)
	if renamed.Username != "invitee2" || renamed.Email != "inv2@example.com" {
		t.Fatalf("admin update not applied: %s %s", renamed.Username, renamed.Email)
	}

	// Proxy keeps the resource prefix and enforces quota.
	var org model.Organization
	db.Where("uuid = ?", orgUUID).First(&org)
	var cons model.OrgResourceConsumption
	c.expect("GET", "/api/v1/instances/abc", userTok, nil, 200)
	c.expect("POST", "/api/v1/instances", userTok, map[string]interface{}{"cpu": 2, "memory": 2048, "disk": 10}, 201)
	db.Where("org_id = ?", org.ID).First(&cons)
	if cons.CPUCores != 2 || cons.RAMGB != 2 || cons.DiskGB != 10 {
		t.Fatalf("quota not reserved: %+v", cons)
	}
	quota := c.expect("POST", "/api/v1/instances", userTok, map[string]interface{}{"cpu": 4, "memory": 1024}, 429)
	if detail := quota["detail"].(map[string]interface{}); detail["error"] != "quota_exceeded" || detail["resource"] != "cpu_cores" {
		t.Fatalf("unexpected quota error: %v", quota)
	}
	c.expect("DELETE", "/api/v1/instances/abc", userTok, nil, 204)
	db.Where("org_id = ?", org.ID).First(&cons)
	if cons.CPUCores != 0 || cons.DiskGB != 0 {
		t.Fatalf("quota not released: %+v", cons)
	}
	c.expect("POST", "/api/v1/instances", userTok, map[string]interface{}{"flavor": "f1", "hypervisor": 3}, 403)

	// A failed org switch keeps the old token; a successful one revokes it.
	c.expect("POST", "/api/v1/auth/switch-org", userTok, map[string]interface{}{"org_uuid": "missing"}, 404)
	c.expect("GET", "/api/v1/auth/me", userTok, nil, 200)
	sw := c.expect("POST", "/api/v1/auth/switch-org", userTok, map[string]interface{}{"org_uuid": orgUUID}, 200)
	c.expect("GET", "/api/v1/auth/me", userTok, nil, 401)
	userTok = sw["access_token"].(string)

	// Notification channels: SSRF validation, disabled flag persisted, secret masked.
	c.expect("POST", "/api/v1/notification-channels", userTok, map[string]interface{}{"name": "n", "type": "webhook", "config": map[string]interface{}{"url": "http://10.0.0.1/hook"}}, 422)
	ch := c.expect("POST", "/api/v1/notification-channels", userTok, map[string]interface{}{"name": "n", "type": "feishu", "enabled": false, "config": map[string]interface{}{"webhook_url": "https://open.feishu.cn/x", "secret": "s3cr3t"}}, 201)
	if ch["enabled"] != false || ch["config"].(map[string]interface{})["secret"] != "******" {
		t.Fatalf("unexpected channel: %v", ch)
	}

	// Settings: flat payload, unknown keys ignored, masked secrets not stored, values JSON-encoded.
	settings := c.expect("PUT", "/api/v1/system/settings", adminTok, map[string]interface{}{"SMTP_HOST": "smtp.example.com", "FEISHU_SECRET": "******", "UNKNOWN": 1}, 200)
	if len(settings["settings"].([]interface{})) != len(services.SettingsMetadata) {
		t.Fatalf("unexpected settings list: %v", settings)
	}
	var stored model.SystemSetting
	db.Where("key = ?", "SMTP_HOST").First(&stored)
	if *stored.Value != `"smtp.example.com"` {
		t.Fatalf("setting should be JSON encoded, got %s", *stored.Value)
	}
	var secretCount int64
	db.Model(&model.SystemSetting{}).Where("key = ?", "FEISHU_SECRET").Count(&secretCount)
	if secretCount != 0 {
		t.Fatal("masked secret must not be stored")
	}

	// Region deletion requires maintenance mode and zero consumption (public IPs included).
	regionPath := "/api/v1/regions/" + regionUUID
	c.expect("DELETE", regionPath, adminTok, nil, 400)
	c.expect("PATCH", regionPath, adminTok, map[string]interface{}{"maintenance_mode": true}, 200)
	db.Model(&model.OrgResourceConsumption{}).Where("org_id = ?", org.ID).Update("public_ips", 1)
	if d := c.expect("DELETE", regionPath, adminTok, nil, 400)["detail"].(string); !strings.Contains(d, "active resources") {
		t.Fatalf("public IP usage must block region deletion: %v", d)
	}
	db.Model(&model.OrgResourceConsumption{}).Where("org_id = ?", org.ID).Update("public_ips", 0)
	c.expect("DELETE", regionPath, adminTok, nil, 204)
	var quotaCount int64
	db.Model(&model.OrgResourceQuota{}).Count(&quotaCount)
	if quotaCount != 0 {
		t.Fatalf("region quotas should be removed, %d left", quotaCount)
	}

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	// 转发到 Region 的业务请求携带 W3C trace 上下文
	tracedRequests := traced // 上方已持有 mu
	if len(tracedRequests) == 0 {
		t.Fatal("no proxied instance requests captured")
	}
	for _, entry := range tracedRequests {
		if !strings.Contains(entry, "traceparent=00-") {
			t.Fatalf("proxied request missing traceparent: %s", entry)
		}
	}

	joined := strings.Join(seen, "\n")
	for _, want := range []string{"GET /api/v1/instances/abc org=", "POST /api/v1/instances org=", "DELETE /api/v1/instances/abc org=", "POST /api/v1/internal/orgs/sync"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("backend did not see %q; got:\n%s", want, joined)
		}
	}
}
