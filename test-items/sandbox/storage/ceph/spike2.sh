#!/bin/bash
# Second half of spike1.sh on the cluster it bootstrapped: the OSD (after the device inventory is there), the pool, the
# RBD client. Log /root/ceph-spike/spike2.log
set -x
W=/root/ceph-spike
exec >$W/spike2.log 2>&1
host=$(hostname)
mount --make-rshared /
vgchange -ay cl-spike
ls -l /dev/cl-spike/
ceph orch device ls --refresh >/dev/null
for i in $(seq 1 30); do ceph orch device ls $host --format json | jq -e 'length > 0 and (.[0].devices | length) > 0' >/dev/null && break; sleep 5; done
ceph orch apply osd --all-available-devices --unmanaged=true
time ceph orch daemon add osd $host:/dev/cl-spike/osd0 --skip-validation
for i in $(seq 1 60); do ceph osd stat | grep -q "1 up" && break; sleep 5; done
ceph osd metadata -f json | jq -c '.[] | {id, hostname, devices, bluestore_bdev_dev_node, device_ids}'
ceph osd tree -f json | jq -c '.nodes[] | select(.type=="osd") | {id, device_class, status}'
ceph osd tree
ceph config set osd osd_memory_target_autotune false
ceph config set osd osd_memory_target 1073741824
ceph config set mon mon_allow_pool_delete false
ceph osd pool create cl_spike
ceph osd pool set cl_spike size 1 --yes-i-really-mean-it; echo "size1 rc=$?"; ceph config set global mon_allow_pool_size_one true; ceph osd pool set cl_spike size 1 --yes-i-really-mean-it; echo "size1 again rc=$?"
ceph osd pool set cl_spike min_size 1
ceph osd pool application enable cl_spike rbd
rbd pool init cl_spike
echo "uuid-of-the-pool" >$W/marker
rados -p cl_spike put cloudland-pool $W/marker
ceph auth get-or-create client.cloudland mon 'profile rbd' osd 'profile rbd pool=cl_spike' mgr 'profile rbd pool=cl_spike' >$W/client.keyring
ceph config generate-minimal-conf >$W/client.conf
echo "keyring = $W/client.keyring" >>$W/client.conf
o="--conf $W/client.conf --id cloudland"
echo "=== as client.cloudland"
rados $o -p cl_spike get cloudland-pool - ; echo "rc=$?"
rbd $o create cl_spike/volume-1 --size 1024 --image-feature layering,exclusive-lock,object-map,fast-diff,deep-flatten; echo "create rc=$?"
rbd $o info cl_spike/volume-1 --format json; echo "info rc=$?"
rbd $o resize cl_spike/volume-1 --size 2048; echo "resize rc=$?"
rbd $o status cl_spike/volume-1 --format json; echo "status rc=$?"
ceph $o df -f json | head -c 600; echo; echo "df rc=$?"
ceph $o osd pool get-quota cl_spike -f json; echo "get-quota rc=$?"
rados $o df -f json | head -c 400; echo; echo "rados df rc=$?"
ceph $o health -f json; echo "health rc=$?"
ceph $o -s -f json | head -c 300; echo; echo "status rc=$?"
ceph $o osd pool ls detail -f json | head -c 300; echo; echo "pool ls rc=$?"
ceph $o fsid; echo "fsid rc=$?"
rbd $o ls -p cl_spike; echo "ls rc=$?"
rbd $o du cl_spike/volume-1 --format json; echo "du rc=$?"
rados $o -p cl_spike put .probe-1 /etc/hostname; echo "probe put rc=$?"; rados $o -p cl_spike rm .probe-1; echo "probe rm rc=$?"
ceph mon dump -f json | jq -c '[.mons[] | .public_addrs.addrvec]'
rbd $o rm cl_spike/volume-1; echo "rm rc=$?"
echo "=== as admin"
ceph df -f json | jq -c '.pools[] | select(.name=="cl_spike") | .stats'
ceph orch ps
docker ps --format '{{.Names}} {{.Image}}'
free -m
echo DONE
