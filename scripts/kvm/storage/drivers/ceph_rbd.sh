# -*- mode: sh -*-
# Driver of the pools on Ceph (shared-storage-design.md §8.3, §9.4): a pool is an RBD pool, a volume the raw RBD
# image volume-<id> in it. Every host reaches the cluster as its client user with /etc/ceph/<cluster uuid>.conf and
# the keyring next to it (ceph_client.sh); QEMU gets the key from the libvirt secret of the cluster. Loaded by drv_load
# (storage_lib.sh); nothing here prints to stdout.

drv_name=ceph_rbd
drv_family=block
# A full cluster blocks the writes instead of failing them; stop is for the errors that do come back
drv_error_policy=stop
# RBD is safe for live migration with the write-back cache of librbd (libvirt checks it)
drv_cache=writeback
drv_rbd_features=layering,exclusive-lock,object-map,fast-diff,deep-flatten

# drv_init <json>: the pool of the JSON clapi sent. Sets guard_error on failure
function drv_init()
{
    local json=$1
    drv_pool=$(jq -r '.pool // empty' <<<"$json" 2>/dev/null)
    drv_cluster=$(jq -r '.cluster // empty' <<<"$json" 2>/dev/null)
    drv_conf=$(jq -r '.conf // empty' <<<"$json" 2>/dev/null)
    drv_user=$(jq -r '.user // empty' <<<"$json" 2>/dev/null)
    drv_secret=$(jq -r '.secret_uuid // empty' <<<"$json" 2>/dev/null)
    drv_ceph_pool=$(jq -r '.ceph_pool // empty' <<<"$json" 2>/dev/null)
    drv_quota=$(jq -r '.quota_bytes // 0' <<<"$json" 2>/dev/null)
    drv_root=""
    if ! valid_uuid "$drv_pool" || ! valid_uuid "$drv_cluster" || ! valid_uuid "$drv_secret"; then
        guard_error="invalid pool, cluster or secret uuid"
        return 1
    fi
    if [ "$drv_conf" != "/etc/ceph/$drv_cluster.conf" ]; then
        guard_error="invalid client configuration '$drv_conf'"
        return 1
    fi
    if ! [[ "$drv_user" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$ ]] || ! [[ "$drv_ceph_pool" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$ ]]; then
        guard_error="invalid client user or RBD pool"
        return 1
    fi
    [[ "$drv_quota" =~ ^[0-9]+$ ]] || drv_quota=0
    return 0
}

# drv_rbd / drv_rados / drv_ceph <args...>: the tools as the client user, never hanging: a cluster that does not
# answer looks like an unavailable pool
function drv_rbd()
{
    timeout ${DRV_TIMEOUT:-60} rbd --conf "$drv_conf" --id "$drv_user" "$@"
}

function drv_rados()
{
    timeout ${DRV_TIMEOUT:-60} rados --conf "$drv_conf" --id "$drv_user" "$@"
}

function drv_ceph()
{
    timeout ${DRV_TIMEOUT:-60} ceph --conf "$drv_conf" --id "$drv_user" "$@"
}

# drv_volume <json> <volume id>: the volume of the JSON, which must be the image of that volume (drv_vol)
function drv_volume()
{
    drv_vol=$(jq -r '.image // empty' <<<"$1" 2>/dev/null)
    if ! [[ "$2" =~ ^[0-9]+$ ]] || [ "$drv_vol" != "volume-$2" ]; then
        guard_error="'$drv_vol' is not the image of volume $2 in pool $drv_pool"
        return 1
    fi
    return 0
}

# drv_guard: the client configuration of the cluster is on this host and the RBD pool is the one clapi means: the
# marker object written with the pool names it. Sets guard_error on failure
function drv_guard()
{
    local marker
    guard_error=""
    if [ ! -f "$drv_conf" ]; then
        guard_error="pool $drv_pool: this host has no client configuration of cluster $drv_cluster"
        return 1
    fi
    marker=$(DRV_TIMEOUT=20 drv_rados -p "$drv_ceph_pool" get cloudland-pool - 2>/dev/null)
    if ! grep -qx "driver=$drv_name" <<<"$marker" || ! grep -qx "pool_uuid=$drv_pool" <<<"$marker"; then
        guard_error="pool $drv_pool: RBD pool $drv_ceph_pool does not answer or its marker names another pool"
        return 1
    fi
    return 0
}

