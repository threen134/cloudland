#!/bin/bash
# CloudLand 主控容器启动脚本 (Go gRPC 版)
# 替代 cloudland-entrypoint.sh — 无需启动 scidv1

set -e

echo "==> cland-go 主控服务启动"
echo "    GRPC_LISTEN=$GRPC_LISTEN"
echo "    CLAPI_ENDPOINT=$CLAPI_ENDPOINT"

exec /opt/cloudland/bin/cland-go
