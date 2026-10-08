/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Recovering the instances of a host that is down on other hosts (shared-storage-design.md §11.3). Only instances
// whose disks are all in shared pools can be: the disks are there for any host that reaches the pools. The host is
// fenced on every storage cluster first (§11.2), then each instance is defined and started on a host that reaches its
// pools from its database record (launch_vm.sh with the existing disks), as an evacuate migration. The copy left on
// the down host is removed when it comes back (§11.4), and the fences lifted after that.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"gorm.io/gorm"
)

const (
	migrationStatusFencing    = "fencing"
	migrationStatusInProgress = "in_progress"
	migrationStatusCompleted  = "completed"
	migrationStatusFailed     = "failed"
	migrationStatusNotDoing   = "not_doing"

	// How long a host must have been offline before its instances are recovered elsewhere: longer than cland's grace
	// (90 seconds) and a reboot. An admin who confirms the host is powered off does not wait
	evacuateGrace = 5 * time.Minute
	// An evacuation whose launch never answered fails after this long
	evacuateLaunchTimeout = 30 * time.Minute
)

// EvacuateGrace is how long a host must have been offline before its instances are evacuated without a confirmation
func EvacuateGrace() time.Duration {
	return evacuateGrace
}

// EvacuateRequest asks to recover the instances of a host that is down
type EvacuateRequest struct {
	// The host to start them on; -1: any active host of their zone that reaches their pools (cland picks by resources)
	TargetHyper int32
	// The admin confirms the host is powered off: it stands in for a fence CloudLand can not run, and the grace period
	// is not waited for
	ConfirmFenced bool
	// Only these instances (IDs); all of the host when empty
	Instances []int64
	// Start none unless every instance asked for can be: a single forced migration (POST /migrations with force)
	// fails as a whole instead of leaving part of its instances behind
	AllOrNothing bool
}

