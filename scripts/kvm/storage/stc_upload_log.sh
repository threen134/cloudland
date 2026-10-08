#!/bin/bash
# Upload the whole log of a storage task run to clapi (shared-storage-design.md §6.2.6): <run ID> <upload URL> <token>.
# The callback of a run only carries the last 64 KiB of its log; this posts the log file (its last 16 MiB) to the
# address clapi gave, with a token for that run only. In the background, so a slow upload does not hold the command
# queue; only a failure is reported:
#
#   |:-COMMAND-:| storage_run_log '<run ID>' 'error' '<message>'

cd $(dirname $0)
source ../../cloudrc
source ../storage_lib.sh
source ./stc_lib.sh

run=$1
url=$2
token=$3
stc_valid_run "$run" || exit 1
[[ "$url" =~ ^https?://[A-Za-z0-9._:-]+/[A-Za-z0-9._/?=\&-]*$ ]] || exit 1
[[ "$token" =~ ^[0-9a-f]{64}$ ]] || exit 1

function upload_job()
{
    local log=$stc_log_dir/run-$run.log size code
    if [ ! -f $log ]; then
        echo "|:-COMMAND-:| storage_run_log '$run' 'error' 'the host has no log of this run'"
        return
    fi
    size=$(stat -c %s $log)
    # The certificate of clapi is its own; the token is what proves the upload
    code=$(tail -c 16777216 $log | curl -ksS -m 300 -o /dev/null -w '%{http_code}' -X POST \
        -H "X-Log-Token: $token" -H 'Content-Type: application/octet-stream' --data-binary @- "$url&size=$size" 2>/dev/null)
    if [ "$code" != 200 ]; then
        echo "|:-COMMAND-:| storage_run_log '$run' 'error' 'the upload to clapi failed (HTTP ${code:-none})'"
    fi
}

async_exec upload_job
exit 0
