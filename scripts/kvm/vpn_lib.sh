#!/bin/bash
# Shared helpers for the VPN gateway scripts. Source after cloudrc (needs router_dir, lb_lock_file, NODE_ID).
#
# Layout on a node, per gateway (see docs/architecture/plan/vpn-gateway-plan.md §4.1):
#   $router_dir/router-<N>/vrrp-<vrrp id>/   keepalived (same layout as a load balancer, so check_lb_process.sh
#                                             restarts it)
#   $router_dir/router-<N>/vpn-<gw id>/      strongswan.conf, swanctl.conf, run/ (charon pid + vici socket,
#                                             bind-mounted over /run for that charon), wg.conf, tunnel_routes,
#                                             state files (vip, peer_ip, vrrp_id, vrrp_vlan, routes.*, nonat.current)
#   /etc/frr/vpn-<gw id>/                    FRR pathspace config (frr.conf, vtysh.conf); daemons run with -N
#   /var/run/frr/vpn-<gw id>/                FRR pid files and sockets
#
# Routes on the VRRP pair are owned by vpn_notify.sh (role dependent): nothing else installs them.

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

# Bring the installed CHILD_SAs of every connection back to the configured traffic selectors:
# vpn_ipsec_reconcile <router> <vpn_dir>. Both sides of a site connection are reconfigured at different
# moments, and close_action = start makes each side re-create its children with whatever configuration
# it has at that instant, so after a change of route mode or networks a tunnel can be left with the old
# selectors on both ends (the BGP link address is then outside every SA). close_action is therefore none
# and this also re-initiates a tunnel the peer closed (initiator side only). Called from the watchdog on
# the floating IP holder; a connection is re-initiated at most once a minute
vpn_ipsec_reconcile()
{
    local router=$1 vpn_dir=$2 conf=$vpn_dir/swanctl.conf name want have stamp child now
    [ -f $vpn_dir/conn_names ] || return 0
    vpn_charon_alive $vpn_dir || return 0
    now=$(date +%s)
    for name in $(cat $vpn_dir/conn_names); do
        want=$(vpn_conn_pairs $conf $name)
        have=$(vpn_swanctl $router $vpn_dir --list-sas --ike $name 2>/dev/null | awk '/^    local  [0-9]/ {l = $2} /^    remote [0-9]/ {print l "|" $2}' | sort -u)
        [ "$have" = "$want" ] && continue
        # nothing installed and we are not the initiator: only the peer can bring it up
        if [ -z "$have" ] && ! awk -v n="    $name {" '$0 == n {f = 1} f && /start_action = start/ {found = 1} f && /^    }/ {exit} END {exit !found}' $conf; then
            continue
        fi
        stamp=$vpn_dir/reconcile-$name
        if [ -f $stamp ] && [ $(($now - $(stat -c %Y $stamp))) -lt 60 ]; then
            continue
        fi
        touch $stamp
        log_debug "$(basename $0)" "vpn: connection $name has selectors [$(echo $have)] but is configured for [$(echo $want)], re-initiating"
        [ -n "$have" ] && vpn_swanctl $router $vpn_dir --terminate --ike $name --force --timeout 10 >/dev/null 2>&1
        if awk -v n="    $name {" '$0 == n {f = 1} f && /start_action = start/ {found = 1} f && /^    }/ {exit} END {exit !found}' $conf; then
            for child in $(vpn_conn_children $conf $name); do
                vpn_swanctl $router $vpn_dir --initiate --ike $name --child $child --timeout 20 >/dev/null 2>&1
            done
        fi
    done
}

