/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"errors"
	"net/http"
	"strconv"

	. "web/src/common"
	"web/src/model"
	"web/src/routes"
	"web/src/utils"

	"github.com/gin-gonic/gin"
)

var userAPI = &UserAPI{}
var userAdmin = &routes.UserAdmin{}

type UserAPI struct{}

type UserPayload struct {
	Username string `json:"username" binding:"required,min=2"`
	Password string `json:"password" binding:"required,min=8,max=32"`
	ID       string `json:"id,omitempty" binding:"omitempty"`
	Org      string `json:"org,omitempty"` // Org name; if set, creates user+org atomically
	// Optional profile fields (can be set at creation time)
	FirstName  string `json:"first_name,omitempty"`
	LastName   string `json:"last_name,omitempty"`
	Region     string `json:"region,omitempty"`
	Language   string `json:"language,omitempty"`
	SystemRole *int   `json:"system_role,omitempty"` // 0=User, 1=Admin (SystemAdmin only)
}

type UserPatchPayload struct {
	Password string `json:"password" binding:"required,min=6"`
}

type UserResponse struct {
	UserInfo    *ResourceReference `json:"user"`
	OrgInfo     *ResourceReference `json:"org,omitempty"`
	AccessToken string             `json:"token,omitempty"`
	Role        string             `json:"role,omitempty"`
}

