/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// MaxPlacementGroupsPerOrg bounds the groups of an organization in a region. Groups hold no resource, so they are
// not a quota of cpgateway (placement-group-plan.md §4.1)
const MaxPlacementGroupsPerOrg = 50

var PlacementGroupAdmin = &PlacementGroupAdminService{}

type PlacementGroupAdminService struct{}

// PlacementGroupStats is how the members of a group are spread, for lists and the detail (§2.3)
type PlacementGroupStats struct {
	MemberCount int
	HostCount   int
	Compliant   bool
}

// PlacementGroupMember is one member in the detail of a group (§7.1)
type PlacementGroupMember struct {
	Instance *model.Instance
	// Numbers of the hosts inside the group: the same host has the same number, 0 when there is none
	HostSlot   int
	TargetSlot int
	Hostid     int32
	// In-flight migration, nil when none
	Migration           *model.Migration
	StaleProvisioning   bool
	StaleMigration      bool
	LastMigrationFailed bool
	IgnoredPlacement    bool
}

func (a *PlacementGroupAdminService) Create(ctx context.Context, name, description, policy string, strict bool, zone *model.Zone) (group *model.PlacementGroup, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckOrgPermission(model.OrgWriter) {
		return nil, NewCLError(ErrPermissionDenied, "Not authorized to create placement groups", nil)
	}
	if policy != model.PlacementPolicySpread && policy != model.PlacementPolicyPack {
		return nil, NewCLError(ErrInvalidParameter, "Policy must be spread or pack", nil)
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	var count int64
	if err = db.Model(&model.PlacementGroup{}).Where("owner = ?", memberShip.OrgID).Count(&count).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to count placement groups", err)
	}
	if count >= MaxPlacementGroupsPerOrg {
		return nil, NewCLError(ErrPlacementGroupLimit, fmt.Sprintf("An organization can have at most %d placement groups", MaxPlacementGroupsPerOrg), nil)
	}
	if err = db.Model(&model.PlacementGroup{}).Where("owner = ? AND name = ?", memberShip.OrgID, name).Count(&count).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to query placement groups", err)
	}
	if count > 0 {
		return nil, NewCLError(ErrPlacementGroupExists, fmt.Sprintf("Placement group %s exists already", name), nil)
	}
	group = &model.PlacementGroup{Model: model.Model{Creater: memberShip.UserID}, Owner: memberShip.OrgID, Name: name,
		Description: description, Policy: policy, Strict: strict, ZoneID: zone.ID}
	if err = db.Create(group).Error; err != nil {
		return nil, placementGroupWriteError(name, err)
	}
	group.Zone = zone
	return
}

// GetByUUID finds a group the caller may read: system admins see every organization's
func (a *PlacementGroupAdminService) GetByUUID(ctx context.Context, uuID string) (group *model.PlacementGroup, err error) {
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckOrgPermission(model.OrgReader) {
		return nil, NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
	}
	where, args := memberShip.GetOrgFilter()
	group = &model.PlacementGroup{}
	if err = db.Preload("Zone").Where(where, args...).Where("uuid = ?", uuID).Take(group).Error; err != nil {
		return nil, NewCLError(ErrPlacementGroupNotFound, "Placement group not found", err)
	}
	return
}

// GetForMember finds the group a new instance joins. It must belong to the organization of the request, system admins
// included: members and their group are always in one organization. Another organization's group is reported as
// not found, not whether it exists (§9).
func (a *PlacementGroupAdminService) GetForMember(ctx context.Context, reference *BaseReference) (group *model.PlacementGroup, err error) {
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	if reference == nil || (reference.ID == "" && reference.Name == "") {
		return nil, NewCLError(ErrInvalidParameter, "Placement group must be given by id or name", nil)
	}
	q := db.Where("owner = ?", memberShip.OrgID)
	if reference.ID != "" {
		q = q.Where("uuid = ?", reference.ID)
	} else {
		q = q.Where("name = ?", reference.Name)
	}
	group = &model.PlacementGroup{}
	if err = q.Take(group).Error; err != nil {
		return nil, NewCLError(ErrPlacementGroupNotFound, "Placement group not found", err)
	}
	return
}

