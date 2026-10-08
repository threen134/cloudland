/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The rotation of the keys of a managed Ceph cluster (storage_cluster_keys.go): the SSH key, which the orchestrator
// keeps a copy of (both halves written with config-key set mgr/cephadm/ssh_identity_key|pub, then a mgr failover:
// cephadm set-priv-key / set-pub-key each pair the new half with the old one and keep the old key silently), and
// the key of client.cloudland, renewed through a pending key
// (ceph auth get-or-create-pending, Squid and later). The cluster takes both keys only until the pending key is first
// used: it then becomes the key and the old one is refused for new sessions (verified on 20.2, 2026-10-04). Sessions
// opened before keep working: their tickets renew without the key, across a mon restart too, so a running QEMU goes on
// with the old key and gets the new one from the libvirt secret when it starts again or migrates

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "api/src/common"
	"api/src/model"

	"gorm.io/gorm"
)

func init() {
	trust := func(pass string) storageStepInput {
		return storageRotateTrustInput(pass, model.StorageRoleAdmin, model.StorageRoleMgr)
	}
	registerStorageTaskKind("ceph:"+StorageTaskRotateKeys, &storageTaskKind{Slot: storageSlotStructural, Finish: storageRotateFinish,
		Steps: map[string]*storageStepDef{
			"trust_add":  {Script: "stc_ssh_trust.sh", Input: trust("add")},
			"ssh_switch": {Script: "stc_ssh_trust.sh", Input: trust("switch")},
			// The orchestrator logs in with its own copy of the key: the record follows when it takes the new one
			"cephadm_key":    {Script: "ceph_cluster.sh", Input: cephCephadmKeyInput, Done: storageRotateRecordKey},
			"trust_drop":     {Script: "stc_ssh_trust.sh", Input: trust("drop")},
			"cephadm_check":  {Script: "ceph_cluster.sh", Input: cephCephadmCheckInput},
			"client_pending": {Script: "ceph_cluster.sh", Input: cephClientKeyInput("client_pending", false), Done: cephClientPendingDone},
			"client_rekey":   {Script: "ceph_client.sh", Input: cephClientRekeyInput},
			"client_commit":  {Script: "ceph_cluster.sh", Input: cephClientKeyInput("client_commit", true), Done: cephClientCommitDone},
		}})
}

// cephRotatePlan: the SSH key in three passes with the orchestrator switched in between and checked once only the new
// key is taken; the client key made pending and given to every host at once (the first host that uses it makes it
// the key: a host still on the old one is refused new sessions for the seconds the step takes), then checked to be
// the key of the user
func cephRotatePlan(scope *StorageTaskScope, all, admins []int32) []*StorageStepPlan {
	a, n := model.StorageStepScopeAdmin, model.StorageStepScopeNodes
	plan := []*StorageStepPlan{}
	if scope.RotateSSH {
		plan = storageRotateSSHSteps(all, &StorageStepPlan{Name: "cephadm_key", Scope: a, Hostids: admins, Timeout: storageRotateStepTimeout})
		plan = append(plan, &StorageStepPlan{Name: "cephadm_check", Scope: a, Hostids: admins, Timeout: storageRotateStepTimeout})
	}
	if scope.RotateClient {
		plan = append(plan,
			&StorageStepPlan{Name: "client_pending", Scope: a, Hostids: admins, Timeout: 5 * time.Minute},
			&StorageStepPlan{Name: "client_rekey", Scope: n, Hostids: all, Timeout: storageRotateStepTimeout},
			&StorageStepPlan{Name: "client_commit", Scope: a, Hostids: admins, Timeout: 5 * time.Minute})
	}
	return plan
}

// cephCephadmHosts are the hosts the orchestrator manages, by the names it has them under
func cephCephadmHosts(db *gorm.DB, task *model.StorageTask, nodes []*model.StorageClusterNode) ([]map[string]interface{}, error) {
	ips, names, err := cephHostFacts(db, task, nodes)
	if err != nil {
		return nil, err
	}
	hosts := []map[string]interface{}{}
	for _, n := range nodes {
		if len(cephOrchLabels(n)) == 0 {
			continue
		}
		hosts = append(hosts, map[string]interface{}{"hostname": names[n.Hostid], "ip": ips[n.Hostid]})
	}
	return hosts, nil
}

func cephCephadmHostsInput(action string) storageStepInput {
	return func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		cluster, nodes, _, err := storageClusterOfTask(db, task)
		if err != nil {
			return nil, err
		}
		hosts, err := cephCephadmHosts(db, task, nodes)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"action": action, "cluster_uuid": cluster.UUID, "fsid": cephInfoOf(cluster).Fsid, "hosts": hosts}, nil
	}
}