// EvacuateResult is what happens to one instance of the host
type EvacuateResult struct {
	InstanceID   int64  `json:"-"`
	InstanceUUID string `json:"instance"`
	Hostname     string `json:"hostname"`
	Migration    string `json:"migration,omitempty"`
	// fencing | in_progress | not_doing
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// loadEvacuee loads an instance with what its launch needs, whoever asks (the background rounds have no member)
func loadEvacuee(db *gorm.DB, id int64) (inst *model.Instance, err error) {
	inst = &model.Instance{Model: model.Model{ID: id}}
	if err = db.Preload("Volumes").Preload("Keys").Take(inst).Error; err != nil {
		return nil, err
	}
	inst.Image = &model.Image{Model: model.Model{ID: inst.ImageID}}
	if err = db.Unscoped().Take(inst.Image).Error; err != nil {
		return nil, fmt.Errorf("image of instance %d not found: %w", id, err)
	}
	if err = db.Preload("SiteSubnets").Preload("SiteSubnets.Group").Preload("SecurityGroups").Preload("Address").Preload("Address.Subnet").
		Preload("SecondAddresses", func(db *gorm.DB) *gorm.DB { return db.Order("addresses.updated_at") }).Preload("SecondAddresses.Subnet").
		Where("instance = ?", inst.ID).Find(&inst.Interfaces).Error; err != nil {
		return nil, err
	}
	if inst.RouterID > 0 {
		inst.Router = &model.Router{Model: model.Model{ID: inst.RouterID}}
		if err = db.Take(inst.Router).Error; err != nil {
			return nil, err
		}
	}
	return
}

// evacueePools are the pools of the disks of an instance and the clusters behind them; an error when a disk is in a
// local pool (it is on the down host) or its pool is gone
func evacueePools(ctx context.Context, inst *model.Instance) (pools map[int64]*model.StoragePool, clusters []int64, err error) {
	pools = map[int64]*model.StoragePool{}
	if len(inst.Volumes) == 0 {
		return nil, nil, fmt.Errorf("it has no disk")
	}
	for _, v := range inst.Volumes {
		pool, perr := VolumePool(ctx, v)
		if perr != nil {
			return nil, nil, fmt.Errorf("the pool of volume %d is gone", v.ID)
		}
		if !pool.Shared() {
			return nil, nil, fmt.Errorf("its disk %s is in local storage pool %s, on the host that is down", v.Name, pool.Name)
		}
		if v.Status == model.VolumeStatusLost || v.Status == model.VolumeStatusDeleting {
			return nil, nil, fmt.Errorf("its disk %s is %s", v.Name, v.Status)
		}
		pools[pool.ID] = pool
		cluster := pool.ClusterID
		// A host that reaches the pool through a remote mount is fenced in its own cluster (§7.11)
		if cluster > 0 {
			var member int64
			dbs.DBContext(ctx).Model(&model.StorageClusterNode{}).Where("cluster_id = ? AND hostid = ?", cluster, inst.Hyper).Count(&member)
			if member == 0 {
				if access, remote := sharedPoolRemoteHost(dbs.DBContext(ctx), pool, inst.Hyper); remote {
					cluster = access
				}
			}
		}
		if cluster > 0 && !slices.Contains(clusters, cluster) {
			clusters = append(clusters, cluster)
		}
	}
	return
}

// Evacuate starts recovering the instances of a host that is down
func (a *HyperAdmin) Evacuate(ctx context.Context, hostid int32, req *EvacuateRequest) (results []*EvacuateResult, err error) {
	if err = requireSystemAdmin(ctx); err != nil {
		return
	}
	db := dbs.DBContext(ctx)
	hyper := &model.Hyper{}
	if err = db.Where("hostid = ?", hostid).Take(hyper).Error; err != nil {
		return nil, NewCLError(ErrHypervisorNotFound, "Hypervisor not found", err)
	}
	if hyper.Status != model.HyperStatusOffline || hyper.OfflineAt == nil {
		return nil, NewCLError(ErrEvacuationRefused, fmt.Sprintf("%s is not offline: its instances are migrated, not evacuated", hyper.Hostname), nil)
	}
	if since := time.Since(*hyper.OfflineAt); since < evacuateGrace && !req.ConfirmFenced {
		return nil, NewCLError(ErrEvacuationRefused, fmt.Sprintf("%s has been offline for %s only: wait until it is %s, or confirm it is powered off",
			hyper.Hostname, since.Round(time.Second), evacuateGrace), nil)
	}
	if req.TargetHyper >= 0 {
		target := &model.Hyper{}
		if err = db.Where("hostid = ?", req.TargetHyper).Take(target).Error; err != nil || target.Status != 1 || target.Hostid == hostid {
			return nil, NewCLError(ErrEvacuationRefused, "The target hypervisor is not an active host", err)
		}
	}
	insts := []*model.Instance{}
	q := db.Where("hyper = ?", hostid).Order("id")
	if len(req.Instances) > 0 {
		q = q.Where("id IN ?", req.Instances)
	}
	if err = q.Find(&insts).Error; err != nil {
		return
	}
	confirmedBy := ""
	if req.ConfirmFenced {
		confirmedBy = GetMemberShip(ctx).UserName
	}
	type candidate struct {
		inst     *model.Instance
		result   *EvacuateResult
		clusters []int64
	}
	cands := []*candidate{}
	results = []*EvacuateResult{}
	clusterSet := []int64{}
	for _, i := range insts {
		res := &EvacuateResult{InstanceID: i.ID, InstanceUUID: i.UUID, Hostname: i.Hostname, Status: migrationStatusNotDoing}
		results = append(results, res)
		var inst *model.Instance
		if inst, err = loadEvacuee(db, i.ID); err != nil {
			res.Reason, err = err.Error(), nil
			continue
		}
		switch {
		case inst.Status == model.InstanceStatusDeleting:
			res.Reason = "it is being deleted"
			continue
		case inst.Status == model.InstanceStatusMigrating:
			res.Reason = "it is being migrated or evacuated"
			continue
		}
		// A reinstall queued for an image copy would give up on the instance recovered elsewhere and put it in error
		if werr := refuseWhileWaiting(db, inst); werr != nil {
			res.Reason = werr.Error()
			continue
		}
		pools, clusters, perr := evacueePools(ctx, inst)
		if perr != nil {
			res.Reason = "it can not be recovered elsewhere: " + perr.Error()
			continue
		}
		if req.TargetHyper >= 0 {
			for _, p := range pools {
				if _, perr = poolUsableOn(db, p, req.TargetHyper, false); perr != nil {
					res.Reason = perr.Error()
					break
				}
			}
			if res.Reason != "" {
				continue
			}
		}
		cands = append(cands, &candidate{inst: inst, result: res, clusters: clusters})
		for _, c := range clusters {
			if !slices.Contains(clusterSet, c) {
				clusterSet = append(clusterSet, c)
			}
		}
	}
	if req.AllOrNothing && evacuationRefused(results) {
		return
	}
	// Fence the host on every cluster first; an instance on a cluster that can not be fenced is not recovered
	unfenced := map[int64]string{}
	for _, cid := range clusterSet {
		cluster := &model.StorageCluster{}
		if err = db.Take(cluster, cid).Error; err != nil {
			return
		}
		if _, ferr := ensureStorageFence(ctx, cluster, hostid, confirmedBy); ferr != nil {
			unfenced[cid] = ferr.Error()
		}
	}
	for _, c := range cands {
		for _, cid := range c.clusters {
			if why, bad := unfenced[cid]; bad {
				c.result.Reason = why
			}
		}
	}
	if req.AllOrNothing && evacuationRefused(results) {
		return
	}
	membership := GetMemberShip(ctx)
	for _, c := range cands {
		if c.result.Reason != "" {
			continue
		}
		mig := &model.Migration{Name: fmt.Sprintf("evacuate-%s", truncate(c.inst.Hostname, 50)), InstanceID: c.inst.ID, Type: model.MigrationTypeEvacuate,
			Force: req.ConfirmFenced, SourceHyper: hostid, TargetHyper: req.TargetHyper, Status: migrationStatusFencing,
			PriorStatus: string(c.inst.Status), CreaterName: membership.UserName, CreaterUUID: membership.UserUUID}
		err = db.Transaction(func(tx *gorm.DB) error {
			// Taken only when nothing else changed the instance meanwhile; the reason of an earlier failed evacuation goes
			claim := tx.Model(&model.Instance{}).Where("id = ? AND hyper = ? AND status = ?", c.inst.ID, hostid, c.inst.Status).
				Updates(map[string]interface{}{"status": model.InstanceStatusMigrating, "reason": ""})
			if claim.Error != nil {
				return claim.Error
			}
			if claim.RowsAffected == 0 {
				return fmt.Errorf("it changed meanwhile")
			}
			return tx.Create(mig).Error
		})
		if err != nil {
			c.result.Reason, err = err.Error(), nil
			continue
		}
		c.result.Migration, c.result.Status = mig.UUID, migrationStatusFencing
	}
	advanceEvacuations(ctx)
	// Report what each one is at now
	for _, r := range results {
		if r.Migration == "" {
			continue
		}
		m := &model.Migration{}
		if db.Where("uuid = ?", r.Migration).Take(m).Error == nil {
			r.Status, r.Reason = m.Status, m.Message
		}
	}
	return
}

// evacuationRefused tells whether an instance of the request can not be evacuated
func evacuationRefused(results []*EvacuateResult) bool {
	for _, r := range results {
		if r.Reason != "" {
			return true
		}
	}
	return false
}

// createForced evacuates instances off their down source hosts, the instances of each host in one evacuation
// (shared-storage-design.md §11.3): a forced migration picks single instances where the evacuation of a host
// (POST /hypers/:uuid/evacuate) takes all of them. Their disks stay in their shared pools, so no target pools; the
// grace period, the fences and the placement groups are those of the evacuation. Without batch the instances of a host
// start only when all of them can, and the first refusal is returned
func (a *MigrationAdmin) createForced(ctx context.Context, instances []*model.Instance, tgtHyper int32, opts *MigrationOptions, batch bool) (migrations []*model.Migration, results []*MigrationResult, err error) {
	if len(opts.Disks) > 0 {
		return nil, nil, NewCLError(ErrInvalidParameter, "The disks of a forced migration stay in their shared pools, target pools can not be given", nil)
	}
	db := dbs.DBContext(ctx)
	byHost := map[int32][]int64{}
	hosts := []int32{}
	byID := map[int64]*model.Instance{}
	for _, inst := range instances {
		if _, ok := byHost[inst.Hyper]; !ok {
			hosts = append(hosts, inst.Hyper)
		}
		byHost[inst.Hyper] = append(byHost[inst.Hyper], inst.ID)
		byID[inst.ID] = inst
	}
	// All or nothing holds within one evacuation: across two hosts the first would be fenced and its instances on
	// their way before the second one is refused. A request evacuates the instances of one host
	if !batch && len(hosts) > 1 {
		return nil, nil, NewCLError(ErrInvalidParameter, "A forced migration moves the instances of one offline host; request the instances of each host separately", nil)
	}
	for _, h := range hosts {
		res, eerr := hyperAdmin.Evacuate(ctx, h, &EvacuateRequest{TargetHyper: tgtHyper, ConfirmFenced: opts.ConfirmFenced,
			Instances: byHost[h], AllOrNothing: !batch})
		if eerr != nil {
			if !batch {
				return migrations, results, eerr
			}
			for _, id := range byHost[h] {
				results = append(results, &MigrationResult{Instance: byID[id], Error: eerr})
			}
			continue
		}
		for _, r := range res {
			mr := &MigrationResult{Instance: byID[r.InstanceID]}
			results = append(results, mr)
			if r.Migration == "" {
				if r.Reason == "" {
					// Fine itself, held back with the others of the call
					mr.Error = NewCLError(ErrEvacuationRefused, fmt.Sprintf("Instance %s is not evacuated: another instance of the request can not be", r.Hostname), nil)
					continue
				}
				mr.Error = NewCLError(ErrEvacuationRefused, fmt.Sprintf("Instance %s can not be evacuated: %s", r.Hostname, r.Reason), nil)
				if !batch && err == nil {
					err = mr.Error
				}
				continue
			}
			m := &model.Migration{}
			if lerr := db.Where("uuid = ?", r.Migration).Take(m).Error; lerr != nil {
				mr.Error = NewCLError(ErrSQLSyntaxError, "Failed to load the migration", lerr)
				continue
			}
			mr.Migration = m
			migrations = append(migrations, m)
		}
		if err != nil {
			return
		}
	}
	return
}

// advanceEvacuations moves the evacuations on: those whose host is fenced on every cluster their disks are on are
// started, a failed fence fails them, a launch that never answered fails
func advanceEvacuations(ctx context.Context) {
	db := dbs.DBContext(ctx)
	migs := []*model.Migration{}
	if err := db.Where("type = ? AND status IN ?", model.MigrationTypeEvacuate, []string{migrationStatusFencing, migrationStatusInProgress}).
		Order("id").Find(&migs).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to list the evacuations: %v", err)
		return
	}
	for _, m := range migs {
		if m.Status == migrationStatusInProgress {
			if time.Since(m.UpdatedAt) > evacuateLaunchTimeout {
				failEvacuation(ctx, m, "the target host did not report the launch")
			}
			continue
		}
		inst, err := loadEvacuee(db, m.InstanceID)
		if err != nil {
			failEvacuation(ctx, m, err.Error())
			continue
		}
		_, clusters, err := evacueePools(ctx, inst)
		if err != nil {
			failEvacuation(ctx, m, err.Error())
			continue
		}
		ready, why := true, ""
		for _, cid := range clusters {
			f, ferr := liveStorageFence(db, cid, m.SourceHyper)
			if ferr != nil {
				ready = false
				break
			}
			if f == nil {
				// Lost meanwhile (lifted by hand): fenced again
				cluster := &model.StorageCluster{}
				if db.Take(cluster, cid).Error == nil {
					confirmedBy := ""
					if m.Force {
						confirmedBy = m.CreaterName
					}
					if _, ferr = ensureStorageFence(ctx, cluster, m.SourceHyper, confirmedBy); ferr != nil {
						why = ferr.Error()
					}
				}
				ready = false
				continue
			}
			switch f.Status {
			case model.StorageFenceFenced, model.StorageFenceConfirmed:
			case model.StorageFenceFencing:
				ready = false
			default:
				ready, why = false, fmt.Sprintf("the host could not be fenced: %s", f.Message)
			}
		}
		if why != "" {
			failEvacuation(ctx, m, why)
			continue
		}
		if !ready {
			continue
		}
		if err = dispatchEvacuation(ctx, m, inst); err != nil {
			failEvacuation(ctx, m, err.Error())
		}
	}
}

