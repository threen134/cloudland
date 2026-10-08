#!/bin/bash
# Keep the daemons of the managed Ceph clusters of this host alive when its memory runs out
# (shared-storage-design.md §6.7.2): the processes of the container of a mon, mgr or OSD get the oom_score_adj, so the
# kernel kills the VMs first, and the container the memory.low of its kind of daemon, so reclaim leaves it what the
# host holds back for it. cephadm runs the daemon under docker, outside the cgroup of its unit, where OOMScoreAdjust
# and MemoryLow of the unit do not reach it.
#
#   ceph_oom_protect.sh <cluster uuid> <daemon> <oom_score_adj> <mon bytes> <mgr bytes> <osd bytes> [unit main pid]
#       one daemon, waiting up to 3 minutes for its container. Started after every start of a unit (drop-in
#       cloudland-oom.conf, through systemd-run: the unit does not wait for it) and by ceph_unit_oom for the daemons
#       running already
#   ceph_oom_protect.sh --sweep
#       every daemon of every cluster with the drop-in, put right where it is not (the heartbeat, every 5 minutes,
#       for a start the first form missed); no waiting
#
# Both then set the memory.low of system.slice to the sum over the daemons deployed on this host: a cgroup is only
# protected as far as every cgroup above it is, and system.slice, where docker puts the containers, has none.
# Prints a line per daemon it changed.

dropins=/etc/systemd/system
uuid_re='^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'

# low_of <daemon> <mon bytes> <mgr bytes> <osd bytes>: the memory.low of a kind of daemon; nothing for the others
# (crash and the like, as killable as anything else)
function low_of()
{
    case "$1" in
        mon.*) echo $2 ;;
        mgr.*) echo $3 ;;
        osd.*) echo $4 ;;
    esac
}

# The container of a daemon: cephadm names the one of mgr.a.b ceph-<fsid>-mgr-a-b (an OSD has an ...-activate
# container before it). Prints "<running> <pid>"
function container_state()
{
    timeout 10 docker inspect -f '{{.State.Running}} {{.State.Pid}}' "ceph-$1-${2//./-}" 2>/dev/null
}

function cgroup_of()
{
    echo /sys/fs/cgroup$(awk -F: '$1 == "0" {print $3}' /proc/$1/cgroup 2>/dev/null)
}

# protected <pid> <adj> <low>: every process of the container of pid has adj and the container has low
function protected()
{
    local cg p
    cg=$(cgroup_of $1)
    [ -f $cg/cgroup.procs ] || return 1
    [ "$(cat $cg/memory.low 2>/dev/null)" = "$3" ] || return 1
    for p in $(cat $cg/cgroup.procs); do
        [ "$(cat /proc/$p/oom_score_adj 2>/dev/null)" = "$2" ] || return 1
    done
}

# apply <uuid> <daemon> <pid> <adj> <low>
function apply()
{
    local uuid=$1 daemon=$2 pid=$3 adj=$4 low=$5 cg p round bad=0 have
    cg=$(cgroup_of $pid)
    [ -f "$cg/cgroup.procs" ] || { echo "$daemon: no cgroup v2 of its container"; return 1; }
    # docker-init and the daemon: read the processes again, one forked while the list was read inherits the old value
    for round in 1 2 3; do
        for p in $(cat $cg/cgroup.procs); do
            echo "$adj" >/proc/$p/oom_score_adj 2>/dev/null
        done
        [ $round -lt 3 ] && sleep 1
    done
    # Through docker, which hands it to systemd (the scope of the container is a unit of its): written into the
    # cgroup directly, systemd puts its own value back the next time it applies the settings of the unit
    # (daemon-reload)
    timeout 30 docker update --memory-reservation "$low" "ceph-$uuid-${daemon//./-}" >/dev/null 2>&1 || echo "$low" >$cg/memory.low 2>/dev/null
    for p in $(cat $cg/cgroup.procs); do
        [ "$(cat /proc/$p/oom_score_adj 2>/dev/null)" = "$adj" ] || bad=$((bad + 1))
    done
    have=$(cat $cg/memory.low 2>/dev/null)
    echo "$daemon: oom_score_adj $adj ($bad processes missed), memory.low $have (wanted $low)"
    [ $bad -eq 0 ] && [ "$have" = "$low" ]
}

