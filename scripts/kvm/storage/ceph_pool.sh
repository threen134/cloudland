#!/bin/bash
# The RBD pool of a CloudLand pool (shared-storage-design.md §8.3). Input: {"action", "cluster_uuid", "fsid",
# "pool_uuid", "ceph_pool", "conf", "user", ...}
#   create:     {"crush_rule", "media", "replicas", "quota_bytes"}  on an admin host of a managed cluster: the
#               placement rule of the media, the pool with its replicas and quota, the rbd application, the marker
#   quota:      {"quota_bytes"}           0 lifts the quota
#   delete:     the pool, when it holds no image and its marker names the CloudLand pool
#   register:   on a host of an imported cluster, as its client user: the RBD pool its admins made gets the marker
#   unregister: the marker goes, nothing else
# The marker object cloudland-pool says which CloudLand pool an RBD pool is (drv_guard checks it before any write).

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/ceph.sh
STC_SCRIPT=$(readlink -f $0)

input=""
fsid=""
pool=""
uuid=""

# marker <rados command...>: the pool uuid in the marker object, empty when there is none
function marker_of()
{
    "$@" -p $pool get cloudland-pool - 2>/dev/null | sed -n 's/^pool_uuid=//p'
}

function put_marker()
{
    local tmp
    tmp=$(mktemp)
    ceph_marker_text $uuid >$tmp
    "$@" -p $pool put cloudland-pool $tmp
    local rc=$?
    rm -f $tmp
    return $rc
}

function pool_exists()
{
    ceph_admin $fsid osd pool ls -f json | jq -e --arg p "$pool" 'index($p) != null' >/dev/null
}

function do_create()
{
    local rule media replicas min quota have
    rule=$(jq -r '.crush_rule // "replicated_rule"' <<<"$input")
    media=$(jq -r '.media // ""' <<<"$input")
    replicas=$(jq -r .replicas <<<"$input")
    quota=$(jq -r '.quota_bytes // 0' <<<"$input")
    [[ "$rule" =~ ^[A-Za-z0-9_-]+$ ]] || stc_fail "invalid placement rule $rule"
    [ -z "$media" ] || [[ "$media" =~ ^(hdd|ssd|nvme)$ ]] || stc_fail "invalid media $media"
    [[ "$replicas" =~ ^[1-3]$ ]] || stc_fail "invalid replicas $replicas"
    [[ "$quota" =~ ^[0-9]+$ ]] || stc_fail "invalid quota $quota"
    ceph_admin_ready $fsid || stc_fail "this host has no working admin configuration of cluster $fsid"
    if [ "$rule" != "replicated_rule" ] && ! ceph_admin $fsid osd crush rule ls -f json | jq -e --arg r "$rule" 'index($r) != null' >/dev/null; then
        [ -n "$media" ] || stc_fail "placement rule $rule needs a media"
        stc_progress 10 "making the placement rule $rule ($media OSDs on different hosts)"
        ceph_admin $fsid osd crush rule create-replicated $rule default host $media >/dev/null || stc_fail "making the placement rule $rule failed"
    fi
    if pool_exists; then
        have=$(marker_of rados_admin $fsid)
        [ -z "$have" ] || [ "$have" = "$uuid" ] || stc_fail "RBD pool $pool belongs to CloudLand pool $have"
        echo "RBD pool $pool exists already"
    else
        stc_progress 30 "making the RBD pool $pool"
        ceph_admin $fsid osd pool create $pool 32 32 replicated $rule >/dev/null || stc_fail "making the RBD pool failed"
    fi
    min=$((replicas - replicas / 2))
    {
        ceph_admin $fsid osd pool set $pool size $replicas --yes-i-really-mean-it &&
        ceph_admin $fsid osd pool set $pool min_size $min &&
        ceph_admin $fsid osd pool application enable $pool rbd --yes-i-really-mean-it &&
        ceph_admin $fsid osd pool set-quota $pool max_bytes $quota
    } >/dev/null || stc_fail "setting up the RBD pool failed"
    stc_progress 70 "initializing the pool for RBD"
    rbd_admin $fsid pool init $pool || stc_fail "rbd pool init failed"
    put_marker rados_admin $fsid || stc_fail "writing the marker failed"
    stc_result "$(jq -cn --arg p "$pool" '{ceph_pool: $p}')"
}

function do_quota()
{
    local quota
    quota=$(jq -r '.quota_bytes // 0' <<<"$input")
    [[ "$quota" =~ ^[0-9]+$ ]] || stc_fail "invalid quota $quota"
    ceph_admin_ready $fsid || stc_fail "this host has no working admin configuration of cluster $fsid"
    [ "$(marker_of rados_admin $fsid)" = "$uuid" ] || stc_fail "RBD pool $pool is not the one of CloudLand pool $uuid"
    ceph_admin $fsid osd pool set-quota $pool max_bytes $quota >/dev/null || stc_fail "setting the quota failed"
    stc_result "$(jq -cn --argjson q "$quota" '{quota_bytes: $q}')"
}

