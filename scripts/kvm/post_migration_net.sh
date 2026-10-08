#!/bin/bash

cd $(dirname $0)
source ../cloudrc

[ $# -lt 1 ] && die "$0 <vm_ID> [garp]"

# 虚拟机迁入本节点后的网络切换（stdout 会作为回调发给 clapi，不输出任何内容）：
# 1) 删除本节点上该虚拟机 MAC 的 VXLAN 静态转发 / 邻居条目（虚拟机在其他节点时由 add_fwrule.sh 写入），
#    以及网桥在 VXLAN 口学到的该 MAC，否则发往它的流量仍被送进隧道
# 2) garp：由本节点 VPC 路由器宣告网关。各节点路由器网关 MAC 不同（set_subnet_gw.sh 按 hostid 生成），
#    虚拟机 ARP 缓存仍指向源节点网关，源节点路由器删除后出方向会断；VXLAN 未配置泛洪条目，广播只到本节点
ID=$1
vm_ID=inst-$ID
garp=$2

virsh domiflist $vm_ID 2>/dev/null | awk 'NR > 2 && $3 ~ /^br[0-9]+$/ {print $3, $5}' | while read br mac; do
    vni=${br#br}
    [ "$vni" -lt 4095 ] && continue
    bridge fdb del $mac dev v-$vni self >/dev/null 2>&1
    bridge fdb del $mac dev v-$vni master >/dev/null 2>&1
    for ip in $(ip neigh show dev v-$vni 2>/dev/null | grep -i "lladdr $mac" | awk '{print $1}'); do
        ip neigh del $ip dev v-$vni >/dev/null 2>&1
    done
    [ "$garp" = "garp" ] || continue
    for router in $(ip netns list | awk '/^router-/ {print $1}'); do
        gateway=$(ip netns exec $router ip -o -4 addr show dev ns-$vni 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1)
        [ -z "$gateway" ] && continue
        setsid nohup sh -c "for i in 1 2 3; do ip netns exec $router arping -q -c 1 -U -I ns-$vni $gateway; sleep 1; done" >/dev/null 2>&1 &
        break
    done
done
exit 0
