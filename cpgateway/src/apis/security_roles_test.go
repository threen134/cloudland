package apis

import (
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"cpgateway/src/dbs"
	"cpgateway/src/model"
)

// Regression tests for the permission / org / quota alignment of 2026-10-14
// (test-items/alignment/01-权限组织与配额.md). The file name sorts after parity_test.go on purpose:
// TestPythonParityFlows expects an empty region list, and these tests create regions.

// P1: the identity forwarded to the region comes from the database, so removing or demoting a
// member, or demoting a system admin, takes effect at once even with a token issued before.
func TestProxyForwardsCurrentRoles(t *testing.T) {
	var mu sync.Mutex
	var last http.Header
	e := newSecEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/instances" {
			mu.Lock()
			last = r.Header.Clone()
			mu.Unlock()
			w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	db := dbs.DB()
	owner := e.user("owner")
	org := e.org("org", owner)
	writer := e.user("writer")
	writerMember := e.member(writer, org, model.OrgRoleWriter, nil)
	gone := e.user("gone")
	goneMember := e.member(gone, org, model.OrgRoleAdmin, nil)
	admin2 := e.user("admin2")
	db.Model(admin2).Updates(map[string]interface{}{"system_role": model.SystemAdmin, "is_superuser": true})
	admin2.SystemRole, admin2.IsSuperuser = model.SystemAdmin, true

	forwarded := func(step, tok string) http.Header {
		t.Helper()
		e.c.expect("GET", "/api/v1/instances", tok, nil, 200)
		mu.Lock()
		defer mu.Unlock()
		return last
	}
	expectHeader := func(step string, h http.Header, key, want string) {
		t.Helper()
		if got := h.Get(key); got != want {
			t.Fatalf("%s: %s = %q, want %q", step, key, got, want)
		}
	}

	// Tokens issued before the changes below, each claiming org admin
	writerTok := e.token(writer, org, model.OrgRoleAdmin)
	goneTok := e.token(gone, org, model.OrgRoleAdmin)
	admin2Tok := e.token(admin2, org, model.OrgRoleAdmin)
	ownerTok := e.token(owner, org, model.OrgRoleReader)

	h := forwarded("writer", writerTok)
	expectHeader("writer claiming admin", h, "X-Org-Role", "2")
	expectHeader("writer claiming admin", h, "X-Is-Owner", "false")

	h = forwarded("owner", ownerTok)
	expectHeader("owner claiming reader", h, "X-Org-Role", "3")
	expectHeader("owner claiming reader", h, "X-Is-Owner", "true")

	// Demoted after the token was issued
	db.Model(writerMember).Update("org_role", model.OrgRoleReader)
	expectHeader("demoted writer", forwarded("demoted writer", writerTok), "X-Org-Role", "1")

	// Removed after the token was issued: refused before reaching the region
	db.Delete(goneMember)
	before := e.countSeen("GET /api/v1/instances")
	e.expectDetail("GET", "/api/v1/instances", goneTok, nil, 403, "User is no longer a member of this organization")
	if e.countSeen("GET /api/v1/instances") != before {
		t.Fatal("a removed member's request reached the region")
	}

	// A system admin is org admin everywhere; once demoted, neither the system role nor the org
	// admin role is forwarded, and without a membership the request is refused
	h = forwarded("system admin", admin2Tok)
	expectHeader("system admin", h, "X-System-Role", "1")
	expectHeader("system admin", h, "X-Org-Role", "3")
	db.Model(admin2).Updates(map[string]interface{}{"system_role": model.SystemUser, "is_superuser": false})
	e.expectDetail("GET", "/api/v1/instances", admin2Tok, nil, 403, "User is no longer a member of this organization")
	e.member(admin2, org, model.OrgRoleReader, nil)
	h = forwarded("demoted system admin", admin2Tok)
	expectHeader("demoted system admin", h, "X-System-Role", "0")
	expectHeader("demoted system admin", h, "X-Org-Role", "1")
}

