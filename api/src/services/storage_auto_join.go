/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Hosts that join a managed cluster as clients on their own (shared-storage-design.md §6.3, §6.2.1). A cluster lists
// zones (auto_join_zones); every online host of those zones that is not in the cluster is queued in pending_clients
// and, once the structural slot is free, joins with an add_nodes task of its own, the roles of a client. One host per
// task, so a host that fails (its system out of the support matrix, the precheck says so) is the only one that does:
// the background loop aborts that task, the host is marked failed with the reason and left alone; its member record
// stays in error until an admin removes it, like any change that was aborted. Runs in the leader loop only.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StoragePendingWaiting = "pending"
	StoragePendingJoining = "joining"
	StoragePendingFailed  = "failed"

	storageAutoJoinRemoveFirst = "its member record from the aborted join is still there: remove the host from the cluster, then retry"
)

// StoragePendingClient is a host of the auto join zones on its way into a cluster
type StoragePendingClient struct {
	Hostid int32     `json:"hostid"`
	Status string    `json:"status"`
	Reason string    `json:"reason,omitempty"`
	TaskID int64     `json:"task_id,omitempty"`
	Since  time.Time `json:"since"`
}

// ParseStoragePendingClients reads storage_clusters.pending_clients
func ParseStoragePendingClients(raw string) []*StoragePendingClient {
	list := []*StoragePendingClient{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &list)
	}
	return list
}

// ParseStorageZones reads storage_clusters.auto_join_zones
func ParseStorageZones(raw string) []int64 {
	ids := []int64{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &ids)
	}
	return ids
}

// storageAutoAdmin is the operator of what the background loop starts or aborts
func storageAutoAdmin(ctx context.Context) context.Context {
	return (&MemberShip{SystemRole: model.SystemAdmin, UserName: "auto-join"}).SetContext(ctx)
}

// StorageClusterUpdate changes the settings of a cluster; nil fields stay
type StorageClusterUpdate struct {
	Description *string
	// Zone UUIDs whose hosts join as clients; an empty list turns it off
	AutoJoinZones *[]string
	// Only hosts registered from now on join (true), or every host of the zones (false); nil leaves it
	AutoJoinNewOnly *bool
	// Forget the hosts that failed to join automatically, so they are tried again
	RetryAutoJoin bool
}

