#!/bin/bash

# 负载均衡进程守护：由 report_rc.sh 每次心跳调用，把运行中异常退出的 keepalived / haproxy 拉起。
# 节点重启后路由器 netns 尚未重建，由 recover_loadbalancer 回调整体恢复，这里跳过；
# 本脚本的 stdout 会被当作回调发给 clapi，调用方须重定向输出

cd `dirname $0`
source ../cloudrc

# 与 create_keepalived_conf.sh / create_haproxy_conf.sh 互斥（见 cloudrc 的 lb_lock_file）；
# 拿不到锁说明配置正在下发，由配置脚本负责启动进程，本轮跳过
exec 9>$lb_lock_file
flock -n 9 || exit 0

for conf in $router_dir/router-*/vrrp-*/keepalived.conf; do
    [ -f "$conf" ] || continue
    vrrp_dir=${conf%/keepalived.conf}
    router=$(basename $(dirname $vrrp_dir))
    [ -f /var/run/netns/$router ] || continue
    lb_proc_alive $vrrp_dir/keepalived.pid $conf && continue
    # VRRP 网卡或浮动 IP 网卡还没建好（恢复流程进行中）时不拉起，由 create_keepalived_conf.sh 启动
    ready=true
    for dev in $(awk '$1 == "interface" {print $2}' $conf) $(awk '$2 == "dev" {print $3}' $conf); do
        ip netns exec $router ip link show $dev >/dev/null 2>&1 || ready=false
    done
    [ "$ready" = "true" ] || continue
    # kill -9 等异常退出时 keepalived 来不及摘除浮动 IP，先摘掉，避免与已接管的对端同时持有；
    # fip 路由表的默认路由随地址一起失效，本端重新成为 MASTER 时由 notify_master 恢复
    awk '$2 == "dev" {print $1, $3}' $conf | while read vip dev; do
        ip netns exec $router ip addr del $vip dev $dev 2>/dev/null
    done
    log_debug "$router" "keepalived for $vrrp_dir is not running, restart it"
    # start_keepalived 已关闭锁的文件描述符，守护进程不会继承锁
    start_keepalived $router $vrrp_dir
done

for conf in $router_dir/router-*/lb-*/haproxy.conf; do
    [ -f "$conf" ] || continue
    lb_dir=${conf%/haproxy.conf}
    router=$(basename $(dirname $lb_dir))
    [ -f /var/run/netns/$router ] || continue
    lb_proc_alive $lb_dir/haproxy.pid $conf && continue
    log_debug "$router" "haproxy for $lb_dir is not running, restart it"
    ip netns exec $router sysctl -qw net.ipv4.ip_nonlocal_bind=1
    ip netns exec $router haproxy -D -p $lb_dir/haproxy.pid -f $conf 9>&-
done
exit 0
