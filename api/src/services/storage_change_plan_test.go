/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The plans of a change of roles (shared-storage-design.md §13.1): the cluster commands never run on the host that
// changes (a host just made admin has no admin keyring yet, Ceph hands it over some time after _admin), unless it is
// the one admin before and after; a Ceph client that gets its first daemon role is installed as a cephadm host first

import (
	"fmt"
	"testing"

	"api/src/model"
)

func TestStorageChangeRolesPlan(t *testing.T) {
	cluster := &model.StorageCluster{Kind: model.StorageKindCeph, Mode: model.StorageModeManaged}
	nodes := func(roles ...string) []*model.StorageClusterNode {
		out := []*model.StorageClusterNode{}
		for i, r := range roles {
			out = append(out, &model.StorageClusterNode{Hostid: int32(i + 1), Roles: r, Status: model.StorageNodeActive})
		}
		return out
	}
	describe := func(plan []*StorageStepPlan) string {
		s := ""
		for _, p := range plan {
			s += fmt.Sprintf("%s%v ", p.Name, p.Hostids)
		}
		return s
	}
	ceph := cephBackend{}
	// Host 1 gets admin (its roles in the nodes are the new ones): the labels change on host 2, the admin before
	plan, err := ceph.TaskPlan(StorageTaskChangeRoles, cluster, &StorageTaskScope{
		Nodes: nodes("admin,mon,osd", "admin,mon,mgr,osd", "mon,osd"), Changed: 1, ChangedFrom: []string{"mon", "osd"}})
	must(t, err)
	if got := describe(plan); got != "ssh_trust[1 2 3] set_labels[2] finish[1] " {
		t.Fatalf("a host made admin: %s", got)
	}
	// A client gets mgr: installed as a cephadm host first
	plan, err = ceph.TaskPlan(StorageTaskChangeRoles, cluster, &StorageTaskScope{
		Nodes: nodes("admin,mon,mgr,osd", "mon,osd", "mon,osd", "mgr"), Changed: 4, ChangedFrom: []string{"client"}})
	must(t, err)
	if got := describe(plan); got != "install[4] ssh_trust[1 2 3 4] set_labels[1] finish[4] " {
		t.Fatalf("a client made mgr: %s", got)
	}
	// The one admin changes its other roles: it runs the change itself, there is no other
	plan, err = ceph.TaskPlan(StorageTaskChangeRoles, cluster, &StorageTaskScope{
		Nodes: nodes("admin,mon,osd", "mon,mgr,osd", "mon,osd"), Changed: 1, ChangedFrom: []string{"admin", "mon", "mgr", "osd"}})
	must(t, err)
	if got := describe(plan); got != "ssh_trust[1 2 3] set_labels[1] finish[1] " {
		t.Fatalf("the one admin: %s", got)
	}
	// GPFS: the same choice of the admin host
	gcluster := &model.StorageCluster{Kind: model.StorageKindGPFS, Mode: model.StorageModeManaged}
	plan, err = gpfsBackend{}.TaskPlan(StorageTaskChangeRoles, gcluster, &StorageTaskScope{
		Nodes: nodes("admin,quorum,nsd", "admin,quorum,nsd", "quorum,nsd"), Changed: 2, ChangedFrom: []string{"quorum", "nsd"}})
	must(t, err)
	if got := describe(plan); got != "ssh_trust[1 2 3] change_roles[1] finish[2] " {
		t.Fatalf("gpfs: %s", got)
	}
}
