/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Upgrading the software of a managed cluster (shared-storage-design.md §7.7, §8.7). What every kind shares lives
// here: the checks, the request, and the step that moves the instances off a host before its storage stops (drain);
// what a release is and the steps come from the backend (storageUpgrader, TaskPlan)

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"
)

const StorageTaskUpgrade = "upgrade"

// StorageUpgrade is what an upgrade goes to
type StorageUpgrade struct {
	// GPFS: the package of the new release, from the package repository (its license accepted)
	PackageUUID string `json:"package"`
	// GPFS: once every host runs the new release, raise the cluster and its file systems to it (mmchconfig
	// release=LATEST, mmchfs -V full): irreversible, so a step of its own
	Finalize bool `json:"finalize"`
	// Ceph: the image of the daemons; empty: the release the hosts install from their distribution
	Image string `json:"image"`
}

// storageUpgradeParams are the parameters of an upgrade task
type storageUpgradeParams struct {
	PackageID int64  `json:"package_id,omitempty"`
	Version   string `json:"version,omitempty"`
	Finalize  bool   `json:"finalize,omitempty"`
	Image     string `json:"image,omitempty"`
}

// storageUpgrader is the part of a backend that knows its releases: it turns a request into the parameters of the
// task, or refuses it
type storageUpgrader interface {
	UpgradeParams(ctx context.Context, cluster *model.StorageCluster, req *StorageUpgrade) (*storageUpgradeParams, error)
}

// Upgrade starts the upgrade of a managed cluster. Every member must be online and in the cluster: a rolling upgrade
// takes one host after the other and a host away would be left behind
func (a *StorageClusterAdmin) Upgrade(ctx context.Context, uuid string, req *StorageUpgrade) (*model.StorageTask, error) {
	if err := requireSystemAdmin(ctx); err != nil {
		return nil, err
	}
	cluster, nodes, disks, backend, err := storageClusterForChange(ctx, uuid, func(c *StorageCapabilities) bool { return c.Upgrade })
	if err != nil {
		return nil, err
	}
	upgrader, ok := backend.(storageUpgrader)
	if !ok {
		return nil, planError("%s clusters can not be upgraded by CloudLand", cluster.Kind)
	}
	if req == nil {
		req = &StorageUpgrade{}
	}
	db := dbs.DBContext(ctx)
	for _, n := range nodes {
		if n.Status != model.StorageNodeActive {
			return nil, planError("%s is %s in the cluster: every member is upgraded", hostName(db, n.Hostid), n.Status)
		}
		if _, online := hostOnline(db, n.Hostid); !online {
			return nil, planError("%s is offline: every member is upgraded", hostName(db, n.Hostid))
		}
	}
	params, err := upgrader.UpgradeParams(ctx, cluster, req)
	if err != nil {
		return nil, err
	}
	steps, err := backend.TaskPlan(StorageTaskUpgrade, cluster, &StorageTaskScope{Nodes: nodes, Disks: disks, Upgrade: params})
	if err != nil {
		return nil, err
	}
	return startStorageTask(ctx, &storageTaskSpec{ClusterID: cluster.ID, Backend: cluster.Kind, Kind: StorageTaskUpgrade, Plan: steps, Params: params})
}

// storageVersionLess compares dotted releases number by number (6.0.0.2 < 6.0.1.1, 19.2.3 < 19.2.10)
func storageVersionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// storageDrainHosts orders the hosts of a rolling upgrade: the hosts out of the quorum first, the quorum ones last,
// so the quorum loses at most one member at a time and as late as possible
func storageDrainHosts(nodes []*model.StorageClusterNode, quorumRole string) []int32 {
	first, last := []int32{}, []int32{}
	for _, n := range nodes {
		if quorumRole != "" && n.HasRole(quorumRole) {
			last = append(last, n.Hostid)
		} else {
			first = append(first, n.Hostid)
		}
	}
	return append(first, last...)
}

// storageDrainPlan is the drain of each host before the step that stops its storage
func storageDrainPlan(hosts []int32, step string, timeout time.Duration) []*StorageStepPlan {
	plan := []*StorageStepPlan{}
	for _, h := range hosts {
		plan = append(plan,
			&StorageStepPlan{Name: "drain", Scope: model.StorageStepScopeNodes, Hostids: []int32{h}, Timeout: 6 * time.Hour},
			&StorageStepPlan{Name: step, Scope: model.StorageStepScopeNodes, Hostids: []int32{h}, Timeout: timeout})
	}
	return plan
}

