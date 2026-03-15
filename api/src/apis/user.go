/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"errors"
	"net/http"
	"strconv"

	. "api/src/common"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var userAPI = &UserAPI{}
var userAdmin = &services.UserAdmin{}

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
	Status      string             `json:"status,omitempty"`
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
		Status: user.Status.String(),
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
// Patch is deprecated. User management is now handled by Middle.
func (v *UserAPI) Patch(c *gin.Context) {
	logger.Error("UserAPI.Patch called — this endpoint is deprecated. User management is handled by Middle.")
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":   "This endpoint is deprecated",
		"message": "User management is now handled by Middle. Use PUT /api/v1/users/{uuid}/password on the Middle API gateway.",
	})
}

// Delete is deprecated. User management is now handled by Middle.
func (v *UserAPI) Delete(c *gin.Context) {
	logger.Error("UserAPI.Delete called — this endpoint is deprecated. User management is handled by Middle.")
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":   "This endpoint is deprecated",
		"message": "User management is now handled by Middle. Use DELETE /api/v1/users/{uuid} on the Middle API gateway.",
	})
}

// Create is deprecated. User management is now handled by Middle.
func (v *UserAPI) Create(c *gin.Context) {
	logger.Error("UserAPI.Create called — this endpoint is deprecated. User management is handled by Middle.")
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":   "This endpoint is deprecated",
		"message": "User creation is now handled by Middle. Use POST /api/v1/auth/register on the Middle API gateway.",
	})
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
			Status: user.Status.String(),
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

// ChangePassword is deprecated. User management is now handled by Middle.
func (v *UserAPI) ChangePassword(c *gin.Context) {
	logger.Error("ChangePassword called — this endpoint is deprecated. User management is handled by Middle.")
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":   "This endpoint is deprecated",
		"message": "Password changes are now handled by Middle. Use PUT /api/v1/users/{uuid}/password on the Middle API gateway.",
	})
}

// @Summary login to get the access token (DEPRECATED)
// @Description DEPRECATED: Token issuance is now handled by Middle. Use Middle's POST /api/v1/auth/token instead.
// @tags Authorization
// @Router /login [post]
func (v *UserAPI) LoginPost(c *gin.Context) {
	logger.Error("LoginPost called — this endpoint is deprecated. Token issuance is handled by Middle.")
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":   "This endpoint is deprecated",
		"message": "Token issuance is now handled by Middle. Use POST /api/v1/auth/token on the Middle API gateway.",
	})
}

// UpdateProfile is deprecated. User management is now handled by Middle.
func (v *UserAPI) UpdateProfile(c *gin.Context) {
	logger.Error("UpdateProfile called — this endpoint is deprecated. User management is handled by Middle.")
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":   "This endpoint is deprecated",
		"message": "Profile updates are now handled by Middle. Use PATCH /api/v1/users/{uuid}/profile on the Middle API gateway.",
	})
}

// DemoteSystemAdmin is deprecated. User management is now handled by Middle.
func (v *UserAPI) DemoteSystemAdmin(c *gin.Context) {
	logger.Error("DemoteSystemAdmin called — this endpoint is deprecated. User management is handled by Middle.")
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":   "This endpoint is deprecated",
		"message": "User role management is now handled by Middle. Use PUT /api/v1/users/{uuid}/demote on the Middle API gateway.",
	})
}

// @Summary switch current organization (DEPRECATED)
// @Description DEPRECATED: Org switching is now handled by Middle. Use Middle's POST /api/v1/auth/switch-org instead.
// @tags Authorization
// @Router /switch-org [post]
func (v *UserAPI) SwitchOrg(c *gin.Context) {
	logger.Error("SwitchOrg called — this endpoint is deprecated. Org switching is handled by Middle.")
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":   "This endpoint is deprecated",
		"message": "Org switching is now handled by Middle. Use POST /api/v1/auth/switch-org on the Middle API gateway.",
	})
}
