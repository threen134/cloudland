/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package model

import (
	"strings"
	"time"

	"api/src/dbs"

	"gorm.io/gorm"
)

// Storage clusters: GPFS or Ceph clusters CloudLand deploys on registered hosts (managed) or only uses (external).
// See docs/architecture/plan/shared-storage-design.md §5. The columns are what every kind has; what only one kind
// has goes into the json columns Params (chosen by the admin), Attrs (found out or generated) and Secrets
// (credentials, encrypted as a whole), read and written only by the backend of the kind (§4.5).

const (
	StorageKindGPFS = "gpfs"
	StorageKindCeph = "ceph"

	StorageModeManaged  = "managed"
	StorageModeExternal = "external"

	StorageLayoutReplica = "replica"
	StorageLayoutECE     = "ece"

	StorageClusterPlanning  = "planning"
	StorageClusterDeploying = "deploying"
	StorageClusterReady     = "ready"
	StorageClusterDegraded  = "degraded"
	StorageClusterError     = "error"
	StorageClusterDeleting  = "deleting"

	StorageHealthHealthy = "healthy"
	StorageHealthWarning = "warning"
	StorageHealthError   = "error"
	StorageHealthUnknown = "unknown"

	// Roles of a member host
	StorageRoleAdmin  = "admin"
	StorageRoleQuorum = "quorum"
	StorageRoleNSD    = "nsd"
	StorageRoleMon    = "mon"
	StorageRoleMgr    = "mgr"
	StorageRoleOSD    = "osd"
	StorageRoleClient = "client"

	StorageNodeJoining = "joining"
	StorageNodeActive  = "active"
	StorageNodeDown    = "down"
	StorageNodeLeaving = "leaving"
	StorageNodeError   = "error"

	StorageDiskRoleNSD = "nsd"
	StorageDiskRoleOSD = "osd"

	StorageDiskClaiming = "claiming"
	StorageDiskActive   = "active"
	StorageDiskDraining = "draining"
	StorageDiskRemoving = "removing"
	StorageDiskFailed   = "failed"
)

// StorageCluster is a GPFS or Ceph cluster in a region
type StorageCluster struct {
	Model
	Name       string `gorm:"type:varchar(64)"`
	Kind       string `gorm:"type:varchar(16)"` // gpfs | ceph
	Mode       string `gorm:"type:varchar(16)"` // managed | external
	Layout     string `gorm:"type:varchar(16)"` // gpfs: replica | ece; ceph: empty
	Status     string `gorm:"type:varchar(16)"`
	Health     string `gorm:"type:varchar(16)"`
	HealthInfo string `gorm:"type:text"` // json summary from the health watchdog
	HealthAt   *time.Time
	Version    string `gorm:"type:varchar(32)"`
	PackageID  int64  // storage_packages row the software came from, 0 = none (ceph installs from the distribution)
	ClusterRef string `gorm:"type:varchar(128)"` // the storage software's own id: gpfs cluster name and id, ceph fsid
	PublicNet  string `gorm:"type:varchar(64)"`
	ClusterNet string `gorm:"type:varchar(64)"`
	Params     string `gorm:"type:text"` // json: what the admin chose, kind specific (StorageBackend.ParseParams)
	Attrs      string `gorm:"type:text"` // json: what the backend found out or generated, e.g. ceph mon addresses
	Secrets    string `gorm:"type:text"` // json of kind specific credentials, encrypted as a whole (common.EncryptSecret)
	// Deployed on an OS or kernel outside the support matrix; no gorm default tag, false is meaningful
	Unsupported bool
	// Task slots (§6.2.1): a structural task and a pool task can run side by side; a failed task keeps its slot
	ActiveTask     int64
	ActivePoolTask int64
	PendingClients string `gorm:"type:varchar(1024)"` // json array of hostids waiting to join as clients
	AutoJoinZones  string `gorm:"type:varchar(256)"`  // json array of zone IDs whose new hosts join as clients
	SSHPubKey      string `gorm:"type:text"`
	SSHPrivKey     string `gorm:"type:text"` // encrypted (common.EncryptSecret)
	Description    string `gorm:"type:varchar(256)"`
}