// evacuationHosts are the hosts an instance can be started on: active hosts of its zone that reach every pool of its
// disks, the given target only when there is one
func evacuationHosts(db *gorm.DB, inst *model.Instance, m *model.Migration, pools map[int64]*model.StoragePool) (hosts []int32, err error) {
	if m.TargetHyper >= 0 {
		hosts = []int32{m.TargetHyper}
	} else if err = db.Model(&model.Hyper{}).Where("status = 1 AND hostid >= 0 AND zone_id = ? AND hostid <> ?", inst.ZoneID, m.SourceHyper).
		Order("hostid").Pluck("hostid", &hosts).Error; err != nil {
		return
	}
	usable := []int32{}
	for _, h := range hosts {
		ok := true
		for _, p := range pools {
			if _, perr := poolUsableOn(db, p, h, false); perr != nil {
				ok = false
				break
			}
		}
		if ok {
			usable = append(usable, h)
		}
	}
	if len(usable) == 0 {
		return nil, fmt.Errorf("no active host of the zone reaches the storage pools of the instance")
	}
	return usable, nil
}

// evacuationMetadata is the metadata of the launch: the instance's, its boot disk to use as it is, and its data disks
func evacuationMetadata(ctx context.Context, inst *model.Instance, m *model.Migration, pools map[int64]*model.StoragePool) (metadata string, bootVolume *model.Volume, bootPool *model.StoragePool, err error) {
	if metadata, err = instanceAdmin.GetMetadata(ctx, inst, ""); err != nil {
		return
	}
	md := map[string]interface{}{}
	if err = json.Unmarshal([]byte(metadata), &md); err != nil {
		return
	}
	dataDisks := []map[string]interface{}{}
	for _, v := range inst.Volumes {
		pool := pools[v.StoragePoolID]
		d, derr := poolDriverOf(pool)
		if derr != nil {
			return "", nil, nil, derr
		}
		args := sharedVolumeArgMap(pool, d, v)
		if v.Booting {
			args["existing"] = true
			if d.Family() == PoolFamilyFile {
				args["nvram"] = path.Join(pool.MountPath, "nvram", fmt.Sprintf("inst-%d_VARS.fd", inst.ID))
			}
			md["boot_disk"] = args
			bootVolume, bootPool = v, pool
			continue
		}
		if v.Target == "" {
			return "", nil, nil, fmt.Errorf("data disk %s has no device name", v.Name)
		}
		args["device"] = v.Target
		dataDisks = append(dataDisks, args)
	}
	if bootVolume == nil {
		return "", nil, nil, fmt.Errorf("it has no boot disk")
	}
	md["data_disks"] = dataDisks
	start := m.PriorStatus == string(model.InstanceStatusRunning) || m.PriorStatus == string(model.InstanceStatusPaused) || m.PriorStatus == "unknown"
	md["evacuate"] = map[string]interface{}{"migration": m.ID, "start": start}
	b, err := json.Marshal(md)
	return string(b), bootVolume, bootPool, err
}

