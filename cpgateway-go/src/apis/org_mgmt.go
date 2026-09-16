package apis

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"cpgateway-go/src/common"
	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
	"cpgateway-go/src/services"
)

func getOrgOr404(c *gin.Context, uuid string) (*model.Organization, bool) {
	var org model.Organization
	if err := dbs.DBContext(c.Request.Context()).Where("uuid = ?", uuid).First(&org).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, "Organization not found")
		return nil, false
	}
	return &org, true
}

func loadUserByID(id int64) *model.User {
	var user model.User
	if dbs.DB().Where("id = ?", id).Limit(1).Find(&user).RowsAffected == 0 {
		return nil
	}
	return &user
}

func loadUsersByID(ids []int64) map[int64]*model.User {
	users := map[int64]*model.User{}
	if len(ids) == 0 {
		return users
	}
	var list []model.User
	dbs.DB().Where("id IN ?", ids).Find(&list)
	for i := range list {
		users[list[i].ID] = &list[i]
	}
	return users
}

// findActiveMember loads the formal membership (see model.ActiveMembers) of a user in an org.
func findActiveMember(userID, orgID int64) *model.Member {
	var member model.Member
	if dbs.DB().Scopes(model.ActiveMembers).Where("user_id = ? AND org_id = ?", userID, orgID).
		Limit(1).Find(&member).RowsAffected == 0 {
		return nil
	}
	return &member
}

func isMember(userID, orgID int64) bool {
	return findActiveMember(userID, orgID) != nil
}

func isOrgAdminMember(userID, orgID int64) bool {
	m := findActiveMember(userID, orgID)
	return m != nil && m.OrgRole >= model.OrgRoleAdmin
}

func queryInt(c *gin.Context, name string, def int) (int, bool) {
	v, ok := c.GetQuery(name)
	if !ok {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		common.AbortValidation(c, "query", fmt.Errorf("%s must be an integer", name))
		return 0, false
	}
	return n, true
}

func internalServerError(c *gin.Context, err error) {
	log.WithContext(c).Errorf("Request failed: %v", err)
	common.AbortWithDetail(c, http.StatusInternalServerError, "Internal Server Error")
}

// POST /orgs (superuser)
func CreateOrg(c *gin.Context) {
	if !requireSuperuser(c) {
		return
	}
	var in struct {
		Name string `json:"name" binding:"required"`
		Slug string `json:"slug" binding:"required"`
	}
	if !bindJSON(c, &in) {
		return
	}
	db := dbs.DBContext(c.Request.Context())
	var count int64
	db.Model(&model.Organization{}).Where("slug = ?", in.Slug).Count(&count)
	if count > 0 {
		common.AbortWithDetail(c, http.StatusBadRequest, fmt.Sprintf("Slug '%s' already exists", in.Slug))
		return
	}

	me := currentUser(c)
	org := model.Organization{Name: in.Name, Slug: in.Slug, OrgType: model.OrgTeam, OwnerUserID: me.ID, Status: model.OrgActive}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&org).Error; err != nil {
			return err
		}
		if err := services.InitializeOrgQuotas(tx, org.ID); err != nil {
			return err
		}
		return tx.Create(&model.Member{UserID: me.ID, OrgID: org.ID, OrgRole: model.OrgRoleAdmin}).Error
	})
	if err != nil {
		internalServerError(c, err)
		return
	}

	go services.SyncOrgToAllRegions(context.WithoutCancel(c.Request.Context()), org.UUID, org.Name, org.Slug)
	c.JSON(http.StatusCreated, toOrgOut(&org, me))
}

