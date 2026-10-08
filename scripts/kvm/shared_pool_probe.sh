#!/bin/bash
# Probe one shared pool in the background and write the result to a state file (shared-storage-design.md §9.2): the
# heartbeat (report_rc.sh) only starts it and reads the state. Touching a hung GPFS mount can block a process in a
# state neither timeout nor kill can end, and that must never be the heartbeat or the serial command queue.
# <pool UUID> <cluster UUID>: the pool first, so probe_alive tells it like the probe of a local pool.

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh
exec </dev/null >/dev/null 2>&1

pool=$1
cluster=$2
valid_uuid "$pool" && valid_uuid "$cluster" || exit 1
mkdir -p $pool_state_dir
state_file=$pool_state_dir/$pool.state
echo "$cluster" >$pool_state_dir/$pool.shared
now=$(date +%s)

function write_state()
{
    local status=$1 reason=$2 extra=$3
    [ -z "$extra" ] && extra='{}'
    jq -cn --arg status "$status" --arg reason "$reason" --argjson extra "$extra" --arg ts "$now" \
        '{status: $status, reason: $reason, ts: ($ts | tonumber), shared: true} + $extra' >$state_file.tmp && mv -f $state_file.tmp $state_file
}

# Size and use of the pool for the monitoring curves (shared-storage-design.md §14.3): every host of the pool writes
# the same numbers, the queries take the largest. The alarms do not come from here but from clapi (§14.2)
function write_metrics()
{
    local up=$1 extra=${2:-'{}'} dir l="cluster=\"$cluster\",pool=\"$pool\""
    dir=$(node_textfile_dir) || return 0
    {
        echo "cloudland_shared_pool_up{$l} $up"
        if [ "$up" = 1 ]; then
            jq -r --arg l "$l" '"cloudland_shared_pool_size_bytes{\($l)} \(.size // 0)",
                "cloudland_shared_pool_used_bytes{\($l)} \(.used // 0)"' <<<"$extra"
        fi
    } >$dir/cloudland_shared_pool_$pool.prom.$$ && mv -f $dir/cloudland_shared_pool_$pool.prom.$$ $dir/cloudland_shared_pool_$pool.prom
}

entry=$(jq -c --arg p "$pool" '.[]? | select(.pool == $p)' $shared_storage_dir/$cluster/shared_pools.json 2>/dev/null | head -1)
if [ -z "$entry" ]; then
    write_state unavailable "not in the pool list of this host"
    write_metrics 0
elif drv_load "$entry" && drv_probe; then
    write_state ready "" "$probe_json"
    write_metrics 1 "$probe_json"
else
    write_state unavailable "$guard_error"
    write_metrics 0
fi
exit 0
