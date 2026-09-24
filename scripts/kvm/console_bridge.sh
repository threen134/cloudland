#!/bin/bash

# Relay one TCP connection to a console (started through start_console_bridge in cloudrc): the serial console
# pty of an instance (start_serial_console.sh) or a shell on this node (start_host_console.sh).
# Only the given sources may connect to the port; the listener gives up when nobody connects in time.

cd $(dirname $0)
source ../cloudrc

[ $# -lt 6 ] && die "$0 <name> <idle_seconds> <socat_address> <listen_ip> <port> <allowed_source>..."

name=$1
# 0 keeps an idle session open
idle_timeout=$2
address=$3
listen_ip=$4
port=$5
shift 5
sources="$@"
accept_timeout=60

bridge_dir=$run_dir/console_bridge
mkdir -p $bridge_dir
pidfile=$bridge_dir/$name.pid
echo $$ >$pidfile

rules=()
for src in $sources; do
    rules+=("-p tcp -s $src -d $listen_ip --dport $port -j ACCEPT")
done
rules+=("-p tcp -d $listen_ip --dport $port -j DROP")

# Rules actually in place, removed on exit in reverse order
applied=()

cleanup()
{
    [ -n "$watchdog" ] && kill $watchdog 2>/dev/null
    [ -n "$relay" ] && kill $relay 2>/dev/null
    for rule in "${applied[@]}"; do
        iptables -w 5 -D INPUT $rule 2>/dev/null
    done
    [ "$(cat $pidfile 2>/dev/null)" = "$$" ] && rm -f $pidfile
    log_debug $name "console bridge on $listen_ip:$port closed"
}
trap cleanup EXIT
trap 'exit 0' TERM INT

# The node accepts everything from private networks by default: put the restriction in front of those rules.
# These rules are the only access control of the port, so a rule that could not be inserted (the heartbeat and
# the load balancer watchdog run iptables constantly and hold the xtables lock) must abort the bridge: -w waits
# for the lock, and cleanup removes what was applied
for ((i=${#rules[@]}-1; i>=0; i--)); do
    if ! iptables -w 5 -I INPUT 1 ${rules[$i]}; then
        die "Unable to restrict access to $listen_ip:$port, not starting the console bridge"
    fi
    applied+=("${rules[$i]}")
done

log_debug $name "console bridge on $listen_ip:$port, allowed: $sources"
socat_opts=()
# socat -T ends the transfer when no data flows in either direction for that long
[ "$idle_timeout" -gt 0 ] 2>/dev/null && socat_opts=(-T $idle_timeout)
# Without fork socat serves a single connection and exits when either side closes
socat "${socat_opts[@]}" TCP-LISTEN:$port,bind=$listen_ip,reuseaddr "$address" &
relay=$!

# Give up if the console proxy does not connect in time
(
    sleep $accept_timeout
    if ! ss -Htn state established "sport = :$port" | grep -q .; then
        kill $relay 2>/dev/null
    fi
) &
watchdog=$!

wait $relay
relay=
