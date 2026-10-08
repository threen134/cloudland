#!/bin/bash
# Keep the pool list of a storage cluster this host is in (shared-storage-design.md §9.2), sent by clapi every few
# minutes as a safety net: <cluster UUID> [remote], the JSON array of the pools on stdin. "remote": the pools of a
# cluster whose file system this host's own cluster mounts (a remote mount), used here, reported by the owner's
# members. Synchronous and quick; the pools themselves are probed in the background (shared_pool_probe.sh, started by
# the heartbeat).

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh

[ $# -lt 1 ] && die "$0 <cluster_UUID>"

list=$(cat)
shared_pools_apply "$1" "$list" "$2" || die "$guard_error"
exit 0
