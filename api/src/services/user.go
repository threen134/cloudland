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
	"strconv"
	"strings"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var (
	userAdmin = &UserAdmin{}
)

type UserAdmin struct{}


// Create creates a new user. Two modes:
// 1. CPGateway call (scenario 2B): no system_role, creates Dormant user without Org
// 2. SystemAdmin creating another SystemAdmin: creates Active user, adds to admin org
func (a *UserAdmin) Create(ctx context.Context, email, password, uuid string, sysRole ...model.SystemRole) (user *model.User, err error) {
	logger.Infof("ENTER UserAdmin.Create: email=%s, uuid=%s, sysRoleCount=%d", email, uuid, len(sysRole))
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.Create: error=%v", err)
		} else {
			logger.Infof("EXIT UserAdmin.Create: userID=%d", user.ID)
		}
	}()
	memberShip := GetMemberShip(ctx)

	// Determine target system role
	targetSysRole := model.SystemUser
	if len(sysRole) > 0 && sysRole[0] == model.SystemAdmin {
		if !memberShip.IsSystemAdmin() {
			err = NewCLError(ErrPermissionDenied, "Only SystemAdmin can create SystemAdmin users", nil)
			return
		}
		targetSysRole = model.SystemAdmin
	}

	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()

	if password, err = a.GenerateFromPassword(password); err != nil {
		return
	}

	// Determine initial status
	status := model.UserDormant // Default for users without Org
	if targetSysRole == model.SystemAdmin {
		status = model.UserActive
	}

	user = &model.User{
		Model:      model.Model{Creater: memberShip.UserID},
		Email:      email,
		Password:   password,
		SystemRole: targetSysRole,
		Status:     status,
	}
	if uuid != "" {
		user.UUID = uuid
	}
	err = db.Create(user).Error
	if err != nil {
		logger.Error("DB failed to create user, %v", err)
		err = NewCLError(ErrUserCreationFailed, "Failed to create user", err)
		return
	}

	// If creating SystemAdmin, auto-add to admin org
	if targetSysRole == model.SystemAdmin {
		adminOrg := &model.Organization{}
		if err = db.Where("org_type = ?", model.OrgTypeSystem).Take(adminOrg).Error; err != nil {
			logger.Error("Failed to find admin org", err)
			err = NewCLError(ErrOrgNotFound, "Admin org not found", err)
			return
		}
		member := &model.Member{
			UserID:  user.ID,
			OrgID:   adminOrg.ID,
			OrgRole: model.OrgAdmin,
		}
		if err = db.Create(member).Error; err != nil {
			logger.Error("Failed to create admin member", err)
			err = NewCLError(ErrMemberCreationFailed, "Failed to create admin member", err)
			return
		}
	}
	return
}

// CreateWithOrg creates a user with a new Org atomically (scenario 1: new user registration).
func (a *UserAdmin) CreateWithOrg(ctx context.Context, email, password, orgName, slug string) (user *model.User, org *model.Organization, err error) {
	logger.Infof("ENTER UserAdmin.CreateWithOrg: email=%s, orgName=%s, slug=%s", email, orgName, slug)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.CreateWithOrg: error=%v", err)
		} else {
			logger.Infof("EXIT UserAdmin.CreateWithOrg: userID=%d, orgID=%d", user.ID, org.ID)
		}
	}()
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()

	if password, err = a.GenerateFromPassword(password); err != nil {
		return
	}

	// Create user
	user = &model.User{
		Email:    email,
		Password: password,
		Status:   model.UserActive,
	}
	if err = db.Create(user).Error; err != nil {
		logger.Error("DB failed to create user, %v", err)
		err = NewCLError(ErrUserCreationFailed, "Failed to create user", err)
		return
	}

	// Determine slug
	if slug == "" {
		if user.UUID == "" {
			slug = fmt.Sprintf("org-%s", uuid.New().String()[:8])
		} else {
			slug = fmt.Sprintf("org-%s", strings.Split(user.UUID, "-")[0])
		}
	}

	// Create org
	org = &model.Organization{
		Name:        orgName,
		Slug:        slug,
		OrgType:     model.OrgTypeTeam,
		OwnerUserID: user.ID,
	}
	if err = db.Create(org).Error; err != nil {
		logger.Error("DB failed to create organization, %v", err)
		err = NewCLError(ErrOrgCreationFailed, "Failed to create organization", err)
		return
	}

	// Create member (OrgAdmin)
	member := &model.Member{
		UserID:  user.ID,
		OrgID:   org.ID,
		OrgRole: model.OrgAdmin,
	}
	if err = db.Create(member).Error; err != nil {
		logger.Error("DB failed to create member, %v", err)
		err = NewCLError(ErrMemberCreationFailed, "Failed to create organization member", err)
		return
	}

	return
}

