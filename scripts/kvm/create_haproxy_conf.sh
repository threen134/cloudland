#!/bin/bash

cd `dirname $0`
source ../cloudrc

[ $# -lt 3 ] && die "$0 <router> <lb_ID> <vrrp_ID>"

router=$1
lb_ID=$2
vrrp_ID=$3
[ "${router/router-/}" = "$router" ] && router=router-$1
src_vrrp_ip=$(grep unicast_src_ip $router_dir/$router/vrrp-$vrrp_ID/keepalived.conf | awk '{print $2}')
lb_dir=$router_dir/$router/lb-$lb_ID
[ ! -d "$lb_dir" ] && mkdir -p $lb_dir
content=$(cat)
floating_ips=$(jq -r .floating_ips <<< $content)
nfloating_ip=$(jq length <<< $floating_ips)
listeners=$(jq -r .listeners <<< $content)
nlistener=$(jq length <<< $listeners)
if [ $nlistener -eq 0 -o $nfloating_ip -eq 0 ]; then
    # 持锁删除：避免 check_lb_process.sh 在停进程与删目录之间把进程重新拉起
    (
        flock 9
        lb_proc_alive $lb_dir/haproxy.pid $lb_dir/haproxy.conf && haproxy_pid=$(cat $lb_dir/haproxy.pid)
        rm -rf $lb_dir
        [ -n "$haproxy_pid" ] && ip netns exec $router kill $haproxy_pid
    ) 9>$lb_lock_file
    exit 0
fi
ip netns exec $router sysctl -qw net.ipv4.ip_nonlocal_bind=1
# 路由器 netns 的 INPUT 默认 DROP，放行 lo，否则 haproxy 统计页（127.0.0.1:8080）无法访问
ip netns exec $router iptables -C INPUT -i lo -j ACCEPT 2>/dev/null || ip netns exec $router iptables -I INPUT -i lo -j ACCEPT
# pid 文件只由命令行 -p 指定：配置里再写 pidfile 会让 haproxy 每次启动都输出告警，被当作回调发给 clapi
cat >$lb_dir/haproxy.conf.new <<EOF
global
    log /dev/log local0 info
    log /dev/log local0 notice
    chroot $lb_dir
    maxconn 4000
    user haproxy
    group haproxy
    daemon
    stats socket $lb_dir/admin.sock mode 660 level admin expose-fd listeners
    stats timeout 30s

defaults
    log global
    mode http
    option httplog
    option dontlognull
    option http-server-close
    option forwardfor except 127.0.0.0/8
    option redispatch
    retries 3
    timeout http-request 10s
    timeout queue 1m
    timeout connect 10s
    timeout client 1m
    timeout server 1m
    timeout http-keep-alive 10s
    timeout check 10s
    maxconn 3000

listen stats
    bind 127.0.0.1:8080
    mode http
    stats enable
    stats uri /haproxy-stats
    stats realm Haproxy\ Statistics
    stats auth admin:password
    stats hide-version
    stats refresh 30s
EOF

i=0
while [ $i -lt $nlistener ]; do
    listener=$(jq -r .[$i] <<< $listeners)
    ssl_config=""
    read -d'\n' -r name mode port key cert< <(jq -r ".name, .mode, .port, .key, .cert" <<<$listener)
    if [ -n "$key" -a -n "$cert" ]; then
        base64 -d <<<"$key" >$lb_dir/$name.pem
	echo >>$lb_dir/$name.pem
        base64 -d <<<"$cert" >>$lb_dir/$name.pem
	echo >>$lb_dir/$name.pem
        ssl_config="ssl crt $lb_dir/$name.pem"
    fi
    cat >>$lb_dir/haproxy.conf.new <<EOF

frontend ${name}_front
EOF
    j=0
    while [ $j -lt $nfloating_ip ]; do
        fip=$(jq -r .[$j] <<< $floating_ips)
        cat >>$lb_dir/haproxy.conf.new <<EOF
    bind ${fip%/*}:$port $ssl_config
EOF
        let j=$j+1
    done
    cat >>$lb_dir/haproxy.conf.new <<EOF
    mode $mode
    default_backend ${name}_back

backend ${name}_back
    mode $mode
    balance roundrobin
    source $src_vrrp_ip
EOF
    backends=$(jq -r .backends <<< $listener)
    nbackend=$(jq length <<< $backends)
    j=0
    while [ $j -lt $nbackend ]; do
        backend=$(jq -r .[$j] <<< $backends)
        read -d'\n' -r backend_url ssl< <(jq -r ".backend_url, .ssl" <<<$backend)
        ssl_option=""
        if [ "$ssl" == "true" ]; then
            ssl_option=" ssl verify none"
        fi
        cat >>$lb_dir/haproxy.conf.new <<EOF
    server ${name}-$j $backend_url check weight 100 maxconn 1000$ssl_option
EOF
        let j=$j+1
    done
    let i=$i+1
done
# 替换配置与启动/重载在锁内完成，与 check_lb_process.sh 互斥（见 cloudrc 的 lb_lock_file），否则双方可能各起一个 haproxy，
# pid 文件只记录其中一个，另一个此后的重载都停不掉、一直按旧配置服务；写完再整体替换，避免读到写了一半的文件
(
    flock 9
    mv -f $lb_dir/haproxy.conf.new $lb_dir/haproxy.conf
    if lb_proc_alive $lb_dir/haproxy.pid $lb_dir/haproxy.conf; then
        # -x 经 stats socket（expose-fd listeners）接管旧进程的监听 socket：只用 -sf 时新旧进程各自绑定端口，
        # 旧进程关闭监听时积压在其队列中的连接被重置，每次重载都可能丢请求（实测 6 次重载丢 4 个）
        ip netns exec $router haproxy -D -p $lb_dir/haproxy.pid -f $lb_dir/haproxy.conf -x $lb_dir/admin.sock -sf $(cat $lb_dir/haproxy.pid) 9>&-
    else
        ip netns exec $router haproxy -D -p $lb_dir/haproxy.pid -f $lb_dir/haproxy.conf 9>&-
    fi
) 9>$lb_lock_file
