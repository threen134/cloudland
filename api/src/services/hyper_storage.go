/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
	"api/src/utils"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var HyperStorage = &HyperStorageAdmin{}

// HyperStorageAdmin manages the local storage pools of hosts (§4 and §5 of the local storage plan)
type HyperStorageAdmin struct{}

const (
	poolCommandDeadline = 30 * time.Minute
	// Scan results older than this can not be used to pick disks
	diskScanValidity = 24 * time.Hour
	// A RAID1 pair whose disks differ more than this in size wastes space: tell the admin
	raidSizeTolerance = 0.01
)

// PoolDevice is one disk of a pool as recorded in HyperStoragePool.Devices
type PoolDevice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Serial    string `json:"serial"`
	Model     string `json:"model"`
	SizeBytes int64  `json:"size_bytes"`
	Media     string `json:"media"`
	Array     string `json:"array"`
	Pair      int    `json:"pair"`
}

// HyperPoolView is a pool row on a host with the figures computed for display
type HyperPoolView struct {
	Row               *model.HyperStoragePool
	Pool              *model.StoragePool
	Hyper             *model.Hyper
	AllocatedBytes    int64
	ReservedBytes     int64
	VolumeCount       int64
	StorageFullPaused int64
	Devices           []*PoolDevice
}

func requireSystemAdmin(ctx context.Context) error {
	if !GetMemberShip(ctx).CheckSystemPermission() {
		return NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
	}
	return nil
}

func parseDevices(raw string) (devices []*PoolDevice) {
	devices = []*PoolDevice{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &devices)
	}
	return
}

func (a *HyperStorageAdmin) view(db *gorm.DB, row *model.HyperStoragePool, pool *model.StoragePool, hyper *model.Hyper) *HyperPoolView {
	v := &HyperPoolView{Row: row, Pool: pool, Hyper: hyper, Devices: parseDevices(row.Devices)}
	v.AllocatedBytes, _ = allocatedBytes(db, row.Hostid, row.PoolID)
	v.ReservedBytes = reservedBytes(db, row.Hostid, row.PoolID)
	db.Model(&model.Volume{}).Where("storage_pool_id = ? AND hyper = ?", row.PoolID, row.Hostid).Count(&v.VolumeCount)
	v.StorageFullPaused = storageFullPaused(db, row.Hostid, row.PoolID)
	return v
}

// ListHostPools returns the pools of a host, the built-in one first
func (a *HyperStorageAdmin) ListHostPools(ctx context.Context, hyper *model.Hyper) (views []*HyperPoolView, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	rows := []*model.HyperStoragePool{}
	if err = db.Preload("Pool").Where("hostid = ?", hyper.Hostid).Find(&rows).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query host pools", err)
	}
	for _, row := range rows {
		if row.Pool == nil {
			continue
		}
		views = append(views, a.view(db, row, row.Pool, hyper))
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Pool.Builtin != views[j].Pool.Builtin {
			return views[i].Pool.Builtin
		}
		return views[i].Pool.Name < views[j].Pool.Name
	})
	return
}

// ListPoolHosts returns the rows of a pool on every host
func (a *HyperStorageAdmin) ListPoolHosts(ctx context.Context, pool *model.StoragePool) (views []*HyperPoolView, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	rows := []*model.HyperStoragePool{}
	if err = db.Where("pool_id = ?", pool.ID).Order("hostid").Find(&rows).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query host pools", err)
	}
	for _, row := range rows {
		hyper := &model.Hyper{}
		if db.Where("hostid = ?", row.Hostid).Take(hyper).Error != nil {
			continue
		}
		views = append(views, a.view(db, row, pool, hyper))
	}
	return
}

// PoolSummary sums a pool over all hosts for the pool list
type PoolSummary struct {
	Hosts          int
	AvailableHosts int
	CapacityBytes  int64
	UsedBytes      int64
	AllocatedBytes int64
}

func (a *HyperStorageAdmin) Summary(ctx context.Context, pool *model.StoragePool) (s *PoolSummary) {
	return a.Summaries(ctx, []*model.StoragePool{pool})[pool.ID]
}

// Summaries builds the summaries of many pools with three queries (the host rows, and the allocations summed per
// host and pool), so the pool list does not query twice per pool and host. Every pool asked for gets a summary
func (a *HyperStorageAdmin) Summaries(ctx context.Context, pools []*model.StoragePool) map[int64]*PoolSummary {
	db := dbs.DBContext(ctx)
	summaries := make(map[int64]*PoolSummary, len(pools))
	builtin := map[int64]bool{}
	poolIDs := make([]int64, 0, len(pools))
	for _, pool := range pools {
		summaries[pool.ID] = &PoolSummary{}
		builtin[pool.ID] = pool.Builtin
		poolIDs = append(poolIDs, pool.ID)
	}
	if len(poolIDs) == 0 {
		return summaries
	}
	rows := []*model.HyperStoragePool{}
	db.Where("pool_id IN ?", poolIDs).Find(&rows)
	alloc := allocatedByHostPool(db, "storage_pool_id IN ?", "pool_id IN ?", poolIDs)
	for _, row := range rows {
		s := summaries[row.PoolID]
		if s == nil {
			continue
		}
		s.Hosts++
		if row.Available() || (builtin[row.PoolID] && row.CapacityAt == nil) {
			s.AvailableHosts++
		}
		s.CapacityBytes += row.CapacityBytes
		s.UsedBytes += row.UsedBytes
		s.AllocatedBytes += alloc[[2]int64{int64(row.Hostid), row.PoolID}]
	}
	return summaries
}

// allocatedByHostPool sums what allocatedBytes counts (volume sizes plus live reservations) per host and pool in
// two grouped queries; volumeCond and reservationCond narrow the rows (hosts or pools) and take the same argument
func allocatedByHostPool(db *gorm.DB, volumeCond, reservationCond string, arg interface{}) map[[2]int64]int64 {
	type allocRow struct {
		Hostid int32
		PoolID int64
		GB     int64
	}
	alloc := map[[2]int64]int64{}
	var volRows, resRows []allocRow
	db.Model(&model.Volume{}).Select("hyper AS hostid, storage_pool_id AS pool_id, COALESCE(SUM(size), 0) AS gb").
		Where(volumeCond, arg).Group("hyper, storage_pool_id").Scan(&volRows)
	db.Model(&model.StorageReservation{}).Select("hostid, pool_id, COALESCE(SUM(size_gb), 0) AS gb").
		Where(reservationCond, arg).Where("expires_at > ?", time.Now()).Group("hostid, pool_id").Scan(&resRows)
	for _, r := range append(volRows, resRows...) {
		alloc[[2]int64{int64(r.Hostid), r.PoolID}] += r.GB * gib
	}
	return alloc
}

