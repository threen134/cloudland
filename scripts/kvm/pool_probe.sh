#!/bin/bash
# Probe one storage pool in the background and write the result to a state file (§4.7 of the plan).
# The heartbeat (report_rc.sh) only starts this script and reads the state file: a broken disk can hang
# df or mount in D state, which neither timeout nor kill can end, and the heartbeat must never wait on it.
# The probe of the built-in pool also resumes instances paused because a local pool ran out of space (§6.1).

cd $(dirname $0)
source ../cloudrc
source ./storage_lib.sh
exec </dev/null >/dev/null 2>&1

pool=$1
[ -z "$pool" ] && exit 1
[ "$pool" = "builtin" ] || valid_uuid "$pool" || exit 1
mkdir -p $pool_state_dir
state_file=$pool_state_dir/$pool.state
now=$(date +%s)

function write_state()
{
    local status=$1 reason=$2 extra=$3
    [ -z "$extra" ] && extra='{}'
    jq -cn --arg status "$status" --arg reason "$reason" --argjson extra "$extra" --arg ts "$now" \
        '{status: $status, reason: $reason, ts: ($ts | tonumber)} + $extra' >$state_file.tmp && mv -f $state_file.tmp $state_file
    write_metrics $status "$extra"
}

# Pool status and use for the alarm rules, through the textfile collector of node_exporter (§4.7):
# cloudland_pool_status 0 ready, 1 degraded, 2 unavailable, 3 maintenance; cloudland_pool_usage_ratio 0-1
function write_metrics()
{
    local status=$1 extra=$2 dir code ratio
    dir=$(ps -eo args= 2>/dev/null | grep -o -- '--collector.textfile.directory[= ][^ ]*' | head -1 | sed 's/^--collector.textfile.directory[= ]//')
    [ -z "$dir" ] && dir=/var/lib/prometheus/node-exporter
    [ -d "$dir" ] || return
    case $status in
        ready) code=0 ;;
        degraded) code=1 ;;
        maintenance) code=3 ;;
        *) code=2 ;;
    esac
    ratio=$(jq -r 'if ((.used // 0) + (.avail // 0)) > 0 then (.used / (.used + .avail)) else 0 end' <<<"$extra" 2>/dev/null)
    printf 'cloudland_pool_status{pool="%s"} %s\ncloudland_pool_usage_ratio{pool="%s"} %s\n' "$pool" $code "$pool" "${ratio:-0}" \
        >$dir/cloudland_pool_$pool.prom.$$ && mv -f $dir/cloudland_pool_$pool.prom.$$ $dir/cloudland_pool_$pool.prom
}

