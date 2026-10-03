#!/bin/bash
# Check a host of an imported GPFS cluster (shared-storage-design.md §7.8): GPFS runs here and the file system is
# mounted where the import says, on GPFS. Nothing is changed; no GPFS admin command is run (the host may well not be
# an admin node of that cluster). Input: {"cluster_uuid", "fs_name", "mount_point"}
# Result: {"capacity_bytes", "free_bytes"}

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh
STC_SCRIPT=$(readlink -f $0)

function stc_main()
{
    local input uuid fs mount type df
    input=$(cat)
    uuid=$(jq -r .cluster_uuid <<<"$input")
    fs=$(jq -r .fs_name <<<"$input")
    mount=$(jq -r .mount_point <<<"$input")
    valid_uuid "$uuid" || stc_fail "invalid cluster uuid"
    [[ "$fs" =~ ^[A-Za-z][A-Za-z0-9_]*$ ]] || stc_fail "invalid file system name"
    [[ "$mount" =~ ^/[A-Za-z0-9_./-]+$ ]] || stc_fail "invalid mount point"
    pgrep -x mmfsd >/dev/null || stc_fail "GPFS (mmfsd) is not running on this host"
    type=$(timeout 10 stat -f -c %T "$mount" 2>/dev/null)
    [ "$type" = "gpfs" ] || stc_fail "$mount is not a GPFS file system here (${type:-not reachable})"
    timeout 10 findmnt -n "$mount" >/dev/null || stc_fail "nothing is mounted at $mount"
    # The device of the mount names the file system
    findmnt -n -o SOURCE "$mount" | grep -qx "$fs" || echo "warning: $mount is mounted from $(findmnt -n -o SOURCE "$mount"), not $fs"
    df=$(timeout 10 df -B1 --output=size,avail "$mount" | tail -1)
    stc_result "$(awk '{printf "{\"capacity_bytes\":%s,\"free_bytes\":%s}", $1, $2}' <<<"$df")"
}

stc_run "$@"
