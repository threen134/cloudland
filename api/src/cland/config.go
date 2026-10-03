/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package cland

import "os"

type Config struct {
	GRPCListen    string // gRPC listen address
	ClapiEndpoint string // clapi HTTP endpoint for callbacks
	SSHKeyDir     string // SSH key directory for NodeAdd
	AuthToken     string // shared auth token for gRPC connections
	AuthDisabled  bool   // local development only: run without a token; SSH keys are not distributed
}

func LoadConfig() *Config {
	cfg := &Config{
		GRPCListen:    envOrDefault("GRPC_LISTEN", "0.0.0.0:5006"),
		ClapiEndpoint: envOrDefault("CLAPI_ENDPOINT", "http://localhost:5005"),
		SSHKeyDir:     envOrDefault("CLOUDLAND_SSH_KEY_DIR", "/opt/cloudland/deploy/.ssh"),
		AuthToken:     os.Getenv("GRPC_AUTH_TOKEN"),
		AuthDisabled:  os.Getenv("GRPC_AUTH_DISABLED") == "true",
	}
	return cfg
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
