#!/bin/bash

cd `dirname $0`
source ../cloudrc

routes_file=$ROUTES_FILE
[ ! -f $routes_file ] && exit 0

for i in {1..150}; do
    while read line; do
        if eval $line; then
       	    pass="true"
	    break
	fi
    done <$routes_file
    [ "$pass" = "true" ] && break
    sleep 2
done

keepalive_conf=$KEEPALIVE_CONF
grep ' dev te-' $keepalive_conf | while read ext_ip _ ext_dev; do
    # keepalived 运行在路由器 netns 内，notify 脚本继承该 netns，直接执行即可（此前引用未定义的 $router，arping 从未成功）
    arping -c 1 -A -U -I $ext_dev ${ext_ip%/*} >/dev/null 2>&1 &
done
