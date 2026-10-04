/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// CloudLand pools on Ceph clusters (shared-storage-design.md §8.3, §9.4): every pool is an RBD pool, a volume the RBD
// image volume-<id> in it. On a managed cluster CloudLand makes the RBD pool (cl_<uuid prefix>) with the placement
// rule of the media asked for; on an imported cluster an RBD pool its admins made is registered by writing the
// marker object into it. The hosts reach a pool as the client user of the cluster, with the configuration and keyring
// client_setup wrote under /etc/ceph and the libvirt secret holding the same key.

import (
	"context"
	"fmt"
	"time"

	"api/src/model"

	"gorm.io/gorm"
)

// rbdDriver is the data path of the pools of Ceph clusters
type rbdDriver struct{}

func (rbdDriver) Name() string   { return model.StorageDriverCephRBD }
func (rbdDriver) Family() string { return PoolFamilyBlock }
func (rbdDriver) Format() string { return "raw" }

func (rbdDriver) VolumeRef(pool *model.StoragePool, volume *model.Volume) string {
	return fmt.Sprintf("volume-%d", volume.ID)
}

// DriverArgs: what a host needs to reach the RBD pool. The monitors are in the configuration file of the cluster,
// which the disks name too (<config file=.../>): new monitors do not change the pools or the disks
func (d rbdDriver) DriverArgs(pool *model.StoragePool) map[string]interface{} {
	dp := poolDriverParams(pool)
	return map[string]interface{}{"driver": d.Name(), "pool": pool.UUID, "cluster": stringParam(dp, "cluster"), "conf": stringParam(dp, "conf"),
		"user": stringParam(dp, "user"), "secret_uuid": stringParam(dp, "secret_uuid"), "ceph_pool": stringParam(dp, "ceph_pool"),
		"quota_bytes": pool.QuotaBytes}
}

// ImageBaseRef: the copy of an image is the RBD image image-<id>-<prefix>, with its snapshot base
func (rbdDriver) ImageBaseRef(pool *model.StoragePool, image *model.Image) string {
	return image.FileBase()
}

// QemuSource: QEMU tools reach the image through librbd, with the configuration file of the cluster and the client
func (rbdDriver) QemuSource(pool *model.StoragePool, volume *model.Volume) string {
	p := poolDriverParams(pool)
	return fmt.Sprintf("rbd:%s/%s:id=%s:conf=%s", stringParam(p, "ceph_pool"), volume.Path, stringParam(p, "user"), stringParam(p, "conf"))
}

// CapacityGroup: the pools of a managed cluster without a quota and with the same placement rule draw on the same
// OSDs (§9.5). A pool of an imported cluster is admitted on its own: what else its RBD pool shares OSDs with is not
// known
func (d rbdDriver) CapacityGroup(pool *model.StoragePool) string {
	dp := poolDriverParams(pool)
	if pool.QuotaBytes > 0 || dp["external"] == true {
		return ""
	}
	return fmt.Sprintf("ceph/%d/%s", pool.ClusterID, stringParam(dp, "crush_rule"))
}

func init() {
	registerPoolDriver(rbdDriver{})
}

// cephPoolTaskPlan lays out the pool tasks like the GPFS ones: the change on the cluster first, then the pool lists of
// the members; a removal takes the pool out of the lists first. On an imported cluster any of its hosts registers
// the pool (the client user may write the marker object), nothing else is changed
func cephPoolTaskPlan(task string, cluster *model.StorageCluster, all, admins []int32) ([]*StorageStepPlan, error) {
	step := func(name, scope string, hosts []int32, timeout time.Duration) *StorageStepPlan {
		return &StorageStepPlan{Name: name, Scope: scope, Hostids: hosts, Timeout: timeout}
	}
	n, a := model.StorageStepScopeNodes, model.StorageStepScopeAdmin
	if cluster.Mode == model.StorageModeExternal {
		switch task {
		case StorageTaskCreatePool:
			return []*StorageStepPlan{step("register_pool", a, admins, 10*time.Minute), step("sync_pools", n, all, 5*time.Minute)}, nil
		case StorageTaskDeletePool:
			return []*StorageStepPlan{step("sync_pools", n, all, 5*time.Minute), step("unregister_pool", a, admins, 10*time.Minute)}, nil
		}
		return nil, planError("A pool of an imported cluster has no %s", task)
	}
	switch task {
	case StorageTaskCreatePool:
		return []*StorageStepPlan{step("create_pool", a, admins, 15*time.Minute), step("sync_pools", n, all, 5*time.Minute)}, nil
	case StorageTaskUpdatePool:
		return []*StorageStepPlan{step("update_pool", a, admins, 10*time.Minute)}, nil
	case StorageTaskDeletePool:
		return []*StorageStepPlan{step("sync_pools", n, all, 5*time.Minute), step("delete_pool", a, admins, 30*time.Minute)}, nil
	}
	return nil, planError("Ceph clusters have no task %s", task)
}

func init() {
	registerSharedPoolTasks(model.StorageKindCeph, map[string]map[string]*storageStepDef{
		StorageTaskCreatePool: {
			"create_pool":   {Script: "ceph_pool.sh", Input: cephPoolInput("create")},
			"register_pool": {Script: "ceph_pool.sh", Input: cephPoolInput("register")},
		},
		StorageTaskUpdatePool: {"update_pool": {Script: "ceph_pool.sh", Input: cephPoolInput("quota")}},
		StorageTaskDeletePool: {
			"delete_pool":     {Script: "ceph_pool.sh", Input: cephPoolInput("delete")},
			"unregister_pool": {Script: "ceph_pool.sh", Input: cephPoolInput("unregister")},
		},
	})
}

func cephPoolInput(action string) storageStepInput {
	return func(ctx context.Context, db *gorm.DB, task *model.StorageTask, step *model.StorageTaskStep, hostid int32) (interface{}, error) {
		cluster, _, _, err := storageClusterOfTask(db, task)
		if err != nil {
			return nil, err
		}
		pool, args, err := sharedPoolOfTask(db, task)
		if err != nil {
			return nil, err
		}
		dp := poolDriverParams(pool)
		info := cephInfoOf(cluster)
		in := map[string]interface{}{"action": action, "cluster_uuid": cluster.UUID, "fsid": info.Fsid, "pool_uuid": pool.UUID,
			"ceph_pool": stringParam(dp, "ceph_pool"), "conf": stringParam(dp, "conf"), "user": stringParam(dp, "user")}
		switch action {
		case "create":
			in["crush_rule"] = stringParam(dp, "crush_rule")
			in["media"] = pool.Media
			in["replicas"] = pool.Replicas
			in["quota_bytes"] = pool.QuotaBytes
		case "quota":
			in["quota_bytes"] = args.QuotaBytes
		case "delete", "unregister":
			// The copies of images CloudLand made in the pool go first (§9.6); nothing is cloned from them any more
			if in["bases"], err = poolImageBases(db, pool); err != nil {
				return nil, err
			}
		}
		return in, nil
	}
}