type UserListResponse struct {
	Offset int             `json:"offset"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Users  []*UserResponse `json:"users"`
}

// @Summary get a user
// @Description get a user
// @tags Authorization
// @Accept  json
// @Produce json
// @Success 200 {object} UserResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /users/{id} [get]
func (v *UserAPI) Get(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	logger.Debugf("Get user by uuid: %s", uuID)
	user, err := userAdmin.GetUserByUUID(ctx, uuID)
	if err != nil {
		logger.Errorf("Failed to get user by uuid: %s", uuID)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query", err)
		return
	}
	userResp := &UserResponse{
		UserInfo: &ResourceReference{
			ID:   user.UUID,
			Name: user.Email,
		},
	}
	logger.Debugf("Got user : %+v", userResp)
	c.JSON(http.StatusOK, userResp)
}

// @Summary patch a user
// @Description patch a user
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   message	body   UserPatchPayload  true   "User patch payload"
// @Success 200 {object} UserResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /users/{id} [patch]
func (v *UserAPI) Patch(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	payload := &UserPatchPayload{}
	err := c.ShouldBindJSON(payload)
	if err != nil {
		logger.Errorf("Failed to bind json: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	user, err := userAdmin.GetUserByUUID(ctx, uuID)
	if err != nil {
		logger.Errorf("Failed to get user by uuid: %s", uuID)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query", err)
		return
	}
	logger.Debugf("Patch user %s with %+v", uuID, payload)
	// Use ChangePassword with empty old password (admin override)
	memberShip := GetMemberShip(ctx)
	if memberShip.IsSystemAdmin() {
		// SystemAdmin can reset password without old password
		hash, hashErr := userAdmin.GenerateFromPassword(payload.Password)
		if hashErr != nil {
			ErrorResponse(c, http.StatusInternalServerError, "Failed to hash password", hashErr)
			return
		}
		db := DB()
		if err = db.Model(user).Update("password", hash).Error; err != nil {
			ErrorResponse(c, http.StatusInternalServerError, "Failed to update password", err)
			return
		}
	} else {
		ErrorResponse(c, http.StatusForbidden, "Only SystemAdmin can reset password via API", nil)
		return
	}
	userResp := &UserResponse{
		UserInfo: &ResourceReference{
			ID:        user.UUID,
			Name:      user.Email,
			CreatedAt: user.CreatedAt.Format(TimeStringForMat),
			UpdatedAt: user.UpdatedAt.Format(TimeStringForMat),
		},
	}
	logger.Debugf("Patched user %s successfully, %+v", uuID, userResp)
	c.JSON(http.StatusOK, userResp)
}

// @Summary delete a user
// @Description delete a user
// @tags Authorization
// @Accept  json
// @Produce json
// @Success 204
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /users/{id} [delete]
func (v *UserAPI) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	logger.Debugf("Deleting user %s", uuID)
	user, err := userAdmin.GetUserByUUID(ctx, uuID)
	if err != nil {
		logger.Errorf("Failed to get user by uuid: %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query", err)
		return
	}
	err = userAdmin.Delete(ctx, user)
	if err != nil {
		logger.Errorf("Failed to delete user %s, %+v", uuID, err)
		ErrorResponse(c, http.StatusBadRequest, "Not able to delete", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// @Summary create a user
// @Description create a user
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   message	body   UserPayload  true   "User create payload"
// @Success 200 {object} UserResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /users [post]
func (v *UserAPI) Create(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &UserPayload{}
	err := c.ShouldBindJSON(payload)
	if err != nil {
		logger.Errorf("Failed to bind json: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	logger.Debugf("Creating user with %+v", payload)
	email := payload.Username // "username" field is email
	password := payload.Password
	userUUID := payload.ID
	if userUUID != "" && !utils.IsUUID(userUUID) {
		logger.Errorf("Invalid user uuid: %s", userUUID)
		ErrorResponse(c, http.StatusBadRequest, "Invalid user uuid", nil)
		return
	}

	// Scenario 2B: if no org specified, create a Dormant user without Org
	// Scenario 1: if org specified, create user + org atomically
	orgName := payload.Org

	userResp := &UserResponse{}
	if orgName == "" {
		// Scenario 2B: Dormant user (no org)
		user, err := userAdmin.Create(ctx, email, password, userUUID)
		if err != nil {
			logger.Errorf("Failed to create dormant user: %+v", err)
			ErrorResponse(c, http.StatusInternalServerError, "Failed to create user", err)
			return
		}
		userResp.UserInfo = &ResourceReference{
			ID:        user.UUID,
			Name:      email,
			CreatedAt: user.CreatedAt.Format(TimeStringForMat),
			UpdatedAt: user.UpdatedAt.Format(TimeStringForMat),
		}
	} else {
		// Scenario 1: user + org
		user, org, err := userAdmin.CreateWithOrg(ctx, email, password, orgName, "")
		if err != nil {
			logger.Errorf("Failed to create user with org: %+v", err)
			ErrorResponse(c, http.StatusInternalServerError, "Failed to create user", err)
			return
		}
		// If UUID was specified, update it
		if userUUID != "" {
			db := DB()
			db.Model(user).Update("uuid", userUUID)
			user.UUID = userUUID
		}
		userResp.UserInfo = &ResourceReference{
			ID:        user.UUID,
			Name:      email,
			CreatedAt: user.CreatedAt.Format(TimeStringForMat),
			UpdatedAt: user.UpdatedAt.Format(TimeStringForMat),
		}
		userResp.OrgInfo = &ResourceReference{
			ID:        org.UUID,
			Name:      orgName,
			CreatedAt: org.CreatedAt.Format(TimeStringForMat),
			UpdatedAt: org.UpdatedAt.Format(TimeStringForMat),
		}
		userResp.Role = model.OrgAdmin.String()
	}
	logger.Debugf("Created user successfully, %+v", userResp)

	// Apply optional profile fields if provided
	if payload.FirstName != "" || payload.LastName != "" || payload.Region != "" || payload.Language != "" {
		if u, e := userAdmin.GetUserByUUID(ctx, userResp.UserInfo.ID); e == nil {
			_ = userAdmin.UpdateProfile(ctx, u.ID, payload.FirstName, payload.LastName, payload.Region, payload.Language, "")
		}
	}

	c.JSON(http.StatusOK, userResp)
}

// @Summary list users
// @Description list users
// @tags Authorization
// @Accept  json
// @Produce json
// @Success 200 {object} UserListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /users [get]
func (v *UserAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "50")
	queryStr := c.DefaultQuery("query", "")
	logger.Debugf("List users, offset:%s, limit:%s, query:%s", offsetStr, limitStr, queryStr)
	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		logger.Errorf("Invalid query offset: %s, %+v", offsetStr, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset: "+offsetStr, err)
		return
	}
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		logger.Errorf("Invalid query limit: %s, %+v", limitStr, err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query limit: "+limitStr, err)
		return
	}
	if offset < 0 || limit < 0 {
		errStr := "Invalid query offset or limit, cannot be negative"
		logger.Errorf(errStr)
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset or limit", errors.New(errStr))
		return
	}
	total, users, err := userAdmin.List(ctx, int64(offset), int64(limit), "-created_at", queryStr)
	if err != nil {
		logger.Errorf("Failed to list users, %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Failed to list users", err)
		return
	}
	userListResp := &UserListResponse{
		Total:  int(total),
		Offset: offset,
		Limit:  len(users),
	}
	userListResp.Users = make([]*UserResponse, userListResp.Limit)
	for i, user := range users {
		userListResp.Users[i] = &UserResponse{
			UserInfo: &ResourceReference{
				ID:        user.UUID,
				Name:      user.Email,
				CreatedAt: user.CreatedAt.Format(TimeStringForMat),
				UpdatedAt: user.UpdatedAt.Format(TimeStringForMat),
			},
		}
	}
	logger.Debugf("List users successfully, %+v", userListResp)
	c.JSON(http.StatusOK, userListResp)
}

// @Summary check if email exists
// @Description check if email exists (for middleware registration)
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   email    query     string  true   "Email to check"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} common.APIError "Bad request"
// @Router /validate [get]
func (v *UserAPI) ValidateEmail(c *gin.Context) {
	email := c.Query("email")
	if email == "" {
		ErrorResponse(c, http.StatusBadRequest, "Email query parameter is required", nil)
		return
	}
	exists, userUUID, err := userAdmin.ValidateEmail(c.Request.Context(), email)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Failed to validate email", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"exists":    exists,
		"user_uuid": userUUID,
	})
}

// @Summary change own password
// @Description change own password
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   message	body   map[string]string  true   "Password change payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /self/password [patch]
func (v *UserAPI) ChangePassword(c *gin.Context) {
	ctx := c.Request.Context()
	memberShip := GetMemberShip(ctx)
	payload := struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required,min=8,max=32"`
	}{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	err := userAdmin.ChangePassword(ctx, memberShip.UserID, payload.OldPassword, payload.NewPassword)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to change password", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// @Summary login to get the access token
// @Description get token by user name
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   message	body   UserPayload  true   "User Credential"
// @Success 200 {object} UserResponse
// @Failure 401 {object} common.APIError "Invalid user name or password"
// @Router /login [post]
func (v *UserAPI) LoginPost(c *gin.Context) {
	payload := &UserPayload{}
	err := c.ShouldBindJSON(payload)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Input JSON format error", err)
		return
	}
	email := payload.Username // "username" field is email
	password := payload.Password
	logger.Debugf("Login with email: %s", email)
	user, err := userAdmin.Validate(c.Request.Context(), email, password)
	if err != nil {
		logger.Errorf("Failed to validate user: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid username or password", err)
		return
	}
	ctx := c.Request.Context()

	// Get org context
	var orgID int64
	if payload.Org != "" {
		org, orgErr := orgAdmin.GetOrgByName(ctx, payload.Org)
		if orgErr != nil {
			logger.Errorf("Failed to get org: %+v", orgErr)
			ErrorResponse(c, http.StatusBadRequest, "Invalid organization", orgErr)
			return
		}
		orgID = org.ID
	}

	oid, _, orgRole, _, token, _, _, err := userAdmin.AccessToken(ctx, user.ID, orgID)
	if err != nil {
		logger.Errorf("Failed to get access token: %+v", err)
		clErr, ok := err.(*CLError)
		if ok && clErr.Code == ErrUserDisabled {
			ErrorResponse(c, http.StatusForbidden, "User account is disabled", err)
		} else {
			ErrorResponse(c, http.StatusBadRequest, "Invalid organization with username", err)
		}
		return
	}

	// Get org info for response
	orgResp := &ResourceReference{}
	if oid > 0 {
		org, orgErr := orgAdmin.Get(ctx, oid)
		if orgErr == nil {
			orgResp.ID = org.UUID
			orgResp.Name = org.Name
		}
	}

	userResp := &UserResponse{
		UserInfo: &ResourceReference{
			Name: email,
			ID:   user.UUID,
		},
		OrgInfo:     orgResp,
		AccessToken: token,
		Role:        orgRole.String(),
	}
	logger.Debugf("Login successfully, %+v", userResp)
	c.JSON(http.StatusOK, userResp)
}

