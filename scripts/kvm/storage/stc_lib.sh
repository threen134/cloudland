# -*- mode: sh -*-
# Shared helpers of the storage cluster task scripts (shared-storage-design.md §6.2.3). Source it after ../../cloudrc.
#
# A step script is started with its run ID and its input as JSON on stdin. Its synchronous part (stc_start) only
# records the job and starts it in the background with async_exec, so a long step never holds the serial command
# queue of cloudlet. The job keeps a directory per run on disk:
#
#   accepted   when the command reached the host       pid, boot_id   the running job
#   input      the input of the step                   progress       "<percent> <message>"
#   message    why it failed (stc_fail)                result.json    output for later steps
#   exit       exit code, once it ended                callback       the final callback line
#
# so stc_poll.sh can always tell whether the job runs, ended (and send the result again), was interrupted, or never
# arrived. Starting is idempotent: a run sent twice runs once.
#
# Nothing may reach stdout except callback lines: every stdout line of a node script is sent to clapi, and the
# output of a background job (stderr included) is sent by the next heartbeat. The job therefore writes everything
# to its log and only the callback to the descriptor stc_start keeps for it.

stc_jobs_dir=$run_dir/storage/jobs
stc_log_dir=$log_dir/storage
stc_lock_dir=/var/lock
stc_cb_fd=6

# backend_load sources the node side of the backend of a kind (backends/<kind>.sh), which defines the hooks of that
# kind (shared-storage-design.md §4.5). Fails for a kind with no such file.
function backend_load()
{
    local kind=$1 file
    [[ "$kind" =~ ^[a-z][a-z0-9_]*$ ]] || return 1
    file=$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/backends/$kind.sh
    [ -f "$file" ] || return 1
    source "$file"
}

function stc_valid_run()
{
    [[ "$1" =~ ^[0-9]+$ ]]
}

function stc_boot_id()
{
    cat /proc/sys/kernel/random/boot_id 2>/dev/null
}

# Report of a run as base64 JSON: <run ID> <message> [result json]
function stc_payload()
{
    local run=$1 message=$2 result=${3:-null} tail=""
    local log=$stc_log_dir/run-$run.log
    [ -f $log ] && tail=$(tail -c 65536 $log)
    jq -cn --arg m "$message" --arg l "$tail" --argjson r "$result" '{message: $m, log: $l, result: $r}' 2>/dev/null | base64 -w0
}

# Callback line of a run: <run ID> <status> <progress> <message> [result json]
function stc_callback_line()
{
    echo "|:-COMMAND-:| storage_task_run '$1' '$2' '$3' '$(stc_payload "$1" "$4" "$5")'"
}

# Whether the job of a run is still going on this boot of the host: <job dir>
function stc_job_alive()
{
    local dir=$1 pid
    pid=$(cat $dir/pid 2>/dev/null)
    [ -n "$pid" ] || return 1
    [ "$(cat $dir/boot_id 2>/dev/null)" = "$(stc_boot_id)" ] || return 1
    # The pid file survives a reboot and pids are reused: the process must be this job
    [ -r /proc/$pid/cmdline ] || return 1
    tr '\0' ' ' 2>/dev/null </proc/$pid/cmdline | grep -qE -- "--stc-job $(basename $dir)( |$)"
}

# Whether the job of a run was accepted a moment ago and has not written its pid yet: <job dir>
function stc_job_starting()
{
    local dir=$1 accepted
    [ -f $dir/pid ] && return 1
    accepted=$(cat $dir/accepted 2>/dev/null)
    [ -n "$accepted" ] && [ "$(cat $dir/boot_id 2>/dev/null)" = "$(stc_boot_id)" ] && [ $(($(date +%s) - accepted)) -lt 60 ]
}

# The synchronous part of every step script: <run ID>; the script itself is run again as the job
function stc_start()
{
    local run=$1 dir
    if ! stc_valid_run "$run"; then
        cat >/dev/null
        exit 1
    fi
    dir=$stc_jobs_dir/$run
    mkdir -p $dir $stc_log_dir
    # The input of a step may hold the key of the cluster or a client key, and so may its result and callback: only
    # root reads the jobs. The files themselves keep the usual mode, as the job also writes files others must read
    chmod 700 $stc_jobs_dir $stc_log_dir
    if [ -f $dir/callback ]; then
        cat >/dev/null
        cat $dir/callback
        exit 0
    fi
    if stc_job_alive $dir || stc_job_starting $dir; then
        cat >/dev/null
        exit 0
    fi
    if [ -f $dir/accepted ]; then
        # Started on this run before and gone without an end: the host restarted or the job was killed
        cat >/dev/null
        stc_end_lost $run "the job was interrupted (the host restarted or the process was killed)"
        cat $dir/callback
        exit 0
    fi
    [ -f $dir/input ] && cat >/dev/null || cat >$dir/input
    date +%s >$dir/accepted
    stc_boot_id >$dir/boot_id
    # Old jobs and logs
    find $stc_jobs_dir -mindepth 1 -maxdepth 1 -type d -mtime +7 -exec rm -rf {} + 2>/dev/null
    find $stc_log_dir -type f -mtime +90 -delete 2>/dev/null
    # setsid: the job leads its own process group, so stc_kill.sh can stop everything it started
    async_exec setsid --wait bash "$STC_SCRIPT" --stc-job $run
    exit 0
}