// PendingInstances lists the instances of a host that wait for a pool to come back before starting
func PendingInstances(ctx context.Context, hostid int32) (instances []*model.Instance) {
	dbs.DBContext(ctx).Where("hyper = ? AND reason = ?", hostid, InstanceReasonStoragePending).
		Order("id").Find(&instances)
	return
}

// AvailablePoolUUIDs lists the pools usable on a host, for choosing where a volume can be attached
func AvailablePoolUUIDs(ctx context.Context, hostid int32) (uuids []string) {
	db := dbs.DBContext(ctx)
	rows := []*model.HyperStoragePool{}
	db.Preload("Pool").Where("hostid = ?", hostid).Find(&rows)
	for _, row := range rows {
		if row.Pool == nil || row.Pool.Status != model.StoragePoolActive {
			continue
		}
		if row.Available() || (row.Pool.Builtin && row.CapacityAt == nil) {
			uuids = append(uuids, row.Pool.UUID)
		}
	}
	return
}

// ---- disks ----

// ScanDisks asks a host to scan its disks; the result arrives with the host_disks callback
func (a *HyperStorageAdmin) ScanDisks(ctx context.Context, hyper *model.Hyper) (err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if _, online := hostOnline(dbs.DBContext(ctx), hyper.Hostid); !online {
		return NewCLError(ErrHypervisorInvalidState, fmt.Sprintf("%s is offline", hyper.Hostname), nil)
	}
	return HyperExecute(ctx, fmt.Sprintf("inter=%d", hyper.Hostid), "/opt/cloudland/scripts/backend/scan_host_disks.sh")
}

func (a *HyperStorageAdmin) ListDisks(ctx context.Context, hyper *model.Hyper) (disks []*model.HyperDisk, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	disks = []*model.HyperDisk{}
	err = dbs.DBContext(ctx).Where("hostid = ?", hyper.Hostid).Order("name").Find(&disks).Error
	return
}

// OrphanCount is how many volumes of a pool wait for adoption from a host that was deleted
func OrphanCount(ctx context.Context, poolRef string, ownerHostid int32) (count int64) {
	db := dbs.DBContext(ctx)
	pool, err := poolByRef(db, poolRef)
	if err != nil {
		return 0
	}
	q := db.Model(&model.Volume{}).Where("storage_pool_id = ? AND status = ?", pool.ID, model.VolumeStatusOrphaned)
	if ownerHostid > 0 {
		q = q.Where("hyper = ?", ownerHostid)
	}
	q.Count(&count)
	return
}

// poolByRef finds a pool by its uuid or by the first characters of it (md array names only carry 8)
// poolPrefixRegex is the 8 hex digits a host names a pool by (the prefix of its uuid in array names and tags)
var poolPrefixRegex = regexp.MustCompile(`^[0-9a-f]{8}$`)

// poolByRef finds a pool by its uuid or by the 8-digit prefix hosts use. The reference comes from a host, so it is
// checked before it goes anywhere near a LIKE: a stray % or _ would match any pool
func poolByRef(db *gorm.DB, ref string) (pool *model.StoragePool, err error) {
	pool = &model.StoragePool{}
	if utils.IsUUID(ref) {
		err = db.Where("uuid = ?", ref).Take(pool).Error
	} else if poolPrefixRegex.MatchString(ref) {
		err = db.Where("uuid LIKE ?", ref+"%").Take(pool).Error
	} else {
		err = gorm.ErrRecordNotFound
	}
	return
}

func (a *HyperStorageAdmin) SetDiskMedia(ctx context.Context, hyper *model.Hyper, diskID int64, media string) (disk *model.HyperDisk, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if media != "" && media != "ssd" && media != "hdd" && media != "nvme" {
		return nil, NewCLError(ErrInvalidParameter, "Media must be ssd, hdd, nvme, or empty to use the detected one", nil)
	}
	db := dbs.DBContext(ctx)
	disk = &model.HyperDisk{}
	if err = db.Where("id = ? AND hostid = ?", diskID, hyper.Hostid).Take(disk).Error; err != nil {
		return nil, NewCLError(ErrResourceNotFound, "Disk not found", err)
	}
	updates := map[string]interface{}{"media": media, "media_source": "manual"}
	if media == "" {
		updates = map[string]interface{}{"media": disk.DetectedMedia, "media_source": "auto"}
	}
	if err = db.Model(disk).Updates(updates).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to update disk", err)
	}
	err = db.Take(disk, disk.ID).Error
	return
}

// ScannedDisk is one disk in the host_disks callback
type ScannedDisk struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Serial      string `json:"serial"`
	Model       string `json:"model"`
	SizeBytes   int64  `json:"size_bytes"`
	Transport   string `json:"transport"`
	Media       string `json:"media"`
	State       string `json:"state"`
	Detail      string `json:"detail"`
	PoolUUID    string `json:"pool_uuid"`
	OwnerHostid int32  `json:"owner_hostid"`
}

