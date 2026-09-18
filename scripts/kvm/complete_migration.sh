#!/bin/bash

cd `dirname $0`
source ../cloudrc

# 热迁移时本命令在源节点迁移完成后才下发，目标域通常已持久定义：同步执行以便立即回调
# （异步任务的输出要等下次心跳才转发，会拉长浮动 IP 切换时间）；否则后台轮询
if [ "$4" = "warm" ] && virsh dominfo inst-$3 2>/dev/null | grep -qE "^Persistent:\s+yes"; then
    ./async_job/$(basename $0) $*
else
    async_exec ./async_job/$(basename $0) "$@"
fi
