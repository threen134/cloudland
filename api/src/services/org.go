/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

History:
   Date     Who ID    Description
   -------- --- ---   -----------
   01/13/19 nanjj  Initial code

*/

package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	orgAdmin = &OrgAdmin{}
)

// slugRegex validates slug format: lowercase letters, digits, hyphens; must start with letter
var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`)

type OrgAdmin struct{}

// Create creates a new Organization. Only SystemAdmin can call this.
func (a *OrgAdmin) Create(ctx context.Context, name string, ownerUserID int64, slug string) (org *model.Organization, err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.Create: name=%s, ownerUserID=%d, slug=%s", name, ownerUserID, slug)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.Create: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT OrgAdmin.Create: orgID=%d, slug=%s", org.ID, org.Slug)
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.IsSystemAdmin() {
		err = NewCLError(ErrPermissionDenied, "Only SystemAdmin can create organizations", nil)
		return
	}

	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()

	// Verify owner user exists and is not Disabled
	user := &model.User{}
	if err = db.Where("id = ?", ownerUserID).Take(user).Error; err != nil {
		err = NewCLError(ErrUserNotFound, "Owner user not found", err)
		return
	}
	if user.Status == model.UserDisabled {
		err = NewCLError(ErrPermissionDenied, "Cannot assign disabled user as owner", nil)
		return
	}

	org = &model.Organization{
		Model:       model.Model{Creater: memberShip.UserID},
		Name:        name,
		OrgType:     model.OrgTypeTeam,
		OwnerUserID: ownerUserID,
	}

	// Handle slug
	if slug != "" {
		// Validate slug format
		if !slugRegex.MatchString(slug) {
			err = NewCLError(ErrSlugInvalid, "Slug must start with a lowercase letter, contain only lowercase letters, digits, and hyphens, length 3-64", nil)
			return
		}
		// Check reserved slugs
		if _, reserved := reservedSlugs[slug]; reserved {
			err = NewCLError(ErrSlugReserved, fmt.Sprintf("Slug '%s' is reserved", slug), nil)
			return
		}
		org.Slug = slug
	}

	if err = db.Create(org).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to create organization ", err)
		err = NewCLError(ErrOrgCreationFailed, "Failed to create organization", err)
		return
	}

	// Auto-generate slug if not provided
	if slug == "" {
		org.Slug = fmt.Sprintf("org-%d", org.ID)
		if err = db.Model(org).Update("slug", org.Slug).Error; err != nil {
			logger.Ctx(ctx).Error("DB failed to update org slug", err)
			err = NewCLError(ErrOrgUpdateFailed, "Failed to update organization slug", err)
			return
		}
	}

	// Create OrgAdmin member for owner
	member := &model.Member{
		UserID:  ownerUserID,
		OrgID:   org.ID,
		OrgRole: model.OrgAdmin,
	}
	if err = db.Create(member).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to create organization member ", err)
		err = NewCLError(ErrMemberCreationFailed, "Failed to create organization member", err)
		return
	}

	// If user was Dormant, restore to Active
	if user.Status == model.UserDormant {
		db.Model(user).Update("status", model.UserActive)
	}

	return
}

// AddMember adds a user to an Org.
func (a *OrgAdmin) AddMember(ctx context.Context, orgID, userID int64, role model.OrgRole) (member *model.Member, err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.AddMember: orgID=%d, userID=%d, role=%v", orgID, userID, role)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.AddMember: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT OrgAdmin.AddMember: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.IsSystemAdmin() {
		err = NewCLError(ErrPermissionDenied, "Only SystemAdmin can add new members to organizations", nil)
		return
	}

	// Cannot set role higher than own (unless SystemAdmin)
	if !memberShip.IsSystemAdmin() && role > memberShip.EffectiveOrgRole() {
		err = NewCLError(ErrPermissionDenied, "Cannot assign a role higher than your own", nil)
		return
	}

	db := DB()

	// Verify target user exists and is not Disabled
	user := &model.User{}
	if err = db.Where("id = ?", userID).Take(user).Error; err != nil {
		err = NewCLError(ErrUserNotFound, "User not found", err)
		return
	}
	if user.Status == model.UserDisabled {
		err = NewCLError(ErrPermissionDenied, "Cannot add disabled user to org", nil)
		return
	}

	member = &model.Member{
		UserID:  userID,
		OrgID:   orgID,
		OrgRole: role,
	}
	if err = db.Create(member).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to create member", err)
		err = NewCLError(ErrMemberCreationFailed, "Failed to add member", err)
		return
	}

	// If user was Dormant, restore to Active
	if user.Status == model.UserDormant {
		db.Model(user).Update("status", model.UserActive)
	}

	return
}

// RemoveMember removes a user from an Org.
func (a *OrgAdmin) RemoveMember(ctx context.Context, orgID, userID int64) (err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.RemoveMember: orgID=%d, userID=%d", orgID, userID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.RemoveMember: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT OrgAdmin.RemoveMember: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.CanManageTargetOrg(orgID) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to manage members of this org", nil)
		return
	}

	db := DB()

	// Check if user is Org Owner — cannot remove owner
	org := &model.Organization{}
	if err = db.Where("id = ?", orgID).Take(org).Error; err != nil {
		err = NewCLError(ErrOrgNotFound, "Organization not found", err)
		return
	}
	if org.OwnerUserID == userID {
		err = NewCLError(ErrCannotRemoveOwner, "Cannot remove Org Owner. Transfer ownership first.", nil)
		return
	}

	// Hard delete member to avoid unique index clashing on re-addition
	if err = db.Unscoped().Where("user_id = ? AND org_id = ?", userID, orgID).Delete(&model.Member{}).Error; err != nil {
		err = NewCLError(ErrMemberDeleteFailed, "Failed to remove member", err)
		return
	}

	// Post-check: if user has no remaining memberships, set Dormant
	var remainingCount int64
	db.Model(&model.Member{}).Where("user_id = ? AND deleted_at IS NULL", userID).Count(&remainingCount)
	if remainingCount == 0 {
		db.Model(&model.User{Model: model.Model{ID: userID}}).Update("status", model.UserDormant)
	}
	return
}

// UpdateMemberRole updates a member's OrgRole.
func (a *OrgAdmin) UpdateMemberRole(ctx context.Context, orgID, userID int64, newRole model.OrgRole) (err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.UpdateMemberRole: orgID=%d, userID=%d, newRole=%v", orgID, userID, newRole)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.UpdateMemberRole: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT OrgAdmin.UpdateMemberRole: success")
		}
	}()
	// Validate newRole is in range 1-3 (OrgReader to OrgAdmin)
	if newRole < model.OrgReader || newRole > model.OrgAdmin {
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Invalid org role value: %d. Must be between %d and %d", newRole, model.OrgReader, model.OrgAdmin), nil)
		return
	}

	memberShip := GetMemberShip(ctx)
	if !memberShip.CanManageTargetOrg(orgID) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to update member role", nil)
		return
	}

	db := DB()

	// Check if target is Org Owner — cannot modify Owner's OrgRole
	org := &model.Organization{}
	if err = db.Where("id = ?", orgID).Take(org).Error; err != nil {
		err = NewCLError(ErrOrgNotFound, "Organization not found", err)
		return
	}
	if org.OwnerUserID == userID {
		err = NewCLError(ErrPermissionDenied, "Cannot modify Org Owner's role. Transfer ownership first.", nil)
		return
	}

	// Cannot set role higher than own (unless SystemAdmin)
	if !memberShip.IsSystemAdmin() && newRole > memberShip.EffectiveOrgRole() {
		err = NewCLError(ErrPermissionDenied, "Cannot assign a role higher than your own", nil)
		return
	}

	if err = db.Model(&model.Member{}).Where("user_id = ? AND org_id = ?", userID, orgID).Update("org_role", newRole).Error; err != nil {
		err = NewCLError(ErrMemberUpdateFailed, "Failed to update member role", err)
	}
	return
}

// TransferOwner transfers org ownership. Only SystemAdmin can do this.
func (a *OrgAdmin) TransferOwner(ctx context.Context, orgID, newOwnerUserID int64) (err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.TransferOwner: orgID=%d, newOwner=%d", orgID, newOwnerUserID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.TransferOwner: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT OrgAdmin.TransferOwner: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.IsSystemAdmin() {
		err = NewCLError(ErrPermissionDenied, "Only SystemAdmin can transfer ownership", nil)
		return
	}

	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()

	// Lock org record for update (prevent race with RemoveMember)
	org := &model.Organization{}
	if err = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", orgID).Take(org).Error; err != nil {
		err = NewCLError(ErrOrgNotFound, "Organization not found", err)
		return
	}

	// Verify new owner is a member
	member := &model.Member{}
	if err = db.Where("user_id = ? AND org_id = ?", newOwnerUserID, orgID).Take(member).Error; err != nil {
		err = NewCLError(ErrMemberNotFound, "New owner must be a member of the organization", err)
		return
	}

	// Update owner (stored org_role is left unchanged; EffectiveOrgRole() handles elevation)
	if err = db.Model(org).Update("owner_user_id", newOwnerUserID).Error; err != nil {
		err = NewCLError(ErrOrgUpdateFailed, "Failed to transfer ownership", err)
	}
	return
}

// RenameOrg renames an Org. OrgOwner/OrgAdmin/SystemAdmin can do this.
func (a *OrgAdmin) RenameOrg(ctx context.Context, orgID int64, newName string) (err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.RenameOrg: orgID=%d, newName=%s", orgID, newName)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.RenameOrg: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT OrgAdmin.RenameOrg: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.CanManageTargetOrg(orgID) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to rename this org", nil)
		return
	}

	db := DB()
	org := &model.Organization{}
	if err = db.Where("id = ?", orgID).Take(org).Error; err != nil {
		err = NewCLError(ErrOrgNotFound, "Organization not found", err)
		return
	}
	if org.OrgType == model.OrgTypeSystem {
		err = NewCLError(ErrPermissionDenied, "Cannot rename system organization", nil)
		return
	}

	if err = db.Model(org).Update("name", newName).Error; err != nil {
		err = NewCLError(ErrOrgUpdateFailed, "Failed to rename organization", err)
	}
	return
}

func (a *OrgAdmin) Get(ctx context.Context, id int64) (org *model.Organization, err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.Get: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT OrgAdmin.Get: orgUUID=%s", org.UUID)
		}
	}()
	if id <= 0 {
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Invalid org ID: %d", id), nil)
		logger.Ctx(ctx).Error("%v", err)
		return
	}
	ctx, db := GetContextDB(ctx)
	org = &model.Organization{}
	err = db.Where("id = ?", id).Take(org).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query org, %v", err)
		err = NewCLError(ErrOrgNotFound, "Failed to find organization", err)
		return
	}
	return
}

func (a *OrgAdmin) GetOrgByUUID(ctx context.Context, uuID string) (org *model.Organization, err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.GetOrgByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.GetOrgByUUID: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT OrgAdmin.GetOrgByUUID: orgID=%d", org.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	org = &model.Organization{}
	err = db.Preload("Members.User").Where("uuid = ?", uuID).Take(org).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query org, %v", err)
		err = NewCLError(ErrOrgNotFound, "Failed to find organization", err)
		return
	}
	return
}

func (a *OrgAdmin) GetOrgByName(ctx context.Context, name string) (org *model.Organization, err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.GetOrgByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.GetOrgByName: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT OrgAdmin.GetOrgByName: orgID=%d", org.ID)
		}
	}()
	org = &model.Organization{}
	ctx, db := GetContextDB(ctx)
	err = db.Where("name = ?", name).Take(org).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to query org, %v", err)
		err = NewCLError(ErrOrgNotFound, "Failed to find organization", err)
		return
	}
	return
}

// GetOrgName returns the name of a local org ID, empty when there is none. The lookup is by an explicit
// condition: with a zero ID in the struct GORM adds no primary key condition and Take returns the first org.
// Deleted orgs are included so that resources they left behind still show their owner.
func (a *OrgAdmin) GetOrgName(ctx context.Context, id int64) (name string) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.GetOrgName: id=%d", id)
	defer func() {
		logger.Ctx(ctx).Infof("EXIT OrgAdmin.GetOrgName: name=%s", name)
	}()
	if id <= 0 {
		return
	}
	org := &model.Organization{}
	ctx, db := GetContextDB(ctx)
	err := db.Unscoped().Where("id = ?", id).Take(org).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to query org", err)
		return
	}
	name = org.Name
	return
}

// GetOrgUUID returns the UUID of a local org ID (empty when not found). The mapping never changes once an
// org exists, so results are cached like GetOrgIDByUUID. Zero is never looked up: see GetOrgName.
func (a *OrgAdmin) GetOrgUUID(ctx context.Context, id int64) string {
	if id <= 0 {
		return ""
	}
	if cached, ok := cachedOrgUUID(id); ok {
		return cached
	}
	org := &model.Organization{}
	_, db := GetContextDB(ctx)
	if err := db.Unscoped().Select("uuid").Where("id = ?", id).Take(org).Error; err != nil || org.UUID == "" {
		return ""
	}
	// Only this direction: the row may be a deleted org, whose UUID must not resolve to it
	orgUUIDByID.Store(id, orgUUIDEntry{uuid: org.UUID, expires: time.Now().Add(orgCacheTTL)})
	return org.UUID
}

// Delete deletes an Org. Only SystemAdmin can do this. OrgTypeSystem cannot be deleted.
func (a *OrgAdmin) Delete(ctx context.Context, org *model.Organization) (err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.Delete: orgID=%d, name=%s", org.ID, org.Name)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.Delete: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT OrgAdmin.Delete: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.IsSystemAdmin() {
		err = NewCLError(ErrPermissionDenied, "Only SystemAdmin can delete organizations", nil)
		return
	}
	if org.OrgType == model.OrgTypeSystem {
		err = NewCLError(ErrPermissionDenied, "Cannot delete system organization", nil)
		return
	}

	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()

	// Check for cloud resources across all resource types
	resourceChecks := []struct {
		target interface{}
		where  string
	}{
		{&model.Instance{}, "owner = ?"},
		{&model.Volume{}, "owner = ?"},
		{&model.Subnet{}, "owner = ?"},
		{&model.FloatingIp{}, "owner = ?"},
		{&model.SecurityGroup{}, "owner = ?"},
		{&model.Key{}, "owner = ?"},
		{&model.PlacementGroup{}, "owner = ?"},
		{&model.LoadBalancer{}, "owner = ?"},
		{&model.Router{}, "owner = ?"},
		{&model.Image{}, "owner = ?"},
		{&model.Interface{}, "owner = ? AND type <> 'gateway'"},
	}
	for _, rc := range resourceChecks {
		var resCount int64
		if err = db.Model(rc.target).Where(rc.where, org.ID).Count(&resCount).Error; err != nil {
			logger.Ctx(ctx).Error("DB failed to query resources, %v", err)
			return
		}
		if resCount > 0 {
			err = NewCLError(ErrOrgHasResources, "Organization has resources. Clear them first.", nil)
			return
		}
	}

	// Collect affected user IDs
	var members []*model.Member
	db.Where("org_id = ? AND deleted_at IS NULL", org.ID).Find(&members)
	var affectedUserIDs []int64
	for _, m := range members {
		affectedUserIDs = append(affectedUserIDs, m.UserID)
	}

	// Hard delete all member records to avoid unique index clashing
	if err = db.Unscoped().Where("org_id = ?", org.ID).Delete(&model.Member{}).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to delete members, %v", err)
		err = NewCLError(ErrMemberDeleteFailed, "Failed to delete organization members", err)
		return
	}

	// Delete keys
	keys := []*model.Key{}
	if dbErr := db.Where("owner = ?", org.ID).Find(&keys).Error; dbErr == nil {
		for _, key := range keys {
			if keyErr := (&KeyAdminService{}).Delete(ctx, key); keyErr != nil {
				logger.Ctx(ctx).Error("Failed to delete key", keyErr)
			}
		}
	}

	// Delete security groups
	secgroups := []*model.SecurityGroup{}
	if dbErr := db.Where("owner = ?", org.ID).Find(&secgroups).Error; dbErr == nil {
		for _, secgroup := range secgroups {
			if sgErr := secgroupAdmin.Delete(ctx, secgroup); sgErr != nil {
				logger.Ctx(ctx).Error("Failed to delete security group", sgErr)
			}
		}
	}

	// Soft delete org first, then rename with Unscoped to avoid stale name on delete failure
	if err = db.Delete(org).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to delete organization, %v", err)
		err = NewCLError(ErrOrgDeleteFailed, "Failed to delete organization", err)
		return
	}
	timestamp := org.CreatedAt.Unix()
	org.Name = fmt.Sprintf("%s-deleted-%d", org.Name, timestamp)
	org.Slug = fmt.Sprintf("%s-deleted-%d", org.Slug, timestamp)
	if err = db.Model(&model.Organization{}).Unscoped().Where("id = ?", org.ID).Updates(map[string]interface{}{"name": org.Name, "slug": org.Slug}).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to update org name", err)
		err = NewCLError(ErrOrgUpdateFailed, "Failed to update organization name", err)
		return
	}

	// Post-check: set affected users to Dormant if they have no other memberships
	// This runs inside the transaction (db is transaction-scoped).
	for _, uid := range affectedUserIDs {
		var remainingCount int64
		if cntErr := db.Model(&model.Member{}).Where("user_id = ? AND deleted_at IS NULL", uid).Count(&remainingCount).Error; cntErr != nil {
			logger.Ctx(ctx).Errorf("Failed to count remaining memberships for user %d: %v", uid, cntErr)
			continue
		}
		if remainingCount == 0 {
			if updErr := db.Model(&model.User{Model: model.Model{ID: uid}}).Update("status", model.UserDormant).Error; updErr != nil {
				logger.Ctx(ctx).Errorf("Failed to set user %d to Dormant: %v", uid, updErr)
			}
		}
	}
	return
}

func (a *OrgAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, orgs []*model.Organization, err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.List: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT OrgAdmin.List: total=%d, orgsCount=%d", total, len(orgs))
		}
	}()
	memberShip := GetMemberShip(ctx)
	if limit == 0 {
		limit = 16
	}
	if order == "" {
		order = "created_at"
	}

	ctx, db := GetContextDB(ctx)

	whereQuery := ""
	whereArgs := []interface{}{}
	if query != "" {
		whereQuery = "name LIKE ?"
		whereArgs = append(whereArgs, "%"+query+"%")
	}
	if !memberShip.IsSystemAdmin() {
		if whereQuery != "" {
			whereQuery += " AND id IN (SELECT org_id FROM members WHERE user_id = ? AND deleted_at IS NULL)"
		} else {
			whereQuery = "id IN (SELECT org_id FROM members WHERE user_id = ? AND deleted_at IS NULL)"
		}
		whereArgs = append(whereArgs, memberShip.UserID)
	}
	orgs = []*model.Organization{}
	if err = db.Model(&orgs).Where(whereQuery, whereArgs...).Count(&total).Error; err != nil {
		logger.Ctx(ctx).Error("DB failed to count organizations, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count organizations", err)
		return
	}
	db = dbs.Sortby(db.Offset(int(offset)).Limit(int(limit)), order)
	err = db.Preload("Members.User").Where(whereQuery, whereArgs...).Find(&orgs).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to query organizations, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query organizations", err)
		return
	}
	return
}

func init() {
	// organizations.uuid is the cross-service key: at most one live row per UUID. Partial like the slug index,
	// because deleted orgs and tombstones keep their UUID.
	dbs.AutoUpgrade("idx_org_uuid_partial", func(db *gorm.DB) error {
		return db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS idx_org_uuid
			ON organizations (uuid)
			WHERE deleted_at IS NULL AND uuid <> ''
		`).Error
	})
}

