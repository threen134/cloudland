#!/bin/bash
# sandbox tests of the GPFS erasure code layout (shared-storage-design.md §7.9) with stub mm commands: the slots step
# (the hidden slot attribute that wants 999, the yes / no write cache check, rolling restarts only where needed), the
# recovery group step (deferred log vdisks, the pdisk list of mmlspdisk in both device forms, a retry that finds it
# done), and teardown_ece (a recovery group that does not delete: servers stopped, then -p)
S=/opt/cloudland/scripts/kvm/storage
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
uuid=aaaaaaaa-9999-2222-3333-444444444444
fake=$(mktemp -d)
export FAKE=$fake

# ---- stubs ----
cat >$fake/mmlscluster <<'EOF'
#!/bin/bash
echo "mmlscluster:clusterNode:HEADER:version:reserved:reserved:nodeNumber:daemonNodeName:ipAddress:adminNodeName:designation:"
echo "mmlscluster:clusterNode:0:1:::1:n1.cl:10.0.0.1:n1:quorum:"
echo "mmlscluster:clusterNode:0:1:::2:n2.cl:10.0.0.2:n2:quorum:"
echo "mmlscluster:clusterNode:0:1:::3:n3.cl:10.0.0.3:n3:quorum:"
EOF
cat >$fake/mmlsnodeclass <<'EOF'
#!/bin/bash
[ -f $FAKE/nc ] || exit 1
echo "mmlsnodeclass::HEADER:version:reserved:reserved:nodeClass:type:memberNodes:"
echo "mmlsnodeclass::0:1:::$(cat $FAKE/nc):user:n1.cl,n2.cl,n3.cl:"
EOF
# mmchconfig: an unknown attribute is written only when 999 is typed (ENTER skips it), the write cache check takes
# yes or no only; pending values go to $FAKE/config, a daemon reads them when it starts
cat >$fake/mmchconfig <<'EOF'
#!/bin/bash
echo "mmchconfig $*" >>$FAKE/calls
kv=$1; a=${kv%%=*}; v=${kv#*=}
case $a in
    nsdRAIDStrictPdiskSlotLocation)
        echo "mmchconfig: Attention: Unknown attribute specified: $a.  Press the ENTER key to continue."
        read -r answer; [ "$answer" = 999 ] || exit 0 ;;
    nsdRAIDDiskCheckVWCE)
        case $v in yes) v=1 ;; no) v=0 ;; *) echo "mmchconfig: $a value must be yes or no."; exit 1 ;; esac ;;
