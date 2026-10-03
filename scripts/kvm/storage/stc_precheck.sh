#!/bin/bash
# Check a host before it joins a storage cluster (shared-storage-design.md §6.5). Nothing is installed or changed.
# Input (services/storage_cluster.go storagePrecheckInput): kind, cluster_uuid, roles, disks, peers, ports, support,
# allow_unsupported, need_headers, need_container, data_dirs, reserve_mb.
# Result: {"items": [{"name", "status": "ok|warn|fail", "detail"}], "facts": {...}}. The step fails when an item fails.

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

items=""

function item()
{
    items="$items$(jq -cn --arg n "$1" --arg s "$2" --arg d "$3" '{name: $n, status: $s, detail: $d}')"$'\n'
    echo "$2: $1: $3"
}

function check_os()
{
    local id version kernel rules match="" rule
    id=$(. /etc/os-release && echo $ID)
    version=$(. /etc/os-release && echo $VERSION_ID)
    kernel=$(uname -r)
    rules=$(jq -c '.support[]?' <<<"$input")
    while read -r rule; do
        [ -z "$rule" ] && continue
        [ "$(jq -r .id <<<"$rule")" = "$id" ] || continue
        [ "$(jq -r .version <<<"$rule")" = "$version" ] || continue
        local prefix suffix
        prefix=$(jq -r '.kernel_prefix // ""' <<<"$rule")
        suffix=$(jq -r '.kernel_suffix // ""' <<<"$rule")
        if [[ "$kernel" == "$prefix"* ]] && [[ "$kernel" == *"$suffix" ]]; then
            match=yes
        else
            match="kernel $kernel is not in the supported series ${prefix}*${suffix}"
        fi
        break
    done <<<"$rules"
    if [ "$match" = "yes" ]; then
        item os ok "$id $version, kernel $kernel"
    else
        local detail="$id $version with kernel $kernel is not supported for $kind${match:+: $match}"
        if [ "$(jq -r '.allow_unsupported' <<<"$input")" = "true" ]; then
            item os warn "$detail (allowed for testing)"
        else
            item os fail "$detail"
        fi
    fi
}

function check_headers()
{
    local pkg=linux-headers-$(uname -r)
    if dpkg -s $pkg >/dev/null 2>&1; then
        item kernel_headers ok "$pkg is installed"
    elif apt-cache policy $pkg 2>/dev/null | grep -q 'Candidate: [0-9]'; then
        item kernel_headers ok "$pkg can be installed"
    else
        item kernel_headers fail "$pkg is not available: the kernel module can not be built"
    fi
    if ! command -v mokutil >/dev/null; then
        item secure_boot ok "mokutil is not installed; assumed off"
    elif mokutil --sb-state 2>/dev/null | grep -qi 'SecureBoot enabled'; then
        item secure_boot fail "Secure Boot is on: a self-built kernel module can not be loaded"
    else
        item secure_boot ok "off"
    fi
}

function check_time()
{
    if [ "$(timedatectl show -p NTPSynchronized --value 2>/dev/null)" = "yes" ]; then
        item time_sync ok "synchronized"
    else
        item time_sync fail "the clock is not synchronized (timedatectl)"
    fi
}

# A refused connection proves the network is open; only a timeout means a firewall drops the traffic
function check_peers()
{
    local peer port rc out failed=""
    for peer in $(jq -r '.peers[]?' <<<"$input"); do
        for port in $(jq -r '.ports[]?' <<<"$input"); do
            out=$(timeout 3 bash -c "exec 3<>/dev/tcp/$peer/$port" 2>&1)
            rc=$?
            if [ $rc -ne 0 ] && ! grep -qi refused <<<"$out"; then
                failed="$failed $peer:$port"
            fi
        done
    done
    if [ -n "$failed" ]; then
        item network fail "no answer from$failed"
    else
        item network ok "$(jq -r '.peers | length' <<<"$input") peers reachable"
    fi
}

function check_disks()
{
    local disk id serial wwn size wipe n=0
    system_disk_list=$(system_disks)
    local_pool_list=$(local_pools)
    while read -r disk; do
        [ -z "$disk" ] && continue
        n=$((n + 1))
        id=$(jq -r .id <<<"$disk")
        serial=$(jq -r '.serial // ""' <<<"$disk")
        wwn=$(jq -r '.wwn // ""' <<<"$disk")
        size=$(jq -r '.size_bytes // 0' <<<"$disk")
        wipe=$(jq -r '.wipe // false' <<<"$disk")
        if ! disk_identity "$id" "$serial" "$wwn" "$size"; then
            item "disk $id" fail "$identity_error"
            continue
        fi
        classify_disk $identity_path
        case $disk_state in
            free) item "disk $id" ok "$identity_path, free" ;;
            dirty)
                if [ "$wipe" = "true" ]; then
                    item "disk $id" warn "$identity_path has data on it and will be wiped"
                else
                    item "disk $id" fail "$identity_path has data on it ($disk_detail)"
                fi
                ;;
            *) item "disk $id" fail "$identity_path can not be used: $disk_state ($disk_detail)" ;;
        esac
    done < <(jq -c '.disks[]?' <<<"$input")
    [ $n -eq 0 ] && item disks ok "no disk from this host"
}