// @Summary update own profile
// @Description update own profile (name, region, language). SystemAdmin can also update any user's remark.
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   id   path   string  true  "User UUID ('self' for own profile)"
// @Param   message body map[string]string true "Profile update payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /users/{id}/profile [patch]
func (v *UserAPI) UpdateProfile(c *gin.Context) {
	ctx := c.Request.Context()
	memberShip := GetMemberShip(ctx)

	uuID := c.Param("id")
	var targetUserID int64
	if uuID == "self" || uuID == "" {
		targetUserID = memberShip.UserID
	} else {
		user, err := userAdmin.GetUserByUUID(ctx, uuID)
		if err != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid user id", err)
			return
		}
		targetUserID = user.ID
	}

	payload := struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Region    string `json:"region"`
		Language  string `json:"language"`
		Remark    string `json:"remark"`
	}{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}

	err := userAdmin.UpdateProfile(ctx, targetUserID, payload.FirstName, payload.LastName, payload.Region, payload.Language, payload.Remark)
	if err != nil {
		ErrorResponse(c, http.StatusForbidden, "Failed to update profile", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// @Summary demote a SystemAdmin to SystemUser
// @Description demote a SystemAdmin to SystemUser. Only SystemAdmin can call this. Cannot demote self or the last SystemAdmin.
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   id   path   string  true  "User UUID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 403 {object} common.APIError "Forbidden"
// @Router /users/{id}/demote [post]
func (v *UserAPI) DemoteSystemAdmin(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	user, err := userAdmin.GetUserByUUID(ctx, uuID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid user id", err)
		return
	}
	if err = userAdmin.DemoteSystemAdmin(ctx, user.ID); err != nil {
		ErrorResponse(c, http.StatusForbidden, "Failed to demote user", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// @Summary switch current organization
// @Description switch the user's current organization context and get a new token
// @tags Authorization
// @Accept  json
// @Produce json
// @Param   message	body   map[string]interface{}  true   "Org switch payload"
// @Success 200 {object} UserResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /switch-org [post]
func (v *UserAPI) SwitchOrg(c *gin.Context) {
	ctx := c.Request.Context()
	memberShip := GetMemberShip(ctx)

	payload := struct {
		OrgID int64 `json:"org_id" binding:"required"`
	}{}
	err := c.ShouldBindJSON(&payload)
	if err != nil {
		logger.Errorf("Failed to bind json: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}

	logger.Debugf("User %d switching to org %d", memberShip.UserID, payload.OrgID)
	token, issueAt, expiresAt, err := userAdmin.SwitchOrg(ctx, memberShip.UserID, payload.OrgID)
	if err != nil {
		logger.Errorf("Failed to switch org: %+v", err)
		ErrorResponse(c, http.StatusForbidden, "Failed to switch organization", err)
		return
	}

	// Get org info for response
	org, err := orgAdmin.Get(ctx, payload.OrgID)
	if err != nil {
		logger.Errorf("Failed to get org info: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid organization", err)
		return
	}

	user, err := userAdmin.Get(ctx, memberShip.UserID)
	if err != nil {
		logger.Errorf("Failed to get user info: %+v", err)
		ErrorResponse(c, http.StatusBadRequest, "Invalid user", err)
		return
	}

	// We need to find the org role for the user in the target org
	// SwitchOrg internally already did this, but we need it for the response body
	member := &model.Member{}
	db := DB()
	var orgRole model.OrgRole
	if err := db.Where("user_id = ? AND org_id = ?", memberShip.UserID, payload.OrgID).Take(member).Error; err == nil {
		orgRole = member.OrgRole
	} else if memberShip.IsSystemAdmin() {
		orgRole = model.OrgNone
	} else {
		ErrorResponse(c, http.StatusForbidden, "Not a member of target organization", err)
		return
	}

	userResp := &UserResponse{
		UserInfo: &ResourceReference{
			Name: user.Email,
			ID:   user.UUID,
		},
		OrgInfo: &ResourceReference{
			ID:   org.UUID,
			Name: org.Name,
		},
		AccessToken: token,
		Role:        orgRole.String(),
	}

	logger.Infof("User %d successfully switched to org %d. New token issued (exp: %d)", memberShip.UserID, payload.OrgID, expiresAt)
	_ = issueAt
	c.JSON(http.StatusOK, userResp)
}
