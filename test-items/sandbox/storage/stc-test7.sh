#!/bin/bash
# sandbox tests of S6 node recovery (shared-storage-design.md §11): the heartbeat asking clapi after a boot or a gap
# (reconcile_check), which instances count as shared, node_reconcile.sh / clear_stale_vm.sh applying the answer with a
# stub virsh, and the GPFS / Ceph fences with stub mmexpelnode / ceph
A=/opt/cloudland/run/async_job
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
export NODE_ID=7
cd /opt/cloudland/scripts/kvm
source ../cloudrc
source ./storage_lib.sh
command -v xmllint >/dev/null || apt-get install -y libxml2-utils >/dev/null
boot=$(cat /proc/sys/kernel/random/boot_id)
now() { date +%s; }
reset_reconcile() { rm -f $run_dir/{heartbeat_at,reconciled_boot,reconcile_pending,reconcile_asked} $reconcile_hold_file; }

# A stub virsh with domains as files: dom-N.xml defined, state-N its state, stuck-N refuses undefine
stub=$(mktemp -d)
cat >$stub/virsh <<EOS
#!/bin/bash
d=$stub
echo "\$*" >>\$d/calls
arg_dom() { for a in "\$@"; do case "\$a" in inst-*) echo \${a#inst-}; return ;; esac; done; }
n=\$(arg_dom "\$@")
case "\$1" in
    list) for f in \$d/dom-*.xml; do [ -f "\$f" ] && { b=\${f##*/dom-}; echo inst-\${b%.xml}; }; done; echo other-domain ;;
    dumpxml) [ -f \$d/dom-\$n.xml ] && cat \$d/dom-\$n.xml || exit 1 ;;
    domstate) [ -f \$d/dom-\$n.xml ] || exit 1; cat \$d/state-\$n 2>/dev/null || echo "shut off" ;;
    dominfo) [ -f \$d/dom-\$n.xml ] ;;
    destroy) exit 0 ;;
    undefine) [ -f \$d/stuck-\$n ] && exit 1; [[ "\$*" == *rescue* ]] && exit 1; rm -f \$d/dom-\$n.xml ;;
    *) exit 0 ;;
esac
EOS
printf '#!/bin/bash\nexec "$@"\n' >$stub/sudo
chmod +x $stub/virsh $stub/sudo
export PATH=$stub:$PATH
dom() { # dom <id> <nvram path or ""> <state>
    { echo "<domain><name>inst-$1</name><os>"; [ -n "$2" ] && echo "<nvram>$2</nvram>"
      echo "</os><devices><interface type='bridge'><target dev='tap-t7$1'/></interface></devices></domain>"; } >$stub/dom-$1.xml
    echo "${3:-shut off}" >$stub/state-$1
}

# ---- reconcile_check: when the heartbeat asks, what it reports ----
eval "$(sed -n '/^function reconcile_check()/,/^}/p' report_rc.sh)"
reset_reconcile
rm -f $stub/dom-*.xml
dom 11 "" running; dom 12 ""
out=$(reconcile_check)
b64=$(grep -o "node_recovered '7' '$boot' 'boot' '[^']*'" <<<"$out" | awk -F"'" '{print $8}')
[ -n "$b64" ] && [ "$(base64 -d <<<"$b64" | jq -c .)" = '[{"id":11,"state":"running"},{"id":12,"state":"shut_off"}]' ] &&
    ok "first heartbeat of a boot asks, with the domains and their states" || ko "boot ask: $out / $(base64 -d <<<"$b64")"
out=$(reconcile_check)
[ -z "$out" ] && ok "asked again within a minute: no" || ko "asked again at once: $out"
echo "$boot $(( $(now) - 61 ))" >$run_dir/reconcile_asked
out=$(reconcile_check)
grep -q "node_recovered '7' '$boot' 'boot'" <<<"$out" && ok "no answer after a minute: asked again" || ko "retry: $out"
echo "an-earlier-boot $(now)" >$run_dir/reconcile_asked
out=$(reconcile_check)
grep -q "node_recovered '7' '$boot' 'boot'" <<<"$out" && ok "asked just before a quick reboot: asked at once all the same" || ko "quick reboot: $out"
echo "$boot" >$run_dir/reconciled_boot
out=$(reconcile_check)
[ -z "$out" ] && ok "reconciled boot, heartbeats regular: quiet" || ko "quiet: $out"
echo $(( $(now) - 120 )) >$run_dir/heartbeat_at
echo "$boot $(now)" >$run_dir/reconcile_asked
touch $run_dir/old_resource_list
out=$(reconcile_check)
grep -q "node_recovered '7' '$boot' 'reconnect'" <<<"$out" && [ -f $run_dir/reconcile_pending ] && [ ! -f $run_dir/old_resource_list ] &&
    ok "gap of two minutes: asks at once (reconnect), remembers it, reports the resources again" || ko "gap: $out $(ls $run_dir)"
