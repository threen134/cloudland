/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Placement groups (docs/architecture/plan/placement-group-plan.md). clapi picks the host of every member itself and
// sends a select= of that single host, so cland only checks the resources again: after a multi-host select= clapi
// would not know where the instance went until the host calls back, minutes later when the image is downloaded,
// and the next member could not keep away from it (§3.1).

import (
	"fmt"
	"sort"
	"strings"
	"time"

	. "api/src/common"
	"api/src/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// A member still being created after this long, or a migration without any update for this long, is flagged in
	// the group detail. Both keep counting: dropping them could put a member next to one really there (§2.2)
	placementStaleAfter = time.Hour
)

// Short messages of Place for a strict pack group with members, told apart by explainPlacement
const (
	msgPackHostFull        = "The host of the group can not take the instances"
	msgPackHostUnavailable = "The host of the group is not available"
)

// inFlightMigrationStatus are the states of a migration whose target already counts as taken (§2.2, third kind)
var inFlightMigrationStatus = []string{"in_progress", "target_prepared", "source_prepared"}

// Demand is what an instance takes on a host: vCPUs, memory in KiB, and disk in bytes of the pool the placement
// looks at (the built-in pool, or the pool of the boot disk when it is another one)
type Demand struct{ Cpu, MemKiB, DiskBytes int64 }

func (d Demand) Add(o Demand) Demand {
	return Demand{d.Cpu + o.Cpu, d.MemKiB + o.MemKiB, d.DiskBytes + o.DiskBytes}
}

func (d Demand) Times(n int64) Demand {
	return Demand{d.Cpu * n, d.MemKiB * n, d.DiskBytes * n}
}

// HostSlot is a candidate host with its free room as reported, before the pending demand is taken off (§5)
type HostSlot struct {
	Hostid        int32
	FreeCpu       int64
	FreeMemKiB    int64
	FreeDiskBytes int64
}

// fits tells whether the host takes d on top of what is pending on it; the test of cland is the same: the demand
// must not exceed what is available
func (h *HostSlot) fits(pending, d Demand) bool {
	return d.Cpu <= h.FreeCpu-pending.Cpu && d.MemKiB <= h.FreeMemKiB-pending.MemKiB && d.DiskBytes <= h.FreeDiskBytes-pending.DiskBytes
}

// score is the fitness formula of cland (testResource in cland/scheduler.go): the larger, the more room is left
func (h *HostSlot) score(pending, d Demand) float64 {
	s := 1.0
	for _, p := range [][2]int64{{d.Cpu, h.FreeCpu - pending.Cpu}, {d.MemKiB, h.FreeMemKiB - pending.MemKiB}, {d.DiskBytes, h.FreeDiskBytes - pending.DiskBytes}} {
		s *= 1 - float64(p[0])/float64(p[1]+1)
	}
	return s
}

// bestSlot is the candidate with the highest score among those keep accepts; ties go to the lowest host id
func bestSlot(candidates []*HostSlot, pending map[int32]Demand, d Demand, keep func(*HostSlot) bool) (best *HostSlot) {
	bestScore := 0.0
	for _, h := range candidates {
		if !keep(h) {
			continue
		}
		s := h.score(pending[h.Hostid], d)
		if best == nil || s > bestScore || (s == bestScore && h.Hostid < best.Hostid) {
			best, bestScore = h, s
		}
	}
	return
}

// packAnchor is the host holding the most members of a group, the lowest host id among equals; -1 without members
func packAnchor(occ map[int32]int) int32 {
	anchor, most := int32(-1), 0
	for host, n := range occ {
		if n > most || (n == most && n > 0 && host < anchor) {
			anchor, most = host, n
		}
	}
	return anchor
}

