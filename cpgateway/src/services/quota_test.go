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
	plan, herr := PrepareQuota(ctx, 1, region, "POST", "/images", "/images", []byte(`{"name":"img"}`), map[string]string{"X-System-Role": "1"})
	if herr != nil || plan.Action != "consume" || plan.Reserved {
		t.Fatalf("system admin image: expected no reservation, got %+v %+v", plan, herr)
	}
	// A body that is not a JSON object is rejected by clapi with 400; the gateway must not turn it into 500
	for _, raw := range []string{``, `[]`, `not json`} {
		plan, herr = PrepareQuota(ctx, 1, region, "POST", "/vpcs", "/vpcs", []byte(raw), nil)
		if herr != nil || plan.Reserved {
			t.Fatalf("body %q: expected no reservation and no error, got %+v %+v", raw, plan, herr)
		}
	}
	// Routes without a rule
	plan, herr = PrepareQuota(ctx, 1, region, "PATCH", "/vpcs/{id}", "/vpcs/x", []byte(`{}`), nil)
	if herr != nil || plan.Action != "" {
		t.Fatalf("PATCH vpcs: expected no action, got %+v %+v", plan, herr)
	}
}