// errEvacuationTaken: another round dispatched the evacuation meanwhile
var errEvacuationTaken = errors.New("the evacuation is dispatched already")

// placeEvacuee decides the host of a placement group member among the hosts that can take it, or checks the given
// target, under the lock of the group (placement-group-plan.md §6.3). The members still on the down host leave it and
// count nowhere there; those whose evacuation was sent already count at their targets, so the next one keeps away
// from them (spread) or joins them (pack). A strict group refuses a host that breaks it
func placeEvacuee(ctx context.Context, tx *gorm.DB, group *model.PlacementGroup, inst *model.Instance, m *model.Migration, hosts []int32) (int32, error) {
	ids := []int64{}
	if err := tx.Model(&model.Instance{}).Where("placement_group_id = ? AND hyper = ?", group.ID, m.SourceHyper).Pluck("id", &ids).Error; err != nil {
		return -1, NewCLError(ErrSQLSyntaxError, "Failed to query the members of the placement group", err)
	}
	leaving := map[int64]bool{inst.ID: true}
	for _, id := range ids {
		leaving[id] = true
	}
	st, err := loadPlacementState(tx, group, leaving, time.Now())
	if err != nil {
		return -1, err
	}
	if m.TargetHyper >= 0 {
		if rule := placementViolation(group, st.Occ, m.TargetHyper, 1, hostLabeler(tx, true)); rule != "" && group.Strict {
			return -1, NewCLError(ErrPlacementGroupConflict, "The target breaks the placement group: "+rule, nil)
		}
		return m.TargetHyper, nil
	}
	need, err := migrationDemand(ctx, tx, inst)
	if err != nil {
		return -1, err
	}
	// A strict pack group goes together: the host must take every member still to be recovered
	rest := need
	if group.Policy == model.PlacementPolicyPack && group.Strict {
		others := []*model.Instance{}
		if err = tx.Where("id IN ? AND id <> ? AND status = ?", ids, inst.ID, model.InstanceStatusMigrating).Find(&others).Error; err != nil {
			return -1, NewCLError(ErrSQLSyntaxError, "Failed to query the members of the placement group", err)
		}
		for _, o := range others {
			d, derr := migrationDemand(ctx, tx, o)
			if derr != nil {
				return -1, derr
			}
			rest = rest.Add(d)
		}
	}
	slots, err := hostSlots(tx, hosts)
	if err != nil {
		return -1, err
	}
	host, perr := Place(group, slots, st.Occ, st.Pending, need, rest)
	if perr != nil {
		return -1, explainPlacement(st, zoneNameOf(tx, group.ZoneID), "the instance", true, perr)
	}
	return host, nil
}