// Place picks one host for one instance. occ is the member count per host (§2.2); pending is the demand counted on
// top of the reported resources (§3.3); need is the demand of this instance; rest is the summed demand of every
// instance that still has to go with this one, this one included: the rest of a creation batch (need times the
// count left), or the members of a strict pack group moving together, whose flavors may differ (§6.3). It returns
// an error when a strict group can not be satisfied. The messages are short: callers explain them with the state of
// the group (explainPlacement).
func Place(g *model.PlacementGroup, candidates []*HostSlot, occ map[int32]int, pending map[int32]Demand, need, rest Demand) (hostid int32, err error) {
	fitsNeed := func(h *HostSlot) bool { return h.fits(pending[h.Hostid], need) }
	fitsRest := func(h *HostSlot) bool { return h.fits(pending[h.Hostid], rest) }
	if g.Policy == model.PlacementPolicySpread {
		if g.Strict {
			if h := bestSlot(candidates, pending, need, func(h *HostSlot) bool { return occ[h.Hostid] == 0 && fitsNeed(h) }); h != nil {
				return h.Hostid, nil
			}
			return -1, NewCLError(ErrPlacementGroupNoHost, "No host without a member of the group can take the instance", nil)
		}
		least := -1
		for _, h := range candidates {
			if fitsNeed(h) && (least < 0 || occ[h.Hostid] < least) {
				least = occ[h.Hostid]
			}
		}
		if least < 0 {
			return -1, NewCLError(ErrNoQualifiedHypervisor, "No hypervisor has room for the instance", nil)
		}
		h := bestSlot(candidates, pending, need, func(h *HostSlot) bool { return occ[h.Hostid] == least && fitsNeed(h) })
		return h.Hostid, nil
	}
	// pack
	anchor := packAnchor(occ)
	if g.Strict {
		if anchor >= 0 {
			for _, h := range candidates {
				if h.Hostid == anchor {
					if fitsRest(h) {
						return anchor, nil
					}
					return -1, NewCLError(ErrPlacementGroupHostFull, msgPackHostFull, nil)
				}
			}
			return -1, NewCLError(ErrPlacementGroupHostFull, msgPackHostUnavailable, nil)
		}
		if h := bestSlot(candidates, pending, need, fitsRest); h != nil {
			return h.Hostid, nil
		}
		return -1, NewCLError(ErrPlacementGroupHostFull, "No host can take all the instances together", nil)
	}
	if anchor < 0 {
		// A new group: one host for the whole batch if there is one, else the batch spills over several (§5)
		if h := bestSlot(candidates, pending, need, fitsRest); h != nil {
			return h.Hostid, nil
		}
		if h := bestSlot(candidates, pending, need, fitsNeed); h != nil {
			return h.Hostid, nil
		}
		return -1, NewCLError(ErrNoQualifiedHypervisor, "No hypervisor has room for the instance", nil)
	}
	fit := []*HostSlot{}
	for _, h := range candidates {
		if fitsNeed(h) {
			fit = append(fit, h)
		}
	}
	if len(fit) == 0 {
		return -1, NewCLError(ErrNoQualifiedHypervisor, "No hypervisor has room for the instance", nil)
	}
	sort.SliceStable(fit, func(i, j int) bool {
		a, b := fit[i], fit[j]
		if occ[a.Hostid] != occ[b.Hostid] {
			return occ[a.Hostid] > occ[b.Hostid]
		}
		sa, sb := a.score(pending[a.Hostid], need), b.score(pending[b.Hostid], need)
		if sa != sb {
			return sa > sb
		}
		return a.Hostid < b.Hostid
	})
	return fit[0].Hostid, nil
}

// placementViolation tells which rule of the group putting count more members on host breaks, empty when none. It
// does not look at the room of the host: it checks a host the admin chose (§6.2, §6.3)
func placementViolation(g *model.PlacementGroup, occ map[int32]int, host int32, count int, hostLabel func(int32) string) string {
	if g.Policy == model.PlacementPolicySpread {
		if occ[host] > 0 {
			return fmt.Sprintf("host %s already holds %d member(s) of spread placement group %s", hostLabel(host), occ[host], g.Name)
		}
		if count > 1 {
			return fmt.Sprintf("spread placement group %s can not have %d instances on one host", g.Name, count)
		}
		return ""
	}
	if anchor := packAnchor(occ); anchor >= 0 && anchor != host {
		return fmt.Sprintf("the members of pack placement group %s are on host %s", g.Name, hostLabel(anchor))
	}
	return ""
}

// placementMember is one member of a group and where it counts (§2.2)
type placementMember struct {
	Instance *model.Instance
	// Where it is (first kind: hyper) or is being created (second kind: placement_hyper), -1 when it has no place
	Host   int32
	Landed bool
	// Target of its in-flight migration (third kind), -1 when there is none
	Target    int32
	Migration *model.Migration
	// CPU, memory and the disks in the built-in pool; TargetDemand is the part landing on the migration target
	Demand            Demand
	TargetDemand      Demand
	StaleProvisioning bool
	StaleMigration    bool
}

// placementState is the occupancy of a group: members per host and the demand not reported by the hosts yet
type placementState struct {
	Group   *model.PlacementGroup
	Members []*placementMember
	Occ     map[int32]int
	// CPU, memory and built-in pool disk counted on top of the reported resources (§3.3)
	Pending   map[int32]Demand
	Migrating []*placementMember // members with an in-flight migration
}

// lockPlacementGroup takes the row lock that serializes every placement decision of a group (§3.2). A deleted group
// is not found: GORM adds deleted_at IS NULL
func lockPlacementGroup(db *gorm.DB, id int64) (group *model.PlacementGroup, err error) {
	group = &model.PlacementGroup{}
	if err = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(group).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, NewCLError(ErrPlacementGroupNotFound, "Placement group not found", err)
		}
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to lock the placement group", err)
	}
	return
}