// Validate checks email+password for login.
func (a *UserAdmin) Validate(ctx context.Context, email, password string) (user *model.User, err error) {
	logger.Infof("ENTER UserAdmin.Validate: email=%s", email)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.Validate: error=%v", err)
		} else {
			logger.Infof("EXIT UserAdmin.Validate: userID=%d", user.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	user = &model.User{}
	err = db.Take(user, "email = ?", email).Error
	if err != nil {
		logger.Error("DB failed to query user", err)
		err = NewCLError(ErrUserNotFound, "User not found", err)
		return
	}
	err = a.CompareHashAndPassword(user.Password, password)
	return
}

// ValidateEmail checks if an email is already registered (public endpoint for middleware).
func (a *UserAdmin) ValidateEmail(ctx context.Context, email string) (exists bool, userUUID string, err error) {
	logger.Infof("ENTER UserAdmin.ValidateEmail: email=%s", email)
	defer func() {
		logger.Infof("EXIT UserAdmin.ValidateEmail: exists=%v, userUUID=%s", exists, userUUID)
	}()
	db := DB()
	user := &model.User{}
	err = db.Where("email = ?", email).Take(user).Error
	if err != nil {
		// Not found is not an error here
		return false, "", nil
	}
	return true, user.UUID, nil
}

// GetUserByEmail looks up a user by email.
func (a *UserAdmin) GetUserByEmail(email string) (user *model.User, err error) {
	logger.Infof("ENTER UserAdmin.GetUserByEmail: email=%s", email)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.GetUserByEmail: error=%v", err)
		} else {
			logger.Infof("EXIT UserAdmin.GetUserByEmail: userID=%d", user.ID)
		}
	}()
	db := DB()
	user = &model.User{}
	if err = db.Where("email = ?", email).Take(user).Error; err != nil {
		logger.Error("DB failed to get user", err)
		err = NewCLError(ErrUserNotFound, "User not found", err)
		return
	}
	return
}

func (a *UserAdmin) Get(ctx context.Context, id int64) (user *model.User, err error) {
	logger.Infof("ENTER UserAdmin.Get: id=%d", id)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.Get: error=%v", err)
		} else {
			logger.Infof("EXIT UserAdmin.Get: userUUID=%s", user.UUID)
		}
	}()
	if id <= 0 {
		err = fmt.Errorf("Invalid user ID: %d", id)
		logger.Error("%v", err)
		return
	}
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	user = &model.User{Model: model.Model{ID: id}}
	err = db.Take(user).Error
	if err != nil {
		logger.Error("Failed to query user, %v", err)
		err = NewCLError(ErrUserNotFound, "Failed to query user", err)
		return
	}
	// SystemAdmin can read any user; otherwise check via membership
	if !memberShip.IsSystemAdmin() {
		permit, _ := memberShip.CheckUser(id)
		if !permit {
			logger.Error("Not authorized to read the user")
			err = NewCLError(ErrPermissionDenied, "Not authorized to read the user", nil)
			return
		}
	}
	return
}

