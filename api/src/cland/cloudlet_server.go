/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

CloudletService implementation — handles cloudlet → cland gRPC streams.
Tracks connected agents and routes commands and results between them and clapi.
*/

package cland

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	validNodesRefreshInterval = 5 * time.Minute
	hyperStatusActive         = 1
)

// validNode is one entry of clapi's GET /internal/nodes/valid.
type validNode struct {
	Hostid   int32  `json:"hostid"`
	Hostname string `json:"hostname"`
	Status   int32  `json:"status"`
}

// SyncValidNodes loads valid nodes from clapi, retrying until clapi is up, then refreshes
// them periodically so nodes deleted in clapi stop being admitted. Until the first load
// succeeds, registrations fall back to per-node verification.
func (s *Server) SyncValidNodes(ctx context.Context) {
	for attempt := 1; ; attempt++ {
		err := s.refreshValidNodes()
		if err == nil {
			break
		}
		tracing.Logf(ctx, "refreshValidNodes attempt %d failed: %v", attempt, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(min(attempt, 10)) * 3 * time.Second):
		}
	}

	ticker := time.NewTicker(validNodesRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.refreshValidNodes(); err != nil {
				tracing.Logf(ctx, "refreshValidNodes failed: %v", err)
			}
		}
	}
}

// refreshValidNodes replaces the valid node cache with clapi's list, served on the internal
// port next to /internal/execute. Active nodes are seeded into the topology so that nodes
// that never reconnect after a cland restart are reported offline; nodes clapi no longer
// lists are dropped from it.
func (s *Server) refreshValidNodes() error {
	// The list may not reflect nodes removed from here on.
	since := s.Registry.RemovalSeq()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(s.Config.ClapiEndpoint + "/internal/nodes/valid")
	if err != nil {
		return fmt.Errorf("failed to fetch valid nodes: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("clapi returned %d", resp.StatusCode)
	}
	var nodes []validNode
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		return fmt.Errorf("failed to decode valid nodes: %w", err)
	}

	listed := make([]int32, 0, len(nodes))
	for _, n := range nodes {
		listed = append(listed, n.Hostid)
	}
	ids := s.Registry.Readmit(listed, since)
	for _, n := range nodes {
		if n.Status == hyperStatusActive {
			s.Registry.Seed(n.Hostid, n.Hostname) // skips nodes that are still removed
		}
	}
	for _, id := range s.validNodes.Replace(ids) {
		s.Registry.Forget(id)
		if _, online := s.Registry.Get(id); online {
			log.Printf("Node %d is no longer listed by clapi; it keeps its stream but cannot reconnect", id)
		}
	}
	log.Printf("Loaded %d valid nodes from clapi", len(ids))
	return nil
}

// isNodeValid checks if a node ID is valid (exists in hypers table).
// First checks in-memory cache, then falls back to clapi HTTP call.
func (s *Server) isNodeValid(nodeID int32) bool {
	if nodeID < 0 {
		return false
	}
	if s.validNodes.Has(nodeID) {
		return true
	}
	return s.verifyNode(nodeID)
}

