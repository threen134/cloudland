package model

import (
	"time"

	"cpgateway-go/src/dbs"

	"gorm.io/gorm"
)

type OrgRole int

const (
	OrgRoleNone   OrgRole = 0
	OrgRoleReader OrgRole = 1
	OrgRoleWriter OrgRole = 2
	OrgRoleAdmin  OrgRole = 3
)

type InvitationStatus int

const (
	InvitationPending   InvitationStatus = 0
	InvitationAccepted  InvitationStatus = 1
	InvitationExpired   InvitationStatus = 2
	InvitationCancelled InvitationStatus = 3
)

type Member struct {
	Model
	UserID  int64   `gorm:"not null;index" json:"user_id"`
	OrgID   int64   `gorm:"not null;index" json:"org_id"`
	OrgRole OrgRole `gorm:"not null" json:"org_role"`

	// Invitation fields (NULL for directly added members)
	InvitationToken     *string           `gorm:"type:varchar(512);uniqueIndex" json:"invitation_token,omitempty"`
	InvitationStatus    *InvitationStatus `gorm:"" json:"invitation_status,omitempty"`
	InvitedBy           *int64            `gorm:"" json:"invited_by,omitempty"`
	InvitationExpiresAt *time.Time        `gorm:"" json:"invitation_expires_at,omitempty"`
	GrantSuperuser      int               `gorm:"default:0;not null" json:"grant_superuser"`

	// Relationships
	User         *User         `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Organization *Organization `gorm:"foreignKey:OrgID" json:"organization,omitempty"`
}

func (Member) TableName() string {
	return "members"
}

// ActiveMembers is a GORM scope limiting member queries to formal memberships: directly added
// members (NULL invitation status) or accepted invitations. Pending, expired and cancelled
// invitation rows never grant membership.
func ActiveMembers(db *gorm.DB) *gorm.DB {
	return db.Where("(members.invitation_status IS NULL OR members.invitation_status = ?)", InvitationAccepted)
}

func init() {
	dbs.AutoMigrate(&Member{})
	dbs.AutoUpgrade("004_member_partial_unique", func(db *gorm.DB) error {
		return db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS uq_member_user_org_active
			ON members (user_id, org_id)
			WHERE deleted_at IS NULL
		`).Error
	})
}
