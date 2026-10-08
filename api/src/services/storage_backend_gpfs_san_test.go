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

// The parameters and the layout of the shared disk layout (shared-storage-design.md §7.10)
func TestGPFSSANLayout(t *testing.T) {
	gpfs := storageBackends[model.StorageKindGPFS]
	for _, c := range []struct {
		params string
		ok     bool
	}{
		{`{"layout":"san"}`, true},
		{`{"layout":"san","data_replicas":1,"test":true}`, true},
		{`{"layout":"san","data_replicas":2}`, false}, // the array protects the data
		{`{"layout":"san","ece_code":"4+2p"}`, false},
		{`{"layout":"san","no_slot_map":true}`, false},
	} {
		if _, err := gpfs.ParseParams(json.RawMessage(c.params)); (err == nil) != c.ok {
			t.Errorf("%s: err %v", c.params, err)
		}
	}
	params, _ := gpfs.ParseParams(json.RawMessage(`{"layout":"san"}`))
	if storageLayoutFor(gpfs, params) != model.StorageLayoutSAN || !storageTakesShared(gpfs, params) {
		t.Error("layout san takes shared LUNs")
	}
	replica, _ := gpfs.ParseParams(json.RawMessage(`{}`))
	if storageTakesShared(gpfs, replica) {
		t.Error("the replica layout refuses shared LUNs")
	}
	three := []*StorageNodePlan{{Hostid: 1, Roles: []string{"admin", "quorum", "nsd"}}, {Hostid: 2, Roles: []string{"quorum", "nsd"}},
		{Hostid: 3, Roles: []string{"quorum"}}}
	lun := func(id string, hosts ...int32) []*StorageDiskPlan {
		out := []*StorageDiskPlan{}
		for _, h := range hosts {
			out = append(out, &StorageDiskPlan{Hostid: h, DiskID: id})
		}
		return out
	}
	if err := gpfs.CheckLayout(three, append(lun("wwn-a", 1, 2), lun("wwn-b", 1)...), params); err != nil {
		t.Errorf("two LUNs, one served by two hosts: %v", err)
	}
	if err := gpfs.CheckLayout(three, lun("wwn-a", 1), params); err == nil {
		t.Error("an NSD host serving no LUN")
	}
	if err := gpfs.CheckLayout(three, nil, params); err == nil {
		t.Error("no LUN")
	}
	nine := []*StorageNodePlan{}
	for h := int32(1); h <= 9; h++ {
		roles := []string{"nsd"}
		if h <= 3 {
			roles = append(roles, "quorum")
		}
		if h == 1 {
			roles = append(roles, "admin")
		}
		nine = append(nine, &StorageNodePlan{Hostid: h, Roles: roles})
	}
	if err := gpfs.CheckLayout(nine, lun("wwn-a", 1, 2, 3, 4, 5, 6, 7, 8, 9), params); err == nil || !strings.Contains(err.Error(), "at most 8") {
		t.Errorf("an NSD takes at most 8 servers: %v", err)
	}
	ece := &model.StorageCluster{Kind: model.StorageKindGPFS, Mode: model.StorageModeManaged, Layout: model.StorageLayoutSAN}
	caps := storageClusterCapabilities(gpfs, ece)
	if !caps.AddDisks || !caps.RemoveDisk || !caps.AddNodes || !caps.RemoveNode || caps.ReplaceDisk || caps.CreateFilesystem {
		t.Errorf("san capabilities %+v", caps)
	}
}

