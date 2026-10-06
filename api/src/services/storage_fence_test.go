/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"reflect"
	"testing"

	"api/src/model"
)

// Where the fence of a host runs (shared-storage-design.md §11.2): never on the host itself; on an admin host of a
// managed cluster, on any other client of an imported Ceph cluster, and nowhere on an imported GPFS cluster
func TestStorageFencePlan(t *testing.T) {
	nodes := []*model.StorageClusterNode{
		{Hostid: 1, Roles: "admin,mon,mgr", Status: model.StorageNodeActive},
		{Hostid: 2, Roles: "admin,mon", Status: model.StorageNodeActive},
		{Hostid: 3, Roles: "admin,mon", Status: model.StorageNodeLeaving},
		{Hostid: 4, Roles: "client", Status: model.StorageNodeActive},
	}
	managed := &model.StorageCluster{Mode: model.StorageModeManaged}
	external := &model.StorageCluster{Mode: model.StorageModeExternal}
	cases := []struct {
		name    string
		backend storageFencer
		cluster *model.StorageCluster
		nodes   []*model.StorageClusterNode
		host    int32
		want    []int32
	}{
		{"ceph managed", cephBackend{}, managed, nodes, 1, []int32{2}},
		{"ceph managed, client down", cephBackend{}, managed, nodes, 4, []int32{1, 2}},
		{"ceph managed, only admin down", cephBackend{}, managed, nodes[:1], 1, nil},
		{"ceph external", cephBackend{}, external, nodes, 2, []int32{1, 3, 4}},
		{"ceph external, alone", cephBackend{}, external, nodes[3:], 4, nil},
		{"gpfs managed", gpfsBackend{}, managed, nodes, 2, []int32{1}},
		{"gpfs external", gpfsBackend{}, external, nodes, 4, nil},
	}
	for _, c := range cases {
		plan, why := c.backend.FencePlan(nil, c.cluster, c.nodes, c.host, StorageTaskFence)
		if c.want == nil {
			if plan != nil || why == "" {
				t.Errorf("%s: fence planned on %v, want refused", c.name, plan)
			}
			continue
		}
		if plan == nil {
			t.Errorf("%s: refused (%s)", c.name, why)
			continue
		}
		if !reflect.DeepEqual(plan.Hostids, c.want) || plan.Scope != model.StorageStepScopeAdmin || plan.Name != StorageTaskFence {
			t.Errorf("%s: plan %+v, want hosts %v", c.name, plan, c.want)
		}
	}
}
