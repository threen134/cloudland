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
	"sync"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

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
	org = &model.Organization{Model: model.Model{ID: id}}
	err = db.Take(org).Error
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

func (a *OrgAdmin) GetOrgName(ctx context.Context, id int64) (name string) {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.GetOrgName: id=%d", id)
	defer func() {
		logger.Ctx(ctx).Infof("EXIT OrgAdmin.GetOrgName: name=%s", name)
	}()
	org := &model.Organization{Model: model.Model{ID: id}}
	ctx, db := GetContextDB(ctx)
	err := db.Take(org).Error
	if err != nil {
		logger.Ctx(ctx).Error("DB failed to query org", err)
		return
	}
	name = org.Name
	return
}

// GetOrgUUID returns the UUID of a local org ID (empty when not found). The mapping never changes once an
// org exists, so results are cached like GetOrgIDByUUID.
func (a *OrgAdmin) GetOrgUUID(ctx context.Context, id int64) string {
	if cached, ok := orgUUIDByID.Load(id); ok {
		return cached.(string)
	}
	org := &model.Organization{Model: model.Model{ID: id}}
	_, db := GetContextDB(ctx)
	if err := db.Select("uuid").Take(org).Error; err != nil || org.UUID == "" {
		return ""
	}
	orgUUIDByID.Store(id, org.UUID)
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

// UpsertOrgByID syncs an org record from CPGateway into the local organizations table.
// Uses PostgreSQL INSERT ... ON CONFLICT (id) DO UPDATE so it is safe to call multiple times.
// owner_user_id is always set to 1 (the local admin user); default_sg stays 0 and is
// lazily created the first time the org creates a VM.
// orgIDByUUID 缓存组织 UUID 到本区域组织 ID 的映射。该对应关系一旦建立就不再变化，
// 无需失效策略；目的是保持 authorize 每请求不查库的特性
var orgIDByUUID sync.Map

// orgUUIDByID caches the reverse mapping (local org ID -> UUID) for resource responses
var orgUUIDByID sync.Map

// GetOrgIDByUUID 把 cpgateway 传来的组织 UUID 解析成本区域的组织 ID。
// 两侧组织表的自增主键各自独立，UUID 才是跨服务契约，不能直接拿对方的 ID 当本地 ID 用
func (a *OrgAdmin) GetOrgIDByUUID(ctx context.Context, uuID string) (int64, error) {
	if v, ok := orgIDByUUID.Load(uuID); ok {
		return v.(int64), nil
	}
	_, db := GetContextDB(ctx)
	org := &model.Organization{}
	if err := db.Where("uuid = ?", uuID).Take(org).Error; err != nil {
		return 0, NewCLError(ErrOrgNotFound, "Organization not found for uuid "+uuID, err)
	}
	orgIDByUUID.Store(uuID, org.ID)
	return org.ID, nil
}

// UpsertOrgByUUID 按 UUID 同步组织，本地 ID 由本区域自行分配。
// 迁移期的衔接：早先的同步不带 UUID，本地已有同名 slug 的组织（其 UUID 是本区域自己生成的，
// 与控制面不同）——这种行上补写控制面的 UUID，避免新建一条导致存量资源的 owner 指向旧组织
func (a *OrgAdmin) UpsertOrgByUUID(ctx context.Context, uuID, name, slug string) error {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.UpsertOrgByUUID: uuid=%s name=%s slug=%s", uuID, name, slug)
	_, db := GetContextDB(ctx)
	org := &model.Organization{}
	err := db.Where("uuid = ?", uuID).Take(org).Error
	if err == nil {
		err = db.Model(org).Updates(map[string]interface{}{"name": name, "slug": slug}).Error
	} else if slug != "" && db.Where("slug = ?", slug).Take(org).Error == nil {
		err = db.Model(org).Updates(map[string]interface{}{"uuid": uuID, "name": name}).Error
		if err == nil {
			logger.Ctx(ctx).Infof("Adopted existing org id=%d (slug=%s) with control-plane uuid=%s", org.ID, slug, uuID)
		}
	} else {
		err = db.Create(&model.Organization{
			Model: model.Model{UUID: uuID}, Name: name, Slug: slug, OrgType: 1, OwnerUserID: 1,
		}).Error
	}
	if err != nil {
		logger.Ctx(ctx).Errorf("EXIT OrgAdmin.UpsertOrgByUUID: error=%v", err)
		return err
	}
	orgIDByUUID.Store(uuID, org.ID)
	logger.Ctx(ctx).Infof("EXIT OrgAdmin.UpsertOrgByUUID: ok id=%d", org.ID)
	return nil
}

func (a *OrgAdmin) UpsertOrgByID(ctx context.Context, id int64, name, slug string) error {
	logger.Ctx(ctx).Infof("ENTER OrgAdmin.UpsertOrgByID: id=%d name=%s slug=%s", id, name, slug)
	_, db := GetContextDB(ctx)
	err := db.Exec(`
		INSERT INTO organizations (id, name, slug, org_type, owner_user_id, default_sg, created_at, updated_at)
		VALUES (?, ?, ?, 1, 1, 0, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, slug = EXCLUDED.slug, updated_at = NOW()
	`, id, name, slug).Error
	if err != nil {
		logger.Ctx(ctx).Errorf("EXIT OrgAdmin.UpsertOrgByID: error=%v", err)
	} else {
		logger.Ctx(ctx).Infof("EXIT OrgAdmin.UpsertOrgByID: ok")
	}
	return err
}

// --- View layer ---
