#!/bin/bash
# A step that only sleeps, reports progress and fails on demand, to check the storage task path to a host
# (shared-storage-design.md §16 S1). Input: {"sleep_sec": N, "fail": bool, "lock": name, "step": N, "attempt": N}

cd $(dirname $0)
source ../../cloudrc
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input sleep_sec fail lock i
    input=$(cat)
    sleep_sec=$(jq -r '.sleep_sec // 0' <<<"$input")
    fail=$(jq -r '.fail // false' <<<"$input")
    lock=$(jq -r '.lock // "selftest"' <<<"$input")
    [[ "$sleep_sec" =~ ^[0-9]+$ ]] || stc_fail "invalid sleep_sec"
    stc_lock "selftest-$lock"
    for i in $(seq 1 $sleep_sec); do
        sleep 1
        stc_progress $((i * 100 / sleep_sec)) "slept $i of $sleep_sec seconds"
    done
    [ "$fail" = "true" ] && stc_fail "failed on purpose (selftest)"
    stc_result "$(jq -cn --arg h "$(hostname)" --arg b "$(stc_boot_id)" --argjson s "$(jq '.step // 0' <<<"$input")" \
        '{host: $h, boot_id: $b, step: $s}')"
}

stc_run "$@"
