/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

var storagePoolAdmin = &StoragePoolAdmin{}

type StoragePoolAdmin struct{}

var poolNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{1,63}$`)

var validMedia = map[string]bool{"": true, "ssd": true, "hdd": true, "nvme": true}

var (
	builtinPoolMu sync.Mutex
	builtinPoolID int64
)

func init() {
	// Idempotent start-up tasks: the built-in pool, its row on every host, and the storage fields of existing volumes
	dbs.AutoUpgrade("20260922-storage-pools", upgradeStoragePools)
}

func upgradeStoragePools(db *gorm.DB) (err error) {
	pool, err := ensureBuiltinPool(db)
	if err != nil {
		return
	}
	// Volumes created before storage pools existed live in the built-in pool
	if err = db.Exec("UPDATE volumes SET storage_pool_id = ? WHERE storage_pool_id = 0 OR storage_pool_id IS NULL", pool.ID).Error; err != nil {
		return
	}
	// Paths used to be file names only; they are relative to the pool root now
	if err = db.Exec(`UPDATE volumes SET path = 'volume/' || path
		WHERE storage_pool_id = ? AND booting = false AND path LIKE 'volume-%.disk' AND path NOT LIKE '%/%'`, pool.ID).Error; err != nil {
		return
	}
	if err = db.Exec(`UPDATE volumes SET path = 'instance/inst-' || instance_id || '.disk'
		WHERE storage_pool_id = ? AND booting = true AND instance_id > 0 AND path NOT LIKE 'instance/%'`, pool.ID).Error; err != nil {
		return
	}
	// Boot volumes never got their host before; take it from their instance
	if err = db.Exec(`UPDATE volumes SET hyper = instances.hyper FROM instances
		WHERE volumes.instance_id = instances.id AND volumes.booting = true AND (volumes.hyper IS NULL OR volumes.hyper <= 0)
		AND instances.hyper > 0`).Error; err != nil {
		return
	}
	err = ensureBuiltinHyperPools(db, pool.ID)
	return
}

func ensureBuiltinPool(db *gorm.DB) (pool *model.StoragePool, err error) {
	pool = &model.StoragePool{}
	err = db.Where("builtin = ?", true).Take(pool).Error
	if err == nil {
		return
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}
	isDefault := true
	var count int64
	if err = db.Model(&model.StoragePool{}).Where("is_default = ?", true).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		isDefault = false
	}
	pool = &model.StoragePool{
		Name:        model.BuiltinPoolName,
		Driver:      model.StorageDriverLocal,
		MountPath:   model.BuiltinPoolRoot,
		Builtin:     true,
		Status:      model.StoragePoolActive,
		IsDefault:   isDefault,
		OverRatio:   1,
		Description: "Built-in local storage of every host",
	}
	err = db.Create(pool).Error
	return
}

// ensureBuiltinHyperPools gives every host a row for the built-in pool, so capacity can be reported and admitted
func ensureBuiltinHyperPools(db *gorm.DB, poolID int64) (err error) {
	hypers := []*model.Hyper{}
	if err = db.Where("hostid >= 0").Find(&hypers).Error; err != nil {
		return
	}
	for _, h := range hypers {
		if err = ensureBuiltinHyperPool(db, poolID, h.Hostid); err != nil {
			return
		}
	}
	return
}

func ensureBuiltinHyperPool(db *gorm.DB, poolID int64, hostid int32) (err error) {
	var count int64
	if err = db.Model(&model.HyperStoragePool{}).Where("hostid = ? AND pool_id = ?", hostid, poolID).Count(&count).Error; err != nil {
		return
	}
	if count > 0 {
		return
	}
	row := &model.HyperStoragePool{
		Hostid: hostid,
		PoolID: poolID,
		Status: model.HyperPoolReady,
		Layout: model.LayoutBuiltin,
	}
	return db.Create(row).Error
}

// BuiltinPool returns the built-in local pool
func BuiltinPool(ctx context.Context) (pool *model.StoragePool, err error) {
	_, db := GetContextDB(ctx)
	builtinPoolMu.Lock()
	id := builtinPoolID
	builtinPoolMu.Unlock()
	pool = &model.StoragePool{}
	if id > 0 {
		if err = db.Unscoped().Take(pool, id).Error; err == nil {
			return
		}
	}
	if err = db.Where("builtin = ?", true).Take(pool).Error; err != nil {
		if pool, err = ensureBuiltinPool(dbs.DBContext(ctx)); err != nil {
			return nil, NewCLError(ErrStoragePoolNotFound, "Built-in storage pool not found", err)
		}
	}
	builtinPoolMu.Lock()
	builtinPoolID = pool.ID
	builtinPoolMu.Unlock()
	return
}

// EnsureBuiltinHyperPool creates the built-in pool row of a newly registered host
func EnsureBuiltinHyperPool(ctx context.Context, hostid int32) (err error) {
	pool, err := BuiltinPool(ctx)
	if err != nil {
		return
	}
	_, db := GetContextDB(ctx)
	return ensureBuiltinHyperPool(db, pool.ID, hostid)
}

// PoolScriptID identifies a pool to node scripts: "builtin" or the pool uuid
func PoolScriptID(pool *model.StoragePool) string {
	if pool == nil || pool.Builtin {
		return "builtin"
	}
	return pool.UUID
}

// PoolRelPath is where a volume file lives inside its pool
func PoolRelPath(pool *model.StoragePool, volume *model.Volume) string {
	if pool != nil && !pool.Builtin {
		return fmt.Sprintf("volumes/volume-%d.disk", volume.ID)
	}
	if volume.Booting && volume.InstanceID > 0 {
		return fmt.Sprintf("instance/inst-%d.disk", volume.InstanceID)
	}
	return fmt.Sprintf("volume/volume-%d.disk", volume.ID)
}

// VolumeAbsPath is the absolute path of a volume file on its host
func VolumeAbsPath(pool *model.StoragePool, volume *model.Volume) string {
	return path.Join(pool.Root(), volume.Path)
}

// VolumePool loads the pool of a volume
func VolumePool(ctx context.Context, volume *model.Volume) (pool *model.StoragePool, err error) {
	if volume.StoragePool != nil && volume.StoragePool.ID == volume.StoragePoolID {
		return volume.StoragePool, nil
	}
	if volume.StoragePoolID == 0 {
		pool, err = BuiltinPool(ctx)
	} else {
		_, db := GetContextDB(ctx)
		pool = &model.StoragePool{}
		if err = db.Unscoped().Take(pool, volume.StoragePoolID).Error; err != nil {
			err = NewCLError(ErrStoragePoolNotFound, "Storage pool of the volume not found", err)
			return
		}
	}
	if err == nil {
		volume.StoragePool = pool
	}
	return
}

// GetDefaultPool returns the pool used when a request names none
func (a *StoragePoolAdmin) GetDefaultPool(ctx context.Context) (pool *model.StoragePool, err error) {
	_, db := GetContextDB(ctx)
	pool = &model.StoragePool{}
	if err = db.Where("is_default = ? AND status = ?", true, model.StoragePoolActive).Take(pool).Error; err == nil {
		return
	}
	return BuiltinPool(ctx)
}

// Resolve finds a pool by UUID or name; an empty reference means the default pool
func (a *StoragePoolAdmin) Resolve(ctx context.Context, reference *BaseReference) (pool *model.StoragePool, err error) {
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		return a.GetDefaultPool(ctx)
	}
	_, db := GetContextDB(ctx)
	pool = &model.StoragePool{}
	q := db
	if reference.ID != "" {
		q = q.Where("uuid = ?", reference.ID)
	} else {
		q = q.Where("name = ?", reference.Name)
	}
	if err = q.Take(pool).Error; err != nil {
		err = NewCLError(ErrStoragePoolNotFound, "Storage pool not found", err)
	}
	return
}

func (a *StoragePoolAdmin) GetByUUID(ctx context.Context, uuid string) (pool *model.StoragePool, err error) {
	return a.Resolve(ctx, &BaseReference{ID: uuid})
}

func (a *StoragePoolAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, pools []*model.StoragePool, err error) {
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	if limit == 0 {
		limit = 50
	}
	if order == "" {
		order = "created_at"
	}
	q := db.Model(&model.StoragePool{}).Scopes(dbs.Contains(query, "name"))
	if !memberShip.IsSystemAdmin() {
		q = q.Where("status = ?", model.StoragePoolActive)
	}
	if err = q.Count(&total).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to count storage pools", err)
		return
	}
	pools = []*model.StoragePool{}
	_, db = GetContextDB(ctx)
	q = dbs.Sortby(db.Offset(int(offset)).Limit(int(limit)), order).Scopes(dbs.Contains(query, "name"))
	if !memberShip.IsSystemAdmin() {
		q = q.Where("status = ?", model.StoragePoolActive)
	}
	if err = q.Find(&pools).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to query storage pools", err)
	}
	return
}

func validatePoolFields(name, media, fallbackGroup string, overRatio float64) (err error) {
	if name != "" && !poolNameRegex.MatchString(name) {
		return NewCLError(ErrInvalidParameter, "Pool name must start with a letter and contain 2-64 letters, digits, '_', '.' or '-'", nil)
	}
	if !validMedia[media] {
		return NewCLError(ErrInvalidParameter, "Media must be one of ssd, hdd, nvme or empty", nil)
	}
	if len(fallbackGroup) > 64 {
		return NewCLError(ErrInvalidParameter, "Fallback group is too long", nil)
	}
	if overRatio < 0 || overRatio > 20 {
		return NewCLError(ErrInvalidParameter, "Over ratio must be between 0 and 20", nil)
	}
	return
}

func (a *StoragePoolAdmin) Create(ctx context.Context, name, media, fallbackGroup string, overRatio float64, isDefault bool, description string) (pool *model.StoragePool, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckSystemPermission() {
		return nil, NewCLError(ErrPermissionDenied, "Not authorized to create storage pools", nil)
	}
	if err = validatePoolFields(name, media, fallbackGroup, overRatio); err != nil {
		return
	}
	if name == "" {
		return nil, NewCLError(ErrInvalidParameter, "Pool name is required", nil)
	}
	if overRatio == 0 {
		overRatio = 1
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	var count int64
	if err = db.Model(&model.StoragePool{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query storage pools", err)
	}
	if count > 0 {
		return nil, NewCLError(ErrInvalidParameter, fmt.Sprintf("Storage pool %s already exists", name), nil)
	}
	pool = &model.StoragePool{
		Model:         model.Model{Creater: memberShip.UserID},
		Name:          name,
		Driver:        model.StorageDriverLocal,
		Media:         media,
		FallbackGroup: strings.TrimSpace(fallbackGroup),
		Status:        model.StoragePoolActive,
		OverRatio:     overRatio,
		Description:   description,
	}
	if err = db.Create(pool).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to create storage pool", err)
	}
	pool.MountPath = path.Join(model.LocalPoolsDir, pool.UUID)
	if err = db.Model(pool).Update("mount_path", pool.MountPath).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to create storage pool", err)
	}
	if isDefault {
		if err = a.setDefault(db, pool); err != nil {
			return
		}
	}
	return
}

func (a *StoragePoolAdmin) setDefault(db *gorm.DB, pool *model.StoragePool) (err error) {
	if err = db.Model(&model.StoragePool{}).Where("id <> ?", pool.ID).Update("is_default", false).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to update storage pools", err)
	}
	if err = db.Model(pool).Update("is_default", true).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to update storage pool", err)
	}
	pool.IsDefault = true
	return
}

// StoragePoolUpdate carries the fields of a PATCH; nil leaves a field alone
type StoragePoolUpdate struct {
	Name          *string
	Media         *string
	FallbackGroup *string
	OverRatio     *float64
	Status        *string
	IsDefault     *bool
	Description   *string
}

func (a *StoragePoolAdmin) Update(ctx context.Context, pool *model.StoragePool, u *StoragePoolUpdate) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckSystemPermission() {
		return NewCLError(ErrPermissionDenied, "Not authorized to update storage pools", nil)
	}
	name, media, group, ratio := "", pool.Media, pool.FallbackGroup, pool.OverRatio
	if u.Name != nil {
		name = *u.Name
	}
	if u.Media != nil {
		media = *u.Media
	}
	if u.FallbackGroup != nil {
		group = strings.TrimSpace(*u.FallbackGroup)
	}
	if u.OverRatio != nil {
		ratio = *u.OverRatio
	}
	if err = validatePoolFields(name, media, group, ratio); err != nil {
		return
	}
	updates := map[string]interface{}{}
	if u.Name != nil && *u.Name != pool.Name {
		updates["name"] = *u.Name
	}
	if !pool.Builtin {
		if u.Media != nil {
			updates["media"] = media
		}
		if u.FallbackGroup != nil {
			updates["fallback_group"] = group
		}
		if u.OverRatio != nil {
			// 0 means "no over-commit", stored as 1 like Create does; dropping it would return 200 for no change
			if ratio <= 0 {
				ratio = 1
			}
			updates["over_ratio"] = ratio
		}
	} else if u.Media != nil || u.FallbackGroup != nil || u.OverRatio != nil {
		return NewCLError(ErrInvalidParameter, "Media, fallback group and over ratio of the built-in pool are fixed (it uses each host's disk_over_ratio)", nil)
	}
	if u.Status != nil {
		if *u.Status != model.StoragePoolActive && *u.Status != model.StoragePoolDisabled {
			return NewCLError(ErrInvalidParameter, "Status must be active or disabled", nil)
		}
		if pool.Builtin && *u.Status != model.StoragePoolActive {
			return NewCLError(ErrInvalidParameter, "The built-in pool can not be disabled", nil)
		}
		updates["status"] = *u.Status
	}
	if u.Description != nil {
		updates["description"] = *u.Description
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if name, ok := updates["name"]; ok {
		var count int64
		if err = db.Model(&model.StoragePool{}).Where("name = ? AND id <> ?", name, pool.ID).Count(&count).Error; err != nil {
			return NewCLError(ErrSQLSyntaxError, "Failed to query storage pools", err)
		}
		if count > 0 {
			return NewCLError(ErrInvalidParameter, fmt.Sprintf("Storage pool %s already exists", name), nil)
		}
	}
	if len(updates) > 0 {
		if err = db.Model(pool).Updates(updates).Error; err != nil {
			return NewCLError(ErrSQLSyntaxError, "Failed to update storage pool", err)
		}
	}
	if u.IsDefault != nil {
		if *u.IsDefault {
			err = a.setDefault(db, pool)
		} else if pool.IsDefault {
			// The built-in pool takes over as the default
			builtin, berr := BuiltinPool(ctx)
			if berr != nil {
				return berr
			}
			if builtin.ID == pool.ID {
				return NewCLError(ErrInvalidParameter, "Make another pool the default instead", nil)
			}
			err = a.setDefault(db, builtin)
		}
	}
	return
}

func (a *StoragePoolAdmin) Delete(ctx context.Context, pool *model.StoragePool) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckSystemPermission() {
		return NewCLError(ErrPermissionDenied, "Not authorized to delete storage pools", nil)
	}
	if pool.Builtin {
		return NewCLError(ErrInvalidParameter, "The built-in pool can not be deleted", nil)
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	var count int64
	if err = db.Unscoped().Model(&model.Volume{}).Where("storage_pool_id = ? AND deleted_at IS NULL", pool.ID).Count(&count).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to count volumes", err)
	}
	if count > 0 {
		return NewCLError(ErrStoragePoolInUse, fmt.Sprintf("Storage pool %s still has %d volumes", pool.Name, count), nil)
	}
	if err = db.Model(&model.HyperStoragePool{}).Where("pool_id = ?", pool.ID).Count(&count).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to count host pools", err)
	}
	if count > 0 {
		return NewCLError(ErrStoragePoolInUse, fmt.Sprintf("Storage pool %s is still set up on %d hosts", pool.Name, count), nil)
	}
	if pool.IsDefault {
		builtin, berr := BuiltinPool(ctx)
		if berr != nil {
			return berr
		}
		if err = a.setDefault(db, builtin); err != nil {
			return
		}
	}
	if err = db.Unscoped().Delete(pool).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to delete storage pool", err)
	}
	return
}