// StorageClusterNode is a host in a storage cluster with its roles
type StorageClusterNode struct {
	Model
	ClusterID     int64  `gorm:"index"`
	Hostid        int32  `gorm:"index"`
	Roles         string `gorm:"type:varchar(128)"` // comma separated
	Attrs         string `gorm:"type:text"`         // json, kind specific, e.g. the gpfs failure group
	Status        string `gorm:"type:varchar(16)"`
	State         string `gorm:"type:varchar(64)"` // what the storage software reports
	Reason        string `gorm:"type:varchar(512)"`
	CheckedAt     *time.Time
	ReservedMemMB int32
}

func (n *StorageClusterNode) HasRole(role string) bool {
	for _, r := range strings.Split(n.Roles, ",") {
		if r == role {
			return true
		}
	}
	return false
}

// StorageClusterDisk is a disk claimed by a storage cluster: an NSD of GPFS or an OSD of Ceph
type StorageClusterDisk struct {
	Model
	ClusterID int64  `gorm:"index"`
	Hostid    int32  `gorm:"index"`
	DiskID    string `gorm:"type:varchar(256)"` // hyper_disks.disk_id
	// Identity checked on the host right before the disk is written (shared-storage-design.md §6.4)
	Serial string `gorm:"type:varchar(128)"`
	WWN    string `gorm:"type:varchar(64)"`
	Role   string `gorm:"type:varchar(16)"` // the disk role of the kind (StorageBackend.DiskRole): nsd | osd
	Name   string `gorm:"type:varchar(64)"` // the storage software's name for it: gpfs NSD name, ceph osd.N
	Media  string `gorm:"type:varchar(16)"`
	// File system the disk is in, for kinds with that layer (gpfs); 0 = none or not in one yet
	FsID      int64
	Attrs     string `gorm:"type:text"` // json, kind specific, e.g. gpfs usage and storage pool
	SizeBytes int64
	Status    string `gorm:"type:varchar(16)"`
	Reason    string `gorm:"type:varchar(512)"`
}

// StorageFilesystem is a file system of a cluster, for kinds that have that layer between the cluster and the
// CloudLand pools (gpfs)
type StorageFilesystem struct {
	Model
	ClusterID     int64  `gorm:"index"`
	Name          string `gorm:"type:varchar(32)"`
	MountPoint    string `gorm:"type:varchar(256)"`
	BlockSize     string `gorm:"type:varchar(8)"`
	DataReplicas  int32
	MetaReplicas  int32
	Status        string `gorm:"type:varchar(16)"`
	PolicyGen     int64
	CapacityBytes int64
	FreeBytes     int64
	CapacityAt    *time.Time
}

func init() {
	dbs.AutoMigrate(&StorageCluster{}, &StorageClusterNode{}, &StorageClusterDisk{}, &StorageFilesystem{})
	// Unique over the live rows only, so a removed node, disk or file system can come back
	dbs.AutoUpgrade("storage_cluster_unique_indexes_v1", func(db *gorm.DB) error {
		for _, stmt := range []string{
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_storage_cluster_name ON storage_clusters (name) WHERE deleted_at IS NULL",
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_storage_cluster_node ON storage_cluster_nodes (cluster_id, hostid) WHERE deleted_at IS NULL",
			// A disk belongs to one cluster at a time
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_storage_cluster_disk ON storage_cluster_disks (hostid, disk_id) WHERE deleted_at IS NULL",
			"CREATE UNIQUE INDEX IF NOT EXISTS uq_storage_filesystem ON storage_filesystems (cluster_id, name) WHERE deleted_at IS NULL",
		} {
			if err := db.Exec(stmt).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
