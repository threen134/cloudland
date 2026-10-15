#!/bin/bash
# sandbox tests of S6 rotation and upgrade (shared-storage-design.md §6.6, §7.7, §8.7): the three passes of a new SSH key
# against the real sshd of the sandbox (the login checks, the public key file next to the private one), the checks of
# the GPFS rolling upgrade with stub mm commands, and the Ceph upgrade and client key commit with a stub ceph
S=/opt/cloudland/scripts/backend/storage
J=/opt/cloudland/run/storage/jobs
A=/opt/cloudland/run/async_job
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
wait_cb() { for i in $(seq 1 60); do [ -f $J/$1/callback ] && return 0; sleep 0.5; done; return 1; }
status_of() { awk -F"'" '{print $4}' $J/$1/callback; }
message_of() { cat $J/$1/message 2>/dev/null; }
base=$((RANDOM * 10 + 3))
export NODE_ID=7
cd /opt/cloudland/scripts/kvm
source ../cloudrc

# ---- the three passes of a new SSH key, against the sshd of the sandbox ----
pgrep -x sshd >/dev/null || service ssh start >/dev/null 2>&1
ip=$(ip -4 -br addr show eth0 | awk '{print $3}' | cut -d/ -f1)
u=eeeeeeee-8888-2222-3333-$(printf '%012d' $base)
tag=cloudland-storage-$u
keys=/root/.ssh/authorized_keys
cp -p $keys /root/authorized_keys.stc8-bak
k=$(mktemp -d)
ssh-keygen -q -t ed25519 -N '' -f $k/old -C old; ssh-keygen -q -t ed25519 -N '' -f $k/new -C new
known="$ip $(cut -d' ' -f1,2 /etc/ssh/ssh_host_ed25519_key.pub)"
trust() { # trust <run> <rotate> <key name> <with private key> <check>
    jq -cn --arg u $u --arg pub "$(cut -d' ' -f1,2 $k/$3.pub)" --arg priv "$(cat $k/$3)" --arg ip $ip --arg known "$known" --arg rotate "$2" \
        --argjson private $4 --argjson check $5 \
        '{cluster_uuid: $u, kind: "", public_key: $pub, from: [$ip], rotate: $rotate} + (if $private then {private_key: $priv, known_hosts: [$known]} else {} end) +
         (if $check then {check: [$ip]} else {} end)' | $S/stc_ssh_trust.sh $1
    wait_cb $1
}
logs_in() { timeout 20 ssh -i $1 -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o BatchMode=yes -o IdentitiesOnly=yes root@$ip true </dev/null >/dev/null 2>&1; }
dir=/opt/cloudland/run/storage/$u
r=$((base + 1)); trust $r "" old true true
[ "$(status_of $r)" = succeeded ] && [ $(grep -c " $tag$" $keys) = 1 ] && [ "$(cat $dir/id_ed25519.pub | cut -d' ' -f2)" = "$(cut -d' ' -f2 $k/old.pub)" ] &&
    ok "trust: the line, the private key and its public key, the login checked" || ko "trust: $(status_of $r) $(message_of $r)"
r=$((base + 2)); trust $r add new false false
[ "$(status_of $r)" = succeeded ] && [ $(grep -c " $tag-rotate$" $keys) = 1 ] && logs_in $k/old && logs_in $k/new &&
    ok "add: a second line, both keys log in" || ko "add: $(status_of $r) $(message_of $r)"
r=$((base + 3)); trust $r switch new true true
[ "$(status_of $r)" = succeeded ] && cmp -s <(ssh-keygen -y -f $dir/id_ed25519 | cut -d' ' -f2) <(cut -d' ' -f2 $k/new.pub) &&
    [ "$(cut -d' ' -f2 $dir/id_ed25519.pub)" = "$(cut -d' ' -f2 $k/new.pub)" ] && [ $(grep -c " $tag" $keys) = 2 ] &&
    ok "switch: the new private key and its public key, the lines stay, the new key checked" || ko "switch: $(status_of $r) $(message_of $r)"
r=$((base + 4)); trust $r drop new false true
[ "$(status_of $r)" = succeeded ] && [ $(grep -c " $tag$" $keys) = 1 ] && [ $(grep -c " $tag-rotate$" $keys) = 0 ] && grep " $tag$" $keys | grep -q "$(cut -d' ' -f2 $k/new.pub)" &&
    logs_in $k/new && ! logs_in $k/old && ok "drop: one line with the new key, the old one refused" || ko "drop: $(status_of $r) $(message_of $r)"