// dispatchEvacuation sends the launch of an evacuated instance to a host that reaches its pools. A member of a
// placement group gets its host from the group (placeEvacuee), recorded as the target so the group counts it there
func dispatchEvacuation(ctx context.Context, m *model.Migration, inst *model.Instance) (err error) {
	db := dbs.DBContext(ctx)
	pools, _, err := evacueePools(ctx, inst)
	if err != nil {
		return
	}
	hosts, err := evacuationHosts(db, inst, m, pools)
	if err != nil {
		return
	}
	metadata, bootVolume, bootPool, err := evacuationMetadata(ctx, inst, m, pools)
	if err != nil {
		return
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		set := map[string]interface{}{"status": migrationStatusInProgress, "message": ""}
		if inst.PlacementGroupID > 0 {
			group, gerr := lockPlacementGroup(tx, inst.PlacementGroupID)
			if gerr == nil {
				target, perr := placeEvacuee(ctx, tx, group, inst, m, hosts)
				if perr != nil {
					return perr
				}
				hosts, set["target_hyper"] = []int32{target}, target
			} else if !errors.Is(gerr, gorm.ErrRecordNotFound) {
				return gerr
			}
		}
		claim := tx.Model(&model.Migration{}).Where("id = ? AND status = ?", m.ID, migrationStatusFencing).Updates(set)
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			return errEvacuationTaken
		}
		return nil
	})
	if errors.Is(err, errEvacuationTaken) {
		return nil
	}
	if err != nil {
		return
	}
	image := inst.Image
	control := fmt.Sprintf("select=%s %s", hyperGroupOf(inst.ZoneID, hosts), schedulerResources(inst.Cpu, inst.Memory, 0))
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/launch_vm.sh '%d' '%s.%s' '%t' '%d' '%s' '%d' '%d' '%d' '%d' '%t' '%s' '%s' '%s' '%s' '%s'<<'EOF'\n%s\nEOF",
		inst.ID, ShellEscape(image.FileBase()), ShellEscape(image.Format), image.QAEnabled, 1, ShellEscape(inst.Hostname), inst.Cpu, inst.Memory, inst.Disk,
		bootVolume.ID, inst.NestedEnable, ShellEscape(image.BootLoader), ShellEscape(inst.UUID), ShellEscape(""), ShellEscape(PoolScriptID(bootPool)),
		ShellEscape(bootVolume.Path), base64.StdEncoding.EncodeToString([]byte(metadata)))
	if err = HyperExecute(ctx, control, command); err != nil {
		return
	}
	logger.Ctx(ctx).Infof("Evacuation %d of instance %d from host %d sent to %v", m.ID, inst.ID, m.SourceHyper, hosts)
	return
}

