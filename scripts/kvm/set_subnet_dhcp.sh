#!/bin/bash

cd `dirname $0`
source ../cloudrc

[ $# -lt 6 ] && echo "$0 <router> <vlan> <gateway> <network> <hostmin> <hostmax> [name_server]" && exit -1

router=$1
[ "${router/router-/}" = "$router" ] && router=router-$1
vlan=$2
gateway=${3%%/*}
network=$4
hostmin=$5
hostmax=$6
name_server=$7
[ -z "$name_server" ] && name_server=$dns_server

[ "$vlan" -le 4095 ] && exit 0
# 路由器 netns 的 INPUT 默认 DROP（create_local_router.sh），DHCP 请求源地址是 0.0.0.0、不在 nonat 集合，需单独放行。
# 放在这里而不是建路由器时：已存在的路由器在（重新）配置子网时也能补上
ip netns exec $router iptables -C INPUT -i ns-+ -p udp --dport 67 -j ACCEPT 2>/dev/null || ip netns exec $router iptables -A INPUT -i ns-+ -p udp --dport 67 -j ACCEPT
let mtu=$(cat /sys/class/net/$vxlan_interface/mtu)-50

netstr=$(echo $network | tr -s './' '_')
vlan_dir=$cache_dir/router/$router/$vlan
dnsmasq_conf=$vlan_dir/dnsmasq.conf
dhcp_host=$vlan_dir/dhcp_hosts
pid_file=$vlan_dir/dnsmasq.pid
dnsmasq_conf_dir=$vlan_dir/dnsmasq.conf.d
mkdir -p $dnsmasq_conf_dir
# 先建空的 dhcp_hosts：dnsmasq 启动时文件不存在会报 cannot read，要等 set_host.sh 写入并 SIGHUP 才读到
touch $dhcp_host
dhcp_conf=$dnsmasq_conf_dir/dhcp_$netstr.conf
old_conf=$(cat $dnsmasq_conf $dhcp_conf 2>/dev/null)
# 同一路由器 netns 内每个 VNI 一个 dnsmasq：bind-dynamic 只绑定各自 ns-<vni> 接口，
# 否则第一个实例占用 0.0.0.0:67/53，后续子网的 dnsmasq 启动失败（Address already in use）
cat >$dnsmasq_conf <<EOF
no-hosts
cache-size=0
no-resolv
strict-order
bind-dynamic
${cloud_domain:+domain=$cloud_domain}
except-interface=lo
pid-file=$pid_file
log-facility=/var/log/dnsmasq.log
dhcp-hostsfile=$dhcp_host
dhcp-option=26,$mtu
leasefile-ro
dhcp-ignore=tag:!known
conf-dir=$dnsmasq_conf_dir
EOF

# dhcp-option 的 tag 必须由 dhcp-range 的 set: 打上，否则网关/DNS 选项不下发（dnsmasq 会把自己当 DNS 发给客户机）；
# 同一 VNI 上可有多个子网，每个子网一个 tag
cat >$dhcp_conf <<EOF
dhcp-range=set:$netstr,$hostmin,$hostmax,2h
dhcp-option=tag:$netstr,3,$gateway
dhcp-option=tag:$netstr,6,$name_server
EOF

dmasq_cmd=$(ps -ef | grep dnsmasq | grep "\<interface=ns-$vlan\>")
dns_pid=$(echo "$dmasq_cmd" | awk '{print $2}')
# 配置只在启动时读取：有变化时重启 dnsmasq（地址来自 dhcp_hosts 静态绑定，重启不影响已分配地址）
if [ -n "$dns_pid" ] && [ "$old_conf" != "$(cat $dnsmasq_conf $dhcp_conf)" ]; then
    kill $dns_pid
    for i in {1..10}; do
        kill -0 $dns_pid 2>/dev/null || break
        sleep 0.5
    done
    dns_pid=""
fi
if [ -z "$dns_pid" ]; then
    cmd="/usr/sbin/dnsmasq --interface=ns-$vlan -C $dnsmasq_conf"
    ip netns exec $router $cmd
fi