# A check that can not log in fails the pass
r=$((base + 5)); trust $r add old false false
cp $k/old $dir/id_ed25519
r=$((base + 6)); trust $r drop new false true
[ "$(status_of $r)" = failed ] && grep -q "does not log in to: $ip" <<<"$(message_of $r)" && ok "a key that does not log in fails the check" || ko "failed check: $(status_of $r) $(message_of $r)"
# A trust written without rotation removes what an aborted rotation left
r=$((base + 7)); trust $r add old false false
r=$((base + 8)); trust $r "" new true true
[ "$(status_of $r)" = succeeded ] && [ $(grep -c " $tag-rotate$" $keys) = 0 ] && ok "a plain trust drops the line of an aborted rotation" || ko "plain trust: $(message_of $r)"
r=$((base + 9)); jq -cn --arg u $u '{cluster_uuid: $u, remove: true}' | $S/stc_ssh_trust.sh $r; wait_cb $r
[ $(grep -c "$tag" $keys) = 0 ] && [ ! -f $dir/id_ed25519 ] && [ ! -f $dir/id_ed25519.pub ] && ok "remove: both lines and the key files go" || ko "remove: $(grep -c "$tag" $keys)"
cp -p /root/authorized_keys.stc8-bak $keys; rm -rf $k $dir /root/authorized_keys.stc8-bak

# ---- a host leaving a cluster removes the line of an aborted rotation too ----
lu=dddddddd-8888-2222-3333-$(printf '%012d' $base)
cp -p $keys /root/authorized_keys.stc8-leave
echo "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOLDleaveoldleaveoldleaveoldleaveoldleaveol cloudland-storage-$lu" >>$keys
echo "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINEWleavenewleavenewleavenewleavenewleavene cloudland-storage-$lu-rotate" >>$keys
r=$((base + 20))
jq -cn --arg u $lu '{cluster_uuid: $u, kind: "none", wipe: [], purge: false}' | $S/stc_leave.sh $r; wait_cb $r
[ "$(status_of $r)" = succeeded ] && ! grep -q "cloudland-storage-$lu" $keys && ok "leave: the line of the cluster and the one of an aborted rotation both go" ||
    ko "leave: $(status_of $r) $(message_of $r) / $(grep -c "cloudland-storage-$lu" $keys)"
cp -p /root/authorized_keys.stc8-leave $keys; rm -f /root/authorized_keys.stc8-leave

# ---- the client key of a Ceph host: the fallback to the pending key of an aborted rotation, and a failed rekey ----
cc=$(mktemp -d)
cat >$cc/ceph <<EOF
#!/bin/bash
conf=""; prev=""
for a in "\$@"; do [ "\$prev" = "--conf" ] && conf=\$a; prev=\$a; done
keyring=\$(awk '\$1 == "keyring" {print \$3}' \$conf)
key=\$(awk '\$1 == "key" {print \$3}' \$keyring)
echo "\$key" >>$cc/used
[ -f $cc/down ] && { echo "monclient(hunting): authenticate timed out after 300"; exit 1; }
grep -qx "\$key" $cc/accepted || { echo "[errno 13] RADOS permission denied (error connecting to the cluster)"; exit 1; }
case "\$*" in
    *fsid*) echo $(jq -rn '"cccccccc-8888-2222-3333-444444444444"') ;;
    *health*) echo '{"status":"HEALTH_OK"}' ;;
    *version*) echo "ceph version 19.2.3 (abc) squid (stable)" ;;
esac
EOF
cat >$cc/virsh <<EOF
#!/bin/bash
case "\$1" in
    secret-set-value) [ -f $cc/virshfail ] && exit 1; prev=""; for a in "\$@"; do [ "\$prev" = "--file" ] && cp "\$a" $cc/secret; prev=\$a; done ;;