// cephCephadmKeyInput: the new key pair for the orchestrator, which then checks it logs in to every host with it
func cephCephadmKeyInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	p, err := storageRotateParamsOf(task)
	if err != nil {
		return nil, err
	}
	in, err := cephCephadmHostsInput("cephadm_key")(ctx, db, task, step, hostid)
	if err != nil {
		return nil, err
	}
	priv, err := DecryptSecret(p.SSHPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("the new key can not be decrypted: %v", err)
	}
	m := in.(map[string]interface{})
	m["private_key"], m["public_key"] = priv, p.SSHPublicKey
	return m, nil
}

// cephCephadmCheckInput: the hosts and the key the orchestrator must use now, the one recorded when it switched
func cephCephadmCheckInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	in, err := cephCephadmHostsInput("cephadm_check")(ctx, db, task, step, hostid)
	if err != nil {
		return nil, err
	}
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	m := in.(map[string]interface{})
	m["public_key"] = cluster.SSHPubKey
	return m, nil
}

// cephClientKeyInput: an action on the client user, with its pending key when asked for
func cephClientKeyInput(action string, pending bool) storageStepInput {
	return func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		cluster, _, _, err := storageClusterOfTask(db, task)
		if err != nil {
			return nil, err
		}
		info := cephInfoOf(cluster)
		in := map[string]interface{}{"action": action, "cluster_uuid": cluster.UUID, "fsid": info.Fsid, "client_user": info.ClientUser}
		if pending {
			key, err := cephPendingClientKey(cluster)
			if err != nil {
				return nil, err
			}
			in["client_key"] = key
		}
		return in, nil
	}
}

// cephClientRekeyInput: the client configuration of a host with the pending key, which the cluster takes already
func cephClientRekeyInput(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
	in, err := cephClientInput("rekey")(ctx, db, task, step, hostid)
	if err != nil {
		return nil, err
	}
	cluster, _, _, err := storageClusterOfTask(db, task)
	if err != nil {
		return nil, err
	}
	key, err := cephPendingClientKey(cluster)
	if err != nil {
		return nil, err
	}
	m := in.(map[string]interface{})
	m["client_key"] = key
	return m, nil
}

func cephPendingClientKey(cluster *model.StorageCluster) (string, error) {
	secrets, err := storageClusterSecrets(cluster)
	if err != nil {
		return "", err
	}
	if !cephKeyRe.MatchString(secrets["client_key_pending"]) {
		return "", fmt.Errorf("the cluster has no pending client key")
	}
	return secrets["client_key_pending"], nil
}

// cephClientPendingDone keeps the pending key the cluster made with the secrets of the cluster, and drops it from the
// result of the run, which is stored in clear
func cephClientPendingDone(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
	cluster, _, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	for _, r := range runs {
		res := map[string]interface{}{}
		if json.Unmarshal([]byte(r.Result), &res) != nil {
			continue
		}
		key, _ := res["client_key_pending"].(string)
		if !cephKeyRe.MatchString(key) {
			return fmt.Errorf("the cluster made no pending client key")
		}
		secrets, err := storageClusterSecrets(cluster)
		if err != nil {
			return err
		}
		secrets["client_key_pending"] = key
		if err = storageClusterSaveSecrets(tx, cluster, secrets); err != nil {
			return err
		}
		delete(res, "client_key_pending")
		redacted, _ := json.Marshal(res)
		return tx.Model(&model.StorageTaskRun{}).Where("id = ?", r.ID).Update("result", string(redacted)).Error
	}
	return fmt.Errorf("the pending key step has no result")
}

// cephClientCommitDone: the pending key is the client key now, the old one is refused for new sessions
func cephClientCommitDone(ctx context.Context, tx *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, runs []*model.StorageTaskRun) error {
	cluster, _, _, err := storageClusterOfTask(tx, task)
	if err != nil {
		return err
	}
	secrets, err := storageClusterSecrets(cluster)
	if err != nil {
		return err
	}
	if !cephKeyRe.MatchString(secrets["client_key_pending"]) {
		return NewCLError(ErrSecretUnavailable, "The cluster has no pending client key", nil)
	}
	secrets["client_key"] = secrets["client_key_pending"]
	delete(secrets, "client_key_pending")
	return storageClusterSaveSecrets(tx, cluster, secrets)
}
