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

// The erasure code layout runs on the erasure code edition only, the replica layout on any
func TestGPFSECEPackage(t *testing.T) {
	gpfs := storageBackends[model.StorageKindGPFS]
	ece, _ := gpfs.ParseParams(json.RawMessage(`{"layout":"ece"}`))
	replica, _ := gpfs.ParseParams(json.RawMessage(`{}`))
	for _, c := range []struct {
		params  interface{}
		edition string
		ok      bool
	}{
		{ece, "erasure_code", true},
		{ece, "data_management", false},
		{ece, "", false},
		{replica, "data_management", true},
		{replica, "erasure_code", true},
	} {
		err := storageCheckPackage(gpfs, c.params, &model.StoragePackage{Edition: c.edition})
		if (err == nil) != c.ok {
			t.Errorf("%+v edition %q: err %v", c.params, c.edition, err)
		}
	}
	if err := storageCheckPackage(storageBackends[model.StorageKindCeph], nil, &model.StoragePackage{}); err != nil {
		t.Errorf("a backend without the check takes any package: %v", err)
	}
}

// The parameters of the erasure code layout (shared-storage-design.md §7.9)
func TestGPFSECEParams(t *testing.T) {
	gpfs := storageBackends[model.StorageKindGPFS]
	for _, c := range []struct {
		params string
		ok     bool
	}{
		{`{"layout":"ece"}`, true},
		{`{"layout":"ece","ece_code":"8+3p","block_size":"16M","ece_set_size":50,"pagepool_mib":16384,"no_slot_map":true}`, true},
		{`{"layout":"ece","ece_code":"3WayReplication","block_size":"2M"}`, true},
		{`{"layout":"ece","ece_code":"3WayReplication","block_size":"4M"}`, false}, // replication takes up to 2M
		{`{"layout":"ece","ece_code":"4+2p","block_size":"16M"}`, false},
		{`{"layout":"ece","ece_code":"6+2p"}`, false},
		{`{"layout":"ece","pagepool_mib":4096}`, false}, // mmvdisk refuses less than 8 GiB
		{`{"layout":"ece","data_replicas":2}`, false},   // the code protects the data
		{`{"layout":"ece","test":true}`, false},         // no single host layout
		{`{"layout":"ece","ece_set_size":5}`, false},
		{`{"layout":"raid"}`, false},
		{`{"ece_code":"4+2p"}`, false}, // an erasure code parameter without the layout
		{`{"no_slot_map":true}`, false},
		{`{"layout":"replica"}`, true},
	} {
		_, err := gpfs.ParseParams(json.RawMessage(c.params))
		if (err == nil) != c.ok {
			t.Errorf("%s: err %v", c.params, err)
		}
	}
	p, err := gpfs.ParseParams(json.RawMessage(`{"layout":"ece"}`))
	if err != nil {
		t.Fatal(err)
	}
	g := p.(*gpfsParams)
	if g.ECECode != "4+2p" || g.BlockSize != "4M" || g.ECESetSize != 80 || g.PagepoolMiB != 8192 {
		t.Errorf("defaults %+v", g)
	}
	if storageLayoutFor(gpfs, p) != model.StorageLayoutECE {
		t.Error("layout ece")
	}
	p, _ = gpfs.ParseParams(json.RawMessage(`{"layout":"replica"}`))
	if storageLayoutFor(gpfs, p) != model.StorageLayoutReplica || p.(*gpfsParams).Layout != "" {
		t.Error("layout replica, kept out of the stored parameters")
	}
}

