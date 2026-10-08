#!/bin/bash
# The file systems of a GPFS cluster, run on an admin host (shared-storage-design.md §7.2, §7.3, §7.5):
#   create:    {"cluster_uuid", "fs_name", "mount_point", "block_size", "data_replicas", "meta_replicas",
#               "nsds": [{"name", "usage", "failure_group", "pool"}]}    result: {"capacity_bytes", "free_bytes"}
#              make the file system on the NSDs and mount it on every node. The maximum replicas are always 3, so
#              copies can be added later without making the file system again
#   add:       {"cluster_uuid", "fs_name", "nsds": [...]}   put more NSDs into the file system (no rebalancing)
#   remove:    {"cluster_uuid", "fs_name", "groups": [{"fs_name", "nsds": [name]}], "damaged"}   take NSDs out of
#              their file system (their data moves to the other disks first; damaged: the disks are gone, the other
#              copies stay) and delete them; a group for each file system the disks leaving are in
#   rebalance: {"cluster_uuid", "fs_name"}   spread the data over all disks (mmrestripefs -b), long and I/O heavy
#   restore:   {"cluster_uuid", "fs_name"}   give the files that lost a copy with a failed disk their copies back
#              (mmrestripefs -r), after the disk was replaced
#   delete:    {"cluster_uuid", "fs_name", "mount_point", "nsds": [name]}   unmount the file system everywhere, delete it
#              and its NSDs (a file system or an NSD already gone is fine: an aborted making leaves either)

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
    local input=$1 fs=$2 have list="" name damaged opts="" require_down avail left
    damaged=$(jq -r '.damaged // false' <<<"$input")
    require_down=$(jq -r '.require_down // false' <<<"$input")
    have=$(fs_disks $fs)
    for name in $(jq -r '.nsds[]' <<<"$input"); do
        [[ "$name" =~ ^[A-Za-z0-9_]+$ ]] || stc_fail "invalid NSD name $name"
        grep -qx "$name" <<<"$have" && list="${list:+$list;}$name"
    done
    # A disk replaced as failed: GPFS must have it down now, whatever the last health check saw (-p does not move
    # its data, so a disk that works would lose its copies for nothing). A disk recovering is being brought back
    if [ "$require_down" = "true" ] && [ -n "$list" ]; then
        for name in ${list//;/ }; do
            avail=$(timeout 60 $gpfs_bin/mmlsdisk $fs -Y 2>/dev/null | awk -F: -v n="$name" '
                $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "nsdName") a = i; if ($i == "availability") b = i }; next }
                a && b && $a == n { print $b }')
            case "$avail" in
            down | unrecovered) echo "$name is $avail" ;;
            "") stc_fail "the availability of $name could not be read: it is not dropped as failed" ;;
            *) stc_fail "$name is $avail: a disk that works is removed the normal way, not dropped as failed" ;;
            esac
        done
    fi
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
        if ! $gpfs_bin/mmdelnsd "$list"; then
            # A failed disk keeps the NSD id on its platter, which can not be cleared: what counts is that the cluster
            # has no such NSD any more
            left=""
            for name in ${list//;/ }; do
                gpfs_nsd_exists $name && left="$left $name"
            done
            [ -z "$left" ] || stc_fail "mmdelnsd failed for$left"
            echo "mmdelnsd could not clear the disks, their NSDs are gone from the cluster"
        fi
    fi
}

# do_remove_groups: the disks leaving may be in more than one file system (a host leaving with disks in two): each
# group of NSDs leaves its own file system ("groups": [{"fs_name", "nsds"}]); without groups, "nsds" leave "fs_name"
function do_remove_groups()
{
    local input=$1 fs=$2 group gfs
    if [ "$(jq -r '(.groups // []) | length' <<<"$input")" -gt 0 ]; then
        while read -r group; do
            gfs=$(jq -r .fs_name <<<"$group")
            [[ "$gfs" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] || stc_fail "invalid file system name $gfs"
            $gpfs_bin/mmlsfs $gfs >/dev/null 2>&1 || stc_fail "file system $gfs not found"
            do_remove "$(jq -c --argjson g "$group" '. + {nsds: $g.nsds}' <<<"$input")" $gfs
        done < <(jq -c '.groups[]' <<<"$input")
    else
        $gpfs_bin/mmlsfs $fs >/dev/null 2>&1 || stc_fail "file system $fs not found"
        do_remove "$input" $fs
    fi
    stc_result '{"removed": true}'
}

function do_delete()
{
    local input=$1 fs=$2 mount list="" name left i
    mount=$(jq -r .mount_point <<<"$input")
    [[ "$mount" =~ ^/gpfs/[A-Za-z0-9_]+$ ]] || stc_fail "invalid mount point"
    if $gpfs_bin/mmlsfs $fs >/dev/null 2>&1; then
        stc_progress 10 "unmounting $fs on every node"
        $gpfs_bin/mmumount $fs -a -f >/dev/null 2>&1
        for i in $(seq 1 60); do
            [ -z "$($gpfs_bin/mmlsmount $fs -L -Y 2>/dev/null | gpfs_y "" nodeIP | grep .)" ] && break
            stc_progress 20 "waiting for the nodes to unmount $fs"
            sleep 5
        done
        [ -z "$($gpfs_bin/mmlsmount $fs -L -Y 2>/dev/null | gpfs_y "" nodeIP | grep .)" ] || stc_fail "$fs is still mounted on some node"
        stc_progress 40 "deleting file system $fs"
        $gpfs_bin/mmdelfs $fs || stc_fail "mmdelfs $fs failed"
    else
        echo "file system $fs is gone already"
    fi
    for name in $(jq -r '.nsds[]' <<<"$input"); do
        [[ "$name" =~ ^[A-Za-z0-9_]+$ ]] || stc_fail "invalid NSD name $name"
        gpfs_nsd_exists $name && list="${list:+$list;}$name"
    done
    if [ -n "$list" ]; then
        stc_progress 70 "deleting the NSDs $list"
        if ! $gpfs_bin/mmdelnsd "$list"; then
            left=""
            for name in ${list//;/ }; do
                gpfs_nsd_exists $name && left="$left $name"
            done
            [ -z "$left" ] || stc_fail "mmdelnsd failed for$left"
        fi
    fi
    rmdir "$mount" 2>/dev/null
    stc_result '{"deleted": true}'
}

function do_rebalance()
{
    local fs=$2
    stc_progress 5 "rebalancing $fs"
    $gpfs_bin/mmrestripefs $fs -b || stc_fail "mmrestripefs failed"
    stc_result '{"rebalanced": true}'
}

function do_restore()
{
    local fs=$2
    stc_progress 5 "restoring the replication of $fs"
    $gpfs_bin/mmrestripefs $fs -r || stc_fail "mmrestripefs -r failed"
    stc_result '{"restored": true}'
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
        delete)
            stc_lock "gpfs-$uuid"
            do_delete "$input" $fs
            return
            ;;
        remove)
            stc_lock "gpfs-$uuid"
            do_remove_groups "$input" $fs
            return
            ;;
        add|rebalance|restore)
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