func (a *UserAdmin) GetUserByUUID(ctx context.Context, uuID string) (user *model.User, err error) {
	logger.Infof("ENTER UserAdmin.GetUserByUUID: uuID=%s", uuID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.GetUserByUUID: error=%v", err)
		} else {
			logger.Infof("EXIT UserAdmin.GetUserByUUID: userID=%d", user.ID)
		}
	}()
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	user = &model.User{}
	err = db.Where("uuid = ?", uuID).Take(user).Error
	if err != nil {
		logger.Error("Failed to query user, %v", err)
		err = NewCLError(ErrUserNotFound, "User not found", err)
		return
	}
	if !memberShip.IsSystemAdmin() {
		permit, _ := memberShip.CheckUser(user.ID)
		if !permit {
			logger.Error("Not authorized to read the user")
			err = NewCLError(ErrPermissionDenied, "Not authorized to read the user", nil)
			return
		}
	}
	return
}

// Delete deletes a user. Only SystemAdmin can delete users.
func (a *UserAdmin) Delete(ctx context.Context, user *model.User) (err error) {
	logger.Infof("ENTER UserAdmin.Delete: userID=%d, email=%s", user.ID, user.Email)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.Delete: error=%v", err)
		} else {
			logger.Info("EXIT UserAdmin.Delete: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.IsSystemAdmin() {
		err = NewCLError(ErrPermissionDenied, "Not authorized to delete the user", nil)
		return
	}
	// Cannot delete self
	if memberShip.UserID == user.ID {
		err = NewCLError(ErrPermissionDenied, "Cannot delete yourself", nil)
		return
	}
	// If target is SystemAdmin, must have > 1 SystemAdmin
	if user.SystemRole == model.SystemAdmin {
		var count int64
		db := DB()
		db.Model(&model.User{}).Where("system_role = ? AND deleted_at IS NULL", model.SystemAdmin).Count(&count)
		if count <= 1 {
			err = NewCLError(ErrLastSystemAdmin, "Cannot delete the last SystemAdmin", nil)
			return
		}
	}
	// Check if user is Owner of any Org
	db := DB()
	var ownedOrgs []*model.Organization
	db.Where("owner_user_id = ? AND deleted_at IS NULL", user.ID).Find(&ownedOrgs)
	for _, org := range ownedOrgs {
		var memberCount int64
		db.Model(&model.Member{}).Where("org_id = ? AND deleted_at IS NULL", org.ID).Count(&memberCount)
		if memberCount > 1 {
			err = NewCLError(ErrOrgHasMembers, fmt.Sprintf("User is owner of Org '%s' which has other members. Transfer ownership first.", org.Name), nil)
			return
		}
		// Single-member org: check for cloud resources across all resource types
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
			db.Model(rc.target).Where(rc.where, org.ID).Count(&resCount)
			if resCount > 0 {
				err = NewCLError(ErrOrgHasResources, fmt.Sprintf("Org '%s' still has resources. Clear them before deleting the user.", org.Name), nil)
				return
			}
		}
	}

	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()

	// Hard delete all member records to avoid unique index clashing
	if err = db.Unscoped().Where("user_id = ?", user.ID).Delete(&model.Member{}).Error; err != nil {
		logger.Error("DB failed to delete members", err)
		err = NewCLError(ErrMemberDeleteFailed, "Failed to delete organization members", err)
		return
	}
	// Dissolve single-member owned orgs
	for _, org := range ownedOrgs {
		if err = db.Delete(org).Error; err != nil {
			logger.Error("DB failed to delete org", err)
			err = NewCLError(ErrOrgDeleteFailed, "Failed to delete organization", err)
			return
		}
	}
	// Soft delete user first, then rename with Unscoped to avoid stale email on delete failure
	if err = db.Delete(user).Error; err != nil {
		logger.Error("DB failed to delete user", err)
		err = NewCLError(ErrUserDeleteFailed, "Failed to delete user", err)
		return
	}
	user.Email = fmt.Sprintf("%s-deleted-%d", user.Email, user.CreatedAt.Unix())
	if err = db.Model(&model.User{}).Unscoped().Where("id = ?", user.ID).Update("email", user.Email).Error; err != nil {
		logger.Error("DB failed to update user email for deletion", err)
		err = NewCLError(ErrUserUpdateFailed, "Failed to update user email", err)
		return
	}
	return
}

