#!/bin/bash
# List what is in a shared pool for the orphan report (shared-storage-design.md §16 S5): <pool UUID> <cluster UUID>.
# The drv_list hook of the pool's driver gives the files or images; clapi compares them with its records and only
# reports what it does not know, nothing is removed. In the background (a pool can be slow to list); the answer is
#
#   |:-COMMAND-:| shared_pool_objects '<pool UUID>' '<base64 of the gzipped JSON>'
#
# JSON: {"objects": [{"name", "size", "mtime"}], "truncated", "error"}. A callback line is at most 1 MiB (cloudlet):
# the list is cut to fit, truncated says so

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh

pool=$1
cluster=$2
valid_uuid "$pool" && valid_uuid "$cluster" || exit 1

# Most of what a callback line may carry: 1 MiB less the command, the pool and some room
max_payload=1000000

function list_job()
{
    local entry list total keep json payload
    entry=$(jq -c --arg p "$pool" '.[]? | select(.pool == $p)' $shared_storage_dir/$cluster/shared_pools.json 2>/dev/null | head -1)
    list=$(mktemp)
    if [ -z "$entry" ]; then
        json=$(jq -cn '{objects: [], error: "the pool is not in the pool list of this host"}')
    elif ! drv_load "$entry" || ! declare -F drv_list >/dev/null; then
        json=$(jq -cn --arg e "${guard_error:-the driver lists nothing}" '{objects: [], error: $e}')
    elif ! drv_list >$list; then
        json=$(jq -cn --arg e "${guard_error:-listing the pool failed}" '{objects: [], error: $e}')
    else
        total=$(grep -c . $list)
        keep=$total
        while :; do
            json=$(head -n $keep $list | jq -R -s -c --argjson t "$total" --argjson k "$keep" '
                split("\n") | map(select(. != "") | split("\t") | {name: .[0], size: (.[1] | tonumber? // 0), mtime: (.[2] | tonumber? // 0)})
                | {objects: ., truncated: ($k < $t)}')
            payload=$(gzip -c <<<"$json" | base64 -w0)
            [ ${#payload} -le $max_payload ] || [ $keep -le 1000 ] && break
            keep=$((keep * 3 / 4))
        done
    fi
    rm -f $list
    [ -n "$payload" ] || payload=$(gzip -c <<<"$json" | base64 -w0)
    echo "|:-COMMAND-:| shared_pool_objects '$pool' '$payload'"
}

async_exec list_job
exit 0
