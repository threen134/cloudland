#!/bin/bash

# Reconcile this node with the desired state of a transit gateway (vpc-transit-gateway-plan.md §2.4).
# Declarative: the JSON on stdin is the whole state of the gateway, the same for every node. What this node
# applied last is kept in $cache_dir/tgw/<ID>/state.json; whatever of it is no longer wanted is removed.
#
#   tgw-<ID>     netns forwarding between the members: ta-<att> per attachment, "ip rule iif ta-<att> lookup
#                <table>" selects the route table of the attachment (1000 + slot), each table ends with a
#                blackhole default
#   router-<N>   per member: veth tr-<att> (a /31 of 169.254.254.0/24), table 252 (tgw) with the reachable
#                prefixes via the gateway, the blackholes and a throw for each network of the VPC itself,
#                "ip rule pref 100 lookup 252" ahead of the floating IP tables and the main table, nonat entries
#                for the reachable prefixes (recorded in tgw.nonat: other scripts own the rest of the set), and
#                FORWARD rules that keep transit traffic away from router-0, the floating IP ports and VPN devices
#
# A state older than the newest one seen is dropped (asynchronous dispatch, callback retries): the generation is
# recorded as soon as a state is taken, the state itself only once it applied without error. An empty attachment
# list removes everything of the gateway; routers that were only here for it go to clear_local_router.sh. The
# attachments to remove are those of the last applied state and those whose router still names this gateway in
# tgw.owner (a run that failed half way did not record its state).
#
# stdin JSON: {"tgw", "generation", "attachments": [{"id", "router", "tr", "ta", "table", "reachable": [],
#              "blackhole": [], "throw": []}], "tables": [{"table", "routes": [{"prefix", "att"}]}]}
#             A large state comes gzip compressed and base64 encoded behind a "gz:" prefix.
# Reports:    |:-COMMAND-:| apply_tgw.sh '<ID>' '<generation>' 'ok|error' '<reason or ->' '<attachments>'

cd $(dirname $0)
source ../cloudrc

