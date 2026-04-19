#!/bin/bash
# CloudLand 主控容器启动脚本
# 基于 deploy/roles/cland/files/cloudland.sh，适配容器环境

set -e

echo "==> CloudLand 主控服务启动"
echo "    MANAGEMENT_VIP=$MANAGEMENT_VIP"
echo "    SCI_DEVICE_NAME=$SCI_DEVICE_NAME"
echo "    SCI_LISTENER_PORT=$SCI_LISTENER_PORT"

echo "==> API 注册模式：不使用 host.list，节点通过 API 动态注册"

# 清理旧日志，避免新旧日志混在同一文件
rm -f /opt/cloudland/log/*.log /opt/cloudland/log/*.log.* 2>/dev/null || true

exec /opt/cloudland/bin/cloudland
