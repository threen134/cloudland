#!/bin/bash
# Bandwidth limits of floating IPs, shared by create_floating.sh, set_floating_bandwidth.sh, clear_floating.sh
# (instances) and create_lb_floating.sh, clear_lb_floating.sh (load balancers, VPN gateways). Source after cloudrc.
#
# Numbering. The tc IDs come from the public address, not from the floating IP id (an id grows for ever: the
# decimal id read as a hexadecimal class minor stopped working at 10000):
#   n = the low 16 bits of the public address a.b.c.d (c * 256 + d)
#   class 1:<n in hexadecimal> under the htb qdisc (handle 1:) of the device, filter priority n (decimal)
# Devices, one class and one filter per public address on each:
#   ns-<int vlan>            router netns  inbound of an instance floating IP: the router marks what it forwards
#                                          to the floating IP (mangle PREROUTING, mark = floating IP id), fw filter
#   te-<router>-<ext vlan>   router netns  outbound of every floating IP of the router in that VLAN, instances,
#                                          load balancers and VPN gateways alike: u32 filter on the source address
#   ext-<router>-<ext vlan>  host          inbound of a load balancer / VPN gateway floating IP: u32 filter on the
#                                          destination address. The veth peer of te-: tc only shapes what leaves a
#                                          device, and traffic to a VIP enters te- and ends in the router (haproxy,
#                                          charon); the destination filter the load balancers had on te- never matched.
# So the two directions of one address never share a device, and each device only carries addresses of one kind
# of lookup. te- and ext- carry the addresses of the public subnets of one VLAN, ns- those of the floating IPs of
# its instances (any public VLAN): distinct low 16 bits as long as those subnets do not repeat them.
# Known limits:
#   - n = 0 is the qdisc itself and n = 0x10 its default class ("default 10" is hexadecimal; no such class is
#     ever created, unclassified traffic goes out unshaped): addresses with low 16 bits 0.0 or 0.16 get no limit.
#   - two public addresses with the same low 16 bits on one device (two public subnets of a VLAN that differ only
#     above them, or floating IPs of one internal subnet from such subnets): the second one is refused and logged,
#     the limit of the first is never overwritten (fip_tc_claim).
#   - the limits set before this numbering (class 1:<id>, priority <id>) are removed when their floating IP is
#     set again or cleared (fip_tc_clear_legacy); until then a new address whose priority or class they hold is
#     refused like any other conflict.
# Nothing is written to stdout (the callback channel): refusals go to stderr (cloudlet log) and the debug log.

# The number of an address: fip_tc_n <a.b.c.d> -> n; fails for the reserved values and anything but IPv4
function fip_tc_n()
{
    local a b c d
    [[ "$1" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]] || return 1
    IFS=. read a b c d <<<"$1"
    local n=$((c * 256 + d))
    [ $n -ne 0 ] && [ $n -ne 16 ] || return 1
    echo $n
}

# tc in the router netns, or in the host for "-": fip_tc <router|-> <tc arguments>
function fip_tc()
{
    local ns=$1
    shift
    if [ "$ns" = "-" ]; then
        tc "$@"
    else
        ip netns exec $ns tc "$@"
    fi
}

# The identity of a filter as tc prints it: fip_u32_token src|dst <a.b.c.d>, fip_fw_token <mark>
function fip_u32_token()
{
    local a b c d at=12
    IFS=. read a b c d <<<"$2"
    [ "$1" = "dst" ] && at=16
    printf 'match %02x%02x%02x%02x/ffffffff at %d' $a $b $c $d $at
}
function fip_fw_token()
{
    printf 'handle 0x%x ' $1
}

function fip_tc_refused()
{
    echo "floating IP $1: bandwidth limit not set on $2: $3" >&2
    log_debug "floating-ip-$1" "bandwidth limit not set on $2: $3"
}

# True when priority <n> and class 1:<hex> of <dev> are free or already this address's (its filter carries
# <token>): fip_tc_claim <router|-> <dev> <n> <token>. A filter of another owner at that priority, or one of
# another priority sending to that class, is a conflict.
function fip_tc_claim()
{
    local ns=$1 dev=$2 n=$3 token=$4
    fip_tc $ns filter show dev $dev parent 1: 2>/dev/null | awk -v n=$n -v cls="1:$(printf %x $n)" -v token="$token" '
        /^filter / {
            p = ""
            for (i = 1; i < NF; i++) if ($i == "pref") p = $(i + 1)
            if (p == "") next
            cur = p
        }
        cur != "" { text[cur] = text[cur] " " $0 " " }
        END {
            for (p in text) {
                t = text[p]
                if (p == n) { if (index(t, token) == 0) exit 1; continue }
                if (index(t, " flowid " cls " ") || index(t, " classid " cls " ")) exit 1
            }
        }'
}

