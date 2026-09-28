/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package cloudlet

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	pb "api/src/proto/cloudlandpb"
)

// Two additions for fast VPN gateway takeover (docs/architecture/plan/vpn-gateway-plan.md §8.2, F3 and F5).

// AliveCallback is sent on the command stream every second; cland consumes it (cland.AliveCallback) and
// tells clapi within seconds when a node goes silent, instead of the 30-40 s of the gRPC keepalive.
const AliveCallback = "__cloudlet_alive__"

// RunAlive sends AliveCallback every second while a stream is attached
func RunAlive(ctx context.Context, sender *StreamSender, nodeID int32) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !sender.Connected() {
			continue
		}
		_ = sender.Send(&pb.CloudletMessage{
			Payload: &pb.CloudletMessage_Callback{Callback: &pb.CallbackLine{NodeId: nodeID, Command: AliveCallback}},
		})
	}
}

const (
	// Touched by the VPN scripts (vpn_lib.sh vpn_trigger_report) when a gateway changed role or a tunnel
	// changed state: clapi should know now, not at the next heartbeat (1-20 s)
	vpnReportTrigger   = "/run/cloudland/vpn-report.trigger"
	vpnReportPoll      = 250 * time.Millisecond
	vpnReportMinPeriod = time.Second
	vpnReportTimeout   = 30 * time.Second
)

var vpnReportCmd = filepath.Join(filepath.Dir(reportCmd), "report_vpn_status.sh")

// RunVpnReportTrigger runs report_vpn_status.sh whenever the trigger file changes and sends its callback
// lines on the command stream, like the output of a command. The resource line and everything else of the
// heartbeat stay with report_rc.sh.
func RunVpnReportTrigger(ctx context.Context, sender *StreamSender, nodeID int32) {
	var lastMod time.Time
	if st, err := os.Stat(vpnReportTrigger); err == nil {
		lastMod = st.ModTime()
	}
	var lastRun time.Time
	ticker := time.NewTicker(vpnReportPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		st, err := os.Stat(vpnReportTrigger)
		if err != nil || !st.ModTime().After(lastMod) || time.Since(lastRun) < vpnReportMinPeriod {
			continue
		}
		lastMod, lastRun = st.ModTime(), time.Now()
		if sender.Connected() {
			runVpnReport(ctx, sender, nodeID)
		}
	}
}

func runVpnReport(ctx context.Context, sender *StreamSender, nodeID int32) {
	ctx, cancel := context.WithTimeout(ctx, vpnReportTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sudo", "-E", vpnReportCmd)
	cmd.Env = CommandEnv(nodeID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("vpn report: failed to create pipe: %v", err)
		return
	}
	if err = cmd.Start(); err != nil {
		log.Printf("vpn report: failed to start %s: %v", vpnReportCmd, err)
		return
	}
	scanLines(stdout, func(line string) {
		command, isCallback := ParseCallbackLine(line)
		if !isCallback {
			return
		}
		if err := sender.Send(&pb.CloudletMessage{
			Payload: &pb.CloudletMessage_Callback{Callback: &pb.CallbackLine{NodeId: nodeID, Command: command}},
		}); err != nil {
			log.Printf("vpn report: send failed: %v", err)
		}
	})
	_ = cmd.Wait()
}
