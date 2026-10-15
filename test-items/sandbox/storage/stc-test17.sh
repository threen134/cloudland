#!/bin/bash
# sandbox tests of GPFS remote mounts (shared-storage-design.md §7.11) with stub mm commands: the key made once, the
# owner letting the other cluster in (add, then update on a retry) and granting the file system, the other cluster
# learning the owner and mounting the file system (a local file system of the name refused), the unmount and the
# revoke forgetting the other side only when told
S=/opt/cloudland/scripts/kvm/storage
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
uuid=aaaaaaaa-1717-2222-3333-444444444444
fake=$(mktemp -d)
export FAKE=$fake

cat >$fake/mmauth <<'EOF'
#!/bin/bash
echo "mmauth $*" >>$FAKE/calls
case "$1" in
    genkey) [ "$2" = commit ] && echo "PUBKEY-OF-THIS-CLUSTER" >$FAKE/id_rsa.pub; exit 0 ;;
    show)
        if [ "$2" = . ]; then
            echo "Cluster name:        acc.cl (this cluster)"
            echo "Cipher list:         $(cat $FAKE/cipher 2>/dev/null || echo '(none specified)')"
            exit 0
        fi
        [ -f $FAKE/auth-$2 ] ;;
    update)
        if [ "$2" = . ] && [ "$3" = -l ]; then
            [ -f $FAKE/cipher-fails ] && exit 1
            echo "$4" >$FAKE/cipher
        fi ;;
    add) touch $FAKE/auth-$2 ;;
    delete) rm -f $FAKE/auth-$2 ;;
esac
EOF
cat >$fake/mmremotecluster <<'EOF'
#!/bin/bash
echo "mmremotecluster $*" >>$FAKE/calls
case "$1" in
    show) [ -f $FAKE/rc-$2 ] ;;
    add) touch $FAKE/rc-$2 ;;
    delete) rm -f $FAKE/rc-$2 ;;
esac
EOF
cat >$fake/mmremotefs <<'EOF'
#!/bin/bash
echo "mmremotefs $*" >>$FAKE/calls
case "$1" in
    show) [ -f $FAKE/rfs-$2 ] ;;
    add) touch $FAKE/rfs-$2 ;;
    delete) rm -f $FAKE/rfs-$2 ;;