out=$(reconcile_check)
[ -z "$out" ] && ok "pending reconnect, asked just now: quiet" || ko "pending: $out"
touch $run_dir/old_resource_list
echo "$boot $(( $(now) - 61 ))" >$run_dir/reconcile_asked
out=$(reconcile_check)
grep -q "node_recovered '7' '$boot' 'reconnect'" <<<"$out" && [ -f $run_dir/old_resource_list ] && ok "pending reconnect: asked again after a minute (no extra report)" || ko "pending retry: $out"

# ---- instance_shared: a disk anywhere but the builtin pool and the local pools of this host ----
eval "$(sed -n '/^function instance_shared()/,/^}/p' report_rc.sh)"
local_pool=cccccccc-7777-2222-3333-444444444444
mkdir -p $pools_dir/$local_pool
shared_case() { POOLS="$1"; instance_pools() { echo $POOLS; }; instance_shared 1; }
! shared_case "builtin" && ! shared_case "builtin $local_pool" && ! shared_case "" && ok "builtin and local pools are not shared" || ko "local counted as shared"
shared_case "dddddddd-7777-2222-3333-444444444444" && shared_case "builtin dddddddd-7777-2222-3333-444444444444" &&
    ok "a disk in a pool not mounted here as local is shared" || ko "shared not seen"
rmdir $pools_dir/$local_pool

# ---- node_reconcile.sh + clear_stale_vm.sh ----
reset_reconcile
rm -f $stub/dom-*.xml $stub/calls $stub/stuck-*
echo "reconnect 1" >$run_dir/reconcile_pending
reconcile_hold_add 101; pending_start_add 101; reconcile_hold_add 108
dom 101 ""; dom 108 "" running
mkdir -p $image_dir; dom 102 "$image_dir/inst-102_VARS.fd"; touch $image_dir/inst-102_VARS.fd; mkdir -p $xml_dir/inst-102 $cache_dir/meta; touch $cache_dir/meta/inst-102.iso
dom 103 "/gpfs-sb7/fs1/cl_t7/nvram/inst-103_VARS.fd"
dom 104 ""; touch $stub/stuck-104; mkdir -p $xml_dir/inst-104
pending_start_add 104
out=$(jq -cn --arg b "$boot" '{boot: $b, reason: "reconnect", start: [101, 105, 108],
    stale: [{id: 102, router: 0}, {id: 103, router: 0}, {id: 104, router: 0}, {id: "1;touch /tmp/t7-pwned", router: 0}, {id: 110, router: "0 x"}]}' | ./node_reconcile.sh)
grep -qx "|:-COMMAND-:| clear_stale_vm.sh '102' 'done'" <<<"$out" && grep -qx "|:-COMMAND-:| clear_stale_vm.sh '103' 'done'" <<<"$out" &&
    grep -qx "|:-COMMAND-:| clear_stale_vm.sh '104' 'error'" <<<"$out" && ok "stale copies removed and reported, a stuck one reported as error" || ko "stale: $out"
grep -qx "|:-COMMAND-:| node_reconciled '7' '$boot' 'reconnect'" <<<"$out" && [ "$(cat $run_dir/reconciled_boot)" = "$boot" ] &&
    [ ! -f $run_dir/reconcile_pending ] && ok "reconciled: boot recorded, nothing pending, reported" || ko "reconciled: $out"
! reconcile_held 101 && grep -qx 101 $cache_dir/pending_start && ok "a held instance still its own: released to the pending start list" || ko "101: $(cat $reconcile_hold_file 2>/dev/null) / $(cat $cache_dir/pending_start)"
! reconcile_held 108 && ! grep -qx 108 $cache_dir/pending_start && ok "one already running: released, not started again" || ko "108: $(cat $cache_dir/pending_start)"
! grep -qx 105 $cache_dir/pending_start && ok "one that was never held: left alone" || ko "105 queued"
grep -q -- "--nvram inst-102" $stub/calls && [ ! -f $image_dir/inst-102_VARS.fd ] && [ ! -d $xml_dir/inst-102 ] && [ ! -f $cache_dir/meta/inst-102.iso ] &&
    ok "local copy: UEFI variables, definition files and config drive removed" || ko "102: $(grep 102 $stub/calls)"