// SaveScannedDisks stores the scan result of a host; admin-set media survive rescans
func SaveScannedDisks(ctx context.Context, hostid int32, disks []*ScannedDisk) (err error) {
	return dbs.DBContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing := []*model.HyperDisk{}
		if err := tx.Where("hostid = ?", hostid).Find(&existing).Error; err != nil {
			return err
		}
		byID := map[string]*model.HyperDisk{}
		for _, d := range existing {
			byID[d.DiskID] = d
		}
		now := time.Now()
		seen := map[string]bool{}
		for _, s := range disks {
			if s.ID == "" {
				continue
			}
			seen[s.ID] = true
			d := byID[s.ID]
			if d == nil {
				d = &model.HyperDisk{Hostid: hostid, DiskID: s.ID, MediaSource: "auto"}
			}
			d.Name, d.Path, d.Serial, d.DiskModel = s.Name, s.Path, s.Serial, s.Model
			d.SizeBytes, d.Transport, d.DetectedMedia = s.SizeBytes, s.Transport, s.Media
			d.State, d.Detail, d.PoolUUID, d.OwnerHostid, d.ScannedAt = s.State, s.Detail, s.PoolUUID, s.OwnerHostid, now
			if d.MediaSource != "manual" {
				d.Media = s.Media
				d.MediaSource = "auto"
			}
			if err := tx.Save(d).Error; err != nil {
				return err
			}
		}
		for id, d := range byID {
			if !seen[id] {
				if err := tx.Unscoped().Delete(d).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ---- pools on a host ----

// PoolDiskRequest carries the disks of a create, extend or replace request
type PoolDiskRequest struct {
	Disks              []string
	Wipe               bool
	AllowMediaMismatch bool
	DestroyPools       []string
	Confirm            string
}

// PairWarning tells about a RAID1 pair whose disks differ in size
type PairWarning struct {
	Pair        int   `json:"pair"`
	UsableBytes int64 `json:"usable_bytes"`
	WastedBytes int64 `json:"wasted_bytes"`
}

func newVgName(pool *model.StoragePool) string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("cl_%s_%s", pool.UUID[:8], hex.EncodeToString(b))
}

func checkConfirm(hyper *model.Hyper, confirm string) error {
	if confirm != hyper.Hostname {
		return NewCLError(ErrStorageConfirmMismatch, "Type the host name to confirm", nil)
	}
	return nil
}

// pickDisks validates the disks of a request against the last scan (§8.1) and returns them in request order
func (a *HyperStorageAdmin) pickDisks(tx *gorm.DB, hyper *model.Hyper, req *PoolDiskRequest) (disks []*model.HyperDisk, err error) {
	if len(req.Disks) == 0 {
		return nil, NewCLError(ErrInvalidParameter, "No disk selected", nil)
	}
	seen := map[string]bool{}
	for _, id := range req.Disks {
		if seen[id] {
			return nil, NewCLError(ErrInvalidParameter, fmt.Sprintf("Disk %s is selected twice", id), nil)
		}
		seen[id] = true
		disk := &model.HyperDisk{}
		if err = tx.Where("hostid = ? AND disk_id = ?", hyper.Hostid, id).Take(disk).Error; err != nil {
			return nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s is not in the scan result of %s; scan the disks first", id, hyper.Hostname), nil)
		}
		if time.Since(disk.ScannedAt) > diskScanValidity {
			return nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("The scan result of disk %s is too old; scan the disks again", disk.Name), nil)
		}
		switch disk.State {
		case model.DiskFree:
		case model.DiskDirty:
			if !req.Wipe {
				return nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s has data on it; choose to wipe it", disk.Name), nil)
			}
		case model.DiskCloudlandPool:
			if err = a.checkDestroyable(tx, disk, req.DestroyPools); err != nil {
				return
			}
		default:
			return nil, NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s can not be used: %s (%s)", disk.Name, disk.State, disk.Detail), nil)
		}
		disks = append(disks, disk)
	}
	return
}

// checkDestroyable allows wiping the disks of an old CloudLand pool only when nothing still refers to them (§5.9)
func (a *HyperStorageAdmin) checkDestroyable(tx *gorm.DB, disk *model.HyperDisk, destroy []string) error {
	listed := false
	for _, d := range destroy {
		if d != "" && disk.PoolUUID != "" && strings.HasPrefix(d, disk.PoolUUID) {
			listed = true
		}
	}
	if !listed {
		return NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Disk %s holds CloudLand pool %s; adopt it, or list the pool to destroy its data", disk.Name, disk.PoolUUID), nil)
	}
	pool, err := poolByRef(tx, disk.PoolUUID)
	if err != nil {
		return nil // the pool itself was deleted: nothing refers to the data
	}
	var count int64
	q := tx.Model(&model.Volume{}).Where("storage_pool_id = ? AND status IN ?", pool.ID, []string{string(model.VolumeStatusOrphaned), string(model.VolumeStatusLost)})
	if disk.OwnerHostid > 0 {
		q = q.Where("hyper = ?", disk.OwnerHostid)
	}
	q.Count(&count)
	if count > 0 {
		return NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("%d volumes of pool %s wait for these disks; adopt the pool or abandon them first", count, pool.Name), nil)
	}
	if disk.OwnerHostid > 0 {
		var rows int64
		tx.Model(&model.HyperStoragePool{}).Where("hostid = ? AND pool_id = ?", disk.OwnerHostid, pool.ID).Count(&rows)
		if rows > 0 {
			return NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Host %d still runs pool %s on these disks", disk.OwnerHostid, pool.Name), nil)
		}
	}
	return nil
}

// checkMedia implements the media checks of §2.6: the disks must match the pool and each other (and, when
// extending, the disks already in the pool) unless the request says to go ahead anyway
func checkMedia(pool *model.StoragePool, disks []*model.HyperDisk, existing []*PoolDevice, allow bool) error {
	if allow {
		return nil
	}
	var bad []string
	if pool.Media != "" {
		for _, d := range disks {
			if d.Media != pool.Media {
				bad = append(bad, fmt.Sprintf("%s is %s", d.Name, d.Media))
			}
		}
		if len(bad) > 0 {
			return NewCLError(ErrStorageMediaMismatch, fmt.Sprintf("Pool %s is %s but %s; confirm to use them anyway", pool.Name, pool.Media, strings.Join(bad, ", ")), nil)
		}
	}
	media := map[string][]string{}
	for _, d := range existing {
		media[d.Media] = append(media[d.Media], d.Name+" (in the pool)")
	}
	for _, d := range disks {
		media[d.Media] = append(media[d.Media], d.Name)
	}
	if len(media) > 1 {
		var parts []string
		for m, names := range media {
			parts = append(parts, fmt.Sprintf("%s: %s", m, strings.Join(names, ", ")))
		}
		sort.Strings(parts)
		return NewCLError(ErrStorageMediaMismatch, fmt.Sprintf("The disks differ in media (%s); confirm to mix them anyway", strings.Join(parts, "; ")), nil)
	}
	return nil
}

