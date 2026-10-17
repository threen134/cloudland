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
#
# Answering pings again is not always enough to come back: a node unreachable for a while ($hold_after seconds or
# more, as a reboot is) answers long before its gateway is rebuilt, and while the control plane is down the order of
# the lists is still the old one, with the old master first (just after clapi restarts it may be first too, until the
# master reports in). Such a node comes back at once only where the traffic would not go to it: every list has a
# hop before it that is up, and no prefix shared by ecmp has it among its hops (vpn_apply_nexthops takes the first
# reachable hop, or every reachable one). Otherwise after answering for $hold seconds, a few lost probes allowed
# (R2-18, 2026-10-17: the control node, master of two gateways, rebooted and was routed to again for 341 s before
# its gateway was back). A node unreachable only briefly (lost probes, a network blip) kept its gateway running and
# comes back after the ten answers.
# State per probed address in /run/cloudland/vpn-nexthop/<host ip> ("down" or "up"; a "down" dates from the file's
# time), kept when this loop is killed and restarted, removed when it ends by itself: once no gateway routes are left
# on this node. Restarted by set_vpn_route.sh and the heartbeat watchdog.

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

interval=0.3
fail_limit=3
ok_limit=10
hold=${vpn_nexthop_hold:-600}
hold_after=${vpn_nexthop_hold_after:-30}

mkdir -p $vpn_nexthop_state
# Check and claim under a lock, as vpn_watch.sh does: two starters a moment apart would otherwise both run a loop
(
    flock 8
    vpn_nexthop_watch_alive && exit 1
    echo $$ >$vpn_nexthop_state/watch.pid
) 8>$vpn_nexthop_state/watch.lock || exit 0

# carries <host>: whether traffic would go to a node once it is marked up, as vpn_apply_nexthops chooses: a prefix
# shared by ecmp has it among its hops, or no hop before it in a list is up. nexthop_hosts maps the VRRP addresses
# of a gateway to node addresses; nexthops has "<prefix> <ecmp 0|1> <hop>,<hop>..." in clapi's order
carries()
{
    local host=$1 file vpn_dir addrs cidr ecmp hops hop
    for file in $files; do
        addrs=" $(awk -v h=$host '$2 == h {printf "%s ", $1}' $file)"
        [ "$addrs" = " " ] && continue
        vpn_dir=${file%/nexthop_hosts}
        [ -f $vpn_dir/nexthops ] || continue
        while read cidr ecmp hops; do
            for hop in ${hops//,/ }; do
                case "$addrs" in *" $hop "*) return 0 ;; esac
                [ "$ecmp" = "1" ] && continue
                vpn_hop_reachable $vpn_dir $hop && break
            done
        done <$vpn_dir/nexthops
    done
    return 1
}

# may_return <host>: a node marked down may come back (see the top)
may_return()
{
    local host=$1 down=${down_at[$1]}
    # marked down by an earlier loop: since then, by the state file
    [ -n "$down" ] || down=$(stat -c %Y $vpn_nexthop_state/$host 2>/dev/null || date +%s)
    [ $(( ${ok_since[$host]} - down )) -lt $hold_after ] && return 0
    carries $host || return 0
    [ $(( $(date +%s) - ${ok_since[$host]} )) -ge $hold ]
}

declare -A fails oks ok_since down_at
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
            [ -n "${ok_since[$host]}" ] || ok_since[$host]=$(date +%s)
            oks[$host]=$((${oks[$host]:-0} + 1))
            # checked about once a second: it reads the lists of every gateway
            if [ "$state" = "down" ] && [ ${oks[$host]} -ge $ok_limit ] && [ $(( (${oks[$host]} - $ok_limit) % 3 )) -eq 0 ] &&
                may_return $host; then
                echo up >$vpn_nexthop_state/$host
                changed="$changed $host"
                log_debug "$(basename $0)" "vpn: gateway node $host answers again"
            fi
        else
            oks[$host]=0
            fails[$host]=$((${fails[$host]:-0} + 1))
            # a lost probe here and there does not restart the hold time, being down again does
            [ ${fails[$host]} -ge $fail_limit ] && unset "ok_since[$host]"
            if [ "$state" != "down" ] && [ ${fails[$host]} -ge $fail_limit ]; then
                echo down >$vpn_nexthop_state/$host
                down_at[$host]=$(date +%s)
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
if [ "$(cat $vpn_nexthop_state/watch.pid 2>/dev/null)" = "$$" ]; then
    # No gateway routes left here: a state kept for the next loop would hold a node that answered all along
    find $vpn_nexthop_state -maxdepth 1 -type f -regex '.*/[0-9.]+' -delete
    rm -f $vpn_nexthop_state/watch.pid
fi
exit 0
