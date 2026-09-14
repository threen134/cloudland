package apis

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"cpgateway-go/src/common"
	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
)

const (
	ctxClaims = "claims"
	ctxUser   = "current_user"
	ctxOrg    = "current_org"
)

func bearerToken(c *gin.Context) (string, bool) {
	parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func isRevoked(jti string) bool {
	if jti == "" {
		return false
	}
	var count int64
	dbs.DB().Model(&model.TokenRevocation{}).Where("jti = ?", jti).Count(&count)
	return count > 0
}

// ClaimsAuth mirrors _extract_claims used by the /auth endpoints.
func ClaimsAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c)
		if !ok {
			common.AbortWithDetail(c, http.StatusUnauthorized, "Missing or invalid Authorization header")
			return
		}
		claims, err := common.DecodeAccessToken(token)
		if err != nil {
			common.AbortWithDetail(c, http.StatusUnauthorized, "Invalid or expired token")
			return
		}
		if isRevoked(claims.ID) {
			common.AbortWithDetail(c, http.StatusUnauthorized, "Token has been revoked")
			return
		}
		c.Set(ctxClaims, claims)
		c.Next()
	}
}

// tokenClaims mirrors OAuth2PasswordBearer + verify_access_token used by the deps.
// Unlike Python, revoked tokens are rejected here as well.
func tokenClaims(c *gin.Context) (*common.AccessTokenClaims, bool) {
	token, ok := bearerToken(c)
	if !ok {
		c.Header("WWW-Authenticate", "Bearer")
		common.AbortWithDetail(c, http.StatusUnauthorized, "Not authenticated")
		return nil, false
	}
	claims, err := common.DecodeAccessToken(token)
	if err != nil {
		common.AbortWithDetail(c, http.StatusUnauthorized, "Could not validate credentials")
		return nil, false
	}
	if isRevoked(claims.ID) {
		common.AbortWithDetail(c, http.StatusUnauthorized, "Token has been revoked")
		return nil, false
	}
	return claims, true
}

// authenticateUser validates the token and loads the user, rejecting missing, inactive and disabled users.
func authenticateUser(c *gin.Context) (*common.AccessTokenClaims, *model.User, bool) {
	claims, ok := tokenClaims(c)
	if !ok {
		return nil, nil, false
	}
	if claims.Subject == "" {
		common.AbortWithDetail(c, http.StatusForbidden, "Invalid token: missing subject")
		return nil, nil, false
	}
	var user model.User
	if err := dbs.DBContext(c.Request.Context()).Where("uuid = ?", claims.Subject).First(&user).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, "User not found")
		return nil, nil, false
	}
	if !user.IsActive {
		common.AbortWithDetail(c, http.StatusBadRequest, "Inactive user")
		return nil, nil, false
	}
	if user.Status == model.UserDisabled {
		common.AbortWithDetail(c, http.StatusForbidden, "User account is disabled")
		return nil, nil, false
	}
	return claims, &user, true
}

// ActiveUser mirrors get_current_active_user. Disabled users are additionally rejected.
func ActiveUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, user, ok := authenticateUser(c)
		if !ok {
			return
		}
		c.Set(ctxClaims, claims)
		c.Set(ctxUser, user)
		c.Next()
	}
}

// Superuser mirrors get_current_superuser; must run after ActiveUser.
func Superuser() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireSuperuser(c) {
			return
		}
		c.Next()
	}
}

// CurrentOrg resolves the token's organization for org-scoped endpoints. The user must be active
// and, unless SystemAdmin, a formal member of the org; the org role comes from the database, not
// the token claims. write additionally rejects suspended orgs and requires org ADMIN or SystemAdmin.
func CurrentOrg(write bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, user, ok := authenticateUser(c)
		if !ok {
			return
		}
		if claims.OrgID == "" {
			common.AbortWithDetail(c, http.StatusBadRequest, "No active organization in token")
			return
		}
		var org model.Organization
		if err := dbs.DBContext(c.Request.Context()).Where("uuid = ?", claims.OrgID).First(&org).Error; err != nil {
			common.AbortWithDetail(c, http.StatusNotFound, "Organization not found")
			return
		}
		orgRole := model.OrgRoleAdmin
		if !user.IsAdmin() {
			member := findActiveMember(user.ID, org.ID)
			if member == nil {
				common.AbortWithDetail(c, http.StatusForbidden, "Not a member of this organization")
				return
			}
			orgRole = member.OrgRole
		}
		if org.Status == model.OrgPending || org.Status == model.OrgDisabled {
			common.AbortWithDetail(c, http.StatusForbidden, "Organization is not accessible")
			return
		}
		if write && org.Status == model.OrgSuspended {
			common.AbortWithDetail(c, http.StatusForbidden, "Organization is suspended")
			return
		}
		if write && orgRole < model.OrgRoleAdmin {
			common.AbortWithDetail(c, http.StatusForbidden, "Not enough permissions")
			return
		}
		c.Set(ctxClaims, claims)
		c.Set(ctxUser, user)
		c.Set(ctxOrg, &org)
		c.Next()
	}
}

func currentClaims(c *gin.Context) *common.AccessTokenClaims {
	v, _ := c.Get(ctxClaims)
	claims, _ := v.(*common.AccessTokenClaims)
	return claims
}

func currentUser(c *gin.Context) *model.User {
	v, _ := c.Get(ctxUser)
	user, _ := v.(*model.User)
	return user
}

func currentOrg(c *gin.Context) *model.Organization {
	v, _ := c.Get(ctxOrg)
	org, _ := v.(*model.Organization)
	return org
}

func requireSuperuser(c *gin.Context) bool {
	if user := currentUser(c); user == nil || !user.IsAdmin() {
		common.AbortWithDetail(c, http.StatusForbidden, "The user doesn't have enough privileges")
		return false
	}
	return true
}

func bindJSON(c *gin.Context, obj interface{}) bool {
	if err := c.ShouldBindJSON(obj); err != nil {
		common.AbortValidation(c, "body", err)
		return false
	}
	return true
}
