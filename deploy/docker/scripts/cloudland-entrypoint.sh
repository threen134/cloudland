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

# 启动 scidv1 外部启动器守护进程（SCI_USE_EXTLAUNCHER=yes 要求）
# scidv1 会 fork 后 daemonize（父进程立即退出），通过检查端口 6188 确认启动
echo "==> 启动 scidv1 守护进程..."
/opt/sci/sbin/scidv1 -e -l /opt/cloudland/log -p /opt/cloudland/run
for i in $(seq 1 10); do
    if ss -tln 2>/dev/null | grep -q ':6188 ' || netstat -tln 2>/dev/null | grep -q ':6188 '; then
        echo "    scidv1 已启动，监听端口 6188"
        break
    fi
    if [ "$i" -eq 10 ]; then
        echo "    [WARN] scidv1 启动超时（端口 6188 未监听），继续尝试启动 cloudland..."
    fi
    sleep 0.5
done

exec /opt/cloudland/bin/cloudland
