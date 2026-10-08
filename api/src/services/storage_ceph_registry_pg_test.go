/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Ceph against PostgreSQL with the fake cland (shared-storage-design.md §8): a private registry (its password kept
// encrypted with the cluster, given to the cephadm hosts that pull and to the bootstrap), a client only host on a newer
// distribution than the daemon hosts, and mons joining: the mons are read again and every member's client
// configuration names them. The plans of the other mon changes (change of roles, a mon host leaving) are unit tests.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"

	"github.com/spf13/viper"
)

func TestStorageCephRegistryMonsPG(t *testing.T) {
	f := newStcFixture(t)
	ctx, db := f.ctx, f.db
	viper.Set("vpn.secret_key", "storage-ceph-pg-test")
	if err := SecretStoreReady(); err != nil {
		t.Skipf("the credential store was set up without a key earlier in this test binary: %v", err)
	}
	h := stcHosts
	db.Unscoped().Where("hostid = ? AND disk_id = ?", h[0], "wwn-0x5000cec0").Delete(&model.HyperDisk{})
	must(t, db.Create(&model.HyperDisk{Hostid: h[0], DiskID: "wwn-0x5000cec0", Name: "sdc", Serial: "CRG0", SizeBytes: 2 << 40, Media: "hdd",
		State: model.DiskFree, ScannedAt: time.Now()}).Error)
	name := fmt.Sprintf("ceph-reg-%d", time.Now().UnixNano()%1000000)
	const image = "reg.example.com:5000/ceph/ceph:v19.2.3"
	plan := func(params string) *StoragePlan {
		return &StoragePlan{Kind: "ceph", Params: json.RawMessage(params),
			Nodes: []*StorageNodePlan{{Hostid: h[0], Roles: []string{"admin", "mon", "mgr", "osd"}}, {Hostid: h[3], Roles: []string{"client"}}},
			Disks: []*StorageDiskPlan{{Hostid: h[0], DiskID: "wwn-0x5000cec0"}}}
	}
	// A registry login comes whole and with an image of that registry
	_, _, err := StorageClusters.Create(ctx, &StorageClusterCreate{Name: name, Plan: plan(`{"test":true,"image":"` + image + `","registry":"reg.example.com:5000","registry_user":"cl"}`)})
	wantCodeMsg(t, err, ErrStorageInvalidPlan, "password")
	_, _, err = StorageClusters.Create(ctx, &StorageClusterCreate{Name: name, Plan: plan(`{"test":true,"image":"quay.io/ceph/ceph:v19.2.3","registry":"reg.example.com:5000","registry_user":"cl","registry_password":"x"}`)})
	wantCodeMsg(t, err, ErrStorageInvalidPlan, "image of the daemons from it")
	_, _, err = StorageClusters.Create(ctx, &StorageClusterCreate{Name: name, Plan: plan(`{"test":true,"image":"` + image + `","registry":"Reg Example","registry_user":"cl","registry_password":"x"}`)})
	wantCodeMsg(t, err, ErrStorageInvalidPlan, "host[:port]")

	cluster, task, err := StorageClusters.Create(ctx, &StorageClusterCreate{Name: name, Plan: plan(`{"test":true,"replicas":1,"image":"` + image +
		`","registry":"reg.example.com:5000","registry_user":"cl","registry_password":"p@ss w0rd"}`)})
	must(t, err)
	defer func() {
		db.Unscoped().Where("cluster_id = ?", cluster.ID).Delete(&model.StorageClusterDisk{})
		db.Unscoped().Where("cluster_id = ?", cluster.ID).Delete(&model.StorageClusterNode{})
		db.Unscoped().Where("id = ?", cluster.ID).Delete(&model.StorageCluster{})
	}()
	c := &model.StorageCluster{}
	must(t, db.Take(c, cluster.ID).Error)
	if strings.Contains(c.Params, "p@ss") || !strings.Contains(c.Params, `"registry":"reg.example.com:5000"`) || !strings.HasPrefix(c.Secrets, "v1:") {
		t.Fatalf("params %s secrets %q", c.Params, c.Secrets)
	}
	if secrets, err := storageClusterSecrets(c); err != nil || secrets["registry_password"] != "p@ss w0rd" {
		t.Fatalf("secrets %v %v", secrets, err)
	}
	succeed := func(sent []*stcSent, result func(s *stcSent) string) {
		for _, s := range sent {
			must(t, f.report(s, model.StorageRunSucceeded, "", result(s)))
		}
	}
	none := func(*stcSent) string { return "{}" }

	// The client only host runs a newer distribution than the daemon host
	succeed(f.expect("precheck", "stc_precheck.sh", h[0], h[3]), func(s *stcSent) string {
		if s.hostid == h[3] {
			return cephPrecheckResult("ubuntu 26.04")(s)
		}
		return cephPrecheckResult("ubuntu 24.04")(s)
	})
	succeed(f.expect("join", "stc_join.sh", h[0], h[3]), none)
	// The cephadm host logs in to the registry to pull, the client host does not pull
	sent := f.expect("install", "ceph_install.sh", h[0], h[3])
	for _, s := range sent {
		reg, _ := s.input["registry"].(map[string]interface{})
		if s.hostid == h[3] {
			if reg != nil {
				t.Fatalf("the client host gets the registry login: %v", s.input)
			}
		} else if reg["url"] != "reg.example.com:5000" || reg["username"] != "cl" || reg["password"] != "p@ss w0rd" || s.input["image"] != image {
			t.Fatalf("install input of the cephadm host %v", s.input)
		}
	}
	// A client older than the daemons is refused; it runs the newer Ceph of its distribution instead
	clientVersion := "18.2.4"
	install := func(s *stcSent) string {
		if s.hostid == h[3] {
			return fmt.Sprintf(`{"version":%q,"image":""}`, clientVersion)
		}
		return `{"version":"19.2.3","image":"` + image + `","image_version":"19.2.3"}`
	}
	succeed(sent, install)
	f.wantTask(task.ID, model.StorageTaskFailed, "older than the 19.2.3 the daemons run")
	must(t, RetryStorageTask(ctx, task.ID))
	clientVersion = "20.2.0"
	succeed(f.expect("install again", "ceph_install.sh", h[0], h[3]), install)
	succeed(f.expect("trust", "stc_ssh_trust.sh", h[0], h[3]), none)
	sent = f.expect("bootstrap", "ceph_cluster.sh", h[0])
	if reg, _ := sent[0].input["registry"].(map[string]interface{}); reg["password"] != "p@ss w0rd" || sent[0].input["image"] != image {
		t.Fatalf("bootstrap input %v", sent[0].input)
	}
	succeed(sent, none)
	succeed(f.expect("add hosts", "ceph_cluster.sh", h[0]), none)
	succeed(f.expect("configure", "ceph_cluster.sh", h[0]), func(*stcSent) string {
		return fmt.Sprintf(`{"client_key":%q,"mon_addrs":["10.93.0.1"]}`, cephTestKey)
	})
	succeed(f.expect("resolve", "stc_resolve_disks.sh", h[0]), func(*stcSent) string {
		return `{"disks":[{"id":"wwn-0x5000cec0","path":"/dev/sdc","name":"sdc"}]}`
	})
	sent = f.expect("osds", "ceph_cluster.sh", h[0])
	succeed(sent, func(*stcSent) string {
		key := sent[0].input["osds"].([]interface{})[0].(map[string]interface{})["key"]
		return fmt.Sprintf(`{"osds":[{"key":%q,"osd_id":0}]}`, key)
	})
	succeed(f.expect("clients", "ceph_client.sh", h[0], h[3]), none)
	succeed(f.expect("finish", "stc_finish.sh", h[0], h[3]), none)
	f.wantTask(task.ID, model.StorageTaskSucceeded, "")

	// Two mon hosts join: a daemon host on another distribution is refused
	atask, err := StorageClusters.AddNodes(ctx, cluster.UUID, &StorageClusterExpand{Nodes: []*StorageNodePlan{
		{Hostid: h[1], Roles: []string{"mon"}}, {Hostid: h[2], Roles: []string{"mon"}}}})
	must(t, err)
	succeed(f.expect("precheck of the mons", "stc_precheck.sh", h[1], h[2]), func(s *stcSent) string {
		if s.hostid == h[1] {
			return cephPrecheckResult("ubuntu 26.04")(s)
		}
		return cephPrecheckResult("ubuntu 24.04")(s)
	})
	f.wantTask(atask.ID, model.StorageTaskFailed, "daemon hosts")
	must(t, RetryStorageTask(ctx, atask.ID))
	succeed(f.expect("precheck again", "stc_precheck.sh", h[1], h[2]), cephPrecheckResult("ubuntu 24.04"))
	succeed(f.expect("join", "stc_join.sh", h[1], h[2]), none)
	sent = f.expect("install of the mons", "ceph_install.sh", h[1], h[2])
	for _, s := range sent {
		if reg, _ := s.input["registry"].(map[string]interface{}); reg["password"] != "p@ss w0rd" {
			t.Fatalf("install input of a new cephadm host %v", s.input)
		}
	}
	succeed(sent, func(*stcSent) string { return `{"version":"19.2.3","image":"` + image + `","image_version":"19.2.3"}` })
	succeed(f.expect("trust", "stc_ssh_trust.sh", h...), none)
	succeed(f.expect("add hosts", "ceph_cluster.sh", h[0]), none)
	// The mons as they are now, then the client configuration of the members there before
	sent = f.expect("mon addresses", "ceph_cluster.sh", h[0])
	if sent[0].input["action"] != "mon_addrs" || sent[0].input["fsid"] != cluster.UUID {
		t.Fatalf("mon_addrs input %v", sent[0].input)
	}
	succeed(sent, func(*stcSent) string { return `{"mon_addrs":["10.93.0.1","10.93.0.2","10.93.0.3"]}` })
	must(t, db.Take(c, cluster.ID).Error)
	if fmt.Sprint(cephInfoOf(c).MonAddrs) != "[10.93.0.1 10.93.0.2 10.93.0.3]" {
		t.Fatalf("mon addresses after the change %v", cephInfoOf(c).MonAddrs)
	}
	for _, label := range []string{"refresh of the members", "setup of the new hosts"} {
		hosts := []int32{h[0], h[3]}
		if label == "setup of the new hosts" {
			hosts = []int32{h[1], h[2]}
		}
		sent = f.expect(label, "ceph_client.sh", hosts...)
		for _, s := range sent {
			if s.input["action"] != "setup" || len(s.input["mon_addrs"].([]interface{})) != 3 || s.input["client_key"] != cephTestKey {
				t.Fatalf("%s: input %v", label, s.input)
			}
		}
		succeed(sent, none)
	}
	succeed(f.expect("finish", "stc_finish.sh", h[1], h[2]), none)
	f.wantTask(atask.ID, model.StorageTaskSucceeded, "")

	// Hosts and disks only, no mon: no refresh
	if cephMonsChange(StorageTaskAddNodes, &StorageTaskScope{Nodes: []*model.StorageClusterNode{{Hostid: h[3], Roles: "client",
		Status: model.StorageNodeJoining}}}) {
		t.Fatal("a client joining changes the mons")
	}
}

