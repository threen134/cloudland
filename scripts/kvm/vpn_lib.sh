#!/bin/bash
# Shared helpers for the VPN gateway scripts. Source after cloudrc (needs router_dir, lb_lock_file, NODE_ID).
#
# Layout on a node, per gateway (see docs/architecture/plan/vpn-gateway-plan.md §3.4):
#   $router_dir/router-<N>/vrrp-<vrrp id>/   keepalived (same layout as a load balancer, so check_lb_process.sh
#                                             restarts it)
#   $router_dir/router-<N>/vpn-<gw id>/      strongswan.conf, swanctl.conf, run/ (charon pid + vici socket,
#                                             bind-mounted over /run for that charon), wg.conf, tunnel_routes,
#                                             state files (vip, peer_ip, vrrp_id, vrrp_vlan, routes.*, nonat.current)
#   /etc/frr/vpn-<gw id>/                    FRR pathspace config (frr.conf, vtysh.conf); daemons run with -N
#   /var/run/frr/vpn-<gw id>/                FRR pid files and sockets
#
# Routes on the VRRP pair are owned by vpn_notify.sh (role dependent): nothing else installs them. On an
# active_active gateway (ha_mode file) FRR owns the tunnel prefixes on both nodes instead, and the role
# only decides where the client VPN pool goes.
#
# Other nodes of the VPC route through the gateway nodes (set_vpn_route.sh); vpn_nexthop_watch.sh probes
# those nodes and moves the routes to the next one when a node stops answering (plan §8.2, F7).

vpn_scripts=$(cd $(dirname ${BASH_SOURCE[0]}) && pwd)
charon_bin=/usr/lib/ipsec/charon
frr_bin_dir=/usr/lib/frr

# Assign shell variables from JSON fields: vpn_json_read '<json>' var=<jq path> ...
# One variable per field through @sh, so an empty string keeps its slot; a single read over jq's line
# output drops the empty line and shifts every later variable (a responder-only connection has an empty
# remote_gateway). null becomes the string "null" (the callers test for it), numbers and booleans their text
vpn_json_read()
{
    local json=$1 spec filter=""
    shift
    for spec in "$@"; do
        filter="$filter@sh \"${spec%%=*}=\\((${spec#*=}) | if . == null then \"null\" else tostring end)\", "
    done
    eval "$(jq -r "${filter%, }" <<<"$json")"
}

# Names of the CHILD_SAs of a connection block in swanctl.conf: vpn_conn_children <conf> <connection name>
vpn_conn_children()
{
    awk -v n="    $2 {" '$0 == n {f = 1; next} f && /^    }/ {exit} f && /^            net[0-9]+ \{/ {print $1}' $1
}

# Drop forwarding between WireGuard clients and IPsec sites in both directions: vpn_isolate_rules <router> add|del.
# A VPC has at most one gateway, so the wildcard interface names only ever match that gateway's devices.
# The rules are the only thing keeping clients and sites apart: create_vpn_gateway.sh adds them and the
# watchdog re-asserts them on every heartbeat, so a failed insert or a late clear of a replaced gateway
# heals within one heartbeat instead of silently lifting the isolation.
vpn_isolate_rules()
{
    local router=$1 op=$2 pair
    for pair in "wg+ ipsec+" "ipsec+ wg+"; do
        set -- $pair
        if [ "$op" = "add" ]; then
            ip netns exec $router iptables -C FORWARD -i $1 -o $2 -j DROP 2>/dev/null ||
                ip netns exec $router iptables -I FORWARD 1 -i $1 -o $2 -j DROP ||
                log_debug "$(basename $0)" "vpn: failed to add the isolation rule $1 -> $2 in $router"
        else
            while ip netns exec $router iptables -D FORWARD -i $1 -o $2 -j DROP 2>/dev/null; do :; done
        fi
    done
}

# Configured (local_ts|remote_ts) pairs of a connection block: vpn_conn_pairs <conf> <connection name>
vpn_conn_pairs()
{
    awk -v n="    $2 {" '$0 == n {f = 1; next} f && /^    }/ {exit} f && /^ +local_ts = / {l = $3} f && /^ +remote_ts = / {print l "|" $3}' $1 | sort -u
}

# True when this node initiates the connection block <name> of <conf> (start_action = start, which sits deep
# in the block, past the ike / local / remote sections): vpn_conn_initiator <conf> <name>
vpn_conn_initiator()
{
    awk -v n="    $2 {" '$0 == n {f = 1} f && /start_action = start/ {found = 1} f && /^    }/ {exit} END {exit !found}' $1
}

