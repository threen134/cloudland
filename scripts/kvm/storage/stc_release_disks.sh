#!/bin/bash
# Give disks a storage cluster no longer uses back to this host (shared-storage-design.md §7.5): the kind is told which
# disks of the cluster stay here (the GPFS nsddevices exit lists only those), then the released disks are wiped,
# each checked to be the disk that was claimed first, and forget their claim.
# Input: {"cluster_uuid", "kind", "keep": [stable id], "release": [{"id", "serial", "wwn", "size_bytes"}]}

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input uuid kind keep row id path wiped=0
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    kind=$(jq -r .kind <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    stc_lock "$kind-$uuid"
    mapfile -t keep < <(jq -r '.keep[]?' <<<"$input")
    if backend_load "$kind" && declare -F backend_disks_resolved >/dev/null; then
        backend_disks_resolved "$uuid" "${keep[@]}" || stc_fail "the $kind part of releasing the disks failed"
    fi
    for row in $(jq -c '.release[]?' <<<"$input"); do
        id=$(jq -r .id <<<"$row")
        rm -f $run_dir/storage/$uuid/claimed-$(echo -n "$id" | md5sum | cut -c1-16)
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
    stc_result "$(jq -cn --argjson w $wiped '{wiped: $w}')"
}

stc_run "$@"
