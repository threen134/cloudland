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
		{`{"layout":"ece","slot_mode":"lmr","slot_range":[0,23]}`, true},
		{`{"layout":"ece","slot_mode":"nvme","slot_range":[1,24]}`, true},
		{`{"layout":"ece","slot_mode":"lmr"}`, false},                                       // ecedrivemapping prompts without a range
		{`{"layout":"ece","slot_mode":"lmr","slot_range":[5,2]}`, false},                    // min after max
		{`{"layout":"ece","slot_mode":"sas","slot_range":[0,1]}`, false},                    // lmr or nvme
		{`{"layout":"ece","slot_range":[0,1]}`, false},                                      // a range without a mode
		{`{"layout":"ece","no_slot_map":true,"slot_mode":"lmr","slot_range":[0,1]}`, false}, // no slots to map
		{`{"layout":"ece","ece_meta_code":"4WayReplication","ece_meta_block_size":"2M","ece_meta_set_size":50}`, true},
		{`{"layout":"ece","ece_meta_code":"5+1p"}`, false},
		{`{"layout":"ece","ece_meta_block_size":"8M"}`, false}, // 3WayReplication takes up to 2M
		{`{"layout":"ece","ece_meta_set_size":120}`, false},
		{`{"layout":"ece","ece_strict":true}`, true},
		{`{"ece_strict":true}`, false},
		{`{"slot_mode":"lmr","slot_range":[0,1]}`, false},
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
		{"mixed media: HDDs and SSDs on every server", three, disks("1:4", "2:4", "3:4", "1:2:ssd", "2:2:ssd", "3:2:ssd"), true},
		{"mixed media: too few SSDs for the metadata code", three, disks("1:4", "2:4", "3:4", "1:1:ssd", "2:1:ssd", "3:1:ssd"), false},
		{"mixed media: SSDs on one server only", three, disks("1:4", "2:4", "3:4", "1:6:ssd"), false},
		{"SSD and NVMe beside HDDs", three, disks("1:4", "2:4", "3:4", "1:2:ssd", "2:2:ssd", "3:2:nvme"), false},
		{"all NVMe", three, disks("1:4:nvme", "2:4:nvme", "3:4:nvme"), true},
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
	if !caps.AddDisks || !caps.ReplaceDisk || !caps.AddNodes || !caps.RemoveNode || !caps.Upgrade || !caps.Finalize || !caps.ChangeRoles {
		t.Errorf("servers, disks, replacements, roles and upgrades of a recovery group: %+v", caps)
	}
	if caps.RemoveDisk || caps.Rebalance || caps.CreateFilesystem || caps.External {
		t.Errorf("no single disk removal, no rebalance (GNR balances), no second file system: %+v", caps)
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

// The change plans of an erasure code cluster (§7.9): servers join with every server resolving its disks for the disk
// list, disks go in on every server at once, a server leaves the group before the cluster, a pdisk is replaced by a disk
// of its own server; clients, roles and keys as in the replica layout
func TestGPFSECEChangePlans(t *testing.T) {
	gpfs := storageBackends[model.StorageKindGPFS]
	ece := &model.StorageCluster{Kind: model.StorageKindGPFS, Mode: model.StorageModeManaged, Layout: model.StorageLayoutECE}
	member := func(h int32, roles, status string) *model.StorageClusterNode {
		return &model.StorageClusterNode{Hostid: h, Roles: roles, Status: status}
	}
	base := func() []*model.StorageClusterNode {
		return []*model.StorageClusterNode{member(1, "admin,quorum,nsd", "active"), member(2, "quorum,nsd", "active"), member(3, "quorum,nsd", "active")}
	}
	plan := func(task string, scope *StorageTaskScope) (string, map[string][]int32) {
		steps, err := gpfs.TaskPlan(task, ece, scope)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		names, hosts := []string{}, map[string][]int32{}
		for _, s := range steps {
			names = append(names, s.Name)
			hosts[s.Name] = s.Hostids
			if storageTaskKinds["gpfs:"+task].Steps[s.Name] == nil {
				t.Errorf("step %s is not registered with gpfs:%s", s.Name, task)
			}
		}
		return strings.Join(names, " "), hosts
	}
	disk := func(h int32, status string) *model.StorageClusterDisk {
		return &model.StorageClusterDisk{Hostid: h, Status: status}
	}
	// A server joins: every server resolves its disks, the group takes it
	nodes := append(base(), member(4, "nsd", model.StorageNodeJoining))
	got, hosts := plan(StorageTaskAddNodes, &StorageTaskScope{Nodes: nodes, Disks: []*model.StorageClusterDisk{disk(4, "claiming")}})
	if got != "precheck join fetch_package install build_gpl ssh_trust add_node resolve_disks ece_add_servers finish" || len(hosts["resolve_disks"]) != 4 ||
		len(hosts["precheck"]) != 1 {
		t.Errorf("add a server: %s %v", got, hosts)
	}
	// A client joins: nothing of the recovery group
	got, _ = plan(StorageTaskAddNodes, &StorageTaskScope{Nodes: append(base(), member(4, "client", model.StorageNodeJoining))})
	if got != "precheck join fetch_package install build_gpl ssh_trust add_node finish" {
		t.Errorf("add a client: %s", got)
	}
	got, hosts = plan(StorageTaskAddDisks, &StorageTaskScope{Nodes: base(), Disks: []*model.StorageClusterDisk{disk(1, "claiming"), disk(2, "claiming"), disk(3, "claiming")}})
	if got != "resolve_disks ece_resize" || len(hosts["resolve_disks"]) != 3 {
		t.Errorf("add disks: %s %v", got, hosts)
	}
	nodes = base()
	nodes[2].Status = model.StorageNodeLeaving
	got, _ = plan(StorageTaskRemoveNode, &StorageTaskScope{Nodes: nodes})
	if got != "ece_remove_server remove_node leave" {
		t.Errorf("remove a server: %s", got)
	}
	nodes = append(base(), member(4, "client", model.StorageNodeLeaving))
	if got, _ = plan(StorageTaskRemoveNode, &StorageTaskScope{Nodes: nodes, Offline: true}); got != "remove_node" {
		t.Errorf("remove a client that is gone: %s", got)
	}
	got, hosts = plan(StorageTaskReplaceDisk, &StorageTaskScope{Nodes: base(), Disks: []*model.StorageClusterDisk{disk(2, "removing"), disk(2, "claiming")}})
	if got != "resolve_disks ece_replace release_disks" || hosts["resolve_disks"][0] != 2 {
		t.Errorf("replace: %s %v", got, hosts)
	}
	got, _ = plan(StorageTaskReplaceDisk, &StorageTaskScope{Nodes: base(), Disks: []*model.StorageClusterDisk{disk(2, "removing"), disk(3, "claiming")}})
	if !strings.Contains(got, "A pdisk is replaced by a disk of its own server") {
		t.Errorf("replace across servers: %s", got)
	}
	if got, _ = plan(StorageTaskRemoveDisk, &StorageTaskScope{Nodes: base()}); !strings.HasPrefix(got, "error:") {
		t.Errorf("remove a disk: %s", got)
	}
	if got, _ = plan(StorageTaskRebalance, &StorageTaskScope{Nodes: base()}); !strings.HasPrefix(got, "error:") {
		t.Errorf("rebalance: %s", got)
	}
	if got, _ = plan(StorageTaskChangeRoles, &StorageTaskScope{Nodes: base(), Changed: 2}); got != "ssh_trust change_roles finish" {
		t.Errorf("change roles: %s", got)
	}
}

// The memory of the recovery group servers may differ by 10% at most; what the precheck asks of a server
func TestGPFSECEReadiness(t *testing.T) {
	for _, c := range []struct {
		mem map[int32]int64
		ok  bool
	}{
		{map[int32]int64{1: 32000, 2: 32000, 3: 31000}, true},
		{map[int32]int64{1: 32000, 2: 35200}, true},
		{map[int32]int64{1: 32000, 2: 35300}, false},
		{map[int32]int64{1: 32000, 2: 0}, true}, // not reported
		{map[int32]int64{}, true},
	} {
		if err := gpfsECEMemorySpread(c.mem); (err == nil) != c.ok {
			t.Errorf("%v: err %v", c.mem, err)
		}
	}
	gpfs := storageBackends[model.StorageKindGPFS].(gpfsBackend)
	params, _ := gpfs.ParseParams(json.RawMessage(`{"layout":"ece","pagepool_mib":16384,"ece_strict":true}`))
	in := gpfs.ReadinessInput([]string{"quorum", "nsd"}, params)
	if in["min_mem_mib"] != 20480 || in["min_cores"] != gpfsECESupportCores || in["min_link_mbps"] != gpfsECESupportLinkMbps || in["strict"] != true {
		t.Errorf("server readiness %v", in)
	}
	if in := gpfs.ReadinessInput([]string{"client"}, params); in != nil {
		t.Errorf("a client has no readiness of a server: %v", in)
	}
	replica, _ := gpfs.ParseParams(json.RawMessage(`{}`))
	if in := gpfs.ReadinessInput([]string{"nsd"}, replica); in != nil {
		t.Errorf("the replica layout has none: %v", in)
	}
}

// The vdisk sets of a deployment: the data set alone, or with the metadata set on the solid state disks
func TestGPFSECEDeploySets(t *testing.T) {
	cluster := &model.StorageCluster{Model: model.Model{ID: 9}, Layout: model.StorageLayoutECE,
		Params: `{"layout":"ece","ece_code":"8+2p","block_size":"16M","ece_set_size":70,"ece_meta_code":"4WayReplication","ece_meta_block_size":"2M"}`}
	hdd := []*model.StorageClusterDisk{{Media: "hdd"}, {Media: "hdd"}}
	sets, err := gpfsECEDeploySets(cluster, hdd)
	if err != nil || len(sets) != 1 || sets[0]["vdisk_set"] != "cl9_vs" || sets[0]["code"] != "8+2p" || sets[0]["da_type"] != nil {
		t.Errorf("one media: %v %v", sets, err)
	}
	sets, err = gpfsECEDeploySets(cluster, append(hdd, &model.StorageClusterDisk{Media: "nvme"}))
	if err != nil || len(sets) != 2 {
		t.Fatalf("mixed: %v %v", sets, err)
	}
	if sets[0]["vdisk_set"] != "cl9_vs" || sets[0]["da_type"] != "hdd" || sets[0]["nsd_usage"] != "dataOnly" || sets[0]["storage_pool"] != "data" ||
		sets[0]["set_size_pct"] != 70 {
		t.Errorf("data set %v", sets[0])
	}
	if sets[1]["vdisk_set"] != "cl9_vsm" || sets[1]["da_type"] != "nvme" || sets[1]["code"] != "4WayReplication" || sets[1]["block_size"] != "2M" ||
		sets[1]["nsd_usage"] != "metadataOnly" || sets[1]["storage_pool"] != "system" || sets[1]["set_size_pct"] != 80 {
		t.Errorf("metadata set %v", sets[1])
	}
	cluster.Attrs = `{"vdisk_sets":["cl9_vs","cl9_vsm"]}`
	if next := gpfsECENextSet(cluster); next != "cl9_vs3" {
		t.Errorf("next set %s", next)
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
	ece.ID = 7
	info := StorageClusterLayoutInfo(ece)
	if info["code"] != "8+3p" || info["no_slot_map"] != true || info["recovery_group"] != "cl7_rg" || info["vdisk_set"] != "cl7_vs" ||
		info["node_class"] != "cl7_nc" {
		t.Errorf("ece layout info %v", info)
	}
	ece.Params = `{"layout":"ece","ece_code":"8+3p","slot_mode":"lmr","slot_range":[0,11],"ece_meta_code":"4WayReplication"}`
	ece.Attrs = `{"recovery_group":"cl7_rg","vdisk_set":"cl7_vs","vdisk_sets":["cl7_vs","cl7_vsm","cl7_vs3"]}`
	if info := StorageClusterLayoutInfo(ece); info["slot_mode"] != "lmr" || info["meta_code"] != "4WayReplication" ||
		info["vdisk_set"] != "cl7_vs, cl7_vsm, cl7_vs3" {
		t.Errorf("slot mode, metadata code and every vdisk set: %v", info)
	}
	// One media: the wizard sent the metadata parameters, but there is no metadata set to show (review 2026-10-07)
	ece.Attrs = `{"recovery_group":"cl7_rg","vdisk_set":"cl7_vs"}`
	if info := StorageClusterLayoutInfo(ece); info["meta_code"] != nil || info["meta_block_size"] != nil {
		t.Errorf("no metadata set, no metadata code: %v", info)
	}
	ece.Params = `{"layout":"ece","ece_code":"8+3p","no_slot_map":true}`
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