func (a *UserAdmin) List(ctx context.Context, offset, limit int64, order, query string) (total int64, users []*model.User, err error) {
	logger.Infof("ENTER UserAdmin.List: offset=%d, limit=%d, order=%s, query=%s", offset, limit, order, query)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.List: error=%v", err)
		} else {
			logger.Infof("EXIT UserAdmin.List: total=%d, usersCount=%d", total, len(users))
		}
	}()
	memberShip := GetMemberShip(ctx)
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}
	if order == "" {
		order = "created_at"
	}

	emailFilter := ""
	emailArgs := []interface{}{}
	if query != "" {
		emailFilter = "email LIKE ?"
		emailArgs = append(emailArgs, "%"+query+"%")
	}
	if !memberShip.IsSystemAdmin() {
		org := &model.Organization{Model: model.Model{ID: memberShip.OrgID}}
		if err = db.Set("gorm:auto_preload", true).Take(org).Error; err != nil {
			logger.Error("Failed to query organization", err)
			err = NewCLError(ErrOrgNotFound, "Failed to query organization", err)
			return
		}
		var userIDs []int64
		if org.Members != nil {
			for _, member := range org.Members {
				userIDs = append(userIDs, member.UserID)
			}
		}
		if err = db.Model(&model.User{}).Where("id IN (?)", userIDs).Where(emailFilter, emailArgs...).Count(&total).Error; err != nil {
			logger.Error("DB failed to count users", err)
			err = NewCLError(ErrDatabaseError, "Failed to count users", err)
			return
		}
		db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
		if err = db.Where("id IN (?)", userIDs).Where(emailFilter, emailArgs...).Find(&users).Error; err != nil {
			logger.Error("DB failed to get user list, %v", err)
			err = NewCLError(ErrDatabaseError, "Failed to get user list", err)
			return
		}
	} else {
		if err = db.Model(&model.User{}).Where(emailFilter, emailArgs...).Count(&total).Error; err != nil {
			logger.Error("DB failed to count users", err)
			err = NewCLError(ErrDatabaseError, "Failed to count users", err)
			return
		}
		db = dbs.Sortby(db.Offset(offset).Limit(limit), order)
		if err = db.Where(emailFilter, emailArgs...).Find(&users).Error; err != nil {
			logger.Error("DB failed to get user list, %v", err)
			err = NewCLError(ErrDatabaseError, "Failed to get user list", err)
			return
		}
	}
	return
}

