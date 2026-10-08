package apis

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
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

	"cpgateway/src/common"
	"cpgateway/src/dbs"
	"cpgateway/src/model"
	"cpgateway/src/services"
	"cpgateway/src/tracing"
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
	// Sync pushes received by the region, in order: "sync <uuid> <name> <org_type>", "delete <uuid>",
	// "settings" and "channels <action>"
	var orgPushes []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var push struct {
			UUID        string `json:"uuid"`
			Name        string `json:"name"`
			OrgType     int    `json:"org_type"`
			Action      string `json:"action"`
			ChannelUUID string `json:"channel_uuid"`
		}
		switch r.URL.Path {
		case "/api/v1/internal/orgs/sync", "/api/v1/internal/orgs/delete", "/api/v1/internal/notification-channels/sync":
			json.NewDecoder(r.Body).Decode(&push)
		}
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" org="+r.Header.Get("X-Org-UUID"))
		if (r.Method == "POST" && r.URL.Path == "/api/v1/instances") || r.Method == "DELETE" {
			traced = append(traced, r.Method+" "+r.URL.Path+" traceparent="+r.Header.Get("traceparent"))
		}
		switch r.URL.Path {
		case "/api/v1/internal/orgs/sync":
			orgPushes = append(orgPushes, fmt.Sprintf("sync %s %s %d", push.UUID, push.Name, push.OrgType))
		case "/api/v1/internal/orgs/delete":
			orgPushes = append(orgPushes, "delete "+push.UUID)
		case "/api/v1/internal/system-settings/sync":
			orgPushes = append(orgPushes, "settings")
		case "/api/v1/internal/notification-channels/sync":
			if push.ChannelUUID != "" {
				orgPushes = append(orgPushes, "channels "+push.Action+" "+push.ChannelUUID)
			} else {
				orgPushes = append(orgPushes, "channels "+push.Action)
			}
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

	// Org pushes run in goroutines: wait until the region has received n pushes equal to want
	waitOrgPush := func(want string, n int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			mu.Lock()
			count := 0
			for _, p := range orgPushes {
				if p == want {
					count++
				}
			}
			all := strings.Join(orgPushes, "\n")
			mu.Unlock()
			if count >= n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("region did not receive %d x %q; got:\n%s", n, want, all)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

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
	if page := c.expect("GET", "/api/v1/regions", "", nil, 200); page["total"] != float64(0) {
		t.Fatalf("region list should be public and empty: %v", page)
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
	// The member list is paginated: {total, members}
	members := c.expect("GET", "/api/v1/orgs/"+orgUUID+"/members", userTok, nil, 200)
	if list, _ := members["members"].([]interface{}); len(list) != 2 || members["total"] != float64(2) {
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
	memberList, _ := c.expect("GET", "/api/v1/orgs/"+orgUUID+"/members", userTok, nil, 200)["members"].([]interface{})
	foundInvitee := false
	for _, m := range memberList {
		if mm := m.(map[string]interface{}); mm["user_uuid"] == invitee.UUID {
			foundInvitee = true
			if mm["username"] != "invitee" {
				t.Fatalf("member list should expose the real username: %v", mm)
			}
		}
	}
	if !foundInvitee {
		t.Fatalf("member list should contain the invitee: %v", memberList)
	}
	userPath := "/api/v1/users/" + invitee.UUID
	c.expect("PUT", userPath, userTok, map[string]interface{}{"username": "invitee", "email": "inv@example.com", "role": "admin"}, 200)
	var membership model.Member
	db.Where("user_id = ? AND org_id = ?", invitee.ID, member.OrgID).First(&membership)
	if membership.OrgRole != model.OrgRoleAdmin {
		t.Fatalf("org role should be admin, got %d", membership.OrgRole)
	}
	// 用户名创建后不可更改，对任何角色都一样：它是账号的永久标识，审计记录以它指代这个人
	c.expect("PUT", userPath, userTok, map[string]interface{}{"username": "renamed"}, 400)
	c.expect("PUT", "/api/v1/users/"+user["uuid"].(string), userTok, map[string]interface{}{"role": "owner"}, 200)
	c.expect("PUT", userPath, adminTok, map[string]interface{}{"username": "u1"}, 400)
	// 邮箱仍可由系统管理员修改；用户名传原值表示不变，不触发拒绝
	c.expect("PUT", userPath, adminTok, map[string]interface{}{"username": "invitee", "email": "inv2@example.com"}, 200)
	var renamed model.User
	db.Where("id = ?", invitee.ID).First(&renamed)
	if renamed.Username != "invitee" || renamed.Email != "inv2@example.com" {
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

	// Org changes reach the regions. Bringing the region online pushed every org, the system org with its type
	// (the region links it to its own system org).
	var systemOrg model.Organization
	db.Where("org_type = ?", model.OrgSystem).First(&systemOrg)
	waitOrgPush(fmt.Sprintf("sync %s %s 2", systemOrg.UUID, systemOrg.Name), 1)
	waitOrgPush("sync "+orgUUID+" Team One 1", 1)

	// A self-registered org is pushed at once, not only by a later region provisioning
	reg3 := map[string]interface{}{"email": "u3@example.com", "username": "u3", "password": "password123", "org_name": "Team Three", "org_slug": "team-three"}
	c.expect("POST", "/api/v1/auth/register", "", reg3, 201)
	var pending3 model.Organization
	db.Where("slug = ?", "team-three").First(&pending3)
	waitOrgPush("sync "+pending3.UUID+" Team Three 1", 1)
	// Registering again before activation discards the pending org: the region is told to delete it
	reg3["org_slug"], reg3["org_name"] = "team-three-b", "Team Three B"
	c.expect("POST", "/api/v1/auth/register", "", reg3, 201)
	var org3 model.Organization
	db.Where("slug = ?", "team-three-b").First(&org3)
	waitOrgPush("delete "+pending3.UUID, 1)
	waitOrgPush("sync "+org3.UUID+" Team Three B 1", 1)
	// Approval pushes the org again (the push at registration may have failed)
	c.expect("PATCH", "/api/v1/orgs/"+org3.UUID+"/status", adminTok, map[string]interface{}{"status": 1}, 200)
	waitOrgPush("sync "+org3.UUID+" Team Three B 1", 2)
	// Renaming too
	c.expect("PATCH", "/api/v1/orgs/"+org3.UUID, adminTok, map[string]interface{}{"name": "Team Three C"}, 200)
	waitOrgPush("sync "+org3.UUID+" Team Three C 1", 1)
	// An org that still uses resources in a region cannot be deleted: the region would refuse and keep them
	// under an org that no longer exists. Nothing is pushed.
	db.Model(&model.OrgResourceConsumption{}).Where("org_id = ?", org3.ID).Updates(map[string]interface{}{"vpcs": 1, "public_ips": 2})
	refused := c.expect("DELETE", "/api/v1/orgs/"+org3.UUID, adminTok, nil, 400)
	if d, _ := refused["detail"].(string); !strings.Contains(d, "active resources (public_ips, vpcs)") || !strings.Contains(d, "log in to the organization again") {
		t.Fatalf("deleting an org in use: %v", refused)
	}
	c.expect("GET", "/api/v1/orgs/"+org3.UUID, adminTok, nil, 200)
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	for _, p := range orgPushes {
		if p == "delete "+org3.UUID {
			mu.Unlock()
			t.Fatal("a refused deletion must not be pushed to the regions")
		}
	}
	mu.Unlock()
	// Deleting an org tells the regions, so its slug cannot hand its resources to a new org there
	db.Model(&model.OrgResourceConsumption{}).Where("org_id = ?", org3.ID).Updates(map[string]interface{}{"vpcs": 0, "public_ips": 0})
	org3Channel := model.NotificationChannel{UUID: "c0ffee00-0000-4000-8000-000000000003", OrgID: org3.ID, Name: "org3-hook",
		Type: "webhook", Config: `{"url":"https://example.com/hook"}`, Enabled: true}
	if err := db.Create(&org3Channel).Error; err != nil {
		t.Fatalf("create org3 channel: %v", err)
	}
	c.expect("DELETE", "/api/v1/orgs/"+org3.UUID, adminTok, nil, 204)
	waitOrgPush("delete "+org3.UUID, 1)
	// The deleted org's channels go with it (nobody can open the org to delete them any more), regions included
	waitOrgPush("channels delete "+org3Channel.UUID, 1)
	var org3Channels int64
	db.Model(&model.NotificationChannel{}).Where("org_id = ?", org3.ID).Count(&org3Channels)
	if org3Channels != 0 {
		t.Fatalf("the deleted org still has %d notification channels", org3Channels)
	}
	c.expect("GET", "/api/v1/orgs/"+org3.UUID, adminTok, nil, 404)
	// A region that is provisioned again (brought back online, heartbeat recovery) is told about the deleted
	// orgs as well. Order: deleted orgs (a reused slug is free when its new org arrives), live orgs, then
	// settings and channels (channels are pushed with their org's UUID, so the org has to be there first).
	mu.Lock()
	start := len(orgPushes)
	mu.Unlock()
	c.expect("PATCH", "/api/v1/regions/"+regionUUID, adminTok, map[string]interface{}{"is_available": false}, 200)
	c.expect("PATCH", "/api/v1/regions/"+regionUUID, adminTok, map[string]interface{}{"is_available": true}, 200)
	waitOrgPush("delete "+org3.UUID, 2)
	waitOrgPush("delete "+pending3.UUID, 2)
	waitOrgPush("sync "+orgUUID+" Team One 1", 3)
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		provisioned := append([]string(nil), orgPushes[start:]...)
		mu.Unlock()
		if len(provisioned) > 0 && provisioned[len(provisioned)-1] == "channels bulk_sync" {
			rank := func(p string) int {
				switch {
				case strings.HasPrefix(p, "delete "):
					return 0
				case strings.HasPrefix(p, "sync "):
					return 1
				case p == "settings":
					return 2
				}
				return 3
			}
			for i := 1; i < len(provisioned); i++ {
				if rank(provisioned[i]) < rank(provisioned[i-1]) {
					t.Fatalf("provisioning order must be deleted orgs, live orgs, settings, channels; got:\n%s", strings.Join(provisioned, "\n"))
				}
			}
			if !strings.Contains(strings.Join(provisioned, "\n"), "settings") {
				t.Fatalf("provisioning did not push the settings: %v", provisioned)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("provisioning did not finish with the channels; got:\n%s", strings.Join(provisioned, "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The admin lists sort on the server: descending is the reverse of ascending (whatever the collation),
	// an unknown field keeps the default order
	listNames := func(path, key, field string) []string {
		t.Helper()
		list, _ := c.expect("GET", path, adminTok, nil, 200)[key].([]interface{})
		names := make([]string, 0, len(list))
		for _, item := range list {
			names = append(names, item.(map[string]interface{})[field].(string))
		}
		return names
	}
	checkOrder := func(path, key, field string) {
		t.Helper()
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		asc := listNames(path+sep+"order="+field, key, field)
		desc := listNames(path+sep+"order=-"+field, key, field)
		if len(asc) < 2 || len(desc) != len(asc) {
			t.Fatalf("%s: need two rows to check the order: %v %v", path, asc, desc)
		}
		for i := range asc {
			if asc[i] != desc[len(desc)-1-i] {
				t.Fatalf("%s order=%s: asc %v, desc %v", path, field, asc, desc)
			}
		}
		if bogus, byID := listNames(path+sep+"order=id%20DESC", key, field), listNames(path, key, field); strings.Join(bogus, ",") != strings.Join(byID, ",") {
			t.Fatalf("%s: an unknown order field must keep the default order: %v vs %v", path, bogus, byID)
		}
	}
	checkOrder("/api/v1/orgs", "orgs", "name")
	checkOrder("/api/v1/users", "users", "username")
	checkOrder("/api/v1/orgs/"+orgUUID+"/members", "members", "username")

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
	for _, want := range []string{"GET /api/v1/instances/abc org=" + orgUUID, "POST /api/v1/instances org=" + orgUUID, "DELETE /api/v1/instances/abc org=" + orgUUID, "POST /api/v1/internal/orgs/sync"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("backend did not see %q; got:\n%s", want, joined)
		}
	}
}
