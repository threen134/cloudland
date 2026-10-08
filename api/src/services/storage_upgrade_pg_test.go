/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The upgrade of a managed cluster (storage_cluster_upgrade.go), against PostgreSQL with the fake cland: the GPFS
// rolling upgrade (the installer to every host, then drain and upgrade one host after the other, the drain waiting for
// the migrations off the host and failing on one that could not move), the release recorded at the end, finalize as a
// task of its own; the Ceph upgrade (install, then cephadm); refusals

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestStorageVersionLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{{"6.0.0.2", "6.0.1.1", true}, {"6.0.1.1", "6.0.0.2", false}, {"19.2.3", "19.2.10", true}, {"20.2.0", "20.2.0", false}, {"", "6.0.0.2", true}, {"6.0", "6.0.0.1", true}} {
		if got := storageVersionLess(c.a, c.b); got != c.less {
			t.Errorf("%s < %s: %v", c.a, c.b, got)
		}
	}
}

func TestStorageUpgradePG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	client, err := minio.New("127.0.0.1:9", &minio.Options{Creds: credentials.NewStaticV4("k", "s", ""), Region: "us-east-1"})
	must(t, err)
	oldClient, oldBucket := s3Client.Load(), s3Bucket
	s3Client.Store(client)
	s3Bucket = "images"
	defer func() { s3Client.Store(oldClient); s3Bucket = oldBucket }()
	manifest := func(v string) string {
		d := strings.ReplaceAll(v, ".", "-")
		d = d[:strings.LastIndex(d, "-")] + "-" + d[strings.LastIndex(d, "-")+1:]
		return fmt.Sprintf(`{"gpfs.base_%[1]s_amd64.deb":"m1","gpfs.gpl_%[1]s_all.deb":"m2","gpfs.gskit_8.0.55-19.1_amd64.deb":"m3","gpfs.msg.en-us_%[1]s_all.deb":"m4","gpfs.license.dm_%[1]s_amd64.deb":"m5"}`, d)
	}
	pkg := func(version, edition, accepted string) *model.StoragePackage {
		p := &model.StoragePackage{Kind: "gpfs", Edition: edition, Version: version, FileName: "installer-" + version, SizeBytes: 1000, SHA256: strings.Repeat("c", 64),
			ObjectKey: "storage-packages/u/installer", PayloadLine: 680, Distros: `["ubuntu24"]`, Status: model.StoragePackageReady, AcceptedBy: accepted,
			Manifest: manifest(version)}
		must(t, db.Create(p).Error)
		t.Cleanup(func() { db.Unscoped().Delete(p) })
		return p
	}
	cur := pkg("6.0.0.2", "data_management", "admin")
	next := pkg("6.0.1.1", "data_management", "admin")
	unaccepted := pkg("6.0.1.2", "data_management", "")
	other := pkg("6.0.2.0", "erasure_code", "admin")
	must(t, db.Model(f.cluster).Updates(map[string]interface{}{"package_id": cur.ID, "version": cur.Version}).Error)
	pool, _ := f.createPool(fmt.Sprintf("up-%d", time.Now().UnixNano()%1000000), 100, func(hostid int32, p string) string { return readyCheck(p, 100*gib, 0) })
	f.cland.take()

	// Refused: nothing named, an image, the release it runs, a license not accepted, another edition
	_, err = StorageClusters.Upgrade(ctx, f.cluster.UUID, nil)
	wantCode(t, err, ErrStorageInvalidPlan, "Name the package")
	_, err = StorageClusters.Upgrade(ctx, f.cluster.UUID, &StorageUpgrade{Image: "x/y:1"})
	wantCode(t, err, ErrStorageInvalidPlan, "no image")
	_, err = StorageClusters.Upgrade(ctx, f.cluster.UUID, &StorageUpgrade{PackageUUID: cur.UUID})
	wantCode(t, err, ErrStorageInvalidPlan, "newer release")
	_, err = StorageClusters.Upgrade(ctx, f.cluster.UUID, &StorageUpgrade{PackageUUID: unaccepted.UUID})
	wantCode(t, err, ErrStoragePackageState, "license")
	_, err = StorageClusters.Upgrade(ctx, f.cluster.UUID, &StorageUpgrade{PackageUUID: other.UUID})
	wantCode(t, err, ErrStorageInvalidPlan, "edition")

	// Instances on the storage of the cluster: one running on h[1], one shut off on h[2] (stays)
	inst := func(name string, host int32, status model.InstanceStatus) *model.Instance {
		i := &model.Instance{Hostname: fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%1000000), Status: status, Hyper: host, Owner: 1, Cpu: 1, Memory: 512, Disk: 10}
		must(t, db.Create(i).Error)
		v := &model.Volume{Name: i.Hostname + "-boot", Owner: 1, Booting: true, InstanceID: i.ID, StoragePoolID: pool.ID, Size: 10, Status: model.VolumeStatusAttached, Target: "vda"}
		must(t, db.Create(v).Error)
		t.Cleanup(func() {
			db.Unscoped().Where("instance_id = ?", i.ID).Delete(&model.Migration{})
			db.Unscoped().Where("instance_id = ?", i.ID).Delete(&model.Volume{})
			db.Unscoped().Delete(i)
		})
		return i
	}
	running := inst("up-run", h[1], model.InstanceStatusRunning)
	inst("up-off", h[2], model.InstanceStatusShutoff)

	task, err := StorageClusters.Upgrade(ctx, f.cluster.UUID, &StorageUpgrade{PackageUUID: next.UUID})
	must(t, err)
	steps, err := storageTaskSteps(db, task.ID)
	must(t, err)
	names := []string{}
	for _, s := range steps {
		names = append(names, fmt.Sprintf("%s%v", s.Name, parseHostids(s.Hostids)))
	}
	want := fmt.Sprintf("fetch_package%v drain[%d] upgrade_node[%d] drain[%d] upgrade_node[%d] drain[%d] upgrade_node[%d]", h, h[0], h[0], h[1], h[1], h[2], h[2])
	if strings.Join(names, " ") != want {
		t.Fatalf("steps: %s", strings.Join(names, " "))
	}
	sent := f.expect("fetch", "stc_fetch.sh", h...)
	if sent[0].input["sha256"] != next.SHA256 || sent[0].input["name"] != next.FileName {
		t.Fatalf("fetch input %v", sent[0].input)
	}
	for _, s := range sent {
		must(t, f.report(s, model.StorageRunSucceeded, "", "{}"))
	}
	// h[0] has nothing to move: its drain ends at once
	sent = f.expect("upgrade h0", "gpfs_upgrade.sh", h[0])
	in := sent[0].input
	if in["version"] != "6.0.1-1" || in["cluster_uuid"] != f.cluster.UUID || fmt.Sprint(in["filesystems"]) != "[fs1]" || in["debs"] == nil {
		t.Fatalf("upgrade input %v", in)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"version":"6.0.1-1"}`))
	// h[1]: the running instance is moved off as maintenance does; the drain waits for the migration
	f.sent()
	migration := func(host int32) *model.Migration {
		m := &model.Migration{}
		must(t, db.Where("instance_id = ? AND source_hyper = ?", running.ID, host).Order("id DESC").Take(m).Error)
		if !strings.HasPrefix(m.Name, fmt.Sprintf("upgrade-%d-hyper-%d", task.ID, host)) {
			t.Fatalf("migration of the drain: %+v", m)
		}
		return m
	}
	m := migration(h[1])
	advanceStorageTask(ctx, task.ID)
	if got := f.sent(); len(got) != 0 || f.task(task.ID).Status != model.StorageTaskRunning {
		t.Fatalf("the drain did not wait for the migration: %v", got)
	}
	var n int64
	db.Model(&model.Migration{}).Where("instance_id = ? AND source_hyper = ?", running.ID, h[1]).Count(&n)
	if n != 1 {
		t.Fatalf("the instance was sent off twice: %d migrations", n)
	}
	must(t, db.Model(&model.Migration{}).Where("id = ?", m.ID).Update("status", "completed").Error)
	must(t, db.Model(&model.Instance{}).Where("id = ?", running.ID).Updates(map[string]interface{}{"status": model.InstanceStatusRunning, "hyper": h[2]}).Error)
	workStorageControls(ctx)
	sent = f.expect("upgrade h1", "gpfs_upgrade.sh", h[1])
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"version":"6.0.1-1"}`))
	// h[2]: the instance (moved there) goes again; the shut off one stays. Its migration fails: so does the drain,
	// with the reason; moved by the admin, the retry goes on
	m = migration(h[2])
	must(t, db.Model(&model.Migration{}).Where("id = ?", m.ID).Update("status", "failed").Error)
	must(t, db.Create(&model.Task{Mission: m.ID, Source: model.TaskSourceMigration, Name: "Prepare_Target", Status: "failed", Message: "not enough memory"}).Error)
	t.Cleanup(func() { db.Unscoped().Where("mission = ?", m.ID).Delete(&model.Task{}) })
	advanceStorageTask(ctx, task.ID)
	f.wantTask(task.ID, model.StorageTaskFailed, "not enough memory")
	must(t, db.Model(&model.Instance{}).Where("id = ?", running.ID).Update("hyper", h[0]).Error)
	must(t, RetryStorageTask(ctx, task.ID))
	sent = f.expect("upgrade h2", "gpfs_upgrade.sh", h[2])
	if c := f.task(task.ID); c.Status != model.StorageTaskRunning {
		t.Fatalf("before the last host: %s", c.Status)
	}
	if c := &(model.StorageCluster{}); db.Take(c, f.cluster.ID).Error == nil && c.Version != cur.Version {
		t.Fatalf("the release changed before the end: %s", c.Version)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"version":"6.0.1-1"}`))
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	c := &model.StorageCluster{}
	must(t, db.Take(c, f.cluster.ID).Error)
	if c.PackageID != next.ID || c.Version != next.Version {
		t.Fatalf("after the upgrade: package %d version %s", c.PackageID, c.Version)
	}
	// Hosts joining later install the new release
	if _, p, err := gpfsClusterPackage(db, &model.StorageTask{ClusterID: c.ID, Kind: StorageTaskAddNodes}); err != nil || p.ID != next.ID {
		t.Fatalf("package for a new host: %v %v", p, err)
	}

	// Finalize: one step on the admin host
	task, err = StorageClusters.Upgrade(ctx, f.cluster.UUID, &StorageUpgrade{Finalize: true})
	must(t, err)
	sent = f.expect("finalize", "gpfs_cluster.sh", h[0])
	if sent[0].input["action"] != "finalize" || fmt.Sprint(sent[0].input["filesystems"]) != "[fs1]" {
		t.Fatalf("finalize input %v", sent[0].input)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"release":"6.0.1.1"}`))
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")

	// Ceph: install everywhere, then cephadm; the release recorded at the end
	ceph := &model.StorageCluster{Name: fmt.Sprintf("upc-pg-%d", time.Now().UnixNano()%1000000), Kind: model.StorageKindCeph,
		Mode: model.StorageModeManaged, Status: model.StorageClusterReady, Health: model.StorageHealthUnknown, Version: "19.2.3",
		Attrs: `{"version":"19.2.3","image":"quay.io/ceph/ceph:v19.2.3"}`}
	must(t, db.Create(ceph).Error)
	t.Cleanup(func() {
		db.Unscoped().Where("cluster_id = ?", ceph.ID).Delete(&model.StorageClusterNode{})
		db.Unscoped().Delete(ceph)
	})
	must(t, db.Model(&model.StorageClusterNode{}).Where("cluster_id = ?", f.cluster.ID).Update("status", model.StorageNodeLeaving).Error)
	for i, hh := range h {
		roles := []string{"admin,mon,mgr,osd", "mon,osd", "client"}[i]
		must(t, db.Create(&model.StorageClusterNode{ClusterID: ceph.ID, Hostid: hh, Roles: roles, Status: model.StorageNodeActive}).Error)
	}
	_, err = StorageClusters.Upgrade(ctx, ceph.UUID, &StorageUpgrade{PackageUUID: next.UUID})
	wantCode(t, err, ErrStorageInvalidPlan, "no package")
	_, err = StorageClusters.Upgrade(ctx, ceph.UUID, &StorageUpgrade{Image: "Bad Image"})
	wantCode(t, err, ErrInvalidParameter, "Invalid image")
	task, err = StorageClusters.Upgrade(ctx, ceph.UUID, nil)
	must(t, err)
	sent = f.expect("install", "ceph_install.sh", h...)
	for _, s := range sent {
		orch := s.hostid != h[2]
		if s.input["image"] != "" || s.input["orch"] != orch {
			t.Fatalf("install input on %d: %v", s.hostid, s.input)
		}
		// The drop-ins are written again with the upgrade: the redeployed daemons stay protected
		if _, has := s.input["oom"]; has != orch {
			t.Fatalf("OOM protection in the install input on %d: %v", s.hostid, s.input)
		}
		image, iv := "", ""
		if orch {
			image, iv = "quay.io/ceph/ceph:v19.2.4", "19.2.4"
		}
		must(t, f.report(s, model.StorageRunSucceeded, "", fmt.Sprintf(`{"version":"19.2.4","image":%q,"image_version":%q}`, image, iv)))
	}
	sent = f.expect("upgrade", "ceph_cluster.sh", h[0])
	if sent[0].input["action"] != "upgrade" || sent[0].input["version"] != "19.2.4" || sent[0].input["image"] != "quay.io/ceph/ceph:v19.2.4" {
		t.Fatalf("upgrade input %v", sent[0].input)
	}
	if c := cephInfoOf(func() *model.StorageCluster { x := &model.StorageCluster{}; db.Take(x, ceph.ID); return x }()); c.Version != "19.2.3" {
		t.Fatalf("the release changed before the daemons did: %s", c.Version)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"version":"19.2.4"}`))
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	must(t, db.Take(ceph, ceph.ID).Error)
	if info := cephInfoOf(ceph); ceph.Version != "19.2.4" || info.Version != "19.2.4" || info.Image != "quay.io/ceph/ceph:v19.2.4" {
		t.Fatalf("after the Ceph upgrade: %s %+v", ceph.Version, info)
	}
	// An older release from the distribution is refused at the install step
	task, err = StorageClusters.Upgrade(ctx, ceph.UUID, nil)
	must(t, err)
	for _, s := range f.expect("install older", "ceph_install.sh", h...) {
		must(t, f.report(s, model.StorageRunSucceeded, "", `{"version":"19.2.2","image":"quay.io/ceph/ceph:v19.2.2","image_version":"19.2.2"}`))
	}
	f.wantTask(task.ID, model.StorageTaskFailed, "older than the 19.2.4")
	must(t, AbortStorageTask(ctx, task.ID))
	// An image of its own older than the hosts: the daemons are upgraded to (and checked against) the release of the image
	task, err = StorageClusters.Upgrade(ctx, ceph.UUID, &StorageUpgrade{Image: "registry.local/ceph:v19.2.4"})
	must(t, err)
	for _, s := range f.expect("install own image", "ceph_install.sh", h...) {
		res := `{"version":"19.2.5","image":""}`
		if s.hostid != h[2] {
			res = `{"version":"19.2.5","image":"registry.local/ceph:v19.2.4","image_version":"19.2.4"}`
		}
		must(t, f.report(s, model.StorageRunSucceeded, "", res))
	}
	sent = f.expect("upgrade own image", "ceph_cluster.sh", h[0])
	if sent[0].input["version"] != "19.2.4" || sent[0].input["image"] != "registry.local/ceph:v19.2.4" {
		t.Fatalf("upgrade input with an image of its own %v", sent[0].input)
	}
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", `{"version":"19.2.4"}`))
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
}
