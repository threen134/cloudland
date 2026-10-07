#!/bin/bash
# Find the claimed disks of a cluster on this host by their stable id and check them right before they are written
# (shared-storage-design.md §6.4): serial, WWN and size as recorded when they were claimed, not a system disk, not
# mounted, nothing on them (or wiped, when the admin chose that). Input: {"cluster_uuid", "kind", "disks":
# [{"id", "serial", "wwn", "size_bytes", "wipe"}]}. Result: {"disks": [{"id", "path", "name"}]}: path is the device
# the storage uses (the backend may put the disk under something first), name the kernel name of the disk

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input uuid kind row id serial wwn size wipe path name claimed out="[]" ids=() sys
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    kind=$(jq -r .kind <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    stc_lock "$kind-$uuid"
    backend_load "$kind" || stc_fail "this host has no storage backend $kind"
    sys=$(system_disks)
    for row in $(jq -c '.disks[]' <<<"$input"); do
        id=$(jq -r .id <<<"$row")
        serial=$(jq -r '.serial // ""' <<<"$row")
        wwn=$(jq -r '.wwn // ""' <<<"$row")
        size=$(jq -r '.size_bytes // 0' <<<"$row")
        wipe=$(jq -r '.wipe // false' <<<"$row")
        disk_identity "$id" "$serial" "$wwn" "$size" || stc_fail "$identity_error"
        path=$identity_path
        grep -qx "$(basename $path)" <<<"$sys" && stc_fail "$id ($path) holds the system"
        lsblk -nro MOUNTPOINT $path | grep -q . && stc_fail "$id ($path) has something mounted"
        # A disk checked clean by an earlier run of this cluster's task may hold its data by now (an NSD written
        # since): the identity still has to match, the emptiness does not
        claimed=$run_dir/storage/$uuid/claimed-$(echo -n "$id" | md5sum | cut -c1-16)
        if [ -f $claimed ]; then
            echo "$id ($path) was checked by an earlier run of this cluster"
        elif [ -n "$(wipefs -n $path 2>/dev/null)" ] || [ $(lsblk -nlo NAME $path | wc -l) -gt 1 ]; then
            if [ "$wipe" = "true" ]; then
                echo "wiping $id ($path)"
                wipe_disk $path || stc_fail "wiping $id ($path) failed"
            else
                stc_fail "$id ($path) has data on it"
            fi
        fi
        mkdir -p $run_dir/storage/$uuid && echo "$id" >$claimed
        name=$(basename $path)
        # The backend may put the disk under something of its own first (Ceph: a volume group on a loop device)
        if declare -F backend_disk_path >/dev/null; then
            path=$(backend_disk_path "$uuid" "$id" "$path") && [ -b "$path" ] || stc_fail "the $kind part of preparing $id failed"
        fi
        out=$(jq -c --arg i "$id" --arg p "$path" --arg n "$name" '. + [{id: $i, path: $p, name: $n}]' <<<"$out")
        ids+=("$id")
    done
    # "nsddevices": false: the disks go to a GPFS recovery group as pdisks, never as NSDs (the erasure code layout).
    # Not "// true": jq's alternative operator takes false for missing too
    if declare -F backend_disks_resolved >/dev/null && [ "$(jq -r '.nsddevices == false' <<<"$input")" != "true" ]; then
        backend_disks_resolved "$uuid" "${ids[@]}" || stc_fail "the $kind part of resolving the disks failed"
    fi
    stc_result "$(jq -cn --argjson d "$out" '{disks: $d}')"
}

stc_run "$@"
