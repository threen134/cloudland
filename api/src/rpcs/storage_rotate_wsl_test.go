/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

// The rotation part of TestStorageCephWSL (storage_cluster_keys.go), against the real cluster: the three passes of
// a new SSH key with the orchestrator switched between them, the client key made pending and taken by every host, a
// QEMU started before the rotation still writing its disk afterwards, and one started after opening its disk

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"api/src/model"
	"api/src/services"
)

func rotateCheck(t *testing.T, hw *wslHarness, c *model.StorageCluster, cephPool string) {
	db, ctx := hw.db, hw.ctx
	rbd := fmt.Sprintf("rbd --conf /etc/ceph/%s.conf --id cloudland", c.UUID)
	ceph := fmt.Sprintf("ceph --conf /var/lib/ceph/%[1]s/config/ceph.conf --keyring /var/lib/ceph/%[1]s/config/ceph.client.admin.keyring", c.UUID)
	// A TCG domain with an RBD disk, started before the rotation
	domain := func(name string) string {
		return fmt.Sprintf(`source /opt/cloudland/scripts/cloudrc; cd /opt/cloudland/scripts/kvm; source ./storage_lib.sh
dom=%[1]s; virsh destroy $dom >/dev/null 2>&1; virsh undefine $dom >/dev/null 2>&1; rm -rf $xml_dir/$dom; mkdir -p $xml_dir/$dom
%[2]s create --size 64M %[3]s/rot-$dom 2>/dev/null
drv_load '{"driver":"ceph_rbd","conf":"/etc/ceph/%[4]s.conf","user":"cloudland","secret_uuid":"%[4]s","ceph_pool":"%[3]s","cluster":"%[4]s"}' || echo "LOAD FAILED $guard_error"
{ echo "<domain type='qemu'><name>$dom</name><memory unit='MiB'>96</memory><vcpu>1</vcpu>"
  echo "<os><type arch='x86_64' machine='q35'>hvm</type></os><devices><emulator>/usr/bin/qemu-system-x86_64</emulator>"
  drv_disk_xml rot-$dom vdb; echo "</devices></domain>"; } >$xml_dir/$dom/$dom.xml
virsh define $xml_dir/$dom/$dom.xml >/dev/null && virsh start $dom >/dev/null && echo "STARTED $dom" || echo "DOMAIN FAILED $dom"
echo "WATCHERS: $(%[2]s status --format json %[3]s/rot-$dom | jq '.watchers | length')"`, name, rbd, cephPool, c.UUID)
	}
	cleanup := func(name string) string {
		return fmt.Sprintf(`source /opt/cloudland/scripts/cloudrc; virsh destroy %[1]s >/dev/null 2>&1; virsh undefine %[1]s >/dev/null 2>&1
rm -rf $xml_dir/%[1]s; sleep 2; %[2]s rm %[3]s/rot-%[1]s >/dev/null 2>&1; true`, name, rbd, cephPool)
	}
	defer wsl(0, cleanup("inst-990101")+"\n"+cleanup("inst-990102"))
	out, _ := wsl(0, domain("inst-990101"))
	if !strings.Contains(out, "STARTED inst-990101") || !strings.Contains(out, "WATCHERS: 1") {
		t.Fatalf("domain before the rotation: %s", out)
	}
	logRuns := func(id int64) {
		for _, r := range hw.taskRuns(t, id) {
			if r.Status != model.StorageRunSucceeded {
				t.Logf("run %d on %d: %s %s\n%s", r.ID, r.Hostid, r.Status, r.Message, r.LogTail)
			}
		}
	}
	task, err := services.StorageClusters.RotateKeys(ctx, c.UUID, nil)
	must(t, err)
	if done := hw.waitTask(t, task.ID, 10*time.Minute, model.StorageTaskSucceeded, model.StorageTaskFailed); done.Status != model.StorageTaskSucceeded {
		logRuns(task.ID)
		t.Fatalf("rotation: %s", done.Message)
	}
	cl := &model.StorageCluster{}
	must(t, db.Take(cl, c.ID).Error)
	out, _ = wsl(0, fmt.Sprintf(`%[1]s auth get client.cloudland -f json | jq -r '(.[0].key == ($k | ltrimstr("    key = "))), (.[0].pending_key // "none")' --arg k "$(grep 'key = ' /etc/ceph/%[2]s.client.cloudland.keyring)"
grep -c 'cloudland-storage-%[2]s$' /root/.ssh/authorized_keys; grep -c 'cloudland-storage-%[2]s-rotate$' /root/.ssh/authorized_keys
grep 'cloudland-storage-%[2]s$' /root/.ssh/authorized_keys | awk '{print $2, $3}'
%[1]s cephadm get-pub-key`, ceph, c.UUID))
	lines := strings.Split(strings.TrimSpace(out), "\n")
	pub := strings.TrimSpace(cl.SSHPubKey)
	if len(lines) != 6 || lines[0] != "true" || lines[1] != "none" || lines[2] != "1" || lines[3] != "0" || lines[4] != pub || strings.TrimSpace(lines[5]) != pub {
		t.Fatalf("after the rotation (client key, pending, trust lines, orchestrator key): %q", lines)
	}
	// The QEMU started before the rotation keeps its session with the old key: it still changes its image (an online
	// resize goes through its own librbd connection), which the new key reads back
	out, _ = wsl(0, fmt.Sprintf(`virsh blockresize inst-990101 vdb 128M >/dev/null && echo RESIZED
echo "SIZE: $(%[1]s info --format json %[2]s/rot-inst-990101 | jq .size)"`, rbd, cephPool))
	if !strings.Contains(out, "RESIZED") || !strings.Contains(out, "SIZE: 134217728") {
		t.Fatalf("the QEMU from before the rotation: %s", out)
	}
	// A QEMU started now opens its disk with the new key
	out, _ = wsl(0, domain("inst-990102"))
	if !strings.Contains(out, "STARTED inst-990102") || !strings.Contains(out, "WATCHERS: 1") {
		t.Fatalf("domain after the rotation: %s", out)
	}
}

// upgradeCheck: an upgrade to the release the distribution has, here the one the cluster runs: the install step on
// the host, the upgrade step finding every daemon on it, the release recorded
func upgradeCheck(t *testing.T, hw *wslHarness, c *model.StorageCluster) {
	task, err := services.StorageClusters.Upgrade(hw.ctx, c.UUID, nil)
	must(t, err)
	done := hw.waitTask(t, task.ID, 30*time.Minute, model.StorageTaskSucceeded, model.StorageTaskFailed)
	runs := hw.taskRuns(t, task.ID)
	for _, r := range runs {
		t.Logf("upgrade run %d: %s %s %s", r.ID, r.Status, r.Message, r.Result)
	}
	if done.Status != model.StorageTaskSucceeded || len(runs) != 2 || !strings.Contains(runs[1].LogTail, "every daemon runs Ceph") {
		t.Fatalf("upgrade: %s %q", done.Status, done.Message)
	}
	cl := &model.StorageCluster{}
	must(t, hw.db.Take(cl, c.ID).Error)
	if cl.Version == "" || !strings.Contains(cl.Attrs, `"version":"`+cl.Version+`"`) {
		t.Fatalf("release after the upgrade: %q %s", cl.Version, cl.Attrs)
	}
}
