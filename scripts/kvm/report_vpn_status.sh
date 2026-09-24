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
# Traffic history: every master writes the raw tunnel counters of its gateways as node_exporter textfile
# metrics, Prometheus scrapes them and clapi queries rates from there (GET /vpn_gateways/:id/traffic).
# Raw counters on purpose: rate() handles resets and a new master simply continues the series.
metrics_dir=/var/lib/node_exporter
conn_metrics=""
client_metrics=""
# Master identity is re-sent every 20 s: clapi accepts a new master only after the previous one has been
# silent for 30 s, so this interval bounds the failover delay of the other nodes' routes
master_interval=20
now=$(date +%s)
# NODE_ID identifies this node in every callback; it comes from the cloudlet environment, with the
# service configuration as fallback when the caller dropped it
[ -n "$NODE_ID" ] || NODE_ID=$(sed -n 's/^NODE_ID=//p' /etc/sysconfig/cloudlet 2>/dev/null | tr -d '"')
[ -n "$NODE_ID" ] || exit 0

# Byte counters move on every heartbeat while traffic flows: they ride along with a state change, or with a
# refresh at most once a minute (the history comes from Prometheus, the counters only feed the tables)
traffic_interval=60

# Like report_if_changed below, but only <key> (the payload with its byte counters zeroed) decides whether
# something changed; the full line goes out on a change or when the last one is older than <interval>
report_if_key_changed()
{
    local cache=$1 key=$2 interval=$3 line=$4
    if [ -f $cache ] && [ "$(cat $cache)" = "$key" ] && [ $(($now - $(stat -c %Y $cache))) -lt $interval ]; then
        return
    fi
    echo "$line"
    echo "$key" >$cache
}
zero_bytes() { sed -E 's/"bytes_(in|out)":[0-9]+/"bytes_\1":0/g'; }

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
    traffic_every=$traffic_interval
    [ $status_interval -eq 0 ] && traffic_every=0

    # Every counter is read once per heartbeat and used for both the metric lines and the status reports.
    # A counter that cannot be read is left out rather than exported as 0.
    traffic=""
    for name in $(cat $vpn_dir/conn_names 2>/dev/null); do
        if_id=$(vpn_conn_ifid $vpn_dir $name)
        [ -n "$if_id" ] || continue
        cur=$(vpn_dev_bytes $router ipsec-$if_id) || continue
        read rx tx <<<"$cur"
        conn_metrics="$conn_metrics
