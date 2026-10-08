#!/bin/bash
# Change the bandwidth limits of a floating IP attached to an instance, leaving everything else of it in place.
# Only the tc classes and filters (and the mark of the inbound traffic) are touched: create_floating.sh can not
# be run again for that, its "ip rule add" would add each policy rule a second time.
# Limits are in Mbit/s, 0 removes the limit. Classes, filters and the mark are those of create_floating.sh and
# clear_floating.sh (fip_lib.sh, numbered by the public address); the exit status says whether both limits hold.
# Nothing is written to stdout: every line there would be taken for a callback.

cd `dirname $0`
source ../cloudrc
source ./fip_lib.sh

usage="$0 <router> <ext_ip> <ext_vlan> <int_vlan> <mark_id> <inbound> <outbound>"
[ $# -lt 7 ] && echo "$usage" >&2 && exit 1

ID=$1
ext_ip=${2%/*}
ext_vlan=$3
int_vlan=$4
inbound=$6
outbound=$7
for n in "$ID" "$ext_vlan" "$int_vlan" "$5" "$inbound" "$outbound"; do
    [[ "$n" =~ ^[0-9]+$ ]] || { echo "$usage: not a number: $n" >&2; exit 1; }
done
[[ "$2" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+(/[0-9]+)?$ ]] || { echo "$usage: bad address $2" >&2; exit 1; }
router=router-$ID

ip netns list | grep -qw "^$router" || { echo "Router $router does not exist" >&2; exit 1; }
fip_instance_limits $ID $2 $ext_vlan $int_vlan $5 $inbound $outbound
