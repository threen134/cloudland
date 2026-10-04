/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Pool drivers (shared-storage-design.md §4.5.2): what clapi needs to know about the data path of a shared pool.
// Volumes of a file pool are qcow2 files on a shared file system (GPFS, later NFS and the like), volumes of a block
// pool are network block devices (Ceph RBD). The node side mirrors it: one script per operation (create, attach,
// resize, delete a volume) that loads scripts/kvm/storage/drivers/<driver>.sh and calls its drv_* functions. Local
// pools keep their own scripts and are not drivers here.

import (
	"encoding/json"
	"fmt"
	"path"

	. "api/src/common"
	"api/src/model"

	"gorm.io/gorm"
)

const (
	PoolFamilyFile  = "file"
	PoolFamilyBlock = "block"
)

// PoolDriver is the data path of the pools of a kind of storage
type PoolDriver interface {
	// Name is storage_pools.driver
	Name() string
	// Family is file or block
	Family() string
	// Format of the volumes: qcow2 for file pools, raw for RBD
	Format() string
	// VolumeRef is where a volume lives in its pool (volumes.path): a path relative to the pool root for file
	// pools, the image name for block pools
	VolumeRef(pool *model.StoragePool, volume *model.Volume) string
	// DriverArgs are what the node scripts get about the pool, besides the volume: the root and file system type
	// of a file pool, the pool name, configuration and user of an RBD pool
	DriverArgs(pool *model.StoragePool) map[string]interface{}
	// CapacityGroup names the pools that share one capacity: pools of a group are admitted together (§9.5). Empty
	// when the pool has a capacity of its own (a quota)
	CapacityGroup(pool *model.StoragePool) string
	// ImageBaseRef is where the base copy of an image is in a pool (image_storages.path, §9.6): a path relative to
	// the root of a file pool, the name of an RBD image whose snapshot base the boot disks are cloned from
	ImageBaseRef(pool *model.StoragePool, image *model.Image) string
	// QemuSource is a volume as QEMU tools read it on a host of the pool (capture): the file of a file pool, the
	// rbd: address of an RBD pool
	QemuSource(pool *model.StoragePool, volume *model.Volume) string
}

var poolDrivers = map[string]PoolDriver{}

func registerPoolDriver(d PoolDriver) {
	if _, dup := poolDrivers[d.Name()]; dup {
		panic("pool driver registered twice: " + d.Name())
	}
	poolDrivers[d.Name()] = d
}

// poolDriverOf returns the driver of a shared pool
func poolDriverOf(pool *model.StoragePool) (PoolDriver, error) {
	if d := poolDrivers[pool.Driver]; d != nil && pool.Shared() {
		return d, nil
	}
	return nil, NewCLError(ErrStoragePoolInvalidState, fmt.Sprintf("Storage pool %s has no shared pool driver (%s)", pool.Name, pool.Driver), nil)
}

// poolDriverParams decodes the driver parameters of a pool
func poolDriverParams(pool *model.StoragePool) map[string]interface{} {
	params := map[string]interface{}{}
	_ = json.Unmarshal([]byte(pool.DriverParams), &params)
	return params
}

func stringParam(params map[string]interface{}, key string) string {
	s, _ := params[key].(string)
	return s
}

// fileDriver is the common part of the drivers of file pools: the pool is a directory of a shared file system,
// mounted at the same place on every host (storage_pools.mount_path), with volumes/, images/, nvram/ and tmp/ in it
type fileDriver struct {
	name   string
	fsType string // what stat -f -c %T says for the file system, checked before anything is written
}

func (d fileDriver) Name() string   { return d.name }
func (d fileDriver) Family() string { return PoolFamilyFile }
func (d fileDriver) Format() string { return "qcow2" }

func (d fileDriver) VolumeRef(pool *model.StoragePool, volume *model.Volume) string {
	return fmt.Sprintf("volumes/volume-%d.disk", volume.ID)
}

func (d fileDriver) DriverArgs(pool *model.StoragePool) map[string]interface{} {
	return map[string]interface{}{"driver": d.name, "pool": pool.UUID, "root": pool.MountPath, "fs_type": d.fsType}
}

func (d fileDriver) CapacityGroup(pool *model.StoragePool) string {
	return ""
}

func (d fileDriver) ImageBaseRef(pool *model.StoragePool, image *model.Image) string {
	return "images/" + image.FileBase() + ".qcow2"
}

func (d fileDriver) QemuSource(pool *model.StoragePool, volume *model.Volume) string {
	return VolumeAbsPath(pool, volume)
}

// gpfsDriver keeps the volumes of a pool in an independent fileset. Pools without a quota in the same GPFS storage
// pool of one file system draw on the same disks (§9.5)
type gpfsDriver struct{ fileDriver }

func (d gpfsDriver) CapacityGroup(pool *model.StoragePool) string {
	if pool.QuotaBytes > 0 {
		return ""
	}
	return fmt.Sprintf("gpfs/%d/%s", pool.FilesystemID, stringParam(poolDriverParams(pool), "gpfs_pool"))
}

func init() {
	registerPoolDriver(gpfsDriver{fileDriver{name: model.StorageDriverGPFS, fsType: "gpfs"}})
}

// sharedVolumeArgMap is what the scripts of a shared pool get about a volume: the pool, plus the volume
func sharedVolumeArgMap(pool *model.StoragePool, d PoolDriver, volume *model.Volume) map[string]interface{} {
	args := d.DriverArgs(pool)
	args["volume_id"] = volume.ID
	args["size_gb"] = volume.Size
	if d.Family() == PoolFamilyFile {
		args["path"] = path.Join(pool.MountPath, volume.Path)
	} else {
		args["image"] = volume.Path
	}
	return args
}

// sharedVolumeArgs is the JSON the volume scripts of a shared pool read on stdin: the pool, plus the volume
func sharedVolumeArgs(pool *model.StoragePool, volume *model.Volume, extra map[string]interface{}) (string, error) {
	d, err := poolDriverOf(pool)
	if err != nil {
		return "", err
	}
	args := sharedVolumeArgMap(pool, d, volume)
	for k, v := range extra {
		args[k] = v
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "", NewCLError(ErrJSONMarshalFailed, "Failed to encode the volume arguments", err)
	}
	return string(b), nil
}

// sharedPoolEntry is a pool as the hosts of its cluster list it (sync_shared_pools.sh, §9.2)
func sharedPoolEntry(pool *model.StoragePool) map[string]interface{} {
	d, err := poolDriverOf(pool)
	if err != nil {
		return nil
	}
	return d.DriverArgs(pool)
}

// pickPoolHost chooses the host that runs an operation on a shared pool which needs no particular host (create,
// delete, resize a detached volume): one where the pool is ready and that is online, the one that reported last
// first. cland's select= is not used: it picks by CPU and memory and may answer error=resource (§9.3)
func pickPoolHost(db *gorm.DB, pool *model.StoragePool) (int32, error) {
	rows := []*model.HyperStoragePool{}
	if err := db.Where("pool_id = ? AND status = ?", pool.ID, model.HyperPoolReady).Order("checked_at DESC NULLS LAST, hostid").Find(&rows).Error; err != nil {
		return 0, NewCLError(ErrSQLSyntaxError, "Failed to query the hosts of the pool", err)
	}
	for _, r := range rows {
		if _, online := hostOnline(db, r.Hostid); online {
			return r.Hostid, nil
		}
	}
	return 0, NewCLError(ErrStoragePoolUnavailable, fmt.Sprintf("No online host can reach storage pool %s now", pool.Name), nil)
}