// raidPairs pairs disks in request order and reports pairs whose disks differ in size
func raidPairs(disks []*model.HyperDisk, firstPair int) (warnings []*PairWarning) {
	for i := 0; i+1 < len(disks); i += 2 {
		a, b := disks[i].SizeBytes, disks[i+1].SizeBytes
		small, big := a, b
		if small > big {
			small, big = big, small
		}
		if big > 0 && float64(big-small)/float64(big) > raidSizeTolerance {
			warnings = append(warnings, &PairWarning{Pair: firstPair + i/2, UsableBytes: small, WastedBytes: big - small})
		}
	}
	return
}

func devicesOf(disks []*model.HyperDisk, layout string, firstPair int) []*PoolDevice {
	devices := []*PoolDevice{}
	for i, d := range disks {
		dev := &PoolDevice{ID: d.DiskID, Name: d.Name, Serial: d.Serial, Model: d.DiskModel, SizeBytes: d.SizeBytes, Media: d.Media, Pair: -1}
		if layout == model.LayoutRaid1 {
			dev.Pair = firstPair + i/2
		}
		devices = append(devices, dev)
	}
	return devices
}

func quoteArgs(values []string) string {
	var parts []string
	for _, v := range values {
		parts = append(parts, "'"+ShellEscape(v)+"'")
	}
	return strings.Join(parts, " ")
}

// CreatePool sets a storage pool up on a host (§2.5, §4.3)
func (a *HyperStorageAdmin) CreatePool(ctx context.Context, hyper *model.Hyper, pool *model.StoragePool, layout string, req *PoolDiskRequest) (row *model.HyperStoragePool, warnings []*PairWarning, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if pool.Builtin {
		return nil, nil, NewCLError(ErrInvalidParameter, "The built-in pool exists on every host already", nil)
	}
	if err = checkConfirm(hyper, req.Confirm); err != nil {
		return
	}
	switch layout {
	case model.LayoutSingle, model.LayoutLinear:
	case model.LayoutRaid1:
		if !raid1Supported {
			return nil, nil, NewCLError(ErrOperationNotSupported, "RAID1 pools are not supported yet", nil)
		}
	default:
		return nil, nil, NewCLError(ErrInvalidParameter, "Layout must be single, linear or raid1", nil)
	}
	n := len(req.Disks)
	if layout == model.LayoutSingle && n != 1 || layout == model.LayoutLinear && n < 1 || layout == model.LayoutRaid1 && (n < 2 || n%2 != 0) {
		return nil, nil, NewCLError(ErrInvalidParameter, fmt.Sprintf("Layout %s can not be built from %d disks", layout, n), nil)
	}
	db := dbs.DBContext(ctx)
	if _, online := hostOnline(db, hyper.Hostid); !online {
		return nil, nil, NewCLError(ErrHypervisorInvalidState, fmt.Sprintf("%s is offline", hyper.Hostname), nil)
	}
	var command string
	err = db.Transaction(func(tx *gorm.DB) (terr error) {
		disks, terr := a.pickDisks(tx, hyper, req)
		if terr != nil {
			return
		}
		if terr = checkMedia(pool, disks, nil, req.AllowMediaMismatch); terr != nil {
			return
		}
		if layout == model.LayoutRaid1 {
			warnings = raidPairs(disks, 0)
		}
		// The same disk must not be picked by two operations running at once
		var busy int64
		tx.Model(&model.HyperStoragePool{}).Where("hostid = ? AND status IN ?", hyper.Hostid,
			[]string{model.HyperPoolCreating, model.HyperPoolExtending, model.HyperPoolRemoving}).Count(&busy)
		if busy > 0 {
			return NewCLError(ErrStoragePoolInvalidState, "Another storage operation is running on this host", nil)
		}
		row = &model.HyperStoragePool{}
		existing := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("hostid = ? AND pool_id = ?", hyper.Hostid, pool.ID).Take(row).Error
		if existing == nil {
			if row.Status != model.HyperPoolError || row.LastOp != "create" {
				return NewCLError(ErrStoragePoolInvalidState, fmt.Sprintf("Storage pool %s already exists on %s", pool.Name, hyper.Hostname), nil)
			}
		} else {
			row = &model.HyperStoragePool{Hostid: hyper.Hostid, PoolID: pool.ID}
		}
		deadline := time.Now().Add(poolCommandDeadline)
		devices, _ := json.Marshal(devicesOf(disks, layout, 0))
		row.Status, row.Reason, row.LastOp, row.DeadlineAt = model.HyperPoolCreating, "", "create", &deadline
		row.Layout, row.Devices, row.VgName = layout, string(devices), newVgName(pool)
		if terr = tx.Save(row).Error; terr != nil {
			return NewCLError(ErrSQLSyntaxError, "Failed to save host pool", terr)
		}
		wipe := "0"
		if req.Wipe || len(req.DestroyPools) > 0 {
			wipe = "1"
		}
		destroy := "-"
		if len(req.DestroyPools) > 0 {
			destroy = strings.Join(req.DestroyPools, ",")
		}
		ids := []string{}
		for _, d := range disks {
			ids = append(ids, d.DiskID)
		}
		command = fmt.Sprintf("/opt/cloudland/scripts/backend/create_local_pool.sh '%s' '%s' '%s' '%d' '%s' '%s' %s",
			ShellEscape(pool.UUID), ShellEscape(layout), ShellEscape(row.VgName), hyper.Hostid, ShellEscape(wipe), ShellEscape(destroy), quoteArgs(ids))
		return nil
	})
	if err != nil {
		return
	}
	if err = HyperExecute(ctx, fmt.Sprintf("inter=%d", hyper.Hostid), command); err != nil {
		db.Model(row).Updates(map[string]interface{}{"status": model.HyperPoolError, "reason": "the command could not be sent to the host"})
	}
	return
}

// raid1Supported gates the RAID1 layout, which comes with phase L3
var raid1Supported = true

