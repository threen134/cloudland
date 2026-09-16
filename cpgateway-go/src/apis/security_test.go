package apis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/spf13/viper"

	"cpgateway-go/src/common"
	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
	"cpgateway-go/src/services"
)

// secEnv is an isolated fixture for the quota/authorization regression tests: its own fake region
// backend plus users and orgs created directly in the shared test database under unique names.
type secEnv struct {
	t      *testing.T
	c      testClient
	region model.Region
	suffix string

	mu   sync.Mutex
	seen []string
}

func newSecEnv(t *testing.T, handler http.HandlerFunc) *secEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	writeTestKeys(t)
	viper.Set("auth.secret_key", "test-secret")
	viper.Set("superuser.email", "admin@example.com")
	viper.Set("superuser.username", "admin")
	viper.Set("superuser.password", "changeme")
	dbs.DB()
	services.Init()

	e := &secEnv{t: t, suffix: strings.ReplaceAll(uuid.New().String(), "-", "")[:8]}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.seen = append(e.seen, r.Method+" "+r.URL.Path)
		e.mu.Unlock()
		if handler == nil {
			// List endpoints fail so login-triggered consumption sync never overwrites test data.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		handler(w, r)
	}))
	e.region = model.Region{Name: "sec-" + e.suffix, InternalEndpoint: backend.URL, InternalSecret: "secret", IsAvailable: true}
	if err := dbs.DB().Create(&e.region).Error; err != nil {
		t.Fatalf("create region: %v", err)
	}
	t.Cleanup(func() {
		dbs.DB().Model(&model.Region{}).Where("id = ?", e.region.ID).Update("is_available", false)
		backend.Close()
	})
	e.c = testClient{t, Routes()}
	return e
}

func (e *secEnv) name(prefix string) string {
	return prefix + "-" + e.suffix
}

// user creates an active user with password "password123".
func (e *secEnv) user(prefix string) *model.User {
	e.t.Helper()
	hashed, err := common.HashPassword("password123")
	if err != nil {
		e.t.Fatal(err)
	}
	u := model.User{Email: e.name(prefix) + "@example.com", Username: e.name(prefix), HashedPassword: hashed,
		Language: "en", IsActive: true, Status: model.UserActive}
	if err := dbs.DB().Create(&u).Error; err != nil {
		e.t.Fatalf("create user: %v", err)
	}
	return &u
}

// org creates an active org owned by owner (ADMIN member) with quota cpu=8, ram=16, ips=4, disk=100,
// vpcs=1, load_balancers=1, images=1 in the test region.
func (e *secEnv) org(prefix string, owner *model.User) *model.Organization {
	e.t.Helper()
	db := dbs.DB()
	o := model.Organization{Name: e.name(prefix), Slug: e.name(prefix), OrgType: model.OrgTeam, OwnerUserID: owner.ID, Status: model.OrgActive}
	if err := db.Create(&o).Error; err != nil {
		e.t.Fatalf("create org: %v", err)
	}
	if err := db.Create(&model.OrgResourceQuota{OrgID: o.ID, RegionID: e.region.ID,
		MaxCPUCores: 8, MaxRAMGB: 16, MaxPublicIPs: 4, MaxDiskGB: 100,
		MaxVPCs: 1, MaxLoadBalancers: 1, MaxImages: 1}).Error; err != nil {
		e.t.Fatalf("create quota: %v", err)
	}
	if err := db.Create(&model.OrgResourceConsumption{OrgID: o.ID, RegionID: e.region.ID}).Error; err != nil {
		e.t.Fatalf("create consumption: %v", err)
	}
	e.member(owner, &o, model.OrgRoleAdmin, nil)
	return &o
}

// member adds a membership row; a non-nil status makes it an invitation row in that state.
func (e *secEnv) member(u *model.User, o *model.Organization, role model.OrgRole, status *model.InvitationStatus) *model.Member {
	e.t.Helper()
	m := model.Member{UserID: u.ID, OrgID: o.ID, OrgRole: role, InvitationStatus: status}
	if status != nil {
		token := uuid.New().String()
		m.InvitationToken = &token
	}
	if err := dbs.DB().Create(&m).Error; err != nil {
		e.t.Fatalf("create member: %v", err)
	}
	return &m
}

