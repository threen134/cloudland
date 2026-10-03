#!/bin/bash
# The file systems of a GPFS cluster, run on an admin host (shared-storage-design.md §7.2, §7.3, §7.5):
#   create:    {"cluster_uuid", "fs_name", "mount_point", "block_size", "data_replicas", "meta_replicas",
#               "nsds": [{"name", "usage", "failure_group", "pool"}]}    result: {"capacity_bytes", "free_bytes"}
#              make the file system on the NSDs and mount it on every node. The maximum replicas are always 3, so
#              copies can be added later without making the file system again
#   add:       {"cluster_uuid", "fs_name", "nsds": [...]}   put more NSDs into the file system (no rebalancing)
#   remove:    {"cluster_uuid", "fs_name", "nsds": [name], "damaged"}   take NSDs out (their data moves to the other
#              disks first; damaged: the disks are gone, the other copies stay) and delete them
#   rebalance: {"cluster_uuid", "fs_name"}   spread the data over all disks (mmrestripefs -b), long and I/O heavy

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/gpfs.sh
STC_SCRIPT=$(readlink -f $0)

# fs_disks <fs>: the NSD names of a file system
function fs_disks()
{
    $gpfs_bin/mmlsdisk $1 -Y 2>/dev/null | gpfs_y "" nsdName
}

function do_add()
{
    local input=$1 fs=$2 tmp name have made=0
    have=$(fs_disks $fs)
    tmp=$(mktemp)
    for row in $(jq -c '.nsds[]' <<<"$input"); do
        name=$(jq -r .name <<<"$row")
        [[ "$name" =~ ^[A-Za-z0-9_]+$ ]] || stc_fail "invalid NSD name $name"
        grep -qx "$name" <<<"$have" && continue
        jq -r '"%nsd: nsd=\(.name) usage=\(.usage) failureGroup=\(.failure_group) pool=\(.pool)"' <<<"$row" >>$tmp
        made=$((made + 1))
    done
    if [ $made -gt 0 ]; then
        echo "stanzas:"; cat $tmp
        stc_progress 20 "adding $made disks to $fs"
        # No -r: rebalancing is a task of its own, it can take hours
        $gpfs_bin/mmadddisk $fs -F $tmp -v yes || { rm -f $tmp; stc_fail "mmadddisk failed"; }
    fi
    rm -f $tmp
    $gpfs_bin/mmlsdisk $fs
    have=$(fs_disks $fs)
    for name in $(jq -r '.nsds[].name' <<<"$input"); do
        grep -qx "$name" <<<"$have" || stc_fail "$name is not in $fs after mmadddisk"
    done
    stc_result "$(jq -cn --argjson m $made '{added: $m}')"
}

function do_remove()
{
    local input=$1 fs=$2 have list="" name damaged opts=""
    damaged=$(jq -r '.damaged // false' <<<"$input")
    have=$(fs_disks $fs)
    for name in $(jq -r '.nsds[]' <<<"$input"); do
        [[ "$name" =~ ^[A-Za-z0-9_]+$ ]] || stc_fail "invalid NSD name $name"
        grep -qx "$name" <<<"$have" && list="${list:+$list;}$name"
    done
    if [ -n "$list" ]; then
        # -p: the disks can not be read any more; their data is not moved, the other copies stay
        [ "$damaged" = "true" ] && opts="-p"
        stc_progress 10 "taking $list out of $fs (the data moves to the other disks)"
        $gpfs_bin/mmdeldisk $fs "$list" $opts || stc_fail "mmdeldisk failed"
    fi
    list=""
    for name in $(jq -r '.nsds[]' <<<"$input"); do
        gpfs_nsd_exists $name && list="${list:+$list;}$name"
    done
    if [ -n "$list" ]; then
        stc_progress 80 "deleting the NSDs $list"
        if [ "$damaged" = "true" ]; then
            $gpfs_bin/mmdelnsd -p "$list" || stc_fail "mmdelnsd failed"
        else
            $gpfs_bin/mmdelnsd "$list" || stc_fail "mmdelnsd failed"
        fi
    fi
    stc_result '{"removed": true}'
}

