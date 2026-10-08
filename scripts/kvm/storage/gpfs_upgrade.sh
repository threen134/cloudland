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
# A recovery group server of the erasure code layout (input "ece": {"recovery_group"}) is suspended in the group
# instead (mmvdisk recoverygroup change --suspend stops GPFS and suspends its pdisks, for an hour before the group
# rebuilds their data elsewhere) and resumed afterwards; it goes only when no pdisk of the group needs attention and no
# declustered array rebuilds, and the next host goes only once every pdisk is back (§7.9).
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

# local_node: the GPFS name of this node (mmgetstate answers with it when GPFS is down too)
function local_node()
{
    local me
    me=$(timeout 30 $gpfs_bin/mmgetstate -Y 2>/dev/null | gpfs_y "" nodeName | head -1)
    echo ${me:-$(hostname)}
}

# rg_not_ready <rg>: what keeps a server of the recovery group from going now: pdisks that need attention, arrays
# that rebuild (a strip may be at its fault tolerance already), or a state that can not be read (a query that fails or
# times out is no answer: taking a second server out while a strip is at its fault tolerance loses data); nothing when
# it may go. mmvdisk exits 0 with no rows when every pdisk is ok
function rg_not_ready()
{
    local rg=$1 out bad
    if ! out=$(timeout 120 $gpfs_bin/mmvdisk pdisk list --recovery-group $rg --not-ok -Y </dev/null 2>/dev/null); then
        echo "the pdisks of $rg can not be read"
        return 0
    fi
    bad=$(gpfs_y_rows pdiskSummary pdiskName state <<<"$out" | awk -F'\t' '$1 != "" {print $1 " (" $2 ")"}' | xargs)
    [ -n "$bad" ] && echo "pdisks not ok: $bad"
    if ! out=$(timeout 120 $gpfs_bin/mmvdisk recoverygroup list --recovery-group $rg --declustered-array -Y </dev/null 2>/dev/null); then
        echo "the declustered arrays of $rg can not be read"
        return 0
    fi
    bad=$(gpfs_y_rows rgDeclusteredArray declusteredArray backgroundTask <<<"$out" | awk -F'\t' 'tolower($2) ~ /rebuild/ {print $1 " (" $2 ")"}' | xargs)
    [ -n "$bad" ] && echo "rebuilding: $bad"
    return 0
}

# rg_suspended <rg>: yes / no whether a server of the recovery group is suspended, unknown when it can not be asked
# (mmvdisk asks the daemon: with GPFS down on this server the query fails, which is exactly when it is needed)
function rg_suspended()
{
    local out flag
    if ! out=$(timeout 120 $gpfs_bin/mmvdisk recoverygroup list -Y </dev/null 2>/dev/null); then
        echo unknown
        return 0
    fi
    flag=$(gpfs_y_rows rgSummary rgName suspendedServer <<<"$out" | awk -F'\t' -v rg="$1" '$1 == rg {print $2; exit}')
    case "$flag" in
        yes) echo yes ;;
        no) echo no ;;
        *) echo unknown ;;
    esac
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
    local version have users bad fs i mp rg me uuid marker state
    input=$(cat)
    rg=$(jq -r '.ece.recovery_group // ""' <<<"$input")
    [ -z "$rg" ] || [[ "$rg" =~ ^[A-Za-z][A-Za-z0-9_]{0,31}$ ]] || stc_fail "invalid recovery group name"
    uuid=$(jq -r .cluster_uuid <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    # Written before this server is suspended, removed once it is resumed: a retry after a failure in between resumes
    # it although mmvdisk can not be asked here while GPFS is down (the run directory survives a reboot)
    marker=$run_dir/storage/$uuid/rg-suspended
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
            if [ -n "$rg" ]; then
                bad=$(rg_not_ready $rg | xargs)
                [ -z "$bad" ] || stc_fail "recovery group $rg is not ready for a server to go: $bad"
                me=$(local_node)
                stc_progress 12 "suspending $me in recovery group $rg"
                mkdir -p $(dirname $marker) && echo "$rg" >$marker
                timeout 900 $gpfs_bin/mmvdisk recoverygroup change --recovery-group $rg --suspend -N $me --window 60 </dev/null ||
                    stc_fail "mmvdisk recoverygroup change --suspend -N $me failed"
                for i in $(seq 1 60); do
                    local_active || break
                    sleep 5
                done
                local_active && stc_fail "GPFS still runs here after suspending it in $rg"
            else
                timeout 600 $gpfs_bin/mmshutdown || stc_fail "mmshutdown failed"
            fi
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
        state=no
        if [ -n "$rg" ]; then
            if [ -f $marker ]; then
                state=yes
            else
                state=$(rg_suspended $rg)
            fi
        fi
        if [ "$state" = yes ]; then
            # Resuming starts GPFS here and gives the group its pdisks back
            me=$(local_node)
            timeout 900 $gpfs_bin/mmvdisk recoverygroup change --recovery-group $rg --resume -N $me </dev/null ||
                stc_fail "mmvdisk recoverygroup change --resume -N $me failed"
            rm -f $marker
        elif [ "$state" = unknown ]; then
            # Starting GPFS on a suspended server leaves its pdisks suspended; resuming one that is not suspended
            # is not known to be harmless either: the admin looks
            stc_fail "whether this server is suspended in recovery group $rg can not be told here (mmvdisk recoverygroup list on an admin host shows it); resume it with mmvdisk recoverygroup change --resume or start GPFS, then retry"
        else
            timeout 600 $gpfs_bin/mmstartup || stc_fail "mmstartup failed"
        fi
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
    if [ -n "$rg" ]; then
        # The next server goes only once the pdisks of this one are back and nothing rebuilds
        for i in $(seq 1 120); do
            bad=$(rg_not_ready $rg | xargs)
            [ -z "$bad" ] && break
            stc_progress 90 "waiting for recovery group $rg: $bad"
            sleep 15
        done
        [ -z "$bad" ] || stc_fail "recovery group $rg is not ready 30 minutes after the upgrade: $bad"
    fi
    stc_result "$(jq -cn --arg v "$(dpkg-query -W -f='${Version}' gpfs.base)" '{version: $v}')"
}

stc_run "$@"
