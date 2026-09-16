package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestFetchAllResourcesPaginates(t *testing.T) {
	const total = 250
	var requests []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		switch r.URL.Path {
		case "/instances":
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			items := []map[string]interface{}{}
			for i := offset; i < offset+limit && i < total; i++ {
				items = append(items, map[string]interface{}{"cpu": 1})
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"offset": offset, "total": total, "limit": len(items), "instances": items})
		case "/volumes":
			// 额外查询参数要带上
			if r.URL.Query().Get("type") != "all" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Write([]byte(`{"total":1,"volumes":[{"size":10}]}`))
		case "/bare":
			w.Write([]byte(`[{"size":1},{"size":2}]`))
		case "/nototal":
			w.Write([]byte(`{"floating_ips":[{},{}]}`))
		case "/liar":
			// total 大于实际能取到的条数：取到空页即停止，不能死循环
			w.Write([]byte(`{"total":1000,"items":[]}`))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer backend.Close()
	ctx := context.Background()
	client := backend.Client()

	items, err := fetchAllResources(ctx, client, backend.URL+"/instances", "", nil)
	if err != nil || len(items) != total {
		t.Fatalf("expected %d instances across pages, got %d, err %v", total, len(items), err)
	}
	if len(requests) != 3 {
		t.Errorf("expected 3 page requests for %d items, got %d: %v", total, len(requests), requests)
	}

	if items, err = fetchAllResources(ctx, client, backend.URL+"/volumes", "type=all", nil); err != nil || len(items) != 1 {
		t.Fatalf("volumes with extra query: got %d, err %v", len(items), err)
	}
	if items, err = fetchAllResources(ctx, client, backend.URL+"/bare", "", nil); err != nil || len(items) != 2 {
		t.Fatalf("bare list: got %d, err %v", len(items), err)
	}
	if items, err = fetchAllResources(ctx, client, backend.URL+"/nototal", "", nil); err != nil || len(items) != 2 {
		t.Fatalf("list without total: got %d, err %v", len(items), err)
	}
	if items, err = fetchAllResources(ctx, client, backend.URL+"/liar", "", nil); err != nil || len(items) != 0 {
		t.Fatalf("empty page should stop: got %d, err %v", len(items), err)
	}
	// 后端报错时必须返回错误，不能当作空列表（否则会把用量覆盖成 0）
	if _, err = fetchAllResources(ctx, client, backend.URL+"/forbidden", "", nil); err == nil {
		t.Fatalf("expected error on non-200 response")
	}
}