esac
grep -v "^$a " $FAKE/config >$FAKE/config.x 2>/dev/null; echo "$a $v" >>$FAKE/config.x; mv $FAKE/config.x $FAKE/config
EOF
cat >$fake/mmshutdown <<'EOF'
#!/bin/bash
echo "mmshutdown $*" >>$FAKE/calls
[ "$1" = -N ] && for n in ${2//,/ }; do echo down >$FAKE/state-$n; done
exit 0
EOF
cat >$fake/mmstartup <<'EOF'
#!/bin/bash
echo "mmstartup $*" >>$FAKE/calls
if [[ "$2" =~ ^[0-9.]+$ ]]; then ip=$2; else ip=$($FAKE/mmlscluster | awk -F: -v n="$2" '$3 != "HEADER" && ($8 == n || $10 == n) {print $9}'); fi
cp $FAKE/config $FAKE/diag-$ip 2>/dev/null || : >$FAKE/diag-$ip
echo active >$FAKE/state-$2
EOF
cat >$fake/mmgetstate <<'EOF'
#!/bin/bash
echo "mmgetstate::HEADER:version:reserved:reserved:nodeName:nodeNumber:state:"
echo "mmgetstate::0:1:::$2:1:$(cat $FAKE/state-$2 2>/dev/null || echo active):"
EOF
# mmdiag --config of the node gpfs_rsh logged in to (DIAG_NODE)
cat >$fake/mmdiag <<'EOF'
#!/bin/bash
echo "=== mmdiag: config ==="
echo "   pagepool 8589934592"
while read -r a v; do [ -n "$a" ] && echo " ! $a $v"; done <$FAKE/diag-$DIAG_NODE 2>/dev/null
EOF
# mmvdisk: the recovery group, its log groups (deferred until --complete-log-format), deletes
cat >$fake/mmvdisk <<'EOF'
#!/bin/bash
echo "mmvdisk $*" >>$FAKE/calls
case "$1 $2" in
    "recoverygroup list")
        [ -f $FAKE/rg ] || exit 1
        case "$*" in
            *--log-group*) echo "mmvdisk:rgLogGroup:HEADER:version:reserved:reserved:recoveryGroup:logGroup:"
                for i in $(seq 1 $(cat $FAKE/lg)); do echo "mmvdisk:rgLogGroup:0:1:::$(cat $FAKE/rg):lg$i:"; done ;;
            *) echo "rg $(cat $FAKE/rg)" ;;
        esac ;;
    "recoverygroup create")
        if [ "$3" = --complete-log-format ]; then echo 7 >$FAKE/lg; exit 0; fi
        echo "$4" >$FAKE/rg; echo 1 >$FAKE/lg
        echo "mmvdisk: Deferring recovery group log vdisk creation and format." ;;
    "recoverygroup delete")
        case "$*" in
            *" -p"*) grep -q "^mmshutdown -N n1.cl,n2.cl,n3.cl" $FAKE/calls || { echo "mmvdisk: The '-p' option may not be used on an active recovery group."; exit 1; }
                read -r answer; [ "$answer" = yes ] || exit 1; rm -f $FAKE/rg ;;
            *) awk -v r="$4" '$4 == r' $FAKE/vs 2>/dev/null | grep -q . && { echo "mmvdisk: Recovery group '$4' is in use by vdisk set."; exit 1; }
                [ -f $FAKE/rg_broken ] && { echo "mmvdisk: Vdisk RG001ROOTLOGHOME not found."; exit 1; }
                read -r answer; rm -f $FAKE/rg ;;
        esac ;;
    "server unconfigure") rm -f $FAKE/configured ;;
    "recoverygroup change") read -r a l <$FAKE/rgserver; echo "$8 $l" >$FAKE/rgserver ;;
    "nodeclass delete") rm -f $FAKE/nc ;;
    "vdiskset list")
        if [ "$3" = --vdisk-set ]; then
            awk -v v="$4" '$1 == v' $FAKE/vs 2>/dev/null | grep -q . || exit 1
            echo "mmvdisk:vdisksetAttributes:HEADER:version:reserved:reserved:vdisksetName:vdiskCount:created:"
            echo "mmvdisk:vdisksetAttributes:0:1:::$4:6:yes:"; exit 0
        fi
        echo "mmvdisk:vdisksetSummary:HEADER:version:reserved:reserved:vdisksetName:created:fsName:recoveryGroups:"
        [ -f $FAKE/vs ] && while read -r n c f r; do [ "$f" = - ] && f=""; echo "mmvdisk:vdisksetSummary:0:1:::$n:$c:$f:$r:"; done <$FAKE/vs ;;
    "vdiskset define")
        awk -v v="$4" '$1 == v' $FAKE/vs 2>/dev/null | grep -q . && { echo "mmvdisk: Vdisk set '$4' already exists."; exit 1; }
        echo "$4 no - $6" >>$FAKE/vs ;;
    "vdiskset create") awk -v v="$4" '$1 == v {$2 = "yes"} {print}' $FAKE/vs >$FAKE/vs.x; mv $FAKE/vs.x $FAKE/vs ;;
    "vdiskset delete")
        read -r answer; [ "$answer" = yes ] || exit 1
        awk -v v="$4" '$1 == v && $3 != "-" {bad = 1} END {exit bad}' $FAKE/vs || { echo "mmvdisk: vdisk set $4 is in use by a file system."; exit 1; }
        awk -v v="$4" '$1 == v {$2 = "no"} {print}' $FAKE/vs >$FAKE/vs.x; mv $FAKE/vs.x $FAKE/vs ;;
    "vdiskset undefine")
        [ -f $FAKE/undefine_noop ] && exit 0
        awk -v v="$4" '$1 == v && $2 == "yes" {bad = 1} END {exit bad}' $FAKE/vs || { echo "mmvdisk: vdisk set $4 has vdisks."; exit 1; }
        awk -v v="$4" '$1 != v' $FAKE/vs >$FAKE/vs.x; mv $FAKE/vs.x $FAKE/vs ;;
    "filesystem delete") awk -v f="$4" '$3 == f {$3 = "-"} {print}' $FAKE/vs >$FAKE/vs.x; mv $FAKE/vs.x $FAKE/vs ;;
