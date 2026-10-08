/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The orphan report of a shared pool (shared-storage-design.md §16 S5): what is in the pool (files of a GPFS pool,
// RBD images of a Ceph pool) and has no record in clapi. Only reported, nothing is removed: an admin looks and
// decides. One host that reaches the pool lists it (stc_pool_objects.sh, in the background) and clapi compares:
// every volume of the pool (deleted ones are not expected: their file left behind is exactly what is looked for),
// every base copy of an image, the NVRAM of the instances whose disks are there. Temporary files younger than a day
// and files changed in the last hour are left out, they may belong to work going on.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StorageReconcileRunning = "running"
	StorageReconcileDone    = "done"
	StorageReconcileError   = "error"
	// A host that did not answer in this time is not waited for any more
	storageReconcileTimeout = 10 * time.Minute
)

// StorageOrphan is an object of a pool clapi has no record of
type StorageOrphan struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime,omitempty"`
	// volume, deleted_volume (the record of the volume is deleted), image, nvram, temporary, other
	Kind string `json:"kind"`
}

// StoragePoolObject is an object a host found in a pool
type StoragePoolObject struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

// StoragePoolObjects is what a host reports of a pool
type StoragePoolObjects struct {
	Objects   []*StoragePoolObject `json:"objects"`
	Truncated bool                 `json:"truncated"`
	Error     string               `json:"error"`
}

// DecodeStoragePoolObjects reads the gzipped base64 JSON of a host's report
func DecodeStoragePoolObjects(raw string) (*StoragePoolObjects, error) {
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return nil, err
	}
	out := &StoragePoolObjects{}
	return out, json.Unmarshal(body, out)
}

// ReconcilePool asks a host that reaches a shared pool for what is in it; a request still waiting is not sent again
func (a *StoragePoolAdmin) ReconcilePool(ctx context.Context, uuid string) (*model.StoragePoolReconcile, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	db := dbs.DBContext(ctx)
	pool := &model.StoragePool{}
	if err := db.Where("uuid = ?", uuid).Take(pool).Error; err != nil {
		return nil, NewCLError(ErrStoragePoolNotFound, "Storage pool not found", err)
	}
	if !pool.Shared() || pool.ClusterID == 0 {
		return nil, NewCLError(ErrStoragePoolInvalidState, "Only a shared pool is reconciled", nil)
	}
	if pool.Status != model.StoragePoolActive {
		return nil, NewCLError(ErrStoragePoolInvalidState, fmt.Sprintf("Storage pool %s is %s", pool.Name, pool.Status), nil)
	}
	report := &model.StoragePoolReconcile{}
	found := db.Where("pool_id = ?", pool.ID).Take(report).Error == nil
	if found && report.Status == StorageReconcileRunning && time.Since(report.RequestedAt) < storageReconcileTimeout {
		return report, nil
	}
	cluster := &model.StorageCluster{}
	if err := db.Take(cluster, pool.ClusterID).Error; err != nil {
		return nil, NewCLError(ErrStorageClusterNotFound, "The cluster of the pool is gone", err)
	}
	rows := []*model.HyperStoragePool{}
	db.Where("pool_id = ? AND status IN ?", pool.ID, []string{model.HyperPoolReady, model.HyperPoolDegraded}).Order("hostid").Find(&rows)
	host := int32(-1)
	for _, r := range rows {
		if _, online := hostOnline(db, r.Hostid); online {
			host = r.Hostid
			break
		}
	}
	if host < 0 {
		return nil, NewCLError(ErrStoragePoolInvalidState, fmt.Sprintf("No online host reaches storage pool %s now", pool.Name), nil)
	}
	now := time.Now()
	row := &model.StoragePoolReconcile{PoolID: pool.ID, Status: StorageReconcileRunning, Hostid: host, RequestedAt: now}
	if err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "pool_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{"status": StorageReconcileRunning, "hostid": host, "error": "", "requested_at": now})}).
		Create(row).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to record the reconcile request", err)
	}
	command := fmt.Sprintf("%s/stc_pool_objects.sh '%s' '%s'", storageScriptDir, ShellEscape(pool.UUID), ShellEscape(cluster.UUID))
	if err := HyperExecute(ctx, fmt.Sprintf("inter=%d", host), command); err != nil {
		db.Model(&model.StoragePoolReconcile{}).Where("pool_id = ?", pool.ID).Updates(map[string]interface{}{"status": StorageReconcileError,
			"error": "the host could not be asked"})
		return nil, NewCLError(ErrStoragePoolInvalidState, "Failed to ask the host for the objects of the pool", err)
	}
	db.Where("pool_id = ?", pool.ID).Take(report)
	return report, nil
}