func (a *HyperStorageAdmin) hostPool(db *gorm.DB, hyper *model.Hyper, pool *model.StoragePool, lock bool) (row *model.HyperStoragePool, err error) {
	row, err = getHyperPool(db, hyper.Hostid, pool.ID, lock)
	if err != nil {
		return nil, NewCLError(ErrStoragePoolNotFound, fmt.Sprintf("%s has no storage pool %s", hyper.Hostname, pool.Name), err)
	}
	return
}

// ExtendPool adds disks to a pool online (§4.4)
func (a *HyperStorageAdmin) ExtendPool(ctx context.Context, hyper *model.Hyper, pool *model.StoragePool, req *PoolDiskRequest) (warnings []*PairWarning, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if err = checkConfirm(hyper, req.Confirm); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	if _, online := hostOnline(db, hyper.Hostid); !online {
		return nil, NewCLError(ErrHypervisorInvalidState, fmt.Sprintf("%s is offline", hyper.Hostname), nil)
	}
	var command string
	var row *model.HyperStoragePool
	err = db.Transaction(func(tx *gorm.DB) (terr error) {
		if row, terr = a.hostPool(tx, hyper, pool, true); terr != nil {
			return
		}
		if !row.Available() {
			return NewCLError(ErrStoragePoolInvalidState, fmt.Sprintf("Storage pool %s on %s is %s", pool.Name, hyper.Hostname, row.Status), nil)
		}
		if row.Layout == model.LayoutRaid1 && len(req.Disks)%2 != 0 {
			return NewCLError(ErrInvalidParameter, "A RAID1 pool grows by pairs of disks", nil)
		}
		disks, terr := a.pickDisks(tx, hyper, req)
		if terr != nil {
			return
		}
		existing := parseDevices(row.Devices)
		if terr = checkMedia(pool, disks, existing, req.AllowMediaMismatch); terr != nil {
			return
		}
		firstPair := 0
		for _, d := range existing {
			if d.Pair >= firstPair {
				firstPair = d.Pair + 1
			}
		}
		if row.Layout == model.LayoutRaid1 {
			warnings = raidPairs(disks, firstPair)
		}
		deadline := time.Now().Add(poolCommandDeadline)
		row.PrevStatus, row.Status, row.Reason, row.LastOp, row.DeadlineAt = row.Status, model.HyperPoolExtending, "", "extend", &deadline
		if terr = tx.Save(row).Error; terr != nil {
			return NewCLError(ErrSQLSyntaxError, "Failed to save host pool", terr)
		}
		ids := []string{}
		for _, d := range disks {
			ids = append(ids, d.DiskID)
		}
		wipe := "0"
		if req.Wipe {
			wipe = "1"
		}
		command = fmt.Sprintf("/opt/cloudland/scripts/backend/extend_local_pool.sh '%s' '%d' '%s' %s", ShellEscape(pool.UUID), hyper.Hostid, ShellEscape(wipe), quoteArgs(ids))
		return nil
	})
	if err != nil {
		return
	}
	if err = HyperExecute(ctx, fmt.Sprintf("inter=%d", hyper.Hostid), command); err != nil {
		db.Model(row).Updates(map[string]interface{}{"status": row.PrevStatus, "reason": "the command could not be sent to the host"})
	}
	return
}

// ReplaceDisk swaps a failed member of a RAID1 pool for a new disk (§4.9)
func (a *HyperStorageAdmin) ReplaceDisk(ctx context.Context, hyper *model.Hyper, pool *model.StoragePool, failedDisk string, req *PoolDiskRequest) (err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if err = checkConfirm(hyper, req.Confirm); err != nil {
		return
	}
	if len(req.Disks) != 1 {
		return NewCLError(ErrInvalidParameter, "Choose exactly one new disk", nil)
	}
	db := dbs.DBContext(ctx)
	if _, online := hostOnline(db, hyper.Hostid); !online {
		return NewCLError(ErrHypervisorInvalidState, fmt.Sprintf("%s is offline", hyper.Hostname), nil)
	}
	var command string
	var row *model.HyperStoragePool
	err = db.Transaction(func(tx *gorm.DB) (terr error) {
		if row, terr = a.hostPool(tx, hyper, pool, true); terr != nil {
			return
		}
		if row.Layout != model.LayoutRaid1 {
			return NewCLError(ErrOperationNotSupported, "Only disks of RAID1 pools can be replaced; declare the pool lost instead", nil)
		}
		if row.Status != model.HyperPoolDegraded {
			return NewCLError(ErrStoragePoolInvalidState, "Disks are replaced in a degraded pool", nil)
		}
		existing := parseDevices(row.Devices)
		var failed *PoolDevice
		for _, d := range existing {
			if d.ID == failedDisk {
				failed = d
			}
		}
		if failed == nil {
			return NewCLError(ErrInvalidParameter, fmt.Sprintf("Disk %s is not in the pool", failedDisk), nil)
		}
		disks, terr := a.pickDisks(tx, hyper, req)
		if terr != nil {
			return
		}
		others := []*PoolDevice{}
		for _, d := range existing {
			if d.ID != failedDisk {
				others = append(others, d)
			}
		}
		if terr = checkMedia(pool, disks, others, req.AllowMediaMismatch); terr != nil {
			return
		}
		if disks[0].SizeBytes < failed.SizeBytes {
			return NewCLError(ErrStorageDiskNotAllowed, "The new disk is smaller than the one it replaces", nil)
		}
		deadline := time.Now().Add(poolCommandDeadline)
		row.PrevStatus, row.Status, row.Reason, row.LastOp, row.DeadlineAt = row.Status, model.HyperPoolExtending, "", "replace", &deadline
		if terr = tx.Save(row).Error; terr != nil {
			return NewCLError(ErrSQLSyntaxError, "Failed to save host pool", terr)
		}
		wipe := "0"
		if req.Wipe {
			wipe = "1"
		}
		command = fmt.Sprintf("/opt/cloudland/scripts/backend/replace_local_pool_disk.sh '%s' '%d' '%s' '%s' '%s' '%s'",
			ShellEscape(pool.UUID), hyper.Hostid, ShellEscape(failed.Array), ShellEscape(failedDisk), ShellEscape(disks[0].DiskID), ShellEscape(wipe))
		return nil
	})
	if err != nil {
		return
	}
	if err = HyperExecute(ctx, fmt.Sprintf("inter=%d", hyper.Hostid), command); err != nil {
		db.Model(row).Updates(map[string]interface{}{"status": row.PrevStatus, "reason": "the command could not be sent to the host"})
	}
	return
}

