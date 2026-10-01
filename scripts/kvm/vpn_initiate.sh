#!/bin/bash

# Bring IPsec tunnels of a VPN gateway up in the background, away from the lb lock (vpn_lib.sh
# vpn_ipsec_reconcile explains why): started by the heartbeat watchdog (check_vpn_process.sh) and by
# create_vpn_ipsec_conf.sh, never by clapi. stdout is not read: the starters redirect it.
# Usage: vpn_initiate.sh reconcile <router ID> <gw ID>
#          bring the SAs of every tunnel back to the configuration, one run per gateway at a time
#        vpn_initiate.sh initiate <router ID> <gw ID> <tunnel name>
#          initiate a tunnel the configuration script has just reloaded and terminated
# A tunnel is worked on by one process at a time (initiate-<name>.lock): two initiates of the same child
# build a duplicate CHILD_SA.

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

[ $# -lt 3 ] && exit 1
mode=$1
ID=$2
gw=$3
name=$4
router=router-$ID
vpn_dir=$(vpn_dir_of $ID $gw)
[ -d "$vpn_dir" ] || exit 0

case $mode in
reconcile)
    exec 8>$vpn_dir/reconcile.lock
    flock -n 8 || exit 0
    vpn_ipsec_reconcile_now $router $vpn_dir
    ;;
initiate)
    [ -n "$name" ] || exit 1
    (
        # a reconciliation of this tunnel ends within a minute (20 s per child); the new configuration is
        # what counts, so wait for it rather than skip
        flock -w 90 7 || exit 0
        vpn_tunnel_run $router $vpn_dir $name no
    ) 7>$vpn_dir/initiate-$name.lock
    ;;
esac
exit 0
