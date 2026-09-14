/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

ClandService implementation — handles clapi → cland gRPC calls.
Replaces C++ rpcworker.cpp HTTP endpoints.
*/

package cland

import (
	"context"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	pb "api/src/proto/cloudlandpb"
	"api/src/utils/tracing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// nodeRemoveGrace keeps a removed node's stream open long enough to deliver its ShutdownRequest.
const nodeRemoveGrace = 5 * time.Second

type Server struct {
	pb.UnimplementedClandServiceServer
	pb.UnimplementedCloudletServiceServer

	Config     *Config
	Registry   *NodeRegistry
	GroupMgr   *GroupManager
	Scheduler  *Scheduler
	Dispatcher *Dispatcher
	Callback   *CallbackForwarder
	Status     *StatusReporter

	validNodes *nodeCache // hostids known to clapi, for node validation
}

func NewServer(cfg *Config) *Server {
	registry := NewNodeRegistry()
	groupMgr := NewGroupManager()
	scheduler := NewScheduler()
	callback := NewCallbackForwarder(cfg.ClapiEndpoint)
	dispatcher := NewDispatcher(registry, groupMgr, scheduler, callback)

	_, port, _ := net.SplitHostPort(cfg.GRPCListen)
	hostname, _ := os.Hostname()
	statusReporter := NewStatusReporter(registry, scheduler, callback, port, hostname)
	dispatcher.status = statusReporter

	return &Server{
		Config:     cfg,
		Registry:   registry,
		GroupMgr:   groupMgr,
		Scheduler:  scheduler,
		Dispatcher: dispatcher,
		Callback:   callback,
		Status:     statusReporter,
		validNodes: newNodeCache(),
	}
}

// Execute dispatches a command to the appropriate cloudlet(s).
// Replaces POST /internal/execute.
func (s *Server) Execute(ctx context.Context, req *pb.ExecuteRequest) (*pb.ExecuteReply, error) {
	tracing.Logf(ctx, "Execute: id=%d extra=%d control=%s trace=%s",
		req.Id, req.Extra, req.Control, tracing.TraceID(ctx))
	return s.Dispatcher.Dispatch(ctx, req)
}

// NodeAdd admits a node and returns the SSH key pair distributed to compute nodes.
// Replaces POST /internal/node/add; called by `cloudlet-go node-add` from the deploy script.
// The command stream itself is established when the cloudlet connects via CommandStream.
func (s *Server) NodeAdd(ctx context.Context, req *pb.NodeAddRequest) (*pb.NodeAddReply, error) {
	tracing.Logf(ctx, "NodeAdd: hostname=%s id=%d level=%d", req.Hostname, req.Id, req.Level)

	if req.Hostname == "" || req.Id < 0 {
		return nil, status.Error(codes.InvalidArgument, "hostname and non-negative id required")
	}
	// Always ask clapi rather than the cache: this call hands out the private key.
	if !s.verifyNode(req.Id) {
		return nil, status.Errorf(codes.NotFound, "node %d not registered in hypers table", req.Id)
	}

	reply := &pb.NodeAddReply{Status: "ok", Id: req.Id}
	if s.Config.AuthToken == "" {
		// Without authentication anyone reaching the port could collect the key.
		tracing.Logf(ctx, "NodeAdd: gRPC auth disabled, SSH keys are not distributed")
		return reply, nil
	}

	// Read SSH keys (ported from rpcworker.cpp lines 469-489)
	keyDir := s.Config.SSHKeyDir
	pubKeyPath := keyDir + "/cland.key.pub"
	privKeyPath := keyDir + "/cland.key"

	pubKey, pubErr := os.ReadFile(pubKeyPath)
	privKey, privErr := os.ReadFile(privKeyPath)
	switch {
	case pubErr != nil || privErr != nil:
		tracing.Logf(ctx, "NodeAdd: SSH key files not readable in %s (pub: %v, priv: %v), skipping key distribution", keyDir, pubErr, privErr)
	case len(pubKey) == 0 || len(privKey) == 0:
		tracing.Logf(ctx, "NodeAdd: SSH key files in %s are empty, skipping key distribution", keyDir)
	default:
		reply.PublicKey = string(pubKey)
		reply.PrivateKey = string(privKey)
	}

	return reply, nil
}

// NodeRemove removes a node from the validation cache and disconnects it.
// Replaces POST /internal/node/remove.
func (s *Server) NodeRemove(ctx context.Context, req *pb.NodeRemoveRequest) (*pb.NodeRemoveReply, error) {
	tracing.Logf(ctx, "NodeRemove: id=%d", req.Id)

	// Remove leaves a tombstone first, so a registration racing with this call is refused.
	node := s.Registry.Remove(req.Id)
	s.validNodes.Delete(req.Id)
	s.Scheduler.RemoveResource(req.Id)

	// The cloudlet exits on node_removed; cancel the stream afterwards in case it does not.
	// The stream handler no longer finds the node registered, so the disconnect is logged here.
	if node != nil {
		tracing.Logf(ctx, "Node %d (%s) removed, disconnecting, total=%d", node.ID, node.Hostname, s.Registry.Count())
		_ = node.Send(&pb.ClandMessage{
			Payload: &pb.ClandMessage_Shutdown{
				Shutdown: &pb.ShutdownRequest{Reason: "node_removed"},
			},
		})
		time.AfterFunc(nodeRemoveGrace, node.Cancel)
	}

	return &pb.NodeRemoveReply{Status: "ok"}, nil
}

// TransmitFile handles file transfer from clapi to a compute node.
func (s *Server) TransmitFile(stream pb.ClandService_TransmitFileServer) error {
	ackStatus := "ok"
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		nodeID, err := strconv.ParseInt(extractValue(chunk.Control, "inter="), 10, 32)
		if err != nil || nodeID < 0 {
			ackStatus = "error: file transfer requires inter=<node>"
			continue
		}

		msg := &pb.ClandMessage{
			Payload: &pb.ClandMessage_File{
				File: &pb.FileTransfer{
					MsgId:        chunk.Id,
					Filepath:     chunk.Filepath,
					Filesize:     chunk.Filesize,
					Fileseek:     chunk.Fileseek,
					Content:      chunk.Content,
					Checksum:     chunk.Checksum,
					TraceContext: tracing.Inject(stream.Context()),
				},
			},
		}
		if err := s.Registry.SendTo(int32(nodeID), msg); err != nil {
			ackStatus = "error: node not connected"
		}
	}
	return stream.SendAndClose(&pb.TransmitAck{Status: ackStatus})
}

// ListGroups returns all group names.
func (s *Server) ListGroups(ctx context.Context, req *pb.ListGroupsRequest) (*pb.ListGroupsReply, error) {
	return &pb.ListGroupsReply{Groups: s.GroupMgr.List()}, nil
}
