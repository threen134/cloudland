/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"encoding/json"
	"strings"
	"testing"

	"api/src/model"
)

// Every registered backend keeps the rules the framework relies on (shared-storage-design.md §4.5)
func TestStorageBackendRegistry(t *testing.T) {
	kinds := []string{}
	for _, b := range StorageBackendList() {
		kinds = append(kinds, b.Kind())
		roles := b.Roles()
		if !hasRole(roles, model.StorageRoleClient) {
			t.Errorf("%s: no client role", b.Kind())
		}
		if r := b.DiskRole(); r != "" && !hasRole(roles, r) {
			t.Errorf("%s: disk role %s is not one of its roles", b.Kind(), r)
		}
		base, once := b.DefaultRoles()
		for _, r := range append(base, once...) {
			if !hasRole(roles, r) {
				t.Errorf("%s: default role %s is not one of its roles", b.Kind(), r)
			}
		}
		req := b.Requirements()
		if len(req.Systems) == 0 || len(req.PeerPorts) == 0 {
			t.Errorf("%s: requirements without systems or ports", b.Kind())
		}
		if _, err := b.ParseParams(nil); err != nil {
			t.Errorf("%s: no parameters must give the defaults: %v", b.Kind(), err)
		}
	}
	if strings.Join(kinds, ",") != "ceph,gpfs" {
		t.Fatalf("registered kinds %v", kinds)
	}
	if _, err := storageBackendOf("nfs"); err == nil {
		t.Fatal("an unknown kind must be refused")
	}
	for _, s := range storageBackends[model.StorageKindGPFS].Requirements().Systems {
		if s.Version == "26.04" {
			t.Fatal("GPFS can not build its kernel module on 26.04 (§2.2)")
		}
	}
}

func TestStorageBackendParams(t *testing.T) {
	for _, c := range []struct {
		kind, params string
		ok           bool
	}{
		{"gpfs", `{"test":true,"fs_name":"fs1","block_size":"4M","data_replicas":2,"pagepool_mib":2048}`, true},
		{"gpfs", `{"block_size":"3M"}`, false},
		{"gpfs", `{"data_replicas":4}`, false},
		{"gpfs", `{"pagepool_mib":100}`, false},
		{"gpfs", `{"fs_name":"` + strings.Repeat("f", 33) + `"}`, false},
		// What gpfs_fs.sh accepts, refused up front rather than at create_fs
		{"gpfs", `{"fs_name":"vm-data"}`, false},
		{"gpfs", `{"fs_name":"1fs"}`, false},
		{"gpfs", `{"fs_name":"vm_data2"}`, true},
		{"gpfs", `{"fs_name":""}`, true},
		{"gpfs", `{"replicas":3}`, false}, // a Ceph parameter: misspelt or the wrong kind
		{"ceph", `{"test":true,"replicas":1,"osd_memory_target_mib":2048}`, true},
		{"ceph", `{"osd_memory_target_mib":512}`, false},
		{"ceph", `{"pagepool_mib":1024}`, false},
		{"ceph", `null`, true},
		{"ceph", `[1]`, false},
	} {
		_, err := storageBackends[c.kind].ParseParams(json.RawMessage(c.params))
		if (err == nil) != c.ok {
			t.Errorf("%s %s: err %v", c.kind, c.params, err)
		}
	}
}