grep -q -- "--keep-nvram inst-103" $stub/calls && ok "UEFI variables in a shared pool kept for the copy running elsewhere" || ko "103: $(grep 103 $stub/calls)"
[ -d $xml_dir/inst-104 ] && ! grep -qx 104 $cache_dir/pending_start && ok "stuck one: its files kept, never started" || ko "104 files gone or queued"
[ ! -e /tmp/t7-pwned ] && ! grep -qE '1;touch|inst-110' $stub/calls && ok "an invalid id is skipped" || ko "invalid id ran"
! grep -qE "destroy inst-10[158]|undefine .*inst-10[158]" $stub/calls && ok "own instances are not touched" || ko "own touched: $(grep -E 'inst-10[158]' $stub/calls)"

# An answer to an earlier boot only cleans up
rm -f $stub/stuck-104 $stub/calls
echo "old-boot" >$run_dir/reconciled_boot
reconcile_hold_add 106; dom 106 ""; dom 107 ""
out=$(jq -cn '{boot: "an-earlier-boot", reason: "boot", start: [106], stale: [{id: 107, router: 0}]}' | ./node_reconcile.sh)
grep -qx "|:-COMMAND-:| clear_stale_vm.sh '107' 'done'" <<<"$out" && ! grep -q node_reconciled <<<"$out" && reconcile_held 106 &&
    [ "$(cat $run_dir/reconciled_boot)" = "old-boot" ] && ok "answer of an earlier boot: cleaned up, nothing released, not reconciled" || ko "earlier boot: $out"

# The evacuation source cleaned when it is back (boot "-"): the same clean-up, no reconcile
rm -f $stub/calls; dom 109 ""
out=$(jq -cn '{boot: "-", reason: "evacuated", start: [], stale: [{id: 109, router: 0}]}' | ./node_reconcile.sh)
grep -qx "|:-COMMAND-:| clear_stale_vm.sh '109' 'done'" <<<"$out" && ! grep -q node_reconciled <<<"$out" && ok "clean-up after an evacuation" || ko "evacuated: $out"

# An instance left undecided (a migration involving it goes on, clapi named it nowhere) stays held, and the host asks
# again (reason leave) once a minute; a held one whose domain went (its delete went through) is no longer waited for
reset_reconcile; rm -f $stub/calls $cache_dir/pending_start
reconcile_hold_add 111; dom 111 ""; reconcile_hold_add 112
out=$(jq -cn --arg b "$boot" '{boot: $b, reason: "boot", start: [], stale: []}' | ./node_reconcile.sh)
grep -qx "|:-COMMAND-:| node_reconciled '7' '$boot' 'boot'" <<<"$out" && reconcile_held 111 && ! reconcile_held 112 &&
    [ "$(cut -d' ' -f1 $run_dir/reconcile_pending 2>/dev/null)" = "leave" ] && [ "$(cat $run_dir/reconciled_boot)" = "$boot" ] &&
    ok "left undecided: still held and asked again (leave); one whose domain went is let go" || ko "leave: $out / $(cat $reconcile_hold_file) / $(cat $run_dir/reconcile_pending 2>/dev/null)"
echo "$boot $(( $(now) - 61 ))" >$run_dir/reconcile_asked; echo $(now) >$run_dir/heartbeat_at
out=$(reconcile_check)
grep -q "node_recovered '7' '$boot' 'leave'" <<<"$out" && ok "the next ask a minute later says leave" || ko "ask again: $out"
out=$(reconcile_check)
[ -z "$out" ] && ok "not asked again within the minute" || ko "asked twice: $out"
out=$(jq -cn --arg b "$boot" '{boot: $b, reason: "leave", start: [111], stale: []}' | ./node_reconcile.sh)
! reconcile_held 111 && [ ! -f $run_dir/reconcile_pending ] && ok "decided at last: released, nothing pending" || ko "decided: $(cat $reconcile_hold_file) / $(ls $run_dir/reconcile_pending 2>&1)"
reset_reconcile; rm -f $cache_dir/pending_start

