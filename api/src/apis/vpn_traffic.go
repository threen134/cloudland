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
	"strings"
	"time"

	. "api/src/common"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

const (
	// Prometheus refuses a range query with more than 11000 points per series
	vpnTrafficMaxPoints = 11000
	// Prometheus keeps 30 days (--storage.tsdb.retention.time=720h); a little more is allowed for rounding
	vpnTrafficMaxRange = 31 * 24 * 3600
)

// VpnTrafficSeries is the rate of one site connection or WireGuard client in bits per second, aligned with
// VpnTrafficResponse.Timestamps. A tunnel that is down still exports flat counters and shows 0; null means
// no sample at all: the gateway was not exporting then (paused, no master yet, node_exporter down)
type VpnTrafficSeries struct {
	ID   string     `json:"id"`
	Name string     `json:"name"`
	In   []*float64 `json:"in"`  // from the site / from the client
	Out  []*float64 `json:"out"` // to the site / to the client
}

type VpnTrafficResponse struct {
	Start       int64               `json:"start"`
	End         int64               `json:"end"`
	Step        string              `json:"step"`
	Timestamps  []int64             `json:"timestamps"`
	Connections []*VpnTrafficSeries `json:"connections"`
	Clients     []*VpnTrafficSeries `json:"clients"`
}

// @Summary VPN gateway traffic history
// @Description Rate of every site connection and WireGuard client of the gateway in bits per second, from the tunnel counters the gateway master exports to Prometheus. Deleted connections and clients are left out.
// @tags VPN Gateway
// @Produce json
// @Param   id    path  string true "VPN gateway UUID"
// @Param   start query string true "Start, unix seconds"
// @Param   end   query string true "End, unix seconds"
// @Param   step  query string true "Resolution as a duration, e.g. 60s or 5m"
// @Success 200 {object} VpnTrafficResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /vpn_gateways/{id}/traffic [get]
func (v *VpnGatewayAPI) Traffic(c *gin.Context) {
	ctx := c.Request.Context()
	gateway, err := vpnGatewayAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPN gateway query", err)
		return
	}
	stepStr := c.Query("step")
	start, end, err := validateAndParseTimeParams(c.Query("start"), c.Query("end"), stepStr)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, err.Error(), nil)
		return
	}
	// Bound the range before any arithmetic on it: end-start of arbitrary int64 values overflows and would
	// defeat the point limit below (the timestamps are built here, before Prometheus is asked)
	if start < 0 || end > time.Now().Unix()+3600 || end-start > vpnTrafficMaxRange {
		ErrorResponse(c, http.StatusBadRequest, "Invalid time range: at most 31 days, not ending in the future", nil)
		return
	}
	// Whole seconds only: the timestamps built here must be the ones Prometheus evaluates at, and a
	// fractional step such as 1.5m parses in Go but not in Prometheus
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
	// The counters are refreshed every heartbeat (1-20 s) and scraped every 30 s: a rate needs a few
	// samples, so at least two minutes, and a coarse step averages over the whole step instead of sampling it
	window := stepSec
	if window < 120 {
		window = 120
	}
	queries := map[string]string{
		"connection": fmt.Sprintf(`sum by (connection, direction) (rate(cloudland_vpn_connection_bytes_total{gateway_id="%d"}[%ds])) * 8`, gateway.ID, window),
		"client_key": fmt.Sprintf(`sum by (client_key, direction) (rate(cloudland_vpn_client_bytes_total{gateway_id="%d"}[%ds])) * 8`, gateway.ID, window),
	}
	resp := &VpnTrafficResponse{Start: start, End: end, Step: promStep, Connections: []*VpnTrafficSeries{}, Clients: []*VpnTrafficSeries{}}
	index := map[int64]int{}
	for t := start; t <= end; t += stepSec {
		index[t] = len(resp.Timestamps)
		resp.Timestamps = append(resp.Timestamps, t)
	}

	// Names of the objects that still exist, by the label the node exports
	names := map[string]map[string][2]string{"connection": {}, "client_key": {}}
	for _, conn := range gateway.Connections {
		names["connection"][services.VpnConnName(conn)] = [2]string{conn.UUID, conn.Name}
	}
	for _, client := range gateway.Clients {
		names["client_key"][client.PublicKey] = [2]string{client.UUID, client.Name}
	}

	for label, query := range queries {
		result, qerr := queryPrometheus(ctx, PrometheusRangeURL, query, strconv.FormatInt(start, 10), strconv.FormatInt(end, 10), promStep)
		if qerr != nil || result.Status != "success" {
			ErrorResponse(c, http.StatusInternalServerError, "Failed to query traffic metrics", qerr)
			return
		}
		series := map[string]*VpnTrafficSeries{}
		for _, r := range result.Data.Result {
			ref, ok := names[label][r.Metric[label]]
			if !ok {
				continue
			}
			s := series[ref[0]]
			if s == nil {
				s = &VpnTrafficSeries{ID: ref[0], Name: ref[1], In: make([]*float64, len(resp.Timestamps)), Out: make([]*float64, len(resp.Timestamps))}
				series[ref[0]] = s
			}
			target := s.In
			if r.Metric["direction"] == "out" {
				target = s.Out
			}
			for _, point := range r.Values {
				if len(point) != 2 {
					continue
				}
				ts, tok := point[0].(float64)
				str, vok := point[1].(string)
				if !tok || !vok {
					continue
				}
				i, found := index[int64(math.Round(ts))]
				if !found {
					continue
				}
				value, perr := strconv.ParseFloat(str, 64)
				if perr != nil || math.IsNaN(value) || math.IsInf(value, 0) {
					continue
				}
				target[i] = &value
			}
		}
		list := make([]*VpnTrafficSeries, 0, len(series))
		for _, s := range series {
			list = append(list, s)
		}
		sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
		if label == "connection" {
			resp.Connections = list
		} else {
			resp.Clients = list
		}
	}
	c.JSON(http.StatusOK, resp)
}
