#!/bin/bash
# sandbox tests of the progress and the slots of image imports into shared pools (shared-storage-design.md §9.6): the
# percent read from qemu-img -p and curl's bar, the import slots of a host (the next import waits), the report of the
# heartbeat (a phase at once, a percent at most every 10 seconds, the record of a dead job dropped), and an import job
# end to end on a tmpfs pool: it waits for a slot, fetches through file://, writes, reports synced and leaves no record
S=/opt/cloudland/scripts/backend
A=/opt/cloudland/run/async_job
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
waitjobs() {
    local i
    for i in $(seq 1 120); do
        ls $A/*.done >/dev/null 2>&1 && ! ls $A/*.in_progress >/dev/null 2>&1 && break
        sleep 0.5
    done
    for f in $(ls $A/*.done 2>/dev/null); do cat $f; rm -f $f; done
}
command -v qemu-img >/dev/null || apt-get install -y qemu-utils >/dev/null 2>&1
rm -f $A/*
export NODE_ID=7
cd /opt/cloudland/scripts/kvm
source ../cloudrc
source ./storage_lib.sh
rm -rf $image_import_dir /var/lock/cloudland-image-import-*.lock
tmp=$(mktemp -d)

# ---- the percent ----
printf '    (12.00/100%%)\r    (45.31/100%%)\r' >$tmp/wr
[ "$(image_import_percent $tmp/wr)" = 45 ] && ok "qemu-img -p: 45" || ko "qemu-img percent $(image_import_percent $tmp/wr)"
printf '##                                                           3.2%%\r#####################                                     37.9%%\r' >$tmp/dl
[ "$(image_import_percent $tmp/dl)" = 37 ] && ok "curl bar: 37" || ko "curl percent $(image_import_percent $tmp/dl)"
: >$tmp/empty
[ "$(image_import_percent $tmp/empty)" = 0 ] && [ "$(image_import_percent $tmp/none)" = 0 ] && ok "no percent yet: 0" || ko "empty percent"
printf '    (100.00/100%%)\r' >$tmp/wr2
[ "$(image_import_percent $tmp/wr2)" = 100 ] && ok "qemu-img done: 100" || ko "qemu-img 100: $(image_import_percent $tmp/wr2)"

# ---- the slots ----
hold() {
    ( source ../cloudrc; source ./storage_lib.sh; image_import_concurrency=2 image_import_wait=0.2 image_import_slot
      echo "$(date +%s.%N)" >$tmp/got-$1; sleep $2 ) &
}
hold 1 3; hold 2 3
sleep 0.5
[ -f $tmp/got-1 ] && [ -f $tmp/got-2 ] && ok "two imports hold the two slots" || ko "first two slots"
start=$(date +%s.%N)
hold 3 0
sleep 1
[ ! -f $tmp/got-3 ] && ok "a third import waits" || ko "third did not wait"
wait
waited=$(echo "$(cat $tmp/got-3) - $start" | bc)
[ "$(echo "$waited > 2" | bc)" = 1 ] && ok "the third got a slot once one was free (${waited%.*}s)" || ko "third waited $waited"
( source ../cloudrc; source ./storage_lib.sh; image_import_concurrency=x image_import_wait=0.2 image_import_slot; echo ok >$tmp/bad ) &
sleep 1
[ -f $tmp/bad ] && ok "a bad concurrency falls back to 2" || ko "bad concurrency"
wait

# ---- the report of the heartbeat ----
mkdir -p $tmp/job/async_job
printf '#!/bin/bash\nsleep 60\n' >$tmp/job/async_job/import_image_shared.sh
chmod +x $tmp/job/async_job/import_image_shared.sh
$tmp/job/async_job/import_image_shared.sh 77 abc &
jobpid=$!
mkdir -p $image_import_dir
printf '#####  20.0%%\r' >$image_import_dir/77.dl
echo "download $image_import_dir/77.dl $jobpid" >$image_import_dir/77
out=$(image_import_report)
[ "$out" = "|:-COMMAND-:| image_storage_progress '77' 'download' '20'" ] && ok "the progress is reported: $out" || ko "report: $out"
[ -z "$(image_import_report)" ] && ok "nothing changed: no report" || ko "repeated report"
printf '#####  25.0%%\r' >>$image_import_dir/77.dl
[ -z "$(image_import_report)" ] && ok "a new percent within 10 seconds waits" || ko "percent reported too soon"
sed -i 's/ [0-9]*$/ 1/' $image_import_dir/.77.reported
out=$(image_import_report)
[ "$out" = "|:-COMMAND-:| image_storage_progress '77' 'download' '25'" ] && ok "the new percent after 10 seconds" || ko "late percent: $out"
printf '    (3.00/100%%)\r' >$image_import_dir/77.wr
echo "write $image_import_dir/77.wr $jobpid" >$image_import_dir/77
out=$(image_import_report)
[ "$out" = "|:-COMMAND-:| image_storage_progress '77' 'write' '3'" ] && ok "a new phase at once" || ko "phase: $out"
# review 2026-10-07: an unchanged phase is reported again every 10 minutes (clapi counts its timeout from the last
# report), and an import with no report yet does not take the values of the one before it
sed -i 's/ [0-9]*$/ '"$(( $(date +%s) - 700 ))"'/' $image_import_dir/.77.reported
out=$(image_import_report)
[ "$out" = "|:-COMMAND-:| image_storage_progress '77' 'write' '3'" ] && ok "an unchanged phase again after 10 minutes" || ko "keepalive: $out"
$tmp/job/async_job/import_image_shared.sh 79 abc &
job79=$!
printf '    (3.00/100%%)\r' >$image_import_dir/79.wr
echo "write $image_import_dir/79.wr $job79" >$image_import_dir/79
out=$(image_import_report)
[ "$out" = "|:-COMMAND-:| image_storage_progress '79' 'write' '3'" ] && ok "a new import is reported although the one before has the same phase and percent" || ko "stale locals: $out"
kill $job79; wait $job79 2>/dev/null
# A pid that is not the import (reused after a reboot)
echo "wait - $$" >$image_import_dir/78
kill $jobpid; wait $jobpid 2>/dev/null
out=$(image_import_report)
[ -z "$out" ] && [ ! -e $image_import_dir/77 ] && [ ! -e $image_import_dir/77.wr ] && [ ! -e $image_import_dir/78 ] && \
    ok "the records of jobs that are gone are dropped" || ko "dead job: $out / $(ls -a $image_import_dir)"

# ---- an import job end to end ----
pool=bbbbbbbb-1111-2222-3333-555555555555
mnt=/gpfs-sb11/fs1
root=$mnt/cl_bbbbbbbb
umount $mnt 2>/dev/null
rm -rf /gpfs-sb11
mkdir -p $mnt
mount -t tmpfs -o size=512m tmpfs $mnt
mkdir -p $root/volumes $root/images $root/nvram $root/tmp
printf 'driver=gpfs\npool_uuid=%s\n' $pool >$root/.cloudland-pool
mkdir -p /tmp/fakegpfs
printf '#!/bin/bash\ncase $1 in snap) chmod 444 "$2" ;; *) exit 2 ;; esac\n' >/tmp/fakegpfs/mmclone
chmod +x /tmp/fakegpfs/mmclone
export gpfs_bin=/tmp/fakegpfs
img=image-11-ab12cd34
src=$tmp/served/$img.qcow2
mkdir -p $tmp/served
qemu-img create -q -f qcow2 $src 64M
qemu-io -f qcow2 -c "write -P 0xcd 0 8M" $src >/dev/null
rm -f /opt/cloudland/cache/image/$img.qcow2
url=$(echo -n "file://$src" | base64 -w0)
in=$(jq -cn --arg p $pool --arg r $root --arg u "$url" --arg b "$root/images/$img.qcow2" \
    '{driver:"gpfs", pool:$p, root:$r, fs_type:"tmpfs", image_storage_id:11, base:$b, image_name:"'$img'.qcow2", image_url_b64:$u, clone_mode:"clone", image_size:67108864}')
# One slot, held by another import: this one waits and says so
echo "image_import_concurrency=1" >>/opt/cloudland/scripts/cloudrc.local
( source ../cloudrc; source ./storage_lib.sh; image_import_slot; sleep 4 ) &
holder=$!
sleep 0.5
$S/import_image_shared.sh 11 <<<"$in"
sleep 2
read -r ph _ _ <$image_import_dir/11 2>/dev/null
[ "$ph" = wait ] && ok "the import waits for the slot" || ko "phase while waiting: $ph"
out=$(image_import_report)
[ "$out" = "|:-COMMAND-:| image_storage_progress '11' 'wait' '0'" ] && ok "the wait is reported" || ko "wait report: $out"
wait $holder
out=$(waitjobs)
[ "$out" = "|:-COMMAND-:| image_storage_status '11' 'synced' '-'" ] && ok "imported through file:// once the slot was free" || ko "import: $out"
[ -f $root/images/$img.qcow2 ] && [ -f /opt/cloudland/cache/image/$img.qcow2 ] && ok "the copy is in the pool, the image in the cache" || ko "files"
[ -z "$(ls -A $image_import_dir 2>/dev/null)" ] && ok "no record left" || ko "left: $(ls -A $image_import_dir)"
sed -i '/^image_import_concurrency=/d' /opt/cloudland/scripts/cloudrc.local

umount $mnt; rm -rf /gpfs-sb11 $tmp /opt/cloudland/cache/image/$img.qcow2
echo "== $pass passed, $fail failed"
