#!/bin/bash
# Upgrade GPFS on this host to the release of a fetched installer, one host of the cluster after the other
# (shared-storage-design.md §7.7). clapi moved the instances that use the cluster off first (drain); this looks again,
# one may have been started here since. Input: what gpfs_install.sh takes, plus {"cluster_uuid", "filesystems"}.
#   1. no running domain has a disk in a file system of the cluster, and every disk of the file systems is up: with two
#      copies, the disks of this host may hold the only copy left while a disk of another host is down
#   2. the manager roles of this node go to another node (move_managers), GPFS stops here (mmshutdown), the packages
#      of the new release are installed, the portability layer is built
#      again for the running kernel (the modules of the release before do not load with the new one)
#   3. GPFS starts (mmstartup) and is active, the file systems mount, the disks that went down while it was stopped are
#      started (mmchdisk start, which also brings their data up to date) and every disk is up before the next host goes
# A host on the new release already (a retry) only does 3. Result: {"version"}

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/gpfs.sh
STC_SCRIPT=$(readlink -f $0)

input=""

# mount_points: the mount points of the file systems of the cluster, decoded from the -Y output (%2F for /)
function mount_points()
{
    local fs
    for fs in $(jq -r '.filesystems[]?' <<<"$input"); do
        timeout 60 $gpfs_bin/mmlsfs $fs -T -Y 2>/dev/null | gpfs_y "" data | head -1 | sed 's/%/\\x/g' | xargs -0 printf '%b'
    done
}

# local_users: the running domains of this host with a disk under one of the mount points
function local_users()
{
    local dom mps mp
    mps=$(mount_points)
    [ -n "$mps" ] || return 0
    for dom in $(timeout 30 virsh list --name 2>/dev/null); do
        for mp in $mps; do
            if timeout 30 virsh dumpxml $dom 2>/dev/null | grep -qE "(file|dev)='$mp/"; then
                echo $dom
                break
            fi
        done
    done
}

# disks_not_up: the disks of the file systems whose availability is not up or whose status is not ready
function disks_not_up()
{
    local fs
    for fs in $(jq -r '.filesystems[]?' <<<"$input"); do
        timeout 60 $gpfs_bin/mmlsdisk $fs -Y 2>/dev/null | awk -F: -v fs=$fs '
            $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "nsdName") n = i; if ($i == "status") s = i; if ($i == "availability") a = i }; next }
            n && ($a != "up" || $s != "ready") { print fs "/" $n " (" $s ", " $a ")" }'
    done
}

function local_active()
{
    [ "$(timeout 30 $gpfs_bin/mmgetstate -Y 2>/dev/null | gpfs_y "" state | head -1)" = "active" ]
}

# move_managers: hand the file system manager and cluster manager roles of this node to another active quorum node
# before GPFS stops here. The node that leaves is recovered only after its lease ran out (about a minute); while the
# file system manager is the one waiting, writes that need a block stall that long on every node (TC-24 UPG-01)
function move_managers()
{
    local me quorum other fs
    me=$(timeout 30 $gpfs_bin/mmgetstate -Y 2>/dev/null | gpfs_y "" nodeName | head -1)
    # Asked of the daemon, not over ssh: only the admin hosts have the key of the cluster (mmgetstate -a from another
    # host reports every node unknown). Active: has a file system mounted; the cluster manager has to be a quorum node
    quorum=$(timeout 60 $gpfs_bin/mmlscluster -Y 2>/dev/null | awk -F: '
        $2 == "clusterNode" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "daemonNodeName") n = i; if ($i == "designation") d = i }; next }
        $2 == "clusterNode" && n && $d ~ /quorum/ { print $n }' | xargs)
    other=$(timeout 60 $gpfs_bin/mmlsmount all -L -Y 2>/dev/null | awk -F: -v me="$me" -v quorum=" $quorum " '
        $3 == "HEADER" { for (i = 1; i <= NF; i++) if ($i == "nodeName") n = i; next }
        n && $n != me && index(quorum, " " $n " ") { print $n; exit }')
    if [ -z "$me" ] || [ -z "$other" ]; then
        echo "no other active quorum node to take the managers"
        return 0
    fi
    for fs in $(timeout 60 $gpfs_bin/mmlsmgr -Y 2>/dev/null | awk -F: -v me="$me" '
        $2 == "filesystemManager" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "filesystem") f = i; if ($i == "manager") m = i }; next }
        $2 == "filesystemManager" && f && $m == me { print $f }'); do
        echo "moving the manager of $fs to $other"
        timeout 300 $gpfs_bin/mmchmgr $fs $other || echo "mmchmgr $fs $other failed, going on"
    done
    if [ "$(timeout 60 $gpfs_bin/mmlsmgr -Y 2>/dev/null | gpfs_y clusterManager manager | head -1)" = "$me" ]; then
        echo "moving the cluster manager to $other"
        timeout 300 $gpfs_bin/mmchmgr -c $other || echo "mmchmgr -c $other failed, going on"
    fi
}

