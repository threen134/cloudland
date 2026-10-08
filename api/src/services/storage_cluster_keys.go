/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Renewing the credentials of a managed cluster (shared-storage-design.md §6.6, §8.4, §16 S6): the SSH key its admin
// hosts log in to the members with, and the key of the Ceph client user QEMU and the scripts use. Neither is ever
// missing for a moment:
//   - the new SSH key is taken by every member before the admin hosts use it, and the old one is refused only once
//     they do (stc_ssh_trust.sh, three passes); the record of the cluster follows when its tools switch
//   - the new client key is made pending on the cluster and given to every host at once; the first use makes it the
//     key. A QEMU running before keeps its session (Ceph renews its tickets without the key) and takes the new key
//     from the libvirt secret when it starts again or migrates

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

const (
	StorageTaskRotateKeys    = "rotate_keys"
	storageRotateStepTimeout = 10 * time.Minute
)

// StorageRotateKeys is what a rotation renews; both when neither is named
type StorageRotateKeys struct {
	SSH    bool `json:"ssh"`
	Client bool `json:"client"`
}

// storageRotateParams are the parameters of a rotation: the new SSH key, its private part encrypted like the one of
// the cluster. The new client key is made on the cluster and kept with its secrets (client_key_pending)
type storageRotateParams struct {
	SSH           bool   `json:"ssh"`
	Client        bool   `json:"client"`
	SSHPublicKey  string `json:"ssh_public_key,omitempty"`
	SSHPrivateKey string `json:"ssh_private_key,omitempty"`
}

// RotateKeys starts the rotation of the credentials of a managed cluster. Every member must be online: a host away
// would keep the old keys and lose the cluster when they go
func (a *StorageClusterAdmin) RotateKeys(ctx context.Context, uuid string, req *StorageRotateKeys) (*model.StorageTask, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	cluster, nodes, disks, backend, err := storageClusterForChange(ctx, uuid, func(c *StorageCapabilities) bool { return c.RotateKeys })
	if err != nil {
		return nil, err
	}
	if req == nil || (!req.SSH && !req.Client) {
		req = &StorageRotateKeys{SSH: true, Client: backend.Capabilities().ClientKey}
	}
	if req.Client && !backend.Capabilities().ClientKey {
		return nil, planError("%s clusters have no client key", cluster.Kind)
	}
	db := dbs.DBContext(ctx)
	for _, n := range nodes {
		if n.Status != model.StorageNodeActive {
			return nil, planError("%s is %s in the cluster: every member takes the new keys", hostName(db, n.Hostid), n.Status)
		}
		if _, online := hostOnline(db, n.Hostid); !online {
			return nil, planError("%s is offline: every member takes the new keys", hostName(db, n.Hostid))
		}
	}
	params := &storageRotateParams{SSH: req.SSH, Client: req.Client}
	if req.SSH {
		if params.SSHPublicKey, params.SSHPrivateKey, err = newStorageSSHKey(); err != nil {
			return nil, err
		}
	}
	steps, err := backend.TaskPlan(StorageTaskRotateKeys, cluster, &StorageTaskScope{Nodes: nodes, Disks: disks, RotateSSH: req.SSH, RotateClient: req.Client})
	if err != nil {
		return nil, err
	}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskRotateKeys, Plan: steps, Params: params})
}

func storageRotateParamsOf(task *model.StorageTask) (*storageRotateParams, error) {
	p := &storageRotateParams{}
	if err := json.Unmarshal([]byte(task.Params), p); err != nil {
		return nil, fmt.Errorf("the task has no rotation parameters")
	}
	if p.SSH && (p.SSHPublicKey == "" || p.SSHPrivateKey == "") {
		return nil, fmt.Errorf("the task has no new SSH key")
	}
	return p, nil
}

// storageRotateTrustInput is the input of a pass of stc_ssh_trust.sh with the new key (add, switch, drop). The admin
// hosts check they log in to every member with it once it is taken everywhere (switch, drop)
func storageRotateTrustInput(pass string, roles ...string) storageStepInput {
	return func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		p, err := storageRotateParamsOf(task)
		if err != nil {
			return nil, err
		}
		in, err := storageTrustInputKey(db, task, hostid, roles, p.SSHPublicKey, p.SSHPrivateKey)
		if err != nil {
			return nil, err
		}
		in["rotate"] = pass
		_, admin := in["private_key"]
		if pass != "switch" {
			// The private key changes in the switch only
			delete(in, "private_key")
			delete(in, "known_hosts")
		}
		if admin && pass != "add" {
			_, nodes, _, err := storageClusterOfTask(db, task)
			if err != nil {
				return nil, err
			}
			ips, err := storageHostIPs(db, nodeHostids(nodes, ""))
			if err != nil {
				return nil, err
			}
			check := []string{}
			for _, n := range nodes {
				check = append(check, ips[n.Hostid])
			}
			in["check"] = check
		}
		return in, nil
	}
}

