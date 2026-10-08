/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	. "api/src/common"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

// StorageMetricSeries is one curve of a chart, aligned with StorageMetricsResponse.Timestamps; null means no sample
// (nothing exported then: the hosts or the mgr were down, node_exporter stopped)
type StorageMetricSeries struct {
	// What the curve is: read, write, used, total, active, mounted, up, in, pool
	Key string `json:"key"`
	// The file system or the pool (its name) when the chart has one curve of Key per file system or pool
	Label  string     `json:"label,omitempty"`
	Values []*float64 `json:"values"`
}

type StorageMetricChart struct {
	// capacity, throughput, iops, pools, nodes (GPFS: hosts running GPFS and mounting each file system), osds
	Key string `json:"key"`
	// bytes, bytes_per_second, ops_per_second, percent, count
	Unit   string                 `json:"unit"`
	Series []*StorageMetricSeries `json:"series"`
}

type StorageMetricsResponse struct {
	Start      int64                 `json:"start"`
	End        int64                 `json:"end"`
	Step       string                `json:"step"`
	Timestamps []int64               `json:"timestamps"`
	Charts     []*StorageMetricChart `json:"charts"`
}

// @Summary storage cluster metrics
// @Description Curves of a storage cluster from Prometheus (shared-storage-design.md §14.3): the use of its pools, its capacity, throughput and IOPS, and its hosts (GPFS) or OSDs (Ceph). Only for system admins. A chart with no data at all is left out.
// @tags StorageCluster
// @Produce json
// @Param   id    path  string true "Cluster UUID"
// @Param   start query string true "Start, unix seconds"
// @Param   end   query string true "End, unix seconds"
// @Param   step  query string true "Resolution as a duration, e.g. 60s or 5m"
// @Success 200 {object} StorageMetricsResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /storage_clusters/{id}/metrics [get]
func (v *StorageClusterAPI) Metrics(c *gin.Context) {
	ctx := c.Request.Context()
	stepStr := c.Query("step")
	start, end, err := validateAndParseTimeParams(c.Query("start"), c.Query("end"), stepStr)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, err.Error(), nil)
		return
	}
	// The same limits as the VPN traffic: bound the range before any arithmetic on it, whole seconds only
	if start < 0 || end > time.Now().Unix()+3600 || end-start > vpnTrafficMaxRange {
		ErrorResponse(c, http.StatusBadRequest, "Invalid time range: at most 31 days, not ending in the future", nil)
		return
	}
	step, _ := time.ParseDuration(stepStr)
	if step%time.Second != 0 {
		ErrorResponse(c, http.StatusBadRequest, "step must be a whole number of seconds", nil)
		return
	}
	stepSec := int64(step / time.Second)
	promStep := fmt.Sprintf("%ds", stepSec)
	if (end-start)/stepSec+1 > vpnTrafficMaxPoints {
		ErrorResponse(c, http.StatusBadRequest, "Too many points: use a larger step", nil)
		return
	}
	// The GPFS counters change once a minute and are scraped every 30 s: a rate needs at least two changes
	window := stepSec
	if window < 180 {
		window = 180
	}
	_, queries, poolNames, err := storageClusterAdmin.StorageMetricQueries(ctx, c.Param("id"), window)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid storage cluster", err)
		return
	}
	resp := &StorageMetricsResponse{Start: start, End: end, Step: promStep, Timestamps: []int64{}, Charts: []*StorageMetricChart{}}
	index := map[int64]int{}
	for t := start; t <= end; t += stepSec {
		index[t] = len(resp.Timestamps)
		resp.Timestamps = append(resp.Timestamps, t)
	}
	charts := map[string]*StorageMetricChart{}
	for _, q := range queries {
		result, qerr := queryPrometheus(ctx, PrometheusRangeURL, q.Query, strconv.FormatInt(start, 10), strconv.FormatInt(end, 10), promStep)
		if qerr != nil || result.Status != "success" {
			ErrorResponse(c, http.StatusInternalServerError, "Failed to query storage metrics", qerr)
			return
		}
		for _, r := range result.Data.Result {
			label := ""
			if q.Split != "" {
				label = r.Metric[q.Split]
				if q.Split == "pool" {
					// A pool deleted since is left out; the curve is named after the pool
					name, ok := poolNames[label]
					if !ok {
						continue
					}
					label = name
				}
			}
			s := &StorageMetricSeries{Key: q.Series, Label: label, Values: make([]*float64, len(resp.Timestamps))}
			found := false
			for _, point := range r.Values {
				if len(point) != 2 {
					continue
				}
				ts, tok := point[0].(float64)
				str, vok := point[1].(string)
				if !tok || !vok {
					continue
				}
				i, ok := index[int64(math.Round(ts))]
				if !ok {
					continue
				}
				value, perr := strconv.ParseFloat(str, 64)
				if perr != nil || math.IsNaN(value) || math.IsInf(value, 0) {
					continue
				}
				s.Values[i] = &value
				found = true
			}
			if !found {
				continue
			}
			chart := charts[q.Chart]
			if chart == nil {
				chart = &StorageMetricChart{Key: q.Chart, Unit: services.StorageChartUnit(q.Chart), Series: []*StorageMetricSeries{}}
				charts[q.Chart] = chart
				resp.Charts = append(resp.Charts, chart)
			}
			chart.Series = append(chart.Series, s)
		}
	}
	// Curves split by a label in a steady order: by key as the backend listed them, then by label
	for _, chart := range resp.Charts {
		order := map[string]int{}
		for i, q := range queries {
			if q.Chart == chart.Key {
				if _, seen := order[q.Series]; !seen {
					order[q.Series] = i
				}
			}
		}
		sort.SliceStable(chart.Series, func(i, j int) bool {
			a, b := chart.Series[i], chart.Series[j]
			if order[a.Key] != order[b.Key] {
				return order[a.Key] < order[b.Key]
			}
			return a.Label < b.Label
		})
	}
	c.JSON(http.StatusOK, resp)
}