function do_rebalance()
{
    local fs=$2
    stc_progress 5 "rebalancing $fs"
    $gpfs_bin/mmrestripefs $fs -b || stc_fail "mmrestripefs failed"
    stc_result '{"rebalanced": true}'
}

function stc_main()
{
    local input uuid fs mount bs data meta tmp nodes mounted i df size free action
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    fs=$(jq -r .fs_name <<<"$input")
    mount=$(jq -r .mount_point <<<"$input")
    bs=$(jq -r .block_size <<<"$input")
    data=$(jq -r .data_replicas <<<"$input")
    meta=$(jq -r .meta_replicas <<<"$input")
    action=$(jq -r .action <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    [[ "$fs" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] || stc_fail "invalid file system name"
    case $action in
        create) ;;
        add|remove|rebalance)
            stc_lock "gpfs-$uuid"
            $gpfs_bin/mmlsfs $fs >/dev/null 2>&1 || stc_fail "file system $fs not found"
            do_$action "$input" $fs
            return
            ;;
        *) stc_fail "unknown action $action" ;;
    esac
    [[ "$mount" =~ ^/gpfs/[A-Za-z0-9_]+$ ]] || stc_fail "invalid mount point"
    [[ "$bs" =~ ^[0-9]+M$ ]] || stc_fail "invalid block size"
    [[ "$data$meta" =~ ^[1-3][1-3]$ ]] || stc_fail "invalid replicas"
    stc_lock "gpfs-$uuid"
    if $gpfs_bin/mmlsfs $fs >/dev/null 2>&1; then
        echo "file system $fs exists already"
    else
        tmp=$(mktemp)
        jq -r '.nsds[] | "%nsd: nsd=\(.name) usage=\(.usage) failureGroup=\(.failure_group) pool=\(.pool)"' <<<"$input" >$tmp
        if grep -q "pool=data" $tmp; then
            # Disks of two kinds: metadata and small files on the fast ones in system, data on the HDDs (§7.3)
            echo "%pool: pool=data blockSize=$bs layoutMap=cluster" >>$tmp
        fi
        echo "stanzas:"; cat $tmp
        stc_progress 10 "creating file system $fs"
        $gpfs_bin/mmcrfs $fs -F $tmp -B $bs -m $meta -M 3 -r $data -R 3 -T $mount -A yes -Q yes -v yes ||
            { rm -f $tmp; stc_fail "mmcrfs failed"; }
        if grep -q "pool=data" $tmp; then
            echo "RULE 'default' SET POOL 'data'" >$tmp.policy
            $gpfs_bin/mmchpolicy $fs $tmp.policy || { rm -f $tmp $tmp.policy; stc_fail "installing the placement rule failed"; }
            rm -f $tmp.policy
        fi
        rm -f $tmp
    fi
    stc_progress 60 "mounting $fs on every node"
    $gpfs_bin/mmmount $fs -a
    nodes=$($gpfs_bin/mmlscluster -Y | gpfs_y clusterNode daemonNodeName | grep -c .)
    for i in $(seq 1 60); do
        mounted=$($gpfs_bin/mmlsmount $fs -L -Y 2>/dev/null | gpfs_y "" nodeIP | sort -u | grep -c .)
        [ "$mounted" -ge "$nodes" ] && break
        stc_progress $((60 + i / 2)) "mounted on $mounted of $nodes nodes"
        sleep 5
    done
    $gpfs_bin/mmlsmount $fs -L
    [ "$mounted" -ge "$nodes" ] || stc_fail "$fs is mounted on $mounted of $nodes nodes after 5 minutes"
    df=$($gpfs_bin/mmdf $fs -Y)
    size=$(gpfs_y fsTotal fsSize <<<"$df" | head -1)
    free=$(gpfs_y fsTotal freeBlocks <<<"$df" | head -1)
    stc_result "$(jq -cn --argjson s "${size:-0}" --argjson f "${free:-0}" --argjson n "$mounted" \
        '{capacity_bytes: ($s * 1024), free_bytes: ($f * 1024), mounted_nodes: $n}')"
}

stc_run "$@"
