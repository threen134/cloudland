#!/bin/bash

cd $(dirname $0)
source ../cloudrc

# ext_vlan：外部 VLAN 标识；
# ext_ip：路由器的外部 IP 地址（需带子网掩码，如 192.168.1.10/24）；
# gateway：网关地址（支持带子网掩码格式，会自动截取纯 IP）。
[ $# -lt 3 ] && echo "$0 <ext_vlan> <ext_ip> <gateway>" && exit -1

ext_vlan=$1
ext_ip=$2
gateway=${3%/*}
router=router-0

 # 创建名为router-0的网络命名空间
ip netns add $router
# 进入命名空间，启用回环网卡lo
ip netns exec $router ip link set lo up

# 3. 虚拟网卡创建与 IP 配置
# create_veth.sh 作用是创建一对虚拟以太网网卡（veth pair）：
# 一端 ext-$ext_vlan 通常留在主机网络命名空间，用于对接外部网络；
# 另一端 link-$ext_vlan 放入 router-0 命名空间，作为路由器的外网网卡；
 # 调用外部脚本创建一对veth虚拟网卡
./create_veth.sh $router ext-$ext_vlan link-$ext_vlan
# 清理旧的 link 接口（非当前 ext_vlan 的）
for dev in $(ip netns exec $router ip -o link show | awk -F': ' '{print $2}' | cut -d'@' -f1 | grep '^link-'); do
    [ "$dev" != "link-$ext_vlan" ] && ip netns exec $router ip link del $dev
done
 # 刷新旧 IP 并为命名空间内的网卡配置外部IP
ip netns exec $router ip addr flush dev link-$ext_vlan
ip netns exec $router ip addr add $ext_ip dev link-$ext_vlan
# 配置默认路由后，router-0 命名空间内的流量可通过网关访问外部网络。
  # 设置命名空间的默认路由（指向网关）
ip netns exec $router ip route replace default via $gateway
 # 截取路由器IP（去除子网掩码）
route_ip=${ext_ip%/*}
# 4. iptables 防火墙规则配置
# 规则逻辑：
# iptables -C 先检查规则是否已存在，不存在则通过 -I 插入（避免重复规则）；
# 封禁 25/465/587 端口的转发流量：这三个端口是 SMTP 邮件发送端口，常用于垃圾邮件发送，属于安全管控；
# NAT 地址伪装：让命名空间内的设备通过路由器的外网 IP 访问外部网络（类似家用路由器的 NAT 功能）。
# 禁用INPUT链（默认丢弃所有入站流量）
ip netns exec $router iptables -P INPUT DROP
# 允许网关 ICMP（健康检查）
ip netns exec $router iptables -C INPUT -s $gateway -p icmp -j ACCEPT
[ $? -ne 0 ] && ip netns exec $router iptables -I INPUT -s $gateway -p icmp -j ACCEPT
# 封禁SMTP邮件相关端口（25/465/587）的转发流量，避免邮件滥用
ip netns exec $router iptables -C FORWARD -p tcp --dport 25 -j DROP
[ $? -ne 0 ] && ip netns exec $router iptables -I FORWARD -p tcp --dport 25 -j DROP
ip netns exec $router iptables -C FORWARD -p tcp --dport 465 -j DROP
[ $? -ne 0 ] && ip netns exec $router iptables -I FORWARD -p tcp --dport 465 -j DROP
ip netns exec $router iptables -C FORWARD -p tcp --dport 587 -j DROP
[ $? -ne 0 ] && ip netns exec $router iptables -I FORWARD -p tcp --dport 587 -j DROP
# 配置SNAT（MASQUERADE）：出站流量通过link-$ext_vlan网卡做地址伪装（动态NAT）
ip netns exec $router iptables -t nat -C POSTROUTING -o link-$ext_vlan -j MASQUERADE
[ $? -ne 0 ] && ip netns exec $router iptables -t nat -I POSTROUTING -o link-$ext_vlan -j MASQUERADE

ip netns exec $router bash -c "echo 1 >/proc/sys/net/ipv4/ip_forward"