func (e *secEnv) root() *model.User {
	e.t.Helper()
	var u model.User
	if err := dbs.DB().Where("username = ?", "admin").First(&u).Error; err != nil {
		e.t.Fatalf("root user: %v", err)
	}
	return &u
}

// token mints an access token directly, so claims (e.g. org role) can be stale or forged.
func (e *secEnv) token(u *model.User, o *model.Organization, role model.OrgRole) string {
	e.t.Helper()
	orgUUID, orgName, isOwner := "", "", false
	if o != nil {
		orgUUID, orgName, isOwner = o.UUID, o.Name, o.OwnerUserID == u.ID
	}
	tok, _, err := common.CreateAccessToken(u.UUID, u.Email, orgUUID, orgName, e.region.UUID,
		int(u.SystemRole), int(role), int(u.Status), isOwner)
	if err != nil {
		e.t.Fatalf("create token: %v", err)
	}
	return tok
}

func (e *secEnv) consumption(o *model.Organization) model.OrgResourceConsumption {
	e.t.Helper()
	var cons model.OrgResourceConsumption
	if err := dbs.DB().Where("org_id = ? AND region_id = ?", o.ID, e.region.ID).First(&cons).Error; err != nil {
		e.t.Fatalf("consumption: %v", err)
	}
	return cons
}

func (e *secEnv) expectConsumption(step string, o *model.Organization, cpu, ram, disk float64) {
	e.t.Helper()
	if cons := e.consumption(o); cons.CPUCores != cpu || cons.RAMGB != ram || cons.DiskGB != disk {
		e.t.Fatalf("%s: expected cpu=%v ram=%v disk=%v, got %+v", step, cpu, ram, disk, cons)
	}
}

func (e *secEnv) expectDetail(method, path, token string, body interface{}, status int, detail string) {
	e.t.Helper()
	if d := e.c.expect(method, path, token, body, status)["detail"]; d != detail {
		e.t.Fatalf("%s %s: expected detail %q, got %v", method, path, detail, d)
	}
}

func (e *secEnv) countSeen(entry string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, s := range e.seen {
		if s == entry {
			n++
		}
	}
	return n
}

// Issue 1: quota settlement depends only on the backend's answer, not on the client connection.
func TestProxyQuotaSettlementIgnoresClientDisconnect(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	arrived := make(chan struct{}, 1)
	e := newSecEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/instances":
			arrived <- struct{}{}
			<-release
			w.Write([]byte(`[{"id":"i1"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/volumes":
			// The status line arrives, then the connection breaks in the middle of the body.
			w.Header().Set("Content-Length", "64")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id"`))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	defer releaseOnce.Do(func() { close(release) })

	owner := e.user("owner")
	org := e.org("org", owner)
	tok := e.token(owner, org, model.OrgRoleAdmin)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw, _ := json.Marshal(map[string]interface{}{"cpu": 2, "memory": 2048, "disk": 10})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		e.c.r.ServeHTTP(w, req)
		close(done)
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("create request never reached the backend")
	}
	cancel() // client disconnects while clapi is still working
	time.Sleep(200 * time.Millisecond)
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not finish")
	}
	e.expectConsumption("client disconnected, backend succeeded", org, 2, 2, 10)

	if code, body := e.c.do(http.MethodPost, "/api/v1/volumes", tok, map[string]interface{}{"size": 5}); code != http.StatusBadGateway {
		t.Fatalf("truncated backend body should be 502, got %d %v", code, body)
	}
	e.expectConsumption("backend 201 with truncated body", org, 2, 2, 15)
}

