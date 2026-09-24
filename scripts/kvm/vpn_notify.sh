#!/bin/bash

# keepalived notify hook of a VPN gateway (also called with "sync" by set_vpn_route.sh and the process
# watchdog). Called as: vpn_notify.sh <gw_ID> <master|backup|fault|sync> [keepalived's own arguments].
#
# master:  restore the fip route table (set_route_table.sh: the default route of that table disappears
#          with the floating IP), install the tunnel routes, start charon, then zebra and bgpd
# backup:  point the routes at the peer, stop charon and FRR (they need the floating IP)
# fault:   blackhole the routes (the peer's state is unknown), stop the processes
# sync:    do whichever of master/backup matches the floating IP right now
#
# This runs as a child of keepalived: its stdout is not a callback channel, nothing is reported here.
# The lb lock serializes it with the config scripts and the watchdog.

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

# keepalived runs notify scripts with a restrictive umask; every file and directory created here must
# get the same modes as when the config scripts create them (FRR's run directory must be enterable by
# the frr user, see vpn_start_frr)
umask 022

[ $# -lt 2 ] && exit 0
gw=$1
mode=$2
ID=$(vpn_router_of $gw) || exit 0
router=router-$ID
vpn_dir=$(vpn_dir_of $ID $gw)
[ -f /var/run/netns/$router ] || exit 0

# Step log for diagnosing slow role changes (the outage of a failover is bounded by this script)
vlog() { echo "$(date +%T.%N | cut -c1-12) [$$] $mode $*" >>$vpn_dir/notify.log; }
export VPN_TRACE=$vpn_dir/notify.log
vlog "start (args: $*)"
exec 9>$lb_lock_file
flock 9
vlog "lock acquired"

if [ "$mode" = "sync" ]; then
    if vpn_holds_vip $router $vpn_dir; then
        mode=master
    else
        mode=backup
    fi
fi

vrrp_ID=$(cat $vpn_dir/vrrp_id 2>/dev/null)
vrrp_dir=$router_dir/$router/vrrp-$vrrp_ID

# Paused gateway: whatever the role, blackhole the tunnel routes before the processes go (a route to a
# tunnel that no longer exists would leak through the default route) and stop them. The master still
# restores the fip route table: the floating IP stays and so must its policy routing.
if vpn_disabled $vpn_dir; then
    if [ "$mode" = "master" ] && [ -f $vrrp_dir/routes ]; then
        ROUTES_FILE=$vrrp_dir/routes KEEPALIVE_CONF=$vrrp_dir/keepalived.conf timeout 60 ip netns exec $router ./set_route_table.sh >/dev/null 2>&1
    fi
    vpn_apply_routes $router $vpn_dir disabled
    vpn_stop_charon $vpn_dir
    vpn_stop_frr $gw
    # zebra withdraws what it installed when it exits: make sure the blackholes are ours afterwards
    vpn_apply_routes $router $vpn_dir disabled
    ip netns exec $router ip link set wg-$gw down >/dev/null 2>&1
    vlog "disabled: routes blackholed, processes stopped"
    exit 0
fi

case $mode in
master)
    # Step 0: the fip table default route, or every packet sourced from the floating IP leaves through
    # router-0 and gets SNATed. Always run it inside the router netns: keepalived's notify inherits it,
    # but the sync callers (set_vpn_route.sh, the watchdog) run in the host netns where the route can
    # never be installed and the script would retry for the whole timeout while holding the lock
    if [ -f $vrrp_dir/routes ]; then
        ROUTES_FILE=$vrrp_dir/routes KEEPALIVE_CONF=$vrrp_dir/keepalived.conf timeout 60 ip netns exec $router ./set_route_table.sh >/dev/null 2>&1
        vlog "set_route_table done (rc=$?)"
    fi
    vpn_apply_routes $router $vpn_dir master
    vlog "routes applied"
    vpn_start_charon $router $vpn_dir
    vlog "charon started (rc=$?)"
    vpn_start_frr $router $gw
    vlog "frr started (rc=$?)"
    # FRR up: hand the summary blackholes over to staticd (see vpn_apply_routes)
    vpn_apply_routes $router $vpn_dir master
    ;;
backup)
    vpn_apply_routes $router $vpn_dir backup
    vpn_stop_charon $vpn_dir
    vpn_stop_frr $gw
    ;;
*)
    vpn_apply_routes $router $vpn_dir fault
    vpn_stop_charon $vpn_dir
    vpn_stop_frr $gw
    ;;
esac
vlog "done"
exit 0
