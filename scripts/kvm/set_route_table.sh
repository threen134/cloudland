#!/bin/bash

cd `dirname $0`
source ../cloudrc

routes_file=$ROUTES_FILE
[ ! -f $routes_file ] && exit 0

# Every line must go through: each is the default route of one fip table, and two floating IPs in two
# VLANs have two tables. A line fails while its floating IP is not on the port yet: retry for up to 5 minutes
pending=$(sort -u $routes_file)
for i in {1..150}; do
    left=""
    while read line; do
        [ -n "$line" ] || continue
        eval $line || left="$left$line
"
    done <<<"$pending"
    pending=$left
    [ -z "$pending" ] && break
    sleep 2
done

keepalive_conf=$KEEPALIVE_CONF
grep ' dev te-' $keepalive_conf | while read ext_ip _ ext_dev; do
    # keepalived 运行在路由器 netns 内，notify 脚本继承该 netns，直接执行即可（此前引用未定义的 $router，arping 从未成功）
    arping -c 1 -A -U -I $ext_dev ${ext_ip%/*} >/dev/null 2>&1 &
done
