#!/bin/bash
# GPFS multi-cluster remote mounts, run on an admin host of either cluster (shared-storage-design.md §7.11). Every
# action can run again: what is there is left alone.
#   key:     {"cluster_uuid"}   result: {"cluster_name", "public_key"}
#            the key pair of the cluster for remote access (mmauth genkey new + commit, once), a cipher list (AUTHONLY
#            when none is set: remote clusters authenticate to each other) and its GPFS name
#   grant:   {"cluster_uuid", "remote_name", "remote_key", "filesystem"}
#            the owner lets the other cluster in with its key and grants it the file system read-write
#   mount:   {"cluster_uuid", "remote_name", "remote_key", "contacts": [ip], "filesystem", "mount_point"}
#            result: {"mounted_nodes"}
#            the other cluster learns the owner and mounts the file system under its name and mount point on all its
#            nodes, at their start too
#   unmount: {"cluster_uuid", "remote_name", "filesystem", "forget_cluster"}
#            unmounted on every node and forgotten; the owner too when nothing else of it is mounted
#   revoke:  {"cluster_uuid", "remote_name", "filesystem", "forget_cluster"}
#            the grant goes; the other cluster's key too when it is granted nothing else

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/gpfs.sh
STC_SCRIPT=$(readlink -f $0)

pubkey=/var/mmfs/ssl/id_rsa.pub

valid_fs() { [[ "$1" =~ ^[A-Za-z][A-Za-z0-9_]{0,31}$ ]]; }
# A GPFS cluster name: what mmlscluster says, a host name like gpfs1.work-01
valid_cluster() { [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$ ]]; }

# key_file <base64>: the key of the other cluster in a file of its own, its path in key_path (removed by the caller)
function key_file()
{
    key_path=$(mktemp)
    base64 -d <<<"$1" >$key_path 2>/dev/null && grep -q . $key_path || { rm -f $key_path; stc_fail "invalid key of the other cluster"; }
}

# cipher_list: the cipher list of this cluster as mmauth shows it; nothing when none is set ("(none specified)" or
# EMPTY, what a cluster made with mmcrcluster has)
function cipher_list()
{
    local cl
    cl=$($gpfs_bin/mmauth show . 2>/dev/null | awk -F': *' 'tolower($1) ~ /^cipher ?list/ {print $2; exit}' | xargs)
    case "$cl" in
        "" | "(none specified)" | EMPTY | empty) ;;
        *) echo "$cl" ;;
    esac
}

function do_key()
{
    local name
    if [ ! -s $pubkey ]; then
        stc_progress 20 "generating the key of the cluster"
        $gpfs_bin/mmauth genkey new </dev/null || stc_fail "mmauth genkey new failed"
        $gpfs_bin/mmauth genkey commit </dev/null || stc_fail "mmauth genkey commit failed"
    fi
    [ -s $pubkey ] || stc_fail "the cluster has no key at $pubkey"
    # A remote mount authenticates the clusters to each other: both need a cipher list, AUTHONLY (authentication, no
    # encryption of the data) unless the admin set one. Whether GPFS takes the change while its daemons run is not
    # verified on a real cluster (shared-storage-design.md §7.11, experiment E11)
    if [ -z "$(cipher_list)" ]; then
        stc_progress 40 "setting the cipher list of the cluster to AUTHONLY"
        $gpfs_bin/mmauth update . -l AUTHONLY </dev/null ||
            stc_fail "mmauth update . -l AUTHONLY failed (an older GPFS may need GPFS stopped on every node of the cluster to change the cipher list)"
        [ -n "$(cipher_list)" ] || stc_fail "the cipher list of the cluster is still not set after mmauth update"
    fi
    name=$($gpfs_bin/mmlscluster -Y 2>/dev/null | gpfs_y clusterSummary clusterName | head -1)
    valid_cluster "$name" || stc_fail "the name of the cluster is not known"
    stc_result "$(jq -cn --arg n "$name" --arg k "$(base64 -w0 $pubkey)" '{cluster_name: $n, public_key: $k}')"
}

function do_grant()
{
    local input=$1 remote key fs f
    remote=$(jq -r .remote_name <<<"$input")
    key=$(jq -r .remote_key <<<"$input")
    fs=$(jq -r .filesystem <<<"$input")
    valid_cluster "$remote" || stc_fail "invalid cluster name"
    valid_fs "$fs" || stc_fail "invalid file system name"
    $gpfs_bin/mmlsfs $fs >/dev/null 2>&1 || stc_fail "file system $fs is not there"
    key_file "$key"
    f=$key_path
    if $gpfs_bin/mmauth show $remote >/dev/null 2>&1; then
        stc_progress 30 "updating the key of $remote"
        $gpfs_bin/mmauth update $remote -k $f </dev/null || { rm -f $f; stc_fail "mmauth update $remote failed"; }
    else
        stc_progress 30 "letting $remote in"
        $gpfs_bin/mmauth add $remote -k $f </dev/null || { rm -f $f; stc_fail "mmauth add $remote failed"; }
    fi
    rm -f $f
    stc_progress 60 "granting $fs to $remote"
    $gpfs_bin/mmauth grant $remote -f $fs -a rw </dev/null || stc_fail "mmauth grant $remote -f $fs failed"
    $gpfs_bin/mmauth show $remote
    stc_result '{}'
}