// Issue 2: resize quota applies to POST /instances/:id/resize and POST /volumes/:id/resize.
func TestProxyResizeQuota(t *testing.T) {
	for _, tc := range []struct{ method, template, want string }{
		{"POST", "/instances/{id}/resize", "resize"},
		{"POST", "/volumes/{id}/resize", "resize"},
		{"PUT", "/instances/{id}", ""},
		{"PUT", "/volumes/{id}", ""},
		{"PATCH", "/instances/{id}", ""},
		{"DELETE", "/instances/{id}/interfaces/{interface_id}", ""},
		{"DELETE", "/instances/{id}", "release"},
		{"POST", "/instances", "consume"},
		{"POST", "/floating_ips/batch_attach", ""},
		{"POST", "/vpcs", "consume"},
		{"DELETE", "/vpcs/{id}", "release"},
		{"POST", "/load_balancers", "consume"},
		{"DELETE", "/load_balancers/{id}", "release"},
		{"POST", "/images", "consume"},
		{"DELETE", "/images/{id}", "release"},
		// Load balancer floating IPs count as public IPs, not as load balancers
		{"POST", "/load_balancers/{id}/floating_ips", "consume"},
		{"DELETE", "/load_balancers/{id}/floating_ips/{floating_ip_id}", "release"},
		{"GET", "/load_balancers/{id}/floating_ips", ""},
		{"PATCH", "/vpcs/{id}", ""},
	} {
		if got := services.MatchQuotaRule(tc.method, tc.template); got != tc.want {
			t.Fatalf("MatchQuotaRule(%s %s) = %q, want %q", tc.method, tc.template, got, tc.want)
		}
	}
	if id := services.ExtractResourceID("/instances/i1/resize"); id != "i1" {
		t.Fatalf("ExtractResourceID: %q", id)
	}

	e := newSecEnv(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/instances/"):
			w.Write([]byte(`{"cpu":2,"memory":2048,"disk":10}`))
		case r.Method == http.MethodGet && p == "/api/v1/volumes/v1":
			w.Write([]byte(`{"size":10}`))
		case r.Method == http.MethodPost && p == "/api/v1/instances/fail/resize":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"Failed to resize instance"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/resize"):
			w.Write([]byte(`null`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	owner := e.user("owner")
	org := e.org("org", owner)
	tok := e.token(owner, org, model.OrgRoleAdmin)
	dbs.DB().Model(&model.OrgResourceConsumption{}).Where("org_id = ? AND region_id = ?", org.ID, e.region.ID).
		Updates(map[string]interface{}{"cpu_cores": 2, "ram_gb": 2, "disk_gb": 20})

	e.c.expect("POST", "/api/v1/instances/i1/resize", tok, map[string]interface{}{"cpu": 4}, 200)
	e.expectConsumption("cpu 2->4, memory omitted", org, 4, 2, 20)
	e.c.expect("POST", "/api/v1/instances/i1/resize", tok, map[string]interface{}{"memory": 1024}, 200)
	e.expectConsumption("memory 2048->1024 released", org, 4, 1, 20)
	quota := e.c.expect("POST", "/api/v1/instances/i1/resize", tok, map[string]interface{}{"cpu": 16}, 429)
	if detail, _ := quota["detail"].(map[string]interface{}); detail["resource"] != "cpu_cores" {
		t.Fatalf("unexpected quota error: %v", quota)
	}
	e.expectConsumption("over quota", org, 4, 1, 20)
	e.c.expect("POST", "/api/v1/instances/fail/resize", tok, map[string]interface{}{"cpu": 6}, 400)
	e.expectConsumption("backend rejected resize", org, 4, 1, 20)
	e.c.expect("POST", "/api/v1/volumes/v1/resize", tok, map[string]interface{}{"size": 30}, 200)
	e.expectConsumption("volume 10->30", org, 4, 1, 40)
	if e.countSeen("POST /api/v1/instances/i1/resize") != 2 || e.countSeen("POST /api/v1/volumes/v1/resize") != 1 {
		t.Fatalf("backend saw unexpected resize requests: %v", e.seen)
	}
}

// Issue 3: creating instances reserves quota for every instance in count.
func TestProxyCreateInstanceCountQuota(t *testing.T) {
	e := newSecEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/instances" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var body struct {
			Hostname string `json:"hostname"`
			Count    int    `json:"count"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		n := body.Count
		if n < 1 {
			n = 1
		}
		switch body.Hostname {
		case "fail":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"Failed to create instances"}`))
			return
		case "partial":
			n--
		}
		items := make([]map[string]string, n)
		for i := range items {
			items[i] = map[string]string{"id": fmt.Sprintf("i%d", i)}
		}
		json.NewEncoder(w).Encode(items)
	})
	owner := e.user("owner")
	org := e.org("org", owner)
	tok := e.token(owner, org, model.OrgRoleAdmin)
	create := func(hostname string, count interface{}, status int) {
		t.Helper()
		body := map[string]interface{}{"hostname": hostname, "cpu": 1, "memory": 1024, "disk": 5}
		if count != nil {
			body["count"] = count
		}
		if code, out := e.c.do("POST", "/api/v1/instances", tok, body); code != status {
			t.Fatalf("create %s count=%v: expected %d, got %d %v", hostname, count, status, code, out)
		}
	}

	create("vm", 3, 200)
	e.expectConsumption("count=3", org, 3, 3, 15)
	create("vm", 0, 200)
	e.expectConsumption("count=0 counts as 1", org, 4, 4, 20)
	create("vm", nil, 200)
	e.expectConsumption("count omitted counts as 1", org, 5, 5, 25)
	create("vm", 4, 429)
	e.expectConsumption("count=4 exceeds cpu quota", org, 5, 5, 25)
	create("partial", 3, 200)
	e.expectConsumption("only 2 of 3 created", org, 7, 7, 35)
	create("fail", 1, 400)
	e.expectConsumption("backend rejected create", org, 7, 7, 35)
}