[ $# -lt 2 ] && die "$0 <tgw ID> <generation>"
ID=$1
generation=$2
[[ "$ID" =~ ^[0-9]+$ ]] && [[ "$generation" =~ ^[0-9]+$ ]] || die "$0: invalid arguments"
tgw=tgw-$ID
state_dir=$cache_dir/tgw/$ID
content=$(cat)
if [ "${content:0:3}" = "gz:" ]; then
    content=$(base64 -d <<<"${content:3}" 2>/dev/null | gunzip 2>/dev/null)
fi
count=$(jq -r '.attachments | length' <<<"$content" 2>/dev/null)

function report()
{
    local reason
    reason=$(echo "$2" | tr -d "'\n" | cut -c1-300)
    echo "|:-COMMAND-:| apply_tgw.sh '$ID' '$generation' '$1' '${reason:--}' '${count:-1}'"
}

if ! [[ "$count" =~ ^[0-9]+$ ]]; then
    count=1
    report error "unreadable state"
    exit 1
fi

mkdir -p $state_dir $run_dir/lock
exec 7>$run_dir/lock/tgw-$ID.lock
if ! flock -w 120 7; then
    report error "the transit gateway is busy on this node"
    exit 1
fi

seen=$(cat $state_dir/generation 2>/dev/null)
if [[ "$seen" =~ ^[0-9]+$ ]] && [ "$generation" -lt "$seen" ]; then
    log_debug $tgw "apply_tgw.sh: generation $generation is older than $seen, dropped"
    exit 0
fi
# Recorded before applying: a delayed older state must not undo a newer one that failed half way
echo $generation >$state_dir/generation
old=$(cat $state_dir/state.json 2>/dev/null)
[ -n "$old" ] || old='{"attachments":[],"tables":[]}'

errors=""
function fail()
{
    log_debug $tgw "apply_tgw.sh: $1"
    [ -z "$errors" ] && errors=$1
}

# The xtables lock is one for every netns, and the heartbeat and load balancer scripts take it all the time
function ipt()
{
    ip netns exec $1 iptables -w 10 "${@:2}"
}

# FORWARD rules of a member router: transit traffic never leaves through router-0 (it would be SNATed out to the
# internet when the destination subnet has no gateway port here yet) or a floating IP port, the internet side can
# not reach another VPC through the router, VPN clients and sites do not transit the gateway, and nothing goes from
# the gateway back to the gateway (a loop, or two gateways bridged by a router changing gateway)
tgw_forward_rules="tr+:ti-+ ti-+:tr+ tr+:te+ te+:tr+ tr+:wg+ wg+:tr+ tr+:ipsec+ ipsec+:tr+ tr+:tr+"

# nonat entries of a router that other scripts own as well, one per line: the networks of the VPC itself and its
# VPN prefixes (the throw lists of the attachment in the new and the last applied state), and what a VPN gateway
# route set added (set_vpn_route.sh). clapi keeps them apart from the reachable prefixes; this only guards the
# removal.
function nonat_keep()
{
    local router=$1 att=$2
    {
        jq -r --argjson id $att '.attachments[]? | select(.id == $id) | .throw[]?' <<<"$content"
        jq -r --argjson id $att '.attachments[]? | select(.id == $id) | .throw[]?' <<<"$old"
        cat $router_dir/$router/vpn-*/nonat.current 2>/dev/null | tr ' ' '\n'
    } | awk 'NF' | sort -u
}

# Remove what a router has of the gateway: routes, policy rule, nonat entries, forward rules, veth. Under its lock.
# The router-wide part only when this attachment still owns the router: a VPC moved to another gateway may have
# been set up by that gateway already (the two dispatches reach the node in any order)
function router_detach()
{
    local router=$1 att=$2 pair cidr owner keep
    ip netns exec $router ip link del tr-$att 2>/dev/null
    owner=$(cat $router_dir/$router/tgw.owner 2>/dev/null)
    [ -n "$owner" ] && [ "$owner" != "$ID $att" ] && return 0
    rm -f $router_dir/$router/tgw.owner
    ip netns exec $router ip route flush table $tgw_table 2>/dev/null
    while ip netns exec $router ip rule del pref 100 lookup $tgw_table 2>/dev/null; do :; done
    keep=$(nonat_keep $router $att)
    for cidr in $(cat $router_dir/$router/tgw.nonat 2>/dev/null); do
        grep -qxF "$cidr" <<<"$keep" || ip netns exec $router ipset del nonat $cidr 2>/dev/null
    done
    rm -f $router_dir/$router/tgw.nonat
    for pair in $tgw_forward_rules; do
        while ipt $router -D FORWARD -i ${pair%%:*} -o ${pair##*:} -j DROP 2>/dev/null; do :; done
    done
}

# "<type> <prefix>" of each route of "ip route show" on stdin; a host route is printed without its /32
function route_keys()
{
    awk '{t = "unicast"; p = $1} $1 == "blackhole" || $1 == "throw" || $1 == "unreachable" || $1 == "prohibit" {t = $1; p = $2}
        NF {if (p != "default" && p !~ /\//) p = p "/32"; print t, p}'
}

# Bring the routes of table 252 of a router to "<type> <prefix>" lines on stdin (type unicast, blackhole or throw)
function sync_table_routes()
{
    local router=$1 att=$2 via=$3 type prefix line
    local desired current
    desired=$(cat)
    while read -r type prefix; do
        [ -z "$prefix" ] && continue
        case $type in
        unicast) ip netns exec $router ip route replace $prefix via $via dev tr-$att table $tgw_table || fail "$router: route $prefix" ;;
        *) ip netns exec $router ip route replace $type $prefix table $tgw_table || fail "$router: $type $prefix" ;;
        esac
    done <<<"$desired"
    current=$(ip netns exec $router ip -4 route show table $tgw_table 2>/dev/null | route_keys)
    while read -r type prefix; do
        [ -z "$prefix" ] && continue
        grep -qxF "$type $prefix" <<<"$desired" && continue
        if [ "$type" = "unicast" ]; then
            ip netns exec $router ip route del $prefix table $tgw_table 2>/dev/null
        else
            ip netns exec $router ip route del $type $prefix table $tgw_table 2>/dev/null
        fi
    done <<<"$current"
}

# Plug one attachment into its router and the gateway. Under the router lock.
function router_attach()
{
    local router=$1 att=$2 tr=$3 ta=$4 entry pair cidr desired current dev keep
    # A VPC belongs to one attachment at a time (clapi), so another tr- here is left over from an attachment being
    # removed: it would carry the same /31 when it had the same slot in another gateway
    for dev in $(ip netns exec $router ip -o link show 2>/dev/null | awk -F': ' '{print $2}' | sed -n 's/^\(tr-[0-9][0-9]*\).*/\1/p'); do
        [ "$dev" = "tr-$att" ] || ip netns exec $router ip link del $dev 2>/dev/null
    done
    if ! ip netns exec $router ip link show tr-$att >/dev/null 2>&1; then
        ip netns exec $tgw ip link del ta-$att 2>/dev/null
        ip link del tr-$att 2>/dev/null
        ip link add tr-$att type veth peer name ta-$att || { fail "$router: veth tr-$att"; return 1; }
        ip link set tr-$att netns $router && ip link set ta-$att netns $tgw || {
            ip link del tr-$att 2>/dev/null
            fail "$router: moving veth tr-$att"
            return 1
        }
    fi
    mkdir -p $router_dir/$router
    echo "$ID $att" >$router_dir/$router/tgw.owner
    # Both ends live in a netns: set the MTU of each (VXLAN overhead, as on the other ports of the router)
    ip netns exec $router ip link set tr-$att mtu 1450 up
    ip netns exec $tgw ip link set ta-$att mtu 1450 up
    ip netns exec $router ip -4 addr show dev tr-$att | grep -q " $tr/31 " || {
        ip netns exec $router ip addr flush dev tr-$att
        ip netns exec $router ip addr add $tr/31 dev tr-$att || fail "$router: address of tr-$att"
    }
    ip netns exec $tgw ip -4 addr show dev ta-$att | grep -q " $ta/31 " || {
        ip netns exec $tgw ip addr flush dev ta-$att
        ip netns exec $tgw ip addr add $ta/31 dev ta-$att || fail "$tgw: address of ta-$att"
    }
    # The router keeps its loose mode: a source behind the gateway is in table 252, which the rule of pref 100
    # (no input interface selector) makes visible to the reverse path lookup
    ip netns exec $router sysctl -qw net.ipv4.conf.all.rp_filter=2 "net.ipv4.conf.tr-$att.rp_filter=2" >/dev/null 2>&1
    ip netns exec $tgw sysctl -qw "net.ipv4.conf.ta-$att.rp_filter=0" >/dev/null 2>&1

    # The FORWARD rules come before any route to the gateway: without them transit traffic could already leave
    # through router-0. A router missing one is left without routes this round.
    for pair in $tgw_forward_rules; do
        ipt $router -C FORWARD -i ${pair%%:*} -o ${pair##*:} -j DROP 2>/dev/null ||
            ipt $router -I FORWARD 1 -i ${pair%%:*} -o ${pair##*:} -j DROP || {
            fail "$router: forward rule $pair"
            return 1
        }
    done

    entry=$(jq -c --argjson id $att '.attachments[] | select(.id == $id)' <<<"$content")
    desired=$(jq -r '(.reachable[] | "unicast \(.)"), (.blackhole[] | "blackhole \(.)"), (.throw[] | "throw \(.)")' <<<"$entry")
    sync_table_routes $router $att $ta <<<"$desired"

    if ! ip netns exec $router ip rule show | grep -q "^100:.*lookup \(tgw\|$tgw_table\)"; then
        ip netns exec $router ip rule add pref 100 lookup $tgw_table || fail "$router: policy rule"
    fi

    # nonat: only the entries this script added are ever removed, and none that another script owns too
    desired=$(jq -r '.reachable[]' <<<"$entry" | tr '\n' ' ')
    current=$(cat $router_dir/$router/tgw.nonat 2>/dev/null)
    keep=$(nonat_keep $router $att)
    for cidr in $desired; do
        ip netns exec $router ipset add nonat $cidr -exist || fail "$router: nonat $cidr"
    done
    for cidr in $current; do
        case " $desired " in
        *" $cidr "*) ;;
        *) grep -qxF "$cidr" <<<"$keep" || ip netns exec $router ipset del nonat $cidr 2>/dev/null ;;
        esac
    done
    echo $desired >$router_dir/$router/tgw.nonat
}

new_atts=$(jq -r '.attachments[] | "\(.id) \(.router) \(.tr) \(.ta)"' <<<"$content")
new_routers=" $(awk '{print $2}' <<<"$new_atts" | tr '\n' ' ') "
new_ids=" $(awk '{print $1}' <<<"$new_atts" | tr '\n' ' ') "
# "<att> <router ID>" of what this node may still have of the gateway: the last applied state, and the routers
# whose tgw.owner names this gateway
old_atts=$( {
    jq -r '.attachments[]? | "\(.id) \(.router)"' <<<"$old"
    for f in $router_dir/router-*/tgw.owner; do
        [ -f "$f" ] || continue
        read -r owner_tgw owner_att <"$f"
        [ "$owner_tgw" = "$ID" ] && [[ "$owner_att" =~ ^[0-9]+$ ]] || continue
        r=${f%/tgw.owner}
        echo "$owner_att ${r##*/router-}"
    done
} | awk 'NF == 2' | sort -u)

# Attachments gone from the state: their routers lose what they had of the gateway, or only the old veth when
# the VPC is in the state under another attachment
released=""
while read -r att router_id; do
    [ -z "$att" ] && continue
    case "$new_ids" in *" $att "*) continue ;; esac
    router=router-$router_id
    if [ ! -f /var/run/netns/$router ]; then
        # The router went away with what it had (a reboot also keeps the directory): only the files are left
        [ "$(cat $router_dir/$router/tgw.owner 2>/dev/null)" = "$ID $att" ] &&
            rm -f $router_dir/$router/tgw.owner $router_dir/$router/tgw.nonat
        ip netns exec $tgw ip link del ta-$att 2>/dev/null
        continue
    fi
    if ! (
        flock -w 60 9 || exit 1
        case "$new_routers" in
        *" $router_id "*) ip netns exec $router ip link del tr-$att 2>/dev/null ;;
        *) router_detach $router $att ;;
        esac
    ) 9>$(router_lock_file $router); then
        fail "$router: router busy, attachment $att not removed"
        continue
    fi
    case "$new_routers" in *" $router_id "*) ;; *) released="$released $router" ;; esac
    ip netns exec $tgw ip link del ta-$att 2>/dev/null