function do_mount()
{
    local input=$1 remote key contacts fs mp f nodes mounted i ips
    remote=$(jq -r .remote_name <<<"$input")
    key=$(jq -r .remote_key <<<"$input")
    contacts=$(jq -r '.contacts | join(",")' <<<"$input")
    fs=$(jq -r .filesystem <<<"$input")
    mp=$(jq -r .mount_point <<<"$input")
    valid_cluster "$remote" || stc_fail "invalid cluster name"
    valid_fs "$fs" || stc_fail "invalid file system name"
    [[ "$contacts" =~ ^[0-9.,]+$ ]] || stc_fail "invalid contact nodes"
    [[ "$mp" =~ ^/gpfs/[A-Za-z0-9_]+$ ]] || stc_fail "invalid mount point"
    key_file "$key"
    f=$key_path
    if $gpfs_bin/mmremotecluster show $remote >/dev/null 2>&1; then
        stc_progress 20 "updating cluster $remote"
        $gpfs_bin/mmremotecluster update $remote -n $contacts -k $f </dev/null || { rm -f $f; stc_fail "mmremotecluster update failed"; }
    else
        stc_progress 20 "learning cluster $remote"
        $gpfs_bin/mmremotecluster add $remote -n $contacts -k $f </dev/null || { rm -f $f; stc_fail "mmremotecluster add failed"; }
    fi
    rm -f $f
    # The file system under its own name: a local one of that name would be another file system (mmlsfs answers for
    # a remote one too, so only before it is known as remote)
    if $gpfs_bin/mmremotefs show $fs >/dev/null 2>&1; then
        echo "remote file system $fs is known already"
    else
        $gpfs_bin/mmlsfs $fs >/dev/null 2>&1 && stc_fail "this cluster has a file system $fs of its own"
        stc_progress 40 "adding remote file system $fs"
        $gpfs_bin/mmremotefs add $fs -f $fs -C $remote -T $mp -A yes </dev/null || stc_fail "mmremotefs add $fs failed"
    fi
    stc_progress 60 "mounting $fs on every node"
    $gpfs_bin/mmmount $fs -a
    ips=$($gpfs_bin/mmlscluster -Y | gpfs_y clusterNode ipAddress | sort -u)
    nodes=$(grep -c . <<<"$ips")
    for i in $(seq 1 60); do
        # The nodes of this cluster that mount it (the owner's nodes are listed too)
        mounted=$($gpfs_bin/mmlsmount $fs -L -Y 2>/dev/null | gpfs_y "" nodeIP | sort -u | grep -Fxf <(echo "$ips") | grep -c .)
        findmnt -t gpfs "$mp" >/dev/null 2>&1 && [ "$mounted" -ge "$nodes" ] && break
        stc_progress $((60 + i / 2)) "mounted on $mounted nodes"
        sleep 5
    done
    findmnt -t gpfs "$mp" >/dev/null 2>&1 || stc_fail "$fs did not mount on $mp here"
    stc_result "$(jq -cn --argjson n "${mounted:-0}" '{mounted_nodes: $n}')"
}

function do_unmount()
{
    local input=$1 remote fs forget
    remote=$(jq -r .remote_name <<<"$input")
    fs=$(jq -r .filesystem <<<"$input")
    forget=$(jq -r '.forget_cluster // false' <<<"$input")
    valid_cluster "$remote" || stc_fail "invalid cluster name"
    valid_fs "$fs" || stc_fail "invalid file system name"
    if $gpfs_bin/mmremotefs show $fs >/dev/null 2>&1; then
        stc_progress 20 "unmounting $fs on every node"
        $gpfs_bin/mmumount $fs -a </dev/null || stc_fail "mmumount $fs -a failed: something still uses it"
        $gpfs_bin/mmremotefs delete $fs </dev/null || stc_fail "mmremotefs delete $fs failed"
    else
        echo "remote file system $fs is gone already"
    fi
    if [ "$forget" = true ] && $gpfs_bin/mmremotecluster show $remote >/dev/null 2>&1; then
        stc_progress 70 "forgetting cluster $remote"
        $gpfs_bin/mmremotecluster delete $remote </dev/null || stc_fail "mmremotecluster delete $remote failed"
    fi
    stc_result '{}'
}

function do_revoke()
{
    local input=$1 remote fs forget
    remote=$(jq -r .remote_name <<<"$input")
    fs=$(jq -r .filesystem <<<"$input")
    forget=$(jq -r '.forget_cluster // false' <<<"$input")
    valid_cluster "$remote" || stc_fail "invalid cluster name"
    valid_fs "$fs" || stc_fail "invalid file system name"
    if ! $gpfs_bin/mmauth show $remote >/dev/null 2>&1; then
        echo "cluster $remote is not let in"
        stc_result '{}'
        return
    fi
    stc_progress 30 "revoking $fs from $remote"
    $gpfs_bin/mmauth deny $remote -f $fs </dev/null || echo "mmauth deny $remote -f $fs failed, going on"
    if [ "$forget" = true ]; then
        stc_progress 70 "forgetting the key of $remote"
        $gpfs_bin/mmauth delete $remote </dev/null || stc_fail "mmauth delete $remote failed"
    fi
    stc_result '{}'
}

function stc_main()
{
    local input action uuid
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    action=$(jq -r .action <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    [ -x $gpfs_bin/mmauth ] || stc_fail "GPFS is not installed here"
    stc_lock "gpfs-$uuid"
    case $action in
        key) do_key ;;
        grant|mount|unmount|revoke) do_$action "$input" ;;
        *) stc_fail "unknown action $action" ;;
    esac
}

stc_run "$@"