# Bring the installed CHILD_SAs of every connection back to the configured traffic selectors:
# vpn_ipsec_reconcile <router> <vpn_dir>. Both sides of a site connection are reconfigured at different
# moments, and close_action = start makes each side re-create its children with whatever configuration
# it has at that instant, so after a change of route mode or networks a tunnel can be left with the old
# selectors on both ends (the BGP link address is then outside every SA). close_action is therefore none
# and this also re-initiates a tunnel the peer closed (initiator side only). Called from the watchdog on
# the nodes running the tunnels; a connection is re-initiated at most once a minute.
# The work runs in the background (vpn_initiate.sh reconcile, one per gateway at a time): an initiate waits
# for the peer, 20 s per child and longer while an unreachable peer is retransmitted to, and the watchdog
# holds the lb lock. Under the lock three unreachable peers kept it for a minute: the configuration scripts
# of the node waited behind it with the whole cloudlet queue, and check_lb_process.sh skipped its rounds.
vpn_ipsec_reconcile()
{
    local router=$1 vpn_dir=$2
    [ -f $vpn_dir/conn_names ] || return 0
    vpn_charon_alive $vpn_dir || return 0
    setsid bash $vpn_scripts/vpn_initiate.sh reconcile ${router#router-} ${vpn_dir##*/vpn-} >/dev/null 2>&1 9>&- </dev/null &
}

# The reconciliation itself, run by vpn_initiate.sh: vpn_ipsec_reconcile_now <router> <vpn_dir>. Every tunnel
# is looked at in parallel under its own lock (initiate-<name>.lock, shared with vpn_initiate.sh initiate); one
# that another process is bringing up (a configuration change) is left to it.
vpn_ipsec_reconcile_now()
{
    local router=$1 vpn_dir=$2 name
    [ -f $vpn_dir/conn_names ] || return 0
    for name in $(cat $vpn_dir/conn_names); do
        vpn_disabled $vpn_dir && break
        vpn_charon_alive $vpn_dir || break
        (
            flock -n 7 || exit 0
            vpn_tunnel_reconcile $router $vpn_dir $name
        ) 7>$vpn_dir/initiate-$name.lock 2>/dev/null &
    done
    wait
}

# The swanctl listing of the SAs of a tunnel: vpn_tunnel_sas <router> <vpn_dir> <name>. Fails when charon
# does not answer.
vpn_tunnel_sas()
{
    timeout 5 ip netns exec $1 swanctl --list-sas --ike $3 --uri unix://$2/run/charon.vici 2>/dev/null
    [ $? -ne 124 ]
}

# One tunnel of vpn_ipsec_reconcile_now, the caller holding its lock: vpn_tunnel_reconcile <router> <vpn_dir> <name>.
# Decided under the lock, so a reload and initiate that ran meanwhile is seen; at most once a minute.
vpn_tunnel_reconcile()
{
    local router=$1 vpn_dir=$2 name=$3 conf=$2/swanctl.conf want have sas stamp terminate=no
    want=$(vpn_conn_pairs $conf $name)
    sas=$(vpn_tunnel_sas $router $vpn_dir $name) || return 0
    have=$(awk '/^    local  [0-9]/ {l = $2} /^    remote [0-9]/ {print l "|" $2}' <<<"$sas" | sort -u)
    [ "$have" = "$want" ] && return 0
    # nothing installed and we are not the initiator: only the peer can bring it up
    [ -z "$have" ] && ! vpn_conn_initiator $conf $name && return 0
    stamp=$vpn_dir/reconcile-$name
    [ -f $stamp ] && [ $(($(date +%s) - $(stat -c %Y $stamp))) -lt 60 ] && return 0
    touch $stamp
    log_debug "$(basename $0)" "vpn: connection $name has selectors [$(echo $have)] but is configured for [$(echo $want)], re-initiating"
    [ -n "$have" ] && terminate=yes
    vpn_tunnel_run $router $vpn_dir $name $terminate
}

# Terminate (when told to) and initiate the children of a tunnel: vpn_tunnel_run <router> <vpn_dir> <name> yes|no.
# Only the initiator side initiates; the caller holds the initiate lock of the tunnel. Stops as soon as the
# gateway is paused or charon went away (a failover, a deletion): nothing here starts charon. A child that is
# installed, or whose IKE SA charon is already bringing up (keyingtries = 0 retries for ever, a reload with
# start_action = start initiates by itself), is left alone: a second initiate queues a duplicate CHILD_SA.
vpn_tunnel_run()
{
    local router=$1 vpn_dir=$2 name=$3 terminate=$4 child sas
    vpn_charon_alive $vpn_dir || return 0
    if [ "$terminate" = "yes" ]; then
        timeout 20 ip netns exec $router swanctl --terminate --ike $name --force --timeout 10 --uri unix://$vpn_dir/run/charon.vici >/dev/null 2>&1
    fi
    vpn_conn_initiator $vpn_dir/swanctl.conf $name || return 0
    for child in $(vpn_conn_children $vpn_dir/swanctl.conf $name); do
        vpn_disabled $vpn_dir && return 0
        vpn_charon_alive $vpn_dir || return 0
        sas=$(vpn_tunnel_sas $router $vpn_dir $name) || return 0
        grep -qE "^  $child: #[0-9]+, reqid [0-9]+, INSTALLED," <<<"$sas" && continue
        grep -qE "^$name: #[0-9]+, (CREATED|CONNECTING)," <<<"$sas" && continue
        timeout 30 ip netns exec $router swanctl --initiate --ike $name --child $child --timeout 20 --uri unix://$vpn_dir/run/charon.vici >/dev/null 2>&1
    done
}

# Initiate tunnels in the background, outside the lb lock: vpn_initiate_bg <router> <vpn_dir> <name>...
# (vpn_initiate.sh initiate: one process per tunnel, waiting for a reconciliation of the same tunnel to end)
vpn_initiate_bg()
{
    local router=$1 vpn_dir=$2 name
    shift 2
    for name in "$@"; do
        setsid bash $vpn_scripts/vpn_initiate.sh initiate ${router#router-} ${vpn_dir##*/vpn-} $name >/dev/null 2>&1 9>&- </dev/null &
    done
}

# A disabled (paused) gateway has a "disabled" file in its directory, written by create_vpn_gateway.sh on
# both VRRP nodes. Everything that starts a tunnel process honours it: no charon, no FRR, the WireGuard
# interface down and every tunnel route blackholed. keepalived and the floating IP keep running.
vpn_disabled()
{
    [ -f $1/disabled ]
}

# An active_active gateway (create_vpn_gateway.sh writes ha_mode): each node runs the tunnels of its own
# fixed address, charon and FRR stay up whatever the VRRP role, the two nodes exchange routes over iBGP,
# and the role only moves the client VPN floating IP (plan §4.2)
vpn_is_aa()
{
    [ "$(cat $1/ha_mode 2>/dev/null)" = "active_active" ]
}

# True when this node runs the tunnels of the gateway: both nodes of an active_active gateway, the floating
# IP holder of an active_standby one: vpn_runs_tunnels <router> <vpn_dir>
vpn_runs_tunnels()
{
    vpn_is_aa $2 || vpn_holds_vip $1 $2
}

# Address-based client / site isolation: vpn_isolate_pool_rules <router> <vpn_dir> add|del. The interface
# rules of vpn_isolate_rules only see a packet that goes from wg- to ipsec- on one node; on an active_active
# gateway a client packet enters the floating IP holder and may leave through a tunnel of the other node,
# where it arrives on the VRRP NIC. These rules match the client pool itself (client_cidr, written by
# create_vpn_gateway.sh while the client VPN is on) and sit on both nodes; client_cidr.rules is what is installed.
vpn_isolate_pool_rules()
{
    local router=$1 vpn_dir=$2 op=$3 old new cidr
    old=$(cat $vpn_dir/client_cidr.rules 2>/dev/null)
    new=""
    [ "$op" = "add" ] && new=$(cat $vpn_dir/client_cidr 2>/dev/null)
    for cidr in $old; do
        [ "$cidr" = "$new" ] && continue
        while ip netns exec $router iptables -D FORWARD -s $cidr -o ipsec+ -j DROP 2>/dev/null; do :; done
        while ip netns exec $router iptables -D FORWARD -i ipsec+ -d $cidr -j DROP 2>/dev/null; do :; done
    done
    if [ -n "$new" ]; then
        ip netns exec $router iptables -C FORWARD -s $new -o ipsec+ -j DROP 2>/dev/null ||
            ip netns exec $router iptables -I FORWARD 1 -s $new -o ipsec+ -j DROP ||
            log_debug "$(basename $0)" "vpn: failed to add the pool isolation rule for $new in $router"
        ip netns exec $router iptables -C FORWARD -i ipsec+ -d $new -j DROP 2>/dev/null ||
            ip netns exec $router iptables -I FORWARD 1 -i ipsec+ -d $new -j DROP ||
            log_debug "$(basename $0)" "vpn: failed to add the pool isolation rule for $new in $router"
        echo $new >$vpn_dir/client_cidr.rules
    else
        rm -f $vpn_dir/client_cidr.rules
    fi
}

# TCP MSS inside the tunnels: vpn_mss_rules <router> add|del. Handshakes (SYN and SYN-ACK) leaving through
# a tunnel are clamped to its path MTU (IPsec 1360, WireGuard 1390); those coming out of a tunnel get the value set
# explicitly: --clamp-mss-to-pmtu would use the MTU of the interface towards the instance (1450), the
# instance then sent segments the tunnel cannot carry and relied on PMTU discovery, losing the first full
# segments of every connection and again each time its PMTU cache expired (found against IBM Cloud VPN).
# Re-asserted by the watchdog, which also replaces the old inbound rules of existing gateways.
vpn_mss_rules()
{
    local router=$1 op=$2 rule
    # SYN and SYN-ACK: --syn only matches a SYN without ACK, so the MSS a peer announced in its SYN-ACK
    # reached the instance untouched (1360 from IBM Cloud, 1460 from a peer that does not clamp)
    local rules=(
        "-o ipsec+ -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu"
        "-i ipsec+ -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1320"
        "-o wg+ -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu"
        "-i wg+ -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1350"
    )
    local stale=(
        "-o ipsec+ -p tcp --syn -j TCPMSS --clamp-mss-to-pmtu"
        "-i ipsec+ -p tcp --syn -j TCPMSS --clamp-mss-to-pmtu"
        "-i ipsec+ -p tcp --syn -j TCPMSS --set-mss 1320"
        "-o wg+ -p tcp --syn -j TCPMSS --clamp-mss-to-pmtu"
        "-i wg+ -p tcp --syn -j TCPMSS --clamp-mss-to-pmtu"
        "-i wg+ -p tcp --syn -j TCPMSS --set-mss 1350"
    )
    if [ "$op" = "add" ]; then
        for rule in "${stale[@]}"; do
            while ip netns exec $router iptables -t mangle -D FORWARD $rule 2>/dev/null; do :; done
        done
        for rule in "${rules[@]}"; do
            ip netns exec $router iptables -t mangle -C FORWARD $rule 2>/dev/null ||
                ip netns exec $router iptables -t mangle -A FORWARD $rule ||
                log_debug "$(basename $0)" "vpn: failed to add the MSS rule '$rule' in $router"
        done
    else
        for rule in "${rules[@]}" "${stale[@]}"; do
            while ip netns exec $router iptables -t mangle -D FORWARD $rule 2>/dev/null; do :; done
        done
    fi
}

# Tunnel traffic is read from the XFRM interfaces, whose counters survive CHILD_SA rekeys (the SA counters
# restart at every esp_lifetime). What is reported is the traffic since charon started on this node (a
# takeover, a restart): vpn_start_charon calls vpn_traffic_baseline and vpn_traffic_since_base subtracts it.
# vpn_dev_bytes <router> <dev> -> "<rx bytes> <tx bytes>"
# Fails without output when the counters cannot be read: a 0 would look like a counter reset to Prometheus
# and the next real value like the whole lifetime of the device transferred at once
vpn_dev_bytes()
{
    local out
    out=$(ip netns exec $1 cat /sys/class/net/$2/statistics/rx_bytes /sys/class/net/$2/statistics/tx_bytes 2>/dev/null) || return 1
    set -- $out
    [ $# -eq 2 ] || return 1
    echo "$1 $2"
}

# vpn_traffic_baseline <router> <vpn_dir>: snapshot every tunnel interface of the gateway
vpn_traffic_baseline()
{
    local router=$1 vpn_dir=$2 dev cur
    : >$vpn_dir/traffic.base.new
    for dev in $(cat $vpn_dir/ifaces 2>/dev/null); do
        cur=$(vpn_dev_bytes $router $dev) && echo "$dev $cur" >>$vpn_dir/traffic.base.new
    done
    mv -f $vpn_dir/traffic.base.new $vpn_dir/traffic.base
}

# XFRM interface id of a connection: vpn_conn_ifid <vpn_dir> <connection name>
vpn_conn_ifid()
{
    local vpn_dir=$1 name=$2 if_id
    if_id=$(awk -v n=$name '$1 == n {print $2}' $vpn_dir/conn_ifids 2>/dev/null)
    # gateways configured before conn_ifids existed: the if_id sits in the connection block
    [ -n "$if_id" ] || if_id=$(awk -v n="    $name {" '$0 == n {f = 1} f && /if_id_in = / {print $3; exit} f && /^    }$/ {exit}' $vpn_dir/swanctl.conf 2>/dev/null)
    echo $if_id
}

# vpn_traffic_since_base <vpn_dir> <dev> <rx> <tx> -> "<bytes in> <bytes out>" since charon started here,
# from counters the caller already read. An interface without a baseline was created after that (its
# counters started at 0 here); one whose counters went below the baseline was recreated: both count from 0.
vpn_traffic_since_base()
{
    local vpn_dir=$1 dev=$2 rx=$3 tx=$4 base brx btx
    base=$(awk -v d=$dev '$1 == d {print $2, $3}' $vpn_dir/traffic.base 2>/dev/null)
    brx=${base% *}
    btx=${base#* }
    if [ -z "$base" ] || [ "$rx" -lt "$brx" ] || [ "$tx" -lt "$btx" ]; then
        brx=0
        btx=0
    fi
    echo "$(($rx - $brx)) $(($tx - $btx))"
}

# True when the isolation and MSS rules of the router are all in place and no old MSS rule is left:
# vpn_forward_rules_ok <router>. Two iptables calls instead of the dozen the add functions need, so the
# watchdog only repairs when something is missing (it holds the lb lock that failovers wait for)
vpn_forward_rules_ok()
{
    local router=$1 filter mangle rule
    filter=$(ip netns exec $router iptables -S FORWARD 2>/dev/null) || return 1
    mangle=$(ip netns exec $router iptables -t mangle -S FORWARD 2>/dev/null) || return 1
    for rule in "-A FORWARD -i wg+ -o ipsec+ -j DROP" "-A FORWARD -i ipsec+ -o wg+ -j DROP"; do
        grep -qxF -- "$rule" <<<"$filter" || return 1
    done
    for rule in "-o ipsec+ -p tcp -m tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu" \
        "-i ipsec+ -p tcp -m tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1320" \
        "-o wg+ -p tcp -m tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu" \
        "-i wg+ -p tcp -m tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1350"; do
        grep -qxF -- "-A FORWARD $rule" <<<"$mangle" || return 1
    done
    # the --syn rules of older versions
    ! grep -q -- "--tcp-flags FIN,SYN,RST,ACK SYN -j TCPMSS" <<<"$mangle"
}

# True when the router carries another gateway directory than <vpn_dir>: vpn_router_has_other <router> <vpn_dir>.
# One gateway per VPC, but a replacement can be created before the old one is cleared on this node (the
# directory of the old one is removed last), and the router-wide wildcard rules belong to both.
vpn_router_has_other()
{
    local d
    for d in $router_dir/$1/vpn-*; do
        [ -d "$d" ] && [ "$d" != "$2" ] && return 0
    done
    return 1
}

vpn_dir_of()
{
    echo "$router_dir/router-$1/vpn-$2"
}

# Router id of the gateway on this node (the vpn directory lives under the router)
vpn_router_of()
{
    local dir
    dir=$(ls -d $router_dir/router-*/vpn-$1 2>/dev/null | head -1)
    [ -n "$dir" ] || return 1
    dir=${dir%/vpn-*}
    echo ${dir##*/router-}
}

# True when this node currently holds the gateway's floating IP (the VRRP master)
vpn_holds_vip()
{
    local router=$1 vpn_dir=$2 vip
    vip=$(cat $vpn_dir/vip 2>/dev/null)
    [ -n "$vip" ] || return 1
    ip netns exec $router ip -4 -o addr show 2>/dev/null | grep -qF " $vip/"
}

# pid file alive and the process is what we expect (pid files survive reboots, numbers get reused)
vpn_pid_alive()
{
    local pidfile=$1 name=$2 pid
    pid=$(cat $pidfile 2>/dev/null)
    [ -n "$pid" ] && [ -d /proc/$pid ] && grep -q "^$name" /proc/$pid/comm 2>/dev/null
}

vpn_charon_alive()
{
    vpn_pid_alive $1/run/charon.pid charon
}

# swanctl parses the command first and its options after it: vpn_swanctl <router> <vpn_dir> <command> [options]
vpn_swanctl()
{
    local router=$1 vpn_dir=$2
    shift 2
    ip netns exec $router swanctl "$@" --uri unix://$vpn_dir/run/charon.vici 2>/dev/null
}

# Start charon for a gateway inside the router netns with a private /run (the pid file path is compiled
# in, so every instance gets its own directory bind-mounted over it). Loads swanctl.conf once the vici
# socket answers. fd 9 (the lb lock) is closed so the daemon never inherits the lock.
# Optional step trace for the role-change scripts: set VPN_TRACE to a file to get timestamps per step
vpn_trace()
{
    [ -n "$VPN_TRACE" ] && echo "$(date +%T.%N | cut -c1-12) [$$]   $*" >>$VPN_TRACE
    return 0
}
vpn_start_charon()
{
    local router=$1 vpn_dir=$2 i
    [ -f $vpn_dir/swanctl.conf ] || return 0
    vpn_disabled $vpn_dir && return 0
    vpn_charon_alive $vpn_dir && return 0
    rm -f $vpn_dir/run/charon.pid $vpn_dir/run/charon.vici $vpn_dir/run/charon.ctl
    # The reported tunnel traffic restarts with charon on this node, like the establishment time. Every
    # path that brings charon up comes through here: the notify script, the watchdog (which can win the
    # race after a failover) and the config scripts
    vpn_traffic_baseline $router $vpn_dir
    # No SA survives a charon restart: the watch loop sees the tunnels come up again from here
    : >$vpn_dir/tunnels.up
    vpn_trace "charon: spawning"
    STRONGSWAN_CONF=$vpn_dir/strongswan.conf ip netns exec $router unshare -m sh -c "mount --bind $vpn_dir/run /run && exec $charon_bin" >/dev/null 2>&1 9>&- &
    for i in {1..20}; do
        [ -S $vpn_dir/run/charon.vici ] && break
        sleep 0.25
    done
    vpn_trace "charon: vici socket $([ -S $vpn_dir/run/charon.vici ] && echo up || echo MISSING)"
    [ -S $vpn_dir/run/charon.vici ] || return 1
    vpn_load_swanctl $router $vpn_dir
    vpn_trace "charon: swanctl loaded (rc=$?)"
}

vpn_load_swanctl()
{
    local router=$1 vpn_dir=$2
    [ -f $vpn_dir/swanctl.conf ] || return 0
    vpn_swanctl $router $vpn_dir --load-all --file $vpn_dir/swanctl.conf >/dev/null
}

vpn_stop_charon()
{
    local vpn_dir=$1 pid i
    pid=$(cat $vpn_dir/run/charon.pid 2>/dev/null)
    if [ -n "$pid" ] && vpn_charon_alive $vpn_dir; then
        kill -TERM $pid 2>/dev/null
        for i in {1..20}; do
            [ -d /proc/$pid ] || break
            sleep 0.25
        done
        [ -d /proc/$pid ] && kill -KILL $pid 2>/dev/null
    fi
    rm -f $vpn_dir/run/charon.pid $vpn_dir/run/charon.vici $vpn_dir/run/charon.ctl
}

vpn_frr_ns()
{
    echo "vpn-$1"
}

vpn_frr_alive()
{
    local ns=$(vpn_frr_ns $1)
    vpn_pid_alive /var/run/frr/$ns/zebra.pid zebra && vpn_pid_alive /var/run/frr/$ns/mgmtd.pid mgmtd && vpn_pid_alive /var/run/frr/$ns/staticd.pid staticd &&
        vpn_pid_alive /var/run/frr/$ns/bfdd.pid bfdd && vpn_pid_alive /var/run/frr/$ns/bgpd.pid bgpd
}

# Start zebra + mgmtd + staticd + bfdd + bgpd for a gateway in the router netns under their own pathspace,
# then load the integrated config. bfdd runs even without a BFD peer: bgpd registers its sessions with it.
# Load the integrated config. zebra must be up before the others or learned routes never reach the kernel;
# since FRR 9 static routes are configured through mgmtd, without it "ip route" is silently dropped.
# staticd owns the summary blackholes (distance 250): a kernel blackhole for the same prefix would win
# zebra's route selection (distance 0) and the BGP route for that prefix would never be installed.
vpn_start_frr()
{
    local router=$1 gw=$2 ns=$(vpn_frr_ns $2) i
    [ -f /etc/frr/$ns/frr.conf ] || return 0
    vpn_disabled $router_dir/$router/vpn-$gw && return 0
    vpn_frr_alive $gw && return 0
    vpn_trace "frr: stopping leftovers"
    vpn_stop_frr $gw
    # The daemons switch to the frr user and must be able to enter the directory: create it with an
    # explicit mode. keepalived runs its notify scripts with a restrictive umask, and a plain mkdir there
    # produced a 0600 directory in which zebra could not create its pid file and exited at once
    install -d -m 755 -o frr -g frr /var/run/frr/$ns
    vpn_trace "frr: launching zebra"
    if [ -n "$VPN_TRACE" ]; then
        # Everything the daemon prints before it detaches goes to the trace
        ip netns exec $router $frr_bin_dir/zebra -N $ns -d >>$VPN_TRACE 2>&1 9>&-
    else
        ip netns exec $router $frr_bin_dir/zebra -N $ns -d >/dev/null 2>&1 9>&-
    fi
    for i in {1..20}; do
        [ -S /var/run/frr/$ns/zserv.api ] && break
        sleep 0.25
    done
    vpn_trace "frr: zebra $([ -S /var/run/frr/$ns/zserv.api ] && echo up || echo NOT UP), launching mgmtd"
    [ -n "$VPN_TRACE" ] && [ ! -S /var/run/frr/$ns/zserv.api ] && vpn_trace "env: uid=$(id -u) umask=$(umask) netns=$(ip netns identify $$ 2>&1 | tr '
' ' ') rundir=$(ls -ld /var/run/frr/$ns 2>&1 | cut -c1-60) conf=$(ls /etc/frr/$ns/ 2>&1 | tr '
' ' ')"
    ip netns exec $router $frr_bin_dir/mgmtd -N $ns -d >/dev/null 2>&1 9>&-
    for i in {1..20}; do
        [ -S /var/run/frr/$ns/mgmtd.vty ] && break
        sleep 0.25
    done
    vpn_trace "frr: mgmtd $([ -S /var/run/frr/$ns/mgmtd.vty ] && echo up || echo NOT UP), launching staticd, bfdd and bgpd"
    ip netns exec $router $frr_bin_dir/staticd -N $ns -d >/dev/null 2>&1 9>&-
    ip netns exec $router $frr_bin_dir/bfdd -N $ns -d >/dev/null 2>&1 9>&-
    ip netns exec $router $frr_bin_dir/bgpd -N $ns -d >/dev/null 2>&1 9>&-
    for i in {1..20}; do
        [ -S /var/run/frr/$ns/bgpd.vty ] && [ -S /var/run/frr/$ns/staticd.vty ] && [ -S /var/run/frr/$ns/bfdd.vty ] && break
        sleep 0.25
    done
    vpn_trace "frr: bgpd/staticd $([ -S /var/run/frr/$ns/bgpd.vty ] && [ -S /var/run/frr/$ns/staticd.vty ] && echo up || echo NOT UP), loading config"
    timeout 30 vtysh -N $ns -b >/dev/null 2>&1
    vpn_trace "frr: config loaded (rc=$?)"
    # The static tunnel routes of an active_active gateway follow the SAs: frr.conf only had the ones up
    # when it was written, bring them in line now
    vpn_frr_statics $router $router_dir/$router/vpn-$gw
}

# The staticd command of a tunnel route: vpn_static_cmd <cidr> <if_id> <tag> <distance>. The default
# distance is left out, as FRR prints it (the running config and frr.conf must match line by line)
vpn_static_cmd()
{
    local cmd="ip route $1 ipsec-$2 tag $3"
    [ "$4" != "1" ] && cmd="$cmd $4"
    echo "$cmd"
}

# The tunnel routes that should exist on this node of an active_active gateway, sorted: one per remote
# network of every static tunnel whose SA is installed. static_tunnels (create_vpn_bgp_conf.sh) has one line
# per tunnel of this node: <name> <if_id> <tag> <distance> <cidr>...
vpn_static_wanted()
{
    local vpn_dir=$1 name if_id tag distance cidrs cidr up
    up=" $(tr '\n' ' ' <$vpn_dir/tunnels.up 2>/dev/null) "
    [ -f $vpn_dir/static_tunnels ] || return 0
    while read name if_id tag distance cidrs; do
        [ -n "$cidrs" ] || continue
        case "$up" in
        *" $name "*) ;;
        *) continue ;;
        esac
        for cidr in $cidrs; do
            vpn_static_cmd $cidr $if_id $tag $distance
        done
    done <$vpn_dir/static_tunnels | sort
}

# Keep the staticd routes of the static tunnels of an active_active gateway in line with their SAs:
# vpn_frr_statics <router> <vpn_dir>. A tunnel that is up routes its remote networks through its interface
# (the best tunnel with distance 1, a lesser one above the iBGP distance of 200, so that the other node's
# better tunnel wins while it lasts); redistributed into iBGP with the local preference of its rank, the
# other node learns them. What exists is read from the running config: a static route through an ipsec-
# interface with a tag is always one of these (the summary and last-resort routes go to Null0).
vpn_frr_statics()
{
    local router=$1 vpn_dir=$2 gw=${2##*/vpn-} ns want have line cmds=()
    vpn_is_aa $vpn_dir || return 0
    vpn_disabled $vpn_dir && return 0
    vpn_frr_alive $gw || return 0
    ns=$(vpn_frr_ns $gw)
    want=$(vpn_static_wanted $vpn_dir)
    have=$(timeout 10 vtysh -N $ns -c "show running-config" 2>/dev/null) || return 1
    have=$(grep -E '^ip route [0-9./]+ ipsec-[0-9]+ tag [0-9]+( [0-9]+)?$' <<<"$have" | sort)
    [ "$want" = "$have" ] && return 0
    while read line; do
        [ -n "$line" ] && cmds+=(-c "$line")
    done < <(comm -13 <(echo "$have") <(echo "$want"))
    while read line; do
        [ -n "$line" ] && cmds+=(-c "no $line")
    done < <(comm -23 <(echo "$have") <(echo "$want"))
    if [ ${#cmds[@]} -gt 0 ]; then
        timeout 10 vtysh -N $ns -c "configure terminal" "${cmds[@]}" >/dev/null 2>&1 || {
            log_debug "$(basename $0)" "vpn: failed to update the tunnel routes of gateway $gw in FRR"
            return 1
        }
    fi
}

# Apply a changed frr.conf: hot reload when the daemons run, otherwise start them
vpn_reload_frr()
{
    local router=$1 gw=$2 ns=$(vpn_frr_ns $2)
    if vpn_frr_alive $gw; then
        # --confdir keeps frr-reload from ending with "write": it overwrites the file we just generated
        # whenever the file name differs from <confdir>/frr.conf
        timeout 60 $frr_bin_dir/frr-reload.py -N $ns --confdir /etc/frr/$ns --reload /etc/frr/$ns/frr.conf >/dev/null 2>&1 || {
            vpn_stop_frr $gw
            vpn_start_frr $router $gw
        }
    else
        vpn_start_frr $router $gw
    fi
}

vpn_stop_frr()
{
    local ns=$(vpn_frr_ns $1) d pid i
    for d in bgpd bfdd staticd mgmtd zebra; do
        pid=$(cat /var/run/frr/$ns/$d.pid 2>/dev/null)
        [ -n "$pid" ] && [ -d /proc/$pid ] && kill -TERM $pid 2>/dev/null
    done
    for i in {1..20}; do
        vpn_pid_alive /var/run/frr/$ns/bgpd.pid bgpd || vpn_pid_alive /var/run/frr/$ns/bfdd.pid bfdd || vpn_pid_alive /var/run/frr/$ns/staticd.pid staticd ||
            vpn_pid_alive /var/run/frr/$ns/mgmtd.pid mgmtd || vpn_pid_alive /var/run/frr/$ns/zebra.pid zebra || break
        sleep 0.25
    done
    rm -rf /var/run/frr/$ns
}

# Install the tunnel routes of a VRRP node according to its role. tunnel_routes has three columns:
# <cidr> <static|bgp|client> <target on the master>. master: static/client go to the tunnel interface
# (a static connection with two tunnels lists both, primary first: the first one whose SA is installed
# according to tunnels.up wins, the primary when none is),
# bgp summaries become a blackhole (learned prefixes win; without the blackhole a withdrawn prefix would
# leak to the internet through the default route). While FRR runs, its staticd owns that blackhole with
# distance 250 and the kernel one (proto boot) is removed: zebra would otherwise prefer the kernel route
# (distance 0) over a BGP route for the very same prefix and never install the latter. backup: everything
# via the peer's VRRP address. fault: blackhole (the peer's state is unknown, pointing at it could loop).
# routes.installed remembers what was installed so removed prefixes are withdrawn.
vpn_apply_routes()
{
    local router=$1 vpn_dir=$2 role=$3 peer_ip vrrp_vlan cidr type target installed new gw_id aa=false dev args
    gw_id=${vpn_dir##*/vpn-}
    peer_ip=$(cat $vpn_dir/peer_ip 2>/dev/null)
    vrrp_vlan=$(cat $vpn_dir/vrrp_vlan 2>/dev/null)
    installed=$(cat $vpn_dir/routes.installed 2>/dev/null)
    vpn_is_aa $vpn_dir && aa=true
    # A paused gateway drops everything on both nodes, whatever the VRRP role
    vpn_disabled $vpn_dir && role=disabled
    new=""
    if [ -f $vpn_dir/tunnel_routes ]; then
        while read cidr type target; do
            [ -n "$cidr" ] || continue
            # active_active: FRR owns the tunnel prefixes on both nodes (staticd for the static connections,
            # BGP for the others, iBGP between the nodes); a kernel route would win zebra's selection
            if $aa && [ "$role" != "disabled" ] && [ "$type" != "client" ]; then
                ip netns exec $router ip route del $cidr proto boot >/dev/null 2>&1
                continue
            fi
            case $role in
            master)
                if [ "$target" = "blackhole" ]; then
                    # Once zebra knows staticd's blackhole for the prefix (an "S" entry in its RIB, installed
                    # or not), drop ours: zebra only installs its own route when no kernel route competes
                    if [ "$type" = "bgp" ] && vpn_frr_alive $gw_id && timeout 5 vtysh -N $(vpn_frr_ns $gw_id) -c "show ip route $cidr" 2>/dev/null | grep -q 'Known via "static"'; then
                        ip netns exec $router ip route del blackhole $cidr proto boot 2>/dev/null
                    else
                        ip netns exec $router ip route replace blackhole $cidr
                    fi
                elif target=$(vpn_pick_target $vpn_dir $target) && [ "${target//+/}" != "$target" ]; then
                    # ecmp: every tunnel that is up shares the prefix, flows hashed on addresses and ports
                    args=""
                    for dev in ${target//+/ }; do
                        ip netns exec $router ip link show $dev >/dev/null 2>&1 && args="$args nexthop dev $dev"
                    done
                    if [ -n "$args" ]; then
                        ip netns exec $router ip route replace $cidr $args
                    else
                        ip netns exec $router ip route replace blackhole $cidr
                    fi
                elif ip netns exec $router ip link show $target >/dev/null 2>&1; then
                    ip netns exec $router ip route replace $cidr dev $target
                else
                    # interface not built yet: drop rather than leak through the default route
                    ip netns exec $router ip route replace blackhole $cidr
                fi
                ;;
            backup)
                if [ -n "$peer_ip" ] && ip netns exec $router ip link show ns-$vrrp_vlan >/dev/null 2>&1; then
                    ip netns exec $router ip route replace $cidr via $peer_ip dev ns-$vrrp_vlan onlink
                else
                    ip netns exec $router ip route replace blackhole $cidr
                fi
                ;;
            *)
                ip netns exec $router ip route replace blackhole $cidr
                ;;
            esac
            new="$new $cidr"
        done <$vpn_dir/tunnel_routes
    fi
    for cidr in $installed; do
        case " $new " in
        *" $cidr "*) ;;
        *) ip netns exec $router ip route del $cidr proto boot >/dev/null 2>&1 ;;
        esac
    done
    echo $new >$vpn_dir/routes.installed
}

# vpn_pick_target <vpn_dir> <dev[,dev...]|dev[+dev...]>: with commas the first interface whose tunnel is
# up (the first one otherwise); with "+" (ecmp) every interface whose tunnel is up, joined by "+" (all of
# them otherwise)
vpn_pick_target()
{
    local vpn_dir=$1 list=$2 dev name up=""
    case $list in
    *+*)
        for dev in ${list//+/ }; do
            name=$(awk -v i=${dev#ipsec-} '$2 == i {print $1}' $vpn_dir/conn_ifids 2>/dev/null)
            [ -n "$name" ] && grep -qxF "$name" $vpn_dir/tunnels.up 2>/dev/null && up="$up+$dev"
        done
        if [ -n "$up" ]; then echo ${up#+}; else echo $list; fi
        return 0
        ;;
    *,*) ;;
    *) echo $list; return 0 ;;
    esac
    for dev in ${list//,/ }; do
        name=$(awk -v i=${dev#ipsec-} '$2 == i {print $1}' $vpn_dir/conn_ifids 2>/dev/null)
        [ -n "$name" ] && grep -qxF "$name" $vpn_dir/tunnels.up 2>/dev/null && { echo $dev; return 0; }
    done
    echo ${list%%,*}
}

# vpn_tunnels_up <router> <vpn_dir>: names of the tunnels with an established IKE SA and an installed
# CHILD_SA, one per line, sorted; also written to tunnels.up for vpn_apply_routes. Fails when charon does not answer.
vpn_tunnels_up()
{
    local router=$1 vpn_dir=$2 sas up
    vpn_charon_alive $vpn_dir || return 1
    sas=$(timeout 5 ip netns exec $router swanctl --list-sas --uri unix://$vpn_dir/run/charon.vici 2>/dev/null) || return 1
    up=$(awk '
        /^[A-Za-z0-9_-]+: #[0-9]+, / { cur = $1; sub(":$", "", cur); est = ($3 == "ESTABLISHED,") }
        /^  [A-Za-z0-9_-]+: #[0-9]+, reqid [0-9]+, INSTALLED,/ { if (est) up[cur] = 1 }
        END { for (n in up) print n }' <<<"$sas" | sort)
    [ "$up" = "$(cat $vpn_dir/tunnels.up 2>/dev/null)" ] || echo "$up" >$vpn_dir/tunnels.up
    echo "$up"
}

# The last thing charon logged about each tunnel, read from the tail of charon.log: vpn_tunnel_events <vpn_dir>
# -> "<tunnel name><TAB><failure message>" per tunnel seen there, the message empty when the last event was an
# established SA, and a line "*" when charon started within the tail (what was known before is stale).
# With ike_name = yes every line of an IKE_SA carries "<name|unique id>" (failures before a peer config was
# picked carry no name and cannot be told apart). Only known failure messages count, with the per-SA
# numbers ([3], {5}) and retry counters dropped: every retry of an unreachable peer logs the same text, and the status report
# is only re-sent when it changes. Quotes and non-printable characters are replaced (the text goes into JSON).
vpn_tunnel_events()
{
    local log=$1/charon.log
    [ -f $log ] || return 0
    tail -c 524288 $log 2>/dev/null | LC_ALL=C tr -c '\n -~' ' ' | awk '
        /Starting IKE charon daemon/ { delete ev; delete seen; restart = 1; next }
        {
            tag = ""
            for (i = 1; i <= NF && i <= 8; i++) if ($i ~ /^<[^|<>]+\|[0-9]+>$/) { tag = $i; break }
            if (tag == "") next
            name = substr(tag, 2, index(tag, "|") - 2)
            msg = substr($0, index($0, tag " ") + length(tag) + 1)
            if (msg ~ /^IKE_SA [^ ]+ established between / || msg ~ /^CHILD_SA [^ ]+ established /) {
                ev[name] = ""; seen[name] = 1
                next
            }
            if (msg ~ /giving up after [0-9]+ retransmits|peer not responding|received [A-Z0-9_]+ notify error|notify, no CHILD_SA built|failed to establish CHILD_SA|MAC mismatched|no shared key found|authentication of .* failed|no (matching|acceptable) proposal|unable to (resolve|install|allocate|initiate)|constraint check failed|initiate failed/) {
                gsub(/\[[0-9]+\]|\{[0-9]+\}/, "", msg)
                # counters of the retries: "trying again (3/0)" (keyingtries = 0 retries for ever), "retransmit 2 of
                # request message ID 0, seq 1"
                gsub(/ *\([0-9]+\/[0-9]+\)/, "", msg)
                gsub(/retransmit [0-9]+ of/, "retransmit of", msg)
                gsub(/message ID [0-9]+, seq [0-9]+/, "message", msg)
                gsub(/["\\]/, "'"'"'", msg)
                sub(/ +$/, "", msg)
                ev[name] = substr(msg, 1, 200); seen[name] = 1
            }
        }
        END {
            if (restart) print "*"
            for (n in seen) print n "\t" ev[n]
        }'
}

# Ask cloudlet for an immediate VPN status report instead of the next heartbeat (1-20 s): it watches this
# file and runs report_vpn_status.sh when it changes (plan §8.2, F3). A cloudlet that does not
# know about it simply ignores the file.
vpn_trigger_report()
{
    mkdir -p /run/cloudland 2>/dev/null && touch /run/cloudland/vpn-report.trigger 2>/dev/null
    return 0
}

# The per-gateway watch loop on the master (vpn_watch.sh): alive when the pid runs that script for this gateway
vpn_watch_alive()
{
    local vpn_dir=$1 gw=${1##*/vpn-} pid
    pid=$(cat $vpn_dir/watch.pid 2>/dev/null)
    [ -n "$pid" ] && [ -r /proc/$pid/cmdline ] && tr '\0' ' ' </proc/$pid/cmdline | grep -q "vpn_watch.sh $gw\b"
}

vpn_watch_start()
{
    local vpn_dir=$1 gw=${1##*/vpn-}
    vpn_watch_alive $vpn_dir && return 0
    vpn_disabled $vpn_dir && return 0
    setsid $vpn_scripts/vpn_watch.sh $gw >/dev/null 2>&1 9>&- </dev/null &
}

vpn_watch_stop()
{
    local vpn_dir=$1
    vpn_watch_alive $vpn_dir && kill $(cat $vpn_dir/watch.pid) 2>/dev/null
    rm -f $vpn_dir/watch.pid
}

# Make sure the WireGuard interface exists with the current key, port and address (no route: that is
# the notify script's job, see the plan §6.3). Peers come from wg.conf via syncconf.
vpn_apply_wg()
{
    local router=$1 vpn_dir=$2 gw=$3 dev=wg-$3 address port
    [ -f $vpn_dir/wg.conf ] || return 0
    port=$(awk '$1 == "ListenPort" {print $3}' $vpn_dir/wg.conf)
    address=$(cat $vpn_dir/wg_address 2>/dev/null)
    ip netns exec $router ip link show $dev >/dev/null 2>&1 || ip netns exec $router ip link add $dev type wireguard
    ip netns exec $router wg syncconf $dev $vpn_dir/wg.conf
    [ -n "$address" ] && ip netns exec $router ip addr replace $address dev $dev
    if vpn_disabled $vpn_dir; then
        ip netns exec $router ip link set $dev mtu 1390 down
    else
        ip netns exec $router ip link set $dev mtu 1390 up
    fi
}

# ---- Other nodes of the VPC: routes through the gateway nodes (set_vpn_route.sh, vpn_nexthop_watch.sh) ----
# nexthops: <cidr> <ecmp 0|1> <vrrp ip>[,<vrrp ip>...] in order of preference; nexthop_hosts: <vrrp ip> <host
# ip> (the gateway node's own address, which the watcher probes). A node the watcher found dead is marked
# in $vpn_nexthop_state/<host ip> ("down"); anything else counts as reachable.
vpn_nexthop_state=/run/cloudland/vpn-nexthop
vpn_nexthop_lock=$run_dir/vpn_nexthop.lock

vpn_hop_reachable()
{
    local vpn_dir=$1 hop=$2 host
    host=$(awk -v h=$hop '$1 == h {print $2}' $vpn_dir/nexthop_hosts 2>/dev/null)
    [ -n "$host" ] || return 0
    [ "$(cat $vpn_nexthop_state/$host 2>/dev/null)" != "down" ]
}

# Install the routes of the other nodes: vpn_apply_nexthops <router> <vpn_dir>. The first reachable gateway
# node of every prefix, or with ecmp all the reachable ones; the preferred one when none is reachable (the
# probe may be what failed, and there is nothing better). routes.current is what is installed. Callers hold
# $vpn_nexthop_lock.
vpn_apply_nexthops()
{
    local router=$1 vpn_dir=$2 vrrp_vlan cidr ecmp hops hop chosen args installed new=""
    vrrp_vlan=$(cat $vpn_dir/vrrp_vlan 2>/dev/null)
    installed=$(cat $vpn_dir/routes.current 2>/dev/null)
    if [ -n "$vrrp_vlan" ] && [ -f $vpn_dir/nexthops ] && ip netns exec $router ip link show ns-$vrrp_vlan >/dev/null 2>&1; then
        while read cidr ecmp hops; do
            [ -n "$hops" ] || continue
            chosen=""
            for hop in ${hops//,/ }; do
                vpn_hop_reachable $vpn_dir $hop || continue
                chosen="$chosen $hop"
                [ "$ecmp" = "1" ] || break
            done
            [ -n "$chosen" ] || chosen=${hops%%,*}
            set -- $chosen
            if [ $# -gt 1 ]; then
                args=""
                for hop in $chosen; do
                    args="$args nexthop via $hop dev ns-$vrrp_vlan onlink"
                done
                ip netns exec $router ip route replace $cidr $args
            else
                ip netns exec $router ip route replace $cidr via $1 dev ns-$vrrp_vlan onlink
            fi
            new="$new $cidr"
        done <$vpn_dir/nexthops
    fi
    for cidr in $installed; do
        case " $new " in
        *" $cidr "*) ;;
        *) ip netns exec $router ip route del $cidr >/dev/null 2>&1 ;;
        esac
    done
    echo $new >$vpn_dir/routes.current
}

vpn_nexthop_watch_alive()
{
    local pid
    pid=$(cat $vpn_nexthop_state/watch.pid 2>/dev/null)
    [ -n "$pid" ] && [ -r /proc/$pid/cmdline ] && tr '\0' ' ' </proc/$pid/cmdline | grep -q "vpn_nexthop_watch.sh"
}

vpn_nexthop_watch_start()
{
    vpn_nexthop_watch_alive && return 0
    mkdir -p $vpn_nexthop_state
    setsid $vpn_scripts/vpn_nexthop_watch.sh >/dev/null 2>&1 9>&- </dev/null &
}
