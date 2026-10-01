#!/bin/bash

# VPN gateway watchdog, called by report_rc.sh on every heartbeat. It works in both directions:
# on the node holding the floating IP charon and FRR are started if they died, on the other node they are
# stopped if they are still running. The second half matters after a kill -9 of keepalived: notify_backup
# never ran, the floating IP moved, and the old master would keep its tunnel processes and its local routes
# and blackhole what the other nodes send it (plan §4.1). Routes are corrected the same way.
# An active_active gateway runs its processes on both nodes whatever the role: only the client routes
# follow the floating IP.
# keepalived itself is restarted by check_lb_process.sh (the vrrp-* directory layout is shared). On the other
# nodes of the VPCs, the next-hop watcher (vpn_nexthop_watch.sh) is kept running while they have routes.
# Nothing here may wait for a peer: the lb lock is held throughout, and tunnels are (re)initiated in the
# background (vpn_ipsec_reconcile).
# stdout is the callback protocol: the caller redirects everything.

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

exec 9>$lb_lock_file
flock -n 9 || exit 0

for vpn_dir in $router_dir/router-*/vpn-*; do
    [ -d "$vpn_dir" ] || continue
    [ -f $vpn_dir/vrrp_id ] || continue
    router=$(basename $(dirname $vpn_dir))
    gw=${vpn_dir##*/vpn-}
    [ -f /var/run/netns/$router ] || continue
    # Client / site isolation and the MSS rules hold whatever the state, paused or not; repaired only when
    # the cheap check finds something missing
    vpn_forward_rules_ok $router || { vpn_isolate_rules $router add; vpn_mss_rules $router add; }
    vpn_isolate_pool_rules $router $vpn_dir add
    if vpn_disabled $vpn_dir; then
        # Paused: make sure nothing runs and every tunnel route is a blackhole, on both nodes
        vpn_charon_alive $vpn_dir && vpn_stop_charon $vpn_dir
        vpn_frr_alive $gw && vpn_stop_frr $gw
        vpn_apply_routes $router $vpn_dir disabled
        vpn_apply_wg $router $vpn_dir $gw
        continue
    fi
    if vpn_is_aa $vpn_dir; then
        role=backup
        vpn_holds_vip $router $vpn_dir && role=master
        vpn_apply_routes $router $vpn_dir $role
        vpn_start_charon $router $vpn_dir
        vpn_start_frr $router $gw
        vpn_frr_statics $router $vpn_dir
        vpn_ipsec_reconcile $router $vpn_dir
        vpn_watch_start $vpn_dir
        vpn_apply_wg $router $vpn_dir $gw
        continue
    fi
    # Re-read the state under the lock: a notify may have run between the loop start and here
    if vpn_holds_vip $router $vpn_dir; then
        vpn_apply_routes $router $vpn_dir master
        vpn_start_charon $router $vpn_dir
        vpn_start_frr $router $gw
        vpn_ipsec_reconcile $router $vpn_dir
        vpn_watch_start $vpn_dir
    else
        vpn_watch_stop $vpn_dir
        vpn_charon_alive $vpn_dir && vpn_stop_charon $vpn_dir
        vpn_frr_alive $gw && vpn_stop_frr $gw
        vpn_apply_routes $router $vpn_dir backup
    fi
    vpn_apply_wg $router $vpn_dir $gw
done
# Another node of a VPC with a gateway: its routes follow the gateway nodes it can reach
ls $router_dir/router-*/vpn-*/nexthops >/dev/null 2>&1 && vpn_nexthop_watch_start
exit 0