# Record the end of a run that will not end by itself, keeping a callback for stc_poll.sh: <run ID> <message>
function stc_end_lost()
{
    local run=$1 message=$2 dir=$stc_jobs_dir/$1
    [ -f $dir/callback ] && return 0
    echo 137 >$dir/exit
    stc_callback_line $run failed "$(stc_progress_pct $dir)" "$message" >$dir/callback
}

function stc_progress_pct()
{
    local pct
    pct=$(awk '{print $1; exit}' $1/progress 2>/dev/null)
    [[ "$pct" =~ ^[0-9]+$ ]] && echo $pct || echo 0
}

# The background part: called by every step script after it defined stc_main
function stc_job()
{
    local run=$1 dir rc message status result
    dir=$stc_jobs_dir/$run
    STC_RUN=$run
    STC_DIR=$dir
    # Killed by stc_kill.sh: end with an exit code instead of dying of the signal, so the shell of async_exec does
    # not print "Terminated" into the output the heartbeat sends to clapi. stc_kill.sh records the end
    trap 'exit 143' TERM
    eval "exec $stc_cb_fd>&1"
    exec >>$stc_log_dir/run-$run.log 2>&1
    echo $BASHPID >$dir/pid
    echo "=== run $run of $(basename $STC_SCRIPT) on $(hostname) at $(date -u +%FT%TZ)"
    ( stc_main <$dir/input )
    rc=$?
    echo "=== exit $rc at $(date -u +%FT%TZ)"
    if [ $rc -eq 0 ]; then
        status=succeeded
        message=$(cat $dir/message 2>/dev/null)
        echo "100 done" >$dir/progress
    else
        status=failed
        message=$(cat $dir/message 2>/dev/null)
        [ -z "$message" ] && message="exit code $rc"
    fi
    result=$(cat $dir/result.json 2>/dev/null)
    jq -e . >/dev/null 2>&1 <<<"$result" || result=null
    stc_callback_line $run $status "$(stc_progress_pct $dir)" "$message" "$result" >$dir/callback.tmp
    echo $rc >$dir/exit
    mv -f $dir/callback.tmp $dir/callback
    eval "cat $dir/callback >&$stc_cb_fd"
}

# Helpers for stc_main
function stc_progress()
{
    echo "$1 $2" >$STC_DIR/progress.tmp && mv -f $STC_DIR/progress.tmp $STC_DIR/progress
    echo "progress: $1% $2"
}

function stc_result()
{
    echo "$1" >$STC_DIR/result.json
}

function stc_fail()
{
    echo "$1" >$STC_DIR/message
    echo "failed: $1"
    exit 1
}

# Wait for the lock of a cluster on this host: <lock name>. Jobs of one cluster never run side by side on a host,
# even when a retry starts while the job that timed out is still going. The wait counts in the step timeout
function stc_lock()
{
    local name=$1
    [[ "$name" =~ ^[a-z0-9-]+$ ]] || stc_fail "invalid lock name $name"
    exec 5>$stc_lock_dir/cloudland-storage-$name.lock
    echo "waiting for the lock $name"
    flock 5
    echo "got the lock $name"
}

# stc_keys_update <suffix|line> <value> [line to add]: rewrite root's authorized_keys without the lines that end with
# " <value>" (suffix) or equal <value> (line), then with the given line added. Jobs of different clusters run side by
# side on a host and each rewrites the whole file, so the rewrite holds a lock of the host, and the new file replaces
# the old in one rename: a failure never leaves it truncated (it also holds the key cland uses between the hosts)
function stc_keys_update()
{
    local mode=$1 value=$2 add=$3 keys=/root/.ssh/authorized_keys
    mkdir -p /root/.ssh && chmod 700 /root/.ssh
    (
        local tmp
        flock -w 120 4 || { echo "the lock of authorized_keys was not free in 2 minutes"; exit 1; }
        touch $keys
        tmp=$(mktemp $keys.XXXXXX) || exit 1
        if awk -v mode="$mode" -v v="$value" '
                mode == "suffix" && length($0) > length(v) && substr($0, length($0) - length(v)) == " " v { next }
                mode == "line" && $0 == v { next }
                { print }' $keys >$tmp &&
            { [ -z "$add" ] || echo "$add" >>$tmp; } && chmod 600 $tmp && mv -f $tmp $keys; then
            exit 0
        fi
        rm -f $tmp
        exit 1
    ) 4>$stc_lock_dir/cloudland-authorized-keys.lock
}

# Dispatch: run as the job when called with --stc-job, as the synchronous part otherwise. <script args...>
function stc_run()
{
    if [ "$1" = "--stc-job" ]; then
        stc_job $2
    else
        stc_start "$@"
    fi
}
