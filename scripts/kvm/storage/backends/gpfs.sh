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

# gpfs_debs_missing <input>: the packages of the installer (input "debs", files <package>_<version>_<arch>.deb) that are
# not installed at their version, a line each; nothing when all are. Only gpfs.base is not enough: the editions share
# its version, and a host left with the packages of a replica cluster has none of the erasure code ones
function gpfs_debs_missing()
{
    local f pkg rest ver
    for f in $(jq -r '.debs | keys[]' <<<"$1"); do
        pkg=${f%%_*}
        rest=${f#*_}
        ver=${rest%%_*}
        [ "$(dpkg-query -W -f='${Version} ${Status}' "$pkg" 2>/dev/null)" = "$ver install ok installed" ] || echo "$pkg"
    done
}

# gpfs_install_packages <input>: install GPFS from a fetched installer (gpfs_install.sh, gpfs_upgrade.sh): take the
# packages out of its tar.gz payload instead of running the self-extracting script, check them against the md5 sums of
# its manifest and install them with apt, which brings their dependencies and upgrades a release installed before.
# Input: {"sha256", "name", "payload_line", "version", "debs": {file: md5}}. Run in a job, with the gpfs-install lock
function gpfs_install_packages()
{
    local input=$1 sha name line version file tmp members f md5 have aio extra missing
    sha=$(jq -r .sha256 <<<"$input")
    name=$(jq -r .name <<<"$input")
    line=$(jq -r .payload_line <<<"$input")
    version=$(jq -r .version <<<"$input")
    [[ "$sha" =~ ^[0-9a-f]{64}$ ]] || stc_fail "invalid sha256"
    [[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || stc_fail "invalid file name"
    [[ "$line" =~ ^[0-9]+$ ]] || stc_fail "invalid payload line"
    export DEBIAN_FRONTEND=noninteractive
    apt-cache show libaio1t64 >/dev/null 2>&1 && aio=libaio1t64 || aio=libaio1
    # The erasure code packages find the disks and their enclosures with lsscsi and sg3_utils (mmdiscovercomp)
    extra=""
    jq -e '.debs | keys | any(startswith("gpfs.gnr"))' <<<"$input" >/dev/null && extra="lsscsi sg3-utils"
    if [ -z "$(gpfs_debs_missing "$input")" ]; then
        echo "the packages of GPFS $version are installed already"
    else
        file=$cache_dir/storage-pkg/$sha/$name
        [ -f $file ] || stc_fail "the installer was not fetched"
        tmp=$cache_tmp_dir/gpfs-install-$STC_RUN
        rm -rf $tmp && mkdir -p $tmp
        members=$(jq -r '.debs | keys[] | "gpfs_debs/" + .' <<<"$input")
        stc_progress 10 "taking the packages out of the installer"
        tail -n +$line $file | tar -xzf - -C $tmp $members || { rm -rf $tmp; stc_fail "the packages could not be taken out of the installer"; }
        for f in $(jq -r '.debs | keys[]' <<<"$input"); do
            md5=$(jq -r --arg f "$f" '.debs[$f]' <<<"$input")
            have=$(md5sum $tmp/gpfs_debs/$f | cut -d' ' -f1)
            [ "$have" = "$md5" ] || { rm -rf $tmp; stc_fail "$f does not match the md5 of the manifest"; }
        done
        stc_progress 40 "installing"
        apt-get -o DPkg::Lock::Timeout=600 update -q || echo "apt-get update failed, going on with the lists at hand"
        apt-get -o DPkg::Lock::Timeout=600 install -y -q build-essential "linux-headers-$(uname -r)" ksh m4 $aio python3 iputils-arping $extra \
            $tmp/gpfs_debs/*.deb || { rm -rf $tmp; stc_fail "apt-get install failed"; }
        rm -rf $tmp
    fi
    # The build tools may be missing when gpfs.base was there already (installed by hand, or a failed run)
    apt-get -o DPkg::Lock::Timeout=600 install -y -q build-essential "linux-headers-$(uname -r)" ksh m4 $aio python3 iputils-arping $extra >/dev/null ||
        stc_fail "installing the build tools failed"
    missing=$(gpfs_debs_missing "$input")
    [ -z "$missing" ] || stc_fail "not installed: $(echo $missing)"
}

# gpfs_y <section> <field>: the values of a field in the -Y output of a mm command on stdin, for the lines of a
# section (mmlscluster:clusterSummary:..., mmgetstate::...)
function gpfs_y()
{
    awk -F: -v sec="$1" -v fld="$2" '
        $2 == sec && $3 == "HEADER" { for (i = 1; i <= NF; i++) if ($i == fld) col = i; next }
        $2 == sec && col { print $col }'
}

# gpfs_y_rows <section> <field>...: per line of a section of the -Y output on stdin, the fields asked for, tab
# separated, the columns found from the HEADER line (they move between releases)
function gpfs_y_rows()
{
    local sec=$1
    shift
    awk -F: -v sec="$sec" -v want="$*" '
        BEGIN { n = split(want, w, " ") }
        $2 == sec && $3 == "HEADER" { for (i = 1; i <= NF; i++) col[$i] = i; ok = 1; next }
        $2 == sec && ok {
            line = ""
            for (j = 1; j <= n; j++) line = line (j > 1 ? "\t" : "") (col[w[j]] ? $col[w[j]] : "")
            print line
        }'
}

# gpfs_vdisksets: the vdisk sets of the erasure code layout, a line each: name, created (yes / no), file system,
# recovery groups (comma separated). Only the plain "mmvdisk vdiskset list -Y" has this summary; with --vdisk-set it
# prints other sections
function gpfs_vdisksets()
{
    $gpfs_bin/mmvdisk vdiskset list -Y </dev/null 2>/dev/null |
        awk -F: '$2 == "vdisksetSummary" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "vdisksetName") n = i; if ($i == "created") c = i; if ($i == "fsName") f = i; if ($i == "recoveryGroups") r = i }; next }
            $2 == "vdisksetSummary" && n { print $n, $c, ($f == "" ? "-" : $f), $r }'
}

# backend_health <cluster uuid> <input>: the health of the cluster as this host sees it, the JSON of
# shared-storage-design.md §14.1 on stdout. The nodes come from mmgetstate, the disks of each file system from
# mmlsdisk (availability), the file systems from this host's mount and df, the summary of the components from
# mmhealth cluster show (failed: error, degraded: warning; tips are fine). A file system not mounted here or GPFS not
# answering is an error, a node not active or a disk not up a warning.
function backend_health()
{
    local uuid=$1 input=$2 nodes disks="" fs mnt health=healthy msgs="" fsj="" mounted total free lines name state
    local failed degraded comp nodes_total nodes_active disks_total disks_up rg
    if [ ! -x $gpfs_bin/mmgetstate ]; then
        jq -cn '{health: "unknown", error: "GPFS is not installed on this host"}'
        return
    fi
    nodes=$(timeout 60 $gpfs_bin/mmgetstate -a -Y 2>/dev/null | awk -F: '
        $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "nodeName") n = i; if ($i == "state") s = i }; next }
        n && s && $n != "" { print $n "\t" $s }')
    if [ -z "$nodes" ]; then
        jq -cn '{health: "unknown", error: "mmgetstate gave no answer on this host"}'
        return
    fi
    while IFS=$'\t' read -r name state; do
        [ "$state" = active ] && continue
        msgs+="node $name is $state"$'\n'
        [ $health = healthy ] && health=warning
    done <<<"$nodes"
    while IFS=$'\t' read -r fs mnt; do
        [[ "$fs" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] || continue
        lines=$(timeout 60 $gpfs_bin/mmlsdisk $fs -Y 2>/dev/null | awk -F: '
            $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "nsdName") n = i; if ($i == "availability") a = i }; next }
            n && a && $n != "" { print $n "\t" $a }')
        disks+="$lines"$'\n'
        while IFS=$'\t' read -r name state; do
            [ -z "$name" ] || [ "$state" = up ] && continue
            msgs+="disk $name of $fs is $state"$'\n'
            [ $health = healthy ] && health=warning
        done <<<"$lines"
        mounted=false total=0 free=0
        if [ -n "$mnt" ] && [ "$(timeout 10 stat -f -c %T "$mnt" 2>/dev/null)" = gpfs ]; then
            mounted=true
            read -r total free < <(timeout 10 df -B1 --output=size,avail "$mnt" 2>/dev/null | awk 'NR == 2 {print $1, $2}')
        else
            msgs+="$fs is not mounted on $(hostname -s)"$'\n'
            health=error
        fi
        fsj+=$(jq -cn --arg n "$fs" --argjson m $mounted --argjson t "${total:-0}" --argjson f "${free:-0}" \
            '{name: $n, mounted: $m, total: $t, free: $f}')$'\n'
    done < <(jq -r '.filesystems[]? | [.name, .mount] | @tsv' <<<"$input")
    # The erasure code layout: the pdisks of the recovery group are the disks of the cluster, by name. A pdisk that is
    # not ok (diagnosing, missing, draining...) is a warning: the code keeps the data readable while it rebuilds
    rg=$(jq -r '.recovery_group // empty' <<<"$input")
    if [[ "$rg" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]]; then
        lines=$(timeout 60 $gpfs_bin/mmvdisk pdisk list --recovery-group $rg -Y </dev/null 2>/dev/null | awk -F: '
            $2 == "pdiskSummary" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "pdiskName") n = i; if ($i == "state") s = i }; next }
            $2 == "pdiskSummary" && n && s && $n != "" { print $n "\t" ($s == "ok" ? "up" : $s) }')
        if [ -z "$lines" ]; then
            msgs+="recovery group $rg gave no pdisks"$'\n'
            [ $health = healthy ] && health=warning
        fi
        disks+="$lines"$'\n'
        while IFS=$'\t' read -r name state; do
            [ -z "$name" ] || [ "$state" = up ] && continue
            msgs+="pdisk $name of $rg is $state"$'\n'
            [ $health = healthy ] && health=warning
        done <<<"$lines"
    fi
    # The summary of mmhealth: a component with failed entities is an error, with degraded ones a warning
    while IFS=$'\t' read -r comp failed degraded; do
        [ -z "$comp" ] && continue
        if [ "${failed:-0}" -gt 0 ]; then
            msgs+="mmhealth: $comp has $failed failed"$'\n'
            health=error
        elif [ "${degraded:-0}" -gt 0 ]; then
            msgs+="mmhealth: $comp has $degraded degraded"$'\n'
            [ $health = healthy ] && health=warning
        fi
    done < <(timeout 60 $gpfs_bin/mmhealth cluster show -Y 2>/dev/null | awk -F: '
        $2 == "Summary" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "component") c = i; if ($i == "failed") f = i; if ($i == "degraded") d = i }; next }
        $2 == "Summary" && c { print $c "\t" $f "\t" $d }')
    nodes_total=$(grep -c . <<<"$nodes"); nodes_active=$(awk -F'\t' '$2 == "active"' <<<"$nodes" | grep -c .)
    disks_total=$(grep -c . <<<"$disks"); disks_up=$(awk -F'\t' '$2 == "up"' <<<"$disks" | grep -c .)
    jq -n --arg h $health --arg s "$nodes_active/$nodes_total nodes active, $disks_up/$disks_total disks up" \
        --arg msgs "$msgs" --arg nodes "$nodes" --arg disks "$disks" --arg fs "$fsj" '{
        health: $h, summary: $s,
        messages: ($msgs | split("\n") | map(select(. != ""))),
        nodes: ($nodes | split("\n") | map(select(. != "") | split("\t") | {name: .[0], state: .[1]})),
        disks: ($disks | split("\n") | map(select(. != "") | split("\t") | {name: .[0], state: .[1]})),
        filesystems: ($fs | split("\n") | map(select(. != "") | fromjson))}'
}

# Metrics of this host for the node_exporter textfile collector, printed by stc_metrics.sh (shared-storage-design.md
# §14.3, appendix E): whether GPFS is active here, each file system of the cluster mounted here or not with its
# capacity, the I/O counters of this host (mmpmon fs_io_s; they start again from 0 when GPFS restarts, so they are
# only read as rates) and the state of each mmhealth component of this host. The file systems come from mmlsfs and
# are kept in gpfs_mounts for the times GPFS does not answer: a file system that is not mounted is not in
# /proc/mounts at all.
function backend_metrics()
{
    local l="cluster=\"$1\"" dir=$run_dir/storage/$1 state list fs mnt mounted
    [ -x $gpfs_bin/mmgetstate ] || return 0
    state=$(timeout 20 $gpfs_bin/mmgetstate -Y 2>/dev/null | awk -F: '
        $3 == "HEADER" { for (i = 1; i <= NF; i++) if ($i == "state") s = i; next }
        s { print $s; exit }')
    echo "cloudland_gpfs_node_active{$l} $([ "$state" = active ] && echo 1 || echo 0)"
    list=$(timeout 20 $gpfs_bin/mmlsfs all -T -Y 2>/dev/null | awk -F: '
        $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "deviceName") d = i; if ($i == "data") v = i }; next }
        d && v && $d != "" { print $d "\t" $v }')
    if [ -n "$list" ]; then
        # The mount point comes percent encoded (%2Fgpfs%2Ffs1)
        list=$(while IFS=$'\t' read -r fs mnt; do printf '%s\t%b\n' "$fs" "$(sed 's/%/\\x/g' <<<"$mnt")"; done <<<"$list")
        printf '%s\n' "$list" >$dir/gpfs_mounts.tmp && mv -f $dir/gpfs_mounts.tmp $dir/gpfs_mounts
    else
        list=$(cat $dir/gpfs_mounts 2>/dev/null)
    fi
    while IFS=$'\t' read -r fs mnt; do
        [[ "$fs" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] && [[ "$mnt" == /* ]] || continue
        mounted=0
        [ "$(timeout 10 stat -f -c %T "$mnt" 2>/dev/null)" = gpfs ] && mounted=1
        echo "cloudland_gpfs_filesystem_mounted{$l,fs=\"$fs\"} $mounted"
        [ $mounted = 1 ] || continue
        timeout 10 df -B1 --output=size,avail "$mnt" 2>/dev/null | awk -v l="$l,fs=\"$fs\"" 'NR == 2 {
            print "cloudland_gpfs_filesystem_total_bytes{" l "} " $1
            print "cloudland_gpfs_filesystem_free_bytes{" l "} " $2 }'
    done <<<"$list"
    [ "$state" = active ] || return 0
    # Pairs of _name_ value: _fs_ the file system, _br_ / _bw_ bytes read / written, _rdc_ / _wc_ read / write calls
    timeout 20 $gpfs_bin/mmpmon -p -s <<<"fs_io_s" 2>/dev/null | awk -v l="$l" '$1 == "_fs_io_s_" {
        delete v
        for (i = 2; i < NF; i += 2) v[$i] = $(i + 1)
        if (v["_rc_"] != "0" || v["_fs_"] !~ /^[A-Za-z][A-Za-z0-9_]*$/) next
        f = l ",fs=\"" v["_fs_"] "\""
        print "cloudland_gpfs_read_bytes_total{" f "} " v["_br_"] + 0
        print "cloudland_gpfs_write_bytes_total{" f "} " v["_bw_"] + 0
        print "cloudland_gpfs_reads_total{" f "} " v["_rdc_"] + 0
        print "cloudland_gpfs_writes_total{" f "} " v["_wc_"] + 0 }'
    # One row per component of this host (entity type NODE); tips are fine
    timeout 30 $gpfs_bin/mmhealth node show -Y 2>/dev/null | awk -F: -v l="$l" '
        $2 == "State" && $3 == "HEADER" { for (i = 1; i <= NF; i++) { if ($i == "component") c = i; if ($i == "entitytype") e = i; if ($i == "status") s = i }; next }
        $2 == "State" && c && $e == "NODE" && $c ~ /^[A-Z_]+$/ {
            print "cloudland_gpfs_component_healthy{" l ",component=\"" tolower($c) "\"} " (($s == "HEALTHY" || $s == "TIPS") ? 1 : 0) }'
}
