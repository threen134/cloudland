#!/bin/bash
# CloudLand stops using a cluster it imported (shared-storage-design.md §7.8): the pool lists of the cluster and their
# probes go from this host, with what the backend put there to reach it (the Ceph client configuration). The storage
# itself is not touched. Input: {"cluster_uuid", "kind"}

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input uuid kind
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    kind=$(jq -r '.kind // ""' <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    if [ -n "$kind" ] && backend_load "$kind" && declare -F backend_forget >/dev/null; then
        backend_forget "$uuid" || stc_fail "the $kind part of forgetting the cluster failed"
    fi
    rm -rf $run_dir/storage/$uuid
    shared_pools_prune
    stc_result '{"forgotten": true}'
}

stc_run "$@"
