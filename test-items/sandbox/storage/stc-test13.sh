#!/bin/bash
# sandbox tests of the deletion of a GPFS file system (gpfs_fs.sh delete, shared-storage-design.md §7.3) with stub mm
# commands: unmounted everywhere, deleted, its NSDs deleted; a file system or NSDs already gone (an aborted making) are
# fine; a node that keeps it mounted stops the deletion
S=/opt/cloudland/scripts/kvm/storage
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
uuid=dddddddd-1313-2424-3535-464646464646
fake=$(mktemp -d)
export FAKE=$fake

cat >$fake/mmlsfs <<'EOF'
#!/bin/bash
[ -f $FAKE/fs-$1 ]
EOF
cat >$fake/mmumount <<'EOF'
#!/bin/bash
echo "mmumount $*" >>$FAKE/calls
[ -f $FAKE/stuck ] || rm -f $FAKE/mounted-$1
EOF
cat >$fake/mmlsmount <<'EOF'
#!/bin/bash
echo "mmlsmount::HEADER:version:reserved:reserved:deviceName:realDevName:clusterName:mountMode:nodeIP:"
[ -f $FAKE/mounted-$1 ] && echo "mmlsmount::0:1:::$1:$1:c:RW:10.0.0.2:"
exit 0
EOF
cat >$fake/mmdelfs <<'EOF'
#!/bin/bash
echo "mmdelfs $*" >>$FAKE/calls
rm -f $FAKE/fs-$1
EOF
cat >$fake/mmlsnsd <<'EOF'
#!/bin/bash
cat $FAKE/nsds 2>/dev/null
EOF
cat >$fake/mmdelnsd <<'EOF'
#!/bin/bash
echo "mmdelnsd $*" >>$FAKE/calls
for n in ${1//;/ }; do sed -i "/^$n /d" $FAKE/nsds; done
EOF
chmod +x $fake/*

run() {
    (
        cd $S
        source <(sed -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' gpfs_fs.sh)
        gpfs_bin=$fake
        stc_progress() { echo "PROGRESS $*"; }
        stc_result() { echo "RESULT $*"; }
        stc_fail() { echo "FAILED $*"; exit 1; }
        stc_lock() { :; }
        sleep() { :; }
        stc_main <<<"$1"
    )
}
in='{"action":"delete","cluster_uuid":"'$uuid'","fs_name":"fs2","mount_point":"/gpfs/fs2","nsds":["cl1h1d2","cl1h2d2"]}'

# A file system mounted on a node, with its NSDs
touch $fake/fs-fs2 $fake/mounted-fs2
printf 'cl1h1d1 x\ncl1h1d2 x\ncl1h2d2 x\n' >$fake/nsds
: >$fake/calls
out=$(run "$in")
grep -q '^RESULT {"deleted": true}' <<<"$out" && ok "deleted" || ko "delete: $out"
grep -qx 'mmumount fs2 -a -f' $fake/calls && grep -qx 'mmdelfs fs2' $fake/calls && grep -qx 'mmdelnsd cl1h1d2;cl1h2d2' $fake/calls &&
    ok "unmounted, deleted, its NSDs deleted" || ko "calls: $(cat $fake/calls)"
[ "$(grep -n mmumount $fake/calls | cut -d: -f1)" -lt "$(grep -n mmdelfs $fake/calls | cut -d: -f1)" ] && ok "unmounted before it is deleted" || ko "order"
grep -qx 'cl1h1d1 x' $fake/nsds && ! grep -q 'd2 ' $fake/nsds && ok "the NSDs of the other file system stay" || ko "nsds: $(cat $fake/nsds)"
# Again: gone already, nothing to do
: >$fake/calls
out=$(run "$in")
grep -q '^RESULT' <<<"$out" && grep -q 'gone already' <<<"$out" && [ ! -s $fake/calls ] && ok "a file system gone already is fine" || ko "again: $out / $(cat $fake/calls)"
# An aborted making: NSDs made, no file system
printf 'cl1h1d2 x\n' >$fake/nsds
: >$fake/calls
out=$(run "$in")
grep -q '^RESULT' <<<"$out" && grep -qx 'mmdelnsd cl1h1d2' $fake/calls && ok "the NSDs of an aborted making go" || ko "aborted: $out / $(cat $fake/calls)"
# A node that keeps it mounted
touch $fake/fs-fs2 $fake/mounted-fs2 $fake/stuck
: >$fake/calls
out=$(run "$in")
grep -q '^FAILED fs2 is still mounted' <<<"$out" && ! grep -q mmdelfs $fake/calls && ok "a node keeping it mounted stops the deletion" || ko "stuck: $out"
rm -f $fake/stuck
# A bad mount point
out=$(run "${in/\/gpfs\/fs2/\/etc}")
grep -q '^FAILED invalid mount point' <<<"$out" && ok "a mount point outside /gpfs is refused" || ko "mount point: $out"

# ---- remove: the disks of a host leaving are in two file systems, each group leaves its own (review 2026-10-07) ----
cat >$fake/mmlsdisk <<'EOF'
#!/bin/bash
echo "mmlsdisk::HEADER:version:reserved:reserved:nsdName:status:availability:"
for n in $(cat $FAKE/disks-$1 2>/dev/null); do echo "mmlsdisk::0:1:::$n:ready:up:"; done
EOF
cat >$fake/mmdeldisk <<'EOF'
#!/bin/bash
echo "mmdeldisk $*" >>$FAKE/calls
for n in ${2//;/ }; do sed -i "/^$n$/d" $FAKE/disks-$1; done
EOF
cat >$fake/mmlsnsd <<'EOF'
#!/bin/bash
cat $FAKE/nsds 2>/dev/null
EOF
chmod +x $fake/*
touch $fake/fs-fs1 $fake/fs-fs2
printf 'cl1h1d1\ncl1h2d1\ncl1h3d1\n' >$fake/disks-fs1
printf 'cl1h1d2\ncl1h2d2\ncl1h3d2\n' >$fake/disks-fs2
printf 'cl1h1d1 x\ncl1h2d1 x\ncl1h3d1 x\ncl1h1d2 x\ncl1h2d2 x\ncl1h3d2 x\n' >$fake/nsds
: >$fake/calls
rin='{"action":"remove","cluster_uuid":"'$uuid'","fs_name":"fs1","groups":[{"fs_name":"fs1","nsds":["cl1h3d1"]},{"fs_name":"fs2","nsds":["cl1h3d2"]}],"damaged":false}'
out=$(run "$rin")
[ "$(grep -c '^RESULT' <<<"$out")" = 1 ] && grep -q '^RESULT {"removed": true}' <<<"$out" && ok "one result for both groups" || ko "remove groups: $out"
grep -qx 'mmdeldisk fs1 cl1h3d1' $fake/calls && grep -qx 'mmdeldisk fs2 cl1h3d2' $fake/calls && ok "each NSD leaves its own file system" || ko "deldisk: $(cat $fake/calls)"
grep -qx 'mmdelnsd cl1h3d1' $fake/calls && grep -qx 'mmdelnsd cl1h3d2' $fake/calls && ! grep -q 'h[12]d' $fake/calls && ok "only the NSDs of the host go" || ko "delnsd: $(cat $fake/calls)"
# A group of a file system that is not there stops it
out=$(run "${rin/\"fs_name\":\"fs2\"/\"fs_name\":\"fs9\"}")
grep -q '^FAILED file system fs9 not found' <<<"$out" && ok "a group of a missing file system fails" || ko "fs9: $out"

rm -rf $fake
echo "== $pass passed, $fail failed"