// Update changes the description and the auto join settings of a cluster
func (a *StorageClusterAdmin) Update(ctx context.Context, uuid string, req *StorageClusterUpdate) (cluster *model.StorageCluster, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	err = db.Transaction(func(tx *gorm.DB) error {
		cluster = &model.StorageCluster{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("uuid = ?", uuid).Take(cluster).Error; err != nil {
			return NewCLError(ErrStorageClusterNotFound, "Storage cluster not found", err)
		}
		update := map[string]interface{}{}
		if req.Description != nil {
			if len(*req.Description) > 256 {
				return planError("The description is longer than 256 characters")
			}
			update["description"] = *req.Description
		}
		if req.AutoJoinZones != nil {
			backend, err := storageBackendOf(cluster.Kind)
			if err != nil {
				return err
			}
			if len(*req.AutoJoinZones) > 0 && (cluster.Mode != model.StorageModeManaged || !storageClusterCapabilities(backend, cluster).AddNodes) {
				return planError("Hosts join only a managed cluster on their own")
			}
			ids := []int64{}
			for _, z := range *req.AutoJoinZones {
				zone := &model.Zone{}
				if err := tx.Where("uuid = ?", z).Take(zone).Error; err != nil {
					return planError("Zone %s not found", z)
				}
				ids = append(ids, zone.ID)
			}
			raw, _ := json.Marshal(ids)
			update["auto_join_zones"] = string(raw)
			if len(ids) == 0 {
				update["auto_join_since"] = nil
				// Nothing waits any more; a host already joining finishes its task
				list := []*StoragePendingClient{}
				for _, p := range ParseStoragePendingClients(cluster.PendingClients) {
					if p.Status == StoragePendingJoining {
						list = append(list, p)
					}
				}
				update["pending_clients"] = storagePendingJSON(list)
			}
		}
		if req.AutoJoinNewOnly != nil {
			zones := cluster.AutoJoinZones
			if z, ok := update["auto_join_zones"]; ok {
				zones = z.(string)
			}
			switch {
			case !*req.AutoJoinNewOnly:
				update["auto_join_since"] = nil
			case len(ParseStorageZones(zones)) == 0:
				return planError("Choose the zones whose hosts join on their own first")
			case cluster.AutoJoinSince == nil:
				// From now on: the hosts there already are left as they are, one waiting is no longer queued
				update["auto_join_since"] = time.Now()
			}
		}
		if req.RetryAutoJoin {
			// A host whose join was aborted keeps its member record (in error) until an admin removes it, and a host
			// with a record is never queued: it stays listed, told what to do, instead of quietly dropping out
			members := map[int32]bool{}
			nodes := []*model.StorageClusterNode{}
			if err := tx.Where("cluster_id = ?", cluster.ID).Find(&nodes).Error; err != nil {
				return err
			}
			for _, n := range nodes {
				members[n.Hostid] = true
			}
			list := []*StoragePendingClient{}
			for _, p := range ParseStoragePendingClients(cluster.PendingClients) {
				if p.Status == StoragePendingFailed && members[p.Hostid] {
					p.Reason = storageAutoJoinRemoveFirst
					list = append(list, p)
				} else if p.Status != StoragePendingFailed {
					list = append(list, p)
				}
			}
			if _, ok := update["pending_clients"]; !ok {
				update["pending_clients"] = storagePendingJSON(list)
			}
		}
		if len(update) == 0 {
			return nil
		}
		if err := tx.Model(cluster).Updates(update).Error; err != nil {
			return err
		}
		return tx.Take(cluster, cluster.ID).Error
	})
	return
}

func storagePendingJSON(list []*StoragePendingClient) string {
	if len(list) == 0 {
		return ""
	}
	raw, _ := json.Marshal(list)
	return string(raw)
}

// maintainAutoJoin is the round of the auto join of a cluster: the task of the host joining is followed (a failed one
// is aborted), the hosts of the zones not in the cluster are queued, and the next one starts when the slot is free
func maintainAutoJoin(ctx context.Context, cluster *model.StorageCluster) {
	if cluster.Mode != model.StorageModeManaged || (cluster.AutoJoinZones == "" && cluster.PendingClients == "") {
		return
	}
	backend, err := storageBackendOf(cluster.Kind)
	if err != nil || !storageClusterCapabilities(backend, cluster).AddNodes {
		return
	}
	db := dbs.DBContext(ctx)
	var abort []int64
	var next *StoragePendingClient
	err = db.Transaction(func(tx *gorm.DB) error {
		c := &model.StorageCluster{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(c, cluster.ID).Error; err != nil {
			return err
		}
		list := ParseStoragePendingClients(c.PendingClients)
		before := storagePendingJSON(list)
		nodes := []*model.StorageClusterNode{}
		if err := tx.Where("cluster_id = ?", c.ID).Find(&nodes).Error; err != nil {
			return err
		}
		members := map[int32]*model.StorageClusterNode{}
		for _, n := range nodes {
			members[n.Hostid] = n
		}
		// The task of the host joining
		kept := []*StoragePendingClient{}
		for _, p := range list {
			if p.Status == StoragePendingJoining {
				task := &model.StorageTask{}
				if tx.Take(task, p.TaskID).Error != nil {
					p.Status, p.Reason = StoragePendingFailed, "its task is gone"
				} else {
					switch task.Status {
					case model.StorageTaskSucceeded:
						continue
					case model.StorageTaskFailed:
						abort = append(abort, task.ID)
					case model.StorageTaskAborted:
						p.Status = StoragePendingFailed
						p.Reason = storageAutoJoinReason(tx, task, members[p.Hostid])
					}
				}
			}
			kept = append(kept, p)
		}
		list = kept
		// The hosts of the zones that are not in the cluster
		zones := ParseStorageZones(c.AutoJoinZones)
		queued := map[int32]bool{}
		for _, p := range list {
			queued[p.Hostid] = true
		}
		inZones := map[int32]bool{}
		// With new only, a host registered before the setting is not one of the zones' hosts to join
		isNew := func(h *model.Hyper) bool { return c.AutoJoinSince == nil || h.CreatedAt.After(*c.AutoJoinSince) }
		if len(zones) > 0 {
			hypers := []*model.Hyper{}
			if err := tx.Where("zone_id IN ? AND hostid >= 0", zones).Order("hostid").Find(&hypers).Error; err != nil {
				return err
			}
			for _, h := range hypers {
				if !isNew(h) {
					continue
				}
				inZones[h.Hostid] = true
				if members[h.Hostid] != nil || queued[h.Hostid] {
					continue
				}
				if _, online := hostOnline(tx, h.Hostid); !online {
					continue
				}
				// A host that may not be a client while in another cluster of the kind is not queued at all
				if storageClientConflict(tx, backend, c, h.Hostid) != "" {
					continue
				}
				list = append(list, &StoragePendingClient{Hostid: h.Hostid, Status: StoragePendingWaiting, Since: time.Now()})
			}
		}
		// A waiting host that left the zones (or is older than the new only setting), or got into the cluster another
		// way, waits no more
		kept = []*StoragePendingClient{}
		for _, p := range list {
			if p.Status == StoragePendingWaiting && (!inZones[p.Hostid] || members[p.Hostid] != nil) {
				continue
			}
			kept = append(kept, p)
		}
		list = kept
		// The next host, when the slot is free and nobody joins
		if c.Status == model.StorageClusterReady && c.ActiveTask == 0 {
			joining := false
			for _, p := range list {
				joining = joining || p.Status == StoragePendingJoining
			}
			for _, p := range list {
				if !joining && p.Status == StoragePendingWaiting {
					if _, online := hostOnline(tx, p.Hostid); online {
						next = p
						break
					}
				}
			}
		}
		if after := storagePendingJSON(list); after != before {
			return tx.Model(&model.StorageCluster{}).Where("id = ?", c.ID).Update("pending_clients", after).Error
		}
		return nil
	})
	if err != nil {
		logger.Ctx(ctx).Warningf("Auto join of storage cluster %s: %v", cluster.Name, err)
		return
	}
	admin := storageAutoAdmin(ctx)
	for _, id := range abort {
		if err := AbortStorageTask(admin, id); err != nil {
			logger.Ctx(ctx).Warningf("Auto join of storage cluster %s: aborting task %d: %v", cluster.Name, id, err)
		}
	}
	if next != nil {
		autoJoinStart(ctx, cluster, next)
	}
}

// autoJoinStart starts the task of the next host joining and records it. Another task may have taken the cluster
// since the round looked: the host then waits for the next round, it did not fail
func autoJoinStart(ctx context.Context, cluster *model.StorageCluster, next *StoragePendingClient) {
	db := dbs.DBContext(ctx)
	task, err := StorageClusters.AddNodes(storageAutoAdmin(ctx), cluster.UUID, &StorageClusterExpand{
		Nodes: []*StorageNodePlan{{Hostid: next.Hostid, Roles: []string{model.StorageRoleClient}}}, AllowUnsupported: cluster.Unsupported})
	status, reason, taskID := StoragePendingJoining, "", int64(0)
	var clErr *CLError
	switch {
	case err != nil && errors.As(err, &clErr) && clErr.Code == ErrStorageClusterBusy:
		// Another task took the cluster after the round looked: the host waits for the next round
		return
	case err != nil:
		status, reason = StoragePendingFailed, planMessage(err)
	default:
		taskID = task.ID
	}
	_ = db.Transaction(func(tx *gorm.DB) error {
		c := &model.StorageCluster{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(c, cluster.ID).Error; err != nil {
			return err
		}
		list := ParseStoragePendingClients(c.PendingClients)
		for _, p := range list {
			if p.Hostid == next.Hostid && p.Status == StoragePendingWaiting {
				p.Status, p.Reason, p.TaskID, p.Since = status, reason, taskID, time.Now()
			}
		}
		return tx.Model(&model.StorageCluster{}).Where("id = ?", c.ID).Update("pending_clients", storagePendingJSON(list)).Error
	})
}

// storageClientConflict tells why a host may not be a client of a cluster while in another cluster of the kind
func storageClientConflict(db *gorm.DB, backend StorageBackend, cluster *model.StorageCluster, hostid int32) string {
	why, err := storageHostConflict(db, backend, cluster.Kind, cluster.ID, hostid, []string{model.StorageRoleClient})
	if err != nil {
		return err.Error()
	}
	return why
}

// storageAutoJoinReason is why the task of a host joining failed: the message of the run that failed, else the task's
func storageAutoJoinReason(db *gorm.DB, task *model.StorageTask, node *model.StorageClusterNode) string {
	run := &model.StorageTaskRun{}
	err := db.Where("step_id IN (?) AND status = ?", db.Model(&model.StorageTaskStep{}).Select("id").Where("task_id = ?", task.ID), model.StorageRunFailed).
		Order("id DESC").Take(run).Error
	reason := ""
	if err == nil && run.Message != "" {
		reason = run.Message
	} else if task.Message != "" {
		reason = task.Message
	} else if node != nil && node.Reason != "" {
		reason = node.Reason
	} else {
		reason = fmt.Sprintf("task %s was aborted", task.UUID)
	}
	if len(reason) > 300 {
		reason = reason[:300]
	}
	return reason
}