# ---- GPFS fence: expel and let back in, with stub mm commands ----
fake=$(mktemp -d)
cat >$fake/mmlscluster <<'EOF'
#!/bin/bash
echo "mmlscluster:clusterNode:HEADER:version:reserved:reserved:nodeNumber:daemonNodeName:ipAddress:adminNodeName:designation:otherNodeRoles:adminLoginName:otherNodeRolesAlias:"
echo "mmlscluster:clusterNode:0:1:::1:sb1.cl:10.0.0.1:sb1.cl:quorumManager::::"
echo "mmlscluster:clusterNode:0:1:::2:sb2.cl:10.0.0.2:sb2.cl:quorum::::"
echo "mmlscluster:clusterNode:0:1:::3:sb22.cl:10.0.0.22:sb22.cl:::::"
EOF
# The list is "<address> (<node name>)" lines under a dashed line, as the real mmexpelnode -l prints it
cat >$fake/mmexpelnode <<EOF
#!/bin/bash
echo "\$*" >>$fake/calls
case "\$1" in
    -l) [ -f $fake/listfail ] && exit 1
        echo "Expelled Nodes"; echo "---------------------"; [ -s $fake/expelled ] && cat $fake/expelled || echo "(Empty)" ;;
    -N) echo "\$($fake/mmlscluster -Y | awk -F: -v n="\$2" '\$8 == n {print \$9}') (\$2)" >>$fake/expelled ;;
    -r) grep -v "(\$3)\$" $fake/expelled >$fake/x; mv $fake/x $fake/expelled ;;