// orgCacheTTL bounds how long a cached UUID <-> ID mapping is used without the database. The mapping of an
// org can change: it is deleted, or the system org is linked to another control-plane UUID. The replica
// that handles the change drops its own entries at once; the other clapi replicas (HA) follow within the TTL.
// Only a request after the entry expired queries the database, so authorize still does not query it on every
// request.
const orgCacheTTL = time.Minute

type orgIDEntry struct {
	id      int64
	expires time.Time
}

type orgUUIDEntry struct {
	uuid    string
	expires time.Time
}

// orgIDByUUID caches the mapping from an org UUID (the cross-service key sent by CPGateway) to the local org
// ID, as orgIDEntry. It only ever holds real IDs (> 0) of live orgs.
var orgIDByUUID sync.Map

// orgUUIDByID caches the reverse mapping (local org ID -> UUID, as orgUUIDEntry) for resource responses
var orgUUIDByID sync.Map

func cacheOrgID(uuID string, id int64) {
	if uuID == "" || id <= 0 {
		return
	}
	expires := time.Now().Add(orgCacheTTL)
	orgIDByUUID.Store(uuID, orgIDEntry{id: id, expires: expires})
	orgUUIDByID.Store(id, orgUUIDEntry{uuid: uuID, expires: expires})
}

