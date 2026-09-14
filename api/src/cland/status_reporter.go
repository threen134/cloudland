/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

Periodic status reports to clapi.
Ported from: src/filter/scheduler.cpp report_thread (report_availibility + report_topology)
*/

package cland

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	statusReportInterval = 5 * time.Second // report_thread sleeps 5 seconds between reports
	totalRefreshInterval = time.Minute
	// startupGrace is how long a node clapi lists as active may take to connect after
	// cland starts before it is reported offline.
	startupGrace = 90 * time.Second

	topologyStatusOnline  = 1
	topologyStatusOffline = 10 // SCI recovery state; clapi marks the hyper unavailable
)

type StatusReporter struct {
	registry  *NodeRegistry
	scheduler *Scheduler
	callback  *CallbackForwarder
	port      string
	hostname  string
	startedAt time.Time

	mu           sync.Mutex
	lastTopology string // topology last delivered to clapi or in flight; "" forces the next report
	topologyBusy bool   // a topology report is in flight
	lastTotal    string
	lastTotalAt  time.Time
}

func NewStatusReporter(registry *NodeRegistry, scheduler *Scheduler, callback *CallbackForwarder, port, hostname string) *StatusReporter {
	return &StatusReporter{
		registry:  registry,
		scheduler: scheduler,
		callback:  callback,
		port:      port,
		hostname:  hostname,
		startedAt: time.Now(),
	}
}

// Run reports aggregate resources and topology changes until ctx is done.
func (r *StatusReporter) Run(ctx context.Context) {
	ticker := time.NewTicker(statusReportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reportTotal()
			r.reportTopology(0, false)
		}
	}
}

// ReportTopology reports the node topology immediately (toall=agent / inter=-1).
func (r *StatusReporter) ReportTopology(msgID int32) {
	r.reportTopology(msgID, true)
}

// reportTopology sends control "callback=agent" with one "hostid,hostname,status" line
// per known node. A node is reported with status 10 when it disconnected, or when clapi
// lists it as active but it has not connected within startupGrace.
// Like C++ report_topology, which reported every round while SCI had nodes in recovery,
// the topology is repeated while any node is offline; otherwise it is only reported when
// forced or changed. A report that clapi did not receive is repeated on the next round.
// Reports are sent one at a time so that a delayed retry cannot overwrite a newer topology.
func (r *StatusReporter) reportTopology(msgID int32, force bool) {
	nodes := r.registry.Known()
	if len(nodes) == 0 {
		return
	}
	pastGrace := time.Since(r.startedAt) >= startupGrace
	offline := false
	var b strings.Builder
	for _, n := range nodes {
		status := topologyStatusOnline
		if !n.Online && (n.Seen || pastGrace) {
			status = topologyStatusOffline
			offline = true
		}
		fmt.Fprintf(&b, "%d,%s,%d\n", n.ID, n.Hostname, status)
	}
	message := b.String()

	r.mu.Lock()
	changed := message != r.lastTopology
	if r.topologyBusy {
		// Report it on a later round, once the report in flight has settled. clapi does
		// not use the msg_id of a topology report, so a forced one may go out as msg_id 0.
		if changed || force {
			r.lastTopology = ""
		}
		r.mu.Unlock()
		return
	}
	if !changed && !force && !offline {
		r.mu.Unlock()
		return
	}
	r.lastTopology = message
	r.topologyBusy = true
	r.mu.Unlock()

	control := fmt.Sprintf("callback=agent id=-1 port=%s num=%d hostname=%s", r.port, len(nodes), r.hostname)
	done := func(err error) { r.topologyDone(message, err) }
	if !r.callback.TryEnqueue(context.Background(), msgID, -1, control, message, done) {
		done(errCallbackQueueFull)
	}
}

// topologyDone ends the report in flight. When it was not delivered and no newer topology
// replaced it, lastTopology is cleared so the next round sends the topology again.
func (r *StatusReporter) topologyDone(message string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.topologyBusy = false
	if err != nil && r.lastTopology == message {
		r.lastTopology = ""
	}
}

// reportTotal sends the resources of connected nodes as a report_rc.sh callback with
// msg_id -1, which clapi stores as the hostid=-1 resource row.
func (r *StatusReporter) reportTotal() {
	t, ok := r.scheduler.Total(r.registry.AllNodeIDs())
	if !ok {
		return
	}
	message := fmt.Sprintf("report_rc.sh 'cpu=%d/%d' 'memory=%d/%d' 'disk=%d/%d' 'network=%d/%d' 'load=%d/%d'",
		t.CPU, t.CPUTotal, t.Memory, t.MemoryTotal, t.Disk, t.DiskTotal, t.Network, t.NetworkTotal, t.Load, t.LoadTotal)

	r.mu.Lock()
	send := message != r.lastTotal || time.Since(r.lastTotalAt) >= totalRefreshInterval
	if send {
		r.lastTotal = message
		r.lastTotalAt = time.Now()
	}
	r.mu.Unlock()
	if send && !r.callback.TryEnqueue(context.Background(), -1, -1, "report", message, nil) {
		// Retried on the next round.
		r.mu.Lock()
		if r.lastTotal == message {
			r.lastTotal = ""
		}
		r.mu.Unlock()
	}
}