// AccessToken generates a JWT token for login.
// Deprecated: Token issuance is now handled by CPGateway. This function is kept
// for web UI login compatibility during migration. Will be removed.
func (a *UserAdmin) AccessToken(ctx context.Context, uid int64, orgID ...int64) (oid int64, sysRole model.SystemRole, orgRole model.OrgRole, status model.UserStatus, token string, issueAt, expiresAt int64, err error) {
	logger.Infof("ENTER UserAdmin.AccessToken: uid=%d, orgIDCount=%d", uid, len(orgID))
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.AccessToken: error=%v", err)
		} else {
			logger.Infof("EXIT UserAdmin.AccessToken: oid=%d, sysRole=%v, orgRole=%v", oid, sysRole, orgRole)
		}
	}()
	ctx, db := GetContextDB(ctx)

	user := &model.User{Model: model.Model{ID: uid}}
	if err = db.Take(user).Error; err != nil {
		err = NewCLError(ErrUserNotFound, "User not found", err)
		return
	}

	// Check user status
	if user.Status == model.UserDisabled {
		err = NewCLError(ErrUserDisabled, "User is disabled", nil)
		return
	}

	sysRole = user.SystemRole
	status = user.Status

	// Find org context
	if len(orgID) > 0 && orgID[0] > 0 {
		oid = orgID[0]
	} else {
		// Get first membership (ordered by created_at)
		member := &model.Member{}
		if err = db.Where("user_id = ?", uid).Order("created_at").Take(member).Error; err != nil {
			// No membership — Dormant user
			if user.Status == model.UserDormant || user.SystemRole == model.SystemAdmin {
				oid = 0
				orgRole = model.OrgNone
				token, issueAt, expiresAt, err = NewToken(
					user.Email, "", strconv.FormatInt(uid, 10), "",
					sysRole, orgRole, status,
				)
				return
			}
			err = NewCLError(ErrMemberNotFound, "No organization membership found", err)
			return
		}
		oid = member.OrgID
		orgRole = member.OrgRole
	}

	// Validate membership
	if oid > 0 {
		member := &model.Member{}
		if err = db.Where("user_id = ? AND org_id = ?", uid, oid).Take(member).Error; err != nil {
			if user.SystemRole != model.SystemAdmin {
				err = NewCLError(ErrMemberNotFound, "Not a member of this organization", err)
				return
			}
			// SystemAdmin can access any org
			orgRole = model.OrgNone
		} else {
			orgRole = member.OrgRole
		}
	}

	org := &model.Organization{Model: model.Model{ID: oid}}
	if oid > 0 {
		if err = db.Take(org).Error; err != nil {
			err = NewCLError(ErrOrgNotFound, "Organization not found", err)
			return
		}
	}

	token, issueAt, expiresAt, err = NewToken(
		user.Email, org.Name,
		strconv.FormatInt(uid, 10), strconv.FormatInt(oid, 10),
		sysRole, orgRole, status,
	)
	return
}

// SwitchOrg switches the user's current org context and returns a new token.
// Deprecated: Org switching is now handled by CPGateway. This function is kept
// for web UI compatibility during migration. Will be removed.
func (a *UserAdmin) SwitchOrg(ctx context.Context, uid, targetOrgID int64) (token string, issueAt, expiresAt int64, err error) {
	logger.Infof("ENTER UserAdmin.SwitchOrg: uid=%d, targetOrgID=%d", uid, targetOrgID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.SwitchOrg: error=%v", err)
		} else {
			logger.Info("EXIT UserAdmin.SwitchOrg: success")
		}
	}()
	db := DB()

	user := &model.User{Model: model.Model{ID: uid}}
	if err = db.Take(user).Error; err != nil {
		err = NewCLError(ErrUserNotFound, "User not found", err)
		return
	}

	// SystemAdmin can switch to any org
	if user.SystemRole == model.SystemAdmin {
		org := &model.Organization{Model: model.Model{ID: targetOrgID}}
		if err = db.Take(org).Error; err != nil {
			err = NewCLError(ErrOrgNotFound, "Organization not found", err)
			return
		}
		member := &model.Member{}
		orgRole := model.OrgNone
		if err2 := db.Where("user_id = ? AND org_id = ?", uid, targetOrgID).Take(member).Error; err2 == nil {
			orgRole = member.OrgRole
		}
		token, issueAt, expiresAt, err = NewToken(
			user.Email, org.Name,
			strconv.FormatInt(uid, 10), strconv.FormatInt(targetOrgID, 10),
			user.SystemRole, orgRole, user.Status,
		)
		return
	}

	// Normal user: must be a member of target org
	member := &model.Member{}
	if err = db.Where("user_id = ? AND org_id = ?", uid, targetOrgID).Take(member).Error; err != nil {
		err = NewCLError(ErrMemberNotFound, "Not a member of this organization", err)
		return
	}

	org := &model.Organization{Model: model.Model{ID: targetOrgID}}
	if err = db.Take(org).Error; err != nil {
		err = NewCLError(ErrOrgNotFound, "Organization not found", err)
		return
	}

	token, issueAt, expiresAt, err = NewToken(
		user.Email, org.Name,
		strconv.FormatInt(uid, 10), strconv.FormatInt(targetOrgID, 10),
		user.SystemRole, member.OrgRole, user.Status,
	)
	return
}

