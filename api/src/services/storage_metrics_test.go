/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The curves of the storage clusters (shared-storage-design.md §14.3): the GPFS storage pools and the inodes of the
// filesets, and an imported Ceph cluster read by its clients instead of its mgr

import (
	"strings"
	"testing"

	"api/src/model"
)

func TestStorageMetricQueriesShapes(t *testing.T) {
	gpfs := &model.StorageCluster{Model: model.Model{UUID: "11111111-2222-3333-4444-555555555555"}, Kind: model.StorageKindGPFS, Mode: model.StorageModeManaged}
	byChart := map[string][]StorageMetricQuery{}
	for _, q := range (gpfsBackend{}).MetricQueries(gpfs, 180) {
		byChart[q.Chart] = append(byChart[q.Chart], q)
	}
	pools := byChart[StorageChartGpfsPools]
	if len(pools) != 2 || pools[0].Split != "fspool" || !strings.Contains(pools[0].Query, `label_join(max by (fs, gpfs_pool)`) ||
		!strings.Contains(pools[0].Query, `cloudland_gpfs_pool_free_bytes{cluster="`+gpfs.UUID+`"}`) {
		t.Fatalf("gpfs pool queries %+v", pools)
	}
	inodes := byChart[StorageChartInodes]
	if len(inodes) != 1 || inodes[0].Split != "pool" || !strings.Contains(inodes[0].Query, "cloudland_gpfs_fileset_inodes_max") {
		t.Fatalf("inode queries %+v", inodes)
	}
	if StorageChartUnit(StorageChartGpfsPools) != "bytes" || StorageChartUnit(StorageChartInodes) != "percent" {
		t.Fatal("units of the new charts")
	}

	// A managed Ceph cluster from its mgr, an imported one from its clients
	ceph := &model.StorageCluster{Model: model.Model{UUID: "66666666-7777-8888-9999-000000000000"}, Kind: model.StorageKindCeph, Mode: model.StorageModeManaged}
	for _, q := range (cephBackend{}).MetricQueries(ceph, 180) {
		if strings.Contains(q.Query, "cloudland_ceph_") || !strings.Contains(q.Query, `storage_cluster="`+ceph.UUID+`"`) {
			t.Fatalf("managed ceph query %s", q.Query)
		}
	}
	ceph.Mode = model.StorageModeExternal
	charts := map[string]bool{}
	for _, q := range (cephBackend{}).MetricQueries(ceph, 180) {
		charts[q.Chart] = true
		if !strings.Contains(q.Query, "cloudland_ceph_") || !strings.Contains(q.Query, `cluster="`+ceph.UUID+`"`) || !strings.HasPrefix(q.Query, "max(") {
			t.Fatalf("imported ceph query %s", q.Query)
		}
		if strings.Contains(q.Query, "rate(") && !strings.Contains(q.Query, "[180s]") {
			t.Fatalf("rate window %s", q.Query)
		}
	}
	for _, c := range []string{StorageChartCapacity, StorageChartThroughput, StorageChartIOPS, StorageChartOSDs} {
		if !charts[c] {
			t.Fatalf("imported ceph has no %s chart", c)
		}
	}
}
