package apis

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"cpgateway-go/src/common"
	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
)

func findUserOr404(c *gin.Context, uuid, detail string) (*model.User, bool) {
	var user model.User
	if err := dbs.DBContext(c.Request.Context()).Where("uuid = ?", uuid).First(&user).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, detail)
		return nil, false
	}
	return &user, true
}

// GET /users?is_active= (superuser)
func ListUsers(c *gin.Context) {
	q := dbs.DBContext(c.Request.Context()).Order("id ASC")
	if v, ok := c.GetQuery("is_active"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			common.AbortValidation(c, "query", errors.New("is_active must be a boolean"))
			return
		}
		q = q.Where("is_active = ?", b)
	}
	var users []model.User
	q.Find(&users)
	out := make([]userOut, 0, len(users))
	for i := range users {
		out = append(out, toUserOut(&users[i]))
	}
	c.JSON(http.StatusOK, out)
}

// GET /users/:uuid (superuser)
func GetUser(c *gin.Context) {
	uuid := c.Param("uuid")
	user, ok := findUserOr404(c, uuid, fmt.Sprintf("User with UUID %s not found", uuid))
	if !ok {
		return
	}
	c.JSON(http.StatusOK, toUserOut(user))
}

// PUT /users/:uuid/enable (superuser) — Active if the user has memberships, Dormant otherwise.
func EnableUser(c *gin.Context) {
	user, ok := findUserOr404(c, c.Param("uuid"), "User not found")
	if !ok {
		return
	}
	db := dbs.DBContext(c.Request.Context())
	var count int64
	db.Model(&model.Member{}).Scopes(model.ActiveMembers).Where("user_id = ?", user.ID).Count(&count)
	status := model.UserDormant
	if count > 0 {
		status = model.UserActive
	}
	db.Model(user).Updates(map[string]interface{}{"status": status, "is_active": true})
	c.JSON(http.StatusOK, gin.H{"status": "ok", "new_status": status.Name()})
}

