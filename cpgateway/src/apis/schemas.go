package apis

import (
	"time"

	"cpgateway/src/model"
	"cpgateway/src/services"
)

// Response shapes mirroring the Python Pydantic schemas.

type userOut struct {
	Email       string    `json:"email"`
	Username    string    `json:"username"`
	Language    string    `json:"language"`
	UUID        string    `json:"uuid"`
	IsActive    bool      `json:"is_active"`
	IsSuperuser bool      `json:"is_superuser"`
	SystemRole  int       `json:"system_role"`
	Status      string    `json:"status"`
	FirstName   string    `json:"first_name"`
	LastName    string    `json:"last_name"`
	Remark      string    `json:"remark"`
	CreatedAt   time.Time `json:"created_at"`
}

func toUserOut(u *model.User) userOut {
	return userOut{
		Email: u.Email, Username: u.Username, Language: u.Language, UUID: u.UUID,
		IsActive: u.IsActive, IsSuperuser: u.IsSuperuser, SystemRole: int(u.SystemRole),
		Status: u.Status.Name(), FirstName: u.FirstName, LastName: u.LastName,
		Remark: u.Remark, CreatedAt: u.CreatedAt,
	}
}

type orgOut struct {
	UUID       string    `json:"uuid"`
	Name       string    `json:"name"`
	Slug       string    `json:"slug"`
	OrgType    int       `json:"org_type"`
	Status     int       `json:"status"`
	OwnerUUID  string    `json:"owner_uuid"`
	OwnerName  *string   `json:"owner_name"`
	OwnerEmail *string   `json:"owner_email"`
	CreatedAt  time.Time `json:"created_at"`
}

func toOrgOut(org *model.Organization, owner *model.User) orgOut {
	out := orgOut{
		UUID: org.UUID, Name: org.Name, Slug: org.Slug,
		OrgType: int(org.OrgType), Status: int(org.Status), CreatedAt: org.CreatedAt,
	}
	if owner != nil && owner.ID != 0 {
		out.OwnerUUID = owner.UUID
		out.OwnerName = &owner.Username
		out.OwnerEmail = &owner.Email
	}
	return out
}

type orgDetailOut struct {
	orgOut
	MemberCount int64 `json:"member_count"`
}

type memberOut struct {
	UUID             string    `json:"uuid"`
	UserUUID         string    `json:"user_uuid"`
	OrgUUID          string    `json:"org_uuid"`
	OrgRole          int       `json:"org_role"`
	UserEmail        *string   `json:"user_email"`
	Username         string    `json:"username"`
	IsOwner          bool      `json:"is_owner"`
	IsSuperuser      bool      `json:"is_superuser"`
	InvitationStatus *int      `json:"invitation_status"`
	CreatedAt        time.Time `json:"created_at"`
}