// RemovePool takes a pool off a host (§4.5). force deletes leftover files of a pool with no volumes, and only
// unmounts a lost pool without touching its data.
func (a *HyperStorageAdmin) RemovePool(ctx context.Context, hyper *model.Hyper, pool *model.StoragePool, confirm string, force bool) (err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if pool.Builtin {
		return NewCLError(ErrInvalidParameter, "The built-in pool can not be removed", nil)
	}
	if err = checkConfirm(hyper, confirm); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	var command string
	var row *model.HyperStoragePool
	err = db.Transaction(func(tx *gorm.DB) (terr error) {
		if row, terr = a.hostPool(tx, hyper, pool, true); terr != nil {
			return
		}
		switch {
		case row.Status == model.HyperPoolError && row.LastOp == "create":
			// The node rolled the failed creation back: only the record is left
			return tx.Unscoped().Delete(row).Error
		case row.Status == model.HyperPoolLost:
			// The data may still be wanted: unmount only, and drop the record whatever the node says
			command = fmt.Sprintf("/opt/cloudland/scripts/backend/remove_local_pool.sh '%s' 'lost'", ShellEscape(pool.UUID))
			return tx.Unscoped().Delete(row).Error
		case row.Status == model.HyperPoolCreating || row.Status == model.HyperPoolExtending || row.Status == model.HyperPoolRemoving:
			return NewCLError(ErrStoragePoolInvalidState, fmt.Sprintf("Storage pool %s on %s is %s", pool.Name, hyper.Hostname, row.Status), nil)
		}
		var volumes int64
		tx.Model(&model.Volume{}).Where("storage_pool_id = ? AND hyper = ?", pool.ID, hyper.Hostid).Count(&volumes)
		if volumes > 0 {
			return NewCLError(ErrStoragePoolInUse, fmt.Sprintf("Storage pool %s on %s still holds %d volumes", pool.Name, hyper.Hostname, volumes), nil)
		}
		if res := reservedBytes(tx, hyper.Hostid, pool.ID); res > 0 {
			return NewCLError(ErrStoragePoolInUse, fmt.Sprintf("Storage pool %s on %s has space reserved by operations in progress", pool.Name, hyper.Hostname), nil)
		}
		if _, online := hostOnline(tx, hyper.Hostid); !online {
			return NewCLError(ErrHypervisorInvalidState, fmt.Sprintf("%s is offline", hyper.Hostname), nil)
		}
		mode := "normal"
		if force {
			mode = "force"
		}
		deadline := time.Now().Add(poolCommandDeadline)
		row.PrevStatus, row.Status, row.Reason, row.LastOp, row.DeadlineAt = row.Status, model.HyperPoolRemoving, "", "remove", &deadline
		if terr = tx.Save(row).Error; terr != nil {
			return NewCLError(ErrSQLSyntaxError, "Failed to save host pool", terr)
		}
		command = fmt.Sprintf("/opt/cloudland/scripts/backend/remove_local_pool.sh '%s' '%s'", ShellEscape(pool.UUID), ShellEscape(mode))
		return nil
	})
	if err != nil || command == "" {
		return
	}
	if xerr := HyperExecute(ctx, fmt.Sprintf("inter=%d", hyper.Hostid), command); xerr != nil {
		if row.Status == model.HyperPoolRemoving {
			db.Model(row).Updates(map[string]interface{}{"status": row.PrevStatus, "reason": "the command could not be sent to the host"})
			return xerr
		}
		logger.Ctx(ctx).Warningf("Lost pool %s on %s: the unmount command could not be sent: %v", pool.Name, hyper.Hostname, xerr)
	}
	return
}

// SetMaintenance stops the host from mounting a pool automatically while an admin works on it (§5.11)
func (a *HyperStorageAdmin) SetMaintenance(ctx context.Context, hyper *model.Hyper, pool *model.StoragePool, enable bool) (err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if pool.Builtin {
		return NewCLError(ErrInvalidParameter, "The built-in pool has no maintenance mode", nil)
	}
	db := dbs.DBContext(ctx)
	row, err := a.hostPool(db, hyper, pool, false)
	if err != nil {
		return
	}
	if _, online := hostOnline(db, hyper.Hostid); !online {
		return NewCLError(ErrHypervisorInvalidState, fmt.Sprintf("%s is offline", hyper.Hostname), nil)
	}
	switch row.Status {
	case model.HyperPoolCreating, model.HyperPoolExtending, model.HyperPoolRemoving:
		return NewCLError(ErrStoragePoolInvalidState, fmt.Sprintf("Storage pool %s on %s is %s", pool.Name, hyper.Hostname, row.Status), nil)
	}
	flag := "off"
	updates := map[string]interface{}{"maintenance": enable}
	if enable {
		flag = "on"
		updates["status"] = model.HyperPoolMaintenance
		updates["reason"] = "in maintenance"
	} else if row.Status == model.HyperPoolMaintenance {
		// The next report of the host tells how the pool is
		updates["status"] = model.HyperPoolUnavailable
		updates["reason"] = "leaving maintenance"
	}
	// A lost pool stays lost: only the flag on the node changes, so the probe mounts it again and the host can
	// report it healthy, which the restore needs
	if row.Status == model.HyperPoolLost {
		updates = map[string]interface{}{"maintenance": enable}
	}
	// The row first, the host second: a host flagged without the row knowing would show as ready while refusing
	// allocations. When the command can not be sent the row goes back to what it was
	prevMaintenance, prevStatus, prevReason := row.Maintenance, row.Status, row.Reason
	if err = db.Model(row).Updates(updates).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to update the storage pool", err)
	}
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/set_pool_maintenance.sh '%s' '%s'", ShellEscape(pool.UUID), ShellEscape(flag))
	if err = HyperExecute(ctx, fmt.Sprintf("inter=%d", hyper.Hostid), command); err != nil {
		db.Model(row).Updates(map[string]interface{}{"maintenance": prevMaintenance, "status": prevStatus, "reason": prevReason})
		return
	}
	return nil
}

