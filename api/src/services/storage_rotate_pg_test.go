/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The rotation of the keys of a managed cluster (storage_cluster_keys.go), against PostgreSQL with the fake cland:
// the three passes of a new SSH key, the record following when the tools switch, the Ceph orchestrator switched in
// between, the client key made pending, given to every host and checked to be the key; refusals

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"

	"github.com/spf13/viper"
)

func TestStorageRotateKeysPG(t *testing.T) {
	f := newSharedFixture(t)
	ctx, db, h := f.ctx, f.db, f.hosts
	viper.Set("vpn.secret_key", "storage-gpfs-pg-test")
	if err := SecretStoreReady(); err != nil {
		t.Skipf("the credential store was set up without a key earlier in this test binary: %v", err)
	}
	pub, priv, err := newStorageSSHKey()
	must(t, err)
	must(t, db.Model(f.cluster).Updates(map[string]interface{}{"ssh_pub_key": pub, "ssh_priv_key": priv}).Error)
	for i, hh := range h {
		must(t, db.Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND hostid = ?", f.cluster.ID, hh).
			Update("attrs", fmt.Sprintf(`{"failure_group":%d,"host_key":"ssh-ed25519 KEY%d","hostname":"h%d"}`, i+1, hh, hh)).Error)
	}
	ok := func(sent []*stcSent) {
		for _, s := range sent {
			must(t, f.report(s, model.StorageRunSucceeded, "", "{}"))
		}
	}
	cluster := func(id int64) *model.StorageCluster {
		c := &model.StorageCluster{}
		must(t, db.Take(c, id).Error)
		return c
	}

	// GPFS has no client key; every member must be online
	_, err = StorageClusters.RotateKeys(ctx, f.cluster.UUID, &StorageRotateKeys{Client: true})
	wantCode(t, err, ErrStorageInvalidPlan, "no client key")
	f.setHostStatus(h[2], int(model.HyperStatusOffline))
	_, err = StorageClusters.RotateKeys(ctx, f.cluster.UUID, nil)
	wantCode(t, err, ErrStorageInvalidPlan, "offline")
	f.setHostStatus(h[2], 1)

	// Nothing named: the SSH key. Its three passes over every member
	task, err := StorageClusters.RotateKeys(ctx, f.cluster.UUID, nil)
	must(t, err)
	sent := f.expect("trust add", "stc_ssh_trust.sh", h...)
	newPub := ""
	for _, s := range sent {
		newPub, _ = s.input["public_key"].(string)
		if s.input["rotate"] != "add" || newPub == pub || !strings.HasPrefix(newPub, "ssh-ed25519 ") || s.input["private_key"] != nil || s.input["check"] != nil {
			t.Fatalf("add pass: %v", s.input)
		}
	}
	ok(sent)
	if c := cluster(f.cluster.ID); c.SSHPubKey != pub {
		t.Fatalf("the record switched before the admin hosts did")
	}
	sent = f.expect("switch", "stc_ssh_trust.sh", h...)
	for _, s := range sent {
		_, hasKey := s.input["private_key"]
		if s.input["rotate"] != "switch" || s.input["public_key"] != newPub || hasKey != (s.hostid == h[0]) {
			t.Fatalf("switch pass on %d: %v", s.hostid, s.input)
		}
		if s.hostid == h[0] {
			key, _ := s.input["private_key"].(string)
			check, _ := s.input["check"].([]interface{})
			if !strings.Contains(key, "OPENSSH PRIVATE KEY") || len(check) != 3 || len(s.input["known_hosts"].([]interface{})) != 3 {
				t.Fatalf("switch on the admin host: check %v", s.input["check"])
			}
		}
	}
	ok(sent)
	if c := cluster(f.cluster.ID); c.SSHPubKey != newPub || c.SSHPrivKey == priv {
		t.Fatalf("the record did not follow the switch")
	}
	sent = f.expect("drop", "stc_ssh_trust.sh", h...)
	for _, s := range sent {
		_, hasCheck := s.input["check"]
		if s.input["rotate"] != "drop" || s.input["private_key"] != nil || hasCheck != (s.hostid == h[0]) {
			t.Fatalf("drop pass on %d: %v", s.hostid, s.input)
		}
	}
	ok(sent)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")

	// Ceph: the orchestrator between the passes, then the client key
	ceph := &model.StorageCluster{Name: fmt.Sprintf("rot-pg-%d", time.Now().UnixNano()%1000000), Kind: model.StorageKindCeph,
		Mode: model.StorageModeManaged, Status: model.StorageClusterReady, Health: model.StorageHealthUnknown, SSHPubKey: newPub, SSHPrivKey: cluster(f.cluster.ID).SSHPrivKey,
		Attrs: `{"mon_addrs":["10.93.0.1:6789"]}`}
	oldClient := "AQBoldoldoldoldoldoldoldoldoldoldoldold=="
	ceph.Secrets, err = encryptStorageSecrets(map[string]string{"client_key": oldClient})
	must(t, err)
	must(t, db.Create(ceph).Error)
	t.Cleanup(func() {
		db.Unscoped().Where("cluster_id = ?", ceph.ID).Delete(&model.StorageClusterNode{})
		db.Unscoped().Delete(ceph)
	})
	// The fixture's GPFS members are given to the Ceph cluster for this part
	must(t, db.Model(&model.StorageClusterNode{}).Where("cluster_id = ?", f.cluster.ID).Update("status", model.StorageNodeLeaving).Error)
	for i, hh := range h {
		roles := []string{"admin,mon,mgr,osd", "mon,osd", "client"}[i]
		must(t, db.Create(&model.StorageClusterNode{ClusterID: ceph.ID, Hostid: hh, Roles: roles, Status: model.StorageNodeActive,
			Attrs: fmt.Sprintf(`{"host_key":"ssh-ed25519 KEY%d","hostname":"h%d"}`, hh, hh)}).Error)
	}
	task, err = StorageClusters.RotateKeys(ctx, ceph.UUID, &StorageRotateKeys{SSH: true, Client: true})
	must(t, err)
	ok(f.expect("trust add", "stc_ssh_trust.sh", h...))
	sent = f.expect("switch", "stc_ssh_trust.sh", h...)
	for _, s := range sent {
		if from, _ := s.input["from"].([]interface{}); len(from) != 1 || from[0] != "10.93.0.1" {
			t.Fatalf("the admin and mgr host log in: from %v", s.input["from"])
		}
	}
	ok(sent)
	if c := cluster(ceph.ID); c.SSHPubKey != newPub {
		t.Fatalf("the Ceph record switched before the orchestrator")
	}
	sent = f.expect("orchestrator key", "ceph_cluster.sh", h[0])
	in := sent[0].input
	hosts, _ := in["hosts"].([]interface{})
	cephPub, _ := in["public_key"].(string)
	if in["action"] != "cephadm_key" || cephPub == newPub || !strings.Contains(fmt.Sprint(in["private_key"]), "OPENSSH PRIVATE KEY") || len(hosts) != 2 ||
		!strings.Contains(fmt.Sprint(hosts), "10.93.0.2") || strings.Contains(fmt.Sprint(hosts), "10.93.0.3") {
		t.Fatalf("cephadm key input: %v", in)
	}
	ok(sent)
	if c := cluster(ceph.ID); c.SSHPubKey != cephPub {
		t.Fatalf("the record did not follow the orchestrator")
	}
	ok(f.expect("drop", "stc_ssh_trust.sh", h...))
	sent = f.expect("orchestrator check", "ceph_cluster.sh", h[0])
	if sent[0].input["action"] != "cephadm_check" || sent[0].input["public_key"] != cephPub {
		t.Fatalf("check input %v", sent[0].input)
	}
	ok(sent)
	sent = f.expect("pending key", "ceph_cluster.sh", h[0])
	if sent[0].input["action"] != "client_pending" || sent[0].input["client_user"] != "cloudland" || sent[0].input["client_key"] != nil {
		t.Fatalf("pending input %v", sent[0].input)
	}
	newClient := "AQBnewnewnewnewnewnewnewnewnewnewnewnew=="
	must(t, f.report(sent[0], model.StorageRunSucceeded, "", fmt.Sprintf(`{"client_key_pending":%q}`, newClient)))
	run := &model.StorageTaskRun{}
	must(t, db.Take(run, sent[0].run).Error)
	if strings.Contains(run.Result, newClient) {
		t.Fatalf("the pending key is stored in clear with the run: %s", run.Result)
	}
	secrets, err := storageClusterSecrets(cluster(ceph.ID))
	must(t, err)
	if secrets["client_key"] != oldClient || secrets["client_key_pending"] != newClient {
		t.Fatalf("secrets after the pending key: %v", secrets)
	}
	sent = f.expect("rekey", "ceph_client.sh", h...)
	for _, s := range sent {
		if s.input["action"] != "rekey" || s.input["client_key"] != newClient || s.input["secret_uuid"] != ceph.UUID {
			t.Fatalf("rekey input %v", s.input)
		}
	}
	ok(sent)
	// No host used the pending key yet when the commit looks: the old key is still the key until then
	if secrets, _ = storageClusterSecrets(cluster(ceph.ID)); secrets["client_key"] != oldClient {
		t.Fatalf("the old key went before the commit step")
	}
	sent = f.expect("commit", "ceph_cluster.sh", h[0])
	if sent[0].input["action"] != "client_commit" || sent[0].input["client_key"] != newClient {
		t.Fatalf("commit input %v", sent[0].input)
	}
	ok(sent)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")
	secrets, err = storageClusterSecrets(cluster(ceph.ID))
	must(t, err)
	if secrets["client_key"] != newClient || secrets["client_key_pending"] != "" {
		t.Fatalf("secrets after the commit: %v", secrets)
	}
	key, err := cephClientKey(cluster(ceph.ID))
	if err != nil || key != newClient {
		t.Fatalf("hosts set up later get %q (%v)", key, err)
	}

	// Aborted after a host checked the pending key: the cluster took it, the record takes it too
	rotateClient := func(pending string) *model.StorageTask {
		task, err := StorageClusters.RotateKeys(ctx, ceph.UUID, &StorageRotateKeys{Client: true})
		must(t, err)
		sent := f.expect("pending key", "ceph_cluster.sh", h[0])
		must(t, f.report(sent[0], model.StorageRunSucceeded, "", fmt.Sprintf(`{"client_key_pending":%q}`, pending)))
		return task
	}
	usedKey := "AQBusedusedusedusedusedusedusedusedused=="
	task = rotateClient(usedKey)
	for i, s := range f.expect("rekey", "ceph_client.sh", h...) {
		if i == 0 {
			must(t, f.report(s, model.StorageRunSucceeded, "", `{"health":"HEALTH_OK","key":"current"}`))
		} else {
			must(t, f.report(s, model.StorageRunFailed, "the cluster does not answer", ""))
		}
	}
	must(t, AbortStorageTask(ctx, task.ID))
	if secrets, _ = storageClusterSecrets(cluster(ceph.ID)); secrets["client_key"] != usedKey || secrets["client_key_pending"] != "" {
		t.Fatalf("aborted after a host used the pending key: %v", secrets)
	}
	// Aborted before any host checked it: the old key stays the key, the pending one is offered to a new host as a
	// fallback, and becomes the key when a host had to take it
	unusedKey := "AQBunusedunusedunusedunusedunusedunused=="
	task = rotateClient(unusedKey)
	for _, s := range f.expect("rekey", "ceph_client.sh", h...) {
		must(t, f.report(s, model.StorageRunFailed, "the cluster does not answer", ""))
	}
	must(t, AbortStorageTask(ctx, task.ID))
	if secrets, _ = storageClusterSecrets(cluster(ceph.ID)); secrets["client_key"] != usedKey || secrets["client_key_pending"] != unusedKey {
		t.Fatalf("aborted before any host used the pending key: %v", secrets)
	}
	setupTask := &model.StorageTask{ClusterID: ceph.ID}
	setupIn, err := cephClientInput("setup")(ctx, db, setupTask, nil, h[2])
	must(t, err)
	if m := setupIn.(map[string]interface{}); m["client_key"] != usedKey || m["client_key_alt"] != unusedKey {
		t.Fatalf("setup input after an aborted rotation: %v", m)
	}
	if setupIn, _ = cephClientInput("rekey")(ctx, db, setupTask, nil, h[2]); setupIn.(map[string]interface{})["client_key_alt"] != nil {
		t.Fatalf("the fallback key is offered to a rekey: %v", setupIn)
	}
	must(t, cephClientSetupDone(ctx, db, setupTask, nil, []*model.StorageTaskRun{{Result: `{"health":"HEALTH_OK","key":"current"}`}}))
	if secrets, _ = storageClusterSecrets(cluster(ceph.ID)); secrets["client_key"] != usedKey {
		t.Fatalf("a host on the recorded key changed the record: %v", secrets)
	}
	must(t, cephClientSetupDone(ctx, db, setupTask, nil, []*model.StorageTaskRun{{Result: `{"health":"HEALTH_OK","key":"alt"}`}}))
	if secrets, _ = storageClusterSecrets(cluster(ceph.ID)); secrets["client_key"] != unusedKey || secrets["client_key_pending"] != "" {
		t.Fatalf("a host had to take the pending key: %v", secrets)
	}
}
