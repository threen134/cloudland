/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"context"

	"api/src/model"

	"gorm.io/gorm"
)

type MemberShip struct {
	UserID int64
	// UserName 由 cpgateway 经 X-User-Name 传入。用户名不可更改、不可复用，
	// 适合作为审计记录里的操作者标识；本服务没有用户表，无法由 UserID 反查。
	// 不再接收邮箱：它可改、注销后可复用，作为标识不可靠，本服务也无处使用
	UserName string
	// UserUUID 由 cpgateway 经 X-User-UUID 传入，是账号的全局唯一标识。UserID 是控制面的
	// 自增主键，在本服务无法解析；需要持久引用某个用户时存 UUID + 用户名快照
	UserUUID   string
	SystemRole model.SystemRole
	OrgID      int64
	OrgName    string
	OrgRole    model.OrgRole
	IsOrgOwner bool
	AllOrgs    bool
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
// SystemAdmin with AllOrgs=true: ("", nil) — global view across all orgs.
// All other users: scoped to current org.
func (m *MemberShip) GetOrgFilter() (query string, args []interface{}) {
	if m.AllOrgs && m.IsSystemAdmin() {
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
	res := db.Table(table).Select("owner").Where("id = ?", id).Scan(&result)
	err := res.Error
	if err == nil && res.RowsAffected == 0 {
		// GORM v2 的 Scan 查不到记录时不返回 ErrRecordNotFound，这里保持“未找到即报错”
		err = gorm.ErrRecordNotFound
	}
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
// set by CPGateway Proxy in authorize.go. Kept as a no-op stub for compile compatibility
// during migration. Will be removed after migration is complete.
func GetDBMemberShip(userID, orgID int64) (m *MemberShip, err error) {
	logger.Error("GetDBMemberShip called — this function is deprecated. MemberShip should come from X-* headers.")
	return nil, NewCLError(ErrUserNotFound, "GetDBMemberShip is deprecated", nil)
}
