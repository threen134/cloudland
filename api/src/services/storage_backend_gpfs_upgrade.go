/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Rolling upgrade of a managed GPFS cluster (shared-storage-design.md §7.7): the new installer goes to every host,
// then one host after the other has the instances that use the cluster moved off (drain), GPFS stopped, the new
// release installed, the portability layer built again and GPFS started, and its disks brought back before the next
// host goes. Raising the cluster and its file systems to the release (mmchconfig release=LATEST, mmchfs -V full) is
// irreversible and a task of its own, once every host runs the new release

import (
	"context"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

func init() {
	registerStorageTaskKind("gpfs:"+StorageTaskUpgrade, &storageTaskKind{Slot: storageSlotStructural, Finish: gpfsUpgradeFinish,
		Steps: map[string]*storageStepDef{
			"fetch_package": {Script: "stc_fetch.sh", Input: gpfsFetchInput},
			"drain":         {Control: storageDrainControl},
			"upgrade_node":  {Script: "gpfs_upgrade.sh", Input: gpfsUpgradeInput},
			"finalize":      {Script: "gpfs_cluster.sh", Input: gpfsFinalizeInput},
		}})
}

func (gpfsBackend) UpgradeParams(ctx context.Context, cluster *model.StorageCluster, req *StorageUpgrade) (*storageUpgradeParams, error) {
	if req.Image != "" {
		return nil, planError("GPFS clusters take no image: name the package of the new release")
	}
	if req.Finalize {
		if req.PackageUUID != "" {
			return nil, planError("Finalize alone, once every host runs the new release")
		}
		return &storageUpgradeParams{Finalize: true, Version: cluster.Version}, nil
	}
	if req.PackageUUID == "" {
		return nil, planError("Name the package of the new release")
	}
	pkg, err := StoragePackages.get(ctx, req.PackageUUID)
	if err != nil {
		return nil, err
	}
	if pkg.Kind != model.StorageKindGPFS || pkg.Status != model.StoragePackageReady {
		return nil, NewCLError(ErrStoragePackageState, "The package is not a verified gpfs package", nil)
	}
	if pkg.AcceptedBy == "" {
		return nil, NewCLError(ErrStoragePackageState, "Accept the license of the package first", nil)
	}
	if !storageVersionLess(cluster.Version, pkg.Version) {
		return nil, planError("The cluster runs %s: the package (%s) must be of a newer release", cluster.Version, pkg.Version)
	}
	// The edition is the license the cluster runs under
	db := dbs.DBContext(ctx)
	current := &model.StoragePackage{}
	if cluster.PackageID > 0 && db.Take(current, cluster.PackageID).Error == nil && current.Edition != "" && pkg.Edition != current.Edition {
		return nil, planError("The cluster runs the %s edition, the package is of the %s edition", current.Edition, pkg.Edition)
	}
	return &storageUpgradeParams{PackageID: pkg.ID, Version: pkg.Version}, nil
}

// gpfsUpgradePlan: the installer to every host, then drain and upgrade one host after the other, the hosts out of the
// quorum first; or the finalize step alone
func gpfsUpgradePlan(scope *StorageTaskScope, admins []int32) ([]*StorageStepPlan, error) {
	p := scope.Upgrade
	if p == nil {
		return nil, planError("The upgrade names no release")
	}
	if p.Finalize {
		return []*StorageStepPlan{{Name: "finalize", Scope: model.StorageStepScopeAdmin, Hostids: admins, Timeout: 2 * time.Hour}}, nil
	}
	plan := []*StorageStepPlan{{Name: "fetch_package", Scope: model.StorageStepScopeNodes, Hostids: gpfsHostsBy(scope.Nodes, "", ""), Timeout: 30 * time.Minute}}
	return append(plan, storageDrainPlan(storageDrainHosts(scope.Nodes, model.StorageRoleQuorum), "upgrade_node", 2*time.Hour)...), nil
}

// gpfsClusterFilesystems are the names of the file systems of a cluster
func gpfsClusterFilesystems(db *gorm.DB, clusterID int64) ([]string, error) {
	names := []string{}
	err := db.Model(&model.StorageFilesystem{}).Where("cluster_id = ?", clusterID).Order("id").Pluck("name", &names).Error
	return names, err
}

// gpfsUpgradeInput: the packages of the new release as the install step takes them, and the file systems whose disks
// must be up before GPFS stops on the host and are brought up after it starts again
func gpfsUpgradeInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	in, err := gpfsInstallInput(ctx, db, task, step, hostid)
	if err != nil {
		return nil, err
	}
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	fss, err := gpfsClusterFilesystems(db, cluster.ID)
	if err != nil {
		return nil, err
	}
	m := in.(map[string]interface{})
	m["cluster_uuid"], m["filesystems"] = cluster.UUID, fss
	return m, nil
}

func gpfsFinalizeInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	fss, err := gpfsClusterFilesystems(db, cluster.ID)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"action": "finalize", "cluster_uuid": cluster.UUID, "filesystems": fss}, nil
}

// gpfsUpgradeFinish: the cluster runs the package of the new release, which hosts joining later install. An aborted
// upgrade leaves the record on the release before: the hosts upgraded so far run the new one, which GPFS allows, and
// running the upgrade again finishes it (a host on the new release goes through quickly)
func gpfsUpgradeFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	p := storageUpgradeOf(task)
	if !succeeded || p.PackageID == 0 {
		return nil
	}
	return tx.Model(&model.StorageCluster{}).Where("id = ?", task.ClusterID).Updates(map[string]interface{}{"package_id": p.PackageID, "version": p.Version}).Error
}