function check_data_dirs()
{
    local dir path min_gib min_pct p avail size
    while read -r dir; do
        [ -z "$dir" ] && continue
        path=$(jq -r .path <<<"$dir")
        min_gib=$(jq -r '.min_free_gib // 0' <<<"$dir")
        min_pct=$(jq -r '.min_free_percent // 0' <<<"$dir")
        p=$path
        while [ ! -e "$p" ] && [ "$p" != "/" ]; do p=$(dirname "$p"); done
        read -r size avail < <(df -B1 --output=size,avail "$p" | tail -1)
        if [ "$min_gib" -gt 0 ] && [ $((avail / 1073741824)) -lt "$min_gib" ]; then
            item "space $path" fail "$((avail / 1073741824)) GiB free, $min_gib GiB needed"
        elif [ "$min_pct" -gt 0 ] && [ $((avail * 100 / size)) -lt "$min_pct" ]; then
            item "space $path" fail "$((avail * 100 / size))% free, $min_pct% needed"
        else
            item "space $path" ok "$((avail / 1073741824)) GiB free"
        fi
    done < <(jq -c '.data_dirs[]?' <<<"$input")
}

# The memory instances may use here, as the heartbeat reports it, against what the daemons will hold back
function check_memory()
{
    local reserve avail_kib
    reserve=$(jq -r '.reserve_mb // 0' <<<"$input")
    avail_kib=$(awk '{gsub("\x27", ""); print $3}' $run_dir/old_resource_list 2>/dev/null)
    if [ -z "$avail_kib" ]; then
        item memory warn "the heartbeat has not reported the free memory yet; $reserve MiB will be held back"
    elif [ $((avail_kib / 1024 - reserve)) -lt 0 ]; then
        item memory warn "$((avail_kib / 1024)) MiB free for instances, $reserve MiB will be held back for the daemons"
    else
        item memory ok "$((avail_kib / 1024)) MiB free for instances, $reserve MiB will be held back"
    fi
}

function check_dpkg_lock()
{
    if ! command -v fuser >/dev/null; then
        item package_manager ok "fuser is not installed; not checked"
        return
    fi
    local waited=0
    while fuser /var/lib/dpkg/lock-frontend /var/lib/dpkg/lock >/dev/null 2>&1; do
        [ $waited -ge 120 ] && break
        sleep 5
        waited=$((waited + 5))
    done
    if fuser /var/lib/dpkg/lock-frontend /var/lib/dpkg/lock >/dev/null 2>&1; then
        item package_manager fail "another program has held the dpkg lock for 2 minutes"
    else
        item package_manager ok "free"
    fi
}

function check_container()
{
    if docker info --format '{{.ServerVersion}}' >/dev/null 2>&1; then
        item container ok "docker $(docker info --format '{{.ServerVersion}}' 2>/dev/null)"
    elif podman info >/dev/null 2>&1; then
        item container ok "podman"
    else
        item container fail "neither docker nor podman is running"
    fi
}

function check_hostname()
{
    local name
    name=$(hostname)
    if getent hosts "$name" >/dev/null; then
        item hostname ok "$name"
    else
        item hostname fail "$name does not resolve"
    fi
}

# Software of the kind that is not ours: CloudLand marks the hosts of its clusters before installing anything
function check_existing()
{
    local uuid found=""
    uuid=$(jq -r '.cluster_uuid // ""' <<<"$input")
    if [ -n "$uuid" ] && [ -f $run_dir/storage/$uuid/member ]; then
        item existing ok "this host is being set up for the cluster already"
        return
    fi
    if ! backend_load "$kind"; then
        item existing fail "no node side backend for kind $kind"
        return
    fi
    found=$(backend_existing)
    if [ -n "$found" ]; then
        item existing fail "$found that CloudLand did not set up; clean it up first, or import the cluster"
    else
        item existing ok "none"
    fi
}

function stc_main()
{
    input=$(cat)
    kind=$(jq -r .kind <<<"$input")
    check_os
    [ "$(jq -r .need_headers <<<"$input")" = "true" ] && check_headers
    check_time
    check_peers
    check_disks
    check_data_dirs
    check_memory
    check_dpkg_lock
    [ "$(jq -r .need_container <<<"$input")" = "true" ] && check_container
    check_hostname
    check_existing
    local facts host_key
    host_key=$(cat /etc/ssh/ssh_host_ed25519_key.pub 2>/dev/null | awk '{print $1" "$2}')
    facts=$(jq -cn --arg h "$(hostname)" --arg k "$(uname -r)" --arg o "$(. /etc/os-release && echo "$ID $VERSION_ID")" \
        --arg key "$host_key" --arg ips "$(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | xargs)" \
        '{hostname: $h, kernel: $k, os: $o, host_key: $key, addresses: ($ips | split(" "))}')
    stc_result "$(printf '%s' "$items" | jq -cs --argjson f "$facts" '{items: ., facts: $f}')"
    local failed
    failed=$(printf '%s' "$items" | jq -rs '[.[] | select(.status == "fail") | .name] | join(", ")')
    [ -n "$failed" ] && stc_fail "not passed: $failed"
    return 0
}

stc_run "$@"
