#!/bin/bash
# The fileset of a CloudLand pool on a GPFS file system, run on an admin host (shared-storage-design.md §7.4).
# Input: {"action": "create" | "quota" | "delete", "cluster_uuid", "pool_uuid", "fs_name", "fileset", "junction",
#         "inode_limit", "quota_bytes", "multi_pool", "rules": [{"fileset", "gpfs_pool"}]}
# rules are the placement of every other pool of the file system (the new one included on create): with two GPFS
# storage pools CloudLand installs the whole policy each time, a file system with one needs none (§7.3).
# Result of create and delete: {"policy_installed": bool}. Every action checks what is there first, so a retry goes on
# where the last attempt stopped.
# On an imported cluster (§7.8) the directory of the pool exists and no GPFS command is run (the host may not be an
# admin node of that cluster): "register" makes the subdirectories and the marker, "unregister" removes the marker of
# an empty pool. Input: {"action", "cluster_uuid", "pool_uuid", "junction", "mount_point"}

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/gpfs.sh
STC_SCRIPT=$(readlink -f $0)

# gpfs_fileset_state <fs> <fileset>: "missing", "unlinked", or "linked <junction>"
function gpfs_fileset_state()
{
    local out row status path
    out=$($gpfs_bin/mmlsfileset $1 $2 -Y 2>/dev/null)
    # Read the names: like mmlsnsd -d, a query for a name it does not know is not trusted to fail
    row=$(awk -F: -v want="$2" '
        $2 == "" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "filesetName") n = i; if ($i == "status") s = i; if ($i == "path") p = i }; next }
        n && $n == want { print $s ":" $p; exit }' <<<"$out")
    if [ -z "$row" ]; then
        echo missing
        return
    fi
    status=${row%%:*}
    path=${row#*:}
    # -Y output escapes special characters as %xx
    path=$(printf '%b' "${path//%/\\x}")
    if [ "$status" = "Linked" ]; then
        echo "linked $path"
    else
        echo unlinked
    fi
}

# gpfs_install_policy <fs> <rules json> <default pool>: generate and install the placement of the file system, checked
# with -I test first so a bad rule never replaces the policy in place
function gpfs_install_policy()
{
    local fs=$1 rules=$2 def=$3 f
    f=$(mktemp)
    jq -r --arg q "'" '.[] | "RULE \($q)\(.fileset)\($q) SET POOL \($q)\(.gpfs_pool)\($q) FOR FILESET (\($q)\(.fileset)\($q))"' <<<"$rules" >$f
    echo "RULE 'default' SET POOL '$def'" >>$f
    echo "placement rules:"; cat $f
    if ! $gpfs_bin/mmchpolicy $fs $f -I test; then
        rm -f $f
        return 1
    fi
    $gpfs_bin/mmchpolicy $fs $f -I yes
    local rc=$?
    rm -f $f
    return $rc
}

# pool_dirs <root> <pool uuid>: the directories and the marker of a pool, at its root on GPFS
function pool_dirs()
{
    local root=$1 pool=$2 marker
    [ "$(timeout 10 stat -f -c %T $root 2>/dev/null)" = "gpfs" ] || stc_fail "$root is not on GPFS on this host"
    marker=$(cat $root/.cloudland-pool 2>/dev/null)
    if [ -n "$marker" ] && ! grep -qx "pool_uuid=$pool" <<<"$marker"; then
        stc_fail "$root is the root of another pool already: $(tr '\n' ' ' <<<"$marker")"
    fi
    mkdir -p $root/volumes $root/images $root/nvram $root/tmp || stc_fail "creating the directories of the pool failed"
    # libvirt starts QEMU as its own user, whose id may differ between hosts: it must pass through the directories
    chmod 755 $root $root/volumes $root/images $root/nvram $root/tmp
    printf 'driver=gpfs\npool_uuid=%s\n' "$pool" >$root/.cloudland-pool.tmp && mv -f $root/.cloudland-pool.tmp $root/.cloudland-pool ||
        stc_fail "writing the marker of the pool failed"
}