func (a *PlacementGroupAdminService) List(ctx context.Context, offset, limit int64, order, query string, zoneID int64) (total int64, groups []*model.PlacementGroup, stats map[int64]*PlacementGroupStats, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckOrgPermission(model.OrgReader) {
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}
	if order == "" {
		order = "-created_at"
	}
	where, args := memberShip.GetOrgFilter()
	filter := func(tx *gorm.DB) *gorm.DB {
		tx = tx.Where(where, args...).Scopes(dbs.Contains(query, "name"))
		if zoneID > 0 {
			tx = tx.Where("zone_id = ?", zoneID)
		}
		return tx
	}
	groups = []*model.PlacementGroup{}
	if err = db.Model(&model.PlacementGroup{}).Scopes(filter).Count(&total).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to count placement groups", err)
		return
	}
	_, db = GetContextDB(ctx)
	db = dbs.Sortby(db.Offset(int(offset)).Limit(int(limit)), order)
	if err = db.Preload("Zone").Scopes(filter).Find(&groups).Error; err != nil {
		err = NewCLError(ErrSQLSyntaxError, "Failed to query placement groups", err)
		return
	}
	stats, err = a.Stats(ctx, groups)
	return
}

// Stats counts the members and hosts of some groups with two grouped queries, whatever their number (§7.1). Where a
// member is follows §2.3: its host, or the host chosen for it while it is being created; migration targets do not count
func (a *PlacementGroupAdminService) Stats(ctx context.Context, groups []*model.PlacementGroup) (stats map[int64]*PlacementGroupStats, err error) {
	stats = map[int64]*PlacementGroupStats{}
	if len(groups) == 0 {
		return
	}
	ids := make([]int64, len(groups))
	for i, g := range groups {
		ids[i] = g.ID
		stats[g.ID] = &PlacementGroupStats{Compliant: true}
	}
	_, db := GetContextDB(ctx)
	type memberRow struct {
		PlacementGroupID int64
		Members          int
	}
	members := []*memberRow{}
	if err = db.Model(&model.Instance{}).Select("placement_group_id, COUNT(*) AS members").
		Where("placement_group_id IN ?", ids).Group("placement_group_id").Scan(&members).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to count the members of placement groups", err)
	}
	for _, r := range members {
		stats[r.PlacementGroupID].MemberCount = r.Members
	}
	type hostRow struct {
		PlacementGroupID int64
		Host             int32
		Members          int
	}
	hosts := []*hostRow{}
	_, db = GetContextDB(ctx)
	if err = db.Model(&model.Instance{}).
		Select("placement_group_id, CASE WHEN hyper >= 0 THEN hyper ELSE placement_hyper END AS host, COUNT(*) AS members").
		Where("placement_group_id IN ? AND (hyper >= 0 OR status IN ?)", ids, []model.InstanceStatus{model.InstanceStatusProvisioning, model.InstanceStatusDeleting}).
		Group("placement_group_id, CASE WHEN hyper >= 0 THEN hyper ELSE placement_hyper END").Scan(&hosts).Error; err != nil {
		return nil, NewCLError(ErrSQLSyntaxError, "Failed to count the hosts of placement groups", err)
	}
	perHost := map[int64]map[int32]int{}
	for _, r := range hosts {
		if perHost[r.PlacementGroupID] == nil {
			perHost[r.PlacementGroupID] = map[int32]int{}
		}
		perHost[r.PlacementGroupID][r.Host] = r.Members
	}
	for _, g := range groups {
		stats[g.ID].HostCount = len(perHost[g.ID])
		stats[g.ID].Compliant = placementCompliant(g.Policy, perHost[g.ID])
	}
	return
}

