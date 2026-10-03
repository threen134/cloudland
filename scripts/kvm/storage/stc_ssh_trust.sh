#!/bin/bash
# Let the admin hosts of a storage cluster log in to this host as root with the key of the cluster, and only them
# (shared-storage-design.md §6.6). Input: {"cluster_uuid", "kind", "public_key", "from": [addresses], "remove": bool,
# "private_key": "...", "known_hosts": ["<address> <type> <key>", ...]}; the last two only on admin hosts.
# The line in authorized_keys ends with the comment cloudland-storage-<cluster uuid>, which is how it is found again.
# The backend of the kind may then write what its tools need around the key (backend_trust_written).

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input uuid key from remove tag line="" dir kind
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    key=$(jq -r '.public_key // ""' <<<"$input")
    from=$(jq -r '(.from // []) | join(",")' <<<"$input")
    remove=$(jq -r '.remove // false' <<<"$input")
    tag="cloudland-storage-$uuid"
    if [ "$remove" != "true" ]; then
        [[ "$key" =~ ^ssh-ed25519\ [A-Za-z0-9+/=]+$ ]] || stc_fail "invalid public key"
        [[ "$from" =~ ^[0-9.,]+$ ]] || stc_fail "invalid source addresses"
        line="from=\"$from\",no-agent-forwarding,no-port-forwarding,no-X11-forwarding $key $tag"
    fi
    stc_keys_update suffix "$tag" "$line" || stc_fail "rewriting authorized_keys failed"
    dir=$run_dir/storage/$uuid
    if [ "$remove" = "true" ]; then
        rm -f $dir/id_ed25519 $dir/known_hosts
        return 0
    fi
    if [ "$(jq -r '.private_key // ""' <<<"$input")" != "" ]; then
        mkdir -p $dir && chmod 700 $dir
        (umask 077 && jq -r .private_key <<<"$input" >$dir/id_ed25519)
        jq -r '(.known_hosts // [])[]' <<<"$input" >$dir/known_hosts
        chmod 600 $dir/known_hosts
    else
        rm -f $dir/id_ed25519 $dir/known_hosts
    fi
    mkdir -p $dir && chmod 700 $dir
    kind=$(jq -r '.kind // ""' <<<"$input")
    if [ -n "$kind" ] && backend_load "$kind" && declare -F backend_trust_written >/dev/null; then
        backend_trust_written $dir || stc_fail "the $kind part of the ssh trust failed"
    fi
    stc_result "$(jq -cn --arg t "$tag" '{line: $t}')"
}

stc_run "$@"
