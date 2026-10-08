#!/bin/bash

# Watch loop of one VPN gateway on the node holding its floating IP (plan §8.2, F1 and F6).
# Once a second, instead of once per heartbeat (1-20 s):
#   - charon and FRR are restarted when they died
#   - keepalived dead for two checks: the floating IPs would stay here while the peer takes over, so they
#     are dropped and the node steps down (vpn_notify.sh backup); check_lb_process.sh restarts keepalived
#   - the set of tunnels with an installed CHILD_SA is compared with the last one. On a change the tunnel
#     routes are re-applied (a static connection with two tunnels follows its SAs), a BGP tunnel that came
#     up gets its session restarted at once (no wait for the connect timer) and one that went down for two
#     checks loses it at once (no wait for the hold timer or BFD), and cloudlet reports without waiting
#     for the next heartbeat.
# Started by vpn_notify.sh when the node becomes master and by the heartbeat watchdog; it ends by itself
# when the node no longer holds the floating IP, the gateway is paused or removed.
# active_active gateway: runs on both nodes for as long as the gateway exists and is not paused. A tunnel
# change updates the staticd routes of the static tunnels (vpn_frr_statics) instead of kernel routes, and
# losing the client VPN floating IP only moves the client routes.
# Usage: vpn_watch.sh <gw_ID>

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

[ $# -lt 1 ] && exit 1
gw=$1
ID=$(vpn_router_of $gw) || exit 0
router=router-$ID
vpn_dir=$(vpn_dir_of $ID $gw)
ns=$(vpn_frr_ns $gw)
[ -d "$vpn_dir" ] || exit 0
# Check and claim under a lock: two starters a moment apart (vpn_notify.sh, then the heartbeat watchdog)
# would otherwise both find no live loop and both run one. The lock is held for these two steps only: a
# loop holding it for its lifetime would hand it down to the charon and FRR it restarts.
(
    flock 8
    vpn_watch_alive $vpn_dir && exit 1
    echo $$ >$vpn_dir/watch.pid
) 8>$vpn_dir/watch.lock || exit 0

vrrp_dir=$router_dir/$router/vrrp-$(cat $vpn_dir/vrrp_id 2>/dev/null)
last_up=$(cat $vpn_dir/tunnels.up 2>/dev/null)
pending_down=""
keepalived_missing=0
aa=false
vpn_is_aa $vpn_dir && aa=true
held=false
vpn_holds_vip $router $vpn_dir && held=true

# Restart the BGP session of the tunnels named on stdin
bgp_clear()
{
    local name peer
    while read name; do
        [ -n "$name" ] || continue
        peer=$(awk -v n=$name '$1 == n {print $2}' $vpn_dir/bgp_peers 2>/dev/null)
        [ -n "$peer" ] && timeout 5 vtysh -N $ns -c "clear bgp $peer" >/dev/null 2>&1
    done
}

lost=false
while :; do
    sleep 1
    [ -d $vpn_dir ] && [ -f /var/run/netns/$router ] || break
    vpn_disabled $vpn_dir && break
    if $aa; then
        # The tunnels stay here whatever the role; a client VPN floating IP lost without a notify (keepalived
        # killed) only moves the client routes, and tells clapi at once
        holds=false
        vpn_holds_vip $router $vpn_dir && holds=true
        if $held && ! $holds; then
            log_debug "$(basename $0)" "vpn: gateway $gw lost its client VPN floating IP without a notify"
            ./vpn_notify.sh $gw sync >/dev/null 2>&1
            vpn_trigger_report
        fi
        held=$holds
    elif ! vpn_holds_vip $router $vpn_dir; then
        lost=true
        break
    fi
    exec 9>$lb_lock_file
    if ! flock -n 9; then
        exec 9>&-
        continue
    fi
    # Re-check under the lock: a notify may have run meanwhile
    if ! $aa && { ! vpn_holds_vip $router $vpn_dir || vpn_disabled $vpn_dir; }; then
        exec 9>&-
        vpn_disabled $vpn_dir || lost=true
        break
    fi
    report=false
    if [ -f $vrrp_dir/keepalived.conf ] && ! lb_proc_alive $vrrp_dir/keepalived.pid $vrrp_dir/keepalived.conf; then
        let keepalived_missing=$keepalived_missing+1
        if [ $keepalived_missing -ge 2 ]; then
            log_debug "$(basename $0)" "vpn: keepalived of gateway $gw is gone, dropping the floating IPs"
            grep ' dev te-' $vrrp_dir/keepalived.conf | while read ext_ip _ ext_dev; do
                ip netns exec $router ip addr del $ext_ip dev $ext_dev >/dev/null 2>&1
            done
            exec 9>&-
            ./vpn_notify.sh $gw backup >/dev/null 2>&1
            vpn_trigger_report
            $aa || break
            keepalived_missing=0
            held=false
            continue
        fi
    else
        keepalived_missing=0
    fi
    if ! vpn_charon_alive $vpn_dir && [ -f $vpn_dir/swanctl.conf ]; then
        log_debug "$(basename $0)" "vpn: charon of gateway $gw is gone, restarting"
        vpn_start_charon $router $vpn_dir
        report=true
    fi
    if [ -f /etc/frr/$ns/frr.conf ] && ! vpn_frr_alive $gw; then
        log_debug "$(basename $0)" "vpn: FRR of gateway $gw is gone, restarting"
        vpn_start_frr $router $gw
        $aa || vpn_apply_routes $router $vpn_dir master
        report=true
    fi
    if up=$(vpn_tunnels_up $router $vpn_dir); then
        if [ "$up" != "$last_up" ]; then
            if $aa; then
                vpn_frr_statics $router $vpn_dir
            else
                vpn_apply_routes $router $vpn_dir master
            fi
            # BGP tunnels that came up: connect now
            comm -13 <(echo "$last_up") <(echo "$up") | bgp_clear
            report=true
        fi
        # BGP tunnels down for two checks in a row (a rekey never leaves a tunnel without an installed
        # CHILD_SA, but one sample is not worth a session): drop their sessions so the other tunnel takes over
        comm -23 <(echo "$pending_down") <(echo "$up") | bgp_clear
        pending_down=$(comm -23 <(echo "$last_up") <(echo "$up"))
        last_up=$up
    fi
    exec 9>&-
    $report && vpn_trigger_report
done
[ "$(cat $vpn_dir/watch.pid 2>/dev/null)" = "$$" ] && rm -f $vpn_dir/watch.pid
# The floating IP went away without a notify: the keepalived parent was killed and its VRRP child dropped
# the addresses on the way out (notify_backup never runs then). Step down now, like notify_backup would,
# and report at once: the release lets the peer's master claim through without the split-brain window.
if $lost && ! $aa && [ -d $vpn_dir ]; then
    log_debug "$(basename $0)" "vpn: gateway $gw lost its floating IP without a notify, stepping down"
    ./vpn_notify.sh $gw sync >/dev/null 2>&1
    vpn_trigger_report
fi
exit 0