// The plans of the tasks that change the mons in other ways: a host gains or loses the mon role, a mon host leaves
func TestStorageCephMonsChangePlan(t *testing.T) {
	cluster := &model.StorageCluster{Kind: "ceph", Mode: model.StorageModeManaged, Params: `{}`}
	node := func(h int32, roles string, status string) *model.StorageClusterNode {
		return &model.StorageClusterNode{Hostid: h, Roles: roles, Status: status}
	}
	names := func(plan []*StorageStepPlan) string {
		out := []string{}
		for _, s := range plan {
			out = append(out, fmt.Sprintf("%s%v", s.Name, s.Hostids))
		}
		return strings.Join(out, " ")
	}
	// h3 becomes a mon (the cluster then has five)
	nodes := []*model.StorageClusterNode{node(1, "admin,mon,mgr,osd", model.StorageNodeActive), node(2, "mon,osd", model.StorageNodeActive),
		node(3, "mon,mgr", model.StorageNodeActive), node(4, "client", model.StorageNodeActive)}
	plan, err := cephBackend{}.TaskPlan(StorageTaskChangeRoles, cluster, &StorageTaskScope{Nodes: nodes, Changed: 3, ChangedFrom: []string{"mgr"}})
	must(t, err)
	if got := names(plan); !strings.Contains(got, "mon_addrs[1] client_refresh[1 2 3 4] finish[3]") {
		t.Fatalf("change of roles gaining a mon: %s", got)
	}
	// The mgr moves: the mons stay, no refresh
	plan, err = cephBackend{}.TaskPlan(StorageTaskChangeRoles, cluster, &StorageTaskScope{Nodes: nodes, Changed: 3, ChangedFrom: []string{"mon"}})
	must(t, err)
	if got := names(plan); strings.Contains(got, "mon_addrs") {
		t.Fatalf("change of roles keeping the mons: %s", got)
	}
	// A mon host leaves: the admin hosts that stay read the mons, the hosts that stay are refreshed, then it leaves
	nodes[1].Status = model.StorageNodeLeaving
	plan, err = cephBackend{}.TaskPlan(StorageTaskRemoveNode, cluster, &StorageTaskScope{Nodes: nodes})
	must(t, err)
	if got := names(plan); got != "remove_host[1] mon_addrs[1] client_refresh[1 3 4] leave[2]" {
		t.Fatalf("a mon host leaving: %s", got)
	}
	// Gone for good: no leave on it
	plan, err = cephBackend{}.TaskPlan(StorageTaskRemoveNode, cluster, &StorageTaskScope{Nodes: nodes, Offline: true})
	must(t, err)
	if got := names(plan); got != "remove_host[1] mon_addrs[1] client_refresh[1 3 4]" {
		t.Fatalf("a mon host gone for good: %s", got)
	}
	// A client leaving: no refresh
	nodes[1].Status, nodes[3].Status = model.StorageNodeActive, model.StorageNodeLeaving
	plan, err = cephBackend{}.TaskPlan(StorageTaskRemoveNode, cluster, &StorageTaskScope{Nodes: nodes})
	must(t, err)
	if got := names(plan); strings.Contains(got, "mon_addrs") {
		t.Fatalf("a client leaving: %s", got)
	}
}
