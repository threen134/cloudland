#!/bin/bash
# sandbox tests: precheck, ssh trust, memory reservation, disk identity and classification
S=/opt/cloudland/scripts/backend/storage
A=/opt/cloudland/run/async_job
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
heartbeat() { for f in $(ls $A/*.done 2>/dev/null); do cat $f; rm -f $f; done; }
decode() { awk -F"'" '{print $8}' | base64 -d 2>/dev/null; }
rm -f $A/*
base=$((RANDOM * 10 + 7))

# loop disks: one blank, one with a file system, one Ceph-like LVM
mkdir -p /root/stc-disks; cd /root/stc-disks
for n in blank fs ceph; do [ -f $n.img ] || truncate -s 2G $n.img; done
for n in blank fs ceph; do losetup -j $PWD/$n.img | grep -q . || losetup -f $PWD/$n.img; done
dev() { losetup -j $PWD/$1.img -nO NAME | head -1; }
blank=$(dev blank); fsd=$(dev fs); cephd=$(dev ceph)
partx -d $blank 2>/dev/null; wipefs -a -q $blank; dd if=/dev/zero of=$blank bs=1M count=4 conv=notrunc,fsync status=none
blkid $fsd >/dev/null || mkfs.ext4 -q $fsd
if ! pvs $cephd >/dev/null 2>&1; then
    pvcreate -q $cephd && vgcreate -q ceph-1111aaaa-0000-0000-0000-000000000000 $cephd &&
    lvcreate -q -l 100%FREE -n osd-block-x --addtag ceph.osd_id=7 --addtag ceph.cluster_fsid=abcd-ef --addtag ceph.type=block ceph-1111aaaa-0000-0000-0000-000000000000 >/dev/null
fi
# GPFS is installed in this sandbox (the spike): hide it while classifying, show it for the GPFS rule
gpfs_bin=/usr/lpp/mmfs/bin/mmfsd
# Nothing stays behind: ceph-volume of a real cluster in this sandbox lists every LV and dies on one it cannot read
cleanup() {
    [ -e $gpfs_bin ] && chmod 500 $gpfs_bin
    vgremove -qq -f ceph-1111aaaa-0000-0000-0000-000000000000 >/dev/null 2>&1
    pvremove -qq -f $cephd >/dev/null 2>&1
    for n in blank fs ceph; do for d in $(losetup -j /root/stc-disks/$n.img -nO NAME); do losetup -d $d; done; done
}
trap cleanup EXIT
[ -x $gpfs_bin ] && chmod 000 $gpfs_bin
cd /opt/cloudland/scripts/kvm
source ../cloudrc
source ./storage_lib.sh
system_disk_list=$(system_disks); local_pool_list=$(local_pools)
classify_disk $blank; [ "$disk_state" = free ] && ok "classify blank: free" || ko "classify blank: $disk_state $disk_detail"
classify_disk $fsd; [ "$disk_state" = dirty ] && ok "classify file system: dirty" || ko "classify fs: $disk_state $disk_detail"
classify_disk $cephd; [ "$disk_state" = ceph_osd ] && [[ "$disk_detail" == *"OSD 7"* ]] && ok "classify ceph osd: $disk_detail" || ko "classify ceph: $disk_state $disk_detail"
id=$(disk_stable_id $blank); size=$(lsblk -dbno SIZE $blank)
disk_identity "$id" "" "" "$size" && [ "$identity_path" = "$blank" ] && ok "identity by stable id ($id)" || ko "identity: $identity_error"
disk_identity "$id" "" "" "123" && ko "identity size mismatch passed" || ok "identity size mismatch refused: $identity_error"
disk_identity "loop:/nonexistent" "" "" "" && ko "identity of a missing disk passed" || ok "identity missing: $identity_error"
chmod 500 $gpfs_bin
classify_disk $blank; [ "$disk_state" = free ] && ok "zero disk with GPFS installed: free" || ko "gpfs rule, zero disk: $disk_state $disk_detail"
# the descriptor of an older NSD: data in the head, no signature blkid knows
printf 'NSD descriptor for /dev/sdx created by GPFS' | dd of=$blank bs=512 seek=2 conv=notrunc,fsync status=none
classify_disk $blank; [ "$disk_state" = unknown_member ] && ok "data in the head with GPFS installed: unknown_member ($disk_detail)" || ko "gpfs rule, head data: $disk_state"
chmod 000 $gpfs_bin
classify_disk $blank; [ "$disk_state" = free ] && ok "data in the head without GPFS: free" || ko "head data without GPFS: $disk_state"
# a newer NSD: GPT with one partition of the GPFS type, unknown_member with or without GPFS installed
sgdisk -Z $blank >/dev/null 2>&1; sgdisk -n 1:0:0 -t 1:37AFFC90-EF7D-4E96-91C3-2D7AE055B174 $blank >/dev/null && partx -u $blank 2>/dev/null; udevadm settle
classify_disk $blank; [ "$disk_state" = unknown_member ] && [[ "$disk_detail" == *"GPFS type"* ]] && ok "GPT partition of the GPFS type: unknown_member" || ko "gpfs v2 rule: $disk_state $disk_detail"
mib=$(( $(blockdev --getsize64 $blank) / 1048576 ))
head -c 65536 /dev/urandom | dd of=$blank bs=64k seek=$(( (mib - 1) * 16 )) conv=notrunc,fsync status=none
wipe_disk $blank && ok "wipe_disk of the GPFS GPT disk" || ko "wipe_disk failed"
tail_zero=$(dd if=$blank bs=1M skip=$((mib - 10)) count=10 status=none | cmp -s - <(head -c 10485760 /dev/zero) && echo yes)
[ "$tail_zero" = yes ] && ok "wipe_disk zeroes the last 10 MiB" || ko "tail not zeroed"
chmod 500 $gpfs_bin
classify_disk $blank; [ "$disk_state" = free ] && ok "wiped disk with GPFS installed: free" || ko "wiped disk: $disk_state $disk_detail"
chmod 000 $gpfs_bin

# precheck with a blank disk and a dirty one (not to wipe), ceph kind, no peers
r=$((base+1))
in=$(jq -cn --arg id "$id" --arg size "$size" --arg fid "$(disk_stable_id $fsd)" --arg fsize "$(lsblk -dbno SIZE $fsd)" '{kind:"ceph", roles:["mon","osd"],
  disks:[{id:$id, size_bytes:($size|tonumber)}, {id:$fid, size_bytes:($fsize|tonumber)}], peers:["127.0.0.1"], ports:[22,6789],
  support:[{id:"ubuntu",version:"26.04"}], need_container:true, data_dirs:[{path:"/var/lib/ceph", min_free_percent:30}], reserve_mb:5120}')
echo "$in" | $S/stc_precheck.sh $r
sleep 8
hb=$(heartbeat)
line=$(grep "'$r'" <<<"$hb")
grep -q "'$r' 'failed'" <<<"$line" && ok "precheck fails on the dirty disk" || ko "precheck status: $line"
res=$(echo "$line" | decode)
echo "$res" | jq -r '.result.items[] | "  \(.status) \(.name): \(.detail)"'
st() { echo "$res" | jq -r --arg n "$1" '.result.items[] | select(.name == $n) | .status'; }
[ "$(st os)" = ok ] && ok "precheck os ok on ubuntu 26.04 for ceph" || ko "os: $(st os)"
[ "$(st network)" = ok ] && ok "precheck network: refused counts as reachable" || ko "network: $(st network)"
[ "$(st "disk $id")" = ok ] && ok "precheck blank disk ok" || ko "blank: $(st "disk $id")"
echo "$res" | jq -e '.result.facts.hostname and .result.facts.kernel' >/dev/null && ok "precheck facts" || ko "facts"
echo "$res" | jq -r .message | grep -q "disk" && ok "precheck message names the failed items: $(echo "$res" | jq -r .message)" || ko "message"

# gpfs on 26.04 is refused, allowed only with allow_unsupported
r=$((base+2))
in=$(jq -cn '{kind:"gpfs", roles:["quorum"], disks:[], peers:[], ports:[], support:[{id:"ubuntu",version:"24.04",kernel_prefix:"6.8.0-",kernel_suffix:"-generic"}], need_headers:true, data_dirs:[{path:"/var/mmfs", min_free_gib:10}], reserve_mb:2048}')
echo "$in" | $S/stc_precheck.sh $r
r2=$((base+3))
echo "$in" | jq -c '.allow_unsupported = true' | $S/stc_precheck.sh $r2
sleep 8
hb=$(heartbeat)
s1=$(grep "'$r'" <<<"$hb" | decode | jq -r '.result.items[] | select(.name=="os") | .status')
s2=$(grep "'$r2'" <<<"$hb" | decode | jq -r '.result.items[] | select(.name=="os") | .status')
[ "$s1" = fail ] && ok "gpfs on 26.04 refused" || ko "gpfs os: $s1"
[ "$s2" = warn ] && ok "gpfs on 26.04 with allow_unsupported: warn" || ko "gpfs allowed: $s2"
# software already on the host: asked of the node side backend of the kind (backends/<kind>.sh)
e1=$(grep "'$r'" <<<"$hb" | decode | jq -r '.result.items[] | select(.name=="existing") | "\(.status) \(.detail)"')
[[ "$e1" == fail*gpfs.base* || "$e1" == fail*"GPFS cluster"* ]] && ok "gpfs backend finds the GPFS of the sandbox: $e1" || ko "gpfs existing: $e1"
[ "$(st existing)" = ok ] && ok "ceph backend finds no Ceph" || ko "ceph existing: $(st existing)"
r=$((base+20))
echo "$in" | jq -c '.kind = "nfs"' | $S/stc_precheck.sh $r
sleep 8
e3=$(heartbeat | grep "'$r'" | decode | jq -r '.result.items[] | select(.name=="existing") | "\(.status) \(.detail)"')
[[ "$e3" == "fail no node side backend"* ]] && ok "unknown kind refused: $e3" || ko "unknown kind: $e3"

# ssh trust: add, replace, remove, keep other lines
r=$((base+4))
cp /root/.ssh/authorized_keys /root/.ssh/authorized_keys.stc-bak 2>/dev/null || { mkdir -p /root/.ssh; touch /root/.ssh/authorized_keys; cp /root/.ssh/authorized_keys /root/.ssh/authorized_keys.stc-bak; }
echo "ssh-ed25519 AAAAother other@host" >>/root/.ssh/authorized_keys
u=12345678-1234-1234-1234-123456789abc
k="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHhIdGVzdGtleXRlc3RrZXl0ZXN0a2V5dGVzdGtleXQ"
jq -cn --arg u $u --arg k "$k" '{cluster_uuid:$u, public_key:$k, from:["10.0.0.1","10.0.0.2"], private_key:"PRIV", known_hosts:["10.0.0.3 ssh-ed25519 AAAA"]}' | $S/stc_ssh_trust.sh $r
sleep 2; heartbeat >/dev/null
grep -q "^from=\"10.0.0.1,10.0.0.2\",no-agent-forwarding,no-port-forwarding,no-X11-forwarding $k cloudland-storage-$u\$" /root/.ssh/authorized_keys && ok "ssh trust line" || ko "ssh line: $(tail -2 /root/.ssh/authorized_keys)"
[ "$(stat -c %a /opt/cloudland/run/storage/$u/id_ed25519)" = 600 ] && ok "private key 600" || ko "key mode"
jq -cn --arg u $u --arg k "$k" '{cluster_uuid:$u, public_key:$k, from:["10.0.0.9"]}' | $S/stc_ssh_trust.sh $((base+5))
sleep 2; heartbeat >/dev/null
[ $(grep -c "cloudland-storage-$u" /root/.ssh/authorized_keys) -eq 1 ] && grep -q 'from="10.0.0.9"' /root/.ssh/authorized_keys && ok "ssh trust replaced, not duplicated" || ko "replace"
[ ! -f /opt/cloudland/run/storage/$u/id_ed25519 ] && ok "private key gone on a non-admin host" || ko "key kept"
jq -cn --arg u $u '{cluster_uuid:$u, remove:true}' | $S/stc_ssh_trust.sh $((base+6))
sleep 2; heartbeat >/dev/null
! grep -q "cloudland-storage-$u" /root/.ssh/authorized_keys && grep -q "other@host" /root/.ssh/authorized_keys && ok "ssh trust removed, other lines kept" || ko "remove"
cp /root/.ssh/authorized_keys.stc-bak /root/.ssh/authorized_keys

# memory reservation
$S/stc_mem_reserve.sh $u 2048; $S/stc_mem_reserve.sh 22345678-1234-1234-1234-123456789abc 1024
[ "$(cat /opt/cloudland/run/storage_reserved_memory)" = $((3072 * 1024)) ] && ok "memory reservation sums the clusters" || ko "mem: $(cat /opt/cloudland/run/storage_reserved_memory)"
$S/stc_mem_reserve.sh $u 0; $S/stc_mem_reserve.sh 22345678-1234-1234-1234-123456789abc 0
[ "$(cat /opt/cloudland/run/storage_reserved_memory)" = 0 ] && ok "memory reservation removed" || ko "mem clear"
echo "== $pass passed, $fail failed"
