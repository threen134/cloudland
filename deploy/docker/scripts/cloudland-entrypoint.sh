#!/bin/bash
# CloudLand 主控容器启动脚本
# 基于 deploy/roles/cland/files/cloudland.sh，适配容器环境

set -e

echo "==> CloudLand 主控服务启动"
echo "    MANAGEMENT_VIP=$MANAGEMENT_VIP"
echo "    SCI_DEVICE_NAME=$SCI_DEVICE_NAME"
echo "    SCI_LISTENER_PORT=$SCI_LISTENER_PORT"

# 生成 host.list（如果外部未挂载）
if [ ! -f /opt/cloudland/etc/host.list ]; then
    echo "警告: /opt/cloudland/etc/host.list 不存在，使用空白文件"
    touch /opt/cloudland/etc/host.list
else
    echo "==> 规范化 host.list (移除注释和空行，防止 SCI 秩映射错误)"
    sed -i '/^#/d; /^$/d' /opt/cloudland/etc/host.list
fi

# 在容器中直接启动 cloudland，不使用 VIP 检查循环
# （容器的生命周期由 Docker 管理）
exec /opt/cloudland/bin/cloudland