// GET /orgs?skip=&limit= — admins list all orgs, other users their own.
func ListOrgs(c *gin.Context) {
	skip, ok := queryInt(c, "skip", 0)
	if !ok {
		return
	}
	limit, ok := queryInt(c, "limit", 100)
	if !ok {
		return
	}
	me := currentUser(c)
	q := dbs.DBContext(c.Request.Context()).Model(&model.Organization{})
	if !me.IsAdmin() {
		q = q.Select("organizations.*").
			Joins("JOIN members ON members.org_id = organizations.id AND members.deleted_at IS NULL").
			Where("members.user_id = ?", me.ID).
			Scopes(model.ActiveMembers)
	}
	var orgs []model.Organization
	q.Order("organizations.id ASC").Offset(skip).Limit(limit).Find(&orgs)

	ownerIDs := make([]int64, 0, len(orgs))
	for _, o := range orgs {
		ownerIDs = append(ownerIDs, o.OwnerUserID)
	}
	owners := loadUsersByID(ownerIDs)
	out := make([]orgOut, 0, len(orgs))
	for i := range orgs {
		out = append(out, toOrgOut(&orgs[i], owners[orgs[i].OwnerUserID]))
	}
	c.JSON(http.StatusOK, out)
}

// GET /orgs/:uuid
func GetOrg(c *gin.Context) {
	org, ok := getOrgOr404(c, c.Param("uuid"))
	if !ok {
		return
	}
	me := currentUser(c)
	if !me.IsAdmin() && !isMember(me.ID, org.ID) {
		common.AbortWithDetail(c, http.StatusForbidden, "Not a member of this organization")
		return
	}
	var memberCount int64
	dbs.DBContext(c.Request.Context()).Model(&model.Member{}).Scopes(model.ActiveMembers).Where("org_id = ?", org.ID).Count(&memberCount)
	c.JSON(http.StatusOK, orgDetailOut{toOrgOut(org, loadUserByID(org.OwnerUserID)), memberCount})
}

// PATCH /orgs/:uuid — SystemAdmin, org owner or org admin.
func UpdateOrg(c *gin.Context) {
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if !bindJSON(c, &in) {
		return
	}
	org, ok := getOrgOr404(c, c.Param("uuid"))
	if !ok {
		return
	}
	me := currentUser(c)
	if !me.IsAdmin() && org.OwnerUserID != me.ID && !isOrgAdminMember(me.ID, org.ID) {
		common.AbortWithDetail(c, http.StatusForbidden, "Not enough permissions")
		return
	}
	updates := map[string]interface{}{}
	if in.Name != nil {
		updates["name"] = *in.Name
	}
	if in.Description != nil {
		updates["description"] = *in.Description
	}
	db := dbs.DBContext(c.Request.Context())
	if len(updates) > 0 {
		if err := db.Model(org).Updates(updates).Error; err != nil {
			internalServerError(c, err)
			return
		}
	}
	db.Where("id = ?", org.ID).First(org)
	c.JSON(http.StatusOK, toOrgOut(org, loadUserByID(org.OwnerUserID)))
}