// PUT /users/:uuid/disable (superuser)
func DisableUser(c *gin.Context) {
	user, ok := findUserOr404(c, c.Param("uuid"), "User not found")
	if !ok {
		return
	}
	if user.ID == currentUser(c).ID {
		common.AbortWithDetail(c, http.StatusBadRequest, "Cannot disable yourself")
		return
	}
	dbs.DBContext(c.Request.Context()).Model(user).Update("status", model.UserDisabled)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// PUT /users/:uuid/demote (superuser)
func DemoteUser(c *gin.Context) {
	user, ok := findUserOr404(c, c.Param("uuid"), "User not found")
	if !ok {
		return
	}
	if user.ID == currentUser(c).ID {
		common.AbortWithDetail(c, http.StatusBadRequest, "Cannot demote yourself")
		return
	}
	if user.SystemRole != model.SystemAdmin {
		common.AbortWithDetail(c, http.StatusBadRequest, "User is not a SystemAdmin")
		return
	}
	db := dbs.DBContext(c.Request.Context())
	var admins int64
	db.Model(&model.User{}).Where("system_role = ?", model.SystemAdmin).Count(&admins)
	if admins <= 1 {
		common.AbortWithDetail(c, http.StatusBadRequest, "Cannot demote the last SystemAdmin")
		return
	}
	db.Model(user).Updates(map[string]interface{}{"system_role": model.SystemUser, "is_superuser": false})
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// DELETE /users/:uuid (superuser) — soft-deletes memberships, disables the user and
// mangles email/username to release the unique constraints.
func DeleteUser(c *gin.Context) {
	user, ok := findUserOr404(c, c.Param("uuid"), "User not found")
	if !ok {
		return
	}
	if user.ID == currentUser(c).ID {
		common.AbortWithDetail(c, http.StatusBadRequest, "Cannot delete yourself")
		return
	}

	db := dbs.DBContext(c.Request.Context())
	var owned []model.Organization
	db.Where("owner_user_id = ?", user.ID).Find(&owned)
	for _, org := range owned {
		var count int64
		db.Model(&model.Member{}).Scopes(model.ActiveMembers).Where("org_id = ?", org.ID).Count(&count)
		if count > 1 {
			common.AbortWithDetail(c, http.StatusBadRequest,
				fmt.Sprintf("User is owner of Org '%s' which has other members. Transfer ownership first.", org.Name))
			return
		}
	}

	ts := time.Now().Unix()
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", user.ID).Delete(&model.Member{}).Error; err != nil {
			return err
		}
		return tx.Model(user).Updates(map[string]interface{}{
			"is_active": false,
			"status":    model.UserDisabled,
			// 用户名保持原值：它全局唯一且不可复用，注销后不允许被他人重新注册。
			// 邮箱仍改名释放，便于本人日后用同一邮箱重新注册
			"email": fmt.Sprintf("del%d+%s", ts, user.Email),
		}).Error
	})
	if err != nil {
		common.AbortWithDetail(c, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// PATCH /users/:uuid/profile — users update themselves; SystemAdmin updates anyone and sets remark.
func UpdateProfile(c *gin.Context) {
	var in struct {
		FirstName *string `json:"first_name"`
		LastName  *string `json:"last_name"`
		Language  *string `json:"language"`
		Remark    *string `json:"remark"`
	}
	if !bindJSON(c, &in) {
		return
	}
	user, ok := findUserOr404(c, c.Param("uuid"), "User not found")
	if !ok {
		return
	}
	me := currentUser(c)
	isSystemAdmin := me.SystemRole == model.SystemAdmin
	if user.ID != me.ID && !isSystemAdmin {
		common.AbortWithDetail(c, http.StatusForbidden, "Can only update own profile")
		return
	}
	if in.Remark != nil && !isSystemAdmin {
		common.AbortWithDetail(c, http.StatusForbidden, "Only SystemAdmin can modify remark")
		return
	}

	updates := map[string]interface{}{}
	for field, v := range map[string]*string{
		"first_name": in.FirstName, "last_name": in.LastName, "language": in.Language, "remark": in.Remark,
	} {
		if v != nil {
			updates[field] = *v
		}
	}
	if len(updates) > 0 {
		dbs.DBContext(c.Request.Context()).Model(user).Updates(updates)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// PUT /users/:uuid/password — users must confirm the old password; SystemAdmin resets others without it.
func ChangePassword(c *gin.Context) {
	var in struct {
		OldPassword *string `json:"old_password" binding:"required"`
		NewPassword string  `json:"new_password" binding:"required,min=8,max=64"`
	}
	if !bindJSON(c, &in) {
		return
	}
	user, ok := findUserOr404(c, c.Param("uuid"), "User not found")
	if !ok {
		return
	}
	me := currentUser(c)
	isSystemAdmin := me.SystemRole == model.SystemAdmin
	if user.ID != me.ID && !isSystemAdmin {
		common.AbortWithDetail(c, http.StatusForbidden, "Can only change own password")
		return
	}
	if !(isSystemAdmin && me.ID != user.ID) && !common.VerifyPassword(*in.OldPassword, user.HashedPassword) {
		common.AbortWithDetail(c, http.StatusBadRequest, "Incorrect old password")
		return
	}
	hashed, err := common.HashPassword(in.NewPassword)
	if err != nil {
		common.AbortWithDetail(c, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	dbs.DBContext(c.Request.Context()).Model(user).Update("hashed_password", hashed)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Role names sent by the web "edit user" dialog, mapped to org roles.
var editUserRoles = map[string]model.OrgRole{
	"admin":  model.OrgRoleAdmin,
	"writer": model.OrgRoleWriter,
	"reader": model.OrgRoleReader,
	"member": model.OrgRoleReader,
	"user":   model.OrgRoleReader,
}

// UpdateUser handles PUT /users/:uuid used by the web "edit user" dialog.
// Username and email are global account fields that only SystemAdmin may change; unchanged values
// are ignored. Role is the user's role in the caller's current organization and may be changed by
// SystemAdmin or an admin of that organization ("owner" leaves the role untouched).
func UpdateUser(c *gin.Context) {
	var in struct {
		Username *string `json:"username"`
		Email    *string `json:"email" binding:"omitempty,email"`
		Role     *string `json:"role"`
	}
	if !bindJSON(c, &in) {
		return
	}
	user, ok := findUserOr404(c, c.Param("uuid"), "User not found")
	if !ok {
		return
	}
	me := currentUser(c)
	db := dbs.DBContext(c.Request.Context())

	var accountUpdates [][2]string
	// 用户名创建后不可更改：它是账号的永久标识，审计记录与历史数据都以它指代这个人，
	// 改名会让既有记录指向错误的对象
	if in.Username != nil && *in.Username != user.Username {
		common.AbortWithDetail(c, http.StatusBadRequest, "Username cannot be changed")
		return
	}
	if in.Email != nil && *in.Email != user.Email {
		accountUpdates = append(accountUpdates, [2]string{"email", *in.Email})
	}
	if len(accountUpdates) > 0 && !me.IsAdmin() {
		common.AbortWithDetail(c, http.StatusForbidden, "Only SystemAdmin can modify username or email")
		return
	}
	conflictDetails := map[string]string{"username": "Username already taken", "email": "Email already registered"}
	for _, u := range accountUpdates {
		var count int64
		db.Model(&model.User{}).Where(u[0]+" = ? AND id <> ?", u[1], user.ID).Count(&count)
		if count > 0 {
			common.AbortWithDetail(c, http.StatusBadRequest, conflictDetails[u[0]])
			return
		}
	}

	var membership *model.Member
	var newRole model.OrgRole
	if in.Role != nil && *in.Role != "" && *in.Role != "owner" {
		role, valid := editUserRoles[*in.Role]
		if !valid {
			common.AbortWithDetail(c, http.StatusBadRequest, fmt.Sprintf("Invalid role: %s", *in.Role))
			return
		}
		orgUUID := currentClaims(c).OrgID
		if orgUUID == "" {
			common.AbortWithDetail(c, http.StatusBadRequest, "No active organization in token")
			return
		}
		org, found := getOrgOr404(c, orgUUID)
		if !found {
			return
		}
		if !me.IsAdmin() && !isOrgAdminMember(me.ID, org.ID) {
			common.AbortWithDetail(c, http.StatusForbidden, "Not enough permissions")
			return
		}
		if membership, found = findMemberOr404(c, user.ID, org.ID); !found {
			return
		}
		newRole = role
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if len(accountUpdates) > 0 {
			updates := map[string]interface{}{}
			for _, u := range accountUpdates {
				updates[u[0]] = u[1]
			}
			if err := tx.Model(user).Updates(updates).Error; err != nil {
				return err
			}
		}
		if membership != nil && membership.OrgRole != newRole {
			return tx.Model(membership).Update("org_role", newRole).Error
		}
		return nil
	})
	if err != nil {
		internalServerError(c, err)
		return
	}
	db.Where("id = ?", user.ID).First(user)
	c.JSON(http.StatusOK, toUserOut(user))
}
