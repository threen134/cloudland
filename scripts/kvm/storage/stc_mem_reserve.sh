#!/bin/bash
# Hold back memory for the storage daemons of a cluster on this host (shared-storage-design.md §6.7):
# stc_mem_reserve.sh <cluster uuid> <MiB>. 0 removes the reservation. The heartbeat (report_rc.sh) takes the sum of
# all clusters, kept in KiB in $run_dir/storage_reserved_memory, off the memory instances may use.

cd $(dirname $0)
source ../../cloudrc

uuid=$1
mb=$2
valid_uuid() { [[ "$1" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]; }
valid_uuid "$uuid" || exit 1
[[ "$mb" =~ ^[0-9]+$ ]] || exit 1
dir=$run_dir/storage/$uuid
mkdir -p $dir
if [ "$mb" -eq 0 ]; then
    rm -f $dir/reserved_mb
else
    echo $mb >$dir/reserved_mb
fi
total=0
for f in $run_dir/storage/*/reserved_mb; do
    [ -f "$f" ] || continue
    v=$(cat $f)
    [[ "$v" =~ ^[0-9]+$ ]] && total=$((total + v))
done
echo $((total * 1024)) >$run_dir/storage_reserved_memory.tmp && mv -f $run_dir/storage_reserved_memory.tmp $run_dir/storage_reserved_memory