esac
EOF
chmod +x $fake/*
gpfs_fence() { # gpfs_fence <action> <ip>: prints the result or "failed: <message>"
    ( cd storage && source ./backends/gpfs.sh && gpfs_bin=$fake
      eval "$(sed -n '/^function gpfs_daemon_name()/,/^}/p; /^function gpfs_expelled()/,/^}/p; /^function do_fence()/,/^}/p; /^function do_unfence()/,/^}/p' gpfs_cluster.sh)"
      stc_result() { echo "$1"; }; stc_fail() { echo "failed: $1"; exit 1; }; stc_progress() { :; }
      do_$1 "{\"ip\": \"$2\"}" ) 2>&1 | tail -1
}
[ "$(gpfs_fence fence 10.0.0.2)" = '{"node":"sb2.cl"}' ] && grep -qx -- "-N sb2.cl" $fake/calls && ok "GPFS fence expels the node of the address" || ko "gpfs fence: $(cat $fake/calls)"
[ "$(gpfs_fence fence 10.0.0.2)" = '{"node":"sb2.cl"}' ] && [ $(grep -c -- "^-N" $fake/calls) = 1 ] && ok "GPFS fence again: expelled already, not twice" || ko "gpfs refence: $(cat $fake/calls)"
[[ "$(gpfs_fence fence 10.0.0.9)" == "failed: 10.0.0.9 is not a node of this cluster" ]] && ok "GPFS fence of an unknown address fails" || ko "unknown address"
[[ "$(gpfs_fence unfence 10.0.0.9)" == '{"address":"10.0.0.9"}' ]] && ! grep -q -- "^-r" $fake/calls && ok "GPFS unfence of a node removed from the cluster: nothing to let back in" || ko "unfence removed: $(cat $fake/calls)"
[[ "$(gpfs_fence unfence 10.0.0.22)" == '{"node":"sb22.cl"}' ]] && ! grep -q "sb22" $fake/calls && ok "GPFS unfence of a node not expelled: nothing to do (sb2 is not sb22)" || ko "unfence other: $(cat $fake/calls)"
grep -qx "10.0.0.2 (sb2.cl)" $fake/expelled && ok "the stub lists the expelled node as the real mmexpelnode does" || ko "stub list: $(cat $fake/expelled)"
touch $fake/listfail
[[ "$(gpfs_fence unfence 10.0.0.2)" == "failed: mmexpelnode -l failed" ]] && ! grep -q -- "^-r" $fake/calls && ok "GPFS unfence fails when the list can not be read" || ko "unfence listfail: $(cat $fake/calls)"
[[ "$(gpfs_fence fence 10.0.0.22)" == "failed: mmexpelnode -l failed" ]] && ! grep -q -- "-N sb22" $fake/calls && ok "GPFS fence fails when the list can not be read" || ko "fence listfail: $(cat $fake/calls)"
rm -f $fake/listfail
[ "$(gpfs_fence unfence 10.0.0.2)" = '{"node":"sb2.cl"}' ] && grep -qx -- "-r -N sb2.cl" $fake/calls && [ ! -s $fake/expelled ] && ok "GPFS unfence lets it join again" || ko "gpfs unfence: $(cat $fake/calls)"
rm -rf $fake

# ---- Ceph fence: blocklist range, with a stub ceph ----
cs=$(mktemp -d)
cat >$cs/ceph <<EOF
#!/bin/bash
echo "\$*" >>$cs/calls
args="\$*"
case "\$args" in
    *"osd blocklist ls"*) cat $cs/list 2>/dev/null; echo "10.0.0.2:0/3112 2026-10-04T10:00:00" ; echo "cidr:10.0.0.22:0/32 2036-10-01T10:00:00"; echo "listed 3 entries" >&2 ;;
    *"osd blocklist range add "*) a=\${args##*range add }; a=\${a%%/32*}; echo "cidr:\$a:0/32 2036-10-01T10:00:00" >>$cs/list ;;
    *"osd blocklist range rm "*) [ -f $cs/norm ] && { echo "Error EACCES: access denied" >&2; exit 13; }
        a=\${args##*range rm }; a=\${a%%/32*}; grep -v "cidr:\$a:0/32" $cs/list >$cs/x; mv $cs/x $cs/list ;;
esac
EOF
chmod +x $cs/ceph
PATH=$cs:$PATH
u=aaaaaaaa-7777-2222-3333-444444444444
f=bbbbbbbb-7777-2222-3333-444444444444
ceph_fence() { # ceph_fence <input JSON>
    ( cd storage && source ./backends/ceph.sh
      eval "$(sed -n '/^function fence_ceph()/,/^}/p; /^function blocklisted()/,/^}/p; /^function do_fence()/,/^}/p; /^function do_unfence()/,/^}/p' ceph_cluster.sh)"
      stc_result() { echo "$1"; }; stc_fail() { echo "failed: $1"; exit 1; }; stc_progress() { :; }
      input=$1; uuid=$u; fsid=$f; do_$(jq -r .action <<<"$1") ) 2>&1 | tail -1
}
[ "$(ceph_fence '{"action":"fence","address":"10.0.0.2","expire":315360000}')" = '{"address":"10.0.0.2"}' ] &&
    grep -q -- "--conf /var/lib/ceph/$f/config/ceph.conf --keyring /var/lib/ceph/$f/config/ceph.client.admin.keyring osd blocklist range add 10.0.0.2/32 315360000" $cs/calls &&
    ok "Ceph fence of a managed cluster: range of the address, admin key, long expiry" || ko "ceph fence: $(cat $cs/calls)"
[[ "$(ceph_fence '{"action":"fence","address":"10.0.0.2; rm -rf /"}')" == "failed: invalid address"* ]] && ok "Ceph fence refuses an invalid address" || ko "invalid address taken"
[[ "$(ceph_fence '{"action":"fence","address":"10.0.0.3","client_user":"a b"}')" == "failed: invalid client user"* ]] && ok "Ceph fence refuses an invalid client user" || ko "invalid user taken"
rm -f $cs/calls
[ "$(ceph_fence '{"action":"fence","address":"10.0.0.3","client_user":"cloudland"}')" = '{"address":"10.0.0.3"}' ] &&
    grep -q -- "--conf /etc/ceph/$u.conf --id cloudland osd blocklist range add 10.0.0.3/32 315360000" $cs/calls &&
    ok "Ceph fence of an imported cluster: with the CloudLand client, default expiry" || ko "external fence: $(cat $cs/calls)"
touch $cs/norm
[[ "$(ceph_fence '{"action":"unfence","address":"10.0.0.3","client_user":"cloudland"}')" == *"needs its admin to run: ceph osd blocklist range rm 10.0.0.3/32"* ]] &&
    ok "Ceph unfence the client may not do: fails with the command for the admin" || ko "external unfence"
rm -f $cs/norm $cs/calls
[ "$(ceph_fence '{"action":"unfence","address":"10.0.0.2"}')" = '{"address":"10.0.0.2"}' ] && grep -q "range rm 10.0.0.2/32" $cs/calls &&
    ! grep -q "cidr:10.0.0.2:" $cs/list && ok "Ceph unfence removes the range" || ko "ceph unfence: $(cat $cs/calls)"
rm -f $cs/calls
[ "$(ceph_fence '{"action":"unfence","address":"10.0.0.2"}')" = '{"address":"10.0.0.2"}' ] && ! grep -q "range rm" $cs/calls &&
    ok "Ceph unfence of an address not listed (an entry of one client, a longer address) does nothing" || ko "unfence unlisted: $(cat $cs/calls)"
rm -rf $cs $stub
rm -f $A/*

echo "== $pass passed, $fail failed"