# Set or remove the limit of one address on one device: fip_tc_limit <router|-> <dev> <address> <token> <mbit>
# <filter arguments...>; 0 Mbit/s removes it. Fails (and says why) when the address has no number or its number
# is taken by another address on the device.
function fip_tc_limit()
{
    local ns=$1 dev=$2 addr=$3 token=$4 rate=$5 n hex
    shift 5
    if ! n=$(fip_tc_n $addr); then
        [ "$rate" -gt 0 ] && fip_tc_refused $addr $dev "no tc number for this address (low 16 bits 0.0 or 0.16)"
        [ "$rate" -gt 0 ] && return 1
        return 0
    fi
    if ! fip_tc_claim $ns $dev $n "$token"; then
        # nothing of this address to remove there
        [ "$rate" -gt 0 ] || return 0
        fip_tc_refused $addr $dev "priority $n or class 1:$(printf %x $n) is used by another address"
        return 1
    fi
    hex=$(printf %x $n)
    fip_tc $ns filter del dev $dev parent 1:0 prio $n 2>/dev/null
    if [ "$rate" -gt 0 ]; then
        fip_tc $ns qdisc add dev $dev root handle 1: htb default 10 2>/dev/null
        fip_tc $ns class replace dev $dev parent 1: classid 1:$hex htb rate ${rate}mbit burst ${rate}kbit &&
            fip_tc $ns filter add dev $dev protocol ip parent 1:0 prio $n "$@" flowid 1:$hex
    else
        fip_tc $ns class del dev $dev parent 1: classid 1:$hex 2>/dev/null
        return 0
    fi
}

# Remove what the numbering before this one left for a floating IP: fip_tc_clear_legacy <router|-> <dev> <id>
# <token>. Its filter had priority <id> and sent to class 1:<id> (the decimal id read as hexadecimal); only a
# filter carrying this address's token is taken, with its class.
function fip_tc_clear_legacy()
{
    local ns=$1 dev=$2 id=$3 token=$4 cur
    [[ "$id" =~ ^[1-9][0-9]*$ ]] && [ "$id" -le 65535 ] || return 0
    cur=$(fip_tc $ns filter show dev $dev parent 1: prio $id 2>/dev/null)
    [ -n "$cur" ] && grep -qF -- "$token" <<<"$cur" || return 0
    fip_tc $ns filter del dev $dev parent 1:0 prio $id 2>/dev/null
    [[ "$id" =~ ^[0-9]{1,4}$ ]] && fip_tc $ns class del dev $dev parent 1: classid 1:$id 2>/dev/null
    return 0
}

# All limits of an instance floating IP: fip_instance_limits <router ID> <address> <ext vlan|-> <int vlan>
# <floating IP id> <inbound Mbit/s> <outbound Mbit/s>. Inbound on ns-<int vlan> with the mark (the floating IP
# id), outbound on te-<router>-<ext vlan> (skipped for "-": the port is not known). Fails when a limit could
# not be set.
function fip_instance_limits()
{
    local ID=$1 addr=${2%/*} ext_vlan=$3 int_vlan=$4 mark=$(($5 % 2147483647)) inbound=${6:-0} outbound=${7:-0} rc=0
    local router=router-$ID token
    token=$(fip_fw_token $mark)
    fip_tc_clear_legacy $router ns-$int_vlan $mark "$token"
    if [ "$inbound" -gt 0 ]; then
        ip netns exec $router iptables -t mangle -C PREROUTING -d $addr -j MARK --set-mark $mark 2>/dev/null ||
            ip netns exec $router iptables -t mangle -I PREROUTING -d $addr -j MARK --set-mark $mark || rc=1
    else
        while ip netns exec $router iptables -t mangle -D PREROUTING -d $addr -j MARK --set-mark $mark 2>/dev/null; do :; done
    fi
    fip_tc_limit $router ns-$int_vlan $addr "$token" $inbound handle $mark fw || rc=1
    if [ "$ext_vlan" != "-" ]; then
        fip_tc_limit $router te-$ID-$ext_vlan $addr "$(fip_u32_token src $addr)" $outbound u32 match ip src $addr/32 || rc=1
    fi
    return $rc
}

# All limits of a load balancer or VPN gateway floating IP: fip_vip_limits <router ID> <address> <ext vlan>
# <floating IP id> <inbound Mbit/s> <outbound Mbit/s>. Inbound on the host side ext- of the port, outbound on te-.
function fip_vip_limits()
{
    local ID=$1 addr=${2%/*} ext_vlan=$3 id=$(($4 % 2147483647)) inbound=${5:-0} outbound=${6:-0} rc=0
    local router=router-$ID
    # before this numbering the inbound filter (destination) sat on te-
    fip_tc_clear_legacy $router te-$ID-$ext_vlan $id "$(fip_u32_token dst $addr)"
    fip_tc_limit - ext-$ID-$ext_vlan $addr "$(fip_u32_token dst $addr)" $inbound u32 match ip dst $addr/32 || rc=1
    fip_tc_limit $router te-$ID-$ext_vlan $addr "$(fip_u32_token src $addr)" $outbound u32 match ip src $addr/32 || rc=1
    return $rc
}
