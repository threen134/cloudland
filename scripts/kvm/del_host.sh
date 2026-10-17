#!/bin/bash

cd `dirname $0`
source ../cloudrc

[ $# -lt 4 ] && die "$0 <router> <vlan> <mac> <ip>"

router=$1
[ "${router/router-/}" = "$router" ] && router=router-$1
vlan=$2
vm_mac=$3
vm_ip=${4%%/*}

vlan_dir=$cache_dir/router/$router/$vlan
dhcp_host=$vlan_dir/dhcp_hosts
# Lines are "<mac>,<name>.<domain>,<ip>": the fields compared whole, so a host whose name holds the address
# (ip-192-168-30-5) or whose MAC begins with this one stays. Rewritten as set_host.sh does: a temporary file of its
# own, readable by dnsmasq (nobody), renamed over the old one
if [ -f $dhcp_host ] && tmp=$(mktemp $dhcp_host.XXXXXX); then
    awk -F, -v mac="$vm_mac" -v ip="$vm_ip" '$1 != mac && $NF != ip' $dhcp_host >$tmp && chmod 644 $tmp &&
        mv -f $tmp $dhcp_host || rm -f $tmp
fi
ip netns exec $router dhcp_release ns-$vlan $vm_ip $vm_mac
dns_pid=`ps -ef | grep dnsmasq | grep "\<interface=ns-$vlan\>" | awk '{print $2}'`
[ -n "$dns_pid" ] && kill -HUP $dns_pid
ip netns exec $router dhcp_release ns-$vlan $vm_ip $vm_mac
echo "DHCP config for $vm_mac: $vm_ip in vlan $vlan was removed."
