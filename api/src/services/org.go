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
	"fmt"
	"regexp"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

)

var (
	orgAdmin = &OrgAdmin{}
)

// slugRegex validates slug format: lowercase letters, digits, hyphens; must start with letter
var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`)

type OrgAdmin struct{}


// Create creates a new Organization. Only SystemAdmin can call this.
func (a *OrgAdmin) Create(ctx context.Context, name string, ownerUserID int64, slug string) (org *model.Organization, err error) {
	logger.Infof("ENTER OrgAdmin.Create: name=%s, ownerUserID=%d, slug=%s", name, ownerUserID, slug)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT OrgAdmin.Create: error=%v", err)
		} else {
			logger.Infof("EXIT OrgAdmin.Create: orgID=%d, slug=%s", org.ID, org.Slug)
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
	user := &model.User{Model: model.Model{ID: ownerUserID}}
	if err = db.Take(user).Error; err != nil {
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
		logger.Error("DB failed to create organization ", err)
		err = NewCLError(ErrOrgCreationFailed, "Failed to create organization", err)
		return
	}

	// Auto-generate slug if not provided
	if slug == "" {
		org.Slug = fmt.Sprintf("org-%d", org.ID)
		if err = db.Model(org).Update("slug", org.Slug).Error; err != nil {
			logger.Error("DB failed to update org slug", err)
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
		logger.Error("DB failed to create organization member ", err)
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
	logger.Infof("ENTER OrgAdmin.AddMember: orgID=%d, userID=%d, role=%v", orgID, userID, role)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT OrgAdmin.AddMember: error=%v", err)
		} else {
			logger.Info("EXIT OrgAdmin.AddMember: success")
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
	user := &model.User{Model: model.Model{ID: userID}}
	if err = db.Take(user).Error; err != nil {
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
		logger.Error("DB failed to create member", err)
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
	logger.Infof("ENTER OrgAdmin.RemoveMember: orgID=%d, userID=%d", orgID, userID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT OrgAdmin.RemoveMember: error=%v", err)
		} else {
			logger.Info("EXIT OrgAdmin.RemoveMember: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.CanManageTargetOrg(orgID) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to manage members of this org", nil)
		return
	}

	db := DB()

	// Check if user is Org Owner — cannot remove owner
	org := &model.Organization{Model: model.Model{ID: orgID}}
	if err = db.Take(org).Error; err != nil {
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
	logger.Infof("ENTER OrgAdmin.UpdateMemberRole: orgID=%d, userID=%d, newRole=%v", orgID, userID, newRole)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT OrgAdmin.UpdateMemberRole: error=%v", err)
		} else {
			logger.Info("EXIT OrgAdmin.UpdateMemberRole: success")
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
	org := &model.Organization{Model: model.Model{ID: orgID}}
	if err = db.Take(org).Error; err != nil {
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
	logger.Infof("ENTER OrgAdmin.TransferOwner: orgID=%d, newOwner=%d", orgID, newOwnerUserID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT OrgAdmin.TransferOwner: error=%v", err)
		} else {
			logger.Info("EXIT OrgAdmin.TransferOwner: success")
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
	if err = db.Set("gorm:query_option", "FOR UPDATE").Where("id = ?", orgID).Take(org).Error; err != nil {
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
	logger.Infof("ENTER OrgAdmin.RenameOrg: orgID=%d, newName=%s", orgID, newName)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT OrgAdmin.RenameOrg: error=%v", err)
		} else {
			logger.Info("EXIT OrgAdmin.RenameOrg: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.CanManageTargetOrg(orgID) {
		err = NewCLError(ErrPermissionDenied, "Not authorized to rename this org", nil)
		return
	}

	db := DB()
	org := &model.Organization{Model: model.Model{ID: orgID}}
	if err = db.Take(org).Error; err != nil {
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
	logger.Infof("ENTER OrgAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT OrgAdmin.Get: error=%v", err)
		} else {
			logger.Infof("EXIT OrgAdmin.Get: orgUUID=%s", org.UUID)
		}
	}()
	if id <= 0 {
		err = NewCLError(ErrInvalidParameter, fmt.Sprintf("Invalid org ID: %d", id), nil)
		logger.Error("%v", err)
		return
	}
	ctx, db := GetContextDB(ctx)
	org = &model.Organization{Model: model.Model{ID: id}}
	err = db.Take(org).Error
	if err != nil {
		logger.Error("Failed to query org, %v", err)
		err = NewCLError(ErrOrgNotFound, "Failed to find organization", err)
		return
	}
	return
}

func (a *OrgAdmin) GetOrgByUUID(ctx context.Context, uuID string) (org *model.Organization, err error) {
	logger.Infof("ENTER OrgAdmin.GetOrgByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT OrgAdmin.GetOrgByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT OrgAdmin.GetOrgByUUID: orgID=%d", org.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	org = &model.Organization{}
	err = db.Preload("Members.User").Where("uuid = ?", uuID).Take(org).Error
	if err != nil {
		logger.Error("Failed to query org, %v", err)
		err = NewCLError(ErrOrgNotFound, "Failed to find organization", err)
		return
	}
	return
}

func (a *OrgAdmin) GetOrgByName(ctx context.Context, name string) (org *model.Organization, err error) {
	logger.Infof("ENTER OrgAdmin.GetOrgByName: name=%s", name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT OrgAdmin.GetOrgByName: error=%v", err)
		} else {
			logger.Infof("EXIT OrgAdmin.GetOrgByName: orgID=%d", org.ID)
		}
	}()
	org = &model.Organization{}
	ctx, db := GetContextDB(ctx)
	err = db.Where("name = ?", name).Take(org).Error
	if err != nil {
		logger.Error("Failed to query org, %v", err)
		err = NewCLError(ErrOrgNotFound, "Failed to find organization", err)
		return
	}
	return
}

func (a *OrgAdmin) GetOrgName(ctx context.Context, id int64) (name string) {
	logger.Infof("ENTER OrgAdmin.GetOrgName: id=%d", id)
	defer func() {
		logger.Infof("EXIT OrgAdmin.GetOrgName: name=%s", name)
	}()
	org := &model.Organization{Model: model.Model{ID: id}}
	ctx, db := GetContextDB(ctx)
	err := db.Take(org).Error
	if err != nil {
		logger.Error("DB failed to query org", err)
		return
	}
	name = org.Name
	return
}

// Delete deletes an Org. Only SystemAdmin can do this. OrgTypeSystem cannot be deleted.
func (a *OrgAdmin) Delete(ctx context.Context, org *model.Organization) (err error) {
	logger.Infof("ENTER OrgAdmin.Delete: orgID=%d, name=%s", org.ID, org.Name)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT OrgAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT OrgAdmin.Delete: success")
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
		{&model.LoadBalancer{}, "owner = ?"},
		{&model.Router{}, "owner = ?"},
		{&model.Image{}, "owner = ?"},
		{&model.Interface{}, "owner = ? AND type <> 'gateway'"},
	}
	for _, rc := range resourceChecks {
		var resCount int64
		if err = db.Model(rc.target).Where(rc.where, org.ID).Count(&resCount).Error; err != nil {
			logger.Error("DB failed to query resources, %v", err)
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
		logger.Error("DB failed to delete members, %v", err)
		err = NewCLError(ErrMemberDeleteFailed, "Failed to delete organization members", err)
		return
	}

	// Delete keys
	keys := []*model.Key{}
	if dbErr := db.Where("owner = ?", org.ID).Find(&keys).Error; dbErr == nil {
		for _, key := range keys {
			if keyErr := (&KeyAdminService{}).Delete(ctx, key); keyErr != nil {
				logger.Error("Failed to delete key", keyErr)
			}
		}
	}

	// Delete security groups
	secgroups := []*model.SecurityGroup{}
	if dbErr := db.Where("owner = ?", org.ID).Find(&secgroups).Error; dbErr == nil {
		for _, secgroup := range secgroups {
			if sgErr := secgroupAdmin.Delete(ctx, secgroup); sgErr != nil {
				logger.Error("Failed to delete security group", sgErr)
			}
		}
	}

	// Soft delete org first, then rename with Unscoped to avoid stale name on delete failure
	if err = db.Delete(org).Error; err != nil {
		logger.Error("DB failed to delete organization, %v", err)
		err = NewCLError(ErrOrgDeleteFailed, "Failed to delete organization", err)
		return
	}
	timestamp := org.CreatedAt.Unix()
	org.Name = fmt.Sprintf("%s-deleted-%d", org.Name, timestamp)
	org.Slug = fmt.Sprintf("%s-deleted-%d", org.Slug, timestamp)
	if err = db.Model(&model.Organization{}).Unscoped().Where("id = ?", org.ID).Updates(map[string]interface{}{"name": org.Name, "slug": org.Slug}).Error; err != nil {
		logger.Error("DB failed to update org name", err)
		err = NewCLError(ErrOrgUpdateFailed, "Failed to update organization name", err)
		return
	}

	// Post-check: set affected users to Dormant if they have no other memberships
	// This runs inside the transaction (db is transaction-scoped).
	for _, uid := range affectedUserIDs {
		var remainingCount int64
		if cntErr := db.Model(&model.Member{}).Where("user_id = ? AND deleted_at IS NULL", uid).Count(&remainingCount).Error; cntErr != nil {
			logger.Errorf("Failed to count remaining memberships for user %d: %v", uid, cntErr)
			continue
		}
		if remainingCount == 0 {
			if updErr := db.Model(&model.User{Model: model.Model{ID: uid}}).Update("status", model.UserDormant).Error; updErr != nil {
				logger.Errorf("Failed to set user %d to Dormant: %v", uid, updErr)
			}
		}
	}
	return
}

func (a *OrgAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, orgs []*model.Organization, err error) {
	logger.Infof("ENTER OrgAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT OrgAdmin.List: error=%v", err)
		} else {
			logger.Infof("EXIT OrgAdmin.List: total=%d, orgsCount=%d", total, len(orgs))
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
		logger.Error("DB failed to count organizations, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to count organizations", err)
		return
	}
	db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
	err = db.Preload("Members.User").Where(whereQuery, whereArgs...).Find(&orgs).Error
	if err != nil {
		logger.Error("DB failed to query organizations, %v", err)
		err = NewCLError(ErrSQLSyntaxError, "Failed to query organizations", err)
		return
	}
	return
}

// UpsertOrgByID syncs an org record from CPGateway into the local organizations table.
// Uses PostgreSQL INSERT ... ON CONFLICT (id) DO UPDATE so it is safe to call multiple times.
// owner_user_id is always set to 1 (the local admin user); default_sg stays 0 and is
// lazily created the first time the org creates a VM.
func (a *OrgAdmin) UpsertOrgByID(ctx context.Context, id int64, name, slug string) error {
	logger.Infof("ENTER OrgAdmin.UpsertOrgByID: id=%d name=%s slug=%s", id, name, slug)
	_, db := GetContextDB(ctx)
	err := db.Exec(`
		INSERT INTO organizations (id, name, slug, org_type, owner_user_id, default_sg, created_at, updated_at)
		VALUES (?, ?, ?, 1, 1, 0, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, slug = EXCLUDED.slug, updated_at = NOW()
	`, id, name, slug).Error
	if err != nil {
		logger.Errorf("EXIT OrgAdmin.UpsertOrgByID: error=%v", err)
	} else {
		logger.Infof("EXIT OrgAdmin.UpsertOrgByID: ok")
	}
	return err
}

// --- View layer ---

