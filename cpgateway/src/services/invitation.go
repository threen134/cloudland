package services

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"gorm.io/gorm"

	"cpgateway/src/common"
	"cpgateway/src/dbs"
	"cpgateway/src/model"
)

func activationExpireHours() int {
	if h := viper.GetInt("auth.activation_token_expire_hours"); h > 0 {
		return h
	}
	return 24
}

func badRequest(detail string) *common.HTTPError {
	return common.NewHTTPError(http.StatusBadRequest, detail)
}

// getOrCreateInvitedUser returns the user with this email, creating an INVITED placeholder if absent.
func getOrCreateInvitedUser(db *gorm.DB, email string) (*model.User, error) {
	var user model.User
	if err := db.Where("email = ?", email).First(&user).Error; err == nil {
		return &user, nil
	}
	hashed, err := common.HashPassword(strings.ReplaceAll(uuid.New().String(), "-", ""))
	if err != nil {
		return nil, err
	}
	user = model.User{
		Email:          email,
		Username:       "_inv_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12],
		HashedPassword: hashed,
		Language:       "en",
		IsActive:       false,
		Status:         model.UserInvited,
	}
	if err := db.Create(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// CreateInvitation creates a pending invitation member record and notifies the invitee.
// Existing unaccepted invitations for the same user+org are cancelled first.
func CreateInvitation(ctx context.Context, email string, org *model.Organization, orgRole model.OrgRole, inviter *model.User, isSuperuser bool) (*model.Member, *common.HTTPError) {
	db := dbs.DBContext(ctx)
	user, err := getOrCreateInvitedUser(db, email)
	if err != nil {
		return nil, common.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
	}

	var active int64
	db.Model(&model.Member{}).Scopes(model.ActiveMembers).
		Where("user_id = ? AND org_id = ?", user.ID, org.ID).
		Count(&active)
	if active > 0 {
		return nil, badRequest("User is already a member of this organization")
	}

	token, err := common.CreateInvitationToken(email, org.UUID)
	if err != nil {
		return nil, common.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
	}

	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(activationExpireHours()) * time.Hour)
	pending := model.InvitationPending
	grant := 0
	if isSuperuser {
		grant = 1
	}
	member := model.Member{
		UserID:              user.ID,
		OrgID:               org.ID,
		OrgRole:             orgRole,
		InvitationToken:     &token,
		InvitationStatus:    &pending,
		InvitedBy:           &inviter.ID,
		InvitationExpiresAt: &expiresAt,
		GrantSuperuser:      grant,
	}

	txErr := db.Transaction(func(tx *gorm.DB) error {
		// Expired records are included too: they still hold the (user_id, org_id) active unique slot.
		if err := tx.Model(&model.Member{}).
			Where("user_id = ? AND org_id = ? AND invitation_status IN ?", user.ID, org.ID,
				[]model.InvitationStatus{model.InvitationPending, model.InvitationExpired, model.InvitationCancelled}).
			Updates(map[string]interface{}{"invitation_status": model.InvitationCancelled, "deleted_at": now}).Error; err != nil {
			return err
		}
		return tx.Create(&member).Error
	})
	if txErr != nil {
		log.WithContext(ctx).Errorf("Failed to create invitation for %s: %v", email, txErr)
		return nil, common.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
	}

	isExisting := user.IsActive && user.Status == model.UserActive
	inviterName := inviter.Username
	if inviterName == "" {
		inviterName = inviter.Email
	}
	go SendInvitationNotification(context.WithoutCancel(ctx), email, org.Name, inviterName, token, isExisting)
	return &member, nil
}

// findPendingInvitation validates the token and loads the pending, unexpired member record.
func findPendingInvitation(db *gorm.DB, token string) (*model.Member, *common.HTTPError) {
	if _, _, err := common.VerifyInvitationToken(token); err != nil {
		return nil, badRequest("Invalid or expired invitation token")
	}
	var member model.Member
	if err := db.Where("invitation_token = ? AND invitation_status = ?", token, model.InvitationPending).
		First(&member).Error; err != nil {
		return nil, badRequest("Invitation not found or already used")
	}
	if member.InvitationExpiresAt != nil && member.InvitationExpiresAt.Before(time.Now()) {
		db.Model(&member).Update("invitation_status", model.InvitationExpired)
		return nil, badRequest("Invitation has expired")
	}
	return &member, nil
}

// GetInvitationInfo returns the invitation details shown on the accept page.
func GetInvitationInfo(token string) (map[string]interface{}, *common.HTTPError) {
	db := dbs.DB()
	member, herr := findPendingInvitation(db, token)
	if herr != nil {
		return nil, herr
	}

	var org model.Organization
	db.Unscoped().Where("id = ?", member.OrgID).Limit(1).Find(&org)
	var inviter model.User
	if member.InvitedBy != nil {
		db.Where("id = ?", *member.InvitedBy).Limit(1).Find(&inviter)
	}
	var user model.User
	db.Where("id = ?", member.UserID).Limit(1).Find(&user)

	return map[string]interface{}{
		"email":            user.Email,
		"org_name":         org.Name,
		"org_role":         member.OrgRole,
		"inviter_email":    inviter.Email,
		"is_existing_user": user.ID != 0 && user.IsActive && user.Status == model.UserActive,
		"expires_at":       member.InvitationExpiresAt,
	}, nil
}

// AcceptInvitation activates the membership; new (INVITED) or inactive users must set username/password.
func AcceptInvitation(token string, username, password *string) (map[string]interface{}, *common.HTTPError) {
	db := dbs.DB()
	member, herr := findPendingInvitation(db, token)
	if herr != nil {
		return nil, herr
	}

	var org model.Organization
	if err := db.Where("id = ?", member.OrgID).First(&org).Error; err != nil {
		return nil, badRequest("Organization no longer exists")
	}
	var user model.User
	if err := db.Where("id = ?", member.UserID).First(&user).Error; err != nil {
		return nil, badRequest("User not found")
	}

	name, pass := "", ""
	if username != nil {
		name = *username
	}
	if password != nil {
		pass = *password
	}

	isNewUser := user.Status == model.UserInvited
	updates := map[string]interface{}{}
	if isNewUser || !user.IsActive {
		if name == "" || pass == "" {
			return nil, badRequest("Username and password are required for new users")
		}
		var taken int64
		// Unscoped：用户名不可复用，已注销用户仍占着这个名字。
		// 派生独立 Session，避免 Unscoped 污染后续查询（GORM 链式调用复用 Statement）
		db.Session(&gorm.Session{}).Unscoped().Model(&model.User{}).Where("username = ? AND id <> ?", name, user.ID).Count(&taken)
		if taken > 0 {
			return nil, badRequest("Username already taken")
		}
		hashed, err := common.HashPassword(pass)
		if err != nil {
			return nil, common.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
		}
		updates["username"] = name
		updates["hashed_password"] = hashed
		updates["is_active"] = true
		updates["status"] = model.UserActive
	}
	if member.GrantSuperuser != 0 {
		updates["is_superuser"] = true
		updates["system_role"] = model.SystemAdmin
	}

	txErr := db.Transaction(func(tx *gorm.DB) error {
		if len(updates) > 0 {
			if err := tx.Model(&user).Updates(updates).Error; err != nil {
				return err
			}
		}
		return tx.Model(member).Update("invitation_status", model.InvitationAccepted).Error
	})
	if txErr != nil {
		log.Errorf("Failed to accept invitation: %v", txErr)
		return nil, common.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
	}

	log.Infof("Invitation accepted: %s joined org %s as role %d", user.Email, org.Name, member.OrgRole)
	return map[string]interface{}{
		"message":     "Invitation accepted",
		"org_uuid":    org.UUID,
		"org_name":    org.Name,
		"is_new_user": isNewUser,
	}, nil
}
