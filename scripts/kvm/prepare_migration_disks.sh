#!/bin/bash
# Run on the target host over ssh by source_migration.sh (§7.3 of the local storage plan):
#   prepare <migration_ID> <hostid> <live|offline>   check the target pools and make room for the disks
#   cleanup <migration_ID> <hostid>                  undo prepare after a failure
# stdin of prepare: JSON array of {dst_path, dst_pool, vsize, cluster, actual} (sizes in bytes).
# Prints "OK" or "ERROR <reason>". Nothing here is a callback: the output goes back to the source over ssh.

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

action=$1
migration_ID=$2
hostid=$3
mode=$4
[[ "$migration_ID" =~ ^[0-9]+$ ]] || { echo "ERROR invalid migration id"; exit 1; }
created_list=$run_dir/migration-$migration_ID.created

# Room already promised to other migrations coming into a pool: <pool root>
function incoming_bytes()
{
    local tmp=$1/tmp
    [ "$1" = "$cache_dir" ] && tmp=$cache_tmp_dir
    # Placeholders older than a day belong to migrations that died: their reservation in clapi is gone too
    find $tmp -maxdepth 1 -name '.incoming-*' -mmin -1440 -exec cat {} + 2>/dev/null | awk '{s += $1} END {print s + 0}'
}

function cleanup()
{
    local path
    # Only files this migration created; a domain defined here means the instance lives on this host
    if ! virsh domstate $vm_ID >/dev/null 2>&1; then
        while read path; do
            [ -n "$path" ] && rm -f "$path"
        done < <(cat $created_list 2>/dev/null)
    fi
    rm -f $created_list $cache_tmp_dir/.incoming-$migration_ID $pools_dir/*/tmp/.incoming-$migration_ID
}

if [ "$action" = "cleanup" ]; then
    vm_ID=${mode:+inst-$mode}
    cleanup
    echo OK
    exit 0
fi

[ "$action" = "prepare" ] || { echo "ERROR unknown action $action"; exit 1; }
disks=$(cat)
pools=$(jq -r '.[].dst_pool' <<<"$disks" | sort -u)
for pool in $pools; do
    root=$(pool_root "$pool") || { echo "ERROR invalid pool $pool"; exit 1; }
    # Shared: writing into the pool. The space check and the placeholder of two migrations are serialised apart,
    # so a long image download holding the pool shared does not block migrations
    if ! pool_lock "$pool" shared 60; then
        echo "ERROR pool $pool is busy on the target"
        exit 1
    fi
    exec 6>$pool_lock_dir/cloudland-incoming-$pool.lock
    if ! flock -w 60 -x 6; then
        echo "ERROR another migration into pool $pool is being prepared"
        exit 1
    fi
    if [ "$pool" != "builtin" ] && ! pool_guard "$root" "$pool" "$hostid"; then
        echo "ERROR $guard_error"
        exit 1
    fi
    # The copy needs the real data of the disks, not their virtual size
    need=$(jq -r --arg p "$pool" '[.[] | select(.dst_pool == $p) | .actual] | add // 0' <<<"$disks")
    avail=$(timeout 10 df -B1 --output=avail "$root" 2>/dev/null | tail -1 | tr -d ' ')
    [ -z "$avail" ] && { echo "ERROR can not read the free space of $root"; exit 1; }
    promised=$(incoming_bytes $root)
    if [ $((avail - promised)) -lt "$need" ]; then
        echo "ERROR pool $pool on the target has $((avail - promised)) bytes free for $need bytes of data"
        exit 1
    fi
    tmp=$root/tmp
    [ "$pool" = "builtin" ] && tmp=$cache_tmp_dir
    mkdir -p $tmp && echo $need >$tmp/.incoming-$migration_ID
    for row in $(jq -c --arg p "$pool" '.[] | select(.dst_pool == $p)' <<<"$disks"); do
        path=$(jq -r .dst_path <<<"$row")
        if [[ "$path" != "$root"/* || "$path" == *"/../"* ]]; then
            echo "ERROR $path is not inside pool $pool"
            exit 1
        fi
        if [ -e "$path" ]; then
            echo "ERROR disk $path already exists on the target"
            exit 1
        fi
        mkdir -p "$(dirname $path)"
        echo "$path" >>$created_list
        if [ "$mode" = "live" ]; then
            # An empty image with the virtual size and cluster size of the source; QEMU copies the data into it
            vsize=$(jq -r .vsize <<<"$row")
            cluster=$(jq -r '.cluster // 65536' <<<"$row")
            if ! qemu-img create -q -f qcow2 -o cluster_size=$cluster "$path" $vsize >/dev/null 2>&1; then
                echo "ERROR failed to create $path on the target"
                exit 1
            fi
        fi
    done
    exec 8>&- 6>&-
done
echo OK
