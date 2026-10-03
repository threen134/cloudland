#!/bin/bash
# Stop the job of a storage task run when its task is aborted (shared-storage-design.md §6.2.5): the job leads its
# own process group, so everything it started goes with it. Reports the run as failed.

cd $(dirname $0)
source ../../cloudrc
source ./stc_lib.sh

run=$1
stc_valid_run "$run" || exit 1
dir=$stc_jobs_dir/$run
[ -d $dir ] || exit 0

if stc_job_alive $dir; then
    pid=$(cat $dir/pid)
    kill -TERM -- -$pid 2>/dev/null
    for i in $(seq 10); do
        stc_job_alive $dir || break
        sleep 0.5
    done
    stc_job_alive $dir && kill -KILL -- -$pid 2>/dev/null
fi
# The job may have ended on its own meanwhile; otherwise record the end it will not write
stc_end_lost $run "stopped because the task was aborted"
cat $dir/callback
