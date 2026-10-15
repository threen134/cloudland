#!/bin/bash
# Does a client that authenticated with the old key keep working once a pending key is rotated into place?
# Tickets shortened to 60 s (mon) and 30 s (service) for the experiment, put back at the end
f=$(ls -d /var/lib/ceph/*-*-*-*-*/ | head -1 | xargs basename)
c="ceph --conf /var/lib/ceph/$f/config/ceph.conf --keyring /var/lib/ceph/$f/config/ceph.client.admin.keyring"
pool=$($c osd pool ls | grep ^cl_ | head -1)
t=/root/key-ttl; rm -rf $t; mkdir -p $t
$c config set global auth_mon_ticket_ttl 60
$c config set global auth_service_ticket_ttl 30
$c auth get-or-create client.cl-ttl mon "profile rbd" osd "profile rbd pool=$pool" -f json | jq -r '.[0].key' >$t/old.key
printf "[client.cl-ttl]\n key = %s\n" "$(cat $t/old.key)" >$t/old.keyring
cat >$t/writer.py <<PY
import rados, time, sys
c = rados.Rados(conffile="/var/lib/ceph/$f/config/ceph.conf", rados_id="cl-ttl", conf={"keyring": "$t/old.keyring", "rados_mon_op_timeout": "10", "rados_osd_op_timeout": "10"})
c.connect(timeout=10)
io = c.open_ioctx("$pool")
start = time.time()
while time.time() - start < 240:
    try:
        io.write_full("cl-ttl-obj", str(time.time()).encode())
        r = "ok"
    except Exception as e:
        r = "FAIL " + type(e).__name__
    print("%5.0f %s" % (time.time() - start, r), flush=True)
    time.sleep(5)
PY
setsid python3 $t/writer.py >$t/writer.log 2>&1 &
sleep 20
j=$($c auth get-or-create-pending client.cl-ttl -f json)
printf "[client.cl-ttl]\n key = %s\n" "$(jq -r '.[0].pending_key' <<<"$j")" >$t/new.keyring
timeout 20 ceph --conf /var/lib/ceph/$f/config/ceph.conf --id cl-ttl --keyring $t/new.keyring fsid >/dev/null 2>&1
sleep 5
echo "rotated at ~25 s: $($c auth get client.cl-ttl -f json | jq -c '.[0] | {pending: (.pending_key != null)}')"
echo "restarting the mon at ~30 s"; systemctl restart ceph-$f@mon.$(hostname -s).service; sleep 20; echo "mon back: $($c health 2>&1 | head -1 | cut -c1-40)"
wait
echo "--- writer"; cat $t/writer.log | awk '{print $2}' | uniq -c; grep -m1 FAIL $t/writer.log
$c config rm global auth_mon_ticket_ttl; $c config rm global auth_service_ticket_ttl
$c auth rm client.cl-ttl; $c config get mon auth_mon_ticket_ttl
