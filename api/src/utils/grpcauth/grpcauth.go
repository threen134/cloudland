/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

Shared-token authentication for the clapi → cland and cloudlet → cland gRPC channels.
*/

package grpcauth

import (
	"context"
	"crypto/subtle"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// SessionHeader carries the session ID cland issues for a cloudlet's command stream
// (in the stream response header); the cloudlet presents it on ReportHealth.
const SessionHeader = "x-cloudland-session"

type bearerToken string

var _ credentials.PerRPCCredentials = bearerToken("")

func (t bearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(t)}, nil
}

// RequireTransportSecurity is false: the channels are plaintext gRPC until mTLS is added.
func (t bearerToken) RequireTransportSecurity() bool {
	return false
}

// DialOption attaches token to every RPC on the connection; an empty token adds nothing.
func DialOption(token string) grpc.DialOption {
	if token == "" {
		return grpc.EmptyDialOption{}
	}
	return grpc.WithPerRPCCredentials(bearerToken(token))
}

// ServerOptions returns interceptors that reject RPCs without "authorization: Bearer <token>".
// An empty token disables authentication.
func ServerOptions(token string) []grpc.ServerOption {
	if token == "" {
		return nil
	}
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if err := Validate(ctx, token); err != nil {
				return nil, err
			}
			return handler(ctx, req)
		}),
		grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if err := Validate(ss.Context(), token); err != nil {
				return err
			}
			return handler(srv, ss)
		}),
	}
}

// Validate checks the bearer token in the incoming metadata.
func Validate(ctx context.Context, expected string) error {
	if expected == "" {
		return nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	if len(values) == 0 || !Equal(values[0], "Bearer "+expected) {
		return status.Error(codes.Unauthenticated, "invalid auth token")
	}
	return nil
}

// Equal compares two secrets in constant time.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