func lookupBuiltinPoolID(db *gorm.DB) (id int64, err error) {
	builtin := &model.StoragePool{}
	if err = db.Where("builtin = ?", true).Take(builtin).Error; err != nil {
		return 0, NewCLError(ErrStoragePoolNotFound, "Built-in storage pool not found", err)
	}
	return builtin.ID, nil
}

// loadPlacementState reads the members of a group and where they count. exclude drops the first two kinds of the
// given instances (a migrating instance leaves its source); their migration targets still count.
func loadPlacementState(db *gorm.DB, g *model.PlacementGroup, exclude map[int64]bool, now time.Time) (st *placementState, err error) {
	st = &placementState{Group: g, Occ: map[int32]int{}, Pending: map[int32]Demand{}}
	instances := []*model.Instance{}
	if err = db.Where("placement_group_id = ?", g.ID).Order("id").Find(&instances).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query the members of the placement group", err)
	}
	if len(instances) == 0 {
		return
	}
	ids := make([]int64, len(instances))
	for i, inst := range instances {
		ids[i] = inst.ID
	}
	builtin, err := lookupBuiltinPoolID(db)
	if err != nil {
		return nil, err
	}
	volumes := []*model.Volume{}
	if err = db.Where("instance_id IN ?", ids).Find(&volumes).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query the volumes of the members", err)
	}
	builtinGB := map[int64]int64{}
	for _, v := range volumes {
		if v.StoragePoolID == builtin {
			builtinGB[v.InstanceID] += int64(v.Size)
		}
	}
	migrations := []*model.Migration{}
	if err = db.Where("instance_id IN ? AND status IN ?", ids, inFlightMigrationStatus).Order("id").Find(&migrations).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query the migrations of the members", err)
	}
	inFlight := map[int64]*model.Migration{}
	for _, m := range migrations {
		inFlight[m.InstanceID] = m
	}
	resources := []*model.Resource{}
	if err = db.Find(&resources).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query the resources of the hosts", err)
	}
	reportedAt := map[int32]time.Time{}
	for _, r := range resources {
		reportedAt[r.Hostid] = r.UpdatedAt
	}
	for _, inst := range instances {
		m := &placementMember{Instance: inst, Host: -1, Target: -1,
			Demand: Demand{Cpu: int64(inst.Cpu), MemKiB: int64(inst.Memory) * 1024, DiskBytes: builtinGB[inst.ID] * gib}}
		switch {
		case inst.Hyper >= 0:
			m.Host, m.Landed = inst.Hyper, true
		case inst.Status == model.InstanceStatusProvisioning || inst.Status == model.InstanceStatusDeleting:
			m.Host = inst.PlacementHyper
			m.StaleProvisioning = now.Sub(inst.CreatedAt) > placementStaleAfter
		}
		if mig := inFlight[inst.ID]; mig != nil && mig.TargetHyper >= 0 && mig.TargetHyper != m.Host {
			m.Target, m.Migration = mig.TargetHyper, mig
			m.StaleMigration = now.Sub(mig.UpdatedAt) > placementStaleAfter
			m.TargetDemand = m.Demand
			if plan := MigrationPlan(mig); len(plan) > 0 {
				// The plan says which disks land in the built-in pool of the target
				var gb int64
				for _, item := range plan {
					if item.DstPoolID == builtin {
						gb += int64(item.SizeGB)
					}
				}
				m.TargetDemand.DiskBytes = gb * gib
			}
			st.Migrating = append(st.Migrating, m)
		}
		st.Members = append(st.Members, m)
		if m.Host >= 0 && !exclude[inst.ID] {
			st.Occ[m.Host]++
			if !m.Landed {
				// Second kind: the host does not know about it yet
				st.Pending[m.Host] = st.Pending[m.Host].Add(m.Demand)
			} else if at, ok := reportedAt[m.Host]; ok && inst.UpdatedAt.After(at) {
				// Landed after the last resource report of its host (§3.3, second source)
				st.Pending[m.Host] = st.Pending[m.Host].Add(m.Demand)
			}
		}
		if m.Target >= 0 {
			st.Occ[m.Target]++
			st.Pending[m.Target] = st.Pending[m.Target].Add(m.TargetDemand)
		}
	}
	return
}

// hostCount and compliant judge a group by where the members are, migration targets left out (§2.3)
func (st *placementState) hostCount() int {
	hosts := map[int32]bool{}
	for _, m := range st.Members {
		if m.Host >= 0 {
			hosts[m.Host] = true
		}
	}
	return len(hosts)
}

func placementCompliant(policy string, perHost map[int32]int) bool {
	if policy == model.PlacementPolicyPack {
		return len(perHost) <= 1
	}
	for _, n := range perHost {
		if n > 1 {
			return false
		}
	}
	return true
}

