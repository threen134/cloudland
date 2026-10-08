#!/bin/bash
# Last step of a deployment on every host (shared-storage-design.md §6.7): hold back the memory of the storage
# daemons from the instances. Input: {"cluster_uuid", "kind", "reserve_mb"}

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input uuid mb
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    mb=$(jq -r '.reserve_mb // 0' <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    [[ "$mb" =~ ^[0-9]+$ ]] || stc_fail "invalid reserve_mb"
    ./stc_mem_reserve.sh "$uuid" "$mb" || stc_fail "writing the memory reservation failed"
    echo "active" >$run_dir/storage/$uuid/state
    stc_result "$(jq -cn --argjson m $mb '{reserved_mb: $m}')"
}

stc_run "$@"
