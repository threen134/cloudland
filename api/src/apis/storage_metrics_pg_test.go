/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

// The metrics of a storage cluster against PostgreSQL (CLAPI_TEST_DB_URI) and a fake Prometheus
// (shared-storage-design.md §14.3): the curves are named and aligned, a pool deleted since and a query with no data
// are left out, the time range is bounded, and the scrape targets are the mgr hosts of managed Ceph clusters

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

func TestStorageMetricsPG(t *testing.T) {
	db := apiPGDB(t)
	gin.SetMode(gin.TestMode)
	viper.Set("cpgateway.secret", "pg-secret")
	stamp := time.Now().UnixNano() % 1000000

	// A managed Ceph cluster: a mgr host, an OSD host and a mgr host still joining; an imported one with a mgr
	base := int32(200000 + 3*(stamp%100000))
	hostids := []int32{base, base + 1, base + 2}
	db.Unscoped().Where("hostid IN ?", hostids).Delete(&model.Hyper{})
	for i, id := range hostids {
		h := &model.Hyper{Hostid: id, Hostname: fmt.Sprintf("pgm-%d-%d", stamp, i), HostIP: fmt.Sprintf("10.250.%d.%d", stamp%200, i+1)}
		if err := db.Create(h).Error; err != nil {
			t.Fatal(err)
		}
		defer db.Unscoped().Delete(h)
	}
	cluster := &model.StorageCluster{Name: fmt.Sprintf("pgm-%d", stamp), Kind: model.StorageKindCeph, Mode: model.StorageModeManaged,
		Status: model.StorageClusterReady, Health: model.StorageHealthHealthy}
	external := &model.StorageCluster{Name: fmt.Sprintf("pgm-ext-%d", stamp), Kind: model.StorageKindCeph, Mode: model.StorageModeExternal,
		Status: model.StorageClusterReady, Health: model.StorageHealthHealthy}
	for _, c := range []*model.StorageCluster{cluster, external} {
		if err := db.Create(c).Error; err != nil {
			t.Fatal(err)
		}
		defer db.Unscoped().Delete(c)
	}
	nodes := []*model.StorageClusterNode{
		{ClusterID: cluster.ID, Hostid: hostids[0], Roles: "mon,mgr,osd", Status: model.StorageNodeActive},
		{ClusterID: cluster.ID, Hostid: hostids[1], Roles: "osd", Status: model.StorageNodeActive},
		{ClusterID: cluster.ID, Hostid: hostids[2], Roles: "mon,mgr", Status: model.StorageNodeJoining},
		{ClusterID: external.ID, Hostid: hostids[1], Roles: "mgr", Status: model.StorageNodeActive},
	}
	for _, n := range nodes {
		if err := db.Create(n).Error; err != nil {
			t.Fatal(err)
		}
		defer db.Unscoped().Delete(n)
	}
	pool := &model.StoragePool{Name: fmt.Sprintf("pgm-pool-%d", stamp), Driver: "ceph_rbd", Status: model.StoragePoolActive, ClusterID: cluster.ID}
	if err := db.Create(pool).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Unscoped().Delete(pool)

	// The scrape targets: the active mgr host of the managed cluster only
	targets, err := services.GetPrometheusTargets("storage")
	if err != nil {
		t.Fatal(err)
	}
	mine := []services.SDTarget{}
	for _, tg := range targets {
		if tg.Labels["storage_cluster"] == cluster.UUID || tg.Labels["storage_cluster"] == external.UUID {
			mine = append(mine, tg)
		}
	}
	if len(mine) != 1 || mine[0].Targets[0] != fmt.Sprintf("10.250.%d.1:9283", stamp%200) || mine[0].Labels["storage_cluster"] != cluster.UUID ||
		mine[0].Labels["hostname"] != fmt.Sprintf("pgm-%d-0", stamp) {
		t.Fatalf("scrape targets: %+v", mine)
	}

	// The fake Prometheus answers the pool use (with a pool that is gone), the read throughput (with a gap) and
	// nothing for the rest
	end := (time.Now().Unix() / 60) * 60
	start := end - 300
	queries := []string{}
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		result := []map[string]interface{}{}
		point := func(ts int64, v string) []interface{} { return []interface{}{float64(ts), v} }
		switch {
		case strings.Contains(q, "cloudland_shared_pool_used_bytes"):
			result = append(result,
				map[string]interface{}{"metric": map[string]string{"pool": pool.UUID}, "values": [][]interface{}{point(start, "12.5"), point(end, "13")}},
				map[string]interface{}{"metric": map[string]string{"pool": "00000000-0000-0000-0000-000000000000"}, "values": [][]interface{}{point(start, "50")}})
		case strings.Contains(q, "ceph_pool_rd_bytes"):
			result = append(result, map[string]interface{}{"metric": map[string]string{}, "values": [][]interface{}{point(start, "100"), point(start+120, "NaN"), point(end, "300")}})
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "data": map[string]interface{}{"resultType": "matrix", "result": result}})
	}))
	defer prom.Close()
	saved := PrometheusRangeURL
	PrometheusRangeURL = prom.URL
	defer func() { PrometheusRangeURL = saved }()

	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(Authorize())
	g.GET("/storage_clusters/:id/metrics", storageClusterAPI.Metrics)
	get := func(admin bool, query string) (int, *StorageMetricsResponse) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/storage_clusters/"+cluster.UUID+"/metrics?"+query, nil)
		req.Header.Set("X-Forwarded-Secret", "pg-secret")
		if admin {
			req.Header.Set("X-System-Role", fmt.Sprint(int(model.SystemAdmin)))
		}
		req.Header.Set("X-User-Name", "pg-admin")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		out := &StorageMetricsResponse{}
		json.Unmarshal(w.Body.Bytes(), out)
		return w.Code, out
	}
	code, resp := get(true, fmt.Sprintf("start=%d&end=%d&step=60s", start, end))
	if code != http.StatusOK || len(resp.Timestamps) != 6 || resp.Step != "60s" {
		t.Fatalf("metrics: %d %+v", code, resp)
	}
	if len(resp.Charts) != 2 || resp.Charts[0].Key != services.StorageChartPools || resp.Charts[1].Key != services.StorageChartThroughput {
		t.Fatalf("charts: %+v", resp.Charts)
	}
	pools := resp.Charts[0]
	if pools.Unit != "percent" || len(pools.Series) != 1 || pools.Series[0].Label != pool.Name || pools.Series[0].Key != "pool" ||
		*pools.Series[0].Values[0] != 12.5 || pools.Series[0].Values[1] != nil || *pools.Series[0].Values[5] != 13 {
		t.Fatalf("pool chart: %+v", pools.Series)
	}
	read := resp.Charts[1].Series[0]
	if resp.Charts[1].Unit != "bytes_per_second" || read.Key != "read" || read.Label != "" || *read.Values[0] != 100 || read.Values[2] != nil || *read.Values[5] != 300 {
		t.Fatalf("throughput chart: %+v", resp.Charts[1])
	}
	// Every query names the cluster; the rates are taken over at least 3 minutes
	for _, q := range queries {
		if !strings.Contains(q, `"`+cluster.UUID+`"`) {
			t.Fatalf("a query without the cluster: %s", q)
		}
		if strings.Contains(q, "rate(") && !strings.Contains(q, "[180s]") {
			t.Fatalf("rate window: %s", q)
		}
	}

	// The limits
	for _, q := range []string{
		fmt.Sprintf("start=%d&end=%d&step=1500ms", start, end),
		fmt.Sprintf("start=%d&end=%d&step=60s", end-40*86400, end),
		fmt.Sprintf("start=%d&end=%d&step=60s", start, end+7200),
		fmt.Sprintf("start=%d&end=%d&step=1s", end-30*86400, end),
	} {
		if code, _ := get(true, q); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, code)
		}
	}
	if code, _ := get(false, fmt.Sprintf("start=%d&end=%d&step=60s", start, end)); code == http.StatusOK {
		t.Fatal("a member who is not a system admin got the metrics")
	}
}
