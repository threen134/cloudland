/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

History:
   Date     Who ID    Description
   -------- --- ---   -----------
   01/13/19 nanjj  Initial code

*/

package common

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "api/src/proto/cloudlandpb"
	"api/src/utils/grpcauth"
	"api/src/utils/tracing"
)

// clandRPCTimeout bounds calls to cland so a stuck control plane cannot hang API requests.
const clandRPCTimeout = 30 * time.Second

// Legacy types kept for compatibility with frontback.go json.Unmarshal
type ExecuteRequest struct {
	Id      int32
	Extra   int32
	Control string
	Command string
}

type ExecuteReply struct {
	Status string
}

var (
	clandClient pb.ClandServiceClient
	clandConn   *grpc.ClientConn
	clientOnce  sync.Once
)

// clandEndpoint returns sci.endpoint as a gRPC host:port target. Configs written for the
// C++ HTTP API carry an http:// URL, which the gRPC resolver rejects.
func clandEndpoint() string {
	endpoint := strings.TrimSpace(viper.GetString("sci.endpoint"))
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimSuffix(endpoint, "/")
	if endpoint == "" {
		endpoint = "localhost:5006"
	}
	return endpoint
}

// ClandToken returns the shared gRPC token for cland; GRPC_AUTH_TOKEN overrides sci.token.
func ClandToken() string {
	if token := os.Getenv("GRPC_AUTH_TOKEN"); token != "" {
		return token
	}
	return viper.GetString("sci.token")
}

func getClandClient() pb.ClandServiceClient {
	clientOnce.Do(func() {
		endpoint := clandEndpoint()
		var err error
		clandConn, err = grpc.NewClient(endpoint,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpcauth.DialOption(ClandToken()),
			tracing.GRPCDialOption(),
		)
		if err != nil {
			log.Fatalf("Failed to connect to cland at %s: %v", endpoint, err)
		}
		clandClient = pb.NewClandServiceClient(clandConn)
		log.Printf("Connected to cland-go at %s via gRPC", endpoint)
	})
	return clandClient
}

func NodeRemove(hostID int32) error {
	ctx, cancel := context.WithTimeout(context.Background(), clandRPCTimeout)
	defer cancel()
	client := getClandClient()
	_, err := client.NodeRemove(ctx, &pb.NodeRemoveRequest{
		Id: hostID,
	})
	if err != nil {
		return NewCLError(ErrExecuteOnHyperFailed, fmt.Sprintf("Failed to remove node %d via gRPC", hostID), err)
	}
	logger.Ctx(ctx).Debugf("NodeRemove: hostID=%d", hostID)
	return nil
}

func HyperExecute(ctx context.Context, control, command string) (err error) {
	client := getClandClient()

	// trace 上下文由 otelgrpc 经 gRPC metadata 传播
	logger.Ctx(ctx).Debugf("HyperExecute: control=%s", control)

	// 不跟随调用方取消：API 已写库后，即使 HTTP 客户端断开也必须把命令下发出去（保留 trace/request id）
	rpcCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clandRPCTimeout)
	defer cancel()

	reply, err := client.Execute(rpcCtx, &pb.ExecuteRequest{
		Id:      100,
		Extra:   0,
		Control: control,
		Command: command,
	})
	if err != nil {
		logger.Ctx(ctx).Error("HyperExecute gRPC error:", err)
		return NewCLError(ErrExecuteOnHyperFailed, "gRPC Execute failed", err)
	}
	// 与 C++ 版一致不视为失败（如节点离线），但记录下来便于排查
	if reply.GetStatus() != "ok" {
		logger.Ctx(ctx).Warningf("HyperExecute: cland replied %q for control=%s", reply.GetStatus(), control)
	}
	return nil
}
