/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

// The evacuation part of TestStorageCephWSL (shared-storage-design.md §11.2-§11.3), against the real cluster: the
// fence through the blocklist, launch_vm.sh defining the instance from its record with its RBD disks, the fence
// lifted once the host is back

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"api/src/model"
	"api/src/services"
)

func evacuateCheck(t *testing.T, hw *wslHarness, host int32, c *model.StorageCluster, p *model.StoragePool, cephPool string) {
	db, ctx := hw.db, hw.ctx
	down := int32(9322)
	downIP := "192.0.2.77"
	stamp := time.Now().Unix() % 100000
	db.Unscoped().Where("hostid = ?", down).Delete(&model.StorageFence{})
	db.Unscoped().Where("hostid = ?", down).Delete(&model.Hyper{})
	offlineAt := time.Now().Add(-10 * time.Minute)
	must(t, db.Create(&model.Hyper{Hostid: down, Hostname: "wsl-down", Status: model.HyperStatusOffline, HostIP: downIP, OfflineAt: &offlineAt,
		RouteIP: "198.51.100.77"}).Error)
	image := &model.Image{Name: fmt.Sprintf("ev-wsl-%d", stamp), Format: "qcow2", Status: "available", BootLoader: "bios"}
	must(t, db.Create(image).Error)
	inst := &model.Instance{Hostname: fmt.Sprintf("ev-wsl-%d", stamp), Status: model.InstanceStatusShutoff, Hyper: down, Owner: 1, ImageID: image.ID,
		Cpu: 1, Memory: 128, Disk: 1}
	must(t, db.Create(inst).Error)
	boot := &model.Volume{Name: inst.Hostname + "-boot", Owner: 1, Booting: true, InstanceID: inst.ID, StoragePoolID: p.ID, Size: 1,
		Status: model.VolumeStatusAttached, Target: "vda"}
	must(t, db.Create(boot).Error)
	data := &model.Volume{Name: inst.Hostname + "-data", Owner: 1, InstanceID: inst.ID, StoragePoolID: p.ID, Size: 1,
		Status: model.VolumeStatusAttached, Target: "vdc"}
	must(t, db.Create(data).Error)
	for _, v := range []*model.Volume{boot, data} {
		must(t, db.Model(&model.Volume{}).Where("id = ?", v.ID).Update("path", fmt.Sprintf("volume-%d", v.ID)).Error)
	}
	rbd := fmt.Sprintf("rbd --conf /etc/ceph/%s.conf --id cloudland", c.UUID)
	ceph := fmt.Sprintf("ceph --conf /var/lib/ceph/%[1]s/config/ceph.conf --keyring /var/lib/ceph/%[1]s/config/ceph.client.admin.keyring", c.UUID)
	dom := fmt.Sprintf("inst-%d", inst.ID)
	defer func() {
		_, _ = wsl(0, fmt.Sprintf(`virsh destroy %[1]s >/dev/null 2>&1; virsh undefine %[1]s >/dev/null 2>&1; rm -rf /opt/cloudland/cache/xml/%[1]s
sed -i "s/<domain type='qemu'>/<domain type='kvm'>/" /opt/cloudland/scripts/xml/template_with_qa.xml /opt/cloudland/scripts/xml/template_uefi_with_qa.xml
%[2]s rm -p %[3]s volume-%[4]d >/dev/null 2>&1; %[2]s rm -p %[3]s volume-%[5]d >/dev/null 2>&1
%[6]s osd blocklist range rm %[7]s/32 >/dev/null 2>&1; true`, dom, rbd, cephPool, boot.ID, data.ID, ceph, downIP))
		db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.Migration{})
		db.Unscoped().Where("instance_id = ?", inst.ID).Delete(&model.Volume{})
		db.Unscoped().Delete(inst)
		db.Unscoped().Delete(image)
		db.Unscoped().Where("hostid = ?", down).Delete(&model.StorageFence{})
		db.Unscoped().Where("hostid = ?", down).Delete(&model.Hyper{})
	}()
	out, err := wsl(0, fmt.Sprintf(`set -e
sed -i "s/<domain type='kvm'>/<domain type='qemu'>/" /opt/cloudland/scripts/xml/template_with_qa.xml /opt/cloudland/scripts/xml/template_uefi_with_qa.xml
%[1]s create --size 64M %[2]s/volume-%[3]d; %[1]s create --size 64M %[2]s/volume-%[4]d; echo ok`, rbd, cephPool, boot.ID, data.ID))
	if err != nil || !strings.HasSuffix(strings.TrimSpace(out), "ok") {
		t.Fatalf("evacuation disks: %v %s", err, out)
	}

	results, err := (&services.HyperAdmin{}).Evacuate(ctx, down, &services.EvacuateRequest{TargetHyper: -1})
	must(t, err)
	if len(results) != 1 || results[0].Status != "fencing" {
		t.Fatalf("evacuation: %+v", results)
	}
	m := &model.Migration{}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		must(t, db.Where("instance_id = ? AND type = ?", inst.ID, model.MigrationTypeEvacuate).Order("id DESC").Take(m).Error)
		if m.Status == "completed" || m.Status == "failed" || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	fence := &model.StorageFence{}
	must(t, db.Where("hostid = ? AND cluster_id = ?", down, c.ID).Take(fence).Error)
	if m.Status != "completed" || fence.Status != model.StorageFenceFenced || fence.Method != model.StorageFenceBlocklist || fence.Target != downIP {
		t.Fatalf("evacuation %+v fence %+v", m, fence)
	}
	got := &model.Instance{}
	must(t, db.Take(got, inst.ID).Error)
	if got.Hyper != host || got.Status != model.InstanceStatusShutoff {
		t.Fatalf("evacuated instance: on %d, %s (%s)", got.Hyper, got.Status, got.Reason)
	}
	out, _ = wsl(0, fmt.Sprintf(`echo "STATE: $(virsh domstate %[1]s)"
virsh domblklist %[1]s --details | awk '$1 == "network" {print "DISK:", $3, $4}'
%[2]s osd blocklist ls 2>/dev/null | awk '{print "BLOCKLIST:", $1}'`, dom, ceph))
	t.Logf("evacuated:\n%s", out)
	for _, w := range []string{"STATE: shut off", fmt.Sprintf("DISK: vda %s/volume-%d", cephPool, boot.ID), fmt.Sprintf("DISK: vdc %s/volume-%d", cephPool, data.ID),
		fmt.Sprintf("BLOCKLIST: cidr:%s:0/32", downIP)} {
		if !strings.Contains(out, w) {
			t.Fatalf("evacuation check: want %q", w)
		}
	}
	// One launch: the evacuation is not sent again while it goes on
	if n := hw.fake.commands("launch_vm.sh"); n != 1 {
		t.Fatalf("%d launches sent", n)
	}

	// The host comes back, removed its copy and reconciled: the fence is lifted on the cluster
	must(t, db.Model(&model.Migration{}).Where("id = ?", m.ID).Update("source_cleaned", true).Error)
	must(t, db.Model(&model.Hyper{}).Where("hostid = ?", down).Updates(map[string]interface{}{"status": 1, "offline_at": nil}).Error)
	if _, err = NodeReconciled(context.WithValue(context.Background(), "hostid", down), []string{"node_reconciled", fmt.Sprint(down), "back"}); err != nil {
		t.Fatal(err)
	}
	// As if it came back a while ago: a fence command is undone only after the host settled
	must(t, db.Model(&model.StorageFence{}).Where("hostid = ?", down).Update("created_at", time.Now().Add(-20*time.Minute)).Error)
	must(t, db.Model(&model.Hyper{}).Where("hostid = ?", down).Update("reconciled_at", time.Now().Add(-10*time.Minute)).Error)
	deadline = time.Now().Add(3 * time.Minute)
	for db.Where("hostid = ?", down).Take(&model.StorageFence{}).Error == nil && time.Now().Before(deadline) {
		time.Sleep(time.Second)
	}
	if db.Where("hostid = ?", down).Take(fence).Error == nil {
		t.Fatalf("still fenced: %+v", fence)
	}
	out, _ = wsl(0, fmt.Sprintf(`%s osd blocklist ls 2>/dev/null | grep -c "cidr:%s:"`, ceph, downIP))
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("the blocklist entry is left: %s", out)
	}
}
