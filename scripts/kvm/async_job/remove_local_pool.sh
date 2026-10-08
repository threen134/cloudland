#!/bin/bash
# Remove a local storage pool from this host (§4.5 of the local storage plan)
# mode normal: refuse when volumes/, nvram/ or tmp/ is not empty; force: delete leftover files first;
# lost: only unmount and deactivate, never delete files or wipe disks

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh

[ $# -lt 2 ] && die "$0 <pool_uuid> <normal|force|lost>"

pool=$1
mode=$2
root=$pools_dir/$pool

# The pool is broken half way (unmounted, fstab line gone): clapi marks it as an error
function fail()
{
    pool_status_callback remove $pool error "$1" "${2:-}"
    exit 0
}

# Nothing was changed and the pool is still mounted: clapi puts it back as it was, with the reason
function refuse()
{
    pool_status_callback remove $pool refused "$1" "${2:-}"
    exit 0
}

valid_uuid "$pool" || { pool_status_callback remove "$pool" error "invalid pool uuid" ""; exit 0; }

if [ "$mode" = "lost" ]; then
    # The disks may be broken: bounded waits only, and nothing that could make things worse
    vg=$(pool_vg $pool)
    timeout 30 umount -l $root >/dev/null 2>&1
    [ -n "$vg" ] && timeout 60 vgchange -an $vg >/dev/null 2>&1
    sed -i "\# $root #d" /etc/fstab
    systemctl daemon-reload >/dev/null 2>&1
    rmdir $root 2>/dev/null
    rm -f $pool_state_dir/$pool.* /var/lib/prometheus/node-exporter/cloudland_pool_$pool.prom /var/lib/node_exporter/cloudland_pool_$pool.prom
    pool_status_callback remove $pool removed "" '{"mode":"lost"}'
    exit 0
fi

pool_lock $pool exclusive 60 || refuse "another operation on the pool is running"
disks_lock 60 || refuse "another storage operation is running"
vg=$(pool_vg $pool)
removed_files=""
if timeout 10 mountpoint -q $root; then
    # tmp/ counts too: an .incoming-<migration> placeholder there means a migration prepared to write into this pool
    # (clapi refuses while the migration still holds its reservation; one left after that is a leftover to show)
    left=$(cd $root && ls -A volumes nvram tmp 2>/dev/null | grep -v ':$' | grep -v '^$')
    if [ -n "$left" ]; then
        if [ "$mode" != "force" ]; then
            refuse "pool is not empty" "$(jq -cn --arg files "$left" '{files: ($files | split("\n"))}')"
        fi
        removed_files=$left
        rm -f $root/volumes/* $root/nvram/*
        find $root/tmp -mindepth 1 -delete 2>/dev/null
    fi
    # Still mounted and whole when the unmount fails (a force removal has only emptied it)
    umount $root || refuse "failed to unmount $root, it is in use"
fi
sed -i "\# $root #d" /etc/fstab
systemctl daemon-reload >/dev/null 2>&1
rmdir $root 2>/dev/null

if [ -n "$vg" ]; then
    pvs=$(timeout 20 pvs --noheadings -o pv_name --select vg_name=$vg 2>/dev/null | xargs)
    vgremove -f $vg >/dev/null 2>&1 || fail "vgremove $vg failed"
    for pv in $pvs; do
        if [[ "$(lsblk -dno TYPE $pv 2>/dev/null)" == raid* ]]; then
            md=$(readlink -f $pv)
            uuid=$(md_uuid $md)
            members=$(mdadm --detail $md 2>/dev/null | awk '$NF ~ /^\/dev\// && ($0 ~ /active|spare|rebuilding|faulty/) {print $NF}')
            mdadm --stop $md >/dev/null 2>&1
            for m in $members; do
                mdadm --zero-superblock $m >/dev/null 2>&1
                wipefs -a -q $m >/dev/null 2>&1
            done
            mdadm_conf_del $uuid
        else
            pvremove -y -q $pv >/dev/null 2>&1
            wipefs -a -q $pv >/dev/null 2>&1
        fi
    done
fi
rm -f $pool_state_dir/$pool.* /var/lib/prometheus/node-exporter/cloudland_pool_$pool.prom /var/lib/node_exporter/cloudland_pool_$pool.prom
pool_status_callback remove $pool removed "" "$(jq -cn --arg files "$removed_files" '{mode: "normal", files: ($files | split("\n") | map(select(. != "")))}')"