// DeclareLost gives up on the data of a pool whose disks failed (§5.8)
func (a *HyperStorageAdmin) DeclareLost(ctx context.Context, hyper *model.Hyper, pool *model.StoragePool, confirm string, nodeOfflineAck bool) (count int64, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if pool.Builtin {
		return 0, NewCLError(ErrInvalidParameter, "The built-in pool can not be declared lost", nil)
	}
	if err = checkConfirm(hyper, confirm); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	err = db.Transaction(func(tx *gorm.DB) (terr error) {
		row, terr := a.hostPool(tx, hyper, pool, true)
		if terr != nil {
			return
		}
		if row.Status != model.HyperPoolUnavailable && row.Status != model.HyperPoolMaintenance {
			return NewCLError(ErrStoragePoolInvalidState, "Only an unavailable pool, or one in maintenance, can be declared lost", nil)
		}
		if row.Reason == model.ReasonNodeOffline && !nodeOfflineAck {
			return NewCLError(ErrStoragePoolInvalidState, "The host is offline, so the state of the pool is unknown; confirm that the data is really gone", nil)
		}
		if terr = tx.Model(row).Updates(map[string]interface{}{"status": model.HyperPoolLost, "prev_status": row.Status}).Error; terr != nil {
			return
		}
		res := tx.Model(&model.Volume{}).Where("storage_pool_id = ? AND hyper = ? AND status NOT IN ?", pool.ID, hyper.Hostid,
			[]string{string(model.VolumeStatusLost), string(model.VolumeStatusOrphaned)}).
			Updates(map[string]interface{}{"status": model.VolumeStatusLost, "reason": fmt.Sprintf("storage pool %s on %s was declared lost", pool.Name, hyper.Hostname)})
		count = res.RowsAffected
		return res.Error
	})
	return
}

// Restore takes back a pool declared lost when its host reports it healthy again (§5.8)
func (a *HyperStorageAdmin) Restore(ctx context.Context, hyper *model.Hyper, pool *model.StoragePool) (restored int64, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	err = db.Transaction(func(tx *gorm.DB) (terr error) {
		row, terr := a.hostPool(tx, hyper, pool, true)
		if terr != nil {
			return
		}
		if row.Status != model.HyperPoolLost {
			return NewCLError(ErrStoragePoolInvalidState, "The pool is not lost", nil)
		}
		if row.ReportedStatus != model.HyperPoolReady && row.ReportedStatus != model.HyperPoolDegraded {
			return NewCLError(ErrStoragePoolInvalidState, "The host has not reported the pool healthy again", nil)
		}
		if terr = tx.Model(row).Updates(map[string]interface{}{"status": row.ReportedStatus, "reason": row.ReportedReason}).Error; terr != nil {
			return
		}
		files := fileSet(row.Files)
		volumes := []*model.Volume{}
		tx.Where("storage_pool_id = ? AND hyper = ? AND status = ?", pool.ID, hyper.Hostid, model.VolumeStatusLost).Find(&volumes)
		for _, v := range volumes {
			if !files[fileName(v.Path)] {
				continue
			}
			status := model.VolumeStatusAvailable
			if v.InstanceID > 0 {
				status = model.VolumeStatusAttached
			}
			if terr = tx.Model(v).Updates(map[string]interface{}{"status": status, "reason": ""}).Error; terr != nil {
				return
			}
			restored++
		}
		return nil
	})
	return
}

func fileSet(raw string) map[string]bool {
	names := []string{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &names)
	}
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return set
}

func fileName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// Adopt takes over a pool left on the disks of a host that was registered again (§5.9)
func (a *HyperStorageAdmin) Adopt(ctx context.Context, hyper *model.Hyper, pool *model.StoragePool, confirm string) (err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if err = checkConfirm(hyper, confirm); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	if _, online := hostOnline(db, hyper.Hostid); !online {
		return NewCLError(ErrHypervisorInvalidState, fmt.Sprintf("%s is offline", hyper.Hostname), nil)
	}
	var row *model.HyperStoragePool
	err = db.Transaction(func(tx *gorm.DB) (terr error) {
		disks := []*model.HyperDisk{}
		tx.Where("hostid = ? AND state = ?", hyper.Hostid, model.DiskCloudlandPool).Find(&disks)
		var owner int32
		found := false
		for _, d := range disks {
			if d.PoolUUID != "" && strings.HasPrefix(pool.UUID, d.PoolUUID) {
				found = true
				if d.OwnerHostid > 0 {
					owner = d.OwnerHostid
				}
			}
		}
		if !found {
			return NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("No disk of pool %s found on %s; scan the disks first", pool.Name, hyper.Hostname), nil)
		}
		if owner > 0 && owner != hyper.Hostid {
			var live int64
			tx.Model(&model.Hyper{}).Where("hostid = ?", owner).Count(&live)
			if live > 0 {
				return NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("The disks belong to host %d, which is still registered", owner), nil)
			}
		}
		var elsewhere int64
		tx.Model(&model.HyperStoragePool{}).Where("pool_id = ? AND hostid <> ? AND status <> ?", pool.ID, hyper.Hostid, model.HyperPoolLost).Count(&elsewhere)
		if elsewhere > 0 && owner > 0 {
			var ownerRow int64
			tx.Model(&model.HyperStoragePool{}).Where("pool_id = ? AND hostid = ?", pool.ID, owner).Count(&ownerRow)
			if ownerRow > 0 {
				return NewCLError(ErrStorageDiskNotAllowed, fmt.Sprintf("Host %d still has pool %s set up", owner, pool.Name), nil)
			}
		}
		row = &model.HyperStoragePool{}
		if tx.Where("hostid = ? AND pool_id = ?", hyper.Hostid, pool.ID).Take(row).Error == nil {
			if row.Status != model.HyperPoolError {
				return NewCLError(ErrStoragePoolInvalidState, fmt.Sprintf("Storage pool %s already exists on %s", pool.Name, hyper.Hostname), nil)
			}
		} else {
			row = &model.HyperStoragePool{Hostid: hyper.Hostid, PoolID: pool.ID}
		}
		deadline := time.Now().Add(poolCommandDeadline)
		row.Status, row.Reason, row.LastOp, row.DeadlineAt = model.HyperPoolCreating, "", "adopt", &deadline
		return tx.Save(row).Error
	})
	if err != nil {
		return
	}
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/adopt_local_pool.sh '%s' '%d'", ShellEscape(pool.UUID), hyper.Hostid)
	if err = HyperExecute(ctx, fmt.Sprintf("inter=%d", hyper.Hostid), command); err != nil {
		db.Model(row).Updates(map[string]interface{}{"status": model.HyperPoolError, "reason": "the command could not be sent to the host"})
	}
	return
}

