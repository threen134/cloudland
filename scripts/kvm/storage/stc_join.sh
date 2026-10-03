#!/bin/bash
# First step on a host that joins a storage cluster (shared-storage-design.md §6.5): mark the host as a member, so
# a retry or a later precheck does not take software it half installed for software of somebody else, and hold the
# kernel packages when the kind builds a kernel module (§7.7). Input: {"cluster_uuid", "kind", "hold_kernel": bool}

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input uuid kind hold dir pkgs p held=""
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    kind=$(jq -r .kind <<<"$input")
    hold=$(jq -r '.hold_kernel // false' <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    dir=$run_dir/storage/$uuid
    mkdir -p $dir
    echo "$kind" >$dir/member
    if [ "$hold" = "true" ]; then
        # The metapackages pull new kernels in; the running kernel's own packages must not go either
        pkgs="linux-generic linux-image-generic linux-headers-generic linux-image-$(uname -r) linux-headers-$(uname -r) linux-modules-$(uname -r)"
        for p in $pkgs; do
            dpkg -s $p >/dev/null 2>&1 || continue
            apt-mark hold $p >/dev/null && held="$held $p"
        done
        echo $held >$dir/held_packages
        echo "held:$held"
    fi
    if backend_load "$kind" && declare -F backend_join >/dev/null; then
        backend_join "$uuid" || stc_fail "the $kind part of joining failed"
    fi
    stc_result "$(jq -cn --arg h "$held" '{held: ($h | split(" ") | map(select(. != "")))}')"
}

stc_run "$@"