# The pool of an imported cluster: a directory its admins made
function do_external()
{
    local input=$1 action=$2 pool root mount files
    pool=$(jq -r .pool_uuid <<<"$input")
    root=$(jq -r .junction <<<"$input")
    mount=$(jq -r .mount_point <<<"$input")
    valid_uuid "$pool" || stc_fail "invalid pool uuid"
    [[ "$mount" =~ ^/[A-Za-z0-9_./-]+$ ]] && [[ "$root" =~ ^/[A-Za-z0-9_./-]+$ ]] && [[ "$root" == "$mount"/* ]] &&
        [ "$(realpath -m "$root")" = "$root" ] || stc_fail "invalid pool directory $root"
    case $action in
    register)
        [ -d "$root" ] || stc_fail "$root does not exist: the admins of the cluster make the directory of the pool"
        pool_dirs $root $pool
        ;;
    unregister)
        if [ -d "$root" ]; then
            files=$(ls $root/volumes 2>/dev/null | grep -c .)
            [ "$files" -gt 0 ] && stc_fail "the pool still holds $files files in volumes/"
            grep -qx "pool_uuid=$pool" $root/.cloudland-pool 2>/dev/null && rm -f $root/.cloudland-pool
        fi
        ;;
    esac
    stc_result '{"policy_installed": false}'
}

function stc_main()
{
    local input action uuid pool fs fileset junction inodes quota multi rules state files policy=false kib
    input=$(cat)
    action=$(jq -r .action <<<"$input")
    uuid=$(jq -r .cluster_uuid <<<"$input")
    pool=$(jq -r .pool_uuid <<<"$input")
    fs=$(jq -r .fs_name <<<"$input")
    fileset=$(jq -r .fileset <<<"$input")
    junction=$(jq -r .junction <<<"$input")
    inodes=$(jq -r '.inode_limit // 100000' <<<"$input")
    quota=$(jq -r '.quota_bytes // 0' <<<"$input")
    multi=$(jq -r '.multi_pool // false' <<<"$input")
    rules=$(jq -c '.rules // []' <<<"$input")
    valid_uuid "$uuid" && valid_uuid "$pool" || stc_fail "invalid cluster or pool uuid"
    if [ "$action" = "register" ] || [ "$action" = "unregister" ]; then
        stc_lock "gpfs-$uuid"
        do_external "$input" $action
        return
    fi
    [[ "$fs" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] || stc_fail "invalid file system name"
    [[ "$fileset" =~ ^cl_[0-9a-f]{8}$ ]] || stc_fail "invalid fileset name"
    [ "$junction" = "/gpfs/$fs/$fileset" ] || stc_fail "invalid junction $junction"
    [[ "$inodes" =~ ^[0-9]+$ ]] && [[ "$quota" =~ ^[0-9]+$ ]] || stc_fail "invalid inode limit or quota"
    stc_lock "gpfs-$uuid"
    $gpfs_bin/mmlsfs $fs >/dev/null 2>&1 || stc_fail "file system $fs not found"
    state=$(gpfs_fileset_state $fs $fileset)
    echo "fileset $fileset: $state"
    case $action in
    create)
        # df on a fileset junction reports the quota of the fileset as its size: the capacity of the pool (§9.5)
        if [ "$($gpfs_bin/mmlsfs $fs --filesetdf 2>/dev/null | awk '$1 == "--filesetdf" {print $2}')" != "yes" ]; then
            $gpfs_bin/mmchfs $fs --filesetdf || echo "warning: df of the pools will report the whole file system"
        fi
        if [ "$state" = "missing" ]; then
            stc_progress 20 "creating fileset $fileset"
            $gpfs_bin/mmcrfileset $fs $fileset --inode-space new --inode-limit $inodes || stc_fail "mmcrfileset failed"
            state=unlinked
        fi
        if [ "$state" = "unlinked" ]; then
            stc_progress 40 "linking $fileset at $junction"
            $gpfs_bin/mmlinkfileset $fs $fileset -J $junction || stc_fail "mmlinkfileset failed"
            state="linked $junction"
        fi
        [ "$state" = "linked $junction" ] || stc_fail "fileset $fileset is linked at ${state#linked } instead of $junction"
        if [ "$multi" = "true" ]; then
            stc_progress 60 "installing the placement rules"
            gpfs_install_policy $fs "$rules" data || stc_fail "installing the placement rules failed"
            policy=true
        fi
        if [ "$quota" -gt 0 ]; then
            kib=$((quota / 1024))
            $gpfs_bin/mmsetquota $fs:$fileset --block ${kib}K:${kib}K || stc_fail "mmsetquota failed"
        else
            $gpfs_bin/mmsetquota $fs:$fileset --block 0:0 || stc_fail "mmsetquota failed"
        fi
        pool_dirs $junction $pool
        $gpfs_bin/mmlsfileset $fs $fileset -L
        $gpfs_bin/mmlsquota -j $fileset $fs
        ;;
    quota)
        [ "$state" = "missing" ] && stc_fail "fileset $fileset not found"
        if [ "$quota" -gt 0 ]; then
            kib=$((quota / 1024))
            $gpfs_bin/mmsetquota $fs:$fileset --block ${kib}K:${kib}K || stc_fail "mmsetquota failed"
        else
            $gpfs_bin/mmsetquota $fs:$fileset --block 0:0 || stc_fail "mmsetquota failed"
        fi
        $gpfs_bin/mmlsquota -j $fileset $fs
        ;;
    delete)
        if [ "$state" = "linked $junction" ]; then
            [ "$(timeout 10 stat -f -c %T $junction 2>/dev/null)" = "gpfs" ] || stc_fail "$junction is not reachable on this host"
            # clapi refused while the pool had volumes; a file left here is somebody's data all the same
            files=$(ls $junction/volumes 2>/dev/null | grep -c .)
            [ "$files" -gt 0 ] && stc_fail "the pool still holds $files files in volumes/: $(ls $junction/volumes | head -5 | xargs)"
        fi
        if [[ "$state" == linked* ]]; then
            stc_progress 30 "unlinking $fileset"
            $gpfs_bin/mmunlinkfileset $fs $fileset -f || stc_fail "mmunlinkfileset failed"
            state=unlinked
        fi
        if [ "$state" = "unlinked" ]; then
            stc_progress 60 "deleting $fileset"
            $gpfs_bin/mmdelfileset $fs $fileset -f || stc_fail "mmdelfileset failed"
        fi
        if [ "$multi" = "true" ]; then
            stc_progress 80 "installing the placement rules"
            gpfs_install_policy $fs "$rules" data || stc_fail "installing the placement rules failed"
            policy=true
        fi
        ;;
    *)
        stc_fail "unknown action $action"
        ;;
    esac
    stc_result "$(jq -cn --argjson p $policy '{policy_installed: $p}')"
}

stc_run "$@"
