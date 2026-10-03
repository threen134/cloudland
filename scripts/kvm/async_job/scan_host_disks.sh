#!/bin/bash
# Scan the block devices of this host for building local storage pools (§4.2 of the local storage plan)

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh

if ! disks_lock 60; then
    echo "|:-COMMAND-:| host_disks '$NODE_ID' '' 'another storage operation is running'"
    exit 0
fi
system_disk_list=$(system_disks)
local_pool_list=$(local_pools)
types="disk"
[ "$storage_allow_loop" = "true" ] && types="disk loop"
items=""
for dev in $(lsblk -dnpo NAME,TYPE,RM | awk -v t=" $types " 'index(t, " " $2 " ") && $3 == "0" {print $1}'); do
    # A loop device without a backing file is not a disk
    if [[ "$(basename $dev)" == loop* ]] && [ -z "$(losetup -nO BACK-FILE $dev 2>/dev/null | xargs)" ]; then
        continue
    fi
    classify_disk $dev
    item=$(jq -cn \
        --arg id "$(disk_stable_id $dev)" \
        --arg name "$(basename $dev)" \
        --arg path "$dev" \
        --arg serial "$(lsblk -dno SERIAL $dev 2>/dev/null | xargs)" \
        --arg model "$(lsblk -dno MODEL $dev 2>/dev/null | xargs)" \
        --arg size "$(lsblk -dbno SIZE $dev 2>/dev/null | xargs)" \
        --arg tran "$(lsblk -dno TRAN $dev 2>/dev/null | xargs)" \
        --arg media "$(disk_media $dev)" \
        --arg state "$disk_state" \
        --arg detail "$disk_detail" \
        --arg pool "$disk_pool" \
        --arg owner "$disk_owner" \
        '{id: $id, name: $name, path: $path, serial: $serial, model: $model, size_bytes: ($size | tonumber? // 0),
          transport: $tran, media: $media, state: $state, detail: $detail, pool_uuid: $pool,
          owner_hostid: ($owner | tonumber? // 0)}')
    items="$items$item"$'\n'
done
json=$(printf '%s' "$items" | jq -cs '.')
echo "|:-COMMAND-:| host_disks '$NODE_ID' '$(echo -n "$json" | base64 -w0)' ''"
