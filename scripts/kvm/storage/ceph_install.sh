#!/bin/bash
# Install Ceph from the distribution (shared-storage-design.md §8.1): ceph-common (the client tools and librbd the
# hosts and QEMU use) everywhere; on a host that runs daemons also cephadm and the container image of the daemons.
# The image is the one of the release the host installed (quay.io/ceph/ceph:v<version>) unless one is given: the
# daemons must not be newer than the client libraries of the hosts (a newer release writes keys an older client can
# not read). Input: {"cluster_uuid", "image", "orch"}. Result: {"version", "image"} (image empty on a client host)

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/ceph.sh
STC_SCRIPT=$(readlink -f $0)

function image_version()
{
    docker run --rm --entrypoint ceph "$1" --version 2>/dev/null | awk '{print $3}'
}

function stc_main()
{
    local input uuid image orch version have pkgs i
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    image=$(jq -r '.image // ""' <<<"$input")
    orch=$(jq -r '.orch // false' <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    [ -z "$image" ] || [[ "$image" =~ ^[a-z0-9][a-z0-9./:_@-]{0,199}$ ]] || stc_fail "invalid image $image"
    stc_lock "ceph-install"
    export DEBIAN_FRONTEND=noninteractive
    pkgs="ceph-common qemu-block-extra"
    # cephadm of Ubuntu 24.04 (19.2.3) imports jinja2 without depending on it
    [ "$orch" = "true" ] && pkgs="$pkgs cephadm lvm2 python3-jinja2"
    stc_progress 10 "installing $pkgs"
    apt-get -o DPkg::Lock::Timeout=600 update -q || echo "apt-get update failed, going on with the lists at hand"
    apt-get -o DPkg::Lock::Timeout=600 install -y -q $pkgs || stc_fail "apt-get install $pkgs failed"
    version=$(ceph --version 2>/dev/null | awk '{print $3}')
    [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || stc_fail "ceph --version gave no release ($version)"
    echo "ceph-common $version"
    if [ "$orch" != "true" ]; then
        stc_result "$(jq -cn --arg v "$version" '{version: $v, image: ""}')"
        return 0
    fi
    # The rust coreutils of Ubuntu 26.04 refuse a numeric owner that has no passwd entry, and cephadm makes the
    # directories of the daemons owned by 167, the ceph user of the image
    if ! getent passwd 167 >/dev/null; then
        getent group 167 >/dev/null || groupadd -r -g 167 ceph-ctr || stc_fail "groupadd ceph-ctr failed"
        useradd -r -u 167 -g 167 -M -d /var/lib/ceph -s /usr/sbin/nologin ceph-ctr || stc_fail "useradd ceph-ctr failed"
    fi
    ceph_unit_order $uuid || stc_fail "ordering the units of the cluster failed"
    [ -n "$image" ] || image=quay.io/ceph/ceph:v$version
    stc_progress 40 "pulling $image"
    for i in 1 2 3; do
        timeout 1800 docker pull -q "$image" && break
        [ $i -eq 3 ] && stc_fail "docker pull $image failed"
        sleep 10
    done
    have=$(image_version "$image")
    [[ "$have" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || stc_fail "the image $image runs no ceph"
    if dpkg --compare-versions "$have" gt "$version"; then
        stc_fail "the image runs Ceph $have, newer than the Ceph $version of this host: its keys could not be read here"
    fi
    touch $ceph_ours
    echo "image $image runs Ceph $have"
    stc_result "$(jq -cn --arg v "$version" --arg i "$image" '{version: $v, image: $i}')"
}

stc_run "$@"
