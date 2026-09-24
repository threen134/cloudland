#!/bin/bash

cd `dirname $0`
source ../cloudrc

[ $# -lt 8 ] && die "$0 <router> <vrrp_ID> <vrrp_vlan> <local_ip> <local_mac> <peer_ip> <peer_mac> <role> [vrid]"

ID=$1
router=router-$ID
vrrp_ID=$2
vrrp_vlan=$3
local_ip=$4
local_mac=$5
peer_ip=$6
peer_mac=$7
role=$8
# VRRP virtual router id (1-255) allocated per VRRP subnet by clapi; instances created before the
# column existed keep their primary key as the id
vrid=$9
[ -z "$vrid" -o "$vrid" = "0" ] && vrid=$vrrp_ID

vrrp_dir=$router_dir/$router/vrrp-$vrrp_ID
mkdir -p $vrrp_dir
content=$(cat)
vips=$(jq -r .floating_ips <<< $content)
nvip=$(jq length <<< $vips)
if [ $nvip -eq 0 ]; then
    # 持锁删除：避免 check_lb_process.sh 在停进程与删目录之间把进程拉起，留下没有配置目录、一直持有浮动 IP 的孤儿进程
    (
        flock 9
        lb_proc_alive $vrrp_dir/keepalived.pid $vrrp_dir/keepalived.conf && keepalived_pid=$(cat $vrrp_dir/keepalived.pid)
        rm -rf $vrrp_dir
        [ -n "$keepalived_pid" ] && ip netns exec $router kill $keepalived_pid
    ) 9>$lb_lock_file
    exit 0
fi
ports=$(jq -r .ports <<< $content)
nport=$(jq length <<< $ports)
i=0
while [ $i -lt $nvip ]; do
    vip=$(jq -r .[$i].address <<< $vips)
    ext_ip=${vip%/*}
    for num in $(ip netns exec $router iptables -n -L --line-numbers | grep "\<$ext_ip\>" | awk '{print $1}' | sort -nr); do
        ip netns exec $router iptables -D INPUT $num
    done
    j=0
    while [ $j -lt $nport ]; do
        port=$(jq -r .[$j] <<< $ports)
        ip netns exec $router iptables -C INPUT -p tcp -m tcp -d $ext_ip --dport $port -m conntrack --ctstate NEW -j ACCEPT
        [ $? -ne 0 ] && ip netns exec $router iptables -A INPUT -p tcp -m tcp -d $ext_ip --dport $port -m conntrack --ctstate NEW -j ACCEPT
        let j=$j+1
    done
    let i=$i+1
done

ip netns exec $router ip addr show ns-$vrrp_vlan | grep -q $local_ip
[ $? -ne 0 ] && ./set_vrrp_ip.sh $@

# 两端都以 BACKUP 启动、用优先级决定首次选主：nopreempt 只对初始状态为 BACKUP 的实例生效。
# 若按角色写 state MASTER，该节点 keepalived 重启（进程守护拉起、节点重启恢复）时会抢回主，浮动 IP 多切换一次
priority=100
[ "$role" = "MASTER" ] && priority=110
[ ! -d "$vrrp_dir" ] && mkdir -p $vrrp_dir
cat >$vrrp_dir/keepalived.conf.new <<EOF
vrrp_instance load_balancer_${vrrp_ID} {
    state BACKUP
    interface ns-$vrrp_vlan
    virtual_router_id ${vrid}
    priority $priority
    advert_int 1
    nopreempt

    unicast_src_ip ${local_ip%/*}
    unicast_peer {
        ${peer_ip%/*}
    }

    authentication {
        auth_type PASS
        auth_pass 123456
    }

    virtual_ipaddress {
EOF
export ROUTES_FILE=$vrrp_dir/routes
export KEEPALIVE_CONF=$vrrp_dir/keepalived.conf
rm -f $ROUTES_FILE
i=0
while [ $i -lt $nvip ]; do
    read -d'\n' -r virtual_ip ext_vlan ext_gw mark_id inbound outbound< <(jq -r ".[$i].address, .[$i].vlan, .[$i].gateway, .[$i].mark_id, .[$i].inbound, .[$i].outbound" <<<$vips)
    suffix=${ID}-${ext_vlan}
    ext_dev=te-$suffix
    ./create_veth.sh $router ext-$suffix te-$suffix
    ./create_lb_floating.sh $ID $virtual_ip $ext_gw $ext_vlan $mark_id $inbound $outbound
    cat >>$vrrp_dir/keepalived.conf.new <<EOF
        $virtual_ip dev $ext_dev
EOF
    let i=$i+1
done
cat >>$vrrp_dir/keepalived.conf.new <<EOF
    }
    notify_master $PWD/set_route_table.sh
}
EOF
# 替换配置与启动/重载在锁内完成，与 check_lb_process.sh 互斥（见 cloudrc 的 lb_lock_file）；
# 写完再整体替换，check_lb_process.sh 不会读到写了一半的文件
(
    flock 9
    mv -f $vrrp_dir/keepalived.conf.new $vrrp_dir/keepalived.conf
    if lb_proc_alive $vrrp_dir/keepalived.pid $vrrp_dir/keepalived.conf; then
        kill -HUP $(cat $vrrp_dir/keepalived.pid)
    else
        start_keepalived $router $vrrp_dir
    fi
) 9>$lb_lock_file
exit 0
