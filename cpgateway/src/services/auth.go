package services

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"cpgateway/src/common"
	"cpgateway/src/dbs"
	"cpgateway/src/model"
)

type RegisterInput struct {
	Email    string `json:"email" binding:"required,email"`
	Username string `json:"username" binding:"required"`
	Language string `json:"language"`
	Password string `json:"password" binding:"required"`
	OrgName  string `json:"org_name" binding:"required,min=2,max=128"`
	OrgSlug  string `json:"org_slug" binding:"required,min=2,max=64"`
}

// TokenWithContext mirrors the Python TokenWithContext response schema.
type TokenWithContext struct {
	AccessToken string  `json:"access_token"`
	TokenType   string  `json:"token_type"`
	ExpiresIn   int     `json:"expires_in"`
	OrgUUID     *string `json:"org_uuid"`
	OrgName     *string `json:"org_name"`
	Region      *string `json:"region"`
}

func AccessTokenExpireMinutes() int {
	if v := viper.GetInt("auth.access_token_expire_minutes"); v > 0 {
		return v
	}
	return 120
}

func newTokenResponse(token string, orgUUID, orgName *string, region string) *TokenWithContext {
	return &TokenWithContext{
		AccessToken: token,
		TokenType:   "bearer",
		ExpiresIn:   AccessTokenExpireMinutes() * 60,
		OrgUUID:     orgUUID,
		OrgName:     orgName,
		Region:      &region,
	}
}

func unauthorized(detail string) *common.HTTPError {
	e := common.NewHTTPError(http.StatusUnauthorized, detail)
	e.Headers = map[string]string{"WWW-Authenticate": "Bearer"}
	return e
}

func forbidden(detail string) *common.HTTPError {
	return common.NewHTTPError(http.StatusForbidden, detail)
}

func notFound(detail string) *common.HTTPError {
	return common.NewHTTPError(http.StatusNotFound, detail)
}

func isUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate")
}

// revokeClaims records the token's jti; an already revoked jti is ignored.
func revokeClaims(tx *gorm.DB, claims *common.AccessTokenClaims) error {
	if claims.ID == "" {
		return nil
	}
	expiresAt := time.Unix(0, 0).UTC()
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&model.TokenRevocation{JTI: claims.ID, ExpiresAt: expiresAt}).Error
}

