#!/bin/bash
# Let the admin hosts of a storage cluster log in to this host as root with the key of the cluster, and only them
# (shared-storage-design.md §6.6). Input: {"cluster_uuid", "kind", "public_key", "from": [addresses], "remove": bool,
# "private_key": "...", "known_hosts": ["<address> <type> <key>", ...], "rotate": "", "check": [addresses]}; the
# private key and the known hosts only on admin hosts.
# The line in authorized_keys ends with the comment cloudland-storage-<cluster uuid>, which is how it is found again.
# The backend of the kind may then write what its tools need around the key (backend_trust_written).
# A new key of the cluster comes in three passes over every member (rotate), so a host always takes the key the
# admin hosts log in with, whichever pass they are at:
#   add:    a second line for the new key, ending with cloudland-storage-<cluster uuid>-rotate
#   switch: the admin hosts take the new private key; the lines stay
#   drop:   the line of the cluster gets the new key, the second line goes
# Without rotate the second line goes too: what an aborted rotation left. check: addresses an admin host logs in to
# with the key of the cluster afterwards, as its tools do

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

# trust_line <tag>: the line for the public key of the input, checked before (check_line)
function trust_line()
{
    echo "from=\"$from\",no-agent-forwarding,no-port-forwarding,no-X11-forwarding $key $1"
}

function check_line()
{
    [[ "$key" =~ ^ssh-ed25519\ [A-Za-z0-9+/=]+$ ]] || stc_fail "invalid public key"
    [[ "$from" =~ ^[0-9.,]+$ ]] || stc_fail "invalid source addresses"
}

# write_private <cluster dir>: the private key and the host keys of an admin host, nothing on the others
function write_private()
{
    local dir=$1 kind
    if [ "$(jq -r '.private_key // ""' <<<"$input")" != "" ]; then
        mkdir -p $dir && chmod 700 $dir
        (umask 077 && jq -r .private_key <<<"$input" >$dir/id_ed25519.cl-new) && mv -f $dir/id_ed25519.cl-new $dir/id_ed25519
        # ssh offers the public key next to the private one: one left from the key before is refused once it signs
        ssh-keygen -y -f $dir/id_ed25519 >$dir/id_ed25519.pub || stc_fail "the private key of the cluster is not valid"
        jq -r '(.known_hosts // [])[]' <<<"$input" >$dir/known_hosts
        chmod 600 $dir/known_hosts
    else
        rm -f $dir/id_ed25519 $dir/id_ed25519.pub $dir/known_hosts
    fi
    mkdir -p $dir && chmod 700 $dir
    kind=$(jq -r '.kind // ""' <<<"$input")
    if [ -n "$kind" ] && backend_load "$kind" && declare -F backend_trust_written >/dev/null; then
        backend_trust_written $dir || stc_fail "the $kind part of the ssh trust failed"
    fi
}

# check_logins <cluster dir>: every address takes the key of the cluster
function check_logins()
{
    local dir=$1 addr failed=""
    for addr in $(jq -r '(.check // [])[]' <<<"$input"); do
        [[ "$addr" =~ ^[0-9.]+$ ]] || stc_fail "invalid address $addr"
        [ -f $dir/id_ed25519 ] || stc_fail "this host has no key of the cluster"
        timeout 30 ssh -i $dir/id_ed25519 -o UserKnownHostsFile=$dir/known_hosts -o StrictHostKeyChecking=yes -o BatchMode=yes \
            -o ConnectTimeout=10 -o IdentitiesOnly=yes root@$addr true </dev/null >/dev/null 2>&1 || failed="$failed $addr"
    done
    [ -z "$failed" ] || stc_fail "the key of the cluster does not log in to:$failed"
}

function stc_main()
{
    local uuid key from remove tag rotate dir
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    key=$(jq -r '.public_key // ""' <<<"$input")
    from=$(jq -r '(.from // []) | join(",")' <<<"$input")
    remove=$(jq -r '.remove // false' <<<"$input")
    rotate=$(jq -r '.rotate // ""' <<<"$input")
    tag="cloudland-storage-$uuid"
    dir=$run_dir/storage/$uuid
    if [ "$remove" = "true" ]; then
        stc_keys_update suffix "$tag" || stc_fail "rewriting authorized_keys failed"
        stc_keys_update suffix "$tag-rotate" || stc_fail "rewriting authorized_keys failed"
        rm -f $dir/id_ed25519 $dir/id_ed25519.pub $dir/known_hosts
        return 0
    fi
    case "$rotate" in
        add)
            check_line
            stc_keys_update suffix "$tag-rotate" "$(trust_line "$tag-rotate")" || stc_fail "rewriting authorized_keys failed"
            ;;
        switch)
            write_private $dir
            ;;
        "" | drop)
            check_line
            stc_keys_update suffix "$tag" "$(trust_line "$tag")" || stc_fail "rewriting authorized_keys failed"
            stc_keys_update suffix "$tag-rotate" || stc_fail "rewriting authorized_keys failed"
            [ -z "$rotate" ] && write_private $dir
            ;;
        *) stc_fail "invalid rotation pass $rotate" ;;
    esac
    check_logins $dir
    stc_result "$(jq -cn --arg t "$tag" '{line: $t}')"
}

stc_run "$@"