func cachedOrgID(uuID string) (int64, bool) {
	v, ok := orgIDByUUID.Load(uuID)
	if !ok {
		return 0, false
	}
	if e, _ := v.(orgIDEntry); e.id > 0 && time.Now().Before(e.expires) {
		return e.id, true
	}
	orgIDByUUID.Delete(uuID)
	return 0, false
}

func cachedOrgUUID(id int64) (string, bool) {
	v, ok := orgUUIDByID.Load(id)
	if !ok {
		return "", false
	}
	if e, _ := v.(orgUUIDEntry); e.uuid != "" && time.Now().Before(e.expires) {
		return e.uuid, true
	}
	orgUUIDByID.Delete(id)
	return "", false
}

// lockOrgUUID serializes the sync and the deletion of one org UUID, across transactions and clapi replicas
// (PostgreSQL transaction-level advisory lock; other databases, used by tests only, are skipped).
func lockOrgUUID(tx *gorm.DB, uuID string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", uuID).Error
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// orgRowLocked is called by UpsertOrgByUUID once it holds the row of an existing org. Tests replace it.
var orgRowLocked = func(tx *gorm.DB, org *model.Organization) {}

// GetOrgIDByUUID resolves the org UUID sent by CPGateway to the local org ID. The auto-increment keys of the
// two org tables are independent: the UUID is the cross-service contract, the other side's ID never is.
// A deleted org is not found, and the result is never 0.
func (a *OrgAdmin) GetOrgIDByUUID(ctx context.Context, uuID string) (int64, error) {
	if uuID == "" {
		return 0, NewCLError(ErrOrgNotFound, "Organization UUID is empty", nil)
	}
	if id, ok := cachedOrgID(uuID); ok {
		return id, nil
	}
	_, db := GetContextDB(ctx)
	org := &model.Organization{}
	if err := db.Where("uuid = ?", uuID).Take(org).Error; err != nil {
		return 0, NewCLError(ErrOrgNotFound, "Organization not found for uuid "+uuID, err)
	}
	if org.ID <= 0 {
		return 0, NewCLError(ErrOrgNotFound, "Organization not found for uuid "+uuID, nil)
	}
	cacheOrgID(uuID, org.ID)
	return org.ID, nil
}

// releaseOrgSlug clears the slug of the live rows other than keepID that still hold it. The control plane keeps
// slugs unique among its live orgs, so such a row belongs to an org that was deleted there without being deleted
// here (or whose slug changed). Only the informational slug is cleared: the row keeps its UUID, ID and
// resources, it is never handed to the org that now uses the slug.
func releaseOrgSlug(ctx context.Context, tx *gorm.DB, slug string, keepID int64, uuID string) error {
	if slug == "" {
		return nil
	}
	res := tx.Model(&model.Organization{}).Where("slug = ? AND id <> ?", slug, keepID).Update("slug", "")
	if res.Error == nil && res.RowsAffected > 0 {
		logger.Ctx(ctx).Warningf("Released slug %s held by %d stale org row(s) for org uuid=%s", slug, res.RowsAffected, uuID)
	}
	return res.Error
}

// UpsertOrgByUUID syncs an org pushed by CPGateway and returns its local ID, allocated by this region.
//
// A team org is matched by UUID only. Matching by slug is never done: the control plane frees the slug of a
// deleted org for reuse, and a new org adopting the old row would take over all of its resources.
// The system org is the one exception: this region creates its own system org at bootstrap (with its own
// UUID), and the control plane's system org is linked to it. Both sides have exactly one, so this is an
// identity mapping rather than a guess.
//
// A UUID whose row was deleted here is not recreated (a stale push racing with the deletion); 0 is returned.
func (a *OrgAdmin) UpsertOrgByUUID(ctx context.Context, uuID, name, slug string, orgType model.OrgType) (id int64, err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.UpsertOrgByUUID: uuid=%s name=%s slug=%s type=%d", uuID, name, slug, orgType)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.UpsertOrgByUUID: error=%v", err)
		} else {
			logger.Ctx(ctx).Infof("EXIT OrgAdmin.UpsertOrgByUUID: ok id=%d", id)
		}
	}()
	if uuID == "" {
		return 0, NewCLError(ErrInvalidParameter, "Organization UUID is required", nil)
	}
	if orgType != model.OrgTypeSystem {
		orgType = model.OrgTypeTeam
	}
	var oldUUID string
	_, db := GetContextDB(ctx)
	// The advisory lock serializes pushes of the same UUID (a registration push racing a full region sync);
	// the unique index on live UUIDs is the backstop, and a conflict with it is retried once, finding the row.
	for attempt := 0; ; attempt++ {
		id, oldUUID, err = upsertOrgTx(ctx, db, uuID, name, slug, orgType)
		if err == nil || attempt > 0 || !isUniqueViolation(err) {
			break
		}
		logger.Ctx(ctx).Warningf("Org uuid=%s was created concurrently, syncing it again: %v", uuID, err)
	}
	if err != nil {
		id = 0
		return
	}
	if oldUUID != "" && oldUUID != uuID {
		orgIDByUUID.Delete(oldUUID)
	}
	cacheOrgID(uuID, id)
	return
}

