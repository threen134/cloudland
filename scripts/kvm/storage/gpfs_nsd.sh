#!/bin/bash
# Make NSDs of the claimed disks, run on an admin host (shared-storage-design.md §7.2). The device names come from
# the resolve_disks step that ran right before on the NSD servers. Input: {"action": "create", "cluster_uuid",
# "nsds": [{"name", "device", "server", "failure_group", "usage", "pool"}]}. NSDs that exist are left alone.

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/gpfs.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input uuid tmp row name made=0 kept=0
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    [ "$(jq -r .action <<<"$input")" = "create" ] || stc_fail "unknown action"
    stc_lock "gpfs-$uuid"
    tmp=$(mktemp)
    for row in $(jq -c '.nsds[]' <<<"$input"); do
        name=$(jq -r .name <<<"$row")
        [[ "$name" =~ ^[A-Za-z0-9_]+$ ]] || stc_fail "invalid NSD name $name"
        if gpfs_nsd_exists $name; then
            kept=$((kept + 1))
            continue
        fi
        jq -r '"%nsd: device=\(.device) nsd=\(.name) servers=\(.server) usage=\(.usage) failureGroup=\(.failure_group) pool=\(.pool)"' <<<"$row" >>$tmp
        made=$((made + 1))
    done
    if [ $made -gt 0 ]; then
        echo "stanzas:"; cat $tmp
        stc_progress 20 "creating $made NSDs"
        $gpfs_bin/mmcrnsd -F $tmp -v yes || { rm -f $tmp; stc_fail "mmcrnsd failed"; }
    fi
    rm -f $tmp
    $gpfs_bin/mmlsnsd
    for row in $(jq -c '.nsds[]' <<<"$input"); do
        gpfs_nsd_exists "$(jq -r .name <<<"$row")" || stc_fail "NSD $(jq -r .name <<<"$row") is not there after mmcrnsd"
    done
    stc_result "$(jq -cn --argjson m $made --argjson k $kept '{created: $m, existing: $k}')"
}

stc_run "$@"