# Resume instances paused because every disk that failed ran out of space in a local pool, once those pools
# have room again: at least min(15% of the pool, 50 GiB) available
function resume_nospace()
{
    local dom errs dev src ok p st avail total need last
    for dom in $(virsh list --state-paused --name 2>/dev/null); do
        virsh domstate --reason $dom 2>/dev/null | grep -qi "i/o error" || continue
        errs=$(timeout 10 virsh domblkerror $dom 2>/dev/null)
        [ -z "$errs" ] || grep -qi "no errors" <<<"$errs" && continue
        ok=1
        while read dev err; do
            dev=${dev%:}
            [ -z "$dev" ] && continue
            if ! grep -qi "no space" <<<"$err"; then
                ok=0
                break
            fi
            src=$(virsh domblklist $dom --details 2>/dev/null | awk -v d=$dev '$3 == d {print $4}')
            case "$src" in
                $pools_dir/*) p=${src#$pools_dir/}; p=${p%%/*} ;;
                $cache_dir/*) p=builtin ;;
                *) ok=0; break ;;
            esac
            st=$pool_state_dir/$p.state
            [ -f $st ] || { ok=0; break; }
            avail=$(jq -r '.avail // 0' $st)
            total=$(jq -r '(.used // 0) + (.avail // 0)' $st)
            need=$((total * 15 / 100))
            [ $need -gt 53687091200 ] && need=53687091200
            [ "$avail" -ge "$need" ] || { ok=0; break; }
        done <<<"$errs"
        [ $ok -eq 1 ] || continue
        # At most one attempt per instance every 5 minutes; never during a job such as a migration
        last=$(cat $pool_state_dir/resume-$dom 2>/dev/null)
        [ -n "$last" ] && [ $((now - last)) -lt 300 ] && continue
        timeout 10 virsh domjobinfo $dom 2>/dev/null | grep -q "Job type: *None" || continue
        echo $now >$pool_state_dir/resume-$dom
        timeout 20 virsh resume $dom
    done
}

if [ "$pool" = "builtin" ]; then
    df=$(pool_df_json $cache_dir)
    if [ -z "$df" ]; then
        write_state unavailable "df of $cache_dir failed" '{"layout": "builtin"}'
    else
        own=$(timeout 300 du -sxB1 $image_dir $volume_dir 2>/dev/null | awk '{s += $1} END {print s + 0}')
        write_state ready "" "$(jq -cn --argjson df "{$df}" --arg own "${own:-0}" --arg ratio "${disk_over_ratio:-1}" \
            '{layout: "builtin", size: $df.size, used: $df.used, avail: $df.avail, own: ($own | tonumber),
              over_ratio: ($ratio | tonumber? // 1), sync: 100}')"
        # Placeholders of migrations that never finished (§7.3): they hold room for a day, like the reservation of
        # clapi; a source host that died leaves them behind for good otherwise
        find $cache_tmp_dir -maxdepth 1 -name '.incoming-*' -mmin +1440 -delete 2>/dev/null
    fi
    resume_nospace
    exit 0
fi

root=$pools_dir/$pool
if [ -f $pool_state_dir/$pool.maint ]; then
    extra='{}'
    timeout 10 mountpoint -q $root && extra="{$(pool_df_json $root)}"
    write_state maintenance "in maintenance" "$extra"
    exit 0
fi

if ! timeout 10 mountpoint -q $root; then
    # Retry the mount with a back-off (1, 2, 5, 10 minutes), and never while a structural change holds the pool
    mount_file=$pool_state_dir/$pool.mount
    read attempts next_try <<<"$(cat $mount_file 2>/dev/null)"
    attempts=${attempts:-0}
    next_try=${next_try:-0}
    if [ $now -ge $next_try ]; then
        exec 8>$pool_lock_dir/cloudland-pool-$pool.lock
        if flock -n -s 8; then
            mkdir -p $root
            timeout 120 mount $root && attempts=0
            flock -u 8
        fi
        if ! timeout 10 mountpoint -q $root; then
            attempts=$((attempts + 1))
            case $attempts in
                1) delay=60 ;;
                2) delay=120 ;;
                3) delay=300 ;;
                *) delay=600 ;;
            esac
            echo "$attempts $((now + delay))" >$mount_file
        else
            rm -f $mount_file
        fi
    fi
    if ! timeout 10 mountpoint -q $root; then
        write_state unavailable "not mounted" '{}'
        exit 0
    fi
fi

if ! pool_guard $root $pool $NODE_ID; then
    write_state unavailable "$guard_error" '{}'
    exit 0
fi
vg=$(pool_vg $pool)
status=ready
reason=""
sync=100
layout=single
if [ -n "$vg" ]; then
    layout=$(vg_layout $vg)
    if [ "$layout" = "raid1" ]; then
        vg_raid_health $vg
        status=$raid_state
        sync=$raid_sync
        [ "$status" = "degraded" ] && reason="RAID1 array degraded or syncing"
        [ "$status" = "unavailable" ] && reason="RAID1 array failed"
    fi
fi
df=$(pool_df_json $root)
if [ -z "$df" ]; then
    write_state unavailable "df of $root failed" '{}'
    exit 0
fi
files=$(timeout 30 ls $root/volumes 2>/dev/null | head -5000)
# Placeholders of migrations that never finished: kept for a day, like the reservation of clapi
timeout 30 find $root/tmp -maxdepth 1 -name '.incoming-*' -mmin +1440 -delete 2>/dev/null
write_state $status "$reason" "$(jq -cn --argjson df "{$df}" --arg layout "$layout" --arg vg "$vg" --arg sync "$sync" --arg files "$files" \
    '{layout: $layout, vg: $vg, size: $df.size, used: $df.used, avail: $df.avail, sync: ($sync | tonumber),
      files: ($files | split("\n") | map(select(. != "")))}')"
