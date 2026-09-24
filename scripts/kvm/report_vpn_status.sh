#!/bin/bash

# Report VPN gateway state to clapi, called by report_rc.sh on every heartbeat. Only the node holding the
# floating IP reports (the master), and each report is sent when its content changed or the last one is
# older than $report_interval, so a lost callback is eventually corrected without flooding clapi.
# Four callbacks: the master identity (drives the routes of the other nodes), the IKE SA state of every
# connection, the WireGuard peer counters and the BGP neighbor snapshot. Payloads are base64 JSON.
# stdout is the callback protocol: print nothing except |:-COMMAND-:| lines.

cd `dirname $0`
source ../cloudrc
source ./vpn_lib.sh

report_interval=300
# Master identity is re-sent every 20 s: clapi accepts a new master only after the previous one has been
# silent for 30 s, so this interval bounds the failover delay of the other nodes' routes
master_interval=20
now=$(date +%s)
# NODE_ID identifies this node in every callback; it comes from the cloudlet environment, with the
# service configuration as fallback when the caller dropped it
[ -n "$NODE_ID" ] || NODE_ID=$(sed -n 's/^NODE_ID=//p' /etc/sysconfig/cloudlet 2>/dev/null | tr -d '"')
[ -n "$NODE_ID" ] || exit 0

# Emit a callback when the payload differs from the cached one or the cache is older than $3 seconds
report_if_changed()
{
    local cache=$1 payload=$2 interval=$3 line=$4
    if [ -f $cache ] && [ "$(cat $cache)" = "$payload" ] && [ $(($now - $(stat -c %Y $cache))) -lt $interval ]; then
        return
    fi
    echo "$line"
    echo "$payload" >$cache
}

