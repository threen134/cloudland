#!/bin/bash
# sandbox tests of the changes of the GPFS erasure code layout (shared-storage-design.md §7.9) with stub mm commands:
# servers joining a recovery group (configured into the class, the add, the completion when the callback does not run),
# a server leaving (its file system share, the group, the class), a pdisk replaced (with and without a slot map, a
# working one refused, a retry found done), new disks on every server (the resize, a vdisk set sized from the free
# share, smaller when the definition does not fit), mixed media vdisk sets, the placement rule, the slot map made by
# ecedrivemapping; the upgrade of a recovery group server (suspended, resumed, waited for), the finalize of the group's
# version, and the readiness of a server in the precheck
S=/opt/cloudland/scripts/kvm/storage
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
uuid=aaaaaaaa-1515-2222-3333-444444444444
fake=$(mktemp -d)
export FAKE=$fake

# ---- stubs ----
cat >$fake/mmlscluster <<'EOF'
#!/bin/bash
echo "mmlscluster:clusterNode:HEADER:version:reserved:reserved:nodeNumber:daemonNodeName:ipAddress:adminNodeName:designation:"
for i in 1 2 3 4; do echo "mmlscluster:clusterNode:0:1:::$i:n$i.cl:10.0.0.$i:n$i:quorum:"; done
EOF
# the members of the node class, by daemon name ($FAKE/nc: one a line)
cat >$fake/mmlsnodeclass <<'EOF'
#!/bin/bash
[ -s $FAKE/nc ] || exit 1
echo "mmlsnodeclass::HEADER:version:reserved:reserved:nodeClass:type:memberNodes:"
echo "mmlsnodeclass::0:1:::$1:user:$(paste -sd, $FAKE/nc):"
EOF
cat >$fake/mmchconfig <<'EOF'
#!/bin/bash
echo "mmchconfig $*" >>$FAKE/calls
kv=$1; a=${kv%%=*}; v=${kv#*=}
case $a in
    nsdRAIDStrictPdiskSlotLocation) read -r answer; [ "$answer" = 999 ] || exit 0 ;;
    nsdRAIDDiskCheckVWCE) case $v in yes) v=1 ;; no) v=0 ;; *) exit 1 ;; esac ;;
esac
grep -v "^$a " $FAKE/config >$FAKE/config.x 2>/dev/null; echo "$a $v" >>$FAKE/config.x; mv $FAKE/config.x $FAKE/config
EOF
cat >$fake/mmshutdown <<'EOF'
#!/bin/bash
echo "mmshutdown $*" >>$FAKE/calls
[ "$1" = -N ] && echo down >$FAKE/state-$2
[ -z "$1" ] && echo down >$FAKE/local_state
exit 0
EOF
cat >$fake/mmstartup <<'EOF'
#!/bin/bash
echo "mmstartup $*" >>$FAKE/calls
if [ "$1" = -N ]; then cp $FAKE/config $FAKE/diag-$2 2>/dev/null; echo active >$FAKE/state-$2; else echo active >$FAKE/local_state; fi
EOF
# mmgetstate -N <node>: $FAKE/state-<node>; mmgetstate (this node, n1.cl): $FAKE/local_state
cat >$fake/mmgetstate <<'EOF'
#!/bin/bash
echo "mmgetstate::HEADER:version:reserved:reserved:nodeName:nodeNumber:state:"
if [ "$1" = -N ]; then echo "mmgetstate::0:1:::$2:1:$(cat $FAKE/state-$2 2>/dev/null || echo active):"
else echo "mmgetstate::0:1:::n1.cl:1:$(cat $FAKE/local_state 2>/dev/null || echo active):"; fi
EOF
cat >$fake/mmdiag <<'EOF'
#!/bin/bash
while read -r a v; do [ -n "$a" ] && echo " ! $a $v"; done <$FAKE/diag-$DIAG_NODE 2>/dev/null
EOF
cat >$fake/ecedrivemapping <<'EOF'
#!/bin/bash
echo "ecedrivemapping $* on $DIAG_NODE" >>$FAKE/calls
mkdir -p $FAKE/gems-$DIAG_NODE && touch $FAKE/gems-$DIAG_NODE/slotmap.yaml
EOF
# The pdisks of the recovery group: $FAKE/pdisks "<name> <node> <dev> <wwn> <state> <da>"
cat >$fake/mmlspdisk <<'EOF'
#!/bin/bash
while read -r n node dev wwn st da; do
    [ -n "$n" ] || continue
    printf 'pdisk:\n   name = "%s"\n   device = "//%s/dev/%s"\n   server = "n3.cl"\n   state = "%s"\n   WWN = "%s"\n   declusteredArray = "%s"\n' "$n" "$node" "$dev" "$st" "$wwn" "$da"