// upsertOrgTx is one attempt of UpsertOrgByUUID. It returns id 0 when the org is deleted in this region, and
// the previous UUID of the system org when it was linked to a new one.
func upsertOrgTx(ctx context.Context, db *gorm.DB, uuID, name, slug string, orgType model.OrgType) (id int64, oldUUID string, err error) {
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := lockOrgUUID(tx, uuID); err != nil {
			return err
		}
		org := &model.Organization{}
		res := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("uuid = ?", uuID).Limit(1).Find(org)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			orgRowLocked(tx, org)
			if err := releaseOrgSlug(ctx, tx, slug, org.ID, uuID); err != nil {
				return err
			}
			upd := tx.Model(&model.Organization{}).Where("id = ?", org.ID).
				Updates(map[string]interface{}{"name": name, "slug": slug})
			if upd.Error != nil {
				return upd.Error
			}
			if upd.RowsAffected == 0 {
				// Deleted in the meantime: report it as deleted, and do not cache it
				logger.Ctx(ctx).Warningf("Org uuid=%s was deleted during the sync, ignoring it", uuID)
				return nil
			}
			id = org.ID
			return nil
		}

		var deleted int64
		if err := tx.Unscoped().Model(&model.Organization{}).
			Where("uuid = ? AND deleted_at IS NOT NULL", uuID).Count(&deleted).Error; err != nil {
			return err
		}
		if deleted > 0 {
			logger.Ctx(ctx).Warningf("Org uuid=%s was deleted in this region, ignoring the sync", uuID)
			return nil
		}

		if orgType == model.OrgTypeSystem {
			system := &model.Organization{}
			res = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_type = ?", model.OrgTypeSystem).Order("id").Limit(1).Find(system)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				if err := releaseOrgSlug(ctx, tx, slug, system.ID, uuID); err != nil {
					return err
				}
				oldUUID, id = system.UUID, system.ID
				logger.Ctx(ctx).Infof("Linking the local system org id=%d (uuid=%s) to the control-plane system org uuid=%s", id, oldUUID, uuID)
				return tx.Model(&model.Organization{}).Where("id = ?", system.ID).
					Updates(map[string]interface{}{"uuid": uuID, "name": name, "slug": slug}).Error
			}
		}
		if err := releaseOrgSlug(ctx, tx, slug, 0, uuID); err != nil {
			return err
		}
		org = &model.Organization{Model: model.Model{UUID: uuID}, Name: name, Slug: slug, OrgType: orgType, OwnerUserID: 1}
		if err := tx.Create(org).Error; err != nil {
			return err
		}
		id = org.ID
		return nil
	})
	if err != nil {
		return 0, "", err
	}
	return
}

