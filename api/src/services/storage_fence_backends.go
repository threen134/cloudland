/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The fences of each storage type (shared-storage-design.md §11.2): where they run and what their script gets

import (
	"context"
	"encoding/json"
	"fmt"

	"api/src/model"

	"gorm.io/gorm"
)

func init() {
	registerStorageFenceKinds(model.StorageKindGPFS,
		&storageStepDef{Script: "gpfs_cluster.sh", Input: gpfsFenceInput("fence")},
		&storageStepDef{Script: "gpfs_cluster.sh", Input: gpfsFenceInput("unfence")})
	registerStorageFenceKinds(model.StorageKindCeph,
		&storageStepDef{Script: "ceph_cluster.sh", Input: cephFenceInput("fence")},
		&storageStepDef{Script: "ceph_cluster.sh", Input: cephFenceInput("unfence")})
}

// fenceHosts are the hosts that can run the fence of a host: the given ones but that host
func fenceHosts(candidates []int32, hostid int32) []int32 {
	hosts := []int32{}
	for _, h := range candidates {
		if h != hostid {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// FencePlan of GPFS: an admin host expels the node. CloudLand runs no admin command on an imported cluster
func (gpfsBackend) FencePlan(db *gorm.DB, cluster *model.StorageCluster, nodes []*model.StorageClusterNode, hostid int32, kind string) (*StorageStepPlan, string) {
	if cluster.Mode == model.StorageModeExternal {
		return nil, "CloudLand runs no admin command on an imported GPFS cluster"
	}
	admins := fenceHosts(gpfsWorkingAdmins(nodes), hostid)
	if len(admins) == 0 {
		return nil, "the host is the only admin host of the cluster"
	}
	return &StorageStepPlan{Name: kind, Scope: model.StorageStepScopeAdmin, Hostids: admins, Timeout: storageFenceTimeout}, ""
}

// FencePlan of Ceph: an admin host of a managed cluster blocklists the address; on an imported one any other host
// with the CloudLand client, which may add to the blocklist (not remove from it)
func (cephBackend) FencePlan(db *gorm.DB, cluster *model.StorageCluster, nodes []*model.StorageClusterNode, hostid int32, kind string) (*StorageStepPlan, string) {
	var hosts []int32
	if cluster.Mode == model.StorageModeExternal {
		hosts = fenceHosts(nodeHostids(nodes, ""), hostid)
		if len(hosts) == 0 {
			return nil, "no other host of the cluster"
		}
	} else if hosts = fenceHosts(cephAdmins(kind, nodes), hostid); len(hosts) == 0 {
		return nil, "the host is the only admin host of the cluster"
	}
	return &StorageStepPlan{Name: kind, Scope: model.StorageStepScopeAdmin, Hostids: hosts, Timeout: storageFenceTimeout}, ""
}

func fenceTaskParams(task *model.StorageTask) (*storageFenceParams, error) {
	p := &storageFenceParams{}
	if err := json.Unmarshal([]byte(task.Params), p); err != nil || p.Hostid <= 0 {
		return nil, fmt.Errorf("the task names no host to fence")
	}
	return p, nil
}

func fenceHostIP(db *gorm.DB, hostid int32) (string, error) {
	ips, err := storageHostIPs(db, []int32{hostid})
	if err != nil {
		return "", err
	}
	if ips[hostid] == "" {
		return "", fmt.Errorf("%s has no address", hostName(db, hostid))
	}
	return ips[hostid], nil
}

func gpfsFenceInput(action string) func(context.Context, *gorm.DB, *model.StorageTask, *model.StorageTaskStep, int32) (interface{}, error) {
	return func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		p, err := fenceTaskParams(task)
		if err != nil {
			return nil, err
		}
		cluster := &model.StorageCluster{}
		if err = db.Take(cluster, task.ClusterID).Error; err != nil {
			return nil, err
		}
		ip, err := fenceHostIP(db, p.Hostid)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"action": action, "cluster_uuid": cluster.UUID, "ip": ip}, nil
	}
}

func cephFenceInput(action string) func(context.Context, *gorm.DB, *model.StorageTask, *model.StorageTaskStep, int32) (interface{}, error) {
	return func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		p, err := fenceTaskParams(task)
		if err != nil {
			return nil, err
		}
		cluster := &model.StorageCluster{}
		if err = db.Take(cluster, task.ClusterID).Error; err != nil {
			return nil, err
		}
		address := p.Target
		if address == "" {
			if address, err = fenceHostIP(db, p.Hostid); err != nil {
				return nil, err
			}
		}
		info := cephInfoOf(cluster)
		in := map[string]interface{}{"action": action, "cluster_uuid": cluster.UUID, "fsid": info.Fsid, "address": address,
			"expire": storageBlocklistExpire}
		if cluster.Mode == model.StorageModeExternal {
			in["client_user"] = info.ClientUser
		}
		return in, nil
	}
}
