#!/bin/bash

# VPN gateway watchdog, called by report_rc.sh on every heartbeat. It works in both directions:
# on the node holding the floating IP charon and FRR are started if they died, on the other node they are
# stopped if they are still running. The second half matters after a kill -9 of keepalived: notify_backup
# never ran, the floating IP moved, and the old master would keep its tunnel processes and its local routes
# and blackhole what the other nodes send it (plan §2.4). Routes are corrected the same way.
# keepalived itself is restarted by check_lb_process.sh (the vrrp-* directory layout is shared).
# stdout is the callback protocol: the caller redirects everything.

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

exec 9>$lb_lock_file
flock -n 9 || exit 0

for vpn_dir in $router_dir/router-*/vpn-*; do
    [ -d "$vpn_dir" ] || continue
    [ -f $vpn_dir/vip ] || continue
    router=$(basename $(dirname $vpn_dir))
    gw=${vpn_dir##*/vpn-}
    [ -f /var/run/netns/$router ] || continue
    # Client / site isolation holds whatever the state, paused or not
    vpn_isolate_rules $router add
    if vpn_disabled $vpn_dir; then
        # Paused: make sure nothing runs and every tunnel route is a blackhole, on both nodes
        vpn_charon_alive $vpn_dir && vpn_stop_charon $vpn_dir
        vpn_frr_alive $gw && vpn_stop_frr $gw
        vpn_apply_routes $router $vpn_dir disabled
        vpn_apply_wg $router $vpn_dir $gw
        continue
    fi
    # Re-read the state under the lock: a notify may have run between the loop start and here
    if vpn_holds_vip $router $vpn_dir; then
        vpn_apply_routes $router $vpn_dir master
        vpn_start_charon $router $vpn_dir
        vpn_start_frr $router $gw
        vpn_ipsec_reconcile $router $vpn_dir
    else
        vpn_charon_alive $vpn_dir && vpn_stop_charon $vpn_dir
        vpn_frr_alive $gw && vpn_stop_frr $gw
        vpn_apply_routes $router $vpn_dir backup
    fi
    vpn_apply_wg $router $vpn_dir $gw
done
exit 0
