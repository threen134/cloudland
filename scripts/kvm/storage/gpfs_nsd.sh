#!/bin/bash
# Make NSDs of the claimed disks, run on an admin host (shared-storage-design.md §7.2). The device names come from
# the resolve_disks step that ran right before on the NSD servers. Input: {"action": "create", "cluster_uuid",
# "nsds": [{"name", "device", "server", "failure_group", "usage", "pool"}]}; server is a comma separated list for a
# shared LUN (§7.10). NSDs that exist are left alone.
# {"action": "servers", "cluster_uuid", "nsds": [{"name", "servers"}]}: the shared LUNs that gain or lose servers get
# their new server list (mmchnsd, online since GPFS 5.0); an NSD that has that list already is left alone.

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/gpfs.sh
STC_SCRIPT=$(readlink -f $0)

# nsd_servers <nsd>: the servers of an NSD as GPFS has them, comma separated: mmlsnsd -X has a row for each server of
# a disk. Not read (another output than expected): the list is applied anyway, mmchnsd is idempotent
function nsd_servers()
{
    $gpfs_bin/mmlsnsd -X -Y 2>/dev/null | gpfs_y_rows "" diskName nodeName | awk -F'\t' -v n="$1" '$1 == n && $2 != "" {print $2}' |
        sort -u | paste -sd,
}

# servers: new server lists for shared LUNs
function do_servers()
{
    local input=$1 tmp row name servers ip changed=0
    tmp=$(mktemp)
    while read -r row; do
        [ -n "$row" ] || continue
        name=$(jq -r .name <<<"$row")
        servers=$(jq -r .servers <<<"$row")
        [[ "$name" =~ ^[A-Za-z0-9_]+$ ]] || stc_fail "invalid NSD name $name"
        [[ "$servers" =~ ^[0-9.,]+$ ]] || stc_fail "invalid server list $servers"
        gpfs_nsd_exists $name || stc_fail "NSD $name does not exist"
        # GPFS lists the servers by name or address: compare the addresses
        if [ "$(nsd_servers $name | tr ',' '\n' | while read -r s; do [ -n "$s" ] && getent ahostsv4 "$s" | awk '{print $1; exit}'; done | sort | xargs)" = \
            "$(tr ',' '\n' <<<"$servers" | sort | xargs)" ]; then
            echo "NSD $name is served by $servers already"
            continue
        fi
        echo "%nsd: nsd=$name servers=$servers" >>$tmp
        changed=$((changed + 1))
    done < <(jq -c '.nsds[]?' <<<"$input")
    if [ $changed -gt 0 ]; then
        echo "stanzas:"; cat $tmp
        stc_progress 30 "changing the servers of $changed NSDs"
        $gpfs_bin/mmchnsd -F $tmp || { rm -f $tmp; stc_fail "mmchnsd failed"; }
    fi
    rm -f $tmp
    stc_result "$(jq -cn --argjson c $changed '{changed: $c}')"
}

function stc_main()
{
    local input uuid tmp row name made=0 kept=0 action
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    action=$(jq -r .action <<<"$input")
    [ "$action" = "create" ] || [ "$action" = "servers" ] || stc_fail "unknown action"
    stc_lock "gpfs-$uuid"
    if [ "$action" = servers ]; then
        do_servers "$input"
        return
    fi
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
