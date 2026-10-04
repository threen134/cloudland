#!/bin/bash
# Health of a storage cluster (shared-storage-design.md §14.1), run by clapi on one host of the cluster every minute:
# <cluster UUID>, {"kind", "mode", "filesystems": [{"name", "mount"}], ...} on stdin. The backend_health hook of the
# kind (backends/<kind>.sh) writes the report, the same JSON for every kind, to stdout; it goes back as
#
#   |:-COMMAND-:| storage_health '<cluster UUID>' '<base64 JSON>'
#
# The check runs in the background (async_exec: its output goes back with the next heartbeat), at most one at a time
# per cluster on a host, so a hung storage command does not pile up checks.

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh

uuid=$1
input=$(cat)
valid_uuid "$uuid" || exit 1
kind=$(jq -r '.kind // empty' <<<"$input" 2>/dev/null)
backend_load "$kind" || exit 1
declare -F backend_health >/dev/null || exit 0

function health_job()
{
    local report
    exec 9>$stc_lock_dir/cloudland-storage-health-$uuid.lock
    # return, not exit: async_exec renames the job file to done after the function, and an exit skips that
    flock -n 9 || return 0
    report=$(backend_health "$uuid" "$input" 2>>$stc_log_dir/health-$uuid.log)
    if ! jq -e 'type == "object" and (.health | type == "string")' >/dev/null 2>&1 <<<"$report"; then
        report=$(jq -cn '{health: "unknown", error: "the health check of this host gave no report"}')
    fi
    echo "|:-COMMAND-:| storage_health '$uuid' '$(jq -c . <<<"$report" | base64 -w0)'"
}

mkdir -p $stc_log_dir
async_exec health_job
exit 0