// A LUN has one name on every host that serves it; the server lists rotate over the LUNs; a shared LUN is wiped once,
// and never while another host keeps serving it
func TestGPFSSANNamesAndWipes(t *testing.T) {
	cluster := &model.StorageCluster{Model: model.Model{ID: 4}, Layout: model.StorageLayoutSAN}
	disks := []*model.StorageClusterDisk{
		{Model: model.Model{ID: 10}, Hostid: 1, DiskID: "wwn-a"}, {Model: model.Model{ID: 11}, Hostid: 2, DiskID: "wwn-a"},
		{Model: model.Model{ID: 12}, Hostid: 1, DiskID: "wwn-b"}, {Model: model.Model{ID: 13}, Hostid: 3, DiskID: "wwn-a"},
	}
	names := gpfsNSDNames(cluster, disks)
	if names[10] != "cl4s1" || names[11] != "cl4s1" || names[13] != "cl4s1" || names[12] != "cl4s2" {
		t.Fatalf("names %v", names)
	}
	// A LUN keeps its name; a new one counts on from the highest
	disks[0].Name, disks[1].Name, disks[3].Name, disks[2].Name = "cl4s1", "cl4s1", "cl4s1", "cl4s7"
	disks = append(disks, &model.StorageClusterDisk{Model: model.Model{ID: 14}, Hostid: 2, DiskID: "wwn-c"})
	if names = gpfsNSDNames(cluster, disks); names[12] != "cl4s7" || names[14] != "cl4s8" {
		t.Fatalf("kept and new names %v", names)
	}
	if got := gpfsSANServers([]int32{1, 2, 3}, 1); got[0] != 2 || got[2] != 1 {
		t.Errorf("rotation %v", got)
	}
	if got := gpfsSANServers([]int32{1, 2, 3}, 3); got[0] != 1 {
		t.Errorf("rotation wraps %v", got)
	}
	all := map[int32]bool{1: true, 2: true, 3: true}
	if !gpfsSANPrimary(disks[0], disks, all) || gpfsSANPrimary(disks[1], disks, all) {
		t.Error("the lowest host of a LUN is its primary")
	}
	// Host 1 leaves: LUN a stays served by 2 and 3, not wiped; LUN b leaves with it, wiped there
	leaving := map[int32]bool{1: true}
	if gpfsSANWipedHere(disks[0], disks, leaving) || !gpfsSANWipedHere(disks[2], disks, leaving) {
		t.Error("a LUN another host keeps is not wiped, one leaving with its only host is")
	}
	// The cluster goes: every LUN wiped once, by its lowest host
	if !gpfsSANWipedHere(disks[0], disks, all) || gpfsSANWipedHere(disks[1], disks, all) || gpfsSANWipedHere(disks[3], disks, all) {
		t.Error("a LUN is wiped once when the cluster goes")
	}

	// Who checks a LUN empty while it is resolved (review 2026-10-07): a new LUN, by its lowest host only; a LUN the
	// cluster uses, by nobody, also when the host that comes to serve it has the lowest hostid of all
	live := []*model.StorageClusterDisk{
		{Model: model.Model{ID: 20}, Hostid: 2, DiskID: "wwn-x", Status: model.StorageDiskActive},
		{Model: model.Model{ID: 21}, Hostid: 3, DiskID: "wwn-x", Status: model.StorageDiskActive},
		{Model: model.Model{ID: 22}, Hostid: 1, DiskID: "wwn-x", Status: model.StorageDiskClaiming},
		{Model: model.Model{ID: 23}, Hostid: 2, DiskID: "wwn-y", Status: model.StorageDiskClaiming},
		{Model: model.Model{ID: 24}, Hostid: 1, DiskID: "wwn-y", Status: model.StorageDiskClaiming},
	}
	for _, d := range live[:3] {
		if gpfsSANChecksEmpty(d, live) {
			t.Errorf("record %d of a LUN in use checks it empty (and would wipe a live NSD)", d.ID)
		}
	}
	if gpfsSANChecksEmpty(live[3], live) || !gpfsSANChecksEmpty(live[4], live) {
		t.Error("a new LUN is checked by its lowest host only")
	}
}

