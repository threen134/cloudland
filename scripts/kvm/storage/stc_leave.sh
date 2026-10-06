#!/bin/bash
# A host leaves a deleted storage cluster (shared-storage-design.md §7.6, §8.6): the kind cleans what is left of
# its software, the claimed disks are wiped (identity checked first), the cluster key and the memory reservation go,
# the kernel packages held at join are let go, and the member mark goes last.
# Input: {"cluster_uuid", "kind", "wipe": [{"id", "serial", "wwn", "size_bytes"}], "purge": bool}

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input uuid kind purge dir row id path wiped=0 p
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    kind=$(jq -r .kind <<<"$input")
    purge=$(jq -r '.purge // false' <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    stc_lock "$kind-$uuid"
    dir=$run_dir/storage/$uuid
    if backend_load "$kind" && declare -F backend_leave >/dev/null; then
        stc_progress 10 "cleaning up $kind"
        backend_leave "$uuid" "$purge" || stc_fail "the $kind part of leaving failed"
    fi
    for row in $(jq -c '.wipe[]?' <<<"$input"); do
        id=$(jq -r .id <<<"$row")
        if ! disk_identity "$id" "$(jq -r '.serial // ""' <<<"$row")" "$(jq -r '.wwn // ""' <<<"$row")" "$(jq -r '.size_bytes // 0' <<<"$row")"; then
            # Gone or another disk now: never wipe what is not the claimed disk
            echo "not wiping: $identity_error"
            continue
        fi
        path=$identity_path
        lsblk -nro MOUNTPOINT $path | grep -q . && stc_fail "$id ($path) has something mounted"
        stc_progress 50 "wiping $id ($path)"
        wipe_disk $path || stc_fail "wiping $id ($path) failed"
        wiped=$((wiped + 1))
    done
    # The line of the cluster, and the one of a new key an aborted rotation left
    stc_keys_update suffix "cloudland-storage-$uuid" || stc_fail "rewriting authorized_keys failed"
    stc_keys_update suffix "cloudland-storage-$uuid-rotate" || stc_fail "rewriting authorized_keys failed"
    ./stc_mem_reserve.sh "$uuid" 0
    for p in $(cat $dir/held_packages 2>/dev/null); do
        apt-mark unhold $p >/dev/null 2>&1
    done
    rm -rf $dir
    # The pools of the cluster went before it; a probe left over would never stop otherwise
    shared_pools_prune
    stc_result "$(jq -cn --argjson w $wiped '{wiped: $w}')"
}

stc_run "$@"
