#!/bin/bash
# sandbox tests of the storage job scripts (shared-storage-design.md §6.2.3, §6.2.4)
S=/opt/cloudland/scripts/backend/storage
A=/opt/cloudland/run/async_job
J=/opt/cloudland/run/storage/jobs
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
heartbeat() { for f in $(ls $A/*.done 2>/dev/null); do cat $f; rm -f $f; done; }
decode() { awk -F"'" '{print $8}' | base64 -d 2>/dev/null; }
rm -rf $J /opt/cloudland/log/storage; rm -f $A/*; mkdir -p $A
base=$((RANDOM * 10))

# 1 happy path: nothing on stdout, result by the heartbeat and by a poll
r=$((base+1))
out=$(echo '{"sleep_sec":2,"fail":false,"lock":"t1","step":1}' | $S/stc_selftest.sh $r)
[ -z "$out" ] && ok "1 start prints nothing" || ko "1 start printed: $out"
sleep 1
p=$($S/stc_poll.sh $r)
grep -q "storage_task_run '$r' 'running'" <<<"$p" && ok "1 poll while running" || ko "1 poll while running: $p"
sleep 3
hb=$(heartbeat)
grep -q "storage_task_run '$r' 'succeeded' '100'" <<<"$hb" && ok "1 heartbeat carries the result" || ko "1 heartbeat: $hb"
[ $(grep -c . <<<"$hb") -eq 1 ] && ok "1 heartbeat carries only the callback" || ko "1 heartbeat has other lines: $hb"
res=$(grep "'$r'" <<<"$hb" | decode | jq -c .result)
jq -e '.host and .boot_id' <<<"$res" >/dev/null && ok "1 result json" || ko "1 result: $res"
p=$($S/stc_poll.sh $r)
[ "$p" = "$(grep "'$r'" <<<"$hb")" ] && ok "1 poll after the end repeats the result" || ko "1 poll after end: $p"

# 2 a run sent twice runs once
r=$((base+2))
echo '{"sleep_sec":3,"lock":"t2"}' | $S/stc_selftest.sh $r
echo '{"sleep_sec":3,"lock":"t2"}' | $S/stc_selftest.sh $r
sleep 1
echo '{"sleep_sec":3,"lock":"t2"}' | $S/stc_selftest.sh $r
sleep 4
hb=$(heartbeat)
[ $(grep -c "storage_task_run '$r'" <<<"$hb") -eq 1 ] && ok "2 duplicate dispatch runs once" || ko "2 duplicate: $(grep -c "'$r'" <<<"$hb") results"
[ $(grep -c "=== run $r" /opt/cloudland/log/storage/run-$r.log) -eq 1 ] && ok "2 one job in the log" || ko "2 log: $(grep -c "=== run" /opt/cloudland/log/storage/run-$r.log)"
out=$(echo '{}' | $S/stc_selftest.sh $r)
grep -q "'$r' 'succeeded'" <<<"$out" && ok "2 a dispatch after the end sends the result again" || ko "2 resend: $out"

# 3 failure
r=$((base+3))
echo '{"sleep_sec":0,"fail":true,"lock":"t3"}' | $S/stc_selftest.sh $r
sleep 2
hb=$(heartbeat)
msg=$(grep "'$r'" <<<"$hb" | decode | jq -r .message)
grep -q "'$r' 'failed'" <<<"$hb" && [ "$msg" = "failed on purpose (selftest)" ] && ok "3 failure and its message" || ko "3 failure: $hb / $msg"

# 4 kill
r=$((base+4))
echo '{"sleep_sec":60,"lock":"t4"}' | $S/stc_selftest.sh $r
sleep 2
pid=$(cat $J/$r/pid)
k=$($S/stc_kill.sh $r)
grep -q "'$r' 'failed'" <<<"$k" && ok "4 kill reports failed" || ko "4 kill: $k"
sleep 1
kill -0 $pid 2>/dev/null && ko "4 job still alive" || ok "4 job and its children gone"
pgrep -f "sleep 1" >/dev/null; msg=$(echo "$k" | decode | jq -r .message)
[ "$msg" = "stopped because the task was aborted" ] && ok "4 kill message" || ko "4 kill message: $msg"

# 5 interrupted: the job process vanishes without an end
r=$((base+5))
echo '{"sleep_sec":60,"lock":"t5"}' | $S/stc_selftest.sh $r
sleep 2
kill -9 -- -$(cat $J/$r/pid)
sleep 1
p=$($S/stc_poll.sh $r)
grep -q "'$r' 'failed'" <<<"$p" && ok "5 poll reports an interrupted job" || ko "5 interrupted: $p"
heartbeat >/dev/null

# 6 another boot: same pid file, different boot id
r=$((base+6))
echo '{"sleep_sec":30,"lock":"t6"}' | $S/stc_selftest.sh $r
sleep 2
pid=$(cat $J/$r/pid)
echo 00000000-0000-0000-0000-000000000000 >$J/$r/boot_id
p=$($S/stc_poll.sh $r)
grep -q "'$r' 'failed'" <<<"$p" && ok "6 a job of another boot counts as interrupted" || ko "6 boot id: $p"
kill -9 -- -$pid 2>/dev/null; heartbeat >/dev/null

# 7 missing
p=$($S/stc_poll.sh $((base+7)))
[ "$p" = "|:-COMMAND-:| storage_task_run '$((base+7))' 'missing' '0' ''" ] && ok "7 missing" || ko "7 missing: $p"

# 8 one lock: runs of the same cluster wait for each other
r=$((base+8)); r2=$((base+9))
echo '{"sleep_sec":3,"lock":"same"}' | $S/stc_selftest.sh $r
echo '{"sleep_sec":3,"lock":"same"}' | $S/stc_selftest.sh $r2
sleep 8
hb=$(heartbeat)
t1=$(grep "=== exit" /opt/cloudland/log/storage/run-$r.log | awk '{print $5}')
s2=$(grep "got the lock" /opt/cloudland/log/storage/run-$r2.log | head -1)
[ $(grep -c "'succeeded'" <<<"$hb") -eq 2 ] && ok "8 both ran" || ko "8 both ran: $hb"
first_end=$(stat -c %Y $J/$r/exit); second_start=$(stat -c %Y $J/$r2/progress 2>/dev/null)
grep -h "progress: 33%" /opt/cloudland/log/storage/run-$r.log /opt/cloudland/log/storage/run-$r2.log >/dev/null
end1=$(date -d "$(grep '=== exit' /opt/cloudland/log/storage/run-$r.log | sed 's/.* at //')" +%s)
lock2=$(stat -c %Y $J/$r2/pid)
start2=$(grep -n "got the lock" /opt/cloudland/log/storage/run-$r2.log | cut -d: -f1)
# the second got the lock only after the first ended
e1=$(grep '=== exit' /opt/cloudland/log/storage/run-$r.log | sed 's/.* at //'); g2=$(sed -n 's/^\(.*\)$/\1/p' /dev/null)
awk '/got the lock/{print FILENAME}' /opt/cloudland/log/storage/run-$r2.log >/dev/null
[ "$(date -d "$e1" +%s)" -le "$(stat -c %Y $J/$r2/progress)" ] && ok "8 the second waited for the first" || ko "8 overlap"

echo "== $pass passed, $fail failed"
