package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cpgateway/src/model"
)

const testOrgUUID = "org-a"

func TestQueryResourceAmountBackendStatus(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/instances/missing":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error_code":1,"error_message":"Invalid instance query"}`))
		case "/api/v1/instances/forbidden":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`not json`))
		case "/api/v1/instances/broken":
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/v1/instances/ok":
			w.Write([]byte(`{"cpu":2,"memory":2048,"disk":10,"owner_uuid":"org-a"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()
	region := &model.Region{InternalEndpoint: backend.URL}
	ctx := context.Background()
	headers := map[string]string{"X-Org-UUID": testOrgUUID}

	// 4xx is passed through with the backend's error message
	_, herr := QueryResourceAmount(ctx, region, "instance", "instances/missing", headers)
	if herr == nil || herr.Status != http.StatusBadRequest || herr.Detail != "Invalid instance query" {
		t.Fatalf("expected 400 with backend message, got %+v", herr)
	}
	_, herr = QueryResourceAmount(ctx, region, "instance", "instances/forbidden", headers)
	if herr == nil || herr.Status != http.StatusForbidden {
		t.Fatalf("expected 403 passthrough, got %+v", herr)
	}
	// 5xx is still a gateway error
	_, herr = QueryResourceAmount(ctx, region, "instance", "instances/broken", headers)
	if herr == nil || herr.Status != http.StatusBadGateway {
		t.Fatalf("expected 502 for backend 5xx, got %+v", herr)
	}
	amount, herr := QueryResourceAmount(ctx, region, "instance", "instances/ok", headers)
	if herr != nil || amount["cpu_cores"] != 2 || amount["ram_gb"] != 2 || amount["disk_gb"] != 10 {
		t.Fatalf("unexpected amount %v, err %+v", amount, herr)
	}
}

