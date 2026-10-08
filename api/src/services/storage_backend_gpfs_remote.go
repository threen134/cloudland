/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// GPFS multi-cluster remote mounts (shared-storage-design.md §7.11): both clusters get a key pair (mmauth genkey),
// the owner lets the other cluster in with its key and grants it the file system (mmauth add, grant), the other
// cluster learns the owner with the owner's key and contact nodes and mounts the file system under its name and mount
// point on all its nodes (mmremotecluster add, mmremotefs add, mmmount -a). Unmounting goes the other way. The
// steps run on an admin host of each cluster in turn (gpfs_remote.sh); the keys come from the steps that read them.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"api/src/model"

	"gorm.io/gorm"
)

func init() {
	registerStorageTaskKind("gpfs:"+StorageTaskRemoteMount, &storageTaskKind{Slot: storageSlotStructural, Finish: storageRemoteMountFinish,
		Steps: map[string]*storageStepDef{
			"remote_owner_key":  {Script: "gpfs_remote.sh", Input: gpfsRemoteKeyInput(true)},
			"remote_access_key": {Script: "gpfs_remote.sh", Input: gpfsRemoteKeyInput(false)},
			"remote_grant":      {Script: "gpfs_remote.sh", Input: gpfsRemoteGrantInput, RetryFrom: "remote_owner_key"},
			"remote_mount":      {Script: "gpfs_remote.sh", Input: gpfsRemoteMountInput, RetryFrom: "remote_owner_key"},
		}})
	registerStorageTaskKind("gpfs:"+StorageTaskRemoteUnmount, &storageTaskKind{Slot: storageSlotStructural, Finish: storageRemoteUnmountFinish,
		Steps: map[string]*storageStepDef{
			"remote_owner_key":  {Script: "gpfs_remote.sh", Input: gpfsRemoteKeyInput(true)},
			"remote_access_key": {Script: "gpfs_remote.sh", Input: gpfsRemoteKeyInput(false)},
			"remote_unmount":    {Script: "gpfs_remote.sh", Input: gpfsRemoteUnmountInput, RetryFrom: "remote_owner_key"},
			"remote_revoke":     {Script: "gpfs_remote.sh", Input: gpfsRemoteRevokeInput, RetryFrom: "remote_owner_key"},
		}})
}

// RemoteMountPlan: the keys (and the GPFS names) of both clusters, the grant on the owner, the mount on the other
func (gpfsBackend) RemoteMountPlan(owner, access *model.StorageCluster, ownerNodes, accessNodes []*model.StorageClusterNode) ([]*StorageStepPlan, error) {
	oa, aa := gpfsWorkingAdmins(ownerNodes), gpfsWorkingAdmins(accessNodes)
	if len(oa) == 0 || len(aa) == 0 {
		return nil, planError("Both clusters need an admin host")
	}
	a := model.StorageStepScopeAdmin
	return []*StorageStepPlan{
		{Name: "remote_owner_key", Scope: a, Hostids: oa, Timeout: 10 * time.Minute},
		{Name: "remote_access_key", Scope: a, Hostids: aa, Timeout: 10 * time.Minute},
		{Name: "remote_grant", Scope: a, Hostids: oa, Timeout: 10 * time.Minute},
		{Name: "remote_mount", Scope: a, Hostids: aa, Timeout: 30 * time.Minute},
	}, nil
}

// RemoteUnmountPlan: the names of both clusters, the file system unmounted on the other cluster, the grant revoked
func (gpfsBackend) RemoteUnmountPlan(owner, access *model.StorageCluster, ownerNodes, accessNodes []*model.StorageClusterNode) ([]*StorageStepPlan, error) {
	oa, aa := gpfsWorkingAdmins(ownerNodes), gpfsWorkingAdmins(accessNodes)
	if len(oa) == 0 || len(aa) == 0 {
		return nil, planError("Both clusters need an admin host")
	}
	a := model.StorageStepScopeAdmin
	return []*StorageStepPlan{
		{Name: "remote_owner_key", Scope: a, Hostids: oa, Timeout: 10 * time.Minute},
		{Name: "remote_access_key", Scope: a, Hostids: aa, Timeout: 10 * time.Minute},
		{Name: "remote_unmount", Scope: a, Hostids: aa, Timeout: 30 * time.Minute},
		{Name: "remote_revoke", Scope: a, Hostids: oa, Timeout: 10 * time.Minute},
	}, nil
}

