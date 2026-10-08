#!/bin/bash
# The client side of a Ceph cluster on a host (shared-storage-design.md §8.4, §8.8): the configuration
# /etc/ceph/<cluster uuid>.conf (fsid and monitors), the keyring of the client user next to it, and the libvirt secret
# QEMU authenticates with, holding the same key under the same uuid on every host so a live migration finds it.
# Input: {"action", "cluster_uuid", "fsid", "mon_addrs", "client_user", "client_key", "secret_uuid", "client_key_alt"}
#   setup:  write the three and check the cluster answers; when it refuses the key as client_key_alt (the pending
#           key of an aborted rotation) is given, that one                     result: {"health", "key": current|alt}
#   import: install the client packages if missing, write the three and check the cluster is the one named (its fsid)
#           and answers; nothing on the cluster is changed                         result: {"health", "version"}
#   remove: the three go
#   rekey:  setup with the pending key of a rotation: its first use makes it the key of the user. QEMUs running with
#           the old key keep their sessions; the libvirt secret gives the new key to those starting from now on. When
#           the check fails the key the host had is written back

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
    # The secret first, the keyring last: a host failing in between keeps its keyring, the key its tools (rbd, the
    # pool probe) use, and none of them uses a pending key nobody checked (its first use would make it the key)
    tmp=$(mktemp)
    printf "<secret ephemeral='no' private='yes'>\n  <uuid>%s</uuid>\n  <usage type='ceph'>\n    <name>client.%s %s</name>\n  </usage>\n</secret>\n" \
        "$secret" "$user" "$uuid" >$tmp
    virsh secret-define $tmp >/dev/null || { rm -f $tmp; stc_fail "virsh secret-define failed"; }
    (umask 077 && printf '%s' "$key" >$tmp)
    virsh secret-set-value $secret --file $tmp >/dev/null || { rm -f $tmp; stc_fail "virsh secret-set-value failed"; }
    rm -f $tmp
    # printf is a builtin: the key never shows in the arguments of a process
    (umask 077 && printf '[client.%s]\n    key = %s\n' "$user" "$key" >$keyring.cl-new) && mv -f $keyring.cl-new $keyring
}

function stc_main()
{
    local input action uuid fsid mons user key secret m conf health have version alt old used
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
    alt=$(jq -r '.client_key_alt // empty' <<<"$input")
    [ -z "$alt" ] || [[ "$alt" =~ ^[A-Za-z0-9+/]{38,64}={0,2}$ ]] || stc_fail "invalid alternative client key"
    # The key this host had, to go back to when the pending key of a rotation does not get through
    old=$(awk '$1 == "key" {print $3}' $(ceph_client_keyring $uuid $user) 2>/dev/null)
    stc_progress 30 "writing the client configuration"
    write_client "$uuid" "$fsid" "$mons" "$user" "$key" "$secret"
    conf=$(ceph_client_conf $uuid)
    stc_progress 60 "checking the cluster answers"
    used=current
    have=$(timeout 60 ceph --conf $conf --id $user fsid 2>&1)
    # The pending key of an aborted rotation, when the cluster refuses the recorded key (not when it does not answer:
    # its first use would make it the key)
    if [ "$have" != "$fsid" ] && [ -n "$alt" ] && grep -qiE "permission denied|errno 13" <<<"$have"; then
        echo "the cluster refuses the recorded key, trying the pending key of a rotation"
        write_client "$uuid" "$fsid" "$mons" "$user" "$alt" "$secret"
        have=$(timeout 60 ceph --conf $conf --id $user fsid 2>&1)
        used=alt
    fi
    if [ "$have" != "$fsid" ]; then
        # A failed import leaves nothing behind; a failed rekey puts the key back: a host left with a pending key nobody
        # checked would make it the key the first time a QEMU starts, and the other hosts would be refused
        [ "$action" = "import" ] && ceph_client_remove $uuid $secret
        if [ "$action" = "rekey" ] && [[ "$old" =~ ^[A-Za-z0-9+/]{38,64}={0,2}$ ]]; then
            write_client "$uuid" "$fsid" "$mons" "$user" "$old" "$secret"
            stc_fail "the cluster does not answer as client.$user with the new key, the key before is back: ${have:0:300}"
        fi
        stc_fail "the cluster does not answer as client.$user: ${have:0:300}"
    fi
    health=$(timeout 60 ceph --conf $conf --id $user health -f json 2>/dev/null | jq -r '.status // empty')
    version=$(timeout 60 ceph --conf $conf --id $user version 2>/dev/null | awk '{print $3}')
    echo "cluster $fsid answers: ${health:-health unknown}, ${version:-version unknown}"
    stc_result "$(jq -cn --arg h "$health" --arg v "$version" --arg k "$used" '{health: $h, version: $v, key: $k}')"
}

stc_run "$@"
