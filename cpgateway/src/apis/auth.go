package apis

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"

	"cpgateway/src/common"
	"cpgateway/src/dbs"
	"cpgateway/src/model"
	"cpgateway/src/services"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func requireQuery(c *gin.Context, name string) (string, bool) {
	v, ok := c.GetQuery(name)
	if !ok {
		common.AbortValidation(c, "query", errors.New("Field required: "+name))
	}
	return v, ok
}

// POST /auth/register
func Register(c *gin.Context) {
	var in services.RegisterInput
	if !bindJSON(c, &in) {
		return
	}
	if !slugPattern.MatchString(in.OrgSlug) {
		common.AbortValidation(c, "body", errors.New("org_slug should match pattern '^[a-z0-9][a-z0-9-]*[a-z0-9]$'"))
		return
	}
	user, herr := services.Register(c.Request.Context(), &in)
	if herr != nil {
		common.AbortWithError(c, herr)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "Verification email sent", "user": toUserOut(user)})
}

// GET /auth/activate?token=
func ActivateAccount(c *gin.Context) {
	token, ok := requireQuery(c, "token")
	if !ok {
		return
	}
	message, herr := services.Activate(token)
	if herr != nil {
		c.AbortWithStatusJSON(herr.Status, gin.H{"detail": herr.Detail, "result": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": message, "result": true, "status": "active"})
}

// POST /auth/token (JSON)
func Login(c *gin.Context) {
	var in struct {
		Username string  `json:"username" binding:"required"`
		Password string  `json:"password" binding:"required"`
		OrgUUID  *string `json:"org_uuid"`
		Region   *string `json:"region"`
	}
	if !bindJSON(c, &in) {
		return
	}
	resp, herr := services.Login(in.Username, in.Password, deref(in.OrgUUID), deref(in.Region))
	if herr != nil {
		common.AbortWithError(c, herr)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// POST /auth/token/form (OAuth2 password form)
func LoginForm(c *gin.Context) {
	username, okUser := c.GetPostForm("username")
	password, okPass := c.GetPostForm("password")
	if !okUser || !okPass {
		common.AbortValidation(c, "body", errors.New("username and password are required"))
		return
	}
	resp, herr := services.Login(username, password, "", "")
	if herr != nil {
		common.AbortWithError(c, herr)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// POST /auth/switch-org
func SwitchOrg(c *gin.Context) {
	var in struct {
		OrgUUID string  `json:"org_uuid" binding:"required"`
		Region  *string `json:"region"`
	}
	if !bindJSON(c, &in) {
		return
	}
	resp, herr := services.SwitchOrg(currentClaims(c), in.OrgUUID, in.Region)
	if herr != nil {
		common.AbortWithError(c, herr)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// POST /auth/switch-region
func SwitchRegion(c *gin.Context) {
	var in struct {
		Region string `json:"region" binding:"required"`
	}
	if !bindJSON(c, &in) {
		return
	}
	resp, herr := services.SwitchRegion(currentClaims(c), in.Region)
	if herr != nil {
		common.AbortWithError(c, herr)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// POST /auth/token/revoke
func RevokeToken(c *gin.Context) {
	if herr := services.RevokeToken(currentClaims(c)); herr != nil {
		common.AbortWithError(c, herr)
		return
	}
	c.Status(http.StatusNoContent)
}

// GET /auth/me
func GetMe(c *gin.Context) {
	claims := currentClaims(c)
	var user model.User
	if err := dbs.DBContext(c.Request.Context()).Where("uuid = ?", claims.Subject).First(&user).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, "User not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"uuid":             user.UUID,
		"email":            user.Email,
		"username":         user.Username,
		"first_name":       user.FirstName,
		"last_name":        user.LastName,
		"system_role":      int(user.SystemRole),
		"is_superuser":     user.IsSuperuser,
		"status":           user.Status.Name(),
		"current_org_uuid": claims.OrgID,
		"current_region":   claims.Region,
	})
}

type userOrgItem struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	OrgType   int    `json:"org_type"`
	Status    int    `json:"status"`
	OrgRole   int    `json:"org_role"`
	IsOwner   bool   `json:"is_owner"`
	IsCurrent bool   `json:"is_current"`
}

// GET /auth/me/orgs — admins see every org, other users only their memberships.
func GetMyOrgs(c *gin.Context) {
	claims := currentClaims(c)
	db := dbs.DBContext(c.Request.Context())
	var user model.User
	if err := db.Where("uuid = ?", claims.Subject).First(&user).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, "User not found")
		return
	}

	var members []model.Member
	db.Scopes(model.ActiveMembers).Where("user_id = ?", user.ID).Order("id ASC").Find(&members)
	memberByOrg := make(map[int64]model.Member, len(members))
	orgIDs := make([]int64, 0, len(members))
	for _, m := range members {
		memberByOrg[m.OrgID] = m
		orgIDs = append(orgIDs, m.OrgID)
	}

	var orgs []model.Organization
	if user.IsAdmin() {
		db.Order("id ASC").Find(&orgs)
	} else if len(orgIDs) > 0 {
		db.Where("id IN ?", orgIDs).Order("id ASC").Find(&orgs)
	}

	items := make([]userOrgItem, 0, len(orgs))
	for _, org := range orgs {
		role := int(model.OrgRoleAdmin)
		if m, ok := memberByOrg[org.ID]; ok {
			role = int(m.OrgRole)
		}
		items = append(items, userOrgItem{
			UUID: org.UUID, Name: org.Name, Slug: org.Slug,
			OrgType: int(org.OrgType), Status: int(org.Status), OrgRole: role,
			IsOwner: org.OwnerUserID == user.ID, IsCurrent: org.UUID == claims.OrgID,
		})
	}
	c.JSON(http.StatusOK, items)
}

// GET /auth/invitation/info?token=
func GetInvitationInfo(c *gin.Context) {
	token, ok := requireQuery(c, "token")
	if !ok {
		return
	}
	info, herr := services.GetInvitationInfo(token)
	if herr != nil {
		common.AbortWithError(c, herr)
		return
	}
	c.JSON(http.StatusOK, info)
}

// POST /auth/invitation/accept
func AcceptInvitation(c *gin.Context) {
	var in struct {
		Token    string  `json:"token" binding:"required"`
		Username *string `json:"username"`
		Password *string `json:"password"`
	}
	if !bindJSON(c, &in) {
		return
	}
	result, herr := services.AcceptInvitation(in.Token, in.Username, in.Password)
	if herr != nil {
		common.AbortWithError(c, herr)
		return
	}
	c.JSON(http.StatusOK, result)
}

// GET /auth/public-key
func GetPublicKey(c *gin.Context) {
	pem := common.GetPublicKeyPEM()
	if pem == "" {
		common.AbortWithDetail(c, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"public_key": pem})
}
