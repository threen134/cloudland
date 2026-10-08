#!/bin/bash
# Tell clapi how a storage task run is doing (shared-storage-design.md §6.2.4): ended (the result again), running
# (progress and log tail), interrupted, or missing (the command never reached this host). Runs in the foreground
# and only reads the job directory, so it is quick even while the job is busy.

cd $(dirname $0)
source ../../cloudrc
source ./stc_lib.sh

run=$1
stc_valid_run "$run" || exit 1
dir=$stc_jobs_dir/$run

if [ ! -d $dir ]; then
    echo "|:-COMMAND-:| storage_task_run '$run' 'missing' '0' ''"
    exit 0
fi
if [ -f $dir/callback ]; then
    cat $dir/callback
    exit 0
fi
if stc_job_alive $dir || stc_job_starting $dir; then
    message=$(cut -d' ' -f2- $dir/progress 2>/dev/null)
    stc_callback_line $run running "$(stc_progress_pct $dir)" "$message"
    exit 0
fi
stc_end_lost $run "the job was interrupted (the host restarted or the process was killed)"
cat $dir/callback
