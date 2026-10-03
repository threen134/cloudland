#!/bin/bash
# Give this host the pool list of its storage cluster, and check a new pool at once (shared-storage-design.md §7.4,
# §9.2). The heartbeat keeps probing the pools of the list in the background afterwards (shared_pool_probe.sh).
# Input: {"cluster_uuid", "pools": [{driver, pool, root, fs_type}], "check": "<pool uuid>" or ""}
# Result: {"pools": <count>, "check": {"pool", "status", "reason", "size", "used", "avail"}}

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input uuid list check n entry st
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    list=$(jq -c '.pools // []' <<<"$input")
    check=$(jq -r '.check // ""' <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    shared_pools_apply "$uuid" "$list" || stc_fail "$guard_error"
    n=$(jq length <<<"$list")
    echo "pool list of cluster $uuid: $n pools"
    if [ -z "$check" ]; then
        stc_result "$(jq -cn --argjson n "$n" '{pools: $n}')"
        return 0
    fi
    entry=$(jq -c --arg p "$check" '.[] | select(.pool == $p)' <<<"$list" | head -1)
    [ -n "$entry" ] || stc_fail "pool $check is not in the list"
    stc_progress 50 "checking pool $check"
    # A pool this host can not reach is reported, not failed: the host may be the one with a problem, and the pool
    # works on the others
    if drv_load "$entry" && drv_probe; then
        st=$(jq -cn --arg p "$check" --argjson d "$probe_json" '{pool: $p, status: "ready", reason: ""} + $d')
    else
        echo "pool $check is not usable here: $guard_error"
        st=$(jq -cn --arg p "$check" --arg r "$guard_error" '{pool: $p, status: "unavailable", reason: $r}')
    fi
    echo "check: $st"
    stc_result "$(jq -cn --argjson n "$n" --argjson c "$st" '{pools: $n, check: $c}')"
}

stc_run "$@"
