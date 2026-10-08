/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The orphan report of a shared pool against PostgreSQL with the fake cland (shared-storage-design.md §16 S5): one host
// that reaches the pool is asked, what it found is compared with the records (live volumes, NVRAM of their instances,
// base copies expected; deleted volumes, unknown files reported by kind; recent and temporary files and the marker
// left out), only the host asked is heard, a host error and a host that never answers are shown.

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"api/src/model"
)

func TestStoragePoolReconcilePG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	pool, _ := f.createPool(fmt.Sprintf("rc-%d", time.Now().UnixNano()%1000000), 0, func(hostid int32, p string) string { return readyCheck(p, 100*gib, 0) })
	// Only h[1] reaches the pool
	must(t, db.Model(&model.HyperStoragePool{}).Where("pool_id = ? AND hostid <> ?", pool.ID, h[1]).Update("status", model.HyperPoolUnavailable).Error)
	live := &model.Volume{Name: "rc-live", Size: 1, Status: model.VolumeStatusAttached, InstanceID: 424242, StoragePoolID: pool.ID, Owner: 1}
	must(t, db.Create(live).Error)
	live.Path = fmt.Sprintf("volumes/volume-%d.disk", live.ID)
	must(t, db.Model(live).Update("path", live.Path).Error)
	gone := &model.Volume{Name: "rc-gone", Size: 1, Status: model.VolumeStatusAvailable, StoragePoolID: pool.ID, Owner: 1}
	must(t, db.Create(gone).Error)
	gone.Path = fmt.Sprintf("volumes/volume-%d.disk", gone.ID)
	must(t, db.Model(gone).Update("path", gone.Path).Error)
	must(t, db.Delete(gone).Error)
	cp := &model.ImageStorage{ImageID: 1, StoragePoolID: pool.ID, Path: "images/image-1-ab.qcow2", Status: model.ImageStorageSynced}
	must(t, db.Create(cp).Error)
	defer func() {
		db.Unscoped().Delete(live)
		db.Unscoped().Delete(gone)
		db.Unscoped().Delete(cp)
		db.Where("pool_id = ?", pool.ID).Delete(&model.StoragePoolReconcile{})
	}()
	f.cland.take()

	r, err := storagePoolAdmin.ReconcilePool(ctx, pool.UUID)
	must(t, err)
	sent := ""
	for _, c := range f.cland.take() {
		if strings.Contains(c, "stc_pool_objects.sh") {
			sent = c
		}
	}
	if r.Status != StorageReconcileRunning || r.Hostid != h[1] ||
		sent != fmt.Sprintf("inter=%d | /opt/cloudland/scripts/backend/storage/stc_pool_objects.sh '%s' '%s'", h[1], pool.UUID, f.cluster.UUID) {
		t.Fatalf("request: %+v %q", r, sent)
	}
	// Asked again while it runs: nothing more is sent
	if _, err := storagePoolAdmin.ReconcilePool(ctx, pool.UUID); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.cland.take() {
		if strings.Contains(c, "stc_pool_objects.sh") {
			t.Fatal("sent twice")
		}
	}

	now := time.Now().Unix()
	old := now - 3*86400
	objects := &StoragePoolObjects{Objects: []*StoragePoolObject{
		{Name: live.Path, Size: 1 << 30, Mtime: old},
		{Name: "nvram/inst-424242_VARS.fd", Size: 540672, Mtime: old},
		{Name: cp.Path, Size: 1 << 28, Mtime: old},
		{Name: gone.Path, Size: 1 << 30, Mtime: old},
		{Name: "volumes/volume-99999999.disk", Size: 2 << 30, Mtime: old},
		{Name: "images/image-7-cd.qcow2", Size: 3 << 20, Mtime: old},
		{Name: "nvram/inst-77_VARS.fd", Size: 540672, Mtime: old},
		{Name: "tmp/volume-5.reinstall.disk", Size: 1 << 20, Mtime: old},
		{Name: "tmp/import-1", Size: 1 << 20, Mtime: now - 3600},
		{Name: "volumes/volume-88888888.disk", Size: 1 << 20, Mtime: now - 60},
		{Name: ".cloudland-pool", Size: 64, Mtime: old},
		{Name: "tmp/.probe-2-123", Size: 0, Mtime: old},
		{Name: "notes.txt", Size: 10, Mtime: old},
	}}
	// Not from a host that was not asked
	if err := HandleStoragePoolObjects(ctx, h[0], pool.UUID, objects); err == nil {
		t.Fatal("objects taken from a host not asked")
	}
	// Through the callback decoding, as the host sends it
	raw, _ := json.Marshal(objects)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	zw.Close()
	decoded, err := DecodeStoragePoolObjects(base64.StdEncoding.EncodeToString(buf.Bytes()))
	must(t, err)
	must(t, HandleStoragePoolObjects(ctx, h[1], pool.UUID, decoded))
	r, err = storagePoolAdmin.PoolReconcile(ctx, pool.UUID)
	must(t, err)
	got := []string{}
	for _, o := range ParseStorageOrphans(r.Orphans) {
		got = append(got, o.Kind+":"+o.Name)
	}
	want := []string{"image:images/image-7-cd.qcow2", "nvram:nvram/inst-77_VARS.fd", "other:notes.txt",
		"temporary:tmp/volume-5.reinstall.disk", "deleted_volume:" + gone.Path, "volume:volumes/volume-99999999.disk"}
	if r.Status != StorageReconcileDone || r.Objects != len(objects.Objects) || r.CheckedAt == nil || strings.Join(got, ",") != strings.Join(sortedKinds(want), ",") {
		t.Fatalf("report %s %d:\n got %v\nwant %v", r.Status, r.Objects, got, sortedKinds(want))
	}

	// An RBD pool lists names without times: nothing is left out for its age
	rbd := []*StorageOrphan{}
	for _, o := range mustOrphans(t, f, pool, []*StoragePoolObject{{Name: "volume-12345678", Size: 1}, {Name: "volume-12345678-reinstall", Size: 1}}) {
		rbd = append(rbd, o)
	}
	if len(rbd) != 2 || rbd[0].Kind != "volume" || rbd[1].Kind != "temporary" {
		t.Fatalf("rbd orphans %+v", rbd)
	}

	// The host fails to list; a host that never answers turns the report into an error
	db.Model(&model.StoragePoolReconcile{}).Where("pool_id = ?", pool.ID).Update("requested_at", time.Now().Add(-11*time.Minute))
	_, err = storagePoolAdmin.ReconcilePool(ctx, pool.UUID)
	must(t, err)
	must(t, HandleStoragePoolObjects(ctx, h[1], pool.UUID, &StoragePoolObjects{Error: "listing /gpfs/fs1/x failed"}))
	if r, _ = storagePoolAdmin.PoolReconcile(ctx, pool.UUID); r.Status != StorageReconcileError || !strings.Contains(r.Error, "listing") {
		t.Fatalf("host error: %+v", r)
	}
	db.Model(&model.StoragePoolReconcile{}).Where("pool_id = ?", pool.ID).Updates(map[string]interface{}{"status": StorageReconcileRunning,
		"requested_at": time.Now().Add(-11 * time.Minute)})
	if r, _ = storagePoolAdmin.PoolReconcile(ctx, pool.UUID); r.Status != StorageReconcileError || !strings.Contains(r.Error, "did not answer") {
		t.Fatalf("no answer: %+v", r)
	}
	// No host reaches the pool: refused
	must(t, db.Model(&model.HyperStoragePool{}).Where("pool_id = ?", pool.ID).Update("status", model.HyperPoolUnavailable).Error)
	if _, err := storagePoolAdmin.ReconcilePool(ctx, pool.UUID); err == nil || !strings.Contains(err.Error(), "No online host") {
		t.Fatalf("no host: %v", err)
	}
}

func mustOrphans(t *testing.T, f *sharedFixture, pool *model.StoragePool, objects []*StoragePoolObject) []*StorageOrphan {
	t.Helper()
	list, err := storagePoolOrphans(f.db, pool, objects, time.Now())
	must(t, err)
	return list
}

// sortedKinds sorts "kind:name" entries by name, as the report lists them
func sortedKinds(in []string) []string {
	out := append([]string{}, in...)
	name := func(s string) string { return s[strings.Index(s, ":")+1:] }
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && name(out[j]) < name(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