func (st *placementState) compliant() bool {
	perHost := map[int32]int{}
	for _, m := range st.Members {
		if m.Host >= 0 {
			perHost[m.Host]++
		}
	}
	return placementCompliant(st.Group.Policy, perHost)
}

// explainPlacement turns the short error of Place into a message saying what holds the hosts, the members still
// being created or stuck among them in particular: counting the members the user sees would not add up otherwise
// what names the instances being placed; others is set when the state leaves the instance itself out (a migration)
func explainPlacement(st *placementState, zoneName, what string, others bool, err error) error {
	clErr, ok := err.(*CLError)
	if !ok {
		return err
	}
	g := st.Group
	kind := "best-effort"
	if g.Strict {
		kind = "strict"
	}
	members := "members are"
	if others {
		members = "the other members are"
	}
	var msg string
	switch clErr.Code {
	case ErrPlacementGroupNoHost:
		msg = fmt.Sprintf("No available host of zone %s without a member of strict spread placement group %s has room for %s (%s on %d host(s))",
			zoneName, g.Name, what, members, len(st.Occ))
	case ErrPlacementGroupHostFull:
		switch clErr.Message {
		case msgPackHostUnavailable:
			msg = fmt.Sprintf("The host holding the members of strict pack placement group %s is not available (disabled, in maintenance, offline, or its built-in storage is full)", g.Name)
		case msgPackHostFull:
			msg = fmt.Sprintf("The host holding the members of strict pack placement group %s has no room for %s", g.Name, what)
		default:
			msg = fmt.Sprintf("No available host of zone %s can take %s together (strict pack placement group %s)", zoneName, what, g.Name)
		}
	case ErrNoQualifiedHypervisor:
		msg = fmt.Sprintf("No available host of zone %s has room for %s (%s %s placement group %s)", zoneName, what, kind, g.Policy, g.Name)
	default:
		return err
	}
	if notes := st.inFlightNotes(); notes != "" {
		msg += "; " + notes
	}
	return NewCLError(clErr.Code, msg, nil)
}

// inFlightNotes names the members that hold a host without being there yet, or that are stuck
func (st *placementState) inFlightNotes() string {
	var creating, stuck, migrating []string
	for _, m := range st.Members {
		name := m.Instance.Hostname
		switch {
		case m.StaleProvisioning:
			stuck = append(stuck, fmt.Sprintf("%s (still being created after %s, delete it to free its host)", name, placementStaleAfter))
		case !m.Landed && m.Host >= 0:
			creating = append(creating, name)
		}
		if m.Migration != nil {
			if m.StaleMigration {
				stuck = append(stuck, fmt.Sprintf("%s (migration %s has not moved for over %s)", name, m.Migration.UUID, placementStaleAfter))
			} else {
				migrating = append(migrating, name)
			}
		}
	}
	notes := []string{}
	if len(creating) > 0 {
		notes = append(notes, "members being created: "+strings.Join(creating, ", "))
	}
	if len(migrating) > 0 {
		notes = append(notes, "members migrating (they hold both hosts): "+strings.Join(migrating, ", "))
	}
	if len(stuck) > 0 {
		notes = append(notes, "stuck members: "+strings.Join(stuck, ", "))
	}
	return strings.Join(notes, "; ")
}

// hostSlots builds the slots of some hosts from their reported resources. Hosts that never reported are left out,
// like cland does (cland/scheduler.go). ignore_capacity does not change them: it skips the room of the storage pools
// only, and cland checks CPU, memory and the built-in pool on the chosen host whatever the request says.
func hostSlots(db *gorm.DB, hostids []int32) (slots []*HostSlot, err error) {
	if len(hostids) == 0 {
		return
	}
	resources := []*model.Resource{}
	if err = db.Where("hostid IN ?", hostids).Find(&resources).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query the resources of the hosts", err)
	}
	byHost := map[int32]*model.Resource{}
	for _, r := range resources {
		byHost[r.Hostid] = r
	}
	for _, id := range hostids {
		r := byHost[id]
		if r == nil {
			continue
		}
		slots = append(slots, &HostSlot{Hostid: id, FreeCpu: r.Cpu, FreeMemKiB: r.Memory, FreeDiskBytes: r.Disk})
	}
	return
}

// hostLabeler names hosts in messages; the name of a host is only shown to system admins
func hostLabeler(db *gorm.DB, admin bool) func(int32) string {
	names := map[int32]string{}
	return func(hostid int32) string {
		if !admin {
			return fmt.Sprintf("#%d", hostid)
		}
		if name, ok := names[hostid]; ok {
			return name
		}
		names[hostid] = hostName(db, hostid)
		return names[hostid]
	}
}
