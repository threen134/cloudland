/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

cland-go: gRPC-based cland daemon replacing C++ cloudland + scidv1.
*/

package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"api/src/cland"
	pb "api/src/proto/cloudlandpb"
	"api/src/utils/grpcauth"
	"api/src/utils/tracing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// Version is injected at build time via -ldflags "-X main.Version=..."
var Version = "dev"

// shutdownTimeout bounds GracefulStop: CommandStream never ends on its own.
const shutdownTimeout = 5 * time.Second

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("cland-go starting...")

	flushTracing := tracing.Init(context.Background(), "cland", Version)

	cfg := cland.LoadConfig()
	switch {
	case cfg.AuthToken != "":
		log.Println("gRPC auth enabled (GRPC_AUTH_TOKEN set)")
	case cfg.AuthDisabled:
		log.Println("WARNING: gRPC auth disabled (GRPC_AUTH_DISABLED=true); for local development only")
	default:
		log.Fatal("GRPC_AUTH_TOKEN is required: without it anyone reaching the gRPC port can run commands on compute nodes " +
			"(set GRPC_AUTH_DISABLED=true only for local development)")
	}

	server := cland.NewServer(cfg)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go server.SyncValidNodes(ctx)
	go server.Status.Run(ctx)

	opts := []grpc.ServerOption{
		// CommandStream 为长连接、ReportHealth 为周期上报，不产生 span；单条命令由 dispatcher/executor 手动创建 span
		tracing.GRPCServerOption("CommandStream", "ReportHealth"),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}
	opts = append(opts, grpcauth.ServerOptions(cfg.AuthToken)...)

	grpcServer := grpc.NewServer(opts...)

	// Register both services on the same gRPC server
	pb.RegisterClandServiceServer(grpcServer, server)
	pb.RegisterCloudletServiceServer(grpcServer, server)

	lis, err := net.Listen("tcp", cfg.GRPCListen)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", cfg.GRPCListen, err)
	}
	log.Printf("cland-go listening on %s (clapi callback: %s)", cfg.GRPCListen, cfg.ClapiEndpoint)

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		stop()

		// Cloudlets are not told to shut down: they keep reconnecting until cland is back.
		done := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(shutdownTimeout):
			grpcServer.Stop()
		}
	}()

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("gRPC server failed: %v", err)
	}
	flushTracing()
}
