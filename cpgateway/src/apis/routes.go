package apis

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"cpgateway/src/tracing"
)

func Routes() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	// 健康检查为周期探测，不产生 trace；访问日志需注册在追踪中间件之后才能取到 trace_id
	r.Use(tracing.GinMiddleware("cpgateway", "/health", "/api/v1/telemetry")...)
	r.Use(tracing.GinAccessLog())

	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "Welcome to Cloudland Control Plane Gateway v2.0"})
	})
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := r.Group("/api/v1")
	active := api.Group("", ActiveUser())
	admin := api.Group("", ActiveUser(), Superuser())

	// --- Auth ---
	auth := api.Group("/auth")
	auth.POST("/register", Register)
	auth.GET("/activate", ActivateAccount)
	auth.POST("/token", Login)
	auth.POST("/token/form", LoginForm)
	auth.GET("/invitation/info", GetInvitationInfo)
	auth.POST("/invitation/accept", AcceptInvitation)
	auth.GET("/public-key", GetPublicKey)
	authClaims := auth.Group("", ClaimsAuth())
	authClaims.POST("/switch-org", SwitchOrg)
	authClaims.POST("/switch-region", SwitchRegion)
	authClaims.POST("/token/revoke", RevokeToken)
	authClaims.GET("/me", GetMe)
	authClaims.GET("/me/orgs", GetMyOrgs)

	// --- Users ---
	admin.GET("/users", ListUsers)
	admin.GET("/users/:uuid", GetUser)
	admin.PUT("/users/:uuid/enable", EnableUser)
	admin.PUT("/users/:uuid/disable", DisableUser)
	admin.PUT("/users/:uuid/demote", DemoteUser)
	admin.DELETE("/users/:uuid", DeleteUser)
	active.PATCH("/users/:uuid/profile", UpdateProfile)
	active.PUT("/users/:uuid/password", ChangePassword)
	active.PUT("/users/:uuid", UpdateUser)

	// --- Orgs (superuser-only actions check inside the handlers) ---
	active.POST("/orgs", CreateOrg)
	active.GET("/orgs", ListOrgs)
	active.GET("/orgs/:uuid", GetOrg)
	active.PATCH("/orgs/:uuid", UpdateOrg)
	active.DELETE("/orgs/:uuid", DeleteOrg)
	active.PATCH("/orgs/:uuid/status", UpdateOrgStatus)
	active.POST("/orgs/:uuid/transfer-owner", TransferOwner)
	active.GET("/orgs/:uuid/members", ListMembers)
	active.POST("/orgs/:uuid/members", AddMember)
	active.PATCH("/orgs/:uuid/members/:user_uuid", UpdateMemberRole)
	active.DELETE("/orgs/:uuid/members/:user_uuid", RemoveMember)
	active.POST("/orgs/:uuid/invitations", CreateInvitation)
	active.GET("/orgs/:uuid/invitations", ListInvitations)
	active.DELETE("/orgs/:uuid/invitations/:invitation_uuid", CancelInvitation)

	// --- Regions (listing is public) ---
	api.GET("/regions", ListRegions)
	admin.POST("/regions", CreateRegion)
	admin.GET("/regions/:uuid", GetRegion)
	admin.PATCH("/regions/:uuid", UpdateRegion)
	admin.DELETE("/regions/:uuid", DeleteRegion)
	admin.POST("/regions/:uuid/rotate-secret", RotateRegionSecret)

	// --- Resource quota / consumption ---
	active.GET("/resources/quota/:org_uuid", GetOrgQuotas)
	active.GET("/resources/quota/:org_uuid/:region_uuid", GetOrgRegionQuota)
	admin.PUT("/resources/quota/:org_uuid/:region_uuid", UpdateOrgRegionQuota)
	active.GET("/resources/consumption/:org_uuid", GetOrgConsumptions)
	active.GET("/resources/consumption/:org_uuid/:region_uuid", GetOrgRegionConsumption)
	active.GET("/resources/info/:org_uuid", GetOrgResourceSummary)
	active.GET("/resources/info/:org_uuid/:region_uuid", GetOrgRegionResourceInfo)

	// --- Notification channels (current org from token) ---
	channelsRead := api.Group("/notification-channels", CurrentOrg(false))
	channelsWrite := api.Group("/notification-channels", CurrentOrg(true))
	channelsRead.GET("", ListChannels)
	channelsWrite.POST("", CreateChannel)
	channelsRead.GET("/:channel_uuid", GetChannel)
	channelsWrite.PUT("/:channel_uuid", UpdateChannel)
	channelsWrite.DELETE("/:channel_uuid", DeleteChannel)

	// --- Alarm summary ---
	active.GET("/alarm/summary", GetAlarmSummary)

	// --- Telemetry ---
	active.POST("/telemetry/traces", IngestBrowserTraces)

	// --- System settings & infrastructure ---
	admin.GET("/system/settings", ListSystemSettings)
	admin.PUT("/system/settings", UpdateSystemSettings)
	admin.POST("/system/settings/test-notification", TestNotification)
	admin.GET("/system/infrastructure", GetInfrastructure)
	admin.POST("/system/infrastructure/test-s3", TestS3)

	// --- Cloudland proxy (explicit whitelist) ---
	for _, route := range proxyRoutes {
		group := active
		if route.Admin {
			group = admin
		}
		group.Handle(route.Method, route.Path, proxyHandler(route))
	}

	return r
}
