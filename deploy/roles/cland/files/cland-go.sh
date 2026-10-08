#!/bin/bash
# 仅在管理 VIP 位于本机时运行 cland-go：计算节点经 VIP 连入，clapi 连接本机 127.0.0.1:5006，
# 与 Docker HA 中 keepalived 在 BACKUP 上停止容器的行为一致

[ -z "$MANAGEMENT_VIP" ] && MANAGEMENT_VIP=127.0.0.1
while true; do
    if [ "$MANAGEMENT_VIP" = "127.0.0.1" ] || ip -o addr | grep -q "\<$MANAGEMENT_VIP\>"; then
        pid=$(pidof cland-go)
        [ -z "$pid" ] && /opt/cloudland/bin/cland-go &
    else
        pid=$(pidof cland-go)
        [ -n "$pid" ] && kill $pid
    fi
    sleep 5
done
