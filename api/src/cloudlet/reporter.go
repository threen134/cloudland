/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

Health reporter — periodically runs report_rc.sh and sends results to cland.
Ported from: cloudlet.cpp lines 259-311
*/

package cloudlet

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os/exec"
	"strconv"
	"strings"
	"time"

	pb "api/src/proto/cloudlandpb"
	"api/src/utils/grpcauth"
	"api/src/utils/tracing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	reportCmd     = "/opt/cloudland/scripts/backend/report_rc.sh"
	reportTimeout = 30 * time.Second
	// reportSessionWait bounds how long a finished report waits for the command stream to
	// reconnect. report_rc.sh has already recorded the report as sent, so it waits as long as
	// command output does, which spans several reconnect attempts at cloudlet-go's longest
	// backoff (1 minute plus jitter).
	reportSessionWait = sendWaitTimeout
)

type HealthReporter struct {
	client   pb.CloudletServiceClient
	sender   *StreamSender
	nodeID   int32
	hostname string
}

func NewHealthReporter(client pb.CloudletServiceClient, sender *StreamSender, nodeID int32, hostname string) *HealthReporter {
	return &HealthReporter{client: client, sender: sender, nodeID: nodeID, hostname: hostname}
}

// Run executes report_rc.sh every 1-20 seconds (matching the C++ cloudlet) for the life of
// the process, so two report runs never overlap across reconnects.
// First line → structured resource report; subsequent lines → callback_lines.
// Reports are skipped while disconnected: report_rc.sh records what it has sent (it removes
// *.done files and saves the instance list), so a report that cannot be delivered would
// suppress the next one.
func (h *HealthReporter) Run(ctx context.Context) {
	for {
		if h.sender.Connected() {
			if report := runReportScript(h.nodeID, h.hostname); report != nil {
				if err := h.deliver(ctx, report); err != nil {
					tracing.Logf(ctx, "ReportHealth failed: %v", err)
				}
			}
		}

		sleepDuration := time.Duration(rand.Intn(20)+1) * time.Second
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleepDuration):
		}
	}
}

// deliver sends report with the session that is current once report_rc.sh has finished:
// the script can run long enough for cland to restart and the stream to reconnect with a
// new session. While disconnected it waits up to reportSessionWait for a reconnect. A report
// rejected for its session, or lost with the connection, is retried once on a new session.
func (h *HealthReporter) deliver(ctx context.Context, report *pb.HealthReport) error {
	session := h.sender.WaitSession(ctx, "", reportSessionWait)
	if session == "" {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("no stream to cland within %v", reportSessionWait)
	}
	err := h.send(ctx, report, session)
	if code := status.Code(err); code != codes.PermissionDenied && code != codes.Unavailable {
		return err
	}
	next := h.sender.WaitSession(ctx, session, reportSessionWait)
	if next == "" {
		return err
	}
	tracing.Logf(ctx, "ReportHealth on a replaced session failed (%v), retrying on the new stream", err)
	return h.send(ctx, report, next)
}

func (h *HealthReporter) send(ctx context.Context, report *pb.HealthReport, session string) error {
	rctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	// cland accepts a report only with the session of this node's command stream
	rctx = metadata.AppendToOutgoingContext(rctx, grpcauth.SessionHeader, session)
	_, err := h.client.ReportHealth(rctx, report)
	return err
}

func runReportScript(nodeID int32, hostname string) *pb.HealthReport {
	cmd := exec.Command("sudo", "-E", reportCmd)
	cmd.Env = CommandEnv(nodeID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("report: failed to create pipe: %v", err)
		return nil
	}
	if err := cmd.Start(); err != nil {
		log.Printf("report: failed to start %s: %v", reportCmd, err)
		return nil
	}

	report := &pb.HealthReport{
		NodeId:   nodeID,
		Hostname: hostname,
	}
	first := true
	scanLines(stdout, func(line string) {
		if first {
			// First line: resource report (cpu=X/Y memory=X/Y ...)
			first = false
			report.RawReport = line
			parseResourceLine(line, report)
			return
		}
		// Callback lines: strip the |:-COMMAND-:| marker like C++ frontHandler did
		if command, isCallback := ParseCallbackLine(line); isCallback {
			line = command
		}
		if strings.TrimSpace(line) != "" {
			report.CallbackLines = append(report.CallbackLines, line)
		}
	})

	if err := cmd.Wait(); err != nil {
		log.Printf("report: %s exited with error: %v", reportCmd, err)
	}
	return report
}

// parseResourceLine parses "cpu=X/Y memory=X/Y disk=X/Y network=X/Y load=X/Y"
// Ported from scheduler.cpp getValue()
func parseResourceLine(line string, report *pb.HealthReport) {
	for _, field := range strings.Fields(line) {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		vals := strings.SplitN(parts[1], "/", 2)
		avail, _ := strconv.ParseInt(vals[0], 10, 64)
		total := avail
		if len(vals) == 2 {
			total, _ = strconv.ParseInt(vals[1], 10, 64)
		}
		switch key {
		case "cpu":
			report.CpuAvailable, report.CpuTotal = avail, total
		case "memory":
			report.MemoryAvailable, report.MemoryTotal = avail, total
		case "disk":
			report.DiskAvailable, report.DiskTotal = avail, total
		case "network":
			report.NetworkAvailable, report.NetworkTotal = avail, total
		case "load":
			report.LoadAvailable, report.LoadTotal = avail, total
		}
	}
}