// Count quotas: each VPC, load balancer and private image consumes 1 on create and releases 1 on a successful
// delete of a resource owned by the caller's org; load balancer floating IPs count as public IPs.
func TestProxyCountQuotas(t *testing.T) {
	var ownerUUID string
	e := newSecEnv(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/floating_ips"):
			// Asked for 3 floating IPs, the backend created 2
			w.Write([]byte(`[{"id":"f1"},{"id":"f2"}]`))
		case r.Method == http.MethodPost:
			w.Write([]byte(`{"id":"r1"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/missing"):
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error_message":"not found"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/foreign"):
			w.Write([]byte(`{"id":"foreign","owner_uuid":"another-org"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/load_balancers/"):
			w.Write([]byte(`{"id":"r1","owner_uuid":"` + ownerUUID + `","floating_ips":[{"id":"f1"},{"id":"f2"}]}`))
		case r.Method == http.MethodGet:
			w.Write([]byte(`{"id":"r1","owner_uuid":"` + ownerUUID + `","public":false}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	owner := e.user("owner")
	org := e.org("org", owner)
	ownerUUID = org.UUID
	tok := e.token(owner, org, model.OrgRoleAdmin)

	counts := func() [4]int {
		c := e.consumption(org)
		return [4]int{c.VPCs, c.LoadBalancers, c.Images, c.PublicIPs}
	}
	expect := func(step string, want [4]int) {
		t.Helper()
		if got := counts(); got != want {
			t.Fatalf("%s: expected vpcs/load_balancers/images/public_ips=%v, got %v", step, want, got)
		}
	}

	for i, collection := range []string{"vpcs", "load_balancers", "images"} {
		one := [4]int{}
		one[i] = 1
		if code, out := e.c.do("POST", "/api/v1/"+collection, tok, map[string]interface{}{"name": "a"}); code != 200 {
			t.Fatalf("create %s: expected 200, got %d %v", collection, code, out)
		}
		expect("create "+collection, one)

		// Quota of 1 is used up: the second create is rejected before reaching the backend
		code, out := e.c.do("POST", "/api/v1/"+collection, tok, map[string]interface{}{"name": "b"})
		detail, _ := out.(map[string]interface{})["detail"].(map[string]interface{})
		if code != http.StatusTooManyRequests || detail["resource"] != collection {
			t.Fatalf("second %s create: expected 429 for %s, got %d %v", collection, collection, code, out)
		}
		expect("rejected "+collection, one)

		// A resource the backend cannot find is rejected before the delete and releases nothing
		if code, _ := e.c.do("DELETE", "/api/v1/"+collection+"/missing", tok, nil); code != http.StatusBadRequest {
			t.Fatalf("delete missing %s: expected 400, got %d", collection, code)
		}
		expect("missing "+collection, one)

		// Another org's resource (deleted by a system admin): the delete goes through, the caller's org keeps its count
		if code, _ := e.c.do("DELETE", "/api/v1/"+collection+"/foreign", tok, nil); code != http.StatusNoContent {
			t.Fatalf("delete foreign %s: expected 204, got %d", collection, code)
		}
		expect("foreign "+collection, one)

		if collection == "load_balancers" {
			// 3 floating IPs requested, 2 created: the reservation for the missing one is given back
			if code, out := e.c.do("POST", "/api/v1/load_balancers/r1/floating_ips", tok, map[string]interface{}{"activation_count": 3}); code != 200 {
				t.Fatalf("create lb floating ips: expected 200, got %d %v", code, out)
			}
			expect("lb floating ips", [4]int{0, 1, 0, 2})
			// Deleting the load balancer frees the load balancer and both of its floating IPs
			if code, _ := e.c.do("DELETE", "/api/v1/load_balancers/r1", tok, nil); code != http.StatusNoContent {
				t.Fatalf("delete load balancer: expected 204, got %d", code)
			}
			expect("delete load balancer", [4]int{})
			continue
		}

		if code, _ := e.c.do("DELETE", "/api/v1/"+collection+"/r1", tok, nil); code != http.StatusNoContent {
			t.Fatalf("delete %s: expected 204, got %d", collection, code)
		}
		expect("delete "+collection, [4]int{})
	}
}

// Issue 4: pending/expired/cancelled invitation rows never count as memberships.
func TestInvitationRowsDoNotGrantMembership(t *testing.T) {
	e := newSecEnv(t, nil)
	db := dbs.DB()
	pending, expired, cancelled := model.InvitationPending, model.InvitationExpired, model.InvitationCancelled

	owner := e.user("owner")
	orgA := e.org("org-a", owner)
	invitee := e.user("invitee")
	pm := e.member(invitee, orgA, model.OrgRoleAdmin, &pending)
	db.Model(pm).Update("created_at", time.Now().Add(-time.Hour)) // the invitation is the earliest row
	orgB := e.org("org-b", e.user("owner-b"))
	e.member(invitee, orgB, model.OrgRoleReader, nil)
	expiredUser := e.user("expired")
	e.member(expiredUser, orgA, model.OrgRoleAdmin, &expired)
	cancelledUser := e.user("cancelled")
	e.member(cancelledUser, orgA, model.OrgRoleAdmin, &cancelled)

	for _, u := range []*model.User{invitee, expiredUser, cancelledUser} {
		e.expectDetail("POST", "/api/v1/auth/token", "", map[string]interface{}{
			"username": u.Username, "password": "password123", "org_uuid": orgA.UUID, "region": e.region.UUID,
		}, 403, "User is not a member of this organization")
	}
	login := e.c.expect("POST", "/api/v1/auth/token", "", map[string]interface{}{
		"username": invitee.Username, "password": "password123", "region": e.region.UUID,
	}, 200)
	if login["org_uuid"] != orgB.UUID {
		t.Fatalf("login must pick the first formal membership, got %v", login["org_uuid"])
	}
	tokB := login["access_token"].(string)

	e.expectDetail("POST", "/api/v1/auth/switch-org", tokB, map[string]interface{}{"org_uuid": orgA.UUID}, 403,
		"User is not a member of this organization")
	e.expectDetail("POST", "/api/v1/auth/switch-region", e.token(invitee, orgA, model.OrgRoleAdmin),
		map[string]interface{}{"region": e.region.UUID}, 403, "User is no longer a member of this organization")
	e.c.expect("GET", "/api/v1/orgs/"+orgA.UUID, tokB, nil, 403)
	e.c.expect("GET", "/api/v1/resources/quota/"+orgA.UUID, tokB, nil, 403)
	e.c.expect("GET", "/api/v1/resources/info/"+orgA.UUID, tokB, nil, 403)
	e.c.expect("POST", "/api/v1/orgs/"+orgA.UUID+"/invitations", tokB,
		map[string]interface{}{"email": e.name("x") + "@example.com"}, 403)
	for _, path := range []string{"/api/v1/orgs", "/api/v1/auth/me/orgs"} {
		list := e.c.expectList("GET", path, tokB, 200)
		if len(list) != 1 || list[0].(map[string]interface{})["uuid"] != orgB.UUID {
			t.Fatalf("%s must only list formal memberships: %v", path, list)
		}
	}

	ownerTok := e.token(owner, orgA, model.OrgRoleAdmin)
	if d := e.c.expect("GET", "/api/v1/orgs/"+orgA.UUID, ownerTok, nil, 200); d["member_count"] != float64(1) {
		t.Fatalf("member_count must exclude invitation rows: %v", d)
	}
	memberPath := "/api/v1/orgs/" + orgA.UUID + "/members/" + invitee.UUID
	e.c.expect("PATCH", memberPath, ownerTok, map[string]interface{}{"org_role": 1}, 404)
	e.c.expect("DELETE", memberPath, ownerTok, nil, 404)

	rootTok := e.token(e.root(), nil, model.OrgRoleNone)
	e.expectDetail("POST", "/api/v1/orgs/"+orgA.UUID+"/transfer-owner", rootTok,
		map[string]interface{}{"new_owner_uuid": invitee.UUID}, 400, "New owner must be a member of the organization")

	// A SystemAdmin direct add supersedes the pending invitation instead of hitting the unique index.
	e.c.expect("POST", "/api/v1/orgs/"+orgA.UUID+"/members", rootTok,
		map[string]interface{}{"user_uuid": invitee.UUID, "org_role": 2}, 201)
	var superseded model.Member
	db.Unscoped().Where("id = ?", pm.ID).First(&superseded)
	if !superseded.DeletedAt.Valid || superseded.InvitationStatus == nil || *superseded.InvitationStatus != model.InvitationCancelled {
		t.Fatalf("pending invitation should be cancelled: %+v", superseded)
	}
	e.c.expect("POST", "/api/v1/auth/switch-org", tokB, map[string]interface{}{"org_uuid": orgA.UUID}, 200)
}

// Issue 5: listing members needs membership, listing invitations needs org ADMIN.
func TestOrgMemberAndInvitationListPermissions(t *testing.T) {
	e := newSecEnv(t, nil)
	pending := model.InvitationPending
	owner := e.user("owner")
	org := e.org("org", owner)
	reader := e.user("reader")
	e.member(reader, org, model.OrgRoleReader, nil)
	invitee := e.user("invitee")
	e.member(invitee, org, model.OrgRoleAdmin, &pending)
	outsider := e.user("outsider")

	membersPath := "/api/v1/orgs/" + org.UUID + "/members"
	invitationsPath := "/api/v1/orgs/" + org.UUID + "/invitations"
	for _, tc := range []struct {
		name                 string
		tok                  string
		members, invitations int
	}{
		{"outsider", e.token(outsider, nil, model.OrgRoleNone), 403, 403},
		{"pending invitee", e.token(invitee, org, model.OrgRoleAdmin), 403, 403},
		{"reader", e.token(reader, org, model.OrgRoleReader), 200, 403},
		{"org admin", e.token(owner, org, model.OrgRoleAdmin), 200, 200},
		{"system admin", e.token(e.root(), nil, model.OrgRoleNone), 200, 200},
	} {
		if code, body := e.c.do("GET", membersPath, tc.tok, nil); code != tc.members {
			t.Fatalf("%s: list members expected %d, got %d %v", tc.name, tc.members, code, body)
		}
		code, body := e.c.do("GET", invitationsPath, tc.tok, nil)
		if code != tc.invitations {
			t.Fatalf("%s: list invitations expected %d, got %d %v", tc.name, tc.invitations, code, body)
		}
		if list, _ := body.([]interface{}); code == 200 && len(list) != 1 {
			t.Fatalf("%s: expected the pending invitation, got %v", tc.name, body)
		}
	}
}

// Issue 6: a soft-deleted org is neither carried into a new token nor proxied.
func TestDeletedOrgRejectedBySwitchRegionAndProxy(t *testing.T) {
	e := newSecEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/instances" {
			w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	owner := e.user("owner")
	org := e.org("org", owner)
	root := e.root()
	tok := e.token(owner, org, model.OrgRoleAdmin)
	e.c.expect("GET", "/api/v1/instances", tok, nil, 200)

	e.c.expect("DELETE", "/api/v1/orgs/"+org.UUID, e.token(root, nil, model.OrgRoleNone), nil, 204)
	rootOrgTok := e.token(root, org, model.OrgRoleAdmin)
	for _, tk := range []string{tok, rootOrgTok} {
		e.expectDetail("POST", "/api/v1/auth/switch-region", tk, map[string]interface{}{"region": e.region.UUID}, 404, "Organization not found")
		e.expectDetail("GET", "/api/v1/instances", tk, nil, 403, "Organization is not accessible")
	}
	if n := e.countSeen("GET /api/v1/instances"); n != 1 {
		t.Fatalf("requests for a deleted org must not reach the backend, saw %d", n)
	}
}

// Issue 7: registration can only reuse the unactivated account that owns the email.
func TestRegisterCannotTakeOverAnotherAccount(t *testing.T) {
	e := newSecEnv(t, nil)
	db := dbs.DB()
	register := func(email, username, slug string) map[string]interface{} {
		return map[string]interface{}{
			"email": e.name(email) + "@example.com", "username": e.name(username), "password": "password123",
			"org_name": "Org " + slug, "org_slug": e.name(slug),
		}
	}
	const path = "/api/v1/auth/register"

	victim := e.c.expect("POST", path, "", register("victim", "victim", "victim"), 201)["user"].(map[string]interface{})
	var victimRow model.User
	db.Where("uuid = ?", victim["uuid"]).First(&victimRow)
	var victimOrg model.Organization
	db.Where("owner_user_id = ?", victimRow.ID).First(&victimOrg)

	// Attacker email + the victim's unactivated username must not rewrite the victim's row.
	e.expectDetail("POST", path, "", register("attacker", "victim", "attacker"), 400, "Username already taken")
	var unchanged model.User
	db.Where("id = ?", victimRow.ID).First(&unchanged)
	if unchanged.Email != victimRow.Email || unchanged.HashedPassword != victimRow.HashedPassword {
		t.Fatalf("victim row was modified: %+v", unchanged)
	}
	// Email of inactive user X with the username of inactive user Y.
	e.c.expect("POST", path, "", register("other", "other", "other"), 201)
	e.expectDetail("POST", path, "", register("other", "victim", "other-2"), 400, "Username already taken")

	// The email owner re-registering reuses the row; the earlier pending org is discarded and its slug freed.
	again := register("victim", "victim-new", "victim")
	again["password"] = "newpassword123"
	if reused := e.c.expect("POST", path, "", again, 201)["user"].(map[string]interface{}); reused["uuid"] != victim["uuid"] {
		t.Fatalf("expected the unactivated row to be reused: %v", reused)
	}
	var discarded model.Organization
	db.Unscoped().Where("id = ?", victimOrg.ID).First(&discarded)
	if !discarded.DeletedAt.Valid {
		t.Fatal("earlier pending org should be soft-deleted")
	}
	var discardedMembers int64
	db.Model(&model.Member{}).Where("org_id = ?", victimOrg.ID).Count(&discardedMembers)
	if discardedMembers != 0 {
		t.Fatal("memberships of the discarded org should be removed")
	}

	actToken, _ := common.CreateActivationToken(victimRow.UUID)
	e.c.expect("GET", "/api/v1/auth/activate?token="+actToken, "", nil, 200)
	var owned []model.Organization
	db.Where("owner_user_id = ?", victimRow.ID).Find(&owned)
	if len(owned) != 1 || owned[0].ID == victimOrg.ID || owned[0].Slug != e.name("victim") || owned[0].Status != model.OrgActive {
		t.Fatalf("only the latest org should be activated: %+v", owned)
	}
	db.Unscoped().Where("id = ?", victimOrg.ID).First(&discarded)
	if discarded.Status != model.OrgPending {
		t.Fatal("activation must not touch discarded orgs")
	}
	login := e.c.expect("POST", "/api/v1/auth/token", "", map[string]interface{}{
		"username": e.name("victim-new"), "password": "newpassword123", "region": e.region.UUID,
	}, 200)
	if login["org_uuid"] != owned[0].UUID {
		t.Fatalf("unexpected login org: %v", login)
	}

	e.expectDetail("POST", path, "", register("victim", "brand-new", "brand-new"), 400, "Email already registered")
	e.expectDetail("POST", path, "", register("fresh", "victim-new", "fresh"), 400, "Username already taken")
}

// Issue 8: notification channels require an active user who formally belongs to the org; writes need org ADMIN.
func TestNotificationChannelsRequireOrgMembership(t *testing.T) {
	e := newSecEnv(t, nil)
	db := dbs.DB()
	pending := model.InvitationPending
	owner := e.user("owner")
	org := e.org("org", owner)
	reader := e.user("reader")
	e.member(reader, org, model.OrgRoleReader, nil)
	invitee := e.user("invitee")
	e.member(invitee, org, model.OrgRoleAdmin, &pending)
	outsider := e.user("outsider")
	disabled := e.user("disabled")
	e.member(disabled, org, model.OrgRoleAdmin, nil)
	db.Model(disabled).Update("status", model.UserDisabled)
	inactive := e.user("inactive")
	e.member(inactive, org, model.OrgRoleAdmin, nil)
	db.Model(inactive).Update("is_active", false)
	root := e.root()

	const path = "/api/v1/notification-channels"
	channel := map[string]interface{}{"name": "n", "type": "feishu", "config": map[string]interface{}{"webhook_url": "https://open.feishu.cn/x"}}
	ch := e.c.expect("POST", path, e.token(owner, org, model.OrgRoleAdmin), channel, 201)
	chPath := path + "/" + ch["uuid"].(string)

	// Every token claims org ADMIN: the role must come from the database.
	for _, tc := range []struct {
		name        string
		user        *model.User
		read, write int
	}{
		{"outsider", outsider, 403, 403},
		{"pending invitee", invitee, 403, 403},
		{"disabled member", disabled, 403, 403},
		{"inactive member", inactive, 400, 400},
		{"reader", reader, 200, 403},
		{"org admin", owner, 200, 200},
		{"system admin", root, 200, 200},
	} {
		tok := e.token(tc.user, org, model.OrgRoleAdmin)
		for _, p := range []string{path, chPath} {
			if code, body := e.c.do("GET", p, tok, nil); code != tc.read {
				t.Fatalf("%s: GET %s expected %d, got %d %v", tc.name, p, tc.read, code, body)
			}
		}
		if code, body := e.c.do("PUT", chPath, tok, map[string]interface{}{"name": "renamed"}); code != tc.write {
			t.Fatalf("%s: PUT expected %d, got %d %v", tc.name, tc.write, code, body)
		}
	}
	readerTok := e.token(reader, org, model.OrgRoleAdmin)
	e.expectDetail("POST", path, readerTok, channel, 403, "Not enough permissions")
	e.c.expect("DELETE", chPath, readerTok, nil, 403)
	e.c.expect("POST", path, e.token(root, org, model.OrgRoleNone), channel, 201)
	e.c.expect("DELETE", chPath, e.token(owner, org, model.OrgRoleAdmin), nil, 204)
}