# drop_bases <listed|all> <rbd command...>: the copies of images CloudLand made in the pool (clapi lists them, nothing is
# cloned from them: the pool holds no volume) and the temporary images of imports that died (shared-storage-design.md
# §9.6). all, for a pool CloudLand made and deletes: any copy by its name, also one whose record was lost (an import
# whose report never came); the pool of an imported cluster stays with its owners, only the listed copies go
function drop_bases()
{
    local scope=$1 b out extra=""
    shift
    for b in $(jq -r '.bases // [] | .[]' <<<"$input"); do
        [[ "$b" =~ ^image-[0-9]+-[0-9a-f]+$ ]] || stc_fail "invalid image copy $b"
    done
    [ "$scope" = "all" ] && extra=$("$@" ls -p $pool 2>/dev/null | grep -E '^image-[0-9]+-[0-9a-f]+$')
    for b in $( (jq -r '.bases // [] | .[]' <<<"$input"; echo "$extra") | sort -u) $("$@" ls -p $pool 2>/dev/null | grep -E '^tmp-image-[0-9]+-[0-9a-f]+-[0-9]+-[0-9]+$'); do
        "$@" snap purge --no-progress $pool/$b >/dev/null 2>&1
        if ! out=$("$@" rm --no-progress $pool/$b 2>&1) && ! grep -q "No such file or directory" <<<"$out"; then
            stc_fail "removing the image copy $b failed: $(tail -1 <<<"$out" | cut -c1-200)"
        fi
        echo "removed $pool/$b"
    done
}

function do_delete()
{
    local images have
    ceph_admin_ready $fsid || stc_fail "this host has no working admin configuration of cluster $fsid"
    if ! pool_exists; then
        echo "RBD pool $pool is gone already"
        stc_result '{"deleted": true}'
        return 0
    fi
    have=$(marker_of rados_admin $fsid)
    [ "$have" = "$uuid" ] || stc_fail "RBD pool $pool is not the one of CloudLand pool $uuid (marker: ${have:-none}): not deleting it"
    drop_bases all rbd_admin $fsid
    images=$(rbd_admin $fsid ls -p $pool 2>/dev/null | wc -l)
    [ "$images" -eq 0 ] || stc_fail "RBD pool $pool still holds $images images"
    images=$(rbd_admin $fsid trash ls -p $pool 2>/dev/null | wc -l)
    [ "$images" -eq 0 ] || stc_fail "RBD pool $pool still holds $images images in its trash"
    stc_progress 50 "deleting the RBD pool $pool"
    ceph_admin $fsid config set mon mon_allow_pool_delete true >/dev/null || stc_fail "allowing the deletion failed"
    ceph_admin $fsid osd pool rm $pool $pool --yes-i-really-really-mean-it >/dev/null
    local rc=$?
    ceph_admin $fsid config set mon mon_allow_pool_delete false >/dev/null
    [ $rc -eq 0 ] || stc_fail "deleting the RBD pool failed"
    stc_result '{"deleted": true}'
}

function client_rados()
{
    timeout 60 rados --conf $conf --id $user "$@"
}

function client_rbd()
{
    timeout 600 rbd --conf $conf --id $user "$@"
}

function do_register()
{
    local have
    [ -f "$conf" ] || stc_fail "this host has no client configuration of the cluster"
    timeout 60 rbd --conf $conf --id $user ls -p $pool >/dev/null 2>&1 || stc_fail "RBD pool $pool can not be read as client.$user: does it exist, may the user use it?"
    have=$(marker_of client_rados)
    [ -z "$have" ] || [ "$have" = "$uuid" ] || stc_fail "RBD pool $pool is registered as CloudLand pool $have already"
    put_marker client_rados || stc_fail "writing the marker into $pool failed: client.$user needs write access to it"
    stc_result "$(jq -cn --arg p "$pool" '{ceph_pool: $p}')"
}

function do_unregister()
{
    [ -f "$conf" ] || stc_fail "this host has no client configuration of the cluster"
    if [ "$(marker_of client_rados)" = "$uuid" ]; then
        # The pool stays with its owners: the copies of images CloudLand made in it go
        drop_bases listed client_rbd
        client_rados -p $pool rm cloudland-pool || stc_fail "removing the marker failed"
    fi
    stc_result '{"unregistered": true}'
}

function stc_main()
{
    local action cluster
    input=$(cat)
    action=$(jq -r .action <<<"$input")
    cluster=$(jq -r .cluster_uuid <<<"$input")
    fsid=$(jq -r '.fsid // ""' <<<"$input")
    uuid=$(jq -r .pool_uuid <<<"$input")
    pool=$(jq -r .ceph_pool <<<"$input")
    conf=$(jq -r '.conf // ""' <<<"$input")
    user=$(jq -r '.user // ""' <<<"$input")
    valid_uuid "$cluster" && valid_uuid "$uuid" || stc_fail "invalid cluster or pool uuid"
    [[ "$pool" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$ ]] || stc_fail "invalid RBD pool name $pool"
    case "$action" in
        create|quota|delete) valid_uuid "$fsid" || stc_fail "invalid fsid" ;;
        register|unregister)
            [ "$conf" = "$(ceph_client_conf $cluster)" ] || stc_fail "invalid client configuration $conf"
            [[ "$user" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$ ]] || stc_fail "invalid client user"
            ;;
    esac
    stc_lock "ceph-$cluster"
    case "$action" in
        create) do_create ;;
        quota) do_quota ;;
        delete) do_delete ;;
        register) do_register ;;
        unregister) do_unregister ;;
        *) stc_fail "unknown action $action" ;;
    esac
}

stc_run "$@"