func TestStorageBackendLayout(t *testing.T) {
	nodes := func(roles ...string) []*StorageNodePlan {
		list := []*StorageNodePlan{}
		for i, r := range roles {
			list = append(list, &StorageNodePlan{Hostid: int32(i + 1), Roles: strings.Split(r, ",")})
		}
		return list
	}
	disksOn := func(hostids ...int32) []*StorageDiskPlan {
		list := []*StorageDiskPlan{}
		for _, h := range hostids {
			list = append(list, &StorageDiskPlan{Hostid: h, DiskID: "d"})
		}
		return list
	}
	for _, c := range []struct {
		name, kind, params string
		nodes              []*StorageNodePlan
		disks              []*StorageDiskPlan
		ok                 bool
	}{
		{"gpfs three hosts", "gpfs", ``, nodes("admin,quorum,nsd", "quorum,nsd", "quorum,nsd", "client"), disksOn(1, 2, 3), true},
		{"gpfs two quorum", "gpfs", ``, nodes("admin,quorum,nsd", "quorum,nsd", "nsd"), disksOn(1, 2, 3), false},
		{"gpfs admin not quorum", "gpfs", ``, nodes("admin,nsd", "quorum,nsd", "quorum,nsd", "quorum"), disksOn(1, 2, 3), false},
		{"gpfs two failure groups", "gpfs", ``, nodes("admin,quorum,nsd", "quorum,nsd", "quorum"), disksOn(1, 2), false},
		{"gpfs nsd host without disk", "gpfs", ``, nodes("admin,quorum,nsd", "quorum,nsd", "quorum,nsd"), disksOn(1, 2), false},
		{"gpfs more replicas than groups", "gpfs", `{"test":true,"data_replicas":2}`, nodes("admin,quorum,nsd"), disksOn(1), false},
		{"gpfs test layout", "gpfs", `{"test":true}`, nodes("admin,quorum,nsd"), disksOn(1), true},
		{"gpfs single host without test", "gpfs", ``, nodes("admin,quorum,nsd"), disksOn(1), false},
		{"ceph three hosts", "ceph", ``, nodes("admin,mon,mgr,osd", "mon,osd", "mon,osd", "client"), disksOn(1, 2, 3), true},
		{"ceph three mgr", "ceph", ``, nodes("admin,mon,mgr,osd", "mon,mgr,osd", "mon,mgr,osd"), disksOn(1, 2, 3), false},
		{"ceph admin not mon", "ceph", ``, nodes("admin,mgr,osd", "mon,osd", "mon,osd", "mon"), disksOn(1, 2, 3), false},
		{"ceph no admin", "ceph", ``, nodes("mon,mgr,osd", "mon,osd", "mon,osd"), disksOn(1, 2, 3), false},
		{"ceph two replicas", "ceph", `{"replicas":2}`, nodes("admin,mon,mgr,osd", "mon,osd", "mon,osd"), disksOn(1, 2, 3), false},
		{"ceph two osd hosts", "ceph", ``, nodes("admin,mon,mgr,osd", "mon,osd", "mon"), disksOn(1, 2), false},
		{"ceph test layout", "ceph", `{"test":true}`, nodes("admin,mon,mgr,osd"), disksOn(1), true},
	} {
		b := storageBackends[c.kind]
		params, err := b.ParseParams(json.RawMessage(c.params))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if err = b.CheckLayout(c.nodes, c.disks, params); (err == nil) != c.ok {
			t.Errorf("%s: err %v", c.name, err)
		}
	}
}

func TestStorageBackendHostsAndMemory(t *testing.T) {
	gpfs, ceph := storageBackends[model.StorageKindGPFS], storageBackends[model.StorageKindCeph]
	if gpfs.HostConflict([]string{"client"}, []string{"client"}) == "" {
		t.Error("a host belongs to one GPFS cluster at most")
	}
	if ceph.HostConflict([]string{"client"}, []string{"mon", "osd"}) != "" || ceph.HostConflict([]string{"osd"}, []string{"client"}) != "" {
		t.Error("a Ceph client may use other clusters")
	}
	if ceph.HostConflict([]string{"osd"}, []string{"mon"}) == "" {
		t.Error("a host runs the daemons of one Ceph cluster")
	}
	parse := func(b StorageBackend, raw string) interface{} {
		p, err := b.ParseParams(json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, c := range []struct {
		b      StorageBackend
		params string
		roles  []string
		disks  int
		want   int32
	}{
		{gpfs, ``, []string{"client"}, 0, 2048},
		{gpfs, `{"pagepool_mib":4096}`, []string{"admin", "quorum", "nsd"}, 2, 5120},
		{ceph, ``, []string{"client"}, 0, 0},
		{ceph, ``, []string{"admin", "mon", "mgr", "osd"}, 2, 2048 + 1024 + 2*2560},
		{ceph, `{"osd_memory_target_mib":1024}`, []string{"osd"}, 3, 3 * 1536},
	} {
		if got := c.b.ReserveMB(c.roles, c.disks, parse(c.b, c.params)); got != c.want {
			t.Errorf("%s %v %d disks %s: reserve %d MiB, want %d", c.b.Kind(), c.roles, c.disks, c.params, got, c.want)
		}
	}
}
