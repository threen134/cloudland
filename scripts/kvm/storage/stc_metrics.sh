#!/bin/bash
# Metrics of the storage clusters of this host for the node_exporter textfile collector (shared-storage-design.md
# §14.3, appendix E). The heartbeat starts it once a minute in the background: a storage command can hang on a dead
# mount for good, and that must never be the heartbeat. The backend_metrics hook of a kind (backends/<kind>.sh) prints
# the lines of one cluster; a kind without the hook exports nothing from its hosts (Ceph is scraped from its mgr). The
# file of a cluster this host is no longer in is removed.

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh

mkdir -p $stc_log_dir
exec </dev/null >/dev/null 2>>$stc_log_dir/metrics.log
exec 9>$stc_lock_dir/cloudland-storage-metrics.lock
flock -n 9 || exit 0
dir=$(node_textfile_dir)
[ -n "$dir" ] || exit 0

keep=" "
for d in $shared_storage_dir/*/; do
    uuid=$(basename "$d")
    valid_uuid "$uuid" || continue
    # The kind, written when the host joined; the hosts of an imported GPFS cluster only have its pool list
    kind=$(cat $d/member 2>/dev/null)
    if [ -z "$kind" ] && jq -e 'any(.[]?; .driver == "gpfs")' $d/shared_pools.json >/dev/null 2>&1; then
        kind=gpfs
    fi
    [ -n "$kind" ] || continue
    name=cloudland_storage_$uuid.prom
    lines=$(backend_load "$kind" && declare -F backend_metrics >/dev/null && backend_metrics "$uuid")
    if [ -n "$lines" ]; then
        printf '%s\n' "$lines" >$dir/$name.$$ && mv -f $dir/$name.$$ $dir/$name
        keep+="$name "
    fi
done
for f in $dir/cloudland_storage_*.prom; do
    [ -f "$f" ] || continue
    [[ "$keep" == *" $(basename "$f") "* ]] || rm -f "$f"
done
exit 0