esac
exit 0
EOF
chmod +x $cc/ceph $cc/virsh
cu=cccccccc-8888-2222-3333-$(printf '%012d' $base)
old=AQBoldoldoldoldoldoldoldoldoldoldoldold==; new=AQBnewnewnewnewnewnewnewnewnewnewnewnew==
client() { # client <run> <action> <key> [alt]
    jq -cn --arg u $cu --arg a $2 --arg k $3 --arg alt "${4:-}" '{action: $a, cluster_uuid: $u, fsid: "cccccccc-8888-2222-3333-444444444444",
        mon_addrs: ["10.0.0.1:6789"], client_user: "cloudland", client_key: $k, secret_uuid: $u} + (if $alt != "" then {client_key_alt: $alt} else {} end)' |
        PATH=$cc:$PATH $S/ceph_client.sh $1
    wait_cb $1
}
keyring_key() { awk '$1 == "key" {print $3}' /etc/ceph/$cu.client.cloudland.keyring; }
echo $old >$cc/accepted
r=$((base + 21)); client $r setup $old $new
[ "$(status_of $r)" = succeeded ] && [ "$(jq -r .key $J/$r/result.json)" = current ] && [ "$(keyring_key)" = $old ] && ! grep -qx $new $cc/used &&
    ok "setup: the recorded key works, the pending one is never used" || ko "setup current: $(status_of $r) $(message_of $r) / $(cat $cc/used)"
echo $new >$cc/accepted; rm -f $cc/used
r=$((base + 22)); client $r setup $old $new
[ "$(status_of $r)" = succeeded ] && [ "$(jq -r .key $J/$r/result.json)" = alt ] && [ "$(keyring_key)" = $new ] && [ "$(cat $cc/secret)" = $new ] &&
    ok "setup: the cluster refuses the recorded key, the pending key of the aborted rotation is taken and said so" || ko "setup alt: $(status_of $r) $(message_of $r)"
touch $cc/down; rm -f $cc/used
r=$((base + 23)); client $r setup $old $new
[ "$(status_of $r)" = failed ] && [ "$(grep -c . $cc/used)" = 1 ] && ok "setup: a cluster that does not answer is not tried with the pending key" || ko "setup down: $(status_of $r) / $(cat $cc/used)"
# A rekey that does not get through puts the key the host had back
echo $old >$cc/accepted; rm -f $cc/down
r=$((base + 24)); client $r setup $old
touch $cc/down
r=$((base + 25)); client $r rekey $new
[ "$(status_of $r)" = failed ] && grep -q "the key before is back" <<<"$(message_of $r)" && [ "$(keyring_key)" = $old ] && [ "$(cat $cc/secret)" = $old ] &&
    ok "rekey failed: the key the host had is back in the keyring and the secret" || ko "rekey failed: $(status_of $r) $(message_of $r) / $(keyring_key)"
# The secret can not be set: the keyring, the key of the tools of the host, keeps the key it had
rm -f $cc/down; touch $cc/virshfail
r=$((base + 27)); client $r rekey $new
[ "$(status_of $r)" = failed ] && [ "$(keyring_key)" = $old ] && ok "rekey: the secret could not be set, the keyring kept its key" || ko "rekey virsh: $(status_of $r) $(message_of $r) / $(keyring_key)"
rm -f $cc/virshfail; echo $new >$cc/accepted
r=$((base + 26)); client $r rekey $new
[ "$(status_of $r)" = succeeded ] && [ "$(keyring_key)" = $new ] && ok "rekey: the new key in the keyring" || ko "rekey: $(status_of $r) $(message_of $r)"
rm -f /etc/ceph/$cu.conf /etc/ceph/$cu.client.cloudland.keyring; rm -rf $cc

# ---- GPFS rolling upgrade: the checks before GPFS stops on a host ----
fake=$(mktemp -d)
cat >$fake/mmlsfs <<'EOF'
#!/bin/bash
echo "mmlsfs::HEADER:version:reserved:reserved:deviceName:fieldName:data:remarks:"
echo "mmlsfs::0:1:::$1:defaultMountPoint:%2Fgpfs-sb8%2F$1::"
EOF
cat >$fake/mmlsdisk <<EOF
#!/bin/bash
echo "mmlsdisk::HEADER:version:reserved:reserved:nsdName:driverType:sectorSize:failureGroup:metadata:data:status:availability:diskID:storagePool:remarks:"
echo "mmlsdisk::0:1:::d1:nsd:512:1:Yes:Yes:ready:up:1:system::"
[ -f $fake/down ] && echo "mmlsdisk::0:1:::d2:nsd:512:2:Yes:Yes:ready:down:2:system::" || echo "mmlsdisk::0:1:::d2:nsd:512:2:Yes:Yes:ready:up:2:system::"
EOF
cat >$fake/virsh <<EOF
#!/bin/bash
case "\$1" in
    list) echo inst-1; echo inst-2 ;;
    dumpxml) [ "\$2" = inst-1 ] && echo "<source file='/gpfs-sb8/fs1/cl_x/volumes/volume-1.disk'/>" || echo "<source file='/opt/cloudland/cache/volume/volume-2.disk'/>" ;;
