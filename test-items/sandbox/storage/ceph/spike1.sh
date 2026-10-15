#!/bin/bash
# Ceph spike in the WSL sandbox (shared-storage-design.md S3): a single host cluster with cephadm, an OSD on an LVM
# volume over a loop file, a pool, an RBD client with profile rbd. Answers V10 (packaged cephadm and its image), V14
# (what profile rbd may run) and how ceph-volume takes a loop based LV. Run as root in WSL; log /root/ceph-spike/spike1.log
set -x
W=/root/ceph-spike
mkdir -p $W
exec >$W/spike1.log 2>&1
export DEBIAN_FRONTEND=noninteractive
apt-get install -y -o DPkg::Lock::Timeout=300 openssh-server cephadm ceph-common lvm2 jq >/dev/null
systemctl enable --now ssh
# uutils install (26.04) rejects a numeric owner without a passwd entry: cephadm installs dirs owned by 167
getent passwd 167 >/dev/null || { groupadd -r -g 167 ceph-ctr; useradd -r -u 167 -g 167 -M -d /var/lib/ceph -s /usr/sbin/nologin ceph-ctr; }
cephadm version
cephadm --help 2>&1 | grep -i -A2 "image" | head -8
ip=$(ip -4 -br addr show eth0 | awk '{print $3}' | cut -d/ -f1)
host=$(hostname)
# an LV over a loop file: ceph-volume does not take loop devices themselves
[ -f $W/osd.img ] || truncate -s 30G $W/osd.img
dev=$(losetup -j $W/osd.img -nO NAME | head -1)
[ -n "$dev" ] || dev=$(losetup -f --show $W/osd.img)
vgs cl-spike >/dev/null 2>&1 || { pvcreate -ff -y $dev && vgcreate cl-spike $dev && lvcreate -y -l 100%FREE -n osd0 cl-spike; }
lvs
fsid=$(cat /proc/sys/kernel/random/uuid)
echo $fsid >$W/fsid
time cephadm --image quay.io/ceph/ceph:v20.2.0 bootstrap --fsid $fsid --mon-ip $ip --single-host-defaults --skip-monitoring-stack --skip-dashboard --skip-firewalld \
    --ssh-user root --allow-fqdn-hostname || { echo BOOTSTRAP-FAILED; exit 1; }
ceph -s
cat /etc/ceph/ceph.conf
ceph orch apply osd --all-available-devices --unmanaged=true
time ceph orch daemon add osd $host:/dev/cl-spike/osd0
for i in $(seq 1 60); do ceph osd stat | grep -q "1 up" && break; sleep 5; done
ceph osd tree
ceph config set osd osd_memory_target_autotune false
ceph config set osd osd_memory_target 1073741824
ceph config set mon mon_allow_pool_delete false
ceph osd pool create cl_spike
ceph osd pool set cl_spike size 1 --yes-i-really-mean-it
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
rbd $o rm cl_spike/volume-1; echo "rm rc=$?"
echo "=== as admin"
ceph df -f json | jq -c '.pools[] | select(.name=="cl_spike") | .stats'
ceph orch ps
docker ps --format '{{.Names}} {{.Image}}'
free -m
echo DONE