// AbandonOrphans gives up the volumes of a pool that waited for adoption from a deleted host (§5.9)
func (a *HyperStorageAdmin) AbandonOrphans(ctx context.Context, pool *model.StoragePool, oldHostid int32, confirm string) (count int64, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if confirm != pool.Name {
		return 0, NewCLError(ErrStorageConfirmMismatch, "Type the pool name to confirm", nil)
	}
	res := dbs.DBContext(ctx).Model(&model.Volume{}).Where("storage_pool_id = ? AND hyper = ? AND status = ?", pool.ID, oldHostid, model.VolumeStatusOrphaned).
		Updates(map[string]interface{}{"status": model.VolumeStatusLost, "reason": "adoption abandoned"})
	return res.RowsAffected, res.Error
}

// ScanUsage asks a host to list the actual usage of the files of a pool (§6.1)
func (a *HyperStorageAdmin) ScanUsage(ctx context.Context, hyper *model.Hyper, pool *model.StoragePool) (err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	if _, err = a.hostPool(db, hyper, pool, false); err != nil && !pool.Builtin {
		return
	}
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/pool_usage.sh '%s'", ShellEscape(PoolScriptID(pool)))
	return HyperExecute(ctx, fmt.Sprintf("inter=%d", hyper.Hostid), command)
}

// UsageEntry is one file of a usage report
type UsageEntry struct {
	Path       string `json:"path"`
	Bytes      int64  `json:"bytes"`
	Size       int64  `json:"size"`
	VolumeUUID string `json:"volume_uuid,omitempty"`
	VolumeName string `json:"volume_name,omitempty"`
	Instance   string `json:"instance,omitempty"`
	InstanceID string `json:"instance_uuid,omitempty"`
	Owner      string `json:"owner,omitempty"`
}

// HostPoolFigures is one pool of a host in the disk summary of the host list (§8.3)
type HostPoolFigures struct {
	UUID           string  `json:"uuid"`
	Name           string  `json:"name"`
	Builtin        bool    `json:"builtin"`
	Status         string  `json:"status"`
	CapacityBytes  int64   `json:"capacity_bytes"`
	UsedBytes      int64   `json:"used_bytes"`
	AllocatedBytes int64   `json:"allocated_bytes"`
	UsageRatio     float64 `json:"usage_ratio"`
}

// HostStorageSummary sums the pools of a host (built-in one included) for the disk column of the host list: raw
// capacity and allocations, never multiplied by an over-commit ratio, coloured by the fullest pool
type HostStorageSummary struct {
	TotalBytes     int64
	UsedBytes      int64
	AllocatedBytes int64
	MaxUsageRatio  float64
	Pools          []*HostPoolFigures
}

func StorageSummaryOfHost(ctx context.Context, hostid int32) (s *HostStorageSummary) {
	return StorageSummaryOfHosts(ctx, []int32{hostid})[hostid]
}

// StorageSummaryOfHosts builds the summaries of many hosts with a fixed number of queries (the pool rows, their
// pools, and the allocations summed per host and pool), so the host list does not query once per host and pool.
// Every host asked for gets a summary, empty when it has no pool.
func StorageSummaryOfHosts(ctx context.Context, hostids []int32) map[int32]*HostStorageSummary {
	db := dbs.DBContext(ctx)
	summaries := make(map[int32]*HostStorageSummary, len(hostids))
	for _, id := range hostids {
		summaries[id] = &HostStorageSummary{Pools: []*HostPoolFigures{}}
	}
	if len(hostids) == 0 {
		return summaries
	}
	rows := []*model.HyperStoragePool{}
	db.Preload("Pool").Where("hostid IN ?", hostids).Find(&rows)
	alloc := allocatedByHostPool(db, "hyper IN ?", "hostid IN ?", hostids)

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Pool == nil || rows[j].Pool == nil {
			return rows[i].Pool != nil
		}
		if rows[i].Pool.Builtin != rows[j].Pool.Builtin {
			return rows[i].Pool.Builtin
		}
		return rows[i].Pool.Name < rows[j].Pool.Name
	})
	for _, row := range rows {
		s := summaries[row.Hostid]
		if row.Pool == nil || s == nil {
			continue
		}
		allocated := alloc[[2]int64{int64(row.Hostid), row.PoolID}]
		ratio := row.UsageRatio()
		s.Pools = append(s.Pools, &HostPoolFigures{
			UUID: row.Pool.UUID, Name: row.Pool.Name, Builtin: row.Pool.Builtin, Status: row.Status,
			CapacityBytes: row.CapacityBytes, UsedBytes: row.UsedBytes, AllocatedBytes: allocated, UsageRatio: ratio,
		})
		s.TotalBytes += row.CapacityBytes
		s.UsedBytes += row.UsedBytes
		s.AllocatedBytes += allocated
		if ratio > s.MaxUsageRatio {
			s.MaxUsageRatio = ratio
		}
	}
	return summaries
}

// HostPoolUsage returns the last usage report of a pool on a host (§6.1)
func (a *HyperStorageAdmin) HostPoolUsage(ctx context.Context, hyper *model.Hyper, pool *model.StoragePool) (entries []*UsageEntry, at *time.Time, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	row, err := a.hostPool(dbs.DBContext(ctx), hyper, pool, false)
	if err != nil {
		return
	}
	entries = []*UsageEntry{}
	if row.UsageReport != "" {
		_ = json.Unmarshal([]byte(row.UsageReport), &entries)
	}
	return entries, row.UsageAt, nil
}

// PoolByRef finds a pool by its uuid or its first 8 characters (all an md array name carries)
func PoolByRef(ctx context.Context, ref string) (*model.StoragePool, error) {
	return poolByRef(dbs.DBContext(ctx), ref)
}
