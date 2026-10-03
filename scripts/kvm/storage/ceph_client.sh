#!/bin/bash
# The client side of a Ceph cluster on a host (shared-storage-design.md §8.4, §8.8): the configuration
# /etc/ceph/<cluster uuid>.conf (fsid and monitors), the keyring of the client user next to it, and the libvirt secret
# QEMU authenticates with, holding the same key under the same uuid on every host so a live migration finds it.
# Input: {"action", "cluster_uuid", "fsid", "mon_addrs", "client_user", "client_key", "secret_uuid"}
#   setup:  write the three and check the cluster answers                         result: {"health"}
#   import: install the client packages if missing, write the three and check the cluster is the one named (its fsid)
#           and answers; nothing on the cluster is changed                         result: {"health", "version"}
#   remove: the three go

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
source ./backends/ceph.sh
STC_SCRIPT=$(readlink -f $0)

function write_client()
{
    local uuid=$1 fsid=$2 mons=$3 user=$4 key=$5 secret=$6 conf keyring tmp
    conf=$(ceph_client_conf $uuid)
    keyring=$(ceph_client_keyring $uuid $user)
    mkdir -p $ceph_conf_dir
    {
        echo "# CloudLand: client configuration of storage cluster $uuid"
        echo "[global]"
        echo "    fsid = $fsid"
        echo "    mon_host = $mons"
        echo "[client.$user]"
        echo "    keyring = $keyring"
    } >$conf.cl-new
    chmod 644 $conf.cl-new
    mv -f $conf.cl-new $conf
    # printf is a builtin: the key never shows in the arguments of a process
    (umask 077 && printf '[client.%s]\n    key = %s\n' "$user" "$key" >$keyring.cl-new) && mv -f $keyring.cl-new $keyring
    tmp=$(mktemp)
    printf "<secret ephemeral='no' private='yes'>\n  <uuid>%s</uuid>\n  <usage type='ceph'>\n    <name>client.%s %s</name>\n  </usage>\n</secret>\n" \
        "$secret" "$user" "$uuid" >$tmp
    virsh secret-define $tmp >/dev/null || { rm -f $tmp; stc_fail "virsh secret-define failed"; }
    (umask 077 && printf '%s' "$key" >$tmp)
    virsh secret-set-value $secret --file $tmp >/dev/null || { rm -f $tmp; stc_fail "virsh secret-set-value failed"; }
    rm -f $tmp
}

function stc_main()
{
    local input action uuid fsid mons user key secret m conf health have version
    input=$(cat)
    action=$(jq -r .action <<<"$input")
    uuid=$(jq -r .cluster_uuid <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    stc_lock "ceph-client-$uuid"
    if [ "$action" = "remove" ]; then
        ceph_client_remove $uuid "$(jq -r '.secret_uuid // ""' <<<"$input")"
        stc_result '{"removed": true}'
        return 0
    fi
    fsid=$(jq -r .fsid <<<"$input")
    user=$(jq -r .client_user <<<"$input")
    key=$(jq -r .client_key <<<"$input")
    secret=$(jq -r .secret_uuid <<<"$input")
    mons=""
    for m in $(jq -r '.mon_addrs[]' <<<"$input"); do
        [[ "$m" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}(:[0-9]{2,5})?$ ]] || stc_fail "invalid mon address $m"
        mons="${mons:+$mons,}$m"
    done
    valid_uuid "$fsid" && valid_uuid "$secret" || stc_fail "invalid fsid or secret uuid"
    [[ "$user" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$ ]] || stc_fail "invalid client user"
    [[ "$key" =~ ^[A-Za-z0-9+/]{38,64}={0,2}$ ]] || stc_fail "invalid client key"
    [ -n "$mons" ] || stc_fail "no mon address"
    if [ "$action" = "import" ] && ! { command -v ceph >/dev/null && dpkg -s qemu-block-extra >/dev/null 2>&1; }; then
        stc_progress 10 "installing the Ceph client packages"
        DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=600 install -y -q ceph-common qemu-block-extra ||
            stc_fail "installing ceph-common failed"
    fi
    command -v ceph >/dev/null || stc_fail "ceph-common is not installed"
    stc_progress 30 "writing the client configuration"
    write_client "$uuid" "$fsid" "$mons" "$user" "$key" "$secret"
    conf=$(ceph_client_conf $uuid)
    stc_progress 60 "checking the cluster answers"
    have=$(timeout 60 ceph --conf $conf --id $user fsid 2>&1)
    if [ "$have" != "$fsid" ]; then
        # A failed import leaves nothing behind
        [ "$action" = "import" ] && ceph_client_remove $uuid $secret
        stc_fail "the cluster does not answer as client.$user: ${have:0:300}"
    fi
    health=$(timeout 60 ceph --conf $conf --id $user health -f json 2>/dev/null | jq -r '.status // empty')
    version=$(timeout 60 ceph --conf $conf --id $user version 2>/dev/null | awk '{print $3}')
    echo "cluster $fsid answers: ${health:-health unknown}, ${version:-version unknown}"
    stc_result "$(jq -cn --arg h "$health" --arg v "$version" '{health: $h, version: $v}')"
}

stc_run "$@"