type invitationOut struct {
	UUID         string     `json:"uuid"`
	Email        string     `json:"email"`
	OrgUUID      string     `json:"org_uuid"`
	OrgName      string     `json:"org_name"`
	OrgRole      int        `json:"org_role"`
	Status       int        `json:"status"`
	InviterEmail string     `json:"inviter_email"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    *time.Time `json:"expires_at"`
}

func invitationStatusValue(m *model.Member) int {
	if m.InvitationStatus == nil {
		return 0
	}
	return int(*m.InvitationStatus)
}

type regionPublicOut struct {
	UUID            string     `json:"uuid"`
	Name            string     `json:"name"`
	DisplayName     *string    `json:"display_name"`
	IsAvailable     bool       `json:"is_available"`
	MaintenanceMode bool       `json:"maintenance_mode"`
	Description     *string    `json:"description"`
	LastCheckAt     *time.Time `json:"last_check_at"`
	StatusMessage   *string    `json:"status_message"`
}

type regionAdminOut struct {
	regionPublicOut
	InternalEndpoint string    `json:"internal_endpoint"`
	FailCount        int       `json:"fail_count"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type regionCreatedOut struct {
	regionAdminOut
	InternalSecret string `json:"internal_secret"`
}

func toRegionPublic(r *model.Region) regionPublicOut {
	return regionPublicOut{
		UUID: r.UUID, Name: r.Name, DisplayName: r.DisplayName, IsAvailable: r.IsAvailable,
		MaintenanceMode: r.MaintenanceMode, Description: r.Description,
		LastCheckAt: r.LastCheckAt, StatusMessage: r.StatusMessage,
	}
}

func toRegionAdmin(r *model.Region) regionAdminOut {
	return regionAdminOut{
		regionPublicOut:  toRegionPublic(r),
		InternalEndpoint: r.InternalEndpoint,
		FailCount:        r.FailCount,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
}

type quotaFields struct {
	MaxCPUCores      float64 `json:"max_cpu_cores"`
	MaxRAMGB         float64 `json:"max_ram_gb"`
	MaxPublicIPs     int     `json:"max_public_ips"`
	MaxDiskGB        float64 `json:"max_disk_gb"`
	MaxVPCs          int     `json:"max_vpcs"`
	MaxLoadBalancers int     `json:"max_load_balancers"`
	MaxImages        int     `json:"max_images"`
}

type consumptionFields struct {
	CPUCores      float64 `json:"cpu_cores"`
	RAMGB         float64 `json:"ram_gb"`
	PublicIPs     int     `json:"public_ips"`
	DiskGB        float64 `json:"disk_gb"`
	VPCs          int     `json:"vpcs"`
	LoadBalancers int     `json:"load_balancers"`
	Images        int     `json:"images"`
}

func toQuotaFields(q *model.OrgResourceQuota) quotaFields {
	return quotaFields{q.MaxCPUCores, q.MaxRAMGB, q.MaxPublicIPs, q.MaxDiskGB, q.MaxVPCs, q.MaxLoadBalancers, q.MaxImages}
}

func toConsumptionFields(c *model.OrgResourceConsumption) consumptionFields {
	return consumptionFields{c.CPUCores, c.RAMGB, c.PublicIPs, c.DiskGB, c.VPCs, c.LoadBalancers, c.Images}
}

type quotaOut struct {
	quotaFields
	OrgUUID    string    `json:"org_uuid"`
	RegionUUID string    `json:"region_uuid"`
	RegionName string    `json:"region_name"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toQuotaOut(q *model.OrgResourceQuota, orgUUID string, r *model.Region) quotaOut {
	return quotaOut{toQuotaFields(q), orgUUID, r.UUID, r.Name, q.CreatedAt, q.UpdatedAt}
}

type consumptionOut struct {
	consumptionFields
	OrgUUID    string `json:"org_uuid"`
	RegionUUID string `json:"region_uuid"`
	RegionName string `json:"region_name"`
}

type resourceInfoOut struct {
	RegionUUID  string            `json:"region_uuid"`
	RegionName  string            `json:"region_name"`
	Consumption consumptionFields `json:"consumption"`
	Quota       quotaFields       `json:"quota"`
}

type channelOut struct {
	UUID      string                 `json:"uuid"`
	Name      string                 `json:"name"`
	Type      string                 `json:"type"`
	Config    map[string]interface{} `json:"config"`
	Enabled   bool                   `json:"enabled"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// toChannelOut masks config.secret like ChannelResponse.mask_secrets.
func toChannelOut(ch *model.NotificationChannel) channelOut {
	cfg := services.ParseChannelConfig(ch.Config)
	if services.Truthy(cfg["secret"]) {
		cfg["secret"] = services.MaskedValue
	}
	return channelOut{ch.UUID, ch.Name, ch.Type, cfg, ch.Enabled, ch.CreatedAt, ch.UpdatedAt}
}

type settingOut struct {
	Key         string      `json:"key"`
	Value       interface{} `json:"value"`
	ValueType   string      `json:"value_type"`
	Category    string      `json:"category"`
	Description *string     `json:"description"`
	IsSecret    bool        `json:"is_secret"`
	UpdatedAt   *time.Time  `json:"updated_at"`
}

// toSettingOut deserializes the stored JSON value and masks non-empty secrets.
func toSettingOut(s *model.SystemSetting) settingOut {
	value := services.DeserializeSetting(s.Value)
	if s.IsSecret && services.Truthy(value) {
		value = services.MaskedValue
	}
	out := settingOut{Key: s.Key, Value: value, ValueType: s.ValueType, Category: s.Category, Description: s.Description, IsSecret: s.IsSecret}
	if !s.UpdatedAt.IsZero() {
		updated := s.UpdatedAt
		out.UpdatedAt = &updated
	}
	return out
}
