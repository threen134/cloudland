/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"context"

	"api/src/model"
)

type MemberShip struct {
	UserID     int64
	UserEmail  string
	SystemRole model.SystemRole
	OrgID      int64
	OrgName    string
	OrgRole    model.OrgRole
	IsOrgOwner bool
}

// IsSystemAdmin checks if the user is a system administrator.
func (m *MemberShip) IsSystemAdmin() bool {
	return m.SystemRole == model.SystemAdmin
}

// EffectiveOrgRole returns the effective OrgRole, considering Org Owner status.
// If user is Org Owner, effective role is at least OrgAdmin.
func (m *MemberShip) EffectiveOrgRole() model.OrgRole {
	if m.IsOrgOwner && m.OrgRole < model.OrgAdmin {
		return model.OrgAdmin
	}
	return m.OrgRole
}

// GetOrgFilter returns parameterized SQL filter for resource queries.
// SystemAdmin: ("", nil) — global view (all orgs)
// Normal user: ("owner = ?", []interface{}{m.OrgID})
func (m *MemberShip) GetOrgFilter() (query string, args []interface{}) {
	if m.IsSystemAdmin() {
		return "", nil
	}
	return "owner = ?", []interface{}{m.OrgID}
}

// CheckOrgPermission checks if the user has the required OrgRole in the current Org.
// Uses EffectiveOrgRole to account for Org Owner status.
func (m *MemberShip) CheckOrgPermission(reqRole model.OrgRole) bool {
	if m.IsSystemAdmin() {
		return true
	}
	return m.EffectiveOrgRole() >= reqRole
}

// CheckSystemPermission checks if the user is a SystemAdmin.
func (m *MemberShip) CheckSystemPermission() bool {
	return m.IsSystemAdmin()
}

// CheckResourceOrg checks if the user has the required role AND the resource belongs
// to the user's current Org. ownerOrgID is the resource's Owner field value.
func (m *MemberShip) CheckResourceOrg(reqRole model.OrgRole, ownerOrgID int64) bool {
	if ownerOrgID == 0 {
		return false
	}
	if m.IsSystemAdmin() {
		return true
	}
	if m.EffectiveOrgRole() < reqRole {
		return false
	}
	return m.OrgID == ownerOrgID
}

// CheckResourceOrgByID queries the DB to get the resource's owner Org, then validates.
func (m *MemberShip) CheckResourceOrgByID(reqRole model.OrgRole, table string, id int64) (bool, error) {
	if id == 0 {
		return false, nil
	}
	if m.IsSystemAdmin() {
		return true, nil
	}
	if m.EffectiveOrgRole() < reqRole {
		return false, nil
	}
	type Result struct {
		Owner int64
	}
	var result Result
	db := DB()
	err := db.Table(table).Select("owner").Where("id = ?", id).Scan(&result).Error
	if err != nil {
		logger.Error("Failed to query resource owner", err)
		return false, NewCLError(ErrOwnerNotFound, "Failed to query resource owner", err)
	}
	return m.OrgID == result.Owner, nil
}

// CanManageOrgMembers checks if the user can manage members of the current Org.
func (m *MemberShip) CanManageOrgMembers() bool {
	return m.IsSystemAdmin() || m.EffectiveOrgRole() >= model.OrgAdmin
}

// CanManageTargetOrg checks if the user can manage members of a specific Org.
func (m *MemberShip) CanManageTargetOrg(orgID int64) bool {
	if m.IsSystemAdmin() {
		return true
	}
	return m.CanManageOrgMembers() && m.OrgID == orgID
}

// CheckUser checks if the target user is the current user or if current user is SystemAdmin.
func (m *MemberShip) CheckUser(id int64) (bool, error) {
	if m.UserID == id || m.IsSystemAdmin() {
		return true, nil
	}
	return false, nil
}

func (m *MemberShip) SetContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, "membership", m)
}

func GetMemberShip(ctx context.Context) *MemberShip {
	m := ctx.Value("membership")
	if m != nil {
		return m.(*MemberShip)
	}
	return &MemberShip{}
}

// GetDBMemberShip is deprecated. MemberShip is now constructed from X-* headers
// set by Middle Proxy in authorize.go. Kept as a no-op stub for compile compatibility
// during migration. Will be removed after migration is complete.
func GetDBMemberShip(userID, orgID int64) (m *MemberShip, err error) {
	logger.Error("GetDBMemberShip called — this function is deprecated. MemberShip should come from X-* headers.")
	return nil, NewCLError(ErrUserNotFound, "GetDBMemberShip is deprecated", nil)
}