// verifyNode asks clapi whether the hostid exists in the hypers table and caches a hit.
// A removed node is refused even if clapi answered before deleting its row.
func (s *Server) verifyNode(nodeID int32) bool {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("%s/internal/node/verify?id=%d", s.Config.ClapiEndpoint, nodeID))
	if err != nil {
		log.Printf("verify node %d: %v", nodeID, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || s.Registry.Removed(nodeID) {
		return false
	}
	s.validNodes.Add(nodeID)
	return true
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CommandStream handles bidirectional streaming between cland and a cloudlet.
// First message must be RegisterRequest; validated against clapi's hypers table.
// Subsequent messages are callbacks/results forwarded to clapi.
func (s *Server) CommandStream(stream pb.CloudletService_CommandStreamServer) error {
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	reg := msg.GetRegister()
	if reg == nil {
		return status.Error(codes.InvalidArgument, "first message must be RegisterRequest")
	}

	if !s.isNodeValid(reg.NodeId) {
		rejectRegistration(stream, "node not registered in hypers table")
		log.Printf("Rejected node %d: not found in clapi", reg.NodeId)
		return status.Errorf(codes.PermissionDenied, "node %d not registered in hypers table", reg.NodeId)
	}

	sessionID, err := newSessionID()
	if err != nil {
		return status.Errorf(codes.Internal, "create session: %v", err)
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	node := &ConnectedNode{
		ID:        reg.NodeId,
		Hostname:  reg.Hostname,
		SessionID: sessionID,
		Stream:    stream,
		Cancel:    cancel,
	}

	// Register and acknowledge under the node's send lock: the ack is the first message
	// the cloudlet receives (dispatched commands wait on the lock), and the node is
	// already registered when the cloudlet starts sending ReportHealth.
	// Lock order: sendMu, then the registry lock inside Register.
	node.sendMu.Lock()
	if !s.Registry.Register(node) {
		// NodeRemove ran between the validation above and Register.
		rejectRegistration(stream, "node removed")
		node.sendMu.Unlock()
		log.Printf("Rejected node %d: removed", reg.NodeId)
		return status.Errorf(codes.PermissionDenied, "node %d was removed", reg.NodeId)
	}
	ackErr := stream.SetHeader(metadata.Pairs(grpcauth.SessionHeader, sessionID))
	if ackErr == nil {
		ackErr = stream.Send(&pb.ClandMessage{
			Payload: &pb.ClandMessage_RegisterAck{
				RegisterAck: &pb.RegisterAck{Success: true},
			},
		})
	}
	node.sendMu.Unlock()
	defer func() {
		node.markClosed()
		if s.Registry.Unregister(node) {
			tracing.Logf(ctx, "Node %d (%s) disconnected, total=%d", node.ID, node.Hostname, s.Registry.Count())
		}
	}()
	if ackErr != nil {
		return ackErr
	}
	tracing.Logf(ctx, "Node %d (%s) registered, total=%d", node.ID, node.Hostname, s.Registry.Count())

	// Recv runs in its own goroutine so that cancel() — the node reconnected on a new
	// stream or was removed — ends this handler even while Recv is blocked.
	recvErr := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			s.handleCloudletMessage(node.ID, msg)
		}
	}()

	select {
	case err := <-recvErr:
		if err == io.EOF {
			tracing.Logf(ctx, "Node %d stream EOF", node.ID)
			return nil
		}
		tracing.Logf(ctx, "Node %d stream error: %v", node.ID, err)
		return err
	case <-ctx.Done():
		if err := stream.Context().Err(); err != nil {
			tracing.Logf(ctx, "Node %d stream closed: %v", node.ID, err)
			return err
		}
		tracing.Logf(ctx, "Node %d stream superseded by a new connection or node removed", node.ID)
		return status.Error(codes.Aborted, "stream superseded or node removed")
	}
}

func rejectRegistration(stream pb.CloudletService_CommandStreamServer, reason string) {
	_ = stream.Send(&pb.ClandMessage{
		Payload: &pb.ClandMessage_RegisterAck{
			RegisterAck: &pb.RegisterAck{Success: false, Error: reason},
		},
	})
}

// handleCloudletMessage forwards cloudlet output to clapi. The node ID comes from the
// registered stream, not the message, so a cloudlet cannot report as another node.
func (s *Server) handleCloudletMessage(nodeID int32, msg *pb.CloudletMessage) {
	switch p := msg.Payload.(type) {
	case *pb.CloudletMessage_Result:
		// Plain stdout line: frontHandler "callback" control without |:-COMMAND-:|
		r := p.Result
		s.Callback.Enqueue(tracing.Extract(context.Background(), r.TraceContext), r.MsgId, nodeID, r.Control, r.Output)
	case *pb.CloudletMessage_Callback:
		cb := p.Callback
		s.Callback.Enqueue(tracing.Extract(context.Background(), cb.TraceContext), cb.MsgId, nodeID, "callback", cb.Command)
	case *pb.CloudletMessage_Error:
		e := p.Error
		s.Callback.Enqueue(tracing.Extract(context.Background(), e.TraceContext), e.MsgId, nodeID, "error", e.Command)
	}
}

// sessionMatches reports whether the RPC carries the session issued to a node's command stream.
func sessionMatches(ctx context.Context, sessionID string) bool {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get(grpcauth.SessionHeader)
	return len(values) == 1 && grpcauth.Equal(values[0], sessionID)
}

// ReportHealth processes health reports from cloudlets. A report is accepted only with the
// session of the reporting node's command stream.
//  1. Updates the scheduler resource table. In C++ the first report_rc.sh line only fed
//     the scheduler filter and never reached clapi; clapi gets the aggregate from
//     StatusReporter instead.
//  2. Forwards callback_lines (commands already stripped of |:-COMMAND-:| by the cloudlet)
//     to clapi with control="callback" and msg_id 0, as the C++ cloudlet did.
func (s *Server) ReportHealth(ctx context.Context, r *pb.HealthReport) (*pb.HealthReportAck, error) {
	node, ok := s.Registry.Get(r.NodeId)
	if !ok || !sessionMatches(ctx, node.SessionID) {
		return nil, status.Errorf(codes.PermissionDenied, "health report for node %d does not match its command stream", r.NodeId)
	}

	s.Scheduler.UpdateResource(r.NodeId, &Resource{
		CPU:          r.CpuAvailable,
		CPUTotal:     r.CpuTotal,
		Memory:       r.MemoryAvailable,
		MemoryTotal:  r.MemoryTotal,
		Disk:         r.DiskAvailable,
		DiskTotal:    r.DiskTotal,
		Network:      r.NetworkAvailable,
		NetworkTotal: r.NetworkTotal,
		Load:         r.LoadAvailable,
		LoadTotal:    r.LoadTotal,
	})

	for _, line := range r.CallbackLines {
		if line = strings.TrimSpace(line); line != "" {
			s.Callback.Enqueue(context.Background(), 0, r.NodeId, "callback", line)
		}
	}

	return &pb.HealthReportAck{Accepted: true}, nil
}