// The hosts and disks of a recovery group: 3-32 servers, the same disks on each, 12 in all, one media
func TestGPFSECELayout(t *testing.T) {
	gpfs := storageBackends[model.StorageKindGPFS]
	params, err := gpfs.ParseParams(json.RawMessage(`{"layout":"ece"}`))
	if err != nil {
		t.Fatal(err)
	}
	nodes := func(roles ...string) []*StorageNodePlan {
		list := []*StorageNodePlan{}
		for i, r := range roles {
			list = append(list, &StorageNodePlan{Hostid: int32(i + 1), Roles: strings.Split(r, ",")})
		}
		return list
	}
	// disks gives each host n disks of a media: "1:4" four HDDs on host 1, "2:4:ssd" four SSDs on host 2
	disks := func(spec ...string) []*StorageDiskPlan {
		list := []*StorageDiskPlan{}
		for _, s := range spec {
			f := strings.Split(s, ":")
			media := "hdd"
			if len(f) > 2 {
				media = f[2]
			}
			for i := 0; i < int(f[1][0]-'0'); i++ {
				list = append(list, &StorageDiskPlan{Hostid: int32(f[0][0] - '0'), DiskID: f[1] + string(rune('a'+i)), Media: media})
			}
		}
		return list
	}
	three := nodes("admin,quorum,nsd", "quorum,nsd", "quorum,nsd")
	for _, c := range []struct {
		name  string
		nodes []*StorageNodePlan
		disks []*StorageDiskPlan
		ok    bool
	}{
		{"three servers, 4 disks each", three, disks("1:4", "2:4", "3:4"), true},
		{"with a client", nodes("admin,quorum,nsd", "quorum,nsd", "quorum,nsd", "client"), disks("1:4", "2:4", "3:4"), true},
		{"two servers", nodes("admin,quorum,nsd", "quorum,nsd", "quorum"), disks("1:6", "2:6"), false},
		{"11 disks", three, disks("1:4", "2:4", "3:3"), false},
		{"different counts", three, disks("1:5", "2:4", "3:4"), false},
		{"two media", three, disks("1:4", "2:4", "3:4:ssd"), false},
		{"too few in all", three, disks("1:3", "2:3", "3:3"), false},
		{"a client with disks", nodes("admin,quorum,nsd", "quorum,nsd", "quorum,nsd", "client"), disks("1:4", "2:4", "3:4", "4:1"), false},
		{"quorum still odd", nodes("admin,quorum,nsd", "quorum,nsd", "nsd"), disks("1:4", "2:4", "3:4"), false},
	} {
		if err := gpfs.CheckLayout(c.nodes, c.disks, params); (err == nil) != c.ok {
			t.Errorf("%s: err %v", c.name, err)
		}
	}
	wide, _ := gpfs.ParseParams(json.RawMessage(`{"layout":"ece","ece_code":"8+3p"}`))
	if err := gpfs.CheckLayout(three, disks("1:4", "2:4", "3:4"), wide); err == nil || !strings.Contains(err.Error(), "at least 13 disks") {
		t.Errorf("8+3p spans 11 disks: 12 leave no room, the message names the 13 needed: %v", err)
	}
	if err := gpfs.CheckLayout(three, disks("1:5", "2:5", "3:5"), wide); err != nil {
		t.Errorf("8+3p on 15 disks: %v", err)
	}
}

// Memory, capabilities and the deploy plan of the erasure code layout
func TestGPFSECEPlan(t *testing.T) {
	gpfs := storageBackends[model.StorageKindGPFS]
	params, _ := gpfs.ParseParams(json.RawMessage(`{"layout":"ece","pagepool_mib":10240}`))
	if got := gpfs.ReserveMB([]string{"admin", "quorum", "nsd"}, 4, params); got != 10240+2048 {
		t.Errorf("server reserve %d", got)
	}
	if got := gpfs.ReserveMB([]string{"client"}, 0, params); got != 2048 {
		t.Errorf("client reserve %d", got)
	}
	ece := &model.StorageCluster{Kind: model.StorageKindGPFS, Mode: model.StorageModeManaged, Layout: model.StorageLayoutECE}
	caps := storageClusterCapabilities(gpfs, ece)
	if !caps.Managed || !caps.Pools || !caps.Filesystems || !caps.RotateKeys {
		t.Errorf("ece capabilities %+v", caps)
	}
	if caps.AddDisks || caps.RemoveDisk || caps.ReplaceDisk || caps.AddNodes || caps.RemoveNode || caps.Rebalance || caps.Upgrade || caps.ChangeRoles {
		t.Errorf("the first version changes no hosts or disks of a recovery group: %+v", caps)
	}
	replica := &model.StorageCluster{Kind: model.StorageKindGPFS, Mode: model.StorageModeManaged, Layout: model.StorageLayoutReplica}
	if !storageClusterCapabilities(gpfs, replica).AddDisks {
		t.Error("a replica cluster keeps the capabilities of its kind")
	}
	nodes := []*model.StorageClusterNode{
		{Hostid: 1, Roles: "admin,quorum,nsd"}, {Hostid: 2, Roles: "quorum,nsd"}, {Hostid: 3, Roles: "quorum,nsd"}, {Hostid: 4, Roles: "client"},
	}
	steps, err := gpfs.TaskPlan(StorageTaskDeploy, ece, &StorageTaskScope{Nodes: nodes})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, s := range steps {
		names = append(names, s.Name)
		if s.Name == "resolve_disks" && len(s.Hostids) != 3 {
			t.Errorf("resolve_disks on the servers only: %v", s.Hostids)
		}
	}
	want := "precheck join fetch_package install build_gpl ssh_trust create_cluster start resolve_disks ece_configure ece_slots ece_create_rg ece_create_vs ece_create_fs finish"
	if strings.Join(names, " ") != want {
		t.Errorf("deploy steps %v", names)
	}
	kind := storageTaskKinds["gpfs:"+StorageTaskDeploy]
	for _, n := range names {
		if kind.Steps[n] == nil {
			t.Errorf("step %s is not registered with the deploy task of gpfs", n)
		}
	}
	nc, rg, vs := gpfsECENames(&model.StorageCluster{Model: model.Model{ID: 12}})
	for _, n := range []string{nc, rg, vs} {
		if !gpfsFsNameRe.MatchString(n) {
			t.Errorf("%s is no name mmvdisk and gpfs_ece.sh take", n)
		}
	}
	if teardown := gpfsECETeardown(&model.StorageCluster{Model: model.Model{ID: 12}}); teardown["recovery_group"] != "cl12_rg" {
		t.Errorf("teardown %v", teardown)
	}
}