// ChangePassword allows any logged-in user to change their own password, or a SystemAdmin to change any user's password.
func (a *UserAdmin) ChangePassword(ctx context.Context, userID int64, oldPassword, newPassword string) (err error) {
	logger.Infof("ENTER UserAdmin.ChangePassword: userID=%d", userID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.ChangePassword: error=%v", err)
		} else {
			logger.Info("EXIT UserAdmin.ChangePassword: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	db := DB()
	user := &model.User{Model: model.Model{ID: userID}}
	if err = db.Take(user).Error; err != nil {
		err = NewCLError(ErrUserNotFound, "User not found", err)
		return
	}
	// Skip old password check if SystemAdmin is resetting someone else's password
	if !(memberShip.IsSystemAdmin() && memberShip.UserID != userID) {
		if err = a.CompareHashAndPassword(user.Password, oldPassword); err != nil {
			return
		}
	}
	hash, hashErr := a.GenerateFromPassword(newPassword)
	if hashErr != nil {
		return hashErr
	}
	if err = db.Model(user).Update("password", hash).Error; err != nil {
		err = NewCLError(ErrUserUpdateFailed, "Failed to update password", err)
	}
	return
}

// UpdateProfile allows users to update their own profile fields.
func (a *UserAdmin) UpdateProfile(ctx context.Context, targetUserID int64, firstName, lastName, region, language, remark string) (err error) {
	logger.Infof("ENTER UserAdmin.UpdateProfile: targetUserID=%d, name=%s %s", targetUserID, firstName, lastName)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.UpdateProfile: error=%v", err)
		} else {
			logger.Info("EXIT UserAdmin.UpdateProfile: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	db := DB()

	updates := map[string]interface{}{}
	if firstName != "" {
		updates["first_name"] = firstName
	}
	if lastName != "" {
		updates["last_name"] = lastName
	}
	if region != "" {
		updates["region"] = region
	}
	if language != "" {
		updates["language"] = language
	}

	// Remark can only be set by SystemAdmin on any user
	if remark != "" {
		if !memberShip.IsSystemAdmin() {
			err = NewCLError(ErrPermissionDenied, "Only SystemAdmin can modify remark", nil)
			return
		}
		updates["remark"] = remark
	}

	// Non-SystemAdmin can only update own profile
	if !memberShip.IsSystemAdmin() && memberShip.UserID != targetUserID {
		err = NewCLError(ErrPermissionDenied, "Can only update own profile", nil)
		return
	}

	if len(updates) > 0 {
		if err = db.Model(&model.User{Model: model.Model{ID: targetUserID}}).Updates(updates).Error; err != nil {
			err = NewCLError(ErrUserUpdateFailed, "Failed to update profile", err)
		}
	}
	return
}

// DemoteSystemAdmin demotes a SystemAdmin to SystemUser.
func (a *UserAdmin) DemoteSystemAdmin(ctx context.Context, targetUserID int64) (err error) {
	logger.Infof("ENTER UserAdmin.DemoteSystemAdmin: targetUserID=%d", targetUserID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.DemoteSystemAdmin: error=%v", err)
		} else {
			logger.Info("EXIT UserAdmin.DemoteSystemAdmin: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.IsSystemAdmin() {
		err = NewCLError(ErrPermissionDenied, "Only SystemAdmin can demote", nil)
		return
	}
	if memberShip.UserID == targetUserID {
		err = NewCLError(ErrCannotSelfDemote, "Cannot demote yourself", nil)
		return
	}

	db := DB()
	var count int64
	db.Model(&model.User{}).Where("system_role = ? AND deleted_at IS NULL", model.SystemAdmin).Count(&count)
	if count <= 1 {
		err = NewCLError(ErrLastSystemAdmin, "Cannot demote the last SystemAdmin", nil)
		return
	}

	// Update system_role
	if err = db.Model(&model.User{Model: model.Model{ID: targetUserID}}).Update("system_role", model.SystemUser).Error; err != nil {
		err = NewCLError(ErrUserUpdateFailed, "Failed to demote user", err)
		return
	}

	// Remove from admin org (hard delete to avoid unique constraint violations on re-add)
	adminOrg := &model.Organization{}
	if err = db.Where("org_type = ?", model.OrgTypeSystem).Take(adminOrg).Error; err == nil {
		db.Unscoped().Where("user_id = ? AND org_id = ?", targetUserID, adminOrg.ID).Delete(&model.Member{})
	}

	// Check if user has any remaining memberships
	var remainingCount int64
	db.Model(&model.Member{}).Where("user_id = ? AND deleted_at IS NULL", targetUserID).Count(&remainingCount)
	if remainingCount == 0 {
		db.Model(&model.User{Model: model.Model{ID: targetUserID}}).Update("status", model.UserDormant)
	}
	return
}

// Enable re-enables a disabled user.
func (a *UserAdmin) Enable(ctx context.Context, targetUserID int64) (err error) {
	logger.Infof("ENTER UserAdmin.Enable: targetUserID=%d", targetUserID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.Enable: error=%v", err)
		} else {
			logger.Info("EXIT UserAdmin.Enable: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.IsSystemAdmin() {
		err = NewCLError(ErrPermissionDenied, "Only SystemAdmin can enable users", nil)
		return
	}
	db := DB()
	var memberCount int64
	db.Model(&model.Member{}).Where("user_id = ? AND deleted_at IS NULL", targetUserID).Count(&memberCount)
	newStatus := model.UserDormant
	if memberCount > 0 {
		newStatus = model.UserActive
	}
	if err = db.Model(&model.User{Model: model.Model{ID: targetUserID}}).Update("status", newStatus).Error; err != nil {
		err = NewCLError(ErrUserUpdateFailed, "Failed to enable user", err)
	}
	return
}

// Disable disables a user (prevents login).
func (a *UserAdmin) Disable(ctx context.Context, targetUserID int64) (err error) {
	logger.Infof("ENTER UserAdmin.Disable: targetUserID=%d", targetUserID)
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.Disable: error=%v", err)
		} else {
			logger.Info("EXIT UserAdmin.Disable: success")
		}
	}()
	memberShip := GetMemberShip(ctx)
	if !memberShip.IsSystemAdmin() {
		err = NewCLError(ErrPermissionDenied, "Only SystemAdmin can disable users", nil)
		return
	}
	db := DB()
	if err = db.Model(&model.User{Model: model.Model{ID: targetUserID}}).Update("status", model.UserDisabled).Error; err != nil {
		err = NewCLError(ErrUserUpdateFailed, "Failed to disable user", err)
	}
	return
}

// GenerateFromPassword is slow by design, do not call it too often.
func (a *UserAdmin) GenerateFromPassword(password string) (hash string, err error) {
	logger.Info("ENTER UserAdmin.GenerateFromPassword")
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.GenerateFromPassword: error=%v", err)
		} else {
			logger.Info("EXIT UserAdmin.GenerateFromPassword: success")
		}
	}()
	b, err := bcrypt.GenerateFromPassword([]byte(password), 8)
	if err != nil {
		err = NewCLError(ErrPasswordHashFailed, "Failed to generate password hash", err)
		return
	}
	hash = string(b)
	return
}

// CompareHashAndPassword is slow by design, do not call it too often.
func (a *UserAdmin) CompareHashAndPassword(hash, password string) (err error) {
	logger.Info("ENTER UserAdmin.CompareHashAndPassword")
	defer func() {
		if err != nil {
			logger.Errorf("EXIT UserAdmin.CompareHashAndPassword: error=%v", err)
		} else {
			logger.Info("EXIT UserAdmin.CompareHashAndPassword: success")
		}
	}()
	err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil {
		err = NewCLError(ErrPasswordMismatch, "Failed to compare password hash", err)
	}
	return
}

// --- View layer (web UI) ---

