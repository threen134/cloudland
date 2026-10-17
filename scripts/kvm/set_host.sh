#!/bin/bash

cd `dirname $0`
source ../cloudrc

[ $# -lt 5 ] && echo "$0 <router> <vlan> <mac> <name> <ip> [domain]"

router=router-$1
vlan=$2
vm_mac=$3
vm_name=$4
vm_ip=${5%%/*}
domain=$6
[ -z "$domain" ] && domain=$cloud_domain

vlan_dir=$cache_dir/router/$router/$vlan
mkdir -p $vlan_dir
dhcp_host=$vlan_dir/dhcp_hosts
touch $dhcp_host
# Lines are "<mac>,<name>.<domain>,<ip>": drop the earlier ones of this address or this MAC (an address changed on
# the same NIC), the fields compared whole. A pattern on the line would also take a host whose name holds the address
# (the dots match any character), as ip-192-168-30-5. The new file is renamed over the old one: a temporary file of
# its own (set_host and del_host may run at once), readable by dnsmasq, which runs as nobody
tmp=$(mktemp $dhcp_host.XXXXXX) || die "can not write the DHCP bindings of vlan $vlan"
awk -F, -v mac="$vm_mac" -v ip="$vm_ip" '$1 != mac && $NF != ip' $dhcp_host >$tmp &&
    echo "$vm_mac,$vm_name.$domain,$vm_ip" >>$tmp && chmod 644 $tmp && mv -f $tmp $dhcp_host || rm -f $tmp
dnsmasq_pid=$(ps -ef | grep dnsmasq | grep "\<interface=ns-$vlan\>" | awk '{print $2}')
[ -n "$dnsmasq_pid" ] && kill -HUP $dnsmasq_pid
echo "DHCP config for $vm_mac: $vm_ip in vlan $vlan was setup."
