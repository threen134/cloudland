#!/bin/bash
# sandbox: memory.low of a cgroup only protects as far as every cgroup above it is protected (cgroup v2), the reason
# the Ceph containers need system.slice protected too (shared-storage-design.md §6.7.2). Contained: the pressure is a
# limit of a test slice, nothing outside it is reclaimed.
#   cltest.slice    MemoryMax=700M   the root of the reclaim (stands for the whole host)
#   cltest-t.slice  MemoryLow=<parent low>   stands for system.slice
#   cltest-a        MemoryLow=400M, 300 MiB of page cache   stands for a Ceph container
#   cltest-b        650 MiB of anonymous memory   stands for the VMs
# wsl.exe -u root --exec bash <repo under /mnt/c>/test-items/sandbox/storage/stc-test10-memlow.sh
f=/root/cltest-memlow.dat
[ "$(stat -c %s $f 2>/dev/null)" = 314572800 ] || dd if=/dev/urandom of=$f bs=1M count=300 status=none
cg() { echo /sys/fs/cgroup/cltest.slice/cltest-t.slice/cltest-a.service; }
mib() { echo $(( $1 / 1048576 )); }
run() {
    local plow=$1
    systemctl stop cltest-a cltest-b >/dev/null 2>&1; systemctl reset-failed cltest-a cltest-b >/dev/null 2>&1
    sync; echo 1 >/proc/sys/vm/drop_caches
    systemd-run -q --unit=cltest-a --slice=cltest-t.slice -p MemoryLow=400M bash -c "cat $f >/dev/null; exec sleep 600"
    systemctl set-property --runtime cltest.slice MemoryMax=700M
    systemctl set-property --runtime cltest-t.slice MemoryLow=$plow
    sleep 5
    local before after file_b file_a
    before=$(awk '$1 == "file" {print $2}' $(cg)/memory.stat)
    systemd-run -q --unit=cltest-b --slice=cltest.slice python3 -c "
import time
b = []
for i in range(65):
    b.append(b'x' * (10 << 20)); time.sleep(0.05)
time.sleep(600)"
    sleep 12
    after=$(awk '$1 == "file" {print $2}' $(cg)/memory.stat)
    echo "parent memory.low $plow: container low $(mib $(cat $(cg)/memory.low)) MiB, page cache $(mib $before) -> $(mib $after) MiB; pressure process $(systemctl is-active cltest-b)"
    systemctl stop cltest-a cltest-b >/dev/null 2>&1; systemctl reset-failed cltest-a cltest-b >/dev/null 2>&1
}
run 0
run 400M
rm -f $f
