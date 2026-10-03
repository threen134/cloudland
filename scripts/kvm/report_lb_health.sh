#!/bin/bash

# Report load balancer backend health check results to clapi, called by report_rc.sh on every heartbeat.
# Only the node holding the floating IP (VRRP master) reports, and only when the result changed or the
# last report is older than $report_interval seconds, so a lost callback is eventually corrected.
# stdout is the callback protocol: print nothing except |:-COMMAND-:| lines

cd `dirname $0`
source ../cloudrc

report_interval=300
# haproxy starts every checked server as UP and needs a few check rounds to mark dead ones DOWN;
# skip a freshly (re)loaded process so the transient result is not reported
settle_time=15

now=$(date +%s)
for conf in $router_dir/router-*/lb-*/haproxy.conf; do
    [ -f "$conf" ] || continue
    lb_dir=${conf%/haproxy.conf}
    lb_ID=${lb_dir##*/lb-}
    router=$(basename $(dirname $lb_dir))
    reported=$lb_dir/health.reported
    [ -f /var/run/netns/$router ] || continue
    is_master=false
    for vip in $(awk '$1 == "bind" && $2 !~ /^127\./ {sub(/:[0-9]+$/, "", $2); print $2}' $conf | sort -u); do
        ip netns exec $router ip -4 -o addr show | grep -qF " $vip/" && is_master=true && break
    done
    if [ "$is_master" != "true" ]; then
        # Drop the cache so this node reports immediately after it takes over
        rm -f $reported
        continue
    fi
    lb_proc_alive $lb_dir/haproxy.pid $conf || continue
    [ $(($now - $(stat -c %Y $lb_dir/haproxy.pid))) -lt $settle_time ] && continue
    # A reload means the config changed (e.g. a backend address), and clapi resets the health of changed
    # backends: report again even if the result text is the same as the cached one
    [ $reported -ot $lb_dir/haproxy.pid ] && rm -f $reported
    health=$(python3 - "$lb_dir/admin.sock" 2>/dev/null <<'EOF'
import socket, sys
s = socket.socket(socket.AF_UNIX)
s.settimeout(2)
s.connect(sys.argv[1])
# -1 4 -1: all proxies, servers only
s.sendall(b"show stat -1 4 -1\n")
data = b""
while True:
    chunk = s.recv(65536)
    if not chunk:
        break
    data += chunk
result = []
for line in data.decode().splitlines():
    fields = line.split(",")
    if line.startswith("#") or len(fields) < 18 or not fields[1].startswith("be-"):
        continue
    status = fields[17]
    # "UP 1/3" is still up while failing checks, "DOWN 1/2" still down while recovering
    state = "up" if status.startswith("UP") else "down" if status.startswith("DOWN") else "unknown"
    result.append("%s:%s" % (fields[1][3:], state))
print(" ".join(sorted(result)))
EOF
)
    [ -n "$health" ] || continue
    if [ -f $reported ] && [ "$(cat $reported)" = "$health" ] && [ $(($now - $(stat -c %Y $reported))) -lt $report_interval ]; then
        continue
    fi
    echo "|:-COMMAND-:| lb_health.sh '$lb_ID' '$health'"
    echo "$health" >$reported
done
exit 0
