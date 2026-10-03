/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Imported clusters (shared-storage-design.md §7.8, §8.8): set up and run by their own admins, used by CloudLand. The
// hosts given are checked (the storage is there and mounted) and recorded as clients; pools are registered on
// directories that exist; nothing of the storage itself is ever changed. Deleting an imported cluster only cleans
// what CloudLand put on the hosts.

import (
	"context"
	"encoding/json"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

const StorageTaskImport = "import"

// StorageClusterImport is a request to import a cluster
type StorageClusterImport struct {
	Kind        string
	Name        string
	Description string
	// The hosts that use the cluster: members of it already, set up by its admins
	Hostids []int32
	// Parameters of the import, read by the backend (gpfs: the file system and where it is mounted)
	Params json.RawMessage
}

// Import records an external cluster and starts the task checking its hosts
func (a *StorageClusterAdmin) Import(ctx context.Context, req *StorageClusterImport) (cluster *model.StorageCluster, task *model.StorageTask, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	if !storageClusterNameRe.MatchString(req.Name) {
		return nil, nil, planError("The cluster name starts with a letter and holds at most 63 letters, digits and . _ -")
	}
	if len(req.Description) > 256 {
		return nil, nil, planError("The description is at most 256 characters")
	}
	backend, err := storageBackendOf(req.Kind)
	if err != nil {
		return
	}
	if !backend.Capabilities().External {
		return nil, nil, planError("Importing %s clusters is not supported yet", req.Kind)
	}
	params, err := backend.ParseImportParams(req.Params)
	if err != nil {
		return
	}
	// Credentials of the import (a Ceph client key) are kept encrypted, apart from the parameters (§5.10)
	secrets := ""
	if s, ok := backend.(storageImportSecrets); ok {
		if err = SecretStoreReady(); err != nil {
			return nil, nil, NewCLError(ErrSecretUnavailable, "The credential key (VPN_SECRET_KEY) is not set, so the credentials of the cluster can not be stored", err)
		}
		var plain map[string]string
		params, plain = s.SplitImportSecrets(params)
		if secrets, err = encryptStorageSecrets(plain); err != nil {
			return nil, nil, NewCLError(ErrSecretUnavailable, "Failed to encrypt the credentials of the cluster", err)
		}
	}
	normalized, _ := json.Marshal(params)
	plan := &StoragePlan{Kind: req.Kind, SkipLayout: true}
	for _, h := range req.Hostids {
		plan.Nodes = append(plan.Nodes, &StorageNodePlan{Hostid: h, Roles: []string{model.StorageRoleClient}})
	}
	if _, _, err = checkStoragePlan(dbs.DBContext(ctx), plan); err != nil {
		return
	}
	cluster = &model.StorageCluster{Name: req.Name, Kind: req.Kind, Mode: model.StorageModeExternal, Status: model.StorageClusterDeploying,
		Health: model.StorageHealthUnknown, Params: string(normalized), Secrets: secrets, Description: req.Description}
	nodes := []*model.StorageClusterNode{}
	for _, n := range plan.Nodes {
		nodes = append(nodes, &model.StorageClusterNode{Hostid: n.Hostid, Roles: model.StorageRoleClient, Status: model.StorageNodeJoining})
	}
	steps, err := backend.TaskPlan(StorageTaskImport, cluster, &StorageTaskScope{Nodes: nodes})
	if err != nil {
		return
	}
	task, err = startStorageTask(ctx, &storageTaskSpec{Backend: req.Kind, Kind: StorageTaskImport, Plan: steps, Params: map[string]interface{}{},
		Prepare: func(tx *gorm.DB) (int64, error) {
			var taken int64
			tx.Model(&model.StorageCluster{}).Where("name = ?", req.Name).Count(&taken)
			if taken > 0 {
				return 0, planError("A storage cluster named %s exists already", req.Name)
			}
			if err := tx.Create(cluster).Error; err != nil {
				return 0, err
			}
			for _, n := range nodes {
				n.ClusterID = cluster.ID
				if err := tx.Create(n).Error; err != nil {
					return 0, err
				}
			}
			return cluster.ID, nil
		}})
	if err != nil {
		return nil, nil, err
	}
	return cluster, task, nil
}

// storageForgetStep removes what CloudLand put on a host of a cluster it stops using (the pool lists, the probes),
// leaving the storage alone: the deletion of an imported cluster
var storageForgetStep = &storageStepDef{Script: "stc_forget.sh", OnlineOnly: true,
	Input: func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		cluster, _, _, err := storageClusterOfTask(db, task)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"cluster_uuid": cluster.UUID, "kind": cluster.Kind}, nil
	}}

// storageImportFilesystem records the file system of an imported cluster, with the capacity its hosts saw
func storageImportFilesystem(tx *gorm.DB, task *model.StorageTask, cluster *model.StorageCluster, name, mountPoint string) error {
	fs := &model.StorageFilesystem{ClusterID: cluster.ID, Name: name, MountPoint: mountPoint, Status: "ready"}
	if res, err := storageStepResults(tx, task.ID, "check_import"); err == nil {
		for _, raw := range res {
			r := struct {
				CapacityBytes int64 `json:"capacity_bytes"`
				FreeBytes     int64 `json:"free_bytes"`
			}{}
			if json.Unmarshal(raw, &r) == nil && r.CapacityBytes > 0 {
				fs.CapacityBytes, fs.FreeBytes, fs.CapacityAt = r.CapacityBytes, r.FreeBytes, storageNow()
			}
		}
	}
	var existing int64
	tx.Model(&model.StorageFilesystem{}).Where("cluster_id = ? AND name = ?", cluster.ID, name).Count(&existing)
	if existing > 0 {
		return nil
	}
	return tx.Create(fs).Error
}

// storageImportFinish: the hosts that passed the check use the cluster
func storageImportFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool, fsName, mountPoint string) error {
	cluster, _, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	if !succeeded {
		return tx.Model(cluster).Update("status", model.StorageClusterError).Error
	}
	if fsName != "" {
		if err := storageImportFilesystem(tx, task, cluster, fsName, mountPoint); err != nil {
			return err
		}
	}
	if err := tx.Model(&model.StorageClusterNode{}).Where("cluster_id = ?", cluster.ID).Update("status", model.StorageNodeActive).Error; err != nil {
		return err
	}
	return tx.Model(cluster).Update("status", model.StorageClusterReady).Error
}