function stc_main()
{
    local version have users bad fs i mp
    input=$(cat)
    valid_uuid "$(jq -r .cluster_uuid <<<"$input")" || stc_fail "invalid cluster uuid"
    for fs in $(jq -r '.filesystems[]?' <<<"$input"); do
        [[ "$fs" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] || stc_fail "invalid file system $fs"
    done
    version=$(jq -r .version <<<"$input")
    stc_lock "gpfs-install"
    [ -x $gpfs_bin/mmgetstate ] || stc_fail "GPFS is not installed here"
    have=$(dpkg-query -W -f='${Version}' gpfs.base 2>/dev/null)
    if [ "$have" != "$version" ]; then
        # GPFS down here already: a run before stopped it and failed later; nothing to check through it
        if local_active; then
            users=$(local_users | xargs)
            [ -z "$users" ] || stc_fail "instances still run here on the storage of the cluster: $users (move them off, then retry)"
            bad=$(disks_not_up | xargs)
            [ -z "$bad" ] || stc_fail "disks not up: $bad; stopping GPFS here now could take the only copy of some data away"
            stc_progress 10 "stopping GPFS $have"
            move_managers
            timeout 600 $gpfs_bin/mmshutdown || stc_fail "mmshutdown failed"
        fi
        stc_progress 20 "installing GPFS $version"
        gpfs_install_packages "$input"
        stc_progress 50 "building the portability layer for $(uname -r)"
        timeout 1800 $gpfs_bin/mmbuildgpl || stc_fail "mmbuildgpl failed for kernel $(uname -r)"
        touch $run_dir/storage/gpfs.cloudland
    else
        echo "gpfs.base $version is installed already"
    fi
    if ! local_active; then
        stc_progress 70 "starting GPFS"
        timeout 600 $gpfs_bin/mmstartup || stc_fail "mmstartup failed"
        for i in $(seq 1 120); do
            local_active && break
            sleep 5
        done
        local_active || stc_fail "GPFS did not become active in 10 minutes"
    fi
    for fs in $(jq -r '.filesystems[]?' <<<"$input"); do
        mp=$(timeout 60 $gpfs_bin/mmlsfs $fs -T -Y 2>/dev/null | gpfs_y "" data | head -1 | sed 's/%/\\x/g' | xargs -0 printf '%b')
        [ -n "$mp" ] || stc_fail "the mount point of $fs is not known"
        findmnt -t gpfs "$mp" >/dev/null 2>&1 || timeout 300 $gpfs_bin/mmmount $fs || stc_fail "mounting $fs failed"
        for i in $(seq 1 60); do
            findmnt -t gpfs "$mp" >/dev/null 2>&1 && break
            sleep 5
        done
        findmnt -t gpfs "$mp" >/dev/null 2>&1 || stc_fail "$fs did not mount on $mp"
        if [ -n "$(disks_not_up | grep "^$fs/")" ]; then
            stc_progress 85 "starting the disks of $fs"
            # Synchronous: brings the data written while the disks were down up to date, which may take a while
            $gpfs_bin/mmchdisk $fs start -a || stc_fail "mmchdisk $fs start failed"
        fi
    done
    bad=$(disks_not_up | xargs)
    [ -z "$bad" ] || stc_fail "disks not up after the upgrade: $bad"
    stc_result "$(jq -cn --arg v "$(dpkg-query -W -f='${Version}' gpfs.base)" '{version: $v}')"
}

stc_run "$@"