// Register creates (or reuses an inactive) user plus a PENDING org, quotas and ADMIN membership atomically.
func Register(ctx context.Context, in *RegisterInput) (*model.User, *common.HTTPError) {
	db := dbs.DBContext(ctx)
	language := in.Language
	if language == "" {
		language = "en"
	}

	var user model.User
	herr := func() *common.HTTPError {
		tx := db.Begin()
		fail := func(err error) *common.HTTPError {
			tx.Rollback()
			if isUniqueViolation(err) {
				return badRequest("Email or username already exists")
			}
			log.WithContext(ctx).Errorf("Registration error: %v", err)
			return common.NewHTTPError(http.StatusInternalServerError, "Internal server error during registration")
		}

		// Email and username are looked up separately: only the unactivated account owning this email
		// may be reused, so nobody can take over another person's account (or its pending org) by
		// registering with that account's username.
		var byEmail, byName model.User
		res := tx.Where("email = ?", in.Email).Limit(1).Find(&byEmail)
		if res.Error != nil {
			return fail(res.Error)
		}
		emailHit := res.RowsAffected > 0
		// Unscoped：用户名不可复用，已注销用户仍占着这个名字，必须把软删除的行也算进来，
		// 否则这里放行、随后数据库唯一索引再报冲突，用户看到的是 500 而不是明确提示
		// Session(&gorm.Session{})：从同一事务派生独立 Statement 再加 Unscoped，
		// 否则 Unscoped 会沿用到该事务后续的查询上（GORM 链式调用复用 Statement），
		// 让本该忽略软删除行的查询也看到它们
		if res = tx.Session(&gorm.Session{}).Unscoped().Where("username = ?", in.Username).Limit(1).Find(&byName); res.Error != nil {
			return fail(res.Error)
		}
		if res.RowsAffected > 0 && (!emailHit || byName.ID != byEmail.ID) {
			tx.Rollback()
			return badRequest("Username already taken")
		}
		if emailHit && byEmail.IsActive {
			tx.Rollback()
			return badRequest("Email already registered")
		}
		if emailHit {
			// The latest registration wins: pending orgs of earlier unactivated registrations would
			// otherwise keep their slugs and be activated together with the new org.
			if err := discardPendingOrgs(tx, byEmail.ID); err != nil {
				return fail(err)
			}
		}

		var slugCount int64
		if err := tx.Model(&model.Organization{}).Where("slug = ?", in.OrgSlug).Count(&slugCount).Error; err != nil {
			return fail(err)
		}
		if slugCount > 0 {
			tx.Rollback()
			return badRequest(fmt.Sprintf("Organization slug '%s' already exists", in.OrgSlug))
		}

		hashed, err := common.HashPassword(in.Password)
		if err != nil {
			return fail(err)
		}

		if emailHit {
			if err := tx.Model(&byEmail).Updates(map[string]interface{}{
				"username":        in.Username,
				"language":        language,
				"hashed_password": hashed,
			}).Error; err != nil {
				return fail(err)
			}
			user = byEmail
		} else {
			user = model.User{
				Email:          in.Email,
				Username:       in.Username,
				Language:       language,
				HashedPassword: hashed,
				IsActive:       false,
				Status:         model.UserDormant,
				SystemRole:     model.SystemUser,
			}
			if err := tx.Create(&user).Error; err != nil {
				return fail(err)
			}
		}

		org := model.Organization{
			Name:        in.OrgName,
			Slug:        in.OrgSlug,
			OrgType:     model.OrgTeam,
			OwnerUserID: user.ID,
			Status:      model.OrgPending,
		}
		if err := tx.Create(&org).Error; err != nil {
			return fail(err)
		}
		if err := InitializeOrgQuotas(tx, org.ID); err != nil {
			return fail(err)
		}
		if err := tx.Create(&model.Member{UserID: user.ID, OrgID: org.ID, OrgRole: model.OrgRoleAdmin}).Error; err != nil {
			return fail(err)
		}
		if err := tx.Commit().Error; err != nil {
			return fail(err)
		}
		return nil
	}()
	if herr != nil {
		return nil, herr
	}

	db.Where("id = ?", user.ID).First(&user)
	token, err := common.CreateActivationToken(user.UUID)
	if err != nil {
		log.WithContext(ctx).Errorf("Registration error: %v", err)
		return nil, common.NewHTTPError(http.StatusInternalServerError, "Internal server error during registration")
	}
	go SendActivationNotification(context.WithoutCancel(ctx), user.Email, user.Username, token, user.Language)
	return &user, nil
}

// discardPendingOrgs soft-deletes the PENDING orgs owned by an unactivated user together with their
// memberships and (never used) quota rows. Soft deletion alone frees the slug: uniqueness only
// covers rows with deleted_at IS NULL.
func discardPendingOrgs(tx *gorm.DB, userID int64) error {
	var orgIDs []int64
	if err := tx.Model(&model.Organization{}).Where("owner_user_id = ? AND status = ?", userID, model.OrgPending).
		Pluck("id", &orgIDs).Error; err != nil {
		return err
	}
	if len(orgIDs) == 0 {
		return nil
	}
	if err := tx.Where("org_id IN ?", orgIDs).Delete(&model.Member{}).Error; err != nil {
		return err
	}
	if err := tx.Where("org_id IN ?", orgIDs).Delete(&model.OrgResourceQuota{}).Error; err != nil {
		return err
	}
	if err := tx.Where("org_id IN ?", orgIDs).Delete(&model.OrgResourceConsumption{}).Error; err != nil {
		return err
	}
	return tx.Where("id IN ?", orgIDs).Delete(&model.Organization{}).Error
}

// Activate activates the account and its PENDING orgs. Returns the success message. Soft-deleted
// (discarded) pending orgs stay deleted.
func Activate(token string) (string, *common.HTTPError) {
	userUUID, err := common.VerifyActivationToken(strings.Trim(token, `"`))
	if err != nil {
		return "", badRequest("Invalid or expired activation token")
	}

	db := dbs.DB()
	var user model.User
	if err := db.Where("uuid = ?", userUUID).First(&user).Error; err != nil {
		return "", badRequest("User not found")
	}
	if user.IsActive {
		return "Account already activated", nil
	}

	txErr := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&user).Updates(map[string]interface{}{
			"is_active": true,
			"status":    model.UserActive,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.Organization{}).
			Where("owner_user_id = ? AND status = ?", user.ID, model.OrgPending).
			Update("status", model.OrgActive).Error
	})
	if txErr != nil {
		log.Errorf("Activation error: %v", txErr)
		return "", common.NewHTTPError(http.StatusInternalServerError, "Internal server error during activation")
	}
	return "Account activated", nil
}