esac
EOF
chmod +x $fake/*
gup() { ( cd storage && source ../../cloudrc && source ../storage_lib.sh && source ./stc_lib.sh && source ./backends/gpfs.sh && gpfs_bin=$fake && PATH=$fake:$PATH
    eval "$(sed -n '/^function mount_points()/,/^}/p; /^function local_users()/,/^}/p; /^function disks_not_up()/,/^}/p' gpfs_upgrade.sh)"
    input='{"filesystems":["fs1"]}'; "$@" ) }
[ "$(gup mount_points)" = "/gpfs-sb8/fs1" ] && ok "upgrade: the mount point decoded from mmlsfs -T -Y" || ko "mount points: $(gup mount_points)"
[ "$(gup local_users | xargs)" = "inst-1" ] && ok "upgrade: the running domain with a disk in the file system is found, the local one is not" || ko "users: $(gup local_users)"
[ -z "$(gup disks_not_up)" ] && touch $fake/down && [ "$(gup disks_not_up)" = "fs1/d2 (ready, down)" ] && ok "upgrade: a disk down is found" || ko "disks: $(gup disks_not_up)"
rm -rf $fake

# ---- GPFS rolling upgrade: the manager roles go to another node before GPFS stops (real -Y formats of work-x) ----
mf=$(mktemp -d)
cat >$mf/mmgetstate <<'EOF'
#!/bin/bash
echo "mmgetstate::HEADER:version:reserved:reserved:nodeName:nodeNumber:state:quorum:nodesUp:totalNodes:remarks:cnfsState:daemonShortName:daemonNodeName:"
echo "mmgetstate::0:1:::sb1:1:active:2:3:4:quorum node:(undefined):sb1:sb1:"
EOF
cat >$mf/mmlscluster <<'EOF'
#!/bin/bash
echo "mmlscluster:clusterNode:HEADER:version:reserved:reserved:nodeNumber:daemonNodeName:ipAddress:adminNodeName:designation:otherNodeRoles:adminLoginName:otherNodeRolesAlias"
echo "mmlscluster:clusterNode:0:1:::1:sb1:10.0.0.1:sb1:quorumManager:::"
echo "mmlscluster:clusterNode:0:1:::2:sb2:10.0.0.2:sb2:quorumManager:::"
echo "mmlscluster:clusterNode:0:1:::3:sb3:10.0.0.3:sb3:quorumManager:::"
echo "mmlscluster:clusterNode:0:1:::4:sb4:10.0.0.4:sb4::::"
EOF
cat >$mf/mmlsmount <<EOF
#!/bin/bash
echo "mmlsmount::HEADER:version:reserved:reserved:localDevName:realDevName:owningCluster:totalNodes:nodeIP:nodeName:clusterName:mountMode:"
for n in \$(cat $mf/mounted); do echo "mmlsmount::0:1:::fs1:fs1:c:3:10.0.0.\${n#sb}:\$n:c:RW:"; done
EOF
cat >$mf/mmlsmgr <<EOF
#!/bin/bash
echo "mmlsmgr:clusterManager:HEADER:version:reserved:reserved:manager:"
echo "mmlsmgr:filesystemManager:HEADER:version:reserved:reserved:filesystem:manager:managerIP:"
echo "mmlsmgr:clusterManager:0:1:::\$(cat $mf/cmgr):"
echo "mmlsmgr:filesystemManager:0:1:::fs1:\$(cat $mf/fmgr):10.0.0.9:"
EOF
cat >$mf/mmchmgr <<EOF
#!/bin/bash
echo "\$*" >>$mf/calls
EOF
chmod +x $mf/*
mgr() { rm -f $mf/calls; echo "$1" >$mf/mounted; echo "$2" >$mf/cmgr; echo "$3" >$mf/fmgr
    ( cd storage && source ../../cloudrc && source ./backends/gpfs.sh && gpfs_bin=$mf
      eval "$(sed -n '/^function move_managers()/,/^}/p' gpfs_upgrade.sh)"; move_managers ) 2>&1; }
out=$(mgr "sb1 sb2 sb3" sb1 sb1)
[ "$(cat $mf/calls 2>/dev/null | xargs)" = "fs1 sb2 -c sb2" ] && ok "upgrade: the file system and cluster manager roles of this node go to an active quorum node" || ko "move both: $(cat $mf/calls 2>/dev/null) / $out"
out=$(mgr "sb1 sb2 sb3" sb3 sb2)
[ ! -s $mf/calls ] && ok "upgrade: no role here, nothing moves" || ko "move none: $(cat $mf/calls)"
out=$(mgr "sb1" sb1 sb1)
[ ! -s $mf/calls ] && grep -q "no other active quorum node" <<<"$out" && ok "upgrade: no other node has the file system mounted, the roles stay" || ko "alone: $(cat $mf/calls 2>/dev/null) / $out"
out=$(mgr "sb1 sb4 sb3" sb1 sb1)
[ "$(cat $mf/calls 2>/dev/null | xargs)" = "fs1 sb3 -c sb3" ] && ok "upgrade: a node that is not a quorum node does not take the roles" || ko "quorum only: $(cat $mf/calls 2>/dev/null)"
rm -rf $mf

# ---- GPFS finalize: mmchfs -V asks for a confirmation, the format is checked after ----
ff=$(mktemp -d)
echo "37.00 (5.2.3.0)" >$ff/format
cat >$ff/mmchconfig <<'EOF'
#!/bin/bash
echo "mmchconfig: Command successfully completed"
EOF
cat >$ff/mmchfs <<EOF
#!/bin/bash
echo "Do you want to continue?"
read a
[ -f $ff/ignore ] && exit 0
[ "\$a" = yes ] && echo "38.00 (6.0.0.0)" >$ff/format
exit 0
EOF
cat >$ff/mmlsfs <<EOF
#!/bin/bash
echo "mmlsfs::HEADER:version:reserved:reserved:deviceName:fieldName:data:remarks:"
echo "mmlsfs::0:1:::\$1:filesystemVersion:\$(cat $ff/format)::"
echo "mmlsfs::0:1:::\$1:filesystemHighestSupported:38.00 (6.0.0.0)::"
EOF
cat >$ff/mmlsconfig <<'EOF'
#!/bin/bash
echo "mmlsconfig::HEADER:version:reserved:reserved:configParameter:value:nodeList:"
echo "mmlsconfig::0:1:::minReleaseLevel:6.0.0.2::"
EOF
chmod +x $ff/*
fin() { ( cd storage && source ../../cloudrc && source ./stc_lib.sh && source ./backends/gpfs.sh && gpfs_bin=$ff
    eval "$(sed -n '/^function fs_format()/,/^}/p; /^function do_finalize()/,/^}/p' gpfs_cluster.sh)"
    stc_result() { echo "RESULT $1"; }; stc_fail() { echo "failed: $1"; exit 1; }; stc_progress() { :; }
    do_finalize '{"filesystems":["fs1"]}' ) 2>&1; }
touch $ff/ignore
out=$(fin)
grep -q "failed: fs1 is at format 37.00 (5.2.3.0) after mmchfs -V full, not 38.00 (6.0.0.0)" <<<"$out" && ok "finalize: mmchfs exiting 0 without raising the format fails" || ko "finalize ignored: $out"
rm -f $ff/ignore
out=$(fin)
grep -q 'RESULT {"release":"6.0.0.2"}' <<<"$out" && [ "$(cat $ff/format)" = "38.00 (6.0.0.0)" ] && ok "finalize: the confirmation is given, the format raised and checked" || ko "finalize: $out"
rm -rf $ff

# ---- Ceph: the upgrade and the commit of the client key, with a stub ceph ----
cs=$(mktemp -d)
cat >$cs/ceph <<EOF
#!/bin/bash
d=$cs
args="\$*"
echo "\$args" >>\$d/calls
case "\$args" in
    *"versions -f json"*) v=\$(cat \$d/version); echo "{\"overall\":{\"ceph version \$v (abc) squid (stable)\":3}}" ;;
    *"orch upgrade status"*) n=\$(cat \$d/polls 2>/dev/null || echo 0); echo \$((n + 1)) >\$d/polls
        if [ -f \$d/started ] && [ \$n -lt 2 ]; then echo "{\"target_image\":\"img:v2\",\"in_progress\":true,\"progress\":\"\$((n + 1))/3 daemons upgraded\",\"message\":\"\"}"
        elif [ -f \$d/started ]; then echo \$(cat \$d/target) >\$d/version; echo "{\"target_image\":\"img:v2\",\"in_progress\":false}"
        else echo "There are no upgrades in progress currently."; fi ;;
    *"orch upgrade start"*) touch \$d/started ;;
    *"health detail"*) [ -f \$d/failhealth ] && echo '{"checks":{"UPGRADE_FAILED_PULL":{"summary":{"message":"Upgrade: failed to pull target image"}}}}' || echo '{"checks":{}}' ;;
    *"auth get client.cloudland"*) echo "[{\"entity\":\"client.cloudland\",\"key\":\"\$(cat \$d/key)\"\$( [ -f \$d/pending ] && echo ",\"pending_key\":\"\$(cat \$d/pending)\"")}]" ;;
    *"auth commit-pending"*) [ -f \$d/pending ] && mv \$d/pending \$d/key ;;
esac
EOF
chmod +x $cs/ceph
f=ffffffff-8888-2222-3333-444444444444
cfun() { ( cd storage && source ../../cloudrc && source ../storage_lib.sh && source ./stc_lib.sh && source ./backends/ceph.sh && PATH=$cs:$PATH
    eval "$(sed -n '/^function daemon_versions()/,/^}/p; /^function upgrade_status()/,/^}/p; /^function do_upgrade()/,/^}/p; /^function client_entity()/,/^}/p; /^function do_client_commit()/,/^}/p' ceph_cluster.sh)"
    function sleep() { :; }; stc_result() { echo "RESULT $1"; }; stc_fail() { echo "failed: $1"; exit 1; }; stc_progress() { echo "PROGRESS $1 $2"; }
    fsid=$f; input=$1; shift; "$@" ) 2>&1; }
echo 2.0.0 >$cs/version; echo 2.0.0 >$cs/target
out=$(cfun '{"version":"2.0.0","image":"img:v2"}' do_upgrade)
grep -q 'RESULT {"version":"2.0.0"}' <<<"$out" && ! grep -q "upgrade start" $cs/calls && ok "Ceph upgrade: every daemon on the release already, nothing started" || ko "noop: $out"
rm -f $cs/calls $cs/polls; echo 1.0.0 >$cs/version
out=$(cfun '{"version":"2.0.0","image":"img:v2"}' do_upgrade)
grep -q "upgrade start --image img:v2" $cs/calls && grep -q "PROGRESS 65 2 of 3 daemons" <<<"$out" && grep -q 'RESULT {"version":"2.0.0"}' <<<"$out" &&
    ok "Ceph upgrade: started, followed with its progress, the daemons on the release at the end" || ko "upgrade: $out"
rm -f $cs/started $cs/polls $cs/calls; echo 1.0.0 >$cs/version; touch $cs/failhealth
out=$(cfun '{"version":"2.0.0","image":"img:v2"}' do_upgrade)
grep -q "failed: the upgrade stopped: UPGRADE_FAILED_PULL: Upgrade: failed to pull target image" <<<"$out" && ok "Ceph upgrade: an upgrade health check fails the step with its message" || ko "failed upgrade: $out"
rm -f $cs/failhealth $cs/polls; touch $cs/started; echo 0 >$cs/polls
out=$(cfun '{"version":"2.0.0","image":"img:v3"}' do_upgrade)
grep -q "failed: cephadm upgrades to img:v2 already" <<<"$out" && ok "Ceph upgrade: another upgrade going on is not taken over" || ko "other upgrade: $out"
rm -f $cs/started $cs/polls
K1=AQBoldoldoldoldoldoldoldoldoldoldoldold==; K2=AQBnewnewnewnewnewnewnewnewnewnewnewnew==
echo $K2 >$cs/key; rm -f $cs/pending
out=$(cfun "{\"client_user\":\"cloudland\",\"client_key\":\"$K2\"}" do_client_commit)
grep -q "the new one already" <<<"$out" && grep -q 'RESULT' <<<"$out" && ! grep -q commit-pending $cs/calls && ok "client commit: a host used the key, it is the key already" || ko "commit used: $out"
echo $K1 >$cs/key; echo $K2 >$cs/pending
out=$(cfun "{\"client_user\":\"cloudland\",\"client_key\":\"$K2\"}" do_client_commit)
grep -q "commit-pending client.cloudland" $cs/calls && [ "$(cat $cs/key)" = $K2 ] && grep -q RESULT <<<"$out" && ok "client commit: a pending key nobody used is committed" || ko "commit pending: $out"
echo $K1 >$cs/key; echo AQBothotherotherotherotherotherotherot== >$cs/pending
out=$(cfun "{\"client_user\":\"cloudland\",\"client_key\":\"$K2\"}" do_client_commit)
grep -q "failed: the pending key of client.cloudland is not the one the hosts were given" <<<"$out" && ok "client commit: another pending key is refused" || ko "commit other: $out"
rm -rf $cs
rm -f $A/*

echo "== $pass passed, $fail failed"