// failEvacuation ends an evacuation that can not go on: the instance stays the down host's, as it was
func failEvacuation(ctx context.Context, m *model.Migration, why string) {
	db := dbs.DBContext(ctx)
	err := db.Transaction(func(tx *gorm.DB) error {
		claim := tx.Model(&model.Migration{}).Where("id = ? AND status IN ?", m.ID, []string{migrationStatusFencing, migrationStatusInProgress}).
			Updates(map[string]interface{}{"status": migrationStatusFailed, "message": truncate(why, 512)})
		if claim.Error != nil || claim.RowsAffected == 0 {
			return claim.Error
		}
		prior := m.PriorStatus
		if prior == "" {
			prior = "unknown"
		}
		// A launch that got as far as the NICs moved them to its host (attach_vm_nic): they are the source's again, as
		// the instance is; the source sends their forwarding entries again when it starts the instance
		if err := tx.Model(&model.Interface{}).Where("instance = ? AND hyper <> ?", m.InstanceID, m.SourceHyper).
			Update("hyper", m.SourceHyper).Error; err != nil {
			return err
		}
		return tx.Model(&model.Instance{}).Where("id = ? AND status = ?", m.InstanceID, model.InstanceStatusMigrating).
			Updates(map[string]interface{}{"status": prior, "reason": truncate("evacuation failed: "+why, 255)}).Error
	})
	if err != nil {
		logger.Ctx(ctx).Errorf("Failed to fail evacuation %d: %v", m.ID, err)
		return
	}
	logger.Ctx(ctx).Warningf("Evacuation %d of instance %d from host %d failed: %s", m.ID, m.InstanceID, m.SourceHyper, why)
}