// Login authenticates and issues an RS256 token. Without org_uuid the earliest membership is
// used; without region the first available region is used.
func Login(username, password, orgUUID, region string) (*TokenWithContext, *common.HTTPError) {
	db := dbs.DB()

	var user model.User
	if err := db.Where("(username = ? OR email = ?)", username, username).First(&user).Error; err != nil ||
		!common.VerifyPassword(password, user.HashedPassword) {
		return nil, unauthorized("Incorrect username or password")
	}
	if !user.IsActive {
		return nil, unauthorized("User account is not active")
	}
	if user.Status == model.UserDisabled {
		return nil, forbidden("User account is disabled")
	}

	var org *model.Organization
	if orgUUID != "" {
		var o model.Organization
		if err := db.Where("uuid = ?", orgUUID).First(&o).Error; err != nil {
			return nil, unauthorized("Organization not found")
		}
		org = &o
	} else {
		var first model.Member
		if err := db.Scopes(model.ActiveMembers).
			Joins("JOIN organizations ON organizations.id = members.org_id AND organizations.deleted_at IS NULL").
			Where("members.user_id = ?", user.ID).Order("members.created_at ASC").First(&first).Error; err == nil {
			var o model.Organization
			if err := db.Where("id = ?", first.OrgID).First(&o).Error; err == nil {
				org = &o
			}
		}
	}

	orgRole := model.OrgRoleNone
	isOwner := false
	if org != nil {
		if !user.IsAdmin() {
			var m model.Member
			if err := db.Scopes(model.ActiveMembers).Where("user_id = ? AND org_id = ?", user.ID, org.ID).First(&m).Error; err != nil {
				return nil, forbidden("User is not a member of this organization")
			}
			orgRole = m.OrgRole
		} else {
			orgRole = model.OrgRoleAdmin
		}
		isOwner = org.OwnerUserID == user.ID
	}

	var regionObj *model.Region
	if region != "" {
		var r model.Region
		if err := db.Where("uuid = ? AND is_available = ?", region, true).First(&r).Error; err != nil {
			return nil, unauthorized(fmt.Sprintf("Region '%s' not found or unavailable", region))
		}
		regionObj = &r
	} else {
		var r model.Region
		if err := db.Where("is_available = ?", true).Order("id ASC").First(&r).Error; err == nil {
			regionObj = &r
		}
	}
	targetRegion := ""
	if regionObj != nil {
		targetRegion = regionObj.UUID
	}

	var orgUUIDOut, orgNameOut *string
	claimOrgUUID, claimOrgName, orgLog := "", "", "none"
	if org != nil {
		claimOrgUUID, claimOrgName, orgLog = org.UUID, org.Name, org.Name
		orgUUIDOut, orgNameOut = &org.UUID, &org.Name
	}

	token, _, err := common.CreateAccessToken(user.UUID, user.Email, claimOrgUUID, claimOrgName, targetRegion,
		int(user.SystemRole), int(orgRole), int(user.Status), isOwner)
	if err != nil {
		log.Errorf("Login error: %v", err)
		return nil, common.NewHTTPError(http.StatusInternalServerError, "Internal server error during login")
	}
	log.Infof("Login: user=%s, org=%s, region=%s", user.Username, orgLog, targetRegion)

	if org != nil && regionObj != nil {
		TriggerConsumptionSync(org.ID, org.UUID, regionObj.ID, regionObj.InternalEndpoint, regionObj.InternalSecret)
	}
	return newTokenResponse(token, orgUUIDOut, orgNameOut, targetRegion), nil
}

