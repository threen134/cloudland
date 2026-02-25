#!/bin/bash

#这份 create_local_router.sh 是一个 Linux 下创建本地网络命名空间（netns）路由器的 Shell 脚本
#核心目的是为每个指定的 router 实例创建独立的网络命名空间、配置虚拟网卡（veth）、IP 地址、路由规则和 iptables 策略，实现网络隔离与转发。
# 一、脚本初始化与参数校验
# 切换到脚本所在目录
cd $(dirname $0) 
# 加载外部环境变量/配置文件（如 SCI_CLIENT_ID、HOSTNAME 等）
source ../cloudrc 

# 校验参数：必须传入至少1个参数（router名称）
[ $# -lt 1 ] && echo "$0 <router>" && exit -1

router=$1

# 标准化router名称：如果名称不含"router-"前缀，则自动添加
[ "${router/router-/}" = "$router" ] && router=router-$1
# 校验router名称合法性：空值或"router-0"直接退出（router-0是默认核心路由器，不允许重复创建）
[ -z "$router" -o "$router" = "router-0" ] && exit 1
# 校验router对应的netns是否已存在：存在则退出（避免重复创建）
[ -f "/var/run/netns/$router" ] && exit 0
# 脚本首先做基础的参数和环境校验，确保输入合法、目标路由器未被创建，同时加载外部配置。
# /var/run/netns/ 是 Linux 网络命名空间的默认存储路径，存在该文件表示 netns 已创建。

# 二、创建网络命名空间并初始化基础网络
# 创建新的网络命名空间（核心：实现网络隔离）
ip netns add $router 
#在名为$router的 Linux 网络命名空间内，新增一条防火墙规则，所有内核标记为 0x1（十六进制）的入站数据包（发往该命名空间本地），
#都会被防火墙直接放行，允许其到达目标进程
 # 注释的mark规则（预留）
#ip netns exec $router iptables -A INPUT -m mark --mark 0x1/0xffff -j ACCEPT
 # 启动命名空间内的回环网卡（lo）
ip netns exec $router ip link set lo up 
# 提取router名称的后缀（如router-100 → suffix=100）
suffix=${router/router-/}  

# 检查默认路由器router-0的默认路由：若不存在，调用system_router.sh重建
def_route=$(ip netns exec router-0 ip route | grep default)
if [ -z "$def_route" ]; then
    echo "|:-COMMAND-:| system_router.sh '$SCI_CLIENT_ID' '$HOSTNAME'"
fi

# 网络命名空间（netns）：Linux 内核特性，每个 netns 有独立的网卡、IP、路由表、iptables 规则，实现网络隔离（类似 “独立的网络环境”）。
# router-0 是脚本中的 “核心路由器”，所有新创建的 router 都依赖它做网络转发，因此先检查其默认路由是否存在。

# 三、创建虚拟网卡（veth pair）并配置速率限制
# 创建veth pair（int-$suffix ↔ ti-$suffix）：跨netns通信的核心（一对虚拟网卡，两端分别在不同netns）
./create_veth.sh $router int-$suffix ti-$suffix
# 配置iptables转发速率限制（防止流量过载
ip netns exec $router iptables -P INPUT DROP
ip netns exec $router iptables -I INPUT -m state --state RELATED,ESTABLISHED -j ACCEPT
[ -z "$system_packet_rate_limit" ] && system_packet_rate_limit=120
system_packet_burst=$(( $system_packet_rate_limit / 2 ))
ip netns exec $router iptables -C FORWARD -i ti-$suffix -j DROP
[ $? -ne 0 ] && ip netns exec $router iptables -I FORWARD -i ti-$suffix -j DROP
ip netns exec $router iptables -C FORWARD -o ti-$suffix -j DROP
[ $? -ne 0 ] && ip netns exec $router iptables -I FORWARD -o ti-$suffix -j DROP
ip netns exec $router iptables -C FORWARD -i ti-$suffix -m limit --limit $system_packet_rate_limit/second --limit-burst $system_packet_burst -j ACCEPT
[ $? -ne 0 ] && ip netns exec $router iptables -I FORWARD -i ti-$suffix -m limit --limit $system_packet_rate_limit/second --limit-burst $system_packet_burst -j ACCEPT
ip netns exec $router iptables -C FORWARD -o ti-$suffix -m limit --limit $system_packet_rate_limit/second --limit-burst $system_packet_burst -j ACCEPT
[ $? -ne 0 ] && ip netns exec $router iptables -I FORWARD -o ti-$suffix -m limit --limit $system_packet_rate_limit/second --limit-burst $system_packet_burst -j ACCEPT
remaineder=$(( $suffix % 64516 ))
part2=$(( $remaineder / 254 ))
part3=$(( $remaineder % 254 ))
for i in {1..125}; do
    part4=$(( ($RANDOM % 125) * 2 + 3))
    local_ip=169.$part2.$part3.$part4
    peer_ip=169.$part2.$part3.$(( $part4 - 1 ))
    ip netns exec router-0 ip addr | grep "\<$peer_ip\>"
    [ $? -ne 0 ] && break
done
ip netns exec $router ip addr add ${local_ip}/31 dev ti-$suffix
ip netns exec $router ip route add default via $peer_ip

[ ! -f /var/run/netns/router-0 ] && ip netns add router-0
ip link set int-$suffix netns router-0
ip netns exec router-0 ip link set int-$suffix up
ip netns exec router-0 ip addr add ${peer_ip}/31 dev int-$suffix

# 169.x.x.x 网段：属于 APIPA（自动私有 IP 地址），用于内网点对点通信，脚本中用该网段避免与公网 IP 冲突。
# /31 子网：子网掩码 255.255.255.254，仅包含 2 个 IP，专门用于点对点（Point-to-Point）链路（veth pair 正好是两端，适合该子网）。
# 循环生成 IP 并检查冲突：确保每个 router 的 peer_ip 在 router-0 中唯一，避免 IP 重复导致通信异常。

# 五、配置 NAT 与 IP 转发
# 创建ipset（nonat）：用于标记不需要NAT的IP/网段
# 在名为 $router 的 Linux 网络命名空间中，创建一个名为 nonat 的、用于存储 IPv4 网段的 ipset 哈希集合，后续可将需要跳过 NAT 转换的网段添加到该集合中，配合 iptables 规则实现精准的非 NAT 网络策略管控
ip netns exec $router ipset create nonat nethash
ip netns exec $router iptables -A INPUT -m set --match-set nonat src -j ACCEPT
# 配置SNAT规则：仅当源IP在nonat、目标IP不在nonat时，将流量源IP替换为local_ip
# 所有新 router 实例的流量都通过 veth pair 连接到核心 router-0；
# router-0 作为 “网关” 负责最终的公网转发（若需要），而新 router 内部的 SNAT 仅用于 “跨命名空间通信” 的地址转换，而非直接面向公网；
ip netns exec $router iptables -t nat -C POSTROUTING -m set --match-set nonat src -m set ! --match-set nonat dst -j SNAT --to-source $local_ip
[ $? -ne 0 ] && ip netns exec $router iptables -t nat -A POSTROUTING -m set --match-set nonat src -m set ! --match-set nonat dst -j SNAT --to-source $local_ip

# 开启内核IP转发（核心：允许router netns转发IP数据包）
ip netns exec $router bash -c "echo 1 >/proc/sys/net/ipv4/ip_forward"

# ipset（nonat）：Linux 内核的 IP 集合工具，用于批量管理 IP / 网段，这里标记 “不需要 NAT 的地址”。
# SNAT（源地址转换）：修改出站数据包的源 IP 为 local_ip，确保外部网络能正确回包；脚本中仅对 “源在 nonat、目标不在 nonat” 的流量做 SNAT，实现精准的 NAT 控制。
# IP 转发：Linux 内核默认关闭 IP 转发，echo 1 > /proc/sys/net/ipv4/ip_forward 开启后，该 netns 才能转发不同网卡之间的数据包（实现路由器核心功能）。
# 核心总结
# 该脚本的核心逻辑是：
# 为每个 router 创建独立的网络命名空间（隔离网络环境）；
# 通过 veth pair 连接新 router 与核心 router-0（实现跨 ns 通信）；
# 配置唯一的点对点 IP 和默认路由（确保网络可达）；
# 用 iptables 做流量限速和 NAT（控制流量、实现地址转换）；
# 开启 IP 转发（让 router 具备数据包转发能力）。
# 最终效果是：每个 router 实例都是一个隔离的、可转发流量的小型路由器，所有流量都通过核心 router-0 转发，同时通过限速和 NAT 规则保障网络安全与可用性。