done <<<"$old_atts"

if [ "$count" -eq 0 ]; then
    for router in $released; do
        ./clear_local_router.sh $router 2>/dev/null | grep '^|:-COMMAND-:|'
    done
    if [ -n "$errors" ]; then
        report error "$errors"
        exit 1
    fi
    ip netns del $tgw 2>/dev/null
    # The generation stays: a delayed older state must not bring the gateway back
    rm -f $state_dir/state.json
    log_debug $tgw "apply_tgw.sh: generation $generation, removed (released:$released)"
    report ok
    exit 0
fi

if [ ! -f /var/run/netns/$tgw ]; then
    ip netns add $tgw || { report error "can not create $tgw"; exit 1; }
fi
ip netns exec $tgw ip link set lo up
# No reverse path filtering in the gateway: its routes are only reachable through the "iif ta-<att>" rules, and the
# reverse path lookup carries no input interface, so even the loose mode would drop every packet
ip netns exec $tgw sysctl -qw net.ipv4.ip_forward=1 net.ipv4.conf.all.rp_filter=0 net.ipv4.conf.default.rp_filter=0 \
    net.ipv4.conf.all.send_redirects=0 net.ipv4.conf.default.send_redirects=0 >/dev/null 2>&1
# Nothing is addressed to the gateway itself
ip netns exec $tgw iptables -P INPUT DROP
ensure_tgw_table