// PoolReconcile is the last orphan report of a pool, nil when there is none; one the host never answered turns error
func (a *StoragePoolAdmin) PoolReconcile(ctx context.Context, uuid string) (*model.StoragePoolReconcile, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	db := dbs.DBContext(ctx)
	pool := &model.StoragePool{}
	if err := db.Where("uuid = ?", uuid).Take(pool).Error; err != nil {
		return nil, NewCLError(ErrStoragePoolNotFound, "Storage pool not found", err)
	}
	report := &model.StoragePoolReconcile{}
	if db.Where("pool_id = ?", pool.ID).Take(report).Error != nil {
		return nil, nil
	}
	if report.Status == StorageReconcileRunning && time.Since(report.RequestedAt) >= storageReconcileTimeout {
		report.Status, report.Error = StorageReconcileError, "the host did not answer in 10 minutes"
	}
	return report, nil
}

// ParseStorageOrphans reads the orphans of a report
func ParseStorageOrphans(raw string) []*StorageOrphan {
	list := []*StorageOrphan{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &list)
	}
	return list
}

var (
	reOrphanVolume = regexp.MustCompile(`^(?:volumes/)?volume-(\d+)(?:\.disk)?$`)
	reOrphanTemp   = regexp.MustCompile(`(^tmp/|-reinstall$|\.reinstall\.disk$|\.import$|\.part$)`)
)

// HandleStoragePoolObjects compares what a host found in a pool with the records of clapi and keeps the orphans
func HandleStoragePoolObjects(ctx context.Context, hostid int32, poolUUID string, report *StoragePoolObjects) error {
	db := dbs.DBContext(ctx)
	pool := &model.StoragePool{}
	if err := db.Where("uuid = ?", poolUUID).Take(pool).Error; err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		row := &model.StoragePoolReconcile{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("pool_id = ?", pool.ID).Take(row).Error; err != nil {
			return fmt.Errorf("no reconcile of pool %s was asked for", pool.Name)
		}
		if row.Hostid != hostid {
			return fmt.Errorf("host %d was not asked for pool %s", hostid, pool.Name)
		}
		now := time.Now()
		update := map[string]interface{}{"checked_at": now}
		if report.Error != "" {
			msg := report.Error
			if len(msg) > 500 {
				msg = msg[:500]
			}
			update["status"], update["error"] = StorageReconcileError, msg
			return tx.Model(&model.StoragePoolReconcile{}).Where("id = ?", row.ID).Updates(update).Error
		}
		orphans, err := storagePoolOrphans(tx, pool, report.Objects, now)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(orphans)
		update["status"], update["error"], update["objects"], update["truncated"], update["orphans"] =
			StorageReconcileDone, "", len(report.Objects), report.Truncated, string(raw)
		return tx.Model(&model.StoragePoolReconcile{}).Where("id = ?", row.ID).Updates(update).Error
	})
}

// storagePoolOrphans are the objects of a pool with no record, by kind
func storagePoolOrphans(db *gorm.DB, pool *model.StoragePool, objects []*StoragePoolObject, now time.Time) ([]*StorageOrphan, error) {
	expected := map[string]bool{}
	volumes := []*model.Volume{}
	if err := db.Unscoped().Where("storage_pool_id = ?", pool.ID).Find(&volumes).Error; err != nil {
		return nil, err
	}
	deleted := map[string]bool{}
	for _, v := range volumes {
		if v.Path == "" {
			continue
		}
		if v.DeletedAt.Valid {
			deleted[v.Path] = true
			continue
		}
		expected[v.Path] = true
		if v.InstanceID > 0 {
			expected[path.Join("nvram", fmt.Sprintf("inst-%d_VARS.fd", v.InstanceID))] = true
		}
	}
	copies := []*model.ImageStorage{}
	if err := db.Where("storage_pool_id = ?", pool.ID).Find(&copies).Error; err != nil {
		return nil, err
	}
	for _, c := range copies {
		expected[c.Path] = true
	}
	orphans := []*StorageOrphan{}
	for _, o := range objects {
		name := strings.TrimPrefix(o.Name, "./")
		if name == "" || expected[name] {
			continue
		}
		// The marker and the probe files of the pool
		if strings.HasPrefix(path.Base(name), ".") {
			continue
		}
		recent := o.Mtime > 0 && now.Sub(time.Unix(o.Mtime, 0)) < time.Hour
		temp := reOrphanTemp.MatchString(name)
		if recent || (temp && o.Mtime > 0 && now.Sub(time.Unix(o.Mtime, 0)) < 24*time.Hour) {
			continue
		}
		kind := "other"
		switch {
		case temp:
			kind = "temporary"
		case deleted[name]:
			kind = "deleted_volume"
		case reOrphanVolume.MatchString(name):
			kind = "volume"
		case strings.HasPrefix(name, "images/") || strings.HasPrefix(name, "image-"):
			kind = "image"
		case strings.HasPrefix(name, "nvram/"):
			kind = "nvram"
		}
		orphans = append(orphans, &StorageOrphan{Name: name, Size: o.Size, Mtime: o.Mtime, Kind: kind})
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Name < orphans[j].Name })
	return orphans, nil
}