# The clusters with the drop-in on this host: "<uuid> <adj> <mon> <mgr> <osd>" per line, from its ExecStartPost line
function clusters()
{
    local f
    for f in $dropins/ceph-*@.service.d/cloudland-oom.conf; do
        [ -f "$f" ] || continue
        awk '/^ExecStartPost=/ {for (i = 1; i <= NF; i++) if ($i == "%i") {print $(i - 1), $(i + 1), $(i + 2), $(i + 3), $(i + 4); exit}}' "$f"
    done
}

# slice_low: memory.low of system.slice = the sum over the daemons deployed on this host (cephadm keeps a directory
# per daemon), so it covers what the containers claim and leaves little for the other services of the slice
function slice_low()
{
    local uuid adj mon mgr osd d total=0 have l
    while read -r uuid adj mon mgr osd <&3; do
        [[ "$uuid" =~ $uuid_re ]] || continue
        for d in /var/lib/ceph/$uuid/*; do
            [ -d "$d" ] || continue
            l=$(low_of "$(basename "$d")" "$mon" "$mgr" "$osd")
            [[ "$l" =~ ^[0-9]+$ ]] && total=$((total + l))
        done
    done 3< <(clusters)
    have=$(systemctl show -p MemoryLow --value system.slice 2>/dev/null)
    [ "$have" = "$total" ] && return 0
    timeout 30 systemctl set-property system.slice MemoryLow=$total || { echo "setting the memory.low of system.slice failed"; return 1; }
    echo "system.slice: memory.low $total"
}

if [ "$1" = "--sweep" ]; then
    exec 9>/var/lock/cloudland-ceph-oom.lock
    flock -n 9 || exit 0
    while read -r uuid adj mon mgr osd <&3; do
        [[ "$uuid" =~ $uuid_re ]] && [[ "$adj" =~ ^-?[0-9]{1,4}$ ]] || continue
        for d in /var/lib/ceph/$uuid/*; do
            daemon=$(basename "$d")
            low=$(low_of "$daemon" "$mon" "$mgr" "$osd")
            [[ "$low" =~ ^[0-9]+$ ]] || continue
            read -r running pid < <(container_state $uuid $daemon)
            [ "$running" = true ] && [ "${pid:-0}" -gt 0 ] || continue
            protected $pid $adj $low || apply $uuid $daemon $pid $adj $low
        done
    done 3< <(clusters)
    slice_low
    exit 0
fi

uuid=$1 daemon=$2 adj=$3 mainpid=$7
[[ "$uuid" =~ $uuid_re ]] || { echo "invalid cluster uuid"; exit 1; }
[[ "$adj" =~ ^-?[0-9]{1,4}$ ]] && [ "$adj" -ge -1000 ] && [ "$adj" -le 1000 ] || { echo "invalid oom_score_adj $adj"; exit 1; }
low=$(low_of "$daemon" "$4" "$5" "$6")
[ -n "$low" ] || exit 0
[[ "$daemon" =~ ^[a-z]+\.[A-Za-z0-9._-]+$ ]] || { echo "invalid daemon $daemon"; exit 1; }
[[ "$low" =~ ^[0-9]{1,15}$ ]] || { echo "invalid memory.low $low"; exit 1; }
[ -z "$mainpid" ] || [[ "$mainpid" =~ ^[0-9]+$ ]] || mainpid=

pid=0
for i in $(seq 1 180); do
    read -r running pid < <(container_state $uuid $daemon)
    [ "$running" = true ] && [ "${pid:-0}" -gt 0 ] && break
    pid=0
    # Started for a unit: give up when its own process (docker run) ended, the container will not come
    if [ -n "$mainpid" ] && ! kill -0 "$mainpid" 2>/dev/null; then
        echo "$daemon: the unit stopped before its container ran"
        exit 1
    fi
    sleep 1
done
[ "$pid" -gt 0 ] || { echo "$daemon: its container did not run in 3 minutes"; exit 1; }
apply $uuid $daemon $pid $adj $low
rc=$?
exec 9>/var/lock/cloudland-ceph-oom.lock
flock -w 30 9 && slice_low
exit $rc