while read -r att router_id tr ta; do
    [ -z "$att" ] && continue
    router=router-$router_id
    done_ok=false
    for attempt in 1 2 3; do
        # The router of a member VPC with no instance on this node exists only for the gateway: create it. Outside
        # the router lock, which create_local_router.sh takes itself; recheck under it (clear_local_router.sh may
        # have removed it in between)
        if [ ! -f /var/run/netns/$router ]; then
            ./create_local_router.sh $router 2>/dev/null | grep '^|:-COMMAND-:|'
        fi
        exec 9>$(router_lock_file $router)
        if ! flock -w 60 9; then
            exec 9>&-
            continue
        fi
        if [ -f /var/run/netns/$router ]; then
            router_attach $router $att $tr $ta
            done_ok=true
        fi
        exec 9>&-
        $done_ok && break
    done
    $done_ok || fail "$router: router not available"
done <<<"$new_atts"

# The gateway: one rule per attachment selecting its table, then the routes of every table
desired_rules=$(jq -r '.attachments[] | "iif ta-\(.id) lookup \(.table)"' <<<"$content")
# A rule whose ta- is gone is printed "iif ta-<att> [detached] lookup ..."; it still has to be found and removed
current_rules=$(ip netns exec $tgw ip rule show | sed 's/ \[detached\]//' |
    sed -n 's/^[0-9]*:[[:space:]]*from all \(iif ta-[0-9]* lookup [0-9]*\).*/\1/p')
