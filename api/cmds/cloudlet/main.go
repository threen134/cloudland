/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

cloudlet-go: gRPC-based cloudlet agent replacing C++ cloudlet + scidv1 dependency.
Runs on each compute node as a systemd service.

Usage:

	cloudlet-go            run the agent
	cloudlet-go node-add   admit this node with cland and print the SSH key pair as JSON
*/

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"time"

	"api/src/cloudlet"
	pb "api/src/proto/cloudlandpb"
	"api/src/utils/grpcauth"
	"api/src/utils/tracing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

var errNodeRemoved = errors.New("node removed by cland")

// Version is injected at build time via -ldflags "-X main.Version=..."
var Version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	flushTracing := tracing.Init(context.Background(), "cloudlet", Version)

	clandAddr := os.Getenv("CLAND_ENDPOINT")
	if clandAddr == "" {
		clandAddr = "localhost:5006"
	}
	nodeIDStr := os.Getenv("NODE_ID")
	if nodeIDStr == "" {
		nodeIDStr = os.Getenv("SCI_CLIENT_ID") // backward compat
	}
	parsedID, err := strconv.ParseInt(nodeIDStr, 10, 32)
	if err != nil || parsedID < 0 {
		log.Fatalf("Invalid NODE_ID (or SCI_CLIENT_ID): %s", nodeIDStr)
	}
	nodeID := int32(parsedID)
	hostname, _ := os.Hostname()
	if h := os.Getenv("HOSTNAME"); h != "" {
		hostname = h
	}

	// One connection for the life of the process; gRPC re-dials it after failures.
	conn, err := grpc.NewClient(clandAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpcauth.DialOption(os.Getenv("GRPC_AUTH_TOKEN")),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		log.Fatalf("Invalid cland endpoint %s: %v", clandAddr, err)
	}
	defer conn.Close()

	if len(os.Args) > 1 && os.Args[1] == "node-add" {
		code := runNodeAdd(conn, nodeID, hostname)
		flushTracing()
		os.Exit(code)
	}

	// 1 matches the C++ cloudlet: commands for this node run one at a time, in order.
	concurrency := 1
	if v := os.Getenv("CLOUDLET_CONCURRENCY"); v != "" {
		if concurrency, err = strconv.Atoi(v); err != nil || concurrency < 1 {
			log.Fatalf("Invalid CLOUDLET_CONCURRENCY: %s", v)
		}
	}

	log.Printf("cloudlet-go starting: cland=%s nodeID=%d hostname=%s concurrency=%d", clandAddr, nodeID, hostname, concurrency)

	client := pb.NewCloudletServiceClient(conn)
	sender := cloudlet.NewStreamSender()
	queue := cloudlet.NewCommandQueue(concurrency)
	go cloudlet.NewHealthReporter(client, sender, nodeID, hostname).Run(context.Background())

	retry := backoff{base: time.Second, max: time.Minute}
	for {
		registered, err := serve(client, sender, queue, nodeID, hostname)
		if errors.Is(err, errNodeRemoved) {
			log.Printf("Node removed from cland, exiting")
			flushTracing()
			return
		}
		if registered {
			retry.reset()
		}
		delay := retry.next()
		log.Printf("Connection lost: %v, reconnecting in %v...", err, delay)
		time.Sleep(delay)
	}
}

// serve registers with cland and queues received commands until the stream breaks.
// registered reports whether registration succeeded before the stream ended.
func serve(client pb.CloudletServiceClient, sender *cloudlet.StreamSender, queue *cloudlet.CommandQueue, nodeID int32, hostname string) (registered bool, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := client.CommandStream(ctx)
	if err != nil {
		return false, err
	}

	if err := stream.Send(&pb.CloudletMessage{
		Payload: &pb.CloudletMessage_Register{
			Register: &pb.RegisterRequest{NodeId: nodeID, Hostname: hostname},
		},
	}); err != nil {
		return false, err
	}

	ackMsg, err := stream.Recv()
	if err != nil {
		return false, err
	}
	if ack := ackMsg.GetRegisterAck(); ack == nil || !ack.Success {
		return false, fmt.Errorf("registration rejected: %s", ack.GetError())
	}
	header, err := stream.Header()
	if err != nil {
		return false, err
	}
	sessions := header.Get(grpcauth.SessionHeader)
	if len(sessions) != 1 {
		return false, errors.New("cland did not issue a session for the command stream")
	}

	log.Printf("Registered with cland successfully")

	// Commands still running from an earlier connection send their output on this stream.
	sender.Attach(stream, sessions[0])
	defer sender.Detach(stream)

	for {
		msg, err := stream.Recv()
		if err != nil {
			return true, err
		}

		switch p := msg.Payload.(type) {
		case *pb.ClandMessage_Command:
			req := p.Command
			queue.Push(func() { cloudlet.ExecuteCommand(sender, req, nodeID) })

		case *pb.ClandMessage_File:
			// Written inline so chunks of one file are applied in order.
			cloudlet.ReceiveFile(p.File)

		case *pb.ClandMessage_Shutdown:
			if p.Shutdown.Reason == "node_removed" {
				return true, errNodeRemoved
			}
			log.Printf("Ignoring shutdown notice from cland: %s", p.Shutdown.Reason)
		}
	}
}

// runNodeAdd implements `cloudlet-go node-add` for the deploy script: cland verifies the
// node against clapi and returns the cland SSH key pair, printed to stdout as JSON.
// Replaces the HTTP POST /internal/node/add call of the C++ control plane.
func runNodeAdd(conn *grpc.ClientConn, nodeID int32, hostname string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	reply, err := pb.NewClandServiceClient(conn).NodeAdd(ctx, &pb.NodeAddRequest{
		Hostname: hostname,
		Id:       nodeID,
		Level:    1,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "node-add failed: %v\n", err)
		return 1
	}

	out := map[string]interface{}{
		"status":      reply.Status,
		"id":          reply.Id,
		"public_key":  reply.PublicKey,
		"private_key": reply.PrivateKey,
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "node-add: write reply: %v\n", err)
		return 1
	}
	return 0
}

// backoff is exponential with up to 25% jitter, capped at max.
type backoff struct {
	base, max, cur time.Duration
}

func (b *backoff) reset() {
	b.cur = 0
}

func (b *backoff) next() time.Duration {
	switch {
	case b.cur == 0:
		b.cur = b.base
	case b.cur < b.max:
		b.cur *= 2
		if b.cur > b.max {
			b.cur = b.max
		}
	}
	return b.cur + time.Duration(rand.Int63n(int64(b.cur)/4+1))
}
