#!/bin/bash
# Metrics of the storage clusters of this host for the node_exporter textfile collector (shared-storage-design.md
# §14.3, appendix E). The heartbeat starts it once a minute in the background: a storage command can hang on a dead
# mount for good, and that must never be the heartbeat. The backend_metrics hook of a kind (backends/<kind>.sh) prints
# the lines of one cluster, told whether CloudLand manages it (a managed Ceph cluster is scraped from its mgr, an
# imported one is read by its clients here); every cluster also gets cloudland_storage_cluster_info, the list the
# Grafana board picks clusters from. The file of a cluster this host is no longer in is removed, and a cluster whose
# pools this host only uses through a remote mount (a "remote" marker beside its pool list) is not reported here.

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
    mode=managed
    # The pools of a cluster whose file system this host's own cluster mounts: that cluster's members report it (the
    # GPFS commands here would describe this host's own cluster under the owner's uuid)
    if [ -z "$kind" ] && [ -f $d/remote ]; then
        continue
    fi
    if [ -z "$kind" ]; then
        # An imported cluster: its hosts only have its pool list, or the client configuration of a Ceph cluster
        mode=external
        if jq -e 'any(.[]?; .driver == "gpfs")' $d/shared_pools.json >/dev/null 2>&1; then
            kind=gpfs
        elif [ -f /etc/ceph/$uuid.conf ]; then
            kind=ceph
        fi
    fi
    [ -n "$kind" ] || continue
    [[ "$kind" =~ ^[a-z]+$ ]] || continue
    name=cloudland_storage_$uuid.prom
    lines=$(backend_load "$kind" && declare -F backend_metrics >/dev/null && backend_metrics "$uuid" "$mode")
    lines=$(printf 'cloudland_storage_cluster_info{cluster="%s",kind="%s",mode="%s"} 1\n%s' "$uuid" "$kind" "$mode" "$lines")
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
