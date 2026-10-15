#!/bin/bash
# sandbox tests of the shared disk layout of GPFS (shared-storage-design.md §7.10) with stub commands: NSDs of shared
# LUNs with a server list, new server lists (mmchnsd, left alone when GPFS has them), a shared LUN released or left
# without wiping it, the nsddevices exit naming a multipath device dmm, and the disk scan taking a path of a multipath
# device for in use and the multipath device for a shared LUN; the precheck of a shared LUN
S=/opt/cloudland/scripts/kvm/storage
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
uuid=aaaaaaaa-1616-2222-3333-444444444444
fake=$(mktemp -d)
export FAKE=$fake

cat >$fake/mmlsnsd <<'EOF'
#!/bin/bash
echo "mmlsnsd::HEADER:version:reserved:reserved:diskName:volumeId:localDiskName:devtype:nodeName:remarks:"
[ -f $FAKE/nsd_rows ] && cat $FAKE/nsd_rows
exit 0
EOF
cat >$fake/mmchnsd <<'EOF'
#!/bin/bash
echo "mmchnsd $*" >>$FAKE/calls
cat $2 >>$FAKE/stanzas
EOF
cat >$fake/mmcrnsd <<'EOF'
#!/bin/bash
echo "mmcrnsd $*" >>$FAKE/calls
cat $2 >>$FAKE/stanzas
while read -r _ dev nsd rest; do echo "${nsd#nsd=}" >>$FAKE/made; done <$2
EOF
chmod +x $fake/*

run() {
    local file=$1 input=$2
    (
        cd $S
        source <(sed -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' $file)
        gpfs_bin=$fake
        stc_progress() { echo "PROGRESS $*"; }
        stc_result() { echo "RESULT $*"; }
        stc_fail() { echo "FAILED $*"; exit 1; }
        stc_lock() { :; }
        gpfs_nsd_exists() { grep -qx "$1" $FAKE/made 2>/dev/null; }
        getent() { echo "$2"; }   # node names are addresses here
        stc_main <<<"$input"
    )
}

# ---- NSDs of shared LUNs: the server list goes into the stanza ----
: >$fake/calls; : >$fake/stanzas; : >$fake/made
out=$(run gpfs_nsd.sh '{"action":"create","cluster_uuid":"'$uuid'","nsds":[{"name":"cl4s1","device":"/dev/mapper/mpathb","server":"10.0.0.2,10.0.0.3,10.0.0.1","failure_group":1,"usage":"dataAndMetadata","pool":"system"}]}')
grep -qx "%nsd: device=/dev/mapper/mpathb nsd=cl4s1 servers=10.0.0.2,10.0.0.3,10.0.0.1 usage=dataAndMetadata failureGroup=1 pool=system" $fake/stanzas &&
    grep -q '^RESULT' <<<"$out" && ok "create: a shared LUN's NSD with its server list" || ko "create: $out / $(cat $fake/stanzas)"

# ---- new server lists ----
: >$fake/calls; : >$fake/stanzas
printf 'mmlsnsd::0:1:::cl4s1:AAA:dm-3:dmm:10.0.0.2::\nmmlsnsd::0:1:::cl4s1:AAA:dm-3:dmm:10.0.0.3::\nmmlsnsd::0:1:::cl4s1:AAA:dm-4:dmm:10.0.0.1::\n' >$fake/nsd_rows
echo cl4s2 >>$fake/made
out=$(run gpfs_nsd.sh '{"action":"servers","cluster_uuid":"'$uuid'","nsds":[{"name":"cl4s1","servers":"10.0.0.2,10.0.0.3,10.0.0.4,10.0.0.1"},{"name":"cl4s2","servers":"10.0.0.1"}]}')
grep -qx "%nsd: nsd=cl4s1 servers=10.0.0.2,10.0.0.3,10.0.0.4,10.0.0.1" $fake/stanzas && grep -qx "%nsd: nsd=cl4s2 servers=10.0.0.1" $fake/stanzas &&
    grep -q '^mmchnsd -F' $fake/calls && grep -q '^RESULT {"changed":2}' <<<"$out" &&
    ok "servers: mmchnsd with the new lists" || ko "servers: $out / $(cat $fake/stanzas)"
: >$fake/calls; : >$fake/stanzas
out=$(run gpfs_nsd.sh '{"action":"servers","cluster_uuid":"'$uuid'","nsds":[{"name":"cl4s1","servers":"10.0.0.3,10.0.0.1,10.0.0.2"}]}')
! grep -q mmchnsd $fake/calls && grep -q "NSD cl4s1 is served by" <<<"$out" && grep -q '^RESULT {"changed":0}' <<<"$out" &&
    ok "servers: a list GPFS has already (in any order) is left alone" || ko "same list: $out"
out=$(run gpfs_nsd.sh '{"action":"servers","cluster_uuid":"'$uuid'","nsds":[{"name":"cl4s9","servers":"10.0.0.1"}]}')
grep -q '^FAILED NSD cl4s9 does not exist' <<<"$out" && ok "servers: an NSD that is not there fails" || ko "missing: $out"
out=$(run gpfs_nsd.sh '{"action":"servers","cluster_uuid":"'$uuid'","nsds":[{"name":"cl4s1","servers":"10.0.0.1;reboot"}]}')
grep -q '^FAILED invalid server list' <<<"$out" && ok "servers: a bad list refused" || ko "bad list: $out"
out=$(run gpfs_nsd.sh '{"action":"servers","cluster_uuid":"'$uuid'","nsds":[]}')
grep -q '^RESULT {"changed":0}' <<<"$out" && ok "servers: nothing to change" || ko "empty: $out"

# ---- a shared LUN released or left without wiping ----
relrun() {
    local file=$1 input=$2
    (
        cd $S
        source <(sed -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' $file)
        stc_progress() { :; }
        stc_result() { echo "RESULT $*"; }
        stc_fail() { echo "FAILED $*"; exit 1; }
        stc_lock() { :; }
        backend_load() { return 1; }
        disk_identity() { identity_path=/dev/fake-$1; return 0; }
        lsblk() { :; }
        wipe_disk() { echo "WIPE $1"; }
        rm() { :; }
        stc_main <<<"$input"
    )
}
out=$(relrun stc_release_disks.sh '{"cluster_uuid":"'$uuid'","kind":"gpfs","keep":[],"release":[{"id":"wwn-a","size_bytes":1,"shared":true},{"id":"wwn-b","size_bytes":1}]}')
! grep -q "WIPE /dev/fake-wwn-a" <<<"$out" && grep -q "WIPE /dev/fake-wwn-b" <<<"$out" && grep -q "not wiping wwn-a: a shared LUN another host wipes" <<<"$out" &&
    ok "release: a shared LUN another host wipes is left alone" || ko "release: $out"
out=$(relrun stc_leave.sh '{"cluster_uuid":"'$uuid'","kind":"gpfs","wipe":[{"id":"wwn-a","size_bytes":1,"shared":true},{"id":"wwn-c","size_bytes":1}],"purge":false}')
! grep -q "WIPE /dev/fake-wwn-a" <<<"$out" && grep -q "WIPE /dev/fake-wwn-c" <<<"$out" &&
    ok "leave: a shared LUN another host keeps is not wiped" || ko "leave: $out"

# ---- resolve: "in_use" is identity only (a LUN the cluster uses, or one another host checks), never wiped ----
# (review 2026-10-07: the script did not read it, a new server of a live LUN failed "has data on it" or wiped it)
ruuid=bbbbbbbb-1616-2222-3333-555555555555
rdir=$(bash -c "source $S/../../cloudrc >/dev/null 2>&1; echo \$run_dir")/storage/$ruuid
rm -rf $rdir
resrun() {
    (
        cd $S
        source <(sed -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' stc_resolve_disks.sh)
        stc_lock() { :; }
        system_disks() { :; }
        backend_load() { return 0; }
        disk_identity() { identity_path=/dev/fake-$1; return 0; }
        lsblk() { case "$*" in *MOUNTPOINT*) ;; *) printf 'x\nx1\n' ;; esac; }
        wipefs() { echo "gpfs signature"; }
        wipe_disk() { echo "WIPE $1"; }
        stc_progress() { :; }
        stc_result() { echo "RESULT $*"; }
        stc_fail() { echo "FAILED $*"; exit 1; }
        stc_main <<<"$1"
    )
}
out=$(resrun '{"cluster_uuid":"'$ruuid'","kind":"gpfs","disks":[{"id":"wwn-a","size_bytes":1,"in_use":true,"wipe":true}]}')
grep -q '^RESULT' <<<"$out" && ! grep -q WIPE <<<"$out" && grep -q "identity only" <<<"$out" &&
    ok "resolve: a disk in use with data is taken on its identity, not wiped even when wipe is set" || ko "in use: $out"
rm -rf $rdir
out=$(resrun '{"cluster_uuid":"'$ruuid'","kind":"gpfs","disks":[{"id":"wwn-b","size_bytes":1}]}')
grep -q '^FAILED wwn-b (/dev/fake-wwn-b) has data on it' <<<"$out" && ok "resolve: a new disk with data still fails without wipe" || ko "new dirty: $out"
out=$(resrun '{"cluster_uuid":"'$ruuid'","kind":"gpfs","disks":[{"id":"wwn-b","size_bytes":1,"wipe":true}]}')
grep -q 'WIPE /dev/fake-wwn-b' <<<"$out" && grep -q '^RESULT' <<<"$out" && ok "resolve: a new disk with data is wiped when asked" || ko "new wipe: $out"
rm -rf $rdir

# ---- the nsddevices exit: a multipath device is dmm ----
script=$(cd $S && source ../../cloudrc && source ./backends/gpfs.sh && f=$fake/nsddevices &&
    sed -e "s|f=/var/mmfs/etc/nsddevices|f=$fake/nsddevices|" -e 's|mkdir -p /var/mmfs/etc|:|' <(declare -f backend_disks_resolved) >$fake/br.sh &&
    source $fake/br.sh && backend_disks_resolved $uuid wwn-0x6000aaa1 dev:sdb && cat $fake/nsddevices)
grep -q "case \"\$d\" in /dev/dm-\*) t=dmm" <<<"$script" && grep -q "echo 'sdb generic'" <<<"$script" &&
    ok "nsddevices: dm devices typed dmm, plain disks generic" || ko "nsddevices: $script"
# the exit run against a fake by-id: a link to a dm device prints dmm
mkdir -p $fake/byid
ln -sf /dev/null $fake/byid/wwn-0x6000aaa1
out=$(sed -e "s|readlink -f '/dev/disk/by-id/wwn-0x6000aaa1'|echo /dev/dm-7|" -e 's|\[ -b "\$d" \] \&\& ||' $fake/nsddevices | bash)
grep -qx "dm-7 dmm" <<<"$out" && ok "nsddevices: run, a multipath LUN is listed as dm-7 dmm" || ko "nsddevices run: $out"

# ---- the disk scan: a path of a multipath device is in use, the multipath device a shared LUN ----
clrun() {
    (
        cd $S/..
        source ../cloudrc
        source ./storage_lib.sh
        system_disk_list="sda"
        local_pool_list=""
        NODE_ID=1
        # sdc and sdd are the two paths of mpatha (dm-0)
        lsblk() {
            case "$*" in
                "-dno TRAN /dev/sdc"|"-dno TRAN /dev/sdd") echo fc ;;
                "-dno TRAN /dev/mapper/mpatha") echo "" ;;
                "-dno WWN /dev/sdc"|"-dno WWN /dev/sdd") echo 0x6000aaa1 ;;
                "-dno WWN /dev/mapper/mpatha") echo "" ;;
                "-dno WWN") printf '0x6000aaa1\n0x6000aaa1\n' ;;
                "-nlo TYPE /dev/sdc"|"-nlo TYPE /dev/sdd") printf 'disk\nmpath\n' ;;
                "-nlo TYPE /dev/mapper/mpatha") echo mpath ;;
                "-nlo NAME,TYPE /dev/sdc") printf 'sdc disk\nmpatha mpath\n' ;;
                "-dno TYPE /dev/mapper/mpatha") echo mpath ;;
                "-dno TYPE /dev/sdc") echo disk ;;
                *) : ;;
            esac
        }
        disk_tags() { tag_pool=""; tag_host=""; }
        classify_disk $1
        echo "$disk_state|$disk_detail"
    )
}
out=$(clrun /dev/sdc)
[ "$out" = "in_use|a path of multipath device mpatha" ] && ok "classify: a path of a multipath device is in use" || ko "path: $out"
out=$(clrun /dev/mapper/mpatha)
[ "${out%%|*}" = shared ] && ok "classify: the multipath device is a shared LUN" || ko "mpath: $out"

# ---- the precheck of a disk of the shared disk layout: a shared LUN passes (identity only), a mounted one or one that
# is no shared LUN on this host fails; a shared LUN given to a layout of local disks still fails ----
# $1: the disk of the precheck input, $2: the device its identity resolves to; $MNT: what is mounted on it
pcrun() {
    (
        cd $S/..
        source ../cloudrc
        source ./storage_lib.sh
        NODE_ID=1
        lsblk() {
            case "$*" in
                "-dno TRAN /dev/sdc") echo fc ;;
                "-dno WWN /dev/sdc") echo 0x6000aaa1 ;;
                "-dno WWN") printf '0x6000aaa1\n0x6000aaa1\n' ;;
                "-nlo TYPE /dev/sdc") printf 'disk\nmpath\n' ;;
                "-nlo TYPE /dev/mapper/mpatha") echo mpath ;;
                "-nlo NAME,TYPE /dev/sdc") printf 'sdc disk\nmpatha mpath\n' ;;
                "-dno TYPE /dev/mapper/mpatha") echo mpath ;;
                "-nlo MOUNTPOINTS /dev/mapper/mpatha") echo "$MNT" ;;
                *) : ;;
            esac
        }
        disk_tags() { tag_pool=""; tag_host=""; }
        system_disks() { echo sda; }
        local_pools() { :; }
        disk_identity() { identity_path=$2; return 0; }
        item() { echo "$2|$3"; }
        eval "$(sed -e 's/\r$//' $S/stc_precheck.sh | sed -n '/^function check_disks()/,/^}/p')"
        input=$(jq -cn --argjson d "$1" '{disks: [$d]}')
        check_disks
    ) 2>/dev/null
}
san='{"id":"wwn-0x6000aaa1","serial":"/dev/mapper/mpatha","size_bytes":1,"shared":true}'
out=$(MNT="" pcrun "$san")
[ "${out%%|*}" = ok ] && grep -q "shared LUN" <<<"$out" && ok "precheck: a shared LUN of the shared disk layout passes ($out)" || ko "precheck san: $out"
out=$(MNT="" pcrun "$(jq -c '.wipe = true' <<<"$san")")
[ "${out%%|*}" = warn ] && ok "precheck: a shared LUN to wipe warns" || ko "precheck san wipe: $out"
out=$(MNT="/mnt/x" pcrun "$san")
[ "${out%%|*}" = fail ] && grep -q "mounted" <<<"$out" && ok "precheck: a mounted shared LUN fails" || ko "precheck san mounted: $out"
out=$(MNT="" pcrun "$(jq -c '.serial = "/dev/sdc"' <<<"$san")")
[ "${out%%|*}" = fail ] && grep -q "no shared LUN" <<<"$out" && ok "precheck: a path of a multipath device is no shared LUN" || ko "precheck san path: $out"
out=$(MNT="" pcrun "$(jq -c 'del(.shared)' <<<"$san")")
[ "${out%%|*}" = fail ] && grep -q "can not be used: shared" <<<"$out" && ok "precheck: a shared LUN given to a layout of local disks fails" || ko "precheck local: $out"

rm -rf $fake
echo "$pass passed, $fail failed"
[ $fail = 0 ]