while read -r rule; do
    [ -z "$rule" ] && continue
    grep -qxF "$rule" <<<"$desired_rules" || ip netns exec $tgw ip rule del $rule 2>/dev/null
done <<<"$current_rules"
while read -r rule; do
    [ -z "$rule" ] && continue
    grep -qxF "$rule" <<<"$current_rules" || ip netns exec $tgw ip rule add $rule || fail "$tgw: rule $rule"
done <<<"$desired_rules"

tables=$(jq -r '.tables[].table' <<<"$content")
for table in $tables; do
    desired=$(jq -r --argjson t $table '. as $s | .tables[] | select(.table == $t) | .routes[] |
        if .att == 0 then "blackhole \(.prefix)"
        else (.att as $a | ($s.attachments[] | select(.id == $a) | "\(.tr) ta-\(.id)") as $hop | "unicast \(.prefix) \($hop)") end' <<<"$content")
    desired="$desired
blackhole default"
    while read -r type prefix hop dev; do
        [ -z "$prefix" ] && continue
        if [ "$type" = "unicast" ]; then
            ip netns exec $tgw ip route replace $prefix via $hop dev $dev table $table || fail "$tgw: route $prefix table $table"
        else
            ip netns exec $tgw ip route replace blackhole $prefix table $table || fail "$tgw: blackhole $prefix table $table"
        fi
    done <<<"$desired"
    keys=$(awk '{print $1, $2}' <<<"$desired")
    ip netns exec $tgw ip -4 route show table $table 2>/dev/null | route_keys |
        while read -r type prefix; do
            grep -qxF "$type $prefix" <<<"$keys" && continue
            if [ "$type" = "unicast" ]; then
                ip netns exec $tgw ip route del $prefix table $table 2>/dev/null
            else
                ip netns exec $tgw ip route del $type $prefix table $table 2>/dev/null
            fi
        done
done
# Tables of deleted route tables
for table in $(ip netns exec $tgw ip -4 route show table all 2>/dev/null | sed -n 's/.* table \([0-9][0-9]*\).*/\1/p' | sort -u); do
    [ "$table" -ge 1000 ] || continue
    grep -qx "$table" <<<"$tables" || ip netns exec $tgw ip route flush table $table
done
# Veths of attachments that are not in the state (a run interrupted half way)
for dev in $(ip netns exec $tgw ip -o link show 2>/dev/null | awk -F': ' '{print $2}' | sed -n 's/^ta-\([0-9][0-9]*\).*/\1/p'); do
    case "$new_ids" in *" $dev "*) ;; *) ip netns exec $tgw ip link del ta-$dev 2>/dev/null ;; esac
done

for router in $released; do
    ./clear_local_router.sh $router 2>/dev/null | grep '^|:-COMMAND-:|'
done

if [ -n "$errors" ]; then
    report error "$errors"
    exit 1
fi
echo "$content" >$state_dir/state.json
echo $generation >$state_dir/generation
log_debug $tgw "apply_tgw.sh: generation $generation applied ($count attachments, released:$released)"
report ok
exit 0