// Members lists the members of a group with the numbers of their hosts inside the group (§7.1)
func (a *PlacementGroupAdminService) Members(ctx context.Context, group *model.PlacementGroup) (members []*PlacementGroupMember, stats *PlacementGroupStats, err error) {
	_, db := GetContextDB(ctx)
	st, err := loadPlacementState(db, group, nil, time.Now())
	if err != nil {
		return
	}
	hosts := map[int32]bool{}
	for _, m := range st.Members {
		if m.Host >= 0 {
			hosts[m.Host] = true
		}
		if m.Target >= 0 {
			hosts[m.Target] = true
		}
	}
	ordered := []int32{}
	for h := range hosts {
		ordered = append(ordered, h)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	slot := map[int32]int{}
	for i, h := range ordered {
		slot[h] = i + 1
	}
	// The latest finished migration of every member tells whether a split group comes from a failed migration or
	// from an admin ignoring the rules (§2.3)
	latest := map[int64]*model.Migration{}
	if len(st.Members) > 0 {
		ids := make([]int64, len(st.Members))
		for i, m := range st.Members {
			ids[i] = m.Instance.ID
		}
		finished := []*model.Migration{}
		_, db = GetContextDB(ctx)
		if err = db.Where("instance_id IN ? AND status NOT IN ?", ids, append(append([]string{}, inFlightMigrationStatus...), "not_doing")).
			Order("id").Find(&finished).Error; err != nil {
			return nil, nil, NewCLError(ErrSQLSyntaxError, "Failed to query the migrations of the members", err)
		}
		for _, mig := range finished {
			latest[mig.InstanceID] = mig
		}
	}
	for _, m := range st.Members {
		pm := &PlacementGroupMember{Instance: m.Instance, Hostid: m.Host, Migration: m.Migration,
			StaleProvisioning: m.StaleProvisioning, StaleMigration: m.StaleMigration}
		if m.Host >= 0 {
			pm.HostSlot = slot[m.Host]
		}
		if m.Target >= 0 {
			pm.TargetSlot = slot[m.Target]
		}
		if mig := latest[m.Instance.ID]; mig != nil {
			switch mig.Status {
			case "failed", "not_supported", "source_rollback", "rollback", "timeout":
				pm.LastMigrationFailed = true
			}
			pm.IgnoredPlacement = mig.IgnorePlacement && mig.Status == "completed"
		}
		members = append(members, pm)
	}
	stats = &PlacementGroupStats{MemberCount: len(st.Members), HostCount: st.hostCount(), Compliant: st.compliant()}
	return
}

// Update changes the name and the description; the policy, the strictness and the zone are fixed (§6.1)
func (a *PlacementGroupAdminService) Update(ctx context.Context, group *model.PlacementGroup, name, description *string) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, group.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to update the placement group", nil)
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	updates := map[string]interface{}{}
	if name != nil && *name != group.Name {
		var count int64
		if err = db.Model(&model.PlacementGroup{}).Where("owner = ? AND name = ? AND id <> ?", group.Owner, *name, group.ID).Count(&count).Error; err != nil {
			return NewCLError(ErrSQLSyntaxError, "Failed to query placement groups", err)
		}
		if count > 0 {
			return NewCLError(ErrPlacementGroupExists, fmt.Sprintf("Placement group %s exists already", *name), nil)
		}
		updates["name"] = *name
		group.Name = *name
	}
	if description != nil && *description != group.Description {
		updates["description"] = *description
		group.Description = *description
	}
	if len(updates) == 0 {
		return
	}
	if err = db.Model(&model.PlacementGroup{}).Where("id = ?", group.ID).Updates(updates).Error; err != nil {
		return placementGroupWriteError(group.Name, err)
	}
	return
}

// placementGroupWriteError reports a unique violation as the name being taken (a request that got in between the
// check and the write) and anything else as the database error it is
func placementGroupWriteError(name string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return NewCLError(ErrPlacementGroupExists, fmt.Sprintf("Placement group %s exists already", name), nil)
	}
	return NewCLError(ErrSQLSyntaxError, "Failed to save the placement group", err)
}

// Delete removes an empty group. The group row is locked while the members are counted, so a creation can not add
// one in between and leave it pointing to a deleted group (§3.2); a member being deleted still counts.
func (a *PlacementGroupAdminService) Delete(ctx context.Context, group *model.PlacementGroup) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, group.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to delete the placement group", nil)
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if group, err = lockPlacementGroup(db, group.ID); err != nil {
		return
	}
	var count int64
	if err = db.Model(&model.Instance{}).Where("placement_group_id = ?", group.ID).Count(&count).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to count the members of the placement group", err)
	}
	if count > 0 {
		return NewCLError(ErrPlacementGroupInUse, fmt.Sprintf("Placement group %s still has %d member(s); delete them first", group.Name, count), nil)
	}
	if err = db.Delete(group).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to delete the placement group", err)
	}
	// Free the name for a new group (PET-1228)
	if err = db.Model(&model.PlacementGroup{}).Unscoped().Where("id = ?", group.ID).
		Update("name", fmt.Sprintf("%s-%d", group.Name, group.CreatedAt.Unix())).Error; err != nil {
		return NewCLError(ErrSQLSyntaxError, "Failed to rename the deleted placement group", err)
	}
	return
}