// The disks of a deployed cluster get the names of their pdisks: by WWN, else by host address and kernel name
func TestGPFSECEPdiskNames(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"naa.5000C50E0ECE0011", "5000c50e0ece0011"},
		{"0x5000c50e0ece0011", "5000c50e0ece0011"},
		{"wwn-0x6001405B729C020A", "6001405b729c020a"},
		{"", ""},
	} {
		if got := gpfsWWNKey(c.in); got != c.want {
			t.Errorf("%q: %q", c.in, got)
		}
	}
	disks := []*model.StorageClusterDisk{
		{Model: model.Model{ID: 1}, Hostid: 1, DiskID: "wwn-a", WWN: "0x5000c50e0ece0011"},
		{Model: model.Model{ID: 2}, Hostid: 1, DiskID: "virtio-b"},
		{Model: model.Model{ID: 3}, Hostid: 2, DiskID: "wwn-c", WWN: "0x5000c50e0ece0021"},
	}
	ips := map[int32]string{1: "10.0.0.1", 2: "10.0.0.2"}
	resolved := map[int32]map[string]string{1: {"wwn-a": "sda", "virtio-b": "sdb"}, 2: {"wwn-c": "sda"}}
	pdisks := []*gpfsECEPdisk{
		{Name: "n001p001", IP: "10.0.0.1", Device: "sda", WWN: "naa.5000C50E0ECE0011"},
		{Name: "n001p002", IP: "10.0.0.1", Device: "sdb"},
		// The kernel name changed since the resolve: the WWN still finds it
		{Name: "n002p001", IP: "10.0.0.2", Device: "sdc", WWN: "naa.5000C50E0ECE0021"},
	}
	names, err := gpfsECEPdiskNames(disks, pdisks, ips, resolved)
	if err != nil || names[1] != "n001p001" || names[2] != "n001p002" || names[3] != "n002p001" {
		t.Fatalf("names %v, err %v", names, err)
	}
	if _, err := gpfsECEPdiskNames(disks, pdisks[:2], ips, resolved); err == nil {
		t.Error("a disk without its pdisk must fail the deployment")
	}
	twice := []*gpfsECEPdisk{{Name: "n001p001", IP: "10.0.0.1", Device: "sda", WWN: "naa.5000C50E0ECE0011"},
		{Name: "n001p001", IP: "10.0.0.1", Device: "sdb"}, {Name: "n002p001", IP: "10.0.0.2", Device: "sda"}}
	if _, err := gpfsECEPdiskNames(disks, twice, ips, resolved); err == nil {
		t.Error("one pdisk for two disks must fail the deployment")
	}
}

// What the API shows of the layout: the erasure code layer from the parameters and the attrs, nothing for a replica
// cluster or a kind without layouts
func TestGPFSECELayoutInfo(t *testing.T) {
	ece := &model.StorageCluster{Kind: model.StorageKindGPFS, Layout: model.StorageLayoutECE,
		Params: `{"layout":"ece","ece_code":"8+3p","no_slot_map":true}`, Attrs: `{"recovery_group":"cl7_rg","vdisk_set":"cl7_vs","node_class":"cl7_nc"}`}
	info := StorageClusterLayoutInfo(ece)
	if info["code"] != "8+3p" || info["no_slot_map"] != true || info["recovery_group"] != "cl7_rg" || info["vdisk_set"] != "cl7_vs" ||
		info["node_class"] != "cl7_nc" {
		t.Errorf("ece layout info %v", info)
	}
	ece.Attrs = ""
	if info := StorageClusterLayoutInfo(ece); info["code"] != "8+3p" || info["recovery_group"] != nil {
		t.Errorf("before the deployment finished only the parameters: %v", info)
	}
	if info := StorageClusterLayoutInfo(&model.StorageCluster{Kind: model.StorageKindGPFS, Layout: model.StorageLayoutReplica}); info != nil {
		t.Errorf("replica layout shows nothing: %v", info)
	}
	if info := StorageClusterLayoutInfo(&model.StorageCluster{Kind: model.StorageKindCeph}); info != nil {
		t.Errorf("ceph shows nothing: %v", info)
	}
}
