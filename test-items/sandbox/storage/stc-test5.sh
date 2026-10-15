#!/bin/bash
# sandbox tests of the boot disks in shared pools (shared-storage-design.md §9.6-§9.8, stage S4) on a file pool: a
# tmpfs stands in for GPFS and a fake mmclone for the clone parents (snap makes the file read-only, copy copies it).
# Covers the import of the copy of an image (import_image_shared.sh and its background job), the boot disk made from
# it (shared_boot_load / shared_boot_make), the disk element put into a domain definition, the swap of a reinstall,
# the removal of the copy and of a boot disk with its UEFI variables
S=/opt/cloudland/scripts/backend
A=/opt/cloudland/run/async_job
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
# the output of the one background job started last, once it finished
waitjobs() {
    local i
    for i in $(seq 1 120); do
        ls $A/*.done >/dev/null 2>&1 && ! ls $A/*.in_progress >/dev/null 2>&1 && break
        sleep 0.5
    done
    for f in $(ls $A/*.done 2>/dev/null); do cat $f; rm -f $f; done
}
command -v qemu-img >/dev/null || apt-get install -y qemu-utils >/dev/null 2>&1
command -v xmllint >/dev/null || apt-get install -y libxml2-utils >/dev/null 2>&1
rm -f $A/*
export NODE_ID=7

# fake mmclone: snap makes a clone parent (read-only), copy makes a writable clone, refusing an existing target
mkdir -p /tmp/fakegpfs
cat >/tmp/fakegpfs/mmclone <<'EOF'
#!/bin/bash
case $1 in
    snap) chmod 444 "$2" ;;
    copy) [ -e "$3" ] && { echo "$3 exists" >&2; exit 1; }; cp --sparse=always "$2" "$3" && chmod 644 "$3" ;;
    *) exit 2 ;;
esac
EOF
chmod +x /tmp/fakegpfs/mmclone
export gpfs_bin=/tmp/fakegpfs

pool=bbbbbbbb-1111-2222-3333-444444444444
mnt=/gpfs-sb4/fs1
root=$mnt/cl_bbbbbbbb
umount $mnt 2>/dev/null
rm -rf /gpfs-sb4
mkdir -p $mnt
mount -t tmpfs -o size=512m tmpfs $mnt
mkdir -p $root/volumes $root/images $root/nvram $root/tmp
printf 'driver=gpfs\npool_uuid=%s\n' $pool >$root/.cloudland-pool
pj() { jq -cn --arg p $pool --arg r $root "{driver:\"gpfs\", pool:\$p, root:\$r, fs_type:\"tmpfs\"} + $1"; }

# an image in the local cache: 64 MiB virtual, its first 4 MiB written with 0xab
mkdir -p /opt/cloudland/cache/image
img=image-3-ab12cd34
rm -f /opt/cloudland/cache/image/$img.qcow2
qemu-img create -q -f qcow2 /opt/cloudland/cache/image/$img.qcow2 64M
qemu-io -f qcow2 -c "write -P 0xab 0 4M" /opt/cloudland/cache/image/$img.qcow2 >/dev/null
base=$root/images/$img.qcow2
in=$(pj "{image_storage_id:5, base:\"$base\", image_name:\"$img.qcow2\", image_url_b64:\"\", clone_mode:\"clone\"}")

# import: refused at once with a bad copy name, otherwise in the background
out=$(jq -c ".base = \"$root/volumes/x.qcow2\"" <<<"$in" | $S/import_image_shared.sh 5)
[[ "$out" == "|:-COMMAND-:| image_storage_status '5' 'error' "* ]] && ok "import refuses a copy outside images/: $out" || ko "bad base: $out"
out=$(jq -c '.image_name = "x;rm"' <<<"$in" | $S/import_image_shared.sh 5)
[[ "$out" == *"'error' 'invalid image name'"* ]] && ok "import refuses a bad image name" || ko "bad name: $out"
touch -d '-2 hours' $root/tmp/$img.9-999.import
touch $root/tmp/$img.9-998.import
out=$($S/import_image_shared.sh 5 <<<"$in")
[ -z "$out" ] && ok "import goes to the background, nothing on stdout" || ko "import stdout: $out"
out=$(waitjobs)
[ "$out" = "|:-COMMAND-:| image_storage_status '5' 'synced' '-'" ] && ok "import reports synced" || ko "import report: $out"
[ -f $base ] && [ ! -w $base -o "$(stat -c %a $base)" = 444 ] && ok "the copy is a sealed clone parent ($(stat -c %a $base))" || ko "copy: $(ls -l $base)"
[ "$(qemu-img info --output=json $base | jq -r .format)" = qcow2 ] && ok "the copy is qcow2" || ko "copy format"
[ ! -e $root/tmp/$img.9-999.import ] && [ -e $root/tmp/$img.9-998.import ] && ok "a stale import file went, a recent one stayed" || ko "cleanup: $(ls $root/tmp)"
rm -f $root/tmp/$img.9-998.import
[ -z "$(ls $root/tmp)" ] && ok "no temporary file left" || ko "tmp: $(ls $root/tmp)"
# again: the copy is there (a lost report), synced at once
$S/import_image_shared.sh 5 <<<"$in"
out=$(waitjobs)
[ "$out" = "|:-COMMAND-:| image_storage_status '5' 'synced' '-'" ] && ok "a second import finds the copy" || ko "second import: $out"
# an image neither cached nor downloadable
in2=$(pj "{image_storage_id:6, base:\"$root/images/image-4-cd34ab12.qcow2\", image_name:\"image-4-cd34ab12.qcow2\", image_url_b64:\"\", clone_mode:\"clone\"}")
$S/import_image_shared.sh 6 <<<"$in2"
out=$(waitjobs)
[[ "$out" == "|:-COMMAND-:| image_storage_status '6' 'error' 'image image-4-cd34ab12.qcow2 not available'" ]] && ok "a missing image fails the import" || ko "missing image: $out"
# the pool not mounted: the marker check fails
mv $root/.cloudland-pool $root/.cloudland-pool.x
$S/import_image_shared.sh 6 <<<"$in2"
out=$(waitjobs)
[[ "$out" == *"'error' 'pool $pool: the marker"* ]] && ok "an unchecked pool fails the import" || ko "marker: $out"
mv $root/.cloudland-pool.x $root/.cloudland-pool

# the boot disk
cd /opt/cloudland/scripts/kvm
source ../cloudrc
source ./storage_lib.sh
bd=$(pj "{volume_id:12, path:\"$root/volumes/volume-12.disk\", size_gb:1, image_base:\"$base\", image_storage_id:5, clone_mode:\"clone\", nvram:\"$root/nvram/inst-9_VARS.fd\"}")
shared_boot_load "$bd" 12 9 && ok "boot disk loaded: $drv_vol from $sb_base ($sb_mode)" || ko "load: $guard_error"
! shared_boot_load "$bd" 13 9 && [[ "$guard_error" == *"not the file of volume 13"* ]] && ok "the disk must be the one of its volume" || ko "volume check"
! shared_boot_load "$(jq -c '.nvram = "/etc/passwd"' <<<"$bd")" 12 9 && [[ "$guard_error" == *"UEFI variables"* ]] && ok "UEFI variables only in nvram/ of the pool: $guard_error" || ko "nvram check"
! shared_boot_load "$(jq -c '.image_base = "/etc/shadow"' <<<"$bd")" 12 9 && [[ "$guard_error" == *"not an image copy"* ]] && ok "the copy must be in images/" || ko "base check"
! shared_boot_load "$(jq -c '.image_storage_id = 0' <<<"$bd")" 12 9 && [[ "$guard_error" == *"image copy id"* ]] && ok "the copy needs its id" || ko "copy id"
shared_boot_load "$bd" 12 9
shared_boot_make "$drv_vol" 1 && [ "$drv_cloned" = 1 ] && ok "boot disk cloned" || ko "make: $guard_error"
[ "$(qemu-img info --output=json $drv_vol | jq -r '."virtual-size"')" = $((1024 * 1024 * 1024)) ] && ok "grown to the flavor" || ko "size $(qemu-img info $drv_vol)"
qemu-io -f qcow2 -c "read -P 0xab 0 4M" $drv_vol | grep -q "read 4194304" && ok "the disk holds the image" || ko "content"
! shared_boot_make "$drv_vol" 1 && [[ "$guard_error" == *"already exists"* ]] && ok "never over an existing disk" || ko "existing: $guard_error"
# a flavor smaller than the image: the disk made is removed again
qemu-img create -q -f qcow2 $root/tmp/big.qcow2 2G && chmod 444 $root/tmp/big.qcow2 && mv $root/tmp/big.qcow2 $root/images/image-8-00000000.qcow2
shared_boot_load "$(jq -c ".path = \"$root/volumes/volume-14.disk\" | .volume_id = 14 | .image_base = \"$root/images/image-8-00000000.qcow2\"" <<<"$bd")" 14 9
! shared_boot_make "$drv_vol" 1 && [[ "$guard_error" == "flavor is smaller than image size" ]] && [ ! -e $root/volumes/volume-14.disk ] && ok "a flavor too small: refused and removed" || ko "small flavor: $guard_error $(ls $root/volumes)"
# copy mode: a full copy, no clone
shared_boot_load "$(jq -c ".path = \"$root/volumes/volume-15.disk\" | .volume_id = 15 | .clone_mode = \"copy\"" <<<"$bd")" 15 9
shared_boot_make "$drv_vol" 1 && [ "$drv_cloned" = 0 ] && qemu-io -f qcow2 -c "read -P 0xab 0 4M" $drv_vol | grep -q "read 4194304" && ok "copy mode: a full copy" || ko "copy: $guard_error"
# no copy in the pool
shared_boot_load "$(jq -c ".path = \"$root/volumes/volume-16.disk\" | .volume_id = 16 | .image_base = \"$root/images/image-9-00000000.qcow2\"" <<<"$bd")" 16 9
! shared_boot_make "$drv_vol" 1 && [[ "$guard_error" == *"is not in pool"* ]] && ok "no copy: $guard_error" || ko "no copy"
[ "$sb_missing" = 1 ] && ok "no copy: marked missing" || ko "no copy: sb_missing=$sb_missing"
# the failure of a launch / reinstall for a missing copy reports nocopy with the copy, clapi fails the copy (review 11);
# the disk_fail of the shared boot disk (launch_vm.sh has another one for an evacuation)
for s in launch_vm reinstall_vm; do
    fn=$(awk '/function disk_fail\(\)/ {on=1; b=""} on {b = b $0 "\n"} on && /^    }$/ {on=0; if (b ~ /shared_boot_report/) {printf "%s", b; exit}}' $S/$s.sh)
    out=$(vol_ID=16; new_img=""; eval "$fn"; disk_fail "the copy is not in pool" 2>/dev/null)
    [ "$out" = "|:-COMMAND-:| create_boot_shared '16' 'nocopy' '5' 'the copy is not in pool'" ] && ok "$s reports nocopy" || ko "$s nocopy: $out"
done
# a pool that does not answer is no missing copy: a plain error, the copy stays
(
    function timeout() { return 124; }
    ! shared_boot_make "$drv_vol" 1 && [ "$sb_missing" = 0 ] && ok "a pool not answering: not marked missing" || ko "timeout: sb_missing=$sb_missing"
    fn=$(awk '/function disk_fail\(\)/ {on=1; b=""} on {b = b $0 "\n"} on && /^    }$/ {on=0; if (b ~ /shared_boot_report/) {printf "%s", b; exit}}' $S/launch_vm.sh)
    out=$(vol_ID=16; eval "$fn"; disk_fail "x" 2>/dev/null)
    [ "$out" = "|:-COMMAND-:| create_boot_shared '16' 'error' '0' 'x'" ] && ok "a pool not answering reports a plain error" || ko "timeout report: $out"
) | sed 's/^/  /' | tee /tmp/s4-sub.out
pass=$((pass + $(grep -c '^  PASS' /tmp/s4-sub.out))); fail=$((fail + $(grep -c '^  FAIL' /tmp/s4-sub.out)))
# a later success clears the mark
sb_missing=1
shared_boot_load "$(jq -c ".path = \"$root/volumes/volume-17.disk\" | .volume_id = 17" <<<"$bd")" 17 9
shared_boot_make "$drv_vol" 1 && [ "$sb_missing" = 0 ] && ok "a disk made clears the mark" || ko "mark after success: $sb_missing"
drv_drop "$drv_vol"
# the report of a disk made, in one place for launch and reinstall (review 2, 13): the copy only for a clone
out=$(drv_cloned=1; sb_copy_id=5; shared_boot_report 16 attached)
out2=$(drv_cloned=0; sb_copy_id=5; shared_boot_report 16 attached)
out3=$(sb_missing=0; shared_boot_report 16 error "it's broken")
[ "$out" = "|:-COMMAND-:| create_boot_shared '16' 'attached' '5' '-'" ] && [ "$out2" = "|:-COMMAND-:| create_boot_shared '16' 'attached' '0' '-'" ] &&
    [ "$out3" = "|:-COMMAND-:| create_boot_shared '16' 'error' '0' 'its broken'" ] && ok "shared_boot_report: clone, full copy, error" || ko "report: $out / $out2 / $out3"
grep -q "create_boot_shared" $S/launch_vm.sh $S/reinstall_vm.sh && ko "launch_vm.sh / reinstall_vm.sh still write the report themselves" || ok "launch and reinstall report through shared_boot_report only"

# the disk element of the driver in place of the file disk of the template, keeping its PCI address
x=/tmp/s4-dom.xml
sed -e "s#VM_IMG#$root/volumes/volume-12.disk#g" /opt/cloudland/scripts/xml/template_with_qa.xml >$x
before_rest=$(grep -v -e "volume-12" $x | grep -c .)
shared_boot_load "$bd" 12 9
xml_replace_disk $x vda "$(drv_disk_xml "$drv_vol" vda)" && ok "disk replaced" || ko "replace failed"
xmllint --noout $x 2>/dev/null && ok "the definition is well formed" || ko "malformed: $(xmllint --noout $x 2>&1 | head -3)"
[ "$(xmllint --xpath "string(//devices/disk[target/@dev='vda']/driver/@cache)" $x)" = none ] &&
    [ "$(xmllint --xpath "string(//devices/disk[target/@dev='vda']/driver/@error_policy)" $x)" = stop ] &&
    [ "$(xmllint --xpath "string(//devices/disk[target/@dev='vda']/address/@slot)" $x)" = 0x03 ] &&
    [ "$(xmllint --xpath "string(//devices/disk[target/@dev='vda']/source/@file)" $x)" = $root/volumes/volume-12.disk ] &&
    ok "vda: cache none, error policy of gpfs, slot kept" || ko "vda: $(xmllint --xpath "//devices/disk[target/@dev='vda']" $x)"
[ "$(xmllint --xpath 'count(//devices/disk)' $x)" = 2 ] && grep -q "cloudland:vm xmlns:cloudland" $x && ok "the rest is unchanged (cdrom, metadata namespace)" || ko "rest: $(xmllint --xpath 'count(//devices/disk)' $x)"
xml_replace_disk $x vdz "<disk/>" && ko "replaced a device that is not there" || ok "no such device: refused"
# the device of a volume in a domain, by the source element its driver writes (review 2, 11)
(
    function virsh() { cat /tmp/s4-dom.xml; }
    [ "$(drv_dev_of inst-9 $root/volumes/volume-12.disk)" = vda ] && [ -z "$(drv_dev_of inst-9 $root/volumes/volume-99.disk)" ] &&
        ok "drv_dev_of finds the file disk, not another volume" || ko "drv_dev_of file: '$(drv_dev_of inst-9 $root/volumes/volume-12.disk)'"
) | sed 's/^/  /' | tee /tmp/s4-sub.out
pass=$((pass + $(grep -c '^  PASS' /tmp/s4-sub.out))); fail=$((fail + $(grep -c '^  FAIL' /tmp/s4-sub.out)))
rm -f $x

# reinstall: the new disk under its own name, then in place of the old one
tmp_img=$(drv_temp_of "$drv_vol")
[ "$tmp_img" = $root/tmp/volume-12.reinstall.disk ] && ok "reinstall disk: $tmp_img" || ko "temp name $tmp_img"
qemu-io -f qcow2 -c "write -P 0xcd 0 1M" $drv_vol >/dev/null
shared_boot_make "$tmp_img" 1 && drv_drop "$drv_vol" && drv_rename "$tmp_img" "$drv_vol" &&
    qemu-io -f qcow2 -c "read -P 0xab 0 4M" $drv_vol | grep -q "read 4194304" && [ ! -e $tmp_img ] && ok "reinstalled: the new disk is in place" || ko "swap: $guard_error"
! drv_drop $base && [[ "$guard_error" == *"is not a volume file"* ]] && [ -f $base ] && ok "drv_drop never removes a copy" || ko "drop guard"
! drv_drop /etc/hostname && [ -f /etc/hostname ] && ok "drv_drop never removes something else" || ko "drop guard 2"

# removal of the copy: refused outside images/, done for the copy
out=$(jq -c ".base = \"$root/volumes/volume-12.disk\"" <<<"$in" | $S/delete_image_shared.sh 5)
[[ "$out" == *"'delete_failed' "*"not an image copy"* ]] && [ -f $root/volumes/volume-12.disk ] && ok "removal refuses a volume: $out" || ko "remove guard: $out"
out=$($S/delete_image_shared.sh 5 <<<"$in")
[ "$out" = "|:-COMMAND-:| image_storage_status '5' 'deleted' '-'" ] && [ ! -e $base ] && ok "copy removed" || ko "remove: $out"
out=$($S/delete_image_shared.sh 5 <<<"$in")
[ "$out" = "|:-COMMAND-:| image_storage_status '5' 'deleted' '-'" ] && ok "a copy already gone is fine" || ko "remove again: $out"
# a pool that does not answer keeps the copy and its record (review 3)
cp $root/volumes/volume-15.disk $root/images/image-21-0000bbbb.qcow2 2>/dev/null || qemu-img create -q -f qcow2 $root/images/image-21-0000bbbb.qcow2 1M
(
    function timeout() { return 124; }
    ! drv_base_delete $root/images/image-21-0000bbbb.qcow2 && [[ "$guard_error" == *"did not answer"* ]] && ok "removal while the pool does not answer: $guard_error" || ko "removal timeout: $guard_error"
) | sed 's/^/  /' | tee /tmp/s4-sub.out
pass=$((pass + $(grep -c '^  PASS' /tmp/s4-sub.out))); fail=$((fail + $(grep -c '^  FAIL' /tmp/s4-sub.out)))
[ -f $root/images/image-21-0000bbbb.qcow2 ] && drv_base_delete $root/images/image-21-0000bbbb.qcow2 && [ ! -e $root/images/image-21-0000bbbb.qcow2 ] && ok "removed once the pool answers" || ko "removal after timeout: $guard_error"

# the copy put in place atomically (review 12): a copy already there is kept as it is, eight imports at once leave one
c=$root/images/image-20-0000aaaa.qcow2
qemu-img create -q -f qcow2 $c 1M && chmod 444 $c
ino=$(stat -c %i $c)
drv_import_image /opt/cloudland/cache/image/$img.qcow2 qcow2 $c && [ "$(stat -c %i $c)" = $ino ] && [ -z "$(ls $root/tmp)" ] &&
    ok "an import finding the copy in place keeps it" || ko "import over a copy: $guard_error $(stat -c %i $c) $(ls $root/tmp)"
rm -f $c
for i in 1 2 3 4 5 6 7 8; do
    ( NODE_ID=$i; drv_import_image /opt/cloudland/cache/image/$img.qcow2 qcow2 $c; echo $? >/tmp/s4-imp.$i ) &
done
wait
[ "$(cat /tmp/s4-imp.* | sort -u | xargs)" = 0 ] && [ "$(ls $root/images | grep -c '^image-20-')" = 1 ] && [ -z "$(ls $root/tmp)" ] &&
    qemu-io -f qcow2 -c "read -P 0xab 0 4M" $c | grep -q "read 4194304" && ok "eight imports at once: all fine, one copy, nothing left in tmp" ||
    ko "concurrent imports: $(cat /tmp/s4-imp.* | xargs) $(ls $root/images) tmp: $(ls $root/tmp)"
rm -f /tmp/s4-imp.* $c
# the lock of the copy: one left by a job that died (older than 10 minutes) is cleared, one held by another import is
# waited for (GPFS refuses a hard link to a clone parent, so the lock is what keeps two imports apart)
mkdir $root/tmp/image-20-0000aaaa.place && touch -d '-20 minutes' $root/tmp/image-20-0000aaaa.place
drv_import_image /opt/cloudland/cache/image/$img.qcow2 qcow2 $c && [ -f $c ] && [ -z "$(ls $root/tmp)" ] && ok "a stale lock is cleared" || ko "stale lock: $guard_error $(ls $root/tmp)"
rm -f $c
mkdir $root/tmp/image-20-0000aaaa.place
( sleep 2; rmdir $root/tmp/image-20-0000aaaa.place ) &
t0=$(date +%s)
drv_import_image /opt/cloudland/cache/image/$img.qcow2 qcow2 $c && [ -f $c ] && [ $(($(date +%s) - t0)) -ge 2 ] && [ -z "$(ls $root/tmp)" ] &&
    ok "a lock held by another import is waited for ($(($(date +%s) - t0)) s)" || ko "held lock: $guard_error $(ls $root/tmp)"
wait
rm -f $c
# a lock another import keeps holding past the wait fails this import, which leaves that lock alone (review 2, 4)
mkdir $root/tmp/image-20-0000aaaa.place
(
    drv_lock_tries=4
    ! drv_import_image /opt/cloudland/cache/image/$img.qcow2 qcow2 $c && [[ "$guard_error" == *"another import holds"* ]] &&
        [ -d $root/tmp/image-20-0000aaaa.place ] && [ ! -e $c ] && [ -z "$(ls $root/tmp | grep -v '\.place$')" ] &&
        ok "a lock held past the wait: this import fails, the lock of the other stays" || ko "lock held past the wait: $guard_error $(ls $root/tmp)"
) | sed 's/^/  /' | tee /tmp/s4-sub.out
pass=$((pass + $(grep -c '^  PASS' /tmp/s4-sub.out))); fail=$((fail + $(grep -c '^  FAIL' /tmp/s4-sub.out)))
rmdir $root/tmp/image-20-0000aaaa.place
rm -f $c

# RBD: only a missing image is a missing / removed copy, a cluster that does not answer is not (review 3, 11)
(
    source ./storage/drivers/ceph_rbd.sh
    drv_ceph_pool=cl_test5
    rbd_case() { eval "function drv_rbd() { $1; }"; }
    rbd_case 'echo "rbd: error opening image image-3-ab12cd34: (2) No such file or directory" >&2; return 2'
    drv_base_missing image-3-ab12cd34 && ok "rbd: no image is a missing copy" || ko "rbd: enoent not missing"
    drv_base_delete image-3-ab12cd34 && ok "rbd: no image is a removed copy" || ko "rbd: enoent delete: $guard_error"
    rbd_case 'return 124'
    ! drv_base_missing image-3-ab12cd34 && ok "rbd: a timeout is no missing copy" || ko "rbd: timeout missing"
    ! drv_base_delete image-3-ab12cd34 && [[ "$guard_error" == *"snap ls"* ]] && ok "rbd: a timeout keeps the copy ($guard_error)" || ko "rbd: timeout delete: $guard_error"
    rbd_case '[ "$1" = snap ] && echo "[]"; return 0'
    drv_base_missing image-3-ab12cd34 && ok "rbd: an image without its snapshot is no copy" || ko "rbd: nosnap"
    rbd_case 'case "$1 $2" in "snap ls") echo "[{\"name\":\"base\"}]" ;; "children "*) echo "cl_test5/volume-9" ;; esac; return 0'
    ! drv_base_missing image-3-ab12cd34 && ok "rbd: a copy with its snapshot is there" || ko "rbd: base missing"
    ! drv_base_delete image-3-ab12cd34 && [[ "$guard_error" == *"still has clones"* ]] && ok "rbd: a copy with clones stays" || ko "rbd: clones: $guard_error"
    # an RBD boot disk is found by its pool and image, whatever the driver variables are called
    drv_conf=/etc/ceph/x.conf; drv_user=cloudland; drv_secret=11111111-2222-3333-4444-555555555555; drv_cache=writeback; drv_error_policy=report
    printf '<domain><devices>\n%s\n%s\n</devices></domain>\n' "$(drv_disk_xml volume-7 vdb)" "$(drv_disk_xml volume-8 vdc)" >/tmp/s4-rbd-dom.xml
    function virsh() { cat /tmp/s4-rbd-dom.xml; }
    [ "$(drv_dev_of inst-9 volume-8)" = vdc ] && [ "$(drv_dev_of inst-9 volume-7)" = vdb ] && [ -z "$(drv_dev_of inst-9 volume-9)" ] &&
        ok "rbd: drv_dev_of finds each RBD disk" || ko "rbd drv_dev_of: '$(drv_dev_of inst-9 volume-8)'"
    rm -f /tmp/s4-rbd-dom.xml
) | sed 's/^/  /' | tee /tmp/s4-sub.out
pass=$((pass + $(grep -c '^  PASS' /tmp/s4-sub.out))); fail=$((fail + $(grep -c '^  FAIL' /tmp/s4-sub.out)))
rm -f /tmp/s4-sub.out

# a boot disk removed with its UEFI variables; variables named outside nvram/ are left alone
touch $root/nvram/inst-9_VARS.fd /tmp/s4-keep_VARS.fd
out=$($S/delete_volume_shared.sh 12 x <<<"$(jq -c ". + {nvram: \"$root/nvram/inst-9_VARS.fd\"}" <<<"$bd")")
[ "$out" = "|:-COMMAND-:| clear_volume '12' 'deleted' '-'" ] && [ ! -e $root/volumes/volume-12.disk ] && [ ! -e $root/nvram/inst-9_VARS.fd ] && ok "boot disk removed with its UEFI variables" || ko "boot removal: $out"
out=$($S/delete_volume_shared.sh 15 x <<<"$(jq -c ".path = \"$root/volumes/volume-15.disk\" | .volume_id = 15 | . + {nvram: \"/tmp/s4-keep_VARS.fd\"}" <<<"$bd")")
[ -e /tmp/s4-keep_VARS.fd ] && [ ! -e $root/volumes/volume-15.disk ] && ok "variables outside nvram/ are not removed" || ko "nvram guard: $out"
rm -f /tmp/s4-keep_VARS.fd

# UEFI variables kept when a domain goes from a host that does not own them
[ "$(nvram_undefine_flag $image_dir/inst-9_VARS.fd)" = --nvram ] && [ "$(nvram_undefine_flag $root/nvram/inst-9_VARS.fd)" = --keep-nvram ] &&
    [ "$(nvram_undefine_flag /opt/cloudland/pools/x/nvram/inst-9_VARS.fd)" = --nvram ] && [ "$(nvram_undefine_flag '')" = --nvram ] &&
    ok "undefine keeps the variables of a shared pool only" || ko "undefine flag"

# the config drive of the guest carries nothing of the boot disk of a shared pool (review 2, 2)
mkdir -p /tmp/fakeiso
printf '#!/bin/bash\ncp "${@: -1}/openstack/latest/network_data.json" /tmp/s4-netdata.json\n' >/tmp/fakeiso/mkisofs
chmod +x /tmp/fakeiso/mkisofs
rm -f /tmp/s4-netdata.json
md=$(jq -cn --argjson bd "$bd" '{links: [], networks: [], services: [], vlans: [], keys: [], dns: "8.8.8.8", root_passwd: "x", boot_disk: $bd}' | base64 -w0)
PATH=/tmp/fakeiso:$PATH $S/build_meta.sh inst-990 s4-meta <<<"$md" >/dev/null 2>&1
[ -s /tmp/s4-netdata.json ] && ! grep -q boot_disk /tmp/s4-netdata.json && ! grep -q "$root" /tmp/s4-netdata.json &&
    ok "network_data.json of the config drive has no boot_disk" || ko "config drive: $(head -c 300 /tmp/s4-netdata.json 2>/dev/null)"
rm -rf /tmp/fakeiso /tmp/s4-netdata.json /opt/cloudland/cache/meta/inst-990.iso

umount $mnt; rm -rf /gpfs-sb4 /tmp/fakegpfs /opt/cloudland/cache/image/$img.qcow2
echo "== $pass passed, $fail failed"