# drv_probe: whether the pool can be written now, and its room. Sets probe_json ({"size", "used", "avail"} in bytes)
# or guard_error. The room of an RBD pool is what Ceph says it can still store in it (max_avail, which counts the
# replicas and the fullest OSD) plus what it holds; a quota caps it
function drv_probe()
{
    local f stats stored avail size
    probe_json=""
    drv_guard || return 1
    f=.probe-${NODE_ID:-0}-$$
    # A full cluster blocks writes: a probe that can not write in 20 seconds says so
    if ! DRV_TIMEOUT=20 drv_rados -p "$drv_ceph_pool" put $f /proc/self/cmdline 2>/dev/null; then
        guard_error="pool $drv_pool: RBD pool $drv_ceph_pool can not be written"
        return 1
    fi
    DRV_TIMEOUT=20 drv_rados -p "$drv_ceph_pool" rm $f 2>/dev/null
    stats=$(DRV_TIMEOUT=20 drv_ceph df -f json 2>/dev/null | jq -c --arg p "$drv_ceph_pool" '.pools[] | select(.name == $p) | .stats')
    stored=$(jq -r '.stored // empty' <<<"$stats" 2>/dev/null)
    avail=$(jq -r '.max_avail // empty' <<<"$stats" 2>/dev/null)
    if ! [[ "$stored" =~ ^[0-9]+$ ]] || ! [[ "$avail" =~ ^[0-9]+$ ]]; then
        guard_error="pool $drv_pool: ceph df gave no figures for $drv_ceph_pool"
        return 1
    fi
    size=$((stored + avail))
    if [ "$drv_quota" -gt 0 ] && [ "$drv_quota" -lt "$size" ]; then
        size=$drv_quota
        avail=$((size > stored ? size - stored : 0))
    fi
    probe_json=$(jq -cn --argjson s $size --argjson u $stored --argjson a $avail '{size: $s, used: $u, avail: $a}')
}

function drv_exists()
{
    drv_rbd info "$drv_ceph_pool/$1" >/dev/null 2>&1
}

# drv_size <image>: size in bytes
function drv_size()
{
    drv_rbd info --format json "$drv_ceph_pool/$1" 2>/dev/null | jq -r '.size // empty'
}

# drv_create <image> <size GB>: a new image; rbd create fails when the name is taken. Sets guard_error on failure
function drv_create()
{
    local image=$1 gb=$2
    if ! [[ "$gb" =~ ^[1-9][0-9]*$ ]]; then
        guard_error="invalid size '$gb'"
        return 1
    fi
    if ! drv_rbd create "$drv_ceph_pool/$image" --size ${gb}G --image-feature $drv_rbd_features >/dev/null 2>&1; then
        drv_exists "$image" && guard_error="$drv_ceph_pool/$image already exists" || guard_error="rbd create $drv_ceph_pool/$image failed"
        return 1
    fi
    return 0
}

# drv_resize <image> <size GB>: grow an image; growing only, rbd refuses to shrink without --allow-shrink
function drv_resize()
{
    drv_rbd resize "$drv_ceph_pool/$1" --size "${2}G" >/dev/null 2>&1
}

# drv_delete <image>: remove the image of one volume; one that is gone already is fine
function drv_delete()
{
    if ! [[ "$1" =~ ^volume-[0-9]+$ ]]; then
        guard_error="$1 is not a volume image of pool $drv_pool"
        return 1
    fi
    local out rc
    out=$(DRV_TIMEOUT=600 drv_rbd rm --no-progress "$drv_ceph_pool/$1" 2>&1)
    rc=$?
    [ $rc -eq 0 ] && return 0
    # Gone only when Ceph says so: a timeout or a cluster out of reach leaves the image, and clapi would drop the
    # record of a volume that still holds data and capacity. rbd answers ENOENT for a missing image or pool
    grep -q "No such file or directory" <<<"$out" && return 0
    if [ $rc -eq 124 ]; then
        guard_error="rbd rm $drv_ceph_pool/$1 timed out"
    else
        guard_error="rbd rm $drv_ceph_pool/$1 failed: $(tail -1 <<<"$out" | cut -c1-200)"
    fi
    return 1
}

# drv_users <image>: the clients that have the image open (its watchers), by address
function drv_users()
{
    drv_rbd status --format json "$drv_ceph_pool/$1" 2>/dev/null | jq -r '[.watchers[]?.address] | join(" ")'
}

# drv_disk_xml <image> <device>: the libvirt disk of a volume. The monitors come from the configuration file of the
# cluster, so the disk does not change when they do
function drv_disk_xml()
{
    printf "<disk type='network' device='disk'>\n   <driver name='qemu' type='raw' cache='%s' discard='unmap' error_policy='%s'/>\n   <source protocol='rbd' name='%s/%s'>\n      <config file='%s'/>\n      <auth username='%s'>\n         <secret type='ceph' uuid='%s'/>\n      </auth>\n   </source>\n   <target dev='%s' bus='virtio'/>\n</disk>\n" \
        "$drv_cache" "$drv_error_policy" "$drv_ceph_pool" "$1" "$drv_conf" "$drv_user" "$drv_secret" "$2"
}
