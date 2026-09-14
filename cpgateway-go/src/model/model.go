package model

import (
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Model struct {
	ID        int64          `gorm:"primaryKey" json:"id"`
	UUID      string         `gorm:"type:varchar(36);uniqueIndex" json:"uuid"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}

func (m *Model) BeforeCreate(tx *gorm.DB) error {
	if m.UUID == "" {
		m.UUID = uuid.New().String()
		log.Debugf("Generated UUID: %s", m.UUID)
	}
	return nil
}

// generateUUID is a helper for models that don't embed Model
func generateUUID() string {
	return uuid.New().String()
}