// orgBlockingResources are the resources that keep an org from being deleted in this region. The admin list
// views look the owner of each row up by ID and fail when that org is deleted, so an org is only deleted once
// they are gone. Security groups and keys are metadata: their list views skip an owner they cannot find.
var orgBlockingResources = []struct {
	name   string
	target interface{}
	where  string
}{
	{"instances", &model.Instance{}, "owner = ?"},
	{"volumes", &model.Volume{}, "owner = ?"},
	{"floating IPs", &model.FloatingIp{}, "owner = ?"},
	{"VPCs", &model.Router{}, "owner = ?"},
	{"subnets", &model.Subnet{}, "owner = ?"},
	{"load balancers", &model.LoadBalancer{}, "owner = ?"},
	{"listeners", &model.Listener{}, "owner = ?"},
	{"VPN gateways", &model.VpnGateway{}, "owner = ?"},
	{"images", &model.Image{}, "owner = ?"},
	{"placement groups", &model.PlacementGroup{}, "owner = ?"},
	{"tasks", &model.Task{}, "owner = ?"},
	{"interfaces", &model.Interface{}, "owner = ? AND type <> 'gateway'"},
}

// DeleteOrgByUUID deletes an org the control plane deleted. The row is soft-deleted, so its UUID no longer
// resolves (requests get 404 OrgNotFound) and a later push of the same UUID is ignored instead of recreating
// it; the slug is freed for new orgs by the partial unique index. Deleting an already deleted org succeeds.
// Deleting an org this region never saw leaves a tombstone (a deleted row with only the UUID and name), so a
// late push of it is ignored as well. An org that still owns resources here is kept and ErrOrgHasResources is
// returned: they have to be removed first (a system admin can, with all_orgs).
func (a *OrgAdmin) DeleteOrgByUUID(ctx context.Context, uuID, name string) (err error) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.DeleteOrgByUUID: uuid=%s name=%s", uuID, name)
	defer func() {
		if err != nil {
			logger.Ctx(ctx).Errorf("EXIT OrgAdmin.DeleteOrgByUUID: error=%v", err)
		} else {
			logger.Ctx(ctx).Info("EXIT OrgAdmin.DeleteOrgByUUID: success")
		}
	}()
	if uuID == "" {
		return NewCLError(ErrInvalidParameter, "Organization UUID is required", nil)
	}
	_, db := GetContextDB(ctx)
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := lockOrgUUID(tx, uuID); err != nil {
			return err
		}
		org := &model.Organization{}
		res := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("uuid = ?", uuID).Limit(1).Find(org)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			var known int64
			if err := tx.Unscoped().Model(&model.Organization{}).Where("uuid = ?", uuID).Count(&known).Error; err != nil {
				return err
			}
			if known > 0 {
				logger.Ctx(ctx).Infof("Org uuid=%s is already deleted in this region", uuID)
				return nil
			}
			logger.Ctx(ctx).Infof("Org uuid=%s was never synced to this region, leaving a tombstone", uuID)
			return tx.Create(&model.Organization{
				Model:   model.Model{UUID: uuID, DeletedAt: gorm.DeletedAt{Time: time.Now(), Valid: true}},
				Name:    name,
				OrgType: model.OrgTypeTeam, OwnerUserID: 1,
			}).Error
		}
		if org.OrgType == model.OrgTypeSystem {
			return NewCLError(ErrPermissionDenied, "Cannot delete the system organization", nil)
		}
		for _, rc := range orgBlockingResources {
			var count int64
			if err := tx.Model(rc.target).Where(rc.where, org.ID).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return NewCLError(ErrOrgHasResources,
					fmt.Sprintf("Organization %s still has %d %s in this region, delete them first", uuID, count, rc.name), nil)
			}
		}
		return tx.Delete(&model.Organization{}, org.ID).Error
	})
	if err == nil {
		orgIDByUUID.Delete(uuID)
	}
	return
}

// --- View layer ---