func TestQueryResourceAmountByResource(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/vpcs/mine":
			w.Write([]byte(`{"id":"mine","owner_uuid":"org-a"}`))
		case "/api/v1/vpcs/foreign":
			w.Write([]byte(`{"id":"foreign","owner_uuid":"org-b"}`))
		case "/api/v1/vpcs/legacy":
			w.Write([]byte(`{"id":"legacy"}`))
		case "/api/v1/load_balancers/lb":
			w.Write([]byte(`{"id":"lb","owner_uuid":"org-a","floating_ips":[{"id":"f1"},{"id":"f2"}]}`))
		case "/api/v1/load_balancers/lb-no-ip":
			w.Write([]byte(`{"id":"lb-no-ip","owner_uuid":"org-a"}`))
		case "/api/v1/images/private":
			w.Write([]byte(`{"id":"private","owner_uuid":"org-a","public":false}`))
		case "/api/v1/images/public":
			w.Write([]byte(`{"id":"public","owner_uuid":"org-a","public":true}`))
		case "/api/v1/load_balancers/lb/floating_ips/f1":
			w.Write([]byte(`{"id":"f1","owner_uuid":"org-a"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()
	region := &model.Region{InternalEndpoint: backend.URL}
	ctx := context.Background()
	headers := map[string]string{"X-Org-UUID": testOrgUUID}

	for _, tc := range []struct {
		resource, path string
		want           ResourceAmount
	}{
		{"vpc", "vpcs/mine", ResourceAmount{"vpcs": 1}},
		// Another org's resource (deleted by a system admin): the caller's org is not released
		{"vpc", "vpcs/foreign", ResourceAmount{}},
		// Backend without owner_uuid: keep releasing from the caller's org
		{"vpc", "vpcs/legacy", ResourceAmount{"vpcs": 1}},
		// Deleting a load balancer also frees its floating IPs
		{"load_balancer", "load_balancers/lb", ResourceAmount{"load_balancers": 1, "public_ips": 2}},
		{"load_balancer", "load_balancers/lb-no-ip", ResourceAmount{"load_balancers": 1}},
		// Only private images are charged
		{"image", "images/private", ResourceAmount{"images": 1}},
		{"image", "images/public", ResourceAmount{}},
		{"lb_floating_ip", "load_balancers/lb/floating_ips/f1", ResourceAmount{"public_ips": 1}},
	} {
		got, herr := QueryResourceAmount(ctx, region, tc.resource, tc.path, headers)
		if herr != nil {
			t.Fatalf("%s: unexpected error %+v", tc.path, herr)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.path, got, tc.want)
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Fatalf("%s: got %v, want %v", tc.path, got, tc.want)
			}
		}
	}
}

func TestFloatingIPCount(t *testing.T) {
	for _, tc := range []struct {
		body map[string]interface{}
		want int
	}{
		{map[string]interface{}{}, 1},
		{map[string]interface{}{"activation_count": float64(0)}, 1},
		{map[string]interface{}{"activation_count": float64(3)}, 3},
		{map[string]interface{}{"site_subnets": []interface{}{map[string]interface{}{}, map[string]interface{}{}}}, 2},
		{map[string]interface{}{"activation_count": float64(2), "site_subnets": []interface{}{map[string]interface{}{}}}, 3},
	} {
		if got := floatingIPCount(tc.body); got != tc.want {
			t.Errorf("floatingIPCount(%v) = %d, want %d", tc.body, got, tc.want)
		}
	}
}

// Requests that reserve nothing never touch the database, so these run without one.
func TestPrepareQuotaWithoutReservation(t *testing.T) {
	region := &model.Region{InternalEndpoint: "http://unused"}
	ctx := context.Background()

	// A system admin's image is public: nothing is charged
	plan, herr := PrepareQuota(ctx, 1, region, "POST", "/images", "/images", "", []byte(`{"name":"img"}`), map[string]string{"X-System-Role": "1"})
	if herr != nil || plan.Action != "consume" || plan.Reserved {
		t.Fatalf("system admin image: expected no reservation, got %+v %+v", plan, herr)
	}
	// A body that is not a JSON object is rejected by clapi with 400; the gateway must not turn it into 500
	for _, raw := range []string{``, `[]`, `not json`} {
		plan, herr = PrepareQuota(ctx, 1, region, "POST", "/vpcs", "/vpcs", "", []byte(raw), nil)
		if herr != nil || plan.Reserved {
			t.Fatalf("body %q: expected no reservation and no error, got %+v %+v", raw, plan, herr)
		}
	}
	// Routes without a rule
	plan, herr = PrepareQuota(ctx, 1, region, "PATCH", "/vpcs/{id}", "/vpcs/x", "", []byte(`{}`), nil)
	if herr != nil || plan.Action != "" {
		t.Fatalf("PATCH vpcs: expected no action, got %+v %+v", plan, herr)
	}
}

func TestVpnGatewayPublicIps(t *testing.T) {
	list := func(n int) []interface{} { return make([]interface{}, n) }
	cases := []struct {
		name string
		body map[string]interface{}
		want float64
	}{
		{"default", map[string]interface{}{}, 1},
		{"active_standby, two addresses", map[string]interface{}{"public_ips": list(2)}, 2},
		{"active_active", map[string]interface{}{"ha_mode": "active_active"}, 2},
		{"active_active with client VPN", map[string]interface{}{"ha_mode": "active_active", "client_enabled": true, "public_ips": list(1)}, 3},
		{"active_active, all given", map[string]interface{}{"ha_mode": "active_active", "public_ips": list(2)}, 2},
	}
	for _, c := range cases {
		if got := vpnGatewayPublicIps(c.body); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// D10: the GET that measures a resource before deleting or resizing it dropped the query string, so a system
// admin could not delete another org's resource through the gateway (the backend answers 400 record not found
// without all_orgs=true, and the gateway aborted the deletion).
func TestPrepareQuotaKeepsQueryString(t *testing.T) {
	var seen []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if r.URL.Query().Get("all_orgs") != "true" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error_code":100100,"error_message":"Failed to query floatingIp (Details: record not found)"}`))
			return
		}
		switch r.URL.Path {
		case "/api/v1/floating_ips/fip":
			w.Write([]byte(`{"id":"fip","owner_uuid":"org-b"}`))
		case "/api/v1/volumes/vol":
			w.Write([]byte(`{"id":"vol","size":10,"owner_uuid":"org-b"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()
	region := &model.Region{InternalEndpoint: backend.URL}
	ctx := context.Background()
	headers := map[string]string{"X-Org-UUID": testOrgUUID, "X-System-Role": systemAdminRole}

	plan, herr := PrepareQuota(ctx, 1, region, "DELETE", "/floating_ips/{id}", "/floating_ips/fip", "all_orgs=true", nil, headers)
	if herr != nil {
		t.Fatalf("delete with all_orgs: %+v (backend saw %v)", herr, seen)
	}
	// Another org's floating IP: the caller's quota is not released
	if plan.Action != "release" || len(plan.Amount) != 0 {
		t.Fatalf("unexpected plan %+v", plan)
	}
	if seen[len(seen)-1] != "GET /api/v1/floating_ips/fip?all_orgs=true" {
		t.Fatalf("backend saw %v", seen)
	}

	// Resizing another org's volume measures it with the same parameters
	plan, herr = PrepareQuota(ctx, 1, region, "POST", "/volumes/{id}/resize", "/volumes/vol/resize", "all_orgs=true", []byte(`{"size":20}`), headers)
	if herr != nil || plan.Reserved {
		t.Fatalf("resize with all_orgs: %+v %+v (backend saw %v)", plan, herr, seen)
	}
	if seen[len(seen)-1] != "GET /api/v1/volumes/vol?all_orgs=true" {
		t.Fatalf("backend saw %v", seen)
	}

	// Without the parameter the backend still refuses and the gateway passes its answer through
	if _, herr = PrepareQuota(ctx, 1, region, "DELETE", "/floating_ips/{id}", "/floating_ips/fip", "", nil, headers); herr == nil || herr.Status != http.StatusBadRequest {
		t.Fatalf("delete without all_orgs: %+v", herr)
	}
}
