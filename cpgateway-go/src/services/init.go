package services

import (
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"

	"cpgateway-go/src/common"
	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
)

// Init ensures the root superuser, the "admin" system org and the root membership exist.
func Init() {
	db := dbs.DB()

	email := viper.GetString("superuser.email")
	username := viper.GetString("superuser.username")
	password := viper.GetString("superuser.password")
	if email == "" || username == "" || password == "" {
		log.Warn("Superuser config incomplete, skipping initialization")
		return
	}

	var root model.User
	if err := db.Where("username = ?", username).First(&root).Error; err != nil {
		log.Infof("Creating root superuser: %s", username)
		hashed, err := common.HashPassword(password)
		if err != nil {
			log.Errorf("Failed to hash superuser password: %v", err)
			return
		}
		root = model.User{
			Email:          email,
			Username:       username,
			HashedPassword: hashed,
			Language:       "en",
			IsActive:       true,
			IsSuperuser:    true,
			SystemRole:     model.SystemAdmin,
			Status:         model.UserActive,
		}
		if err := db.Create(&root).Error; err != nil {
			log.Errorf("Failed to create root superuser: %v", err)
			return
		}
		log.Info("Root superuser created successfully")
	} else {
		if root.SystemRole != model.SystemAdmin {
			db.Model(&root).Updates(map[string]interface{}{
				"system_role": model.SystemAdmin,
				"status":      model.UserActive,
			})
		}
		log.Info("Root superuser already exists, skipping creation")
	}

	var adminOrg model.Organization
	if err := db.Where("slug = ?", "admin").First(&adminOrg).Error; err != nil {
		adminOrg = model.Organization{
			Name:        "Admin",
			Slug:        "admin",
			OrgType:     model.OrgSystem,
			Status:      model.OrgActive,
			OwnerUserID: root.ID,
		}
		if err := db.Create(&adminOrg).Error; err != nil {
			log.Errorf("Failed to create admin organization: %v", err)
			return
		}
		log.Infof("Default 'Admin' organization created (id=%d)", adminOrg.ID)
	}

	var member model.Member
	if err := db.Where("user_id = ? AND org_id = ?", root.ID, adminOrg.ID).First(&member).Error; err != nil {
		if err := db.Create(&model.Member{UserID: root.ID, OrgID: adminOrg.ID, OrgRole: model.OrgRoleAdmin}).Error; err != nil {
			log.Errorf("Failed to add admin user to 'Admin' organization: %v", err)
			return
		}
		log.Info("Admin user added to 'Admin' organization as ADMIN")
	}
}
