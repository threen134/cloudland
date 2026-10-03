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

entry=$(jq -c --arg p "$pool" '.[]? | select(.pool == $p)' $shared_storage_dir/$cluster/shared_pools.json 2>/dev/null | head -1)
if [ -z "$entry" ]; then
    write_state unavailable "not in the pool list of this host"
elif drv_load "$entry" && drv_probe; then
    write_state ready "" "$probe_json"
else
    write_state unavailable "$guard_error"
fi
exit 0
