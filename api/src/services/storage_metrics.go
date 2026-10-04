/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Metrics of the storage clusters (shared-storage-design.md §14.3): the curves on the monitoring tab of a cluster and
// the scrape targets Prometheus finds through clapi. They are only for looking at; the alarms come from the health
// watchdog (§14.2). What a kind exports and how its curves are queried comes from its backend
// (storageMetricsSource); the use of the shared pools is the same for every kind: each host of a pool writes it for
// the textfile collector of node_exporter (shared_pool_probe.sh).

import (
	"context"
	"fmt"
	"net"
	"strconv"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

// Charts of the monitoring tab and the unit of their values
const (
	StorageChartCapacity   = "capacity"   // bytes
	StorageChartThroughput = "throughput" // bytes per second
	StorageChartIOPS       = "iops"       // operations per second
	StorageChartPools      = "pools"      // percent
	StorageChartNodes      = "nodes"      // count of hosts
	StorageChartOSDs       = "osds"       // count of OSDs
)

var storageChartUnits = map[string]string{
	StorageChartCapacity:   "bytes",
	StorageChartThroughput: "bytes_per_second",
	StorageChartIOPS:       "ops_per_second",
	StorageChartPools:      "percent",
	StorageChartNodes:      "count",
	StorageChartOSDs:       "count",
}

// StorageChartUnit is the unit of the values of a chart
func StorageChartUnit(chart string) string {
	return storageChartUnits[chart]
}

// StorageMetricQuery is one PromQL expression of a chart. Without Split it gives one series; with Split every value
// of that label is a series of its own (the file system, the pool)
type StorageMetricQuery struct {
	Chart  string
	Series string // read, write, used, total, up...
	Query  string
	Split  string
}

// storageMetricsSource is a backend whose clusters have curves
type storageMetricsSource interface {
	// MetricQueries are the queries of the curves of a cluster; rates are taken over window seconds
	MetricQueries(cluster *model.StorageCluster, window int64) []StorageMetricQuery
}

// storageScrapeSource is a backend whose clusters Prometheus scrapes besides the node_exporter of their hosts
type storageScrapeSource interface {
	ScrapeTargets(cluster *model.StorageCluster, nodes []*model.StorageClusterNode, hosts map[int32]*model.Hyper) []SDTarget
}

// promLabel quotes a label value of a PromQL selector. Only UUIDs go in, but a quote must never end the selector
func promLabel(v string) string {
	return strconv.Quote(v)
}

// StorageMetricQueries returns the cluster, the queries of its curves (the pools first) and the names of its pools
// by UUID, for system admins
func (a *StorageClusterAdmin) StorageMetricQueries(ctx context.Context, uuid string, window int64) (cluster *model.StorageCluster,
	queries []StorageMetricQuery, poolNames map[string]string, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	cluster = &model.StorageCluster{}
	if err = db.Where("uuid = ?", uuid).Take(cluster).Error; err != nil {
		return nil, nil, nil, NewCLError(ErrStorageClusterNotFound, "Storage cluster not found", err)
	}
	pools, err := sharedPoolsOfCluster(db, cluster.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	poolNames = map[string]string{}
	for _, p := range pools {
		poolNames[p.UUID] = p.Name
	}
	c := "cluster=" + promLabel(cluster.UUID)
	// Every host of a pool writes the same numbers: the largest of them
	queries = append(queries, StorageMetricQuery{Chart: StorageChartPools, Series: "pool", Split: "pool",
		Query: fmt.Sprintf(`max by (pool) (cloudland_shared_pool_used_bytes{%s}) / max by (pool) (cloudland_shared_pool_size_bytes{%s} > 0) * 100`, c, c)})
	backend, berr := storageBackendOf(cluster.Kind)
	if berr != nil {
		return
	}
	if src, ok := backend.(storageMetricsSource); ok {
		queries = append(queries, src.MetricQueries(cluster, window)...)
	}
	return
}

// StorageScrapeTargets are the targets Prometheus scrapes for the storage clusters (the job storage_clusters,
// http_sd from /api/v1/prometheus/sd/storage), labelled with the cluster
func StorageScrapeTargets() ([]SDTarget, error) {
	db := dbs.DB()
	clusters := []*model.StorageCluster{}
	if err := db.Where("status IN ?", []string{model.StorageClusterReady, model.StorageClusterDegraded}).Order("id").Find(&clusters).Error; err != nil {
		return nil, fmt.Errorf("failed to query storage clusters: %w", err)
	}
	targets := []SDTarget{}
	for _, cluster := range clusters {
		backend, err := storageBackendOf(cluster.Kind)
		if err != nil {
			continue
		}
		src, ok := backend.(storageScrapeSource)
		if !ok {
			continue
		}
		nodes := []*model.StorageClusterNode{}
		db.Where("cluster_id = ?", cluster.ID).Order("hostid").Find(&nodes)
		ids := []int32{}
		for _, n := range nodes {
			ids = append(ids, n.Hostid)
		}
		hyps := []*model.Hyper{}
		db.Where("hostid IN ?", ids).Find(&hyps)
		hosts := map[int32]*model.Hyper{}
		for _, h := range hyps {
			hosts[h.Hostid] = h
		}
		for _, t := range src.ScrapeTargets(cluster, nodes, hosts) {
			if t.Labels == nil {
				t.Labels = map[string]string{}
			}
			t.Labels["storage_cluster"] = cluster.UUID
			targets = append(targets, t)
		}
	}
	return targets, nil
}

// hostPort joins the management address of a host and a port
func hostPort(h *model.Hyper, port int) string {
	return net.JoinHostPort(h.HostIP, strconv.Itoa(port))
}
