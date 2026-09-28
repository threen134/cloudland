#!/bin/bash

# Local fast switch of the other nodes of a VPC (plan §8.2, F7). A node that is not a VRRP node of
# a gateway routes the gateway's prefixes through the gateway nodes (set_vpn_route.sh writes nexthops and
# nexthop_hosts, vpn_lib.sh vpn_apply_nexthops installs them). One loop per node, for every gateway: every
# 0.3 s each gateway node is pinged on its own address (the VRRP addresses cannot be probed from here: this
# node has no address of its own in the VRRP subnet, only the anycast gateway). Three misses in a row mark
# the node down and move the routes of every prefix that used it to the next node in its list; ten answers
# in a row (3 s) bring it back: a node whose network just came back needs a moment for its BGP sessions,
# and routing through it earlier blackholes the traffic meanwhile. Without this the routes only move when
# clapi re-pushes them (cland needs about 3 s to notice a silent node, clapi then pushes to every node).
#
# A false alarm costs little: an active_standby backup node forwards to the master, an active_active node
# forwards over iBGP to the node with the better tunnel.
# State per probed address in /run/cloudland/vpn-nexthop/<host ip> ("down" or "up"). Ends by itself once
# no gateway routes are left on this node; restarted by set_vpn_route.sh and the heartbeat watchdog.

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

interval=0.3
fail_limit=3
ok_limit=10

mkdir -p $vpn_nexthop_state
# Check and claim under a lock, as vpn_watch.sh does: two starters a moment apart would otherwise both run a loop
(
    flock 8
    vpn_nexthop_watch_alive && exit 1
    echo $$ >$vpn_nexthop_state/watch.pid
) 8>$vpn_nexthop_state/watch.lock || exit 0

declare -A fails oks
idle=0
while :; do
    files=$(ls $router_dir/router-*/vpn-*/nexthop_hosts 2>/dev/null)
    if [ -z "$files" ]; then
        # nothing to watch for 10 s: done (the next set_vpn_route.sh starts a new one)
        let idle=$idle+1
        [ $idle -ge 30 ] && break
        sleep $interval
        continue
    fi
    idle=0
    hosts=$(awk '{print $2}' $files | sort -u)
    # One probe per gateway node, in parallel; a reply takes well under a millisecond inside the data center
    results=$(for host in $hosts; do
        (timeout 0.25 ping -c 1 -n -q -W 1 $host >/dev/null 2>&1 && echo "$host ok" || echo "$host fail") &
    done
    wait)
    changed=""
    while read host result; do
        [ -n "$host" ] || continue
        state=$(cat $vpn_nexthop_state/$host 2>/dev/null)
        if [ "$result" = "ok" ]; then
            fails[$host]=0
            oks[$host]=$((${oks[$host]:-0} + 1))
            if [ "$state" = "down" ] && [ ${oks[$host]} -ge $ok_limit ]; then
                echo up >$vpn_nexthop_state/$host
                changed="$changed $host"
                log_debug "$(basename $0)" "vpn: gateway node $host answers again"
            fi
        else
            oks[$host]=0
            fails[$host]=$((${fails[$host]:-0} + 1))
            if [ "$state" != "down" ] && [ ${fails[$host]} -ge $fail_limit ]; then
                echo down >$vpn_nexthop_state/$host
                changed="$changed $host"
                log_debug "$(basename $0)" "vpn: gateway node $host does not answer, routing around it"
            fi
        fi
    done <<<"$results"
    if [ -n "$changed" ]; then
        for file in $files; do
            vpn_dir=${file%/nexthop_hosts}
            for host in $changed; do
                awk -v h=$host '$2 == h {found = 1} END {exit !found}' $file || continue
                router=$(basename $(dirname $vpn_dir))
                (
                    flock 9
                    vpn_apply_nexthops $router $vpn_dir
                ) 9>$vpn_nexthop_lock
                break
            done
        done
    fi
    sleep $interval
done
[ "$(cat $vpn_nexthop_state/watch.pid 2>/dev/null)" = "$$" ] && rm -f $vpn_nexthop_state/watch.pid
exit 0
