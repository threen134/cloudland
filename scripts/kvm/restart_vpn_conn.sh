#!/bin/bash

# Terminate the IKE SA of one connection and initiate it again. Sent to both VRRP nodes; only the one
# with charon running (the floating IP holder) does anything.

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

[ $# -lt 3 ] && die "$0 <router> <gw_ID> <connection name>"

ID=$1
router=router-$ID
gw=$2
name=$3
vpn_dir=$(vpn_dir_of $ID $gw)
[ -d "$vpn_dir" ] || exit 0
vpn_charon_alive $vpn_dir || exit 0

timeout 20 ip netns exec $router swanctl --terminate --ike $name --force --timeout 10 --uri unix://$vpn_dir/run/charon.vici >/dev/null 2>&1
if grep -q "^    $name {" $vpn_dir/swanctl.conf 2>/dev/null; then
    # start_action = start only fires at load time: initiate every child explicitly (one per pair in static mode)
    if awk -v n="    $name {" '$0 == n {f = 1} f && /start_action = start/ {found = 1} f && /^    }/ {exit} END {exit !found}' $vpn_dir/swanctl.conf; then
        for child in $(vpn_conn_children $vpn_dir/swanctl.conf $name); do
            timeout 30 ip netns exec $router swanctl --initiate --ike $name --child $child --timeout 20 --uri unix://$vpn_dir/run/charon.vici >/dev/null 2>&1
        done
    fi
fi
rm -f $vpn_dir/status.reported
exit 0