esac
exit 0
EOF
cat >$fake/mmlspdisk <<'EOF'
#!/bin/bash
cat <<'X'
pdisk:
   name = "n001p001"
   device = "//n1.cl/dev/sdc"
   server = "n3.cl"
   state = "ok"
   WWN = "naa.600140587FEAED4FA89406C9B3A0E524"
pdisk:
   name = "n002p001"
   device = "/dev/sdb,/dev/sdq"
   server = "n2.cl"
   state = "diagnosing"
   WWN = "naa.6001405A01F389DFB164FA2B9C23BF18"
X
# the other 4 of the 6 claimed disks, unless the read comes back short
[ -f $FAKE/pdisk_short ] && exit 0
for n in n001p002 n002p002 n003p001 n003p002; do
    printf 'pdisk:
   name = "%s"
   device = "//n3.cl/dev/sdd"
   state = "ok"
   WWN = "naa.60014050000%s"
' $n ${n: -1}
done
EOF
# dpkg-query: the packages installed, from $FAKE/dpkg ("<package> <version>" lines)
cat >$fake/dpkg-query <<'EOF'
#!/bin/bash
pkg=${@: -1}
v=$(awk -v p="$pkg" '$1 == p {print $2}' $FAKE/dpkg 2>/dev/null)
[ -n "$v" ] || exit 1
echo "$v install ok installed"
EOF
cat >$fake/mmlsfs <<'EOF'
#!/bin/bash
exit 1
EOF
# the active server of the recovery group and its server list ($FAKE/rgserver: "<active> <list>")
cat >$fake/mmlsrecoverygroup <<'EOF'
#!/bin/bash
read -r a l <$FAKE/rgserver 2>/dev/null || { a=n1.cl; l=n1.cl,n2.cl,n3.cl; }
echo "mmlsrecoverygroup:server:HEADER:version:reserved:reserved:ActiveRecoveryGroupServer:Servers:"
echo "mmlsrecoverygroup:server:0:1:::$a:$l:"
EOF
chmod +x $fake/*
mkdir -p /opt/cloudland/run/storage/$uuid
# The remote shell of the cluster: runs the command here, as the node it logged in to
cat >/opt/cloudland/run/storage/$uuid/gpfs_rsh <<EOF
#!/bin/bash
DIAG_NODE=\$1 FAKE=$fake bash -c "\$2"
EOF
chmod +x /opt/cloudland/run/storage/$uuid/gpfs_rsh

# run <function> <input>: a function of gpfs_ece.sh (or gpfs_cluster.sh), with the stubs; prints what it reported
run() {
    local file=${3:-gpfs_ece.sh}
    (
        cd $S
        # without its dispatch, and staying in $S (it cds to the directory of $0, which is this test here)
        source <(sed -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' $file)
        gpfs_bin=$fake
        cluster_uuid=$uuid
        stc_progress() { echo "PROGRESS $*"; }
        stc_result() { echo "RESULT $*"; }
        stc_fail() { echo "FAILED $*"; exit 1; }
        sleep() { :; }
        "$1" "$2"
    )
}

# ---- slots ----
echo cl9_nc >$fake/nc
: >$fake/calls; : >$fake/config
# n2 runs with both checks off already (a daemon restarted earlier), n1 and n3 do not
printf 'nsdRAIDStrictPdiskSlotLocation 0\nnsdRAIDDiskCheckVWCE 0\n' >$fake/diag-10.0.0.2
printf 'nsdRAIDDiskCheckVWCE 1\n' >$fake/diag-10.0.0.1
: >$fake/diag-10.0.0.3
out=$(run do_slots '{"cluster_uuid":"'$uuid'","node_class":"cl9_nc","no_slot_map":true}')
grep -q '^RESULT {"slot_check": "off", "write_cache_check": "off"}' <<<"$out" && ok "slots: both checks off" || ko "slots: $out"
grep -qx "mmchconfig nsdRAIDStrictPdiskSlotLocation=0 -N cl9_nc" $fake/calls && grep -qx "mmchconfig nsdRAIDDiskCheckVWCE=no -N cl9_nc" $fake/calls &&
    grep -qx "nsdRAIDStrictPdiskSlotLocation 0" $fake/config && grep -qx "nsdRAIDDiskCheckVWCE 0" $fake/config &&
    ok "slots: the slot attribute written with 999, the write cache check as no" || ko "chconfig: $(cat $fake/calls) / $(cat $fake/config)"
[ "$(grep -c '^mmstartup' $fake/calls)" = 2 ] && grep -qx "mmstartup -N 10.0.0.1" $fake/calls && grep -qx "mmstartup -N 10.0.0.3" $fake/calls &&
    ok "slots: only the daemons still running with a check restarted (n1, n3)" || ko "restarts: $(grep mm[ss] $fake/calls)"
: >$fake/calls
out=$(run do_slots '{"cluster_uuid":"'$uuid'","node_class":"cl9_nc","no_slot_map":true}')
grep -q '^RESULT' <<<"$out" && ! grep -q '^mmstartup' $fake/calls && ok "slots again: nothing restarted" || ko "slots again: $out / $(cat $fake/calls)"
# a daemon that comes back with the check still on
printf 'nsdRAIDStrictPdiskSlotLocation 0\nnsdRAIDDiskCheckVWCE 1\n' >$fake/diag-10.0.0.3
printf 'nsdRAIDStrictPdiskSlotLocation 0\n' >$fake/config.stuck
cp $fake/mmstartup $fake/mmstartup.orig
sed -i 's|cp $FAKE/config |cp $FAKE/config.stuck |' $fake/mmstartup
out=$(run do_slots '{"cluster_uuid":"'$uuid'","node_class":"cl9_nc","no_slot_map":true}')
grep -q '^FAILED GPFS on 10.0.0.3 still runs with the slot or write cache check' <<<"$out" && ok "slots: a daemon still checking fails the step" || ko "stuck: $out"
mv $fake/mmstartup.orig $fake/mmstartup
# a real server without a slot map
out=$(run do_slots '{"cluster_uuid":"'$uuid'","node_class":"cl9_nc"}')
grep -q '^FAILED no slot map on 10.0.0.1 10.0.0.2 10.0.0.3' <<<"$out" && ok "slots: real servers need ecedrivemapping first" || ko "slot map: $out"

# ---- create_rg ----
: >$fake/calls; rm -f $fake/rg
expr='10.0.0.1:sdb,sdc;10.0.0.2:sdb,sdc;10.0.0.3:sdb,sdc'
out=$(run do_create_rg '{"cluster_uuid":"'$uuid'","recovery_group":"cl9_rg","node_class":"cl9_nc","disk_expr":"'$expr'"}')
grep -q "^mmvdisk recoverygroup create --recovery-group cl9_rg --node-class cl9_nc --disk-list $expr" $fake/calls &&
    grep -qx "mmvdisk recoverygroup create --complete-log-format" $fake/calls && ok "create_rg: deferred log vdisks completed" || ko "rg: $(cat $fake/calls)"
res=$(sed -n 's/^RESULT //p' <<<"$out")
[ "$(jq -c '.pdisks[0]' <<<"$res")" = '{"name":"n001p001","ip":"10.0.0.1","device":"sdc","wwn":"naa.600140587FEAED4FA89406C9B3A0E524","state":"ok","da":""}' ] &&
    [ "$(jq -c '.pdisks[1]' <<<"$res")" = '{"name":"n002p001","ip":"10.0.0.2","device":"sdb","wwn":"naa.6001405A01F389DFB164FA2B9C23BF18","state":"diagnosing","da":""}' ] &&
    ok "create_rg: pdisks from mmlspdisk (//node/dev, the node serving the group in the server line; /dev,/dev with the server line)" || ko "pdisks: $res"
: >$fake/calls
out=$(run do_create_rg '{"cluster_uuid":"'$uuid'","recovery_group":"cl9_rg","node_class":"cl9_nc","disk_expr":"'$expr'"}')
grep -q "recovery group cl9_rg exists already" <<<"$out" && ! grep -q "recoverygroup create" $fake/calls && grep -q '^RESULT' <<<"$out" &&
    ok "create_rg again: found done, nothing made" || ko "rg again: $out / $(cat $fake/calls)"
echo 4 >$fake/lg
: >$fake/calls
out=$(run do_create_rg '{"cluster_uuid":"'$uuid'","recovery_group":"cl9_rg","node_class":"cl9_nc","disk_expr":"'$expr'"}')
grep -qx "mmvdisk recoverygroup create --complete-log-format" $fake/calls && grep -q '^RESULT' <<<"$out" &&
    ok "create_rg retry after an interrupted log format: completes the rest" || ko "rg partial: $out"
# the root log group on another server than the first: moved there
echo "n2.cl n1.cl,n2.cl,n3.cl" >$fake/rgserver; : >$fake/calls
out=$(run do_create_rg '{"cluster_uuid":"'$uuid'","recovery_group":"cl9_rg","node_class":"cl9_nc","disk_expr":"'$expr'"}')
grep -qx "mmvdisk recoverygroup change --recovery-group cl9_rg --log-group root --active n1.cl" $fake/calls && [ "$(cut -d' ' -f1 $fake/rgserver)" = n1.cl ] &&
    grep -q '^RESULT' <<<"$out" && ok "create_rg: root log group moved to the first server" || ko "primary: $out / $(cat $fake/calls)"
: >$fake/calls
out=$(run do_create_rg '{"cluster_uuid":"'$uuid'","recovery_group":"cl9_rg","node_class":"cl9_nc","disk_expr":"'$expr'"}')
! grep -q "recoverygroup change" $fake/calls && ok "create_rg: root on the first server already, not moved" || ko "primary again: $(cat $fake/calls)"
rm -f $fake/rgserver
out=$(run do_create_rg '{"cluster_uuid":"'$uuid'","recovery_group":"cl9 rg","node_class":"cl9_nc","disk_expr":"'$expr'"}')
grep -q '^FAILED invalid recovery group name' <<<"$out" && ok "create_rg: a bad name refused" || ko "bad name: $out"
# mmlspdisk comes back short (busy group): the step fails instead of handing the platform 2 of 6 pdisks
touch $fake/pdisk_short
out=$(run do_create_rg '{"cluster_uuid":"'$uuid'","recovery_group":"cl9_rg","node_class":"cl9_nc","disk_expr":"'$expr'"}')
grep -q '^FAILED recovery group cl9_rg lists 2 pdisks, 6 disks were claimed' <<<"$out" && ! grep -q '^RESULT' <<<"$out" &&
    ok "create_rg: pdisks short of the claimed disks fail the step" || ko "short: $out"
rm -f $fake/pdisk_short
# the node class read fails: not taken for a 1 log group recovery group
mv $fake/nc $fake/nc.away
out=$(run do_create_rg '{"cluster_uuid":"'$uuid'","recovery_group":"cl9_rg","node_class":"cl9_nc","disk_expr":"'$expr'"}')
grep -q '^FAILED node class cl9_nc has 0 members, the disk expression 3 servers' <<<"$out" &&
    ok "create_rg: an unreadable node class fails the step" || ko "no class: $out"
mv $fake/nc.away $fake/nc

# ---- the packages of an installer: each at its version, not only gpfs.base ----
debs='{"debs":{"gpfs.base_6.0.0-2_amd64.deb":"a","gpfs.gnr_6.0.0-2_amd64.deb":"b","gpfs.gnr.base_1.0.0-0_amd64.deb":"c","gpfs.gskit_8.0.55-19.1_amd64.deb":"d"}}'
printf 'gpfs.base 6.0.0-2
gpfs.gskit 8.0.55-19.1
' >$fake/dpkg
out=$(PATH=$fake:$PATH run gpfs_debs_missing "$debs")
[ "$(echo $out)" = "gpfs.gnr.base gpfs.gnr" ] && ok "packages: the erasure code ones missing beside gpfs.base of the same version" || ko "missing: $out"
printf 'gpfs.base 6.0.0-2
gpfs.gskit 8.0.55-19.1
gpfs.gnr 6.0.0-2
gpfs.gnr.base 1.0.0-0
' >$fake/dpkg
out=$(PATH=$fake:$PATH run gpfs_debs_missing "$debs")
[ -z "$out" ] && ok "packages: all installed, nothing missing" || ko "all: $out"
printf 'gpfs.base 5.2.3-8
gpfs.gskit 8.0.55-19.1
gpfs.gnr 6.0.0-2
gpfs.gnr.base 1.0.0-0
' >$fake/dpkg
out=$(PATH=$fake:$PATH run gpfs_debs_missing "$debs")
[ "$out" = "gpfs.base" ] && ok "packages: another version counts as missing" || ko "version: $out"

# ---- resolve_disks: "nsddevices": false leaves the nsddevices exit alone ----
resolve() {
    (
        cd $S
        source <(sed -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' stc_resolve_disks.sh)
        stc_lock() { :; }
        system_disks() { :; }
        backend_load() { backend_disks_resolved() { echo resolved >>$FAKE/resolved; }; }
        stc_result() { echo "RESULT $*"; }
        stc_fail() { echo "FAILED $*"; exit 1; }
        stc_main <<<"$1"
    )
}
rm -f $fake/resolved
resolve '{"cluster_uuid":"'$uuid'","kind":"gpfs","disks":[],"nsddevices":false}' >/dev/null
[ ! -f $fake/resolved ] && ok "resolve_disks: nsddevices false writes no nsddevices exit" || ko "nsddevices false wrote it"
resolve '{"cluster_uuid":"'$uuid'","kind":"gpfs","disks":[]}' >/dev/null
[ -f $fake/resolved ] && ok "resolve_disks: without the flag the nsddevices exit is written" || ko "default did not write it"

# ---- create_vs ----
vsin='{"cluster_uuid":"'$uuid'","recovery_group":"cl9_rg","sets":[{"vdisk_set":"cl9_vs","code":"4+2p","block_size":"4M","set_size_pct":80}]}'
: >$fake/calls; rm -f $fake/vs
out=$(run do_create_vs "$vsin")
grep -qx "mmvdisk vdiskset define --vdisk-set cl9_vs --recovery-group cl9_rg --code 4+2p --block-size 4m --set-size 80%" $fake/calls &&
    grep -qx "mmvdisk vdiskset create --vdisk-set cl9_vs" $fake/calls && [ "$(cat $fake/vs)" = "cl9_vs yes - cl9_rg" ] && grep -q '^RESULT {}' <<<"$out" &&
    ok "create_vs: defined and created" || ko "create_vs: $out / $(cat $fake/calls)"
: >$fake/calls
out=$(run do_create_vs "$vsin")
grep -q "vdisk set cl9_vs is created already" <<<"$out" && ! grep -qE "vdiskset (define|create)" $fake/calls && grep -q '^RESULT' <<<"$out" &&
    ok "create_vs again: found in the summary of the plain list, nothing made" || ko "create_vs again: $out / $(cat $fake/calls)"
echo "cl9_vs no - cl9_rg" >$fake/vs; : >$fake/calls
out=$(run do_create_vs "$vsin")
! grep -q "vdiskset define" $fake/calls && grep -qx "mmvdisk vdiskset create --vdisk-set cl9_vs" $fake/calls &&
    ok "create_vs of a set only defined: created" || ko "defined only: $out / $(cat $fake/calls)"

# ---- teardown_ece ----
td='{"filesystems":["ecefs"],"ece":{"recovery_group":"cl9_rg","node_class":"cl9_nc"}}'
# the full stack: a file system on the vdisk set, plus a set only defined
echo "cl9_vs yes ecefs cl9_rg" >$fake/vs; echo "cl9_vs2 no - cl9_rg" >>$fake/vs; echo "other_vs yes - other_rg" >>$fake/vs
: >$fake/calls
out=$(run teardown_ece "$td" gpfs_cluster.sh)
grep -qx "mmvdisk filesystem delete --file-system ecefs --confirm" $fake/calls && grep -qx "mmvdisk vdiskset delete --vdisk-set cl9_vs" $fake/calls &&
    grep -qx "mmvdisk vdiskset undefine --vdisk-set cl9_vs --confirm" $fake/calls && grep -qx "mmvdisk vdiskset undefine --vdisk-set cl9_vs2 --confirm" $fake/calls &&
    ! grep -q "vdiskset delete --vdisk-set cl9_vs2" $fake/calls && [ "$(cat $fake/vs)" = "other_vs yes - other_rg" ] &&
    [ ! -f $fake/rg ] && [ ! -f $fake/nc ] && ! grep -q mmshutdown $fake/calls && ! grep -q -- "delete --recovery-group cl9_rg --confirm -p" $fake/calls &&
    ok "teardown_ece: file system, vdisk sets of the group (created or only defined), group, servers, node class; nothing stopped" ||
    ko "teardown: $out / $(cat $fake/calls) / $(cat $fake/vs)"
# a vdisk set that does not go away: the group is in use, no forcing
echo cl9_rg >$fake/rg; echo cl9_nc >$fake/nc; echo "cl9_vs no - cl9_rg" >$fake/vs; touch $fake/undefine_noop
: >$fake/calls
out=$(run teardown_ece "$td" gpfs_cluster.sh)
grep -q '^FAILED recovery group cl9_rg still has vdisk sets: cl9_vs' <<<"$out" && ! grep -q mmshutdown $fake/calls && [ -f $fake/rg ] &&
    ok "teardown_ece: a group still in use is not forced (servers left running)" || ko "in use: $out / $(cat $fake/calls)"
rm -f $fake/undefine_noop $fake/vs
# a half made one: the plain delete fails
echo cl9_rg >$fake/rg; echo cl9_nc >$fake/nc; touch $fake/rg_broken
: >$fake/calls
out=$(run teardown_ece "$td" gpfs_cluster.sh)
grep -n . $fake/calls | grep -E "recoverygroup delete|mmshutdown|unconfigure|nodeclass delete" | cut -d: -f2- >$fake/order
[ "$(cat $fake/order)" = "mmvdisk recoverygroup delete --recovery-group cl9_rg --confirm
mmshutdown -N n1.cl,n2.cl,n3.cl
mmvdisk recoverygroup delete --recovery-group cl9_rg --confirm -p
mmvdisk server unconfigure --node-class cl9_nc --recycle none
mmvdisk nodeclass delete --node-class cl9_nc --confirm" ] && [ ! -f $fake/rg ] && [ ! -f $fake/nc ] &&
    ok "teardown_ece of a half made recovery group: servers stopped, -p, then unconfigure and node class" || ko "half made: $out / $(cat $fake/order)"
rm -f $fake/rg_broken

rm -rf $fake /opt/cloudland/run/storage/$uuid
echo "$pass passed, $fail failed"
[ $fail = 0 ]