done <$FAKE/pdisks
EOF
cat >$fake/mmaddpdisk <<'EOF'
#!/bin/bash
echo "mmaddpdisk $1 $4 $5 $6" >>$FAKE/calls
cat $3 >>$FAKE/stanzas
read -r _ p d a <$3; p=${p#pdiskName=}; d=${d#device=//}; node=${d%%/*}; dev=${d##*/}; a=${a#da=}
awk -v p="$p" -v node="$node" -v dev="$dev" -v da="$a" '$1 == p { print p "#0010", $2, $3, $4, "draining", $6; print p, node, dev, "naa.NEW", "ok", da; next } { print }' $FAKE/pdisks >$FAKE/pdisks.x
mv $FAKE/pdisks.x $FAKE/pdisks
EOF
cat >$fake/mmvdisk <<'EOF'
#!/bin/bash
echo "mmvdisk $*" >>$FAKE/calls
arg() { local k=$1; shift; while [ $# -gt 0 ]; do [ "$1" = "$k" ] && { echo "$2"; return; }; shift; done; }
case "$1 $2" in
    "server configure")
        [ -f $FAKE/configure_noadd ] && exit 0
        for ip in $(arg -N "$@" | tr ',' ' '); do echo "n${ip##*.}.cl" >>$FAKE/nc; done ;;
    "nodeclass add") for ip in $(arg -N "$@" | tr ',' ' '); do echo "n${ip##*.}.cl" >>$FAKE/nc; done ;;
    "nodeclass delete") ip=$(arg -N "$@"); grep -vx "n${ip##*.}.cl" $FAKE/nc >$FAKE/nc.x; mv $FAKE/nc.x $FAKE/nc ;;
    "server list")
        echo "mmvdisk:serverDiskTopology:HEADER:version:reserved:reserved:nodeName:needsAttention:diskTopology:"
        while read -r n; do echo "mmvdisk:serverDiskTopology:0:1:::$n:no:$(cat $FAKE/topo-$n 2>/dev/null || echo 'ECE 5 HDD'):"; done <$FAKE/nc ;;
    "recoverygroup add")
        if [ "$3 $4 $5" = "--recovery-group cl1_rg --complete-node-add" ]; then rm -f $FAKE/addnode; echo 9 >$FAKE/lg; exit 0; fi
        arg -N "$@" >$FAKE/addnode
        echo complete >$FAKE/rebalance
        cat $FAKE/new_pdisks >>$FAKE/pdisks ;;
    "recoverygroup list")
        case "$*" in
            *--log-group*) echo "mmvdisk:rgLogGroup:HEADER:version:reserved:reserved:lgName:userVdisks:logVdisks:server:"
                for i in $(seq 1 $(cat $FAKE/lg)); do echo "mmvdisk:rgLogGroup:0:1:::lg$i:1:1:n1.cl:"; done ;;
            *--declustered-array*) echo "mmvdisk:rgDeclusteredArray:HEADER:version:reserved:reserved:declusteredArray:needsService:backgroundTask:"
                while read -r d bg; do echo "mmvdisk:rgDeclusteredArray:0:1:::$d:no:$bg:"; done <$FAKE/da_bg ;;
            *) echo "mmvdisk:rgSummary:HEADER:version:reserved:reserved:rgName:active:server:needsService:userVdisks:mmvdiskControlled:deferredLogVdisks:rgType:AddNode:rebalanceStatus:suspendedServer:className:splitDa:status:"
                echo "mmvdisk:rgSummary:0:1:::cl1_rg:yes:n1.cl:no:3:yes:no:scaleOut:$(cat $FAKE/addnode 2>/dev/null):$(cat $FAKE/rebalance 2>/dev/null):$(cat $FAKE/suspended 2>/dev/null || echo no):cl1_nc::ok:" ;;
        esac ;;
    "recoverygroup delete")
        ip=$(arg -N "$@"); awk -v n="n${ip##*.}.cl" '$2 != n' $FAKE/pdisks >$FAKE/pdisks.x; mv $FAKE/pdisks.x $FAKE/pdisks ;;
    "recoverygroup resize") cat $FAKE/new_pdisks >>$FAKE/pdisks ;;
    "recoverygroup change")
        case "$*" in
            *--suspend*) echo yes >$FAKE/suspended; echo down >$FAKE/local_state ;;
            *--resume*) echo no >$FAKE/suspended; echo active >$FAKE/local_state ;;
        esac ;;
    "filesystem delete"|"filesystem create") ;;
    "filesystem add") vs=$(arg --vdisk-set "$@"); fs=$(arg --file-system "$@"); awk -v v="$vs" -v f="$fs" '$1 == v {$3 = f} {print}' $FAKE/vs >$FAKE/vs.x; mv $FAKE/vs.x $FAKE/vs ;;
    "vdiskset list")
        if [ "$3" = --recovery-group ]; then
            echo "mmvdisk:vdisksetDaSizing:HEADER:version:reserved:reserved:recoveryGroup:declusteredArray:totalRawSize:freeRawSize:freePercent:vdiskSets:hardwareType:"
            while read -r d t f; do echo "mmvdisk:vdisksetDaSizing:0:1:::$4:$d:100:$f:$f::$t:"; done <$FAKE/das
            exit 0
        fi
        [ "$3" = --vdisk-set ] && exit 0
        echo "mmvdisk:vdisksetSummary:HEADER:version:reserved:reserved:vdisksetName:created:fsName:recoveryGroups:"
        [ -f $FAKE/vs ] && while read -r n c f r; do [ "$f" = - ] && f=""; echo "mmvdisk:vdisksetSummary:0:1:::$n:$c:$f:$r:"; done <$FAKE/vs ;;
    "vdiskset define")
        pct=$(arg --set-size "$@"); pct=${pct%\%}
        [ -f $FAKE/define_max ] && [ $pct -gt $(cat $FAKE/define_max) ] && { echo "mmvdisk: The vdisk set does not fit."; exit 1; }
        echo "$(arg --vdisk-set "$@") no - $(arg --recovery-group "$@")" >>$FAKE/vs ;;
    "vdiskset create") awk -v v="$(arg --vdisk-set "$@")" '$1 == v {$2 = "yes"} {print}' $FAKE/vs >$FAKE/vs.x; mv $FAKE/vs.x $FAKE/vs ;;
    "pdisk replace")
        case "$*" in
            *--prepare*) ;;
            *) p=$(arg --pdisk "$@"); read -r node dev <$FAKE/slot_new
               awk -v p="$p" -v node="$node" -v dev="$dev" '$1 == p {$2 = node; $3 = dev; $5 = "ok"} {print}' $FAKE/pdisks >$FAKE/pdisks.x; mv $FAKE/pdisks.x $FAKE/pdisks ;;
        esac ;;
    "pdisk list")
        echo "mmvdisk:pdiskSummary:HEADER:version:reserved:reserved:recoveryGroup:pdiskName:declusteredArray:capacity:freeSpace:fru:nPathsTotal:state:"
        awk '$5 != "ok" {print "mmvdisk:pdiskSummary:0:1:::cl1_rg:" $1 ":" $6 ":1:1:x:1:" $5 ":"}' $FAKE/pdisks ;;