// FailEvacuationOf fails the evacuation of an instance going on, when cland found no host for its launch. It tells
// whether there was one
func FailEvacuationOf(ctx context.Context, instanceID int64, why string) bool {
	db := dbs.DBContext(ctx)
	m := &model.Migration{}
	if db.Where("instance_id = ? AND type = ? AND status = ?", instanceID, model.MigrationTypeEvacuate, migrationStatusInProgress).
		Order("id DESC").Take(m).Error != nil {
		return false
	}
	failEvacuation(ctx, m, why)
	return true
}

// EvacuationLaunched takes the report of the launch of an evacuated instance (launch_vm.sh ... 'evacuate'). On success
// the instance is the target's from now on; it returns the evacuation so the caller rebuilds its network there
func EvacuationLaunched(ctx context.Context, instanceID int64, hostid int32, state, message string) (m *model.Migration, err error) {
	db := dbs.DBContext(ctx)
	m = &model.Migration{}
	if err = db.Where("instance_id = ? AND type = ? AND status = ?", instanceID, model.MigrationTypeEvacuate, migrationStatusInProgress).
		Order("id DESC").Take(m).Error; err != nil {
		logger.Ctx(ctx).Warningf("Launch of instance %d on host %d reported as an evacuation, none is going on", instanceID, hostid)
		return nil, nil
	}
	if hostid == m.SourceHyper {
		return nil, fmt.Errorf("the evacuation of instance %d was reported by its source host %d", instanceID, hostid)
	}
	if state != string(model.InstanceStatusRunning) && state != string(model.InstanceStatusShutoff) {
		if message == "" {
			message = "the launch on " + hostName(db, hostid) + " failed"
		}
		failEvacuation(ctx, m, message)
		return nil, nil
	}
	hyper := &model.Hyper{}
	if err = db.Where("hostid = ?", hostid).Take(hyper).Error; err != nil {
		return nil, err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Migration{}).Where("id = ?", m.ID).Updates(map[string]interface{}{"status": migrationStatusCompleted,
			"target_hyper": hostid, "progress": 100, "message": ""}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Instance{}).Where("id = ?", instanceID).Updates(map[string]interface{}{"status": state, "hyper": hostid,
			"zone_id": hyper.ZoneID, "reason": ""}).Error; err != nil {
			return err
		}
		return tx.Model(&model.Interface{}).Where("instance = ?", instanceID).Update("hyper", hostid).Error
	})
	if err != nil {
		return nil, err
	}
	m.Status, m.TargetHyper = migrationStatusCompleted, hostid
	logger.Ctx(ctx).Infof("Instance %d evacuated from host %d to host %d (%s)", instanceID, m.SourceHyper, hostid, state)
	return
}

// CleanEvacuatedSource asks the source of a completed evacuation to remove its copy at once when it is back already
// (it came back while the evacuation was going on, and its reconcile left the instance alone then)
func CleanEvacuatedSource(ctx context.Context, m *model.Migration, routerID int64) {
	db := dbs.DBContext(ctx)
	if _, online := hostOnline(db, m.SourceHyper); !online {
		return
	}
	body, _ := json.Marshal(map[string]interface{}{"boot": "-", "reason": "evacuated", "start": []int64{},
		"stale": []reconcileStale{{ID: m.InstanceID, Router: routerID}}})
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/node_reconcile.sh <<'EOF'\n%s\nEOF", body)
	if err := HyperExecute(ctx, fmt.Sprintf("inter=%d", m.SourceHyper), command); err != nil {
		logger.Ctx(ctx).Warningf("Failed to have host %d remove its copy of instance %d: %v", m.SourceHyper, m.InstanceID, err)
	}
}