// P4: member roles are 1–3 and the owner's role cannot be changed.
func TestMemberRoleValidation(t *testing.T) {
	e := newSecEnv(t, nil)
	owner := e.user("owner")
	org := e.org("org", owner)
	orgAdmin := e.user("orgadmin")
	e.member(orgAdmin, org, model.OrgRoleAdmin, nil)
	writer := e.user("writer")
	e.member(writer, org, model.OrgRoleWriter, nil)
	adminTok := e.token(orgAdmin, org, model.OrgRoleAdmin)
	membersPath := "/api/v1/orgs/" + org.UUID + "/members/"

	for _, role := range []int{0, -1, 4, 99} {
		e.c.expect("PATCH", membersPath+writer.UUID, adminTok, map[string]interface{}{"org_role": role}, 422)
	}
	e.expectDetail("PATCH", membersPath+owner.UUID, adminTok, map[string]interface{}{"org_role": 1}, 400,
		"Cannot change the role of the organization owner. Transfer ownership first.")
	e.c.expect("PATCH", membersPath+writer.UUID, adminTok, map[string]interface{}{"org_role": 1}, 200)
	var m model.Member
	dbs.DB().Where("user_id = ? AND org_id = ?", writer.ID, org.ID).First(&m)
	if m.OrgRole != model.OrgRoleReader {
		t.Fatalf("role not updated: %v", m.OrgRole)
	}

	// Direct add and invitation take the same roles
	rootTok := e.token(e.root(), nil, model.OrgRoleNone)
	newcomer := e.user("newcomer")
	e.c.expect("POST", "/api/v1/orgs/"+org.UUID+"/members", rootTok,
		map[string]interface{}{"user_uuid": newcomer.UUID, "org_role": 7}, 422)
	e.c.expect("POST", "/api/v1/orgs/"+org.UUID+"/invitations", adminTok,
		map[string]interface{}{"email": e.name("invitee") + "@example.com", "org_role": 0}, 422)
}

// P2: updating a channel's config goes through the same checks as creating it.
func TestUpdateChannelValidatesConfig(t *testing.T) {
	e := newSecEnv(t, nil)
	owner := e.user("owner")
	org := e.org("org", owner)
	tok := e.token(owner, org, model.OrgRoleAdmin)
	ch := e.c.expect("POST", "/api/v1/notification-channels", tok, map[string]interface{}{
		"name": "n", "type": "webhook", "config": map[string]interface{}{"url": "https://hooks.example.com/x"}}, 201)
	path := "/api/v1/notification-channels/" + ch["uuid"].(string)

	for _, cfg := range []map[string]interface{}{
		{"url": "http://10.0.0.1/x"},
		{"url": "http://127.0.0.1:8256/api/v1/rules/file"},
		{"url": "http://169.254.169.254/latest"},
		{"url": "http://100.64.0.1/x"},
		{"url": "http://[64:ff9b:1::a00:1]/x"},
		{"url": "http://[2002:a00:1::1]/x"},
		{"url": "ftp://hooks.example.com/x"},
		{},
	} {
		e.c.expect("PUT", path, tok, map[string]interface{}{"config": cfg}, 422)
	}
	e.c.expect("PUT", path, tok, map[string]interface{}{"name": ""}, 422)
	got := e.c.expect("PUT", path, tok, map[string]interface{}{"config": map[string]interface{}{"url": "https://hooks.example.com/y"}}, 200)
	if cfg, _ := got["config"].(map[string]interface{}); cfg["url"] != "https://hooks.example.com/y" {
		t.Fatalf("config not updated: %v", got)
	}
}

// P7: a system admin resetting someone else's password may omit old_password; changing one's own
// still needs the right one.
func TestChangePasswordOldPasswordRules(t *testing.T) {
	e := newSecEnv(t, nil)
	u := e.user("pw")
	path := "/api/v1/users/" + u.UUID + "/password"
	selfTok := e.token(u, nil, model.OrgRoleNone)

	e.expectDetail("PUT", path, selfTok, map[string]interface{}{"new_password": "newpassword1"}, 400, "Incorrect old password")
	e.expectDetail("PUT", path, selfTok, map[string]interface{}{"old_password": "wrong-one", "new_password": "newpassword1"}, 400, "Incorrect old password")
	e.c.expect("PUT", path, selfTok, map[string]interface{}{"old_password": "password123", "new_password": "short"}, 422)
	e.c.expect("PUT", path, selfTok, map[string]interface{}{"old_password": "password123", "new_password": "newpassword1"}, 200)

	rootTok := e.token(e.root(), nil, model.OrgRoleNone)
	e.c.expect("PUT", path, rootTok, map[string]interface{}{"new_password": "resetpassword1"}, 200)
	e.c.expect("POST", "/api/v1/auth/token", "", map[string]interface{}{"username": u.Username, "password": "resetpassword1"}, 200)
}

// P3 / review: the gateway refuses at save time exactly the ranges clapi refuses at send time,
// otherwise a channel saves fine and then every notification fails.
func TestNonPublicPrefixesMatchClapi(t *testing.T) {
	src, err := os.ReadFile("../../../api/src/services/notifier_dial.go")
	if os.IsNotExist(err) {
		t.Skip("api sources not available")
	}
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`netip\.MustParsePrefix\("([^"]+)"\)`)
	var clapi, gateway []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		clapi = append(clapi, m[1])
	}
	for _, p := range nonPublicPrefixes {
		gateway = append(gateway, p.String())
	}
	sort.Strings(clapi)
	sort.Strings(gateway)
	if len(clapi) == 0 || strings.Join(clapi, ",") != strings.Join(gateway, ",") {
		t.Fatalf("non-public ranges differ:\n  clapi   %v\n  gateway %v", clapi, gateway)
	}
}