func gpfsRemoteKeyInput(ownerSide bool) storageStepInput {
	return func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		_, owner, access, err := storageRemoteMountOf(db, task)
		if err != nil {
			return nil, err
		}
		c := access
		if ownerSide {
			c = owner
		}
		return map[string]interface{}{"action": "key", "cluster_uuid": c.UUID}, nil
	}
}

// gpfsRemoteKey is what a key step read on its cluster: the GPFS name and the public key (base64)
type gpfsRemoteKey struct {
	ClusterName string `json:"cluster_name"`
	PublicKey   string `json:"public_key"`
}

func gpfsRemoteKeyOf(db *gorm.DB, task *model.StorageTask, step string) (*gpfsRemoteKey, error) {
	res, err := storageStepResults(db, task.ID, step)
	if err != nil {
		return nil, err
	}
	for _, raw := range res {
		k := &gpfsRemoteKey{}
		if json.Unmarshal(raw, k) == nil && k.ClusterName != "" && k.PublicKey != "" {
			return k, nil
		}
	}
	return nil, fmt.Errorf("step %s read no cluster name and key", step)
}

func gpfsRemoteGrantInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	mount, owner, _, err := storageRemoteMountOf(db, task)
	if err != nil {
		return nil, err
	}
	accessKey, err := gpfsRemoteKeyOf(db, task, "remote_access_key")
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"action": "grant", "cluster_uuid": owner.UUID, "remote_name": accessKey.ClusterName,
		"remote_key": accessKey.PublicKey, "filesystem": mount.Name}, nil
}

// gpfsRemoteContacts are the contact nodes of the owner the other cluster asks: its quorum hosts (at most 8)
func gpfsRemoteContacts(db *gorm.DB, owner *model.StorageCluster) ([]string, error) {
	nodes := []*model.StorageClusterNode{}
	if err := db.Where("cluster_id = ? AND status = ?", owner.ID, model.StorageNodeActive).Order("hostid").Find(&nodes).Error; err != nil {
		return nil, err
	}
	quorum := []int32{}
	for _, n := range nodes {
		if n.HasRole(model.StorageRoleQuorum) && len(quorum) < 8 {
			quorum = append(quorum, n.Hostid)
		}
	}
	ips, err := storageHostIPs(db, quorum)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, h := range quorum {
		out = append(out, ips[h])
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("storage cluster %s has no active quorum host to contact", owner.Name)
	}
	return out, nil
}

func gpfsRemoteMountInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	mount, owner, access, err := storageRemoteMountOf(db, task)
	if err != nil {
		return nil, err
	}
	ownerKey, err := gpfsRemoteKeyOf(db, task, "remote_owner_key")
	if err != nil {
		return nil, err
	}
	contacts, err := gpfsRemoteContacts(db, owner)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"action": "mount", "cluster_uuid": access.UUID, "remote_name": ownerKey.ClusterName,
		"remote_key": ownerKey.PublicKey, "contacts": contacts, "filesystem": mount.Name, "mount_point": mount.MountPoint}, nil
}

// gpfsRemoteUnmountInput: the other cluster forgets the owner too once it mounts nothing else of it
func gpfsRemoteUnmountInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	mount, owner, access, err := storageRemoteMountOf(db, task)
	if err != nil {
		return nil, err
	}
	ownerKey, err := gpfsRemoteKeyOf(db, task, "remote_owner_key")
	if err != nil {
		return nil, err
	}
	var others int64
	db.Model(&model.StorageRemoteMount{}).Where("owner_cluster_id = ? AND access_cluster_id = ? AND id <> ?", owner.ID, access.ID, mount.ID).Count(&others)
	return map[string]interface{}{"action": "unmount", "cluster_uuid": access.UUID, "remote_name": ownerKey.ClusterName,
		"filesystem": mount.Name, "forget_cluster": others == 0}, nil
}

// gpfsRemoteRevokeInput: the owner forgets the other cluster too once it grants it nothing else
func gpfsRemoteRevokeInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	mount, owner, access, err := storageRemoteMountOf(db, task)
	if err != nil {
		return nil, err
	}
	accessKey, err := gpfsRemoteKeyOf(db, task, "remote_access_key")
	if err != nil {
		return nil, err
	}
	var others int64
	db.Model(&model.StorageRemoteMount{}).Where("owner_cluster_id = ? AND access_cluster_id = ? AND id <> ?", owner.ID, access.ID, mount.ID).Count(&others)
	return map[string]interface{}{"action": "revoke", "cluster_uuid": owner.UUID, "remote_name": accessKey.ClusterName,
		"filesystem": mount.Name, "forget_cluster": others == 0}, nil
}
