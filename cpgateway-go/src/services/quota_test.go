package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cpgateway-go/src/model"
)

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
			w.Write([]byte(`{"cpu":2,"memory":2048,"disk":10}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()
	region := &model.Region{InternalEndpoint: backend.URL}
	ctx := context.Background()

	// 4xx 原样转给客户端，带上后端的错误信息
	_, herr := QueryResourceAmount(ctx, region, "/instances/missing", "missing", nil)
	if herr == nil || herr.Status != http.StatusBadRequest || herr.Detail != "Invalid instance query" {
		t.Fatalf("expected 400 with backend message, got %+v", herr)
	}
	_, herr = QueryResourceAmount(ctx, region, "/instances/forbidden", "forbidden", nil)
	if herr == nil || herr.Status != http.StatusForbidden {
		t.Fatalf("expected 403 passthrough, got %+v", herr)
	}
	// 5xx 仍是网关错误
	_, herr = QueryResourceAmount(ctx, region, "/instances/broken", "broken", nil)
	if herr == nil || herr.Status != http.StatusBadGateway {
		t.Fatalf("expected 502 for backend 5xx, got %+v", herr)
	}
	amount, herr := QueryResourceAmount(ctx, region, "/instances/ok", "ok", nil)
	if herr != nil || amount["cpu_cores"] != 2 || amount["ram_gb"] != 2 || amount["disk_gb"] != 10 {
		t.Fatalf("unexpected amount %v, err %+v", amount, herr)
	}
}