for vpn_dir in $router_dir/router-*/vpn-*; do
    [ -d "$vpn_dir" ] || continue
    [ -f $vpn_dir/vip ] || continue
    router=$(basename $(dirname $vpn_dir))
    gw=${vpn_dir##*/vpn-}
    [ -f /var/run/netns/$router ] || continue
    if ! vpn_holds_vip $router $vpn_dir; then
        # Report immediately after taking over
        rm -f $vpn_dir/master.reported $vpn_dir/master.since $vpn_dir/status.reported $vpn_dir/wg.reported $vpn_dir/bgp.reported
        continue
    fi
    # clapi rejects a master claim while the previous master's report is younger than its flap window, and
    # the node never learns about it: re-send on every heartbeat during the first minute after taking over
    # so the claim gets through as soon as the window has elapsed
    [ -f $vpn_dir/master.since ] || echo $now >$vpn_dir/master.since
    interval=$master_interval
    status_interval=$report_interval
    # The status reports of that minute are rejected too until the master claim went through (clapi only
    # takes them from the recorded master), and their cache would otherwise hold them back for 5 minutes
    [ $(($now - $(cat $vpn_dir/master.since))) -lt 60 ] && interval=0 && status_interval=0
    report_if_changed $vpn_dir/master.reported "$NODE_ID" $interval "|:-COMMAND-:| vpn_master.sh '$gw' '$NODE_ID'"
    if vpn_disabled $vpn_dir; then
        # Paused: no tunnel state to report (clapi shows the connections as disabled). Drop the caches so
        # that the first heartbeat after re-enabling reports at once
        rm -f $vpn_dir/status.reported $vpn_dir/wg.reported $vpn_dir/bgp.reported
        continue
    fi

    # IPsec: parse swanctl --list-sas (name, state, age, per-child byte counters)
    if [ -f $vpn_dir/conn_names ] && vpn_charon_alive $vpn_dir; then
        sas=$(timeout 10 ip netns exec $router swanctl --list-sas --uri unix://$vpn_dir/run/charon.vici 2>/dev/null)
        payload=$(awk -v now=$now -v names="$(tr '\n' ' ' <$vpn_dir/conn_names)" '
            BEGIN { n = split(names, list, " ") }
            /^[A-Za-z0-9_-]+: #[0-9]+, / {
                name = $1; sub(":$", "", name)
                st = $3; sub(",$", "", st)
                if (st == "ESTABLISHED") { up[name] = 1 }
                cur = name
            }
            /^ +established [0-9]+s ago/ { for (i = 1; i <= NF; i++) if ($i == "established") { est[cur] = now - substr($(i+1), 1, length($(i+1)) - 1) } }
            /^ +in +c[0-9a-f]+, +[0-9]+ bytes/ { bin[cur] += $3 }
            /^ +out +c[0-9a-f]+, +[0-9]+ bytes/ { bout[cur] += $3 }
            END {
                printf "["
                for (i = 1; i <= n; i++) {
                    name = list[i]; if (name == "") continue
                    if (i > 1) printf ","
                    printf "{\"name\":\"%s\",\"state\":\"%s\",\"established_at\":%d,\"bytes_in\":%d,\"bytes_out\":%d,\"error\":\"\"}", name, (name in up) ? "up" : "down", est[name] + 0, bin[name] + 0, bout[name] + 0
                }
                printf "]"
            }' <<<"$sas")
        encoded=$(echo -n "$payload" | base64 -w0)
        report_if_changed $vpn_dir/status.reported "$encoded" $status_interval "|:-COMMAND-:| vpn_conn_status.sh '$gw' '$NODE_ID' '$encoded'"
    fi

    # WireGuard: wg show dump gives one peer per line: pubkey psk endpoint allowed-ips handshake rx tx keepalive
    if [ -f $vpn_dir/wg.conf ] && ip netns exec $router ip link show wg-$gw >/dev/null 2>&1; then
        payload=$(timeout 10 ip netns exec $router wg show wg-$gw dump 2>/dev/null | awk 'NR > 1 {
                if (n++) printf ","
                printf "{\"public_key\":\"%s\",\"last_handshake\":%d,\"bytes_in\":%d,\"bytes_out\":%d}", $1, $5, $6, $7
            } BEGIN { printf "[" } END { printf "]" }')
        encoded=$(echo -n "$payload" | base64 -w0)
        # Counters move on every heartbeat while traffic flows; the handshake time is what matters
        report_if_changed $vpn_dir/wg.reported "$encoded" $interval "|:-COMMAND-:| vpn_client_status.sh '$gw' '$NODE_ID' '$encoded'"
    fi

    # BGP: neighbor state, accepted / filtered / advertised prefixes (limited, display only)
    if [ -s $vpn_dir/bgp_peers ] && vpn_frr_alive $gw; then
        ns=$(vpn_frr_ns $gw)
        payload="["
        first=true
        while read name peer; do
            [ -n "$peer" ] || continue
            summary=$(timeout 10 vtysh -N $ns -c "show bgp ipv4 unicast summary json" 2>/dev/null)
            accepted=$(timeout 10 vtysh -N $ns -c "show bgp ipv4 unicast neighbors $peer routes json" 2>/dev/null)
            rejected=$(timeout 10 vtysh -N $ns -c "show bgp ipv4 unicast neighbors $peer filtered-routes json" 2>/dev/null)
            advertised=$(timeout 10 vtysh -N $ns -c "show bgp ipv4 unicast neighbors $peer advertised-routes json" 2>/dev/null)
            item=$(jq -n -c --arg name "$name" --arg peer "$peer" \
                --argjson summary "${summary:-{\}}" --argjson accepted "${accepted:-{\}}" --argjson rejected "${rejected:-{\}}" --argjson advertised "${advertised:-{\}}" '
                ($summary.peers[$peer] // {}) as $p
                | ($accepted.routes // {} | keys) as $acc
                | ($rejected.routes // {} | keys) as $rej
                | ($advertised.advertisedRoutes // {} | keys) as $adv
                | {name: $name, state: ($p.state // "Idle"), uptime: ($p.peerUptime // ""),
                   prefixes_received: ($p.pfxRcd // 0), prefixes_sent: ($p.pfxSnt // 0),
                   accepted: $acc[:200], rejected: $rej[:200], advertised: $adv[:200],
                   truncated: (($acc | length) > 200 or ($rej | length) > 200 or ($adv | length) > 200)}' 2>/dev/null)
            [ -n "$item" ] || continue
            $first || payload="$payload,"
            first=false
            payload="$payload$item"
        done <$vpn_dir/bgp_peers
        payload="$payload]"
        encoded=$(echo -n "$payload" | base64 -w0)
        report_if_changed $vpn_dir/bgp.reported "$encoded" $status_interval "|:-COMMAND-:| vpn_bgp_status.sh '$gw' '$NODE_ID' '$encoded'"
    fi
done
exit 0