esac
exit 0
EOF
cat >$fake/mmlspolicy <<'EOF'
#!/bin/bash
cat $FAKE/policy 2>/dev/null
EOF
cat >$fake/mmchpolicy <<'EOF'
#!/bin/bash
echo "mmchpolicy $1" >>$FAKE/calls
cat $2 >$FAKE/policy
EOF
cat >$fake/mmlsfs <<'EOF'
#!/bin/bash
[ -f $FAKE/fs_made ] || exit 1
EOF
cat >$fake/mmmount <<'EOF'
#!/bin/bash
touch $FAKE/fs_made
EOF
cat >$fake/mmlsmount <<'EOF'
#!/bin/bash
echo "mmlsmount::HEADER:version:reserved:reserved:localDevName:realDevName:owningCluster:totalNodes:nodeIP:nodeName:"
for i in 1 2 3 4; do echo "mmlsmount::0:1:::$1:$1:c:4:10.0.0.$i:n$i.cl:"; done
EOF
cat >$fake/mmdf <<'EOF'
#!/bin/bash
echo "mmdf:fsTotal:HEADER:version:reserved:reserved:fsSize:freeBlocks:"
echo "mmdf:fsTotal:0:1:::1048576:524288:"
EOF
cat >$fake/mmlsconfig <<'EOF'
#!/bin/bash
echo "mmlsconfig::HEADER:version:reserved:reserved:attribute:value:"
echo "mmlsconfig::0:1:::minReleaseLevel:6.0.0.2:"
EOF
chmod +x $fake/*
mkdir -p /opt/cloudland/run/storage/$uuid
# The remote shell of the cluster: runs the command here, as the node it logged in to, with the slot map directory of
# that node in the sandbox
cat >/opt/cloudland/run/storage/$uuid/gpfs_rsh <<EOF
#!/bin/bash
DIAG_NODE=\$1 FAKE=$fake bash -c "\${2//\/usr\/lpp\/mmfs\/data\/gems/$fake/gems-\$1}"
EOF
chmod +x /opt/cloudland/run/storage/$uuid/gpfs_rsh

# run <function> <input> [file]: a function of a node script with the stubs; prints what it reported
run() {
    local file=${3:-gpfs_ece.sh}
    (
        cd $S
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
reset() {
    rm -f $fake/calls $fake/stanzas $fake/addnode $fake/rebalance $fake/configure_noadd $fake/define_max $fake/policy $fake/fs_made $fake/suspended
    printf 'n1.cl\nn2.cl\nn3.cl\n' >$fake/nc
    : >$fake/calls; : >$fake/config
    echo 7 >$fake/lg
    : >$fake/da_bg
    printf 'DA1 HDD 20\n' >$fake/das
    : >$fake/pdisks
    for k in 1 2 3; do for p in 1 2 3 4 5; do echo "n00${k}p00$p n$k.cl sd$(printf "\\x$(printf %x $((97 + p)))") naa.$k$p ok DA1" >>$fake/pdisks; done; done
}

# ---- add_servers: configured into the class, slots, topology, the add, completed by hand when the callback does not ----
reset
for p in 1 2 3 4 5; do echo "n004p00$p n4.cl sd$(printf "\\x$(printf %x $((97 + p)))") naa.4$p ok DA1"; done >$fake/new_pdisks
printf 'nsdRAIDStrictPdiskSlotLocation 0\nnsdRAIDDiskCheckVWCE 0\n' >$fake/config.stuck
full='10.0.0.1:sdb,sdc,sdd,sde,sdf;10.0.0.2:sdb,sdc,sdd,sde,sdf;10.0.0.3:sdb,sdc,sdd,sde,sdf;10.0.0.4:sdb,sdc,sdd,sde,sdf'
addin='{"cluster_uuid":"'$uuid'","node_class":"cl1_nc","recovery_group":"cl1_rg","servers":["10.0.0.4"],"disk_expr":"10.0.0.4:sdb,sdc,sdd,sde,sdf","full_expr":"'$full'","total_servers":4,"no_slot_map":true}'
out=$(run do_add_servers "$addin")
grep -qx "mmvdisk server configure -N 10.0.0.4 --target-node-class cl1_nc --recycle one" $fake/calls &&
    ok "add_servers: the new server configured like the class" || ko "configure: $(cat $fake/calls)"
grep -qx "mmstartup -N 10.0.0.4" $fake/calls && ! grep -q "mmstartup -N 10.0.0.1" $fake/calls &&
    ok "add_servers: only the new server restarted for the checks" || ko "slots: $(grep mmst $fake/calls)"
grep -qx "mmvdisk recoverygroup add --recovery-group cl1_rg -N 10.0.0.4 --disk-list 10.0.0.4:sdb,sdc,sdd,sde,sdf" $fake/calls &&
    grep -qx "mmvdisk recoverygroup add --recovery-group cl1_rg --complete-node-add" $fake/calls &&
    ok "add_servers: added, and completed when the callback did not after the rebalance" || ko "add: $(grep recoverygroup $fake/calls)"
grep -q "mmvdisk server list --node-class cl1_nc --disk-list $full --disk-topology -Y" $fake/calls &&
    ok "add_servers: the topology of every server, the new one too" || ko "topology: $(cat $fake/calls)"
res=$(sed -n 's/^RESULT //p' <<<"$out")
[ "$(jq '.pdisks | length' <<<"$res")" = 20 ] && [ "$(jq -r '.pdisks[] | select(.name == "n004p001") | .ip' <<<"$res")" = 10.0.0.4 ] &&
    ok "add_servers: every pdisk, the new server's by its address" || ko "pdisks: $out"
: >$fake/calls
out=$(run do_add_servers "$addin")
! grep -q "server configure\|recoverygroup add" $fake/calls && grep -q '^RESULT' <<<"$out" &&
    ok "add_servers again: found done, nothing added" || ko "again: $out / $(cat $fake/calls)"
# a server whose topology differs
reset
cp $fake/new_pdisks $fake/new_pdisks.keep
echo "ECE 4 HDD" >$fake/topo-n4.cl
out=$(run do_add_servers "$addin")
grep -q '^FAILED the servers have different disk topologies' <<<"$out" && ! grep -q "recoverygroup add" $fake/calls &&
    ok "add_servers: a different topology stops it before the add" || ko "topology differs: $out"
rm -f $fake/topo-n4.cl
# server configure that does not put it into the class: added to it
reset
touch $fake/configure_noadd
out=$(run do_add_servers "$addin")
grep -qx "mmvdisk nodeclass add --node-class cl1_nc -N 10.0.0.4" $fake/calls && grep -q '^RESULT' <<<"$out" &&
    ok "add_servers: put into the class when configuring did not" || ko "class add: $out"

# ---- remove_server: the file systems' share, the group, the class ----
reset
printf 'n4.cl\n' >>$fake/nc
cat $fake/new_pdisks >>$fake/pdisks
printf 'cl1_vs yes fs1 cl1_rg\ncl1_vs2 yes fs1 cl1_rg\nother yes fs9 other_rg\n' >$fake/vs
out=$(run do_remove_server '{"cluster_uuid":"'$uuid'","node_class":"cl1_nc","recovery_group":"cl1_rg","ip":"10.0.0.4"}')
grep -n . $fake/calls | grep -E "filesystem delete|recoverygroup delete|nodeclass delete" | cut -d: -f2- >$fake/order
[ "$(cat $fake/order)" = "mmvdisk filesystem delete --file-system fs1 --vdisk-set cl1_vs,cl1_vs2 --recovery-group cl1_rg -N 10.0.0.4 --confirm
mmvdisk recoverygroup delete --recovery-group cl1_rg -N 10.0.0.4 --confirm
mmvdisk nodeclass delete --node-class cl1_nc -N 10.0.0.4" ] && ! grep -q n4.cl $fake/pdisks && grep -q '^RESULT {}' <<<"$out" &&
    ok "remove_server: the file system's members, the group, the class, in that order" || ko "remove: $out / $(cat $fake/order)"
: >$fake/calls
out=$(run do_remove_server '{"cluster_uuid":"'$uuid'","node_class":"cl1_nc","recovery_group":"cl1_rg","ip":"10.0.0.4"}')
! grep -q "delete" $fake/calls && grep -q '^RESULT' <<<"$out" && ok "remove_server again: nothing left to do" || ko "remove again: $out"

# ---- replace ----
reset
sed -i 's/^n002p002 \(.*\) ok DA1/n002p002 \1 missing DA1/' $fake/pdisks
rin='{"cluster_uuid":"'$uuid'","recovery_group":"cl1_rg","pdisk":"n002p002","ip":"10.0.0.2","device":"sdj","wwn":"0x5000NEW","slot_map":false}'
out=$(run do_replace "$rin")
grep -qx "mmvdisk pdisk replace --prepare --recovery-group cl1_rg --pdisk n002p002" $fake/calls &&
    [ "$(cat $fake/stanzas)" = "%pdisk: pdiskName=n002p002 device=//n2.cl/dev/sdj da=DA1" ] && grep -qx "mmaddpdisk cl1_rg --replace -v no" $fake/calls &&
    ok "replace without a slot map: the new device named to mmaddpdisk --replace, in the array of the old pdisk" || ko "replace: $out / $(cat $fake/calls) / $(cat $fake/stanzas 2>/dev/null)"
res=$(sed -n 's/^RESULT //p' <<<"$out")
[ "$(jq -r '.pdisks[] | select(.name == "n002p002") | .device' <<<"$res")" = sdj ] && jq -e '.pdisks[] | select(.name == "n002p002#0010")' <<<"$res" >/dev/null &&
    ok "replace: the new disk under the old name, the old one draining under a temporary name" || ko "replace result: $res"
: >$fake/calls
out=$(run do_replace "$rin")
grep -q "pdisk n002p002 is on /dev/sdj already" <<<"$out" && ! grep -q "mmaddpdisk\|pdisk replace" $fake/calls && grep -q '^RESULT' <<<"$out" &&
    ok "replace again: found done" || ko "replace again: $out"
reset
sed -i 's/^n003p001 \(.*\) ok DA1/n003p001 \1 dead DA1/' $fake/pdisks
echo "n3.cl sdk" >$fake/slot_new
out=$(run do_replace '{"cluster_uuid":"'$uuid'","recovery_group":"cl1_rg","pdisk":"n003p001","ip":"10.0.0.3","device":"sdk","wwn":"","slot_map":true}')
grep -qx "mmvdisk pdisk replace --recovery-group cl1_rg --pdisk n003p001 -v no" $fake/calls && ! grep -q mmaddpdisk $fake/calls && grep -q '^RESULT' <<<"$out" &&
    ok "replace with a slot map: mmvdisk finds the disk in the slot" || ko "slot replace: $out / $(cat $fake/calls)"
out=$(run do_replace '{"cluster_uuid":"'$uuid'","recovery_group":"cl1_rg","pdisk":"n001p001","ip":"10.0.0.1","device":"sdz","wwn":"","slot_map":false}')
grep -q '^FAILED pdisk n001p001 is working (state ok): only a failed pdisk is replaced' <<<"$out" && ok "replace: a working pdisk is refused" || ko "working: $out"
out=$(run do_replace '{"cluster_uuid":"'$uuid'","recovery_group":"cl1_rg","pdisk":"n001p001","ip":"10.0.0.1","device":"sd z; rm","wwn":"","slot_map":false}')
grep -q '^FAILED invalid device' <<<"$out" && ok "replace: a bad device name refused" || ko "bad device: $out"

# ---- resize ----
reset
for k in 1 2 3; do echo "n00${k}p006 n$k.cl sdg naa.${k}6 ok DA1"; done >$fake/new_pdisks
printf 'cl1_vs yes fs1 cl1_rg\n' >$fake/vs
printf 'DA1 HDD 60\n' >$fake/das
expr='10.0.0.1:sdb,sdc,sdd,sde,sdf,sdg;10.0.0.2:sdb,sdc,sdd,sde,sdf,sdg;10.0.0.3:sdb,sdc,sdd,sde,sdf,sdg'
zin='{"cluster_uuid":"'$uuid'","recovery_group":"cl1_rg","disk_expr":"'$expr'","vdisk_set":"cl1_vs2","code":"4+2p","block_size":"16M","set_size_pct":80,"fs_name":"fs1"}'
out=$(run do_resize "$zin")
grep -qx "mmvdisk recoverygroup resize --recovery-group cl1_rg --disk-list $expr" $fake/calls &&
    grep -qx "mmvdisk vdiskset define --vdisk-set cl1_vs2 --recovery-group cl1_rg --code 4+2p --block-size 16m --set-size 40% --checksum-granularity 32k" $fake/calls &&
    grep -qx "mmvdisk vdiskset create --vdisk-set cl1_vs2" $fake/calls && grep -qx "mmvdisk filesystem add --file-system fs1 --vdisk-set cl1_vs2" $fake/calls &&
    ok "resize: the group takes the disks, a vdisk set of the free share (80% - 40% used), 32K checksums for 16M, into the file system" ||
    ko "resize: $out / $(cat $fake/calls)"
[ "$(sed -n 's/^RESULT //p' <<<"$out" | jq '.pdisks | length')" = 18 ] && ok "resize: every pdisk reported" || ko "resize pdisks: $out"
: >$fake/calls
out=$(run do_resize "$zin")
! grep -q "resize --recovery-group\|vdiskset define\|vdiskset create\|filesystem add" $fake/calls && grep -q '^RESULT' <<<"$out" &&
    ok "resize again: found done" || ko "resize again: $out / $(cat $fake/calls)"
# the definition does not fit at the computed share: 5 points less at a time
reset
for k in 1 2 3; do echo "n00${k}p006 n$k.cl sdg naa.${k}6 ok DA1"; done >$fake/new_pdisks
printf 'cl1_vs yes fs1 cl1_rg\n' >$fake/vs
printf 'DA1 HDD 60\n' >$fake/das
echo 30 >$fake/define_max
out=$(run do_resize "$zin")
grep -q -- "--set-size 30%" $fake/calls && grep -q '^RESULT' <<<"$out" && ok "resize: a share that does not fit tried smaller (40, 35, 30)" || ko "smaller: $out / $(grep define $fake/calls)"
# nothing left of the share
reset
for k in 1 2 3; do echo "n00${k}p006 n$k.cl sdg naa.${k}6 ok DA1"; done >$fake/new_pdisks
printf 'cl1_vs yes fs1 cl1_rg\n' >$fake/vs
printf 'DA1 HDD 22\n' >$fake/das
out=$(run do_resize "$zin")
grep -q '^FAILED the new disks leave 2% of DA1 for a vdisk set' <<<"$out" && ok "resize: too little new capacity refused" || ko "too little: $out"

# ---- mixed media: a vdisk set per array, the placement rule ----
reset
rm -f $fake/vs
printf 'DA1 HDD 100\nDA2 NVMe 100\n' >$fake/das
vsin='{"cluster_uuid":"'$uuid'","recovery_group":"cl1_rg","sets":[{"vdisk_set":"cl1_vs","code":"8+2p","block_size":"8M","set_size_pct":80,"da_type":"hdd","nsd_usage":"dataOnly","storage_pool":"data"},{"vdisk_set":"cl1_vsm","code":"3WayReplication","block_size":"1M","set_size_pct":80,"da_type":"nvme","nsd_usage":"metadataOnly","storage_pool":"system"}]}'
out=$(run do_create_vs "$vsin")
grep -qx "mmvdisk vdiskset define --vdisk-set cl1_vs --recovery-group cl1_rg --code 8+2p --block-size 8m --set-size 80% --checksum-granularity 32k --declustered-array DA1 --nsd-usage dataOnly --storage-pool data" $fake/calls &&
    grep -qx "mmvdisk vdiskset define --vdisk-set cl1_vsm --recovery-group cl1_rg --code 3WayReplication --block-size 1m --set-size 80% --declustered-array DA2 --nsd-usage metadataOnly --storage-pool system" $fake/calls &&
    grep -q '^RESULT {}' <<<"$out" && ok "create_vs mixed: data on the HDD array, metadata on the NVMe array" || ko "mixed vs: $out / $(cat $fake/calls)"
printf 'DA1 HDD 100\nDA2 SSD 100\n' >$fake/das; rm -f $fake/vs; : >$fake/calls
run do_create_vs "$vsin" >/dev/null
grep -q -- "--vdisk-set cl1_vsm .* --declustered-array DA2" $fake/calls && ok "create_vs mixed: an NVMe set goes on an array mmvdisk calls SSD" || ko "ssd/nvme: $(cat $fake/calls)"
out=$(run do_create_fs '{"cluster_uuid":"'$uuid'","fs_name":"fs1","vdisk_sets":["cl1_vs","cl1_vsm"],"data_pool":"data","mount_point":"/gpfs/fs1"}')
grep -qx "mmvdisk filesystem create --file-system fs1 --vdisk-set cl1_vs,cl1_vsm --mmcrfs -T /gpfs/fs1 -A yes -Q yes" $fake/calls &&
    [ "$(cat $fake/policy)" = "RULE 'default' SET POOL 'data'" ] && grep -q '^RESULT' <<<"$out" &&
    ok "create_fs mixed: both vdisk sets, files placed in the data pool" || ko "mixed fs: $out / $(cat $fake/calls)"
: >$fake/calls
run do_create_fs '{"cluster_uuid":"'$uuid'","fs_name":"fs1","vdisk_sets":["cl1_vs","cl1_vsm"],"data_pool":"data","mount_point":"/gpfs/fs1"}' >/dev/null
! grep -q mmchpolicy $fake/calls && ok "create_fs again: the rule is there, not installed twice" || ko "policy twice: $(cat $fake/calls)"
reset; rm -f $fake/vs
printf 'DA1 HDD 100\n' >$fake/das
out=$(run do_create_vs '{"cluster_uuid":"'$uuid'","recovery_group":"cl1_rg","sets":[{"vdisk_set":"cl1_vs","code":"4+2p","block_size":"4M","set_size_pct":80,"da_type":"ssd"}]}')
grep -q '^FAILED recovery group cl1_rg has no declustered array of ssd media' <<<"$out" && ok "create_vs: no array of the media fails" || ko "no array: $out"

# ---- slots: the slot map made by ecedrivemapping where it is missing ----
reset
rm -rf $fake/gems-*
mkdir -p $fake/gems-10.0.0.2 && touch $fake/gems-10.0.0.2/slotmap.yaml
out=$(run do_slots '{"cluster_uuid":"'$uuid'","node_class":"cl1_nc","slot_mode":"lmr","slot_range":[0,23]}')
grep -q "ecedrivemapping --mode lmr --slotrange 0 23 --force on 10.0.0.1" $fake/calls && grep -q "on 10.0.0.3" $fake/calls &&
    ! grep -q "on 10.0.0.2" $fake/calls && grep -q '^RESULT {"slot_check": "on"}' <<<"$out" &&
    ok "slots: ecedrivemapping on the servers without a map, a map made by hand kept" || ko "drivemapping: $out / $(cat $fake/calls)"
out=$(run do_slots '{"cluster_uuid":"'$uuid'","node_class":"cl1_nc","slot_mode":"lmr","slot_range":[9,2]}')
grep -q '^FAILED invalid slot range' <<<"$out" && ok "slots: a bad range refused" || ko "range: $out"

# ---- the upgrade of a recovery group server: suspended in the group, resumed, waited for ----
reset
upin='{"cluster_uuid":"'$uuid'","version":"6.0.1-1","filesystems":[],"ece":{"recovery_group":"cl1_rg"},"debs":{}}'
# What the script writes before it suspends this server and removes once it resumed it
marker=$(bash -c "source $S/../../cloudrc >/dev/null 2>&1; echo \$run_dir")/storage/$uuid/rg-suspended
rm -f $marker
cat >$fake/dpkg-query <<'EOF'
#!/bin/bash
cat $FAKE/gpfs_version 2>/dev/null || echo 6.0.0-2
EOF
cat >$fake/mmbuildgpl <<'EOF'
#!/bin/bash
echo "mmbuildgpl" >>$FAKE/calls
EOF
cat >$fake/mmlsmgr <<'EOF'
#!/bin/bash
EOF
chmod +x $fake/*
uprun() {
    (
        cd $S
        source <(sed -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' gpfs_upgrade.sh)
        export PATH=$fake:$PATH
        gpfs_bin=$fake
        stc_progress() { echo "PROGRESS $*"; }
        stc_result() { echo "RESULT $*"; }
        stc_fail() { echo "FAILED $*"; exit 1; }
        stc_lock() { :; }
        sleep() { :; }
        virsh() { :; }
        gpfs_install_packages() { echo "install" >>$FAKE/calls; echo 6.0.1-1 >$FAKE/gpfs_version; }
        touch() { :; }
        stc_main <<<"$1"
    )
}
echo "DA1 rebuild-2r" >$fake/da_bg
out=$(uprun "$upin")
grep -q '^FAILED recovery group cl1_rg is not ready for a server to go: rebuilding: DA1 (rebuild-2r)' <<<"$out" && ! grep -q suspend $fake/calls &&
    ok "upgrade: a server does not go while its group rebuilds" || ko "rebuild: $out"
echo "DA1 scrub" >$fake/da_bg
: >$fake/calls; rm -f $fake/gpfs_version
out=$(uprun "$upin")
grep -n . $fake/calls | grep -E "suspend|install|mmbuildgpl|resume|mmshutdown|mmstartup" | cut -d: -f2- >$fake/order
[ "$(cat $fake/order)" = "mmvdisk recoverygroup change --recovery-group cl1_rg --suspend -N n1.cl --window 60
install
mmbuildgpl
mmvdisk recoverygroup change --recovery-group cl1_rg --resume -N n1.cl" ] && grep -q '^RESULT' <<<"$out" &&
    ok "upgrade: suspended in the group instead of mmshutdown, resumed instead of mmstartup" || ko "upgrade: $out / $(cat $fake/order)"
# a pdisk that does not come back after the resume
: >$fake/calls; rm -f $fake/gpfs_version; echo "DA1 scrub" >$fake/da_bg
cp $fake/mmvdisk $fake/mmvdisk.keep
sed -i 's|\*--resume\*) echo no >$FAKE/suspended; echo active >$FAKE/local_state ;;|*--resume*) echo no >$FAKE/suspended; echo active >$FAKE/local_state; sed -i "s/^n001p001 \\(.*\\) ok DA1/n001p001 \\1 diagnosing DA1/" $FAKE/pdisks ;;|' $fake/mmvdisk
out=$(uprun "$upin")
grep -q '^FAILED recovery group cl1_rg is not ready 30 minutes after the upgrade: pdisks not ok: n001p001 (diagnosing)' <<<"$out" &&
    ok "upgrade: the next server waits until the pdisks of this one are back" || ko "not back: $out"
mv $fake/mmvdisk.keep $fake/mmvdisk
# a retry with GPFS down and the server suspended: resumed, not started
: >$fake/calls; echo yes >$fake/suspended; echo down >$fake/local_state; echo 6.0.1-1 >$fake/gpfs_version
sed -i 's/ diagnosing DA1/ ok DA1/' $fake/pdisks
out=$(uprun "$upin")
grep -q -- "--resume -N n1.cl" $fake/calls && ! grep -q mmstartup $fake/calls && grep -q '^RESULT' <<<"$out" &&
    ok "upgrade retry: a suspended server is resumed" || ko "retry: $out / $(cat $fake/calls)"
# review 2026-10-07: a query that fails is no answer. mmvdisk asks the daemon: with it down here, list commands fail
sed -i '2i [ -f $FAKE/vdisk_down ] \&\& [[ "$*" == *list* ]] \&\& exit 1' $fake/mmvdisk
# the state of the group can not be read before going: the server stays (taking a second one out could lose data)
: >$fake/calls; rm -f $fake/gpfs_version $fake/suspended $marker; echo active >$fake/local_state; touch $fake/vdisk_down
out=$(uprun "$upin")
grep -q '^FAILED recovery group cl1_rg is not ready for a server to go: the pdisks of cl1_rg can not be read' <<<"$out" && ! grep -q suspend $fake/calls &&
    ok "upgrade: a group whose state can not be read keeps its servers" || ko "unreadable: $out / $(cat $fake/calls)"
# GPFS down here after a failure in between, mmvdisk can not be asked: the marker says it was suspended, it is resumed
: >$fake/calls; echo yes >$fake/suspended; echo down >$fake/local_state; echo 6.0.1-1 >$fake/gpfs_version
mkdir -p $(dirname $marker); echo cl1_rg >$marker
rm -f $fake/vdisk_down
cp $fake/mmvdisk $fake/mmvdisk.keep
# the daemon answers again once the server is resumed
sed -i 's|\*--resume\*) echo no >$FAKE/suspended; echo active >$FAKE/local_state ;;|*--resume*) echo no >$FAKE/suspended; echo active >$FAKE/local_state; rm -f $FAKE/vdisk_down ;;|' $fake/mmvdisk
touch $fake/vdisk_down
out=$(uprun "$upin")
grep -q -- "--resume -N n1.cl" $fake/calls && ! grep -q mmstartup $fake/calls && grep -q '^RESULT' <<<"$out" && [ ! -f $marker ] &&
    ok "upgrade retry: the marker resumes the server mmvdisk can not be asked about, and goes" || ko "marker: $out / $(cat $fake/calls)"
# no marker and mmvdisk can not be asked: neither started nor resumed blindly, the admin looks
: >$fake/calls; echo yes >$fake/suspended; echo down >$fake/local_state; touch $fake/vdisk_down
out=$(uprun "$upin")
grep -q '^FAILED whether this server is suspended in recovery group cl1_rg can not be told here' <<<"$out" && ! grep -q -- "--resume\|mmstartup" $fake/calls &&
    ok "upgrade retry: an unknown suspension is not guessed" || ko "unknown: $out / $(cat $fake/calls)"
mv $fake/mmvdisk.keep $fake/mmvdisk
sed -i '2d' $fake/mmvdisk
rm -f $fake/vdisk_down $marker
# a client of the erasure code cluster: stopped and started the plain way
: >$fake/calls; rm -f $fake/gpfs_version $fake/suspended; echo active >$fake/local_state
out=$(uprun '{"cluster_uuid":"'$uuid'","version":"6.0.1-1","filesystems":[],"debs":{}}')
grep -qx "mmshutdown " $fake/calls && grep -qx "mmstartup " $fake/calls && ! grep -q suspend $fake/calls &&
    ok "upgrade: a host that serves no recovery group stops plainly" || ko "client upgrade: $out / $(cat $fake/calls)"

# ---- finalize: the group takes the feature version of the release ----
: >$fake/calls
cat >$fake/mmchfs <<'EOF'
#!/bin/bash
EOF
chmod +x $fake/mmchfs
out=$(run do_finalize '{"cluster_uuid":"'$uuid'","filesystems":[],"recovery_group":"cl1_rg"}' gpfs_cluster.sh)
grep -q "^mmchconfig release=LATEST" $fake/calls && grep -qx "mmvdisk recoverygroup change --recovery-group cl1_rg --version LATEST" $fake/calls &&
    grep -q '^RESULT {"release":"6.0.0.2"}' <<<"$out" && ok "finalize: the cluster, then the group's version" || ko "finalize: $out / $(cat $fake/calls)"

# ---- readiness of a server in the precheck ----
ready() {
    (
        cd $S
        source ../../cloudrc
        source ./backends/gpfs.sh
        input='{"peers":["127.0.0.1"]}'
        item() { echo "ITEM $1 $2 $3"; }
        backend_readiness "$1"
    )
}
out=$(ready '{"min_mem_mib":99999999,"min_cores":1,"min_link_mbps":25000,"bare_metal":true,"strict":false}')
grep -q '^ITEM ece_memory fail' <<<"$out" && grep -q '^ITEM ece_cores ok' <<<"$out" && grep -q '^ITEM ece_network warn' <<<"$out" &&
    ok "readiness: too little memory fails, what IBM supports only warns" || ko "readiness: $out"
out=$(ready '{"min_mem_mib":1,"min_cores":4096,"min_link_mbps":25000,"bare_metal":true,"strict":true}')
grep -q '^ITEM ece_memory ok' <<<"$out" && grep -q '^ITEM ece_cores fail' <<<"$out" && grep -q '^ITEM ece_network fail' <<<"$out" &&
    ok "readiness strict: what IBM supports fails" || ko "strict: $out"
virt=$(systemd-detect-virt 2>/dev/null)
if [ -n "$virt" ] && [ "$virt" != none ]; then
    grep -q '^ITEM ece_bare_metal fail' <<<"$out" && ok "readiness strict: a virtual machine ($virt) fails" || ko "virt: $out"
fi

rm -rf $fake /opt/cloudland/run/storage/$uuid
echo "$pass passed, $fail failed"
[ $fail = 0 ]
