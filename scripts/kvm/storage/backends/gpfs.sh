# -*- mode: sh -*-
# Node side of the GPFS storage backend (shared-storage-design.md §4.5). Sourced by the step scripts with
# backend_load; defines the hooks every backend file defines, plus helpers of the gpfs_*.sh scripts.

gpfs_bin=/usr/lpp/mmfs/bin
# CloudLand installed the GPFS packages of this host (gpfs_install.sh); they stay when a cluster is deleted without
# purging them, and are not somebody else's then
gpfs_ours=$run_dir/storage/gpfs.cloudland

# backend_existing prints why the host already has GPFS that CloudLand did not set up, nothing when it has none
function backend_existing()
{
    if [ -f /var/mmfs/gen/mmsdrfs ]; then
        echo "the host belongs to a GPFS cluster"
    elif dpkg -s gpfs.base >/dev/null 2>&1 && [ ! -f $gpfs_ours ]; then
        echo "gpfs.base is installed"
    fi
}

# backend_trust_written <cluster dir>: the remote shell wrappers GPFS runs its admin commands with (mmcrcluster -r /
# -R): ssh and scp with the key and known_hosts of the cluster, never root's own. Written on every member, so the
# paths exist wherever GPFS looks; only the admin hosts have the key
function backend_trust_written()
{
    local dir=$1 opts
    opts="-i $dir/id_ed25519 -o UserKnownHostsFile=$dir/known_hosts -o StrictHostKeyChecking=yes -o BatchMode=yes -o ConnectTimeout=10"
    printf '#!/bin/bash\nexec /usr/bin/ssh %s "$@"\n' "$opts" >$dir/gpfs_rsh
    printf '#!/bin/bash\nexec /usr/bin/scp %s "$@"\n' "$opts" >$dir/gpfs_rcp
    chmod 700 $dir/gpfs_rsh $dir/gpfs_rcp
}

# backend_disks_resolved <cluster uuid> <stable id>...: the nsddevices user exit lists the disks of the cluster
# only, resolved by stable id each time GPFS looks, so it never sees another disk (a local pool's) and a device
# renamed at boot is still found (§6.4)
function backend_disks_resolved()
{
    local uuid=$1 id f=/var/mmfs/etc/nsddevices
    shift
    mkdir -p /var/mmfs/etc
    {
        echo "#!/bin/ksh"
        echo "# CloudLand: NSD disks of storage cluster $uuid"
        for id in "$@"; do
            case "$id" in
                loop:*) echo "d=\$(losetup -j '${id#loop:}' -nO NAME | head -1); [ -n \"\$d\" ] && echo \"\${d#/dev/} generic\"" ;;
                dev:*) echo "echo '${id#dev:} generic'" ;;
                *) echo "d=\$(readlink -f '/dev/disk/by-id/$id'); [ -b \"\$d\" ] && echo \"\${d#/dev/} generic\"" ;;
            esac
        done
        echo "return 0"
    } >$f.cl-new
    chmod 755 $f.cl-new
    mv -f $f.cl-new $f
}

# backend_leave <cluster uuid> <purge true|false>: what is left of GPFS on a host whose cluster is deleted
function backend_leave()
{
    local uuid=$1 purge=$2
    # A host that never joined (its deployment stopped at the precheck) may run the GPFS of somebody else: the
    # daemon, the cluster configuration and the packages are left alone. The join step writes the mark first
    if [ ! -f $run_dir/storage/$uuid/member ]; then
        echo "this host never joined storage cluster $uuid: its GPFS is left alone"
        return 0
    fi
    if pgrep -x mmfsd >/dev/null; then
        $gpfs_bin/mmshutdown >/dev/null 2>&1 || pkill -x mmfsd
    fi
    grep -q "storage cluster $uuid" /var/mmfs/etc/nsddevices 2>/dev/null && rm -f /var/mmfs/etc/nsddevices
    rm -f /var/mmfs/gen/mmsdrfs
    if [ "$purge" = "true" ]; then
        modprobe -r mmfs26 mmfslinux tracedev 2>/dev/null
        DEBIAN_FRONTEND=noninteractive dpkg --purge $(dpkg-query -W -f='${Package}\n' 'gpfs.*' 2>/dev/null) >/dev/null 2>&1
        rm -rf /usr/lpp/mmfs /var/mmfs /var/adm/ras/mmfs* /lib/modules/*/extra/mmfs26.ko /lib/modules/*/extra/mmfslinux.ko /lib/modules/*/extra/tracedev.ko
        depmod -a 2>/dev/null
        rm -f $gpfs_ours
    fi
    return 0
}

# gpfs_nsd_exists <name>: whether an NSD of that name is defined. mmlsnsd -d answers 0 for a name it does not know
# (it only prints "No disks were found"), so the names are read from the list
function gpfs_nsd_exists()
{
    $gpfs_bin/mmlsnsd -X 2>/dev/null | awk '{print $1}' | grep -qx "$1"
}

# gpfs_y <section> <field>: the values of a field in the -Y output of a mm command on stdin, for the lines of a
# section (mmlscluster:clusterSummary:..., mmgetstate::...)
function gpfs_y()
{
    awk -F: -v sec="$1" -v fld="$2" '
        $2 == sec && $3 == "HEADER" { for (i = 1; i <= NF; i++) if ($i == fld) col = i; next }
        $2 == sec && col { print $col }'
}