// DELETE /orgs/:uuid (superuser) — soft delete, slug mangled to free it.
func DeleteOrg(c *gin.Context) {
	if !requireSuperuser(c) {
		return
	}
	org, ok := getOrgOr404(c, c.Param("uuid"))
	if !ok {
		return
	}
	if org.OrgType == model.OrgSystem {
		common.AbortWithDetail(c, http.StatusBadRequest, "Cannot delete system organization")
		return
	}
	now := time.Now().UTC()
	if err := dbs.DBContext(c.Request.Context()).Model(org).Updates(map[string]interface{}{
		"slug":       fmt.Sprintf("%s_del%d", org.Slug, now.Unix()),
		"deleted_at": now,
	}).Error; err != nil {
		internalServerError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// PATCH /orgs/:uuid/status (superuser) — PENDING cannot be set manually.
func UpdateOrgStatus(c *gin.Context) {
	if !requireSuperuser(c) {
		return
	}
	var in struct {
		Status *int `json:"status" binding:"required"`
	}
	if !bindJSON(c, &in) {
		return
	}
	status := *in.Status
	if status == int(model.OrgPending) {
		common.AbortWithDetail(c, http.StatusBadRequest, "Cannot manually set org status to PENDING")
		return
	}
	if status < int(model.OrgPending) || status > int(model.OrgDisabled) {
		common.AbortWithDetail(c, http.StatusBadRequest, fmt.Sprintf("Invalid status value: %d", status))
		return
	}
	org, ok := getOrgOr404(c, c.Param("uuid"))
	if !ok {
		return
	}
	if org.OrgType == model.OrgSystem {
		common.AbortWithDetail(c, http.StatusBadRequest, "Cannot change status of system organization")
		return
	}
	if err := dbs.DBContext(c.Request.Context()).Model(org).Update("status", status).Error; err != nil {
		internalServerError(c, err)
		return
	}
	org.Status = model.OrgStatus(status)
	c.JSON(http.StatusOK, toOrgOut(org, loadUserByID(org.OwnerUserID)))
}

// POST /orgs/:uuid/transfer-owner (superuser)
func TransferOwner(c *gin.Context) {
	if !requireSuperuser(c) {
		return
	}
	var in struct {
		NewOwnerUUID string `json:"new_owner_uuid" binding:"required"`
	}
	if !bindJSON(c, &in) {
		return
	}
	org, ok := getOrgOr404(c, c.Param("uuid"))
	if !ok {
		return
	}
	newOwner, ok := findUserOr404(c, in.NewOwnerUUID, "User not found")
	if !ok {
		return
	}
	if !isMember(newOwner.ID, org.ID) {
		common.AbortWithDetail(c, http.StatusBadRequest, "New owner must be a member of the organization")
		return
	}
	if err := dbs.DBContext(c.Request.Context()).Model(org).Update("owner_user_id", newOwner.ID).Error; err != nil {
		internalServerError(c, err)
		return
	}
	org.OwnerUserID = newOwner.ID
	c.JSON(http.StatusOK, toOrgOut(org, newOwner))
}

// --- Members ---

// GET /orgs/:uuid/members — includes pending invitations. SystemAdmin or formal members only.
func ListMembers(c *gin.Context) {
	org, ok := getOrgOr404(c, c.Param("uuid"))
	if !ok {
		return
	}
	if me := currentUser(c); !me.IsAdmin() && !isMember(me.ID, org.ID) {
		common.AbortWithDetail(c, http.StatusForbidden, "Not a member of this organization")
		return
	}
	var members []model.Member
	dbs.DBContext(c.Request.Context()).Where("org_id = ?", org.ID).Order("id ASC").Find(&members)

	userIDs := make([]int64, 0, len(members))
	for _, m := range members {
		userIDs = append(userIDs, m.UserID)
	}
	users := loadUsersByID(userIDs)

	out := make([]memberOut, 0, len(members))
	for i := range members {
		m := &members[i]
		u, found := users[m.UserID]
		if !found {
			continue
		}
		var invitationStatus *int
		if m.InvitationStatus != nil {
			s := int(*m.InvitationStatus)
			invitationStatus = &s
		}
		out = append(out, memberOut{
			UUID: m.UUID, UserUUID: u.UUID, OrgUUID: org.UUID, OrgRole: int(m.OrgRole),
			UserEmail: &u.Email, Username: u.Username, IsOwner: m.UserID == org.OwnerUserID, IsSuperuser: u.IsSuperuser,
			InvitationStatus: invitationStatus, CreatedAt: m.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, out)
}

// POST /orgs/:uuid/members — direct add, SystemAdmin only (others use invitations).
func AddMember(c *gin.Context) {
	var in struct {
		UserUUID string `json:"user_uuid" binding:"required"`
		OrgRole  *int   `json:"org_role"`
	}
	if !bindJSON(c, &in) {
		return
	}
	org, ok := getOrgOr404(c, c.Param("uuid"))
	if !ok {
		return
	}
	if !currentUser(c).IsAdmin() {
		common.AbortWithDetail(c, http.StatusForbidden, "Direct member addition is restricted to system admins. Use invitations instead.")
		return
	}
	target, ok := findUserOr404(c, in.UserUUID, "User not found")
	if !ok {
		return
	}
	if isMember(target.ID, org.ID) {
		common.AbortWithDetail(c, http.StatusBadRequest, "User is already a member of this organization")
		return
	}
	role := model.OrgRoleReader
	if in.OrgRole != nil {
		role = model.OrgRole(*in.OrgRole)
	}
	member := model.Member{UserID: target.ID, OrgID: org.ID, OrgRole: role}
	err := dbs.DBContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// A direct add supersedes unaccepted invitations, which still hold the (user_id, org_id) active unique slot.
		if err := tx.Model(&model.Member{}).
			Where("user_id = ? AND org_id = ? AND invitation_status IN ?", target.ID, org.ID,
				[]model.InvitationStatus{model.InvitationPending, model.InvitationExpired, model.InvitationCancelled}).
			Updates(map[string]interface{}{"invitation_status": model.InvitationCancelled, "deleted_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		return tx.Create(&member).Error
	})
	if err != nil {
		internalServerError(c, err)
		return
	}
	c.JSON(http.StatusCreated, memberOut{
		UUID: member.UUID, UserUUID: target.UUID, OrgUUID: org.UUID, OrgRole: int(member.OrgRole),
		UserEmail: &target.Email, Username: target.Username, CreatedAt: member.CreatedAt,
	})
}

// memberActionContext resolves org, target user and the org-admin permission shared by
// member update/remove.
func memberActionContext(c *gin.Context) (*model.Organization, *model.User, *model.Member, bool) {
	org, ok := getOrgOr404(c, c.Param("uuid"))
	if !ok {
		return nil, nil, nil, false
	}
	target, ok := findUserOr404(c, c.Param("user_uuid"), "User not found")
	if !ok {
		return nil, nil, nil, false
	}
	if me := currentUser(c); !me.IsAdmin() && !isOrgAdminMember(me.ID, org.ID) {
		common.AbortWithDetail(c, http.StatusForbidden, "Not enough permissions")
		return nil, nil, nil, false
	}
	return org, target, nil, true
}

// findMemberOr404 loads a formal membership; pending/expired invitations are managed via the invitation endpoints.
func findMemberOr404(c *gin.Context, userID, orgID int64) (*model.Member, bool) {
	var member model.Member
	if err := dbs.DBContext(c.Request.Context()).Scopes(model.ActiveMembers).
		Where("user_id = ? AND org_id = ?", userID, orgID).First(&member).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, "Member not found")
		return nil, false
	}
	return &member, true
}

// PATCH /orgs/:uuid/members/:user_uuid
func UpdateMemberRole(c *gin.Context) {
	var in struct {
		OrgRole *int `json:"org_role" binding:"required"`
	}
	if !bindJSON(c, &in) {
		return
	}
	org, target, _, ok := memberActionContext(c)
	if !ok {
		return
	}
	member, ok := findMemberOr404(c, target.ID, org.ID)
	if !ok {
		return
	}
	if err := dbs.DBContext(c.Request.Context()).Model(member).Update("org_role", *in.OrgRole).Error; err != nil {
		internalServerError(c, err)
		return
	}
	c.JSON(http.StatusOK, memberOut{
		UUID: member.UUID, UserUUID: target.UUID, OrgUUID: org.UUID, OrgRole: *in.OrgRole,
		UserEmail: &target.Email, Username: target.Username, CreatedAt: member.CreatedAt,
	})
}

// DELETE /orgs/:uuid/members/:user_uuid
func RemoveMember(c *gin.Context) {
	org, target, _, ok := memberActionContext(c)
	if !ok {
		return
	}
	if org.OwnerUserID == target.ID {
		common.AbortWithDetail(c, http.StatusBadRequest, "Cannot remove the organization owner. Transfer ownership first.")
		return
	}
	member, ok := findMemberOr404(c, target.ID, org.ID)
	if !ok {
		return
	}
	if err := dbs.DBContext(c.Request.Context()).Delete(member).Error; err != nil {
		internalServerError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// --- Invitations ---

func canManageInvitations(me *model.User, orgID int64) bool {
	return me.IsAdmin() || isOrgAdminMember(me.ID, orgID)
}

// POST /orgs/:uuid/invitations
func CreateInvitation(c *gin.Context) {
	var in struct {
		Email       string `json:"email" binding:"required,email"`
		OrgRole     *int   `json:"org_role"`
		IsSuperuser bool   `json:"is_superuser"`
	}
	if !bindJSON(c, &in) {
		return
	}
	org, ok := getOrgOr404(c, c.Param("uuid"))
	if !ok {
		return
	}
	me := currentUser(c)
	if !canManageInvitations(me, org.ID) {
		common.AbortWithDetail(c, http.StatusForbidden, "Not enough permissions to invite members")
		return
	}
	if in.IsSuperuser && org.OrgType != model.OrgSystem {
		common.AbortWithDetail(c, http.StatusBadRequest, "Only the system organization can invite superusers")
		return
	}
	role := model.OrgRoleReader
	if in.OrgRole != nil {
		role = model.OrgRole(*in.OrgRole)
	}
	if in.IsSuperuser {
		role = model.OrgRoleAdmin
	}

	member, herr := services.CreateInvitation(c.Request.Context(), in.Email, org, role, me, in.IsSuperuser)
	if herr != nil {
		common.AbortWithError(c, herr)
		return
	}
	c.JSON(http.StatusCreated, invitationOut{
		UUID: member.UUID, Email: in.Email, OrgUUID: org.UUID, OrgName: org.Name,
		OrgRole: int(member.OrgRole), Status: invitationStatusValue(member), InviterEmail: me.Email,
		CreatedAt: member.CreatedAt, ExpiresAt: member.InvitationExpiresAt,
	})
}

// GET /orgs/:uuid/invitations — pending invitations, newest first. SystemAdmin or org ADMIN members only.
func ListInvitations(c *gin.Context) {
	org, ok := getOrgOr404(c, c.Param("uuid"))
	if !ok {
		return
	}
	if !canManageInvitations(currentUser(c), org.ID) {
		common.AbortWithDetail(c, http.StatusForbidden, "Not enough permissions")
		return
	}
	var members []model.Member
	dbs.DBContext(c.Request.Context()).Where("org_id = ? AND invitation_status = ?", org.ID, model.InvitationPending).
		Order("created_at DESC").Find(&members)

	ids := make([]int64, 0, len(members)*2)
	for _, m := range members {
		ids = append(ids, m.UserID)
		if m.InvitedBy != nil {
			ids = append(ids, *m.InvitedBy)
		}
	}
	users := loadUsersByID(ids)

	out := make([]invitationOut, 0, len(members))
	for i := range members {
		m := &members[i]
		invited, found := users[m.UserID]
		if !found {
			continue
		}
		inviterEmail := ""
		if m.InvitedBy != nil {
			if inviter, ok := users[*m.InvitedBy]; ok {
				inviterEmail = inviter.Email
			}
		}
		out = append(out, invitationOut{
			UUID: m.UUID, Email: invited.Email, OrgUUID: org.UUID, OrgName: org.Name,
			OrgRole: int(m.OrgRole), Status: invitationStatusValue(m), InviterEmail: inviterEmail,
			CreatedAt: m.CreatedAt, ExpiresAt: m.InvitationExpiresAt,
		})
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /orgs/:uuid/invitations/:invitation_uuid — also removes an orphaned INVITED placeholder user.
func CancelInvitation(c *gin.Context) {
	org, ok := getOrgOr404(c, c.Param("uuid"))
	if !ok {
		return
	}
	if !canManageInvitations(currentUser(c), org.ID) {
		common.AbortWithDetail(c, http.StatusForbidden, "Not enough permissions")
		return
	}

	db := dbs.DBContext(c.Request.Context())
	var member model.Member
	if err := db.Where("uuid = ? AND org_id = ? AND invitation_status = ?",
		c.Param("invitation_uuid"), org.ID, model.InvitationPending).First(&member).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, "Invitation not found")
		return
	}
	if err := db.Model(&member).Updates(map[string]interface{}{
		"invitation_status": model.InvitationCancelled,
		"deleted_at":        time.Now().UTC(),
	}).Error; err != nil {
		internalServerError(c, err)
		return
	}

	user := loadUserByID(member.UserID)
	if user != nil && user.Status == model.UserInvited {
		var others int64
		db.Model(&model.Member{}).Where("user_id = ?", user.ID).Count(&others)
		if others == 0 {
			err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Unscoped().Where("user_id = ?", user.ID).Delete(&model.Member{}).Error; err != nil {
					return err
				}
				return tx.Unscoped().Delete(user).Error
			})
			if err != nil {
				log.WithContext(c).Errorf("Failed to clean up placeholder user %s: %v", user.Email, err)
			}
		}
	}
	c.Status(http.StatusNoContent)
}
