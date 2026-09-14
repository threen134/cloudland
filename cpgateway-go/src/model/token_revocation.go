package model

import (
	"time"

	"cpgateway-go/src/dbs"
)

type TokenRevocation struct {
	JTI       string    `gorm:"type:varchar(64);primaryKey" json:"jti"`
	ExpiresAt time.Time `gorm:"not null" json:"expires_at"`
}

func (TokenRevocation) TableName() string {
	return "token_revocations"
}

func init() {
	dbs.AutoMigrate(&TokenRevocation{})
}