// storageRotateRecordKey is the Done of the step that makes the tools of the cluster use the new SSH key: the record
// of the cluster takes it there, so a task that writes the trust afterwards (adding a host) writes the new one
func storageRotateRecordKey(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
	p, err := storageRotateParamsOf(task)
	if err != nil {
		return err
	}
	return tx.Model(&model.StorageCluster{}).Where("id = ?", task.ClusterID).
		Updates(map[string]interface{}{"ssh_pub_key": p.SSHPublicKey, "ssh_priv_key": p.SSHPrivateKey}).Error
}

// storageRotateSSHSteps are the three passes of a new SSH key over every member (§6.6), with the step that switches
// the tools of the kind in between when it has one (the orchestrator of Ceph keeps its own copy of the key)
func storageRotateSSHSteps(all []int32, between ...*StorageStepPlan) []*StorageStepPlan {
	n := model.StorageStepScopeNodes
	plan := []*StorageStepPlan{
		{Name: "trust_add", Scope: n, Hostids: all, Timeout: storageRotateStepTimeout},
		{Name: "ssh_switch", Scope: n, Hostids: all, Timeout: storageRotateStepTimeout},
	}
	plan = append(plan, between...)
	return append(plan, &StorageStepPlan{Name: "trust_drop", Scope: n, Hostids: all, Timeout: storageRotateStepTimeout})
}

// storageRotateFinish: an aborted rotation leaves the cluster on the SSH key its record names, which the members all
// take (the second line of the new key goes with the next trust written, or when a host leaves). The client key
// depends on how far it went: once a host checked the pending key (a client_rekey run succeeded) the cluster took it
// as the key and refuses the old one for new sessions, so the record takes it too; the hosts that did not take it
// get it with the next rotation. Before that the pending key stays pending and the old one is the key. A host may
// still have used it in a run that failed: the client setup of a new host then falls back to it (cephClientInput)
func storageRotateFinish(ctx context.Context, tx *gorm.DB, task *model.StorageTask, succeeded bool) error {
	if succeeded {
		return nil
	}
	logger.Ctx(ctx).Warningf("Rotation %d of the keys of storage cluster %d aborted", task.ID, task.ClusterID)
	p, err := storageRotateParamsOf(task)
	if err != nil || !p.Client {
		return nil
	}
	var used int64
	if err = tx.Model(&model.StorageTaskRun{}).Joins("JOIN storage_task_steps ON storage_task_steps.id = storage_task_runs.step_id").
		Where("storage_task_steps.task_id = ? AND storage_task_steps.name = ? AND storage_task_runs.status = ?", task.ID, "client_rekey",
			model.StorageRunSucceeded).Count(&used).Error; err != nil {
		return err
	}
	if used == 0 {
		return nil
	}
	cluster, _, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	secrets, err := storageClusterSecrets(cluster)
	if err != nil {
		return err
	}
	if !cephKeyRe.MatchString(secrets["client_key_pending"]) {
		return nil
	}
	secrets["client_key"] = secrets["client_key_pending"]
	delete(secrets, "client_key_pending")
	logger.Ctx(ctx).Warningf("Storage cluster %d took the new client key before rotation %d was aborted: the hosts that did not take it "+
		"open no new session until the keys are rotated again", task.ClusterID, task.ID)
	return storageClusterSaveSecrets(tx, cluster, secrets)
}

// storageClusterSaveSecrets encrypts the secrets of a cluster back into its record
func storageClusterSaveSecrets(tx *gorm.DB, cluster *model.StorageCluster, secrets map[string]string) error {
	enc, err := encryptStorageSecrets(secrets)
	if err != nil {
		return err
	}
	if err = tx.Model(&model.StorageCluster{}).Where("id = ?", cluster.ID).Update("secrets", enc).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to save the secrets of the cluster", err)
	}
	cluster.Secrets = enc
	return nil
}