esac
EOF
cat >$fake/mmlscluster <<'EOF'
#!/bin/bash
echo "mmlscluster:clusterSummary:HEADER:version:reserved:reserved:clusterName:clusterId:"
echo "mmlscluster:clusterSummary:0:1:::acc.cl:4321:"
echo "mmlscluster:clusterNode:HEADER:version:reserved:reserved:nodeNumber:daemonNodeName:ipAddress:adminNodeName:designation:"
echo "mmlscluster:clusterNode:0:1:::1:a1.cl:10.0.1.1:a1:quorum:"
echo "mmlscluster:clusterNode:0:1:::2:a2.cl:10.0.1.2:a2::"
EOF
# mmlsfs: a local file system of the name when $FAKE/localfs is there
cat >$fake/mmlsfs <<'EOF'
#!/bin/bash
[ -f $FAKE/localfs ] || [ -f $FAKE/ownerfs ]
EOF
cat >$fake/mmmount <<'EOF'
#!/bin/bash
echo "mmmount $*" >>$FAKE/calls
EOF
cat >$fake/mmumount <<'EOF'
#!/bin/bash
echo "mmumount $*" >>$FAKE/calls
[ ! -f $FAKE/busy ]
EOF
# the owner's nodes and both of this cluster's mount it
cat >$fake/mmlsmount <<'EOF'
#!/bin/bash
echo "mmlsmount::HEADER:version:reserved:reserved:localDevName:realDevName:owningCluster:totalNodes:nodeIP:nodeName:"
for ip in 10.0.0.1 10.0.0.2 10.0.1.1 10.0.1.2; do echo "mmlsmount::0:1:::$1:$1:own.cl:4:$ip:n:"; done
EOF
chmod +x $fake/*

run() {
    (
        cd $S
        source <(sed -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' gpfs_remote.sh)
        gpfs_bin=$fake
        pubkey=$FAKE/id_rsa.pub
        stc_progress() { :; }
        stc_result() { echo "RESULT $*"; }
        stc_fail() { echo "FAILED $*"; exit 1; }
        stc_lock() { :; }
        sleep() { :; }
        findmnt() { [ -f $FAKE/rfs-fs1 ]; }
        stc_main <<<"$1"
    )
}
k=$(echo -n "KEY-OF-OTHER" | base64)

# ---- key ----
: >$fake/calls; rm -f $fake/id_rsa.pub
out=$(run '{"action":"key","cluster_uuid":"'$uuid'"}')
res=$(sed -n 's/^RESULT //p' <<<"$out")
grep -qx "mmauth genkey new" $fake/calls && grep -qx "mmauth genkey commit" $fake/calls && [ "$(jq -r .cluster_name <<<"$res")" = acc.cl ] &&
    [ "$(jq -r .public_key <<<"$res" | base64 -d)" = "PUBKEY-OF-THIS-CLUSTER" ] && ok "key: made once, with the cluster name" || ko "key: $out / $(cat $fake/calls)"
: >$fake/calls
run '{"action":"key","cluster_uuid":"'$uuid'"}' >/dev/null
! grep -q genkey $fake/calls && ok "key again: the key there is kept" || ko "key twice: $(cat $fake/calls)"
# The cipher list: AUTHONLY when none is set (a remote mount authenticates the clusters), once; one the admin set stays
[ "$(cat $fake/cipher 2>/dev/null)" = AUTHONLY ] && ! grep -q "mmauth update . -l" $fake/calls && ok "key: cipher list AUTHONLY, set once" || ko "cipher: $(cat $fake/cipher 2>/dev/null) / $(cat $fake/calls)"
echo AES128-GCM-SHA256 >$fake/cipher; : >$fake/calls
run '{"action":"key","cluster_uuid":"'$uuid'"}' >/dev/null
! grep -q "mmauth update . -l" $fake/calls && [ "$(cat $fake/cipher)" = AES128-GCM-SHA256 ] && ok "key: a cipher list set by the admin stays" || ko "admin cipher: $(cat $fake/calls)"
rm -f $fake/cipher; touch $fake/cipher-fails
out=$(run '{"action":"key","cluster_uuid":"'$uuid'"}')
grep -q '^FAILED mmauth update . -l AUTHONLY failed' <<<"$out" && ok "key: a cipher list that can not be set fails the step" || ko "cipher fails: $out"
rm -f $fake/cipher-fails
run '{"action":"key","cluster_uuid":"'$uuid'"}' >/dev/null

# ---- grant ----
touch $fake/ownerfs; : >$fake/calls
out=$(run '{"action":"grant","cluster_uuid":"'$uuid'","remote_name":"acc.cl","remote_key":"'$k'","filesystem":"fs1"}')
grep -q "^mmauth add acc.cl -k " $fake/calls && grep -qx "mmauth grant acc.cl -f fs1 -a rw" $fake/calls && grep -q '^RESULT {}' <<<"$out" &&
    ok "grant: the other cluster let in and granted the file system" || ko "grant: $out / $(cat $fake/calls)"
: >$fake/calls
run '{"action":"grant","cluster_uuid":"'$uuid'","remote_name":"acc.cl","remote_key":"'$k'","filesystem":"fs1"}' >/dev/null
grep -q "^mmauth update acc.cl -k " $fake/calls && ! grep -q "mmauth add" $fake/calls && ok "grant again: its key updated, not added twice" || ko "grant twice: $(cat $fake/calls)"
out=$(run '{"action":"grant","cluster_uuid":"'$uuid'","remote_name":"acc.cl; reboot","remote_key":"'$k'","filesystem":"fs1"}')
grep -q '^FAILED invalid cluster name' <<<"$out" && ok "grant: a bad cluster name refused" || ko "bad name: $out"
touch $fake/ownerfs
out=$(run '{"action":"grant","cluster_uuid":"'$uuid'","remote_name":"acc.cl","remote_key":"!!notbase64","filesystem":"fs1"}')
grep -q '^FAILED invalid key' <<<"$out" && ok "grant: a bad key refused" || ko "bad key: $out"
rm -f $fake/ownerfs

# ---- mount ----
: >$fake/calls
min='{"action":"mount","cluster_uuid":"'$uuid'","remote_name":"own.cl","remote_key":"'$k'","contacts":["10.0.0.1","10.0.0.2"],"filesystem":"fs1","mount_point":"/gpfs/fs1"}'
out=$(run "$min")
grep -q "^mmremotecluster add own.cl -n 10.0.0.1,10.0.0.2 -k " $fake/calls && grep -qx "mmremotefs add fs1 -f fs1 -C own.cl -T /gpfs/fs1 -A yes" $fake/calls &&
    grep -qx "mmmount fs1 -a" $fake/calls && grep -q '^RESULT {"mounted_nodes":2}' <<<"$out" &&
    ok "mount: the owner learnt, the file system added under its name and mount point, mounted on this cluster's 2 nodes" || ko "mount: $out / $(cat $fake/calls)"
: >$fake/calls
out=$(run "$min")
grep -q "^mmremotecluster update own.cl" $fake/calls && ! grep -q "mmremotefs add" $fake/calls && grep -q '^RESULT' <<<"$out" &&
    ok "mount again: the owner updated, the file system known already" || ko "mount twice: $out / $(cat $fake/calls)"
rm -f $fake/rfs-fs1 $fake/rc-own.cl; touch $fake/localfs
out=$(run "$min")
grep -q '^FAILED this cluster has a file system fs1 of its own' <<<"$out" && ok "mount: a local file system of the name refused" || ko "local fs: $out"
rm -f $fake/localfs
run "$min" >/dev/null

# ---- unmount and revoke ----
: >$fake/calls
out=$(run '{"action":"unmount","cluster_uuid":"'$uuid'","remote_name":"own.cl","filesystem":"fs1","forget_cluster":false}')
grep -qx "mmumount fs1 -a" $fake/calls && grep -qx "mmremotefs delete fs1" $fake/calls && ! grep -q "mmremotecluster delete" $fake/calls &&
    ok "unmount: unmounted and removed, the owner kept (another of its file systems is mounted)" || ko "unmount: $out / $(cat $fake/calls)"
: >$fake/calls
run '{"action":"unmount","cluster_uuid":"'$uuid'","remote_name":"own.cl","filesystem":"fs1","forget_cluster":true}' >/dev/null
! grep -q mmumount $fake/calls && grep -qx "mmremotecluster delete own.cl" $fake/calls && ok "unmount again with forget: the owner forgotten" || ko "forget: $(cat $fake/calls)"
touch $fake/rfs-fs1 $fake/busy
out=$(run '{"action":"unmount","cluster_uuid":"'$uuid'","remote_name":"own.cl","filesystem":"fs1","forget_cluster":true}')
grep -q '^FAILED mmumount fs1 -a failed' <<<"$out" && [ -f $fake/rfs-fs1 ] && ok "unmount: a file system in use stays" || ko "busy: $out"
rm -f $fake/busy $fake/rfs-fs1
: >$fake/calls
out=$(run '{"action":"revoke","cluster_uuid":"'$uuid'","remote_name":"acc.cl","filesystem":"fs1","forget_cluster":true}')
grep -qx "mmauth deny acc.cl -f fs1" $fake/calls && grep -qx "mmauth delete acc.cl" $fake/calls && ok "revoke: denied and the key forgotten" || ko "revoke: $out / $(cat $fake/calls)"
: >$fake/calls
out=$(run '{"action":"revoke","cluster_uuid":"'$uuid'","remote_name":"acc.cl","filesystem":"fs1","forget_cluster":true}')
grep -q "is not let in" <<<"$out" && ! grep -q "deny\|delete" $fake/calls && ok "revoke again: nothing left to do" || ko "revoke twice: $out"

rm -rf $fake
echo "$pass passed, $fail failed"
[ $fail = 0 ]