// storageDrainControl moves off a host the instances that run there with a disk in a pool of the cluster, before its
// storage stops (an upgrade, §7.7), with the migrations maintenance mode makes; shut off ones stay. Done once none is
// left there; an instance that can not be moved fails the step, which a retry tries again (after an admin moved it,
// or made room elsewhere)
func storageDrainControl(ctx context.Context, task *model.StorageTask, step *model.StorageTaskStep) (bool, error) {
	db := dbs.DBContext(ctx)
	hosts := parseHostids(step.Hostids)
	if len(hosts) != 1 || step.StartedAt == nil {
		return false, fmt.Errorf("the step names no host")
	}
	host := hosts[0]
	var poolIDs []int64
	if err := db.Model(&model.StoragePool{}).Where("cluster_id = ?", task.ClusterID).Pluck("id", &poolIDs).Error; err != nil {
		return false, err
	}
	if len(poolIDs) == 0 {
		return true, nil
	}
	instances := []*model.Instance{}
	if err := db.Preload("Volumes").Preload("Image").Preload("Flavor").
		Where("hyper = ? AND status IN ? AND id IN (SELECT instance_id FROM volumes WHERE storage_pool_id IN ? AND deleted_at IS NULL)", host,
			[]model.InstanceStatus{model.InstanceStatusRunning, model.InstanceStatusPaused, model.InstanceStatusMigrating}, poolIDs).
		Find(&instances).Error; err != nil {
		return false, err
	}
	if len(instances) == 0 {
		return true, nil
	}
	move := []*model.Instance{}
	for _, inst := range instances {
		m := &model.Migration{}
		err := db.Where("instance_id = ? AND source_hyper = ? AND created_at >= ?", inst.ID, host, *step.StartedAt).Order("id DESC").Take(m).Error
		switch {
		case err == nil && (m.Status == "failed" || m.Status == "not_doing" || m.Status == "source_rollback" || m.Status == "timeout" ||
			m.Status == "not_supported"):
			reason := m.Status
			phase := &model.Task{}
			if db.Where("mission = ? AND source = ? AND message <> ''", m.ID, model.TaskSourceMigration).Order("id DESC").Take(phase).Error == nil {
				reason = phase.Message
			}
			return false, fmt.Errorf("instance %s could not be moved off %s (%s): move it, then retry", inst.Hostname, hostName(db, host), reason)
		case err == nil || inst.Status == model.InstanceStatusMigrating:
			// Its migration goes on
		default:
			move = append(move, inst)
		}
	}
	if len(move) > 0 {
		actx := (&MemberShip{SystemRole: model.SystemAdmin, UserName: task.CreatorName}).SetContext(ctx)
		_, results, err := migrationAdmin.Create(actx, fmt.Sprintf("upgrade-%d-hyper-%d", task.ID, host), move, false, -1, nil, true)
		if err != nil {
			return false, err
		}
		// Refused before a migration was recorded (nowhere to go, a state that does not migrate)
		for _, r := range results {
			if r != nil && r.Error != nil && r.Instance != nil {
				return false, fmt.Errorf("instance %s could not be moved off %s (%v): move it, then retry", r.Instance.Hostname, hostName(db, host), r.Error)
			}
		}
		logger.Ctx(ctx).Infof("Storage task %d moves %d instances off host %d", task.ID, len(move), host)
	}
	return false, nil
}

// storageUpgradeOf reads the parameters of an upgrade task
func storageUpgradeOf(task *model.StorageTask) *storageUpgradeParams {
	p := &storageUpgradeParams{}
	_ = json.Unmarshal([]byte(task.Params), p)
	return p
}

// An image of the daemons as docker names it (the same pattern ceph_install.sh checks)
var storageImageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9./:_@-]{0,199}$`)

func validStorageImage(image string) error {
	if image != "" && !storageImageRe.MatchString(image) {
		return NewCLError(ErrInvalidParameter, "Invalid image "+image, nil)
	}
	return nil
}