// The change plans of a shared disk cluster
func TestGPFSSANChangePlans(t *testing.T) {
	gpfs := storageBackends[model.StorageKindGPFS]
	san := &model.StorageCluster{Model: model.Model{ID: 4}, Kind: model.StorageKindGPFS, Mode: model.StorageModeManaged, Layout: model.StorageLayoutSAN}
	member := func(h int32, roles, status string) *model.StorageClusterNode {
		return &model.StorageClusterNode{Hostid: h, Roles: roles, Status: status}
	}
	base := func() []*model.StorageClusterNode {
		return []*model.StorageClusterNode{member(1, "admin,quorum,nsd", "active"), member(2, "quorum,nsd", "active"), member(3, "quorum", "active")}
	}
	disk := func(id int64, h int32, lun, status string) *model.StorageClusterDisk {
		return &model.StorageClusterDisk{Model: model.Model{ID: id}, Hostid: h, DiskID: lun, Status: status, Name: map[bool]string{true: "", false: "cl4s1"}[status == "claiming"]}
	}
	plan := func(task string, scope *StorageTaskScope) string {
		steps, err := gpfs.TaskPlan(task, san, scope)
		if err != nil {
			return "error: " + err.Error()
		}
		names := []string{}
		for _, s := range steps {
			names = append(names, s.Name)
			if storageTaskKinds["gpfs:"+task].Steps[s.Name] == nil {
				t.Errorf("step %s is not registered with gpfs:%s", s.Name, task)
			}
		}
		return strings.Join(names, " ")
	}
	active := []*model.StorageClusterDisk{disk(1, 1, "wwn-a", "active"), disk(2, 2, "wwn-a", "active")}
	// A host joins serving the LUN there and a new one
	nodes := append(base(), member(4, "nsd", model.StorageNodeJoining))
	got := plan(StorageTaskAddNodes, &StorageTaskScope{Nodes: nodes, Disks: append(active, disk(3, 4, "wwn-a", "claiming"), disk(4, 4, "wwn-c", "claiming"))})
	if got != "precheck join fetch_package install build_gpl ssh_trust add_node resolve_disks create_nsd san_servers add_disks finish" {
		t.Errorf("add a host with an existing and a new LUN: %s", got)
	}
	// Only gaining a server: no NSD to make, none to add
	got = plan(StorageTaskAddDisks, &StorageTaskScope{Nodes: base(), Disks: append(active, disk(3, 1, "wwn-z", "active"), disk(4, 3, "wwn-a", "claiming"))})
	if got != "resolve_disks san_servers" {
		t.Errorf("a LUN gaining a server: %s", got)
	}
	// Host 2 leaves: LUN a keeps host 1
	nodes = base()
	nodes[1].Status = model.StorageNodeLeaving
	got = plan(StorageTaskRemoveNode, &StorageTaskScope{Nodes: nodes, Disks: []*model.StorageClusterDisk{disk(1, 1, "wwn-a", "active"), disk(2, 2, "wwn-a", "removing")}})
	if got != "san_servers remove_node leave" {
		t.Errorf("a host leaving a shared LUN: %s", got)
	}
	// It served a LUN alone: that one leaves with its data moved
	got = plan(StorageTaskRemoveNode, &StorageTaskScope{Nodes: nodes, Disks: []*model.StorageClusterDisk{disk(1, 1, "wwn-a", "active"), disk(2, 2, "wwn-a", "removing"),
		disk(3, 2, "wwn-b", "removing")}})
	if got != "san_servers remove_disks remove_node leave" {
		t.Errorf("a host leaving with a LUN of its own: %s", got)
	}
	got = plan(StorageTaskRemoveDisk, &StorageTaskScope{Nodes: base(), Disks: []*model.StorageClusterDisk{disk(1, 1, "wwn-a", "removing"), disk(2, 2, "wwn-a", "removing")}})
	if got != "remove_disks release_disks" {
		t.Errorf("a LUN leaving: %s", got)
	}
	if got = plan(StorageTaskReplaceDisk, &StorageTaskScope{Nodes: base(), Disks: active}); !strings.HasPrefix(got, "error:") {
		t.Errorf("replace: %s", got)
	}
	// What leaves with a disk: the records of the same LUN
	sib := gpfsBackend{}.DiskSiblings(san, active[0], append(active, disk(5, 1, "wwn-b", "active")))
	if len(sib) != 1 || sib[0].ID != 2 {
		t.Errorf("siblings %v", sib)
	}
	if sib := (gpfsBackend{}).DiskSiblings(&model.StorageCluster{Layout: model.StorageLayoutReplica}, active[0], active); sib != nil {
		t.Errorf("a replica disk has no siblings: %v", sib)
	}
}