// SwitchOrg issues a token for another org (optionally another region) and revokes the old token
// only after the new one is issued.
func SwitchOrg(claims *common.AccessTokenClaims, orgUUID string, region *string) (*TokenWithContext, *common.HTTPError) {
	db := dbs.DB()

	var user model.User
	if err := db.Where("uuid = ?", claims.Subject).First(&user).Error; err != nil {
		return nil, notFound("User not found")
	}
	var org model.Organization
	if err := db.Where("uuid = ?", orgUUID).First(&org).Error; err != nil {
		return nil, notFound("Organization not found")
	}
	if org.Status == model.OrgPending || org.Status == model.OrgDisabled {
		return nil, forbidden("Organization is not accessible")
	}

	orgRole := model.OrgRoleAdmin
	if !user.IsAdmin() {
		var m model.Member
		if err := db.Scopes(model.ActiveMembers).Where("user_id = ? AND org_id = ?", user.ID, org.ID).First(&m).Error; err != nil {
			return nil, forbidden("User is not a member of this organization")
		}
		orgRole = m.OrgRole
	}

	targetRegion := claims.Region
	if region != nil && *region != "" {
		targetRegion = *region
	}
	if targetRegion != "" {
		var count int64
		db.Model(&model.Region{}).Where("uuid = ? AND is_available = ?", targetRegion, true).Count(&count)
		if count == 0 {
			return nil, notFound(fmt.Sprintf("Region '%s' not found or unavailable", targetRegion))
		}
	}

	token, _, err := common.CreateAccessToken(user.UUID, user.Email, org.UUID, org.Name, targetRegion,
		int(user.SystemRole), int(orgRole), int(user.Status), org.OwnerUserID == user.ID)
	if err == nil {
		err = revokeClaims(db, claims)
	}
	if err != nil {
		log.Errorf("Switch org error: %v", err)
		return nil, common.NewHTTPError(http.StatusInternalServerError, "Internal server error during org switch")
	}
	return newTokenResponse(token, &org.UUID, &org.Name, targetRegion), nil
}

// SwitchRegion re-reads user/org/membership from the DB, issues a token for the new region and
// revokes the old token.
func SwitchRegion(claims *common.AccessTokenClaims, region string) (*TokenWithContext, *common.HTTPError) {
	db := dbs.DB()

	var regionCount int64
	db.Model(&model.Region{}).Where("uuid = ? AND is_available = ?", region, true).Count(&regionCount)
	if regionCount == 0 {
		return nil, notFound(fmt.Sprintf("Region '%s' not found or unavailable", region))
	}

	var user model.User
	if err := db.Where("uuid = ?", claims.Subject).First(&user).Error; err != nil {
		return nil, notFound("User not found")
	}
	if user.Status == model.UserDisabled {
		return nil, forbidden("User account is disabled")
	}

	orgUUID, orgName := claims.OrgID, claims.OrgName
	orgRole := model.OrgRoleNone
	isOwner := false
	if orgUUID != "" {
		// A deleted org must not be carried into a fresh token, not even for SystemAdmin.
		var org model.Organization
		if db.Where("uuid = ?", orgUUID).Limit(1).Find(&org).RowsAffected == 0 {
			return nil, notFound("Organization not found")
		}
		orgName = org.Name
		isOwner = org.OwnerUserID == user.ID
		if user.IsAdmin() {
			orgRole = model.OrgRoleAdmin
		} else {
			var m model.Member
			if err := db.Scopes(model.ActiveMembers).Where("user_id = ? AND org_id = ?", user.ID, org.ID).First(&m).Error; err != nil {
				return nil, forbidden("User is no longer a member of this organization")
			}
			orgRole = m.OrgRole
		}
	}

	token, _, err := common.CreateAccessToken(user.UUID, user.Email, orgUUID, orgName, region,
		int(user.SystemRole), int(orgRole), int(user.Status), isOwner)
	if err == nil {
		err = revokeClaims(db, claims)
	}
	if err != nil {
		log.Errorf("Switch region error: %v", err)
		return nil, common.NewHTTPError(http.StatusInternalServerError, "Internal server error during region switch")
	}
	return newTokenResponse(token, &orgUUID, &orgName, region), nil
}

// RevokeToken revokes the current token.
func RevokeToken(claims *common.AccessTokenClaims) *common.HTTPError {
	if err := revokeClaims(dbs.DB(), claims); err != nil {
		log.Errorf("Revoke token error: %v", err)
		return common.NewHTTPError(http.StatusInternalServerError, "Internal Server Error")
	}
	return nil
}
