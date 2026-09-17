#!/bin/bash

# Relay one TCP connection to an instance's serial console pty (started by start_serial_console.sh).
# Only the given sources may connect to the port; the listener gives up when nobody connects in time.

cd $(dirname $0)
source ../cloudrc

[ $# -lt 5 ] && die "$0 <vm_ID> <pty> <listen_ip> <port> <allowed_source>..."

vm_ID=$1
pty=$2
listen_ip=$3
port=$4
shift 4
sources="$@"
accept_timeout=60

bridge_dir=$run_dir/serial_console
pidfile=$bridge_dir/$vm_ID.pid
echo $$ >$pidfile

rules=()
for src in $sources; do
    rules+=("-p tcp -s $src -d $listen_ip --dport $port -j ACCEPT")
done
rules+=("-p tcp -d $listen_ip --dport $port -j DROP")

cleanup()
{
    [ -n "$watchdog" ] && kill $watchdog 2>/dev/null
    [ -n "$relay" ] && kill $relay 2>/dev/null
    for rule in "${rules[@]}"; do
        iptables -D INPUT $rule 2>/dev/null
    done
    [ "$(cat $pidfile 2>/dev/null)" = "$$" ] && rm -f $pidfile
    log_debug $vm_ID "serial console bridge on $listen_ip:$port closed"
}
trap cleanup EXIT
trap 'exit 0' TERM INT

# The node accepts everything from private networks by default: put the restriction in front of those rules
for ((i=${#rules[@]}-1; i>=0; i--)); do
    iptables -I INPUT 1 ${rules[$i]}
done

log_debug $vm_ID "serial console bridge for $pty on $listen_ip:$port, allowed: $sources"
# Without fork socat serves a single connection and exits when either side closes
socat TCP-LISTEN:$port,bind=$listen_ip,reuseaddr FILE:$pty,raw,echo=0 &
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