cloudland_vpn_connection_bytes_total{gateway_id=\"$gw\",connection=\"$name\",direction=\"in\"} $rx
cloudland_vpn_connection_bytes_total{gateway_id=\"$gw\",connection=\"$name\",direction=\"out\"} $tx"
        traffic="$traffic $name:$(vpn_traffic_since_base $vpn_dir ipsec-$if_id $rx $tx | tr ' ' ':')"
    done
    # wg show dump: the interface line, then one peer per line: pubkey psk endpoint allowed-ips handshake rx tx keepalive
    wgdump=""
    if [ -f $vpn_dir/wg.conf ] && ip netns exec $router ip link show wg-$gw >/dev/null 2>&1; then
        wgdump=$(timeout 10 ip netns exec $router wg show wg-$gw dump 2>/dev/null | tail -n +2)
        while read key psk endpoint allowed handshake rx tx keepalive; do
            [ -n "$tx" ] || continue
            client_metrics="$client_metrics
cloudland_vpn_client_bytes_total{gateway_id=\"$gw\",client_key=\"$key\",direction=\"in\"} $rx
cloudland_vpn_client_bytes_total{gateway_id=\"$gw\",client_key=\"$key\",direction=\"out\"} $tx"
        done <<<"$wgdump"
    fi

    # IPsec: state and age from swanctl --list-sas; traffic from the tunnel interfaces (read above).
    # The byte counters of the SAs were never used: they restart at every rekey, and the swanctl lines of
    # an SA bound to an XFRM interface carry "(-|0x...)" after the SPI, which the old pattern did not allow
    if [ -f $vpn_dir/conn_names ] && vpn_charon_alive $vpn_dir; then
        sas=$(timeout 10 ip netns exec $router swanctl --list-sas --uri unix://$vpn_dir/run/charon.vici 2>/dev/null)
        payload=$(awk -v now=$now -v names="$(tr '\n' ' ' <$vpn_dir/conn_names)" -v traffic="$traffic" '
            BEGIN {
                n = split(names, list, " ")
                m = split(traffic, t, " ")
                for (i = 1; i <= m; i++) { split(t[i], f, ":"); bin[f[1]] = f[2]; bout[f[1]] = f[3] }
            }
            /^[A-Za-z0-9_-]+: #[0-9]+, / {
                name = $1; sub(":$", "", name)
                st = $3; sub(",$", "", st)
                if (st == "ESTABLISHED") { up[name] = 1 }
                cur = name
            }
            /^ +established [0-9]+s ago/ { for (i = 1; i <= NF; i++) if ($i == "established") { est[cur] = now - substr($(i+1), 1, length($(i+1)) - 1) } }
            END {
                printf "["
                for (i = 1; i <= n; i++) {
                    name = list[i]; if (name == "") continue
                    if (i > 1) printf ","
                    printf "{\"name\":\"%s\",\"state\":\"%s\",\"established_at\":%d,\"bytes_in\":%.0f,\"bytes_out\":%.0f,\"error\":\"\"}", name, (name in up) ? "up" : "down", est[name] + 0, bin[name] + 0, bout[name] + 0
                }
                printf "]"
            }' <<<"$sas")
        encoded=$(echo -n "$payload" | base64 -w0)
        report_if_key_changed $vpn_dir/status.reported "$(zero_bytes <<<"$payload")" $traffic_every "|:-COMMAND-:| vpn_conn_status.sh '$gw' '$NODE_ID' '$encoded'"
    fi

    # WireGuard: the peers of the dump read above
    if [ -f $vpn_dir/wg.conf ] && ip netns exec $router ip link show wg-$gw >/dev/null 2>&1; then
        payload=$(awk 'NF >= 7 {
                if (n++) printf ","
                printf "{\"public_key\":\"%s\",\"last_handshake\":%d,\"bytes_in\":%.0f,\"bytes_out\":%.0f}", $1, $5, $6, $7
            } BEGIN { printf "[" } END { printf "]" }' <<<"$wgdump")
        encoded=$(echo -n "$payload" | base64 -w0)
        # The handshake time is what matters; the counters ride along (traffic_interval)
        report_if_key_changed $vpn_dir/wg.reported "$(zero_bytes <<<"$payload")" $traffic_every "|:-COMMAND-:| vpn_client_status.sh '$gw' '$NODE_ID' '$encoded'"
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

# Written on every node every heartbeat, so a former master stops exporting at once; atomic for the collector
if [ -d $metrics_dir ]; then
    {
        echo "# HELP cloudland_vpn_connection_bytes_total Bytes through the tunnel interface of a VPN site connection on the gateway master (in: from the site)."
        echo "# TYPE cloudland_vpn_connection_bytes_total counter"
        if [ -n "$conn_metrics" ]; then echo "${conn_metrics#?}"; fi
        echo "# HELP cloudland_vpn_client_bytes_total Bytes exchanged with a WireGuard client of a VPN gateway on its master (in: from the client)."
        echo "# TYPE cloudland_vpn_client_bytes_total counter"
        if [ -n "$client_metrics" ]; then echo "${client_metrics#?}"; fi
    } >$metrics_dir/.cloudland_vpn.prom.$$ && mv -f $metrics_dir/.cloudland_vpn.prom.$$ $metrics_dir/cloudland_vpn.prom
fi
exit 0