# A disabled (paused) gateway has a "disabled" file in its directory, written by create_vpn_gateway.sh on
# both VRRP nodes. Everything that starts a tunnel process honours it: no charon, no FRR, the WireGuard
# interface down and every tunnel route blackholed. keepalived and the floating IP keep running.
vpn_disabled()
{
    [ -f $1/disabled ]
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
    vpn_pid_alive /var/run/frr/$ns/zebra.pid zebra && vpn_pid_alive /var/run/frr/$ns/mgmtd.pid mgmtd && vpn_pid_alive /var/run/frr/$ns/staticd.pid staticd && vpn_pid_alive /var/run/frr/$ns/bgpd.pid bgpd
}

# Start zebra + mgmtd + staticd + bgpd for a gateway in the router netns under their own pathspace, then
# load the integrated config. zebra must be up before the others or learned routes never reach the kernel;
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
    vpn_trace "frr: mgmtd $([ -S /var/run/frr/$ns/mgmtd.vty ] && echo up || echo NOT UP), launching staticd and bgpd"
    ip netns exec $router $frr_bin_dir/staticd -N $ns -d >/dev/null 2>&1 9>&-
    ip netns exec $router $frr_bin_dir/bgpd -N $ns -d >/dev/null 2>&1 9>&-
    for i in {1..20}; do
        [ -S /var/run/frr/$ns/bgpd.vty ] && [ -S /var/run/frr/$ns/staticd.vty ] && break
        sleep 0.25
    done
    vpn_trace "frr: bgpd/staticd $([ -S /var/run/frr/$ns/bgpd.vty ] && [ -S /var/run/frr/$ns/staticd.vty ] && echo up || echo NOT UP), loading config"
    timeout 30 vtysh -N $ns -b >/dev/null 2>&1
    vpn_trace "frr: config loaded (rc=$?)"
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
    for d in bgpd staticd mgmtd zebra; do
        pid=$(cat /var/run/frr/$ns/$d.pid 2>/dev/null)
        [ -n "$pid" ] && [ -d /proc/$pid ] && kill -TERM $pid 2>/dev/null
    done
    for i in {1..20}; do
        vpn_pid_alive /var/run/frr/$ns/bgpd.pid bgpd || vpn_pid_alive /var/run/frr/$ns/staticd.pid staticd || vpn_pid_alive /var/run/frr/$ns/mgmtd.pid mgmtd || vpn_pid_alive /var/run/frr/$ns/zebra.pid zebra || break
        sleep 0.25
    done
    rm -rf /var/run/frr/$ns
}

# Install the tunnel routes of a VRRP node according to its role. tunnel_routes has three columns:
# <cidr> <static|bgp|client> <target on the master>. master: static/client go to the tunnel interface,
# bgp summaries become a blackhole (learned prefixes win; without the blackhole a withdrawn prefix would
# leak to the internet through the default route). While FRR runs, its staticd owns that blackhole with
# distance 250 and the kernel one (proto boot) is removed: zebra would otherwise prefer the kernel route
# (distance 0) over a BGP route for the very same prefix and never install the latter. backup: everything
# via the peer's VRRP address. fault: blackhole (the peer's state is unknown, pointing at it could loop).
# routes.installed remembers what was installed so removed prefixes are withdrawn.
vpn_apply_routes()
{
    local router=$1 vpn_dir=$2 role=$3 peer_ip vrrp_vlan cidr type target installed new gw_id
    gw_id=${vpn_dir##*/vpn-}
    peer_ip=$(cat $vpn_dir/peer_ip 2>/dev/null)
    vrrp_vlan=$(cat $vpn_dir/vrrp_vlan 2>/dev/null)
    installed=$(cat $vpn_dir/routes.installed 2>/dev/null)
    # A paused gateway drops everything on both nodes, whatever the VRRP role
    vpn_disabled $vpn_dir && role=disabled
    new=""
    if [ -f $vpn_dir/tunnel_routes ]; then
        while read cidr type target; do
            [ -n "$cidr" ] || continue
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

# Make sure the WireGuard interface exists with the current key, port and address (no route: that is
# the notify script's job, see the plan §4.4). Peers come from wg.conf via syncconf.
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
