/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

History:
   Date     Who ID    Description
   -------- --- ---   -----------
   01/13/19 nanjj  Initial code

*/

package services

import (
	"context"
	"fmt"
	"time"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"github.com/jinzhu/gorm"
	"github.com/spf13/viper"
)

func adminPassword() (password string) {
	logger.Infof("ENTER adminPassword")
	defer func() {
		logger.Infof("EXIT adminPassword: password=%s", password)
	}()
	time.Sleep(time.Second * 5)
	password = viper.GetString("admin.password")
	if password == "" {
		password = "passw0rd"
	}
	return
}

func adminEmail() (email string) {
	logger.Infof("ENTER adminEmail")
	defer func() {
		logger.Infof("EXIT adminEmail: email=%s", email)
	}()
	email = viper.GetString("admin.email")
	if email == "" {
		email = "admin@cloudland.local"
	}
	return email
}

// reservedSlugs — system reserved slugs that cannot be used by team orgs
var reservedSlugs = map[string]struct{}{
	"admin":   {},
	"system":  {},
	"root":    {},
	"api":     {},
	"auth":    {},
	"health":  {},
	"metrics": {},
}

// AdminInit creates the initial SystemAdmin user, system org, and default security group.
// It is idempotent — safe to call on every service startup.
func AdminInit() {
	logger.Infof("ENTER AdminInit")
	defer logger.Info("EXIT AdminInit")
	var adminUser *model.User
	var adminOrg *model.Organization
	ctx := context.Background()

	dbs.AutoUpgrade("01-admin-upgrade", func(db *gorm.DB) (err error) {
		// Step 1: Check if SystemAdmin user exists
		adminUser = &model.User{}
		if err = db.Where("system_role = ?", model.SystemAdmin).Take(adminUser).Error; err != nil {
			// No SystemAdmin found, create one
			dbFunc := DB
			defer func() { DB = dbFunc }()
			DB = func() *gorm.DB { return db }

			password := adminPassword()
			email := adminEmail()
			hash, hashErr := (&UserAdmin{}).GenerateFromPassword(password)
			if hashErr != nil {
				return hashErr
			}
			adminUser = &model.User{
				Email:      email,
				Password:   hash,
				SystemRole: model.SystemAdmin,
				Status:     model.UserActive,
			}
			if err = db.Create(adminUser).Error; err != nil {
				return
			}
			logger.Infof("Created admin user with ID: %d, Email: %s", adminUser.ID, adminUser.Email)
		}

		// Step 2: Check if system org exists (inside migration for idempotency)
		adminOrg = &model.Organization{}
		if err = db.Where("org_type = ?", model.OrgTypeSystem).Take(adminOrg).Error; err != nil {
			// Create system org using FirstOrCreate to handle race conditions
			adminOrg = &model.Organization{
				Name:        "admin",
				Slug:        "admin",
				OrgType:     model.OrgTypeSystem,
				OwnerUserID: adminUser.ID,
			}
			if err = db.Where(model.Organization{Name: "admin", OrgType: model.OrgTypeSystem}).
				FirstOrCreate(adminOrg).Error; err != nil {
				logger.Error("Failed to create admin org", err)
				return err
			}
			logger.Infof("Created admin org with ID: %d", adminOrg.ID)
		}

		// Step 3: Check if admin member exists (idempotent via FirstOrCreate)
		member := &model.Member{}
		if err = db.Where("user_id = ? AND org_id = ?", adminUser.ID, adminOrg.ID).Take(member).Error; err != nil {
			member = &model.Member{
				UserID:  adminUser.ID,
				OrgID:   adminOrg.ID,
				OrgRole: model.OrgAdmin,
			}
			if err = db.Where(model.Member{UserID: adminUser.ID, OrgID: adminOrg.ID}).
				FirstOrCreate(member).Error; err != nil {
				logger.Error("Failed to create admin member", err)
				return err
			}
			logger.Infof("Created admin member for user %d in org %d", adminUser.ID, adminOrg.ID)
		}
		return nil
	})

	// Ensure adminUser and adminOrg are populated even if admin already existed
	db := DB()
	if adminUser == nil || adminUser.ID == 0 {
		adminUser = &model.User{}
		if err := db.Where("system_role = ?", model.SystemAdmin).First(adminUser).Error; err != nil {
			logger.Error("Failed to query admin user after init", err)
			return
		}
	}
	if adminOrg == nil || adminOrg.ID == 0 {
		adminOrg = &model.Organization{}
		if err := db.Where("org_type = ?", model.OrgTypeSystem).First(adminOrg).Error; err != nil {
			logger.Error("Failed to query admin org after init", err)
			return
		}
	}

	// Step 4: Create default security group if not exists
	_, err := (&SecgroupAdmin{}).GetSecgroupByName(ctx, SystemDefaultSGName)
	if err != nil {
		memberShip := &MemberShip{
			UserID:     adminUser.ID,
			UserEmail:  adminUser.Email,
			SystemRole: model.SystemAdmin,
			OrgID:      adminOrg.ID,
			OrgName:    adminOrg.Name,
			OrgRole:    model.OrgAdmin,
			IsOrgOwner: true,
		}
		ctx = memberShip.SetContext(ctx)
		sgName := fmt.Sprintf("%s-%d", SystemDefaultSGName, adminOrg.ID)
		_ = sgName // unused for now, use the constant name
		_, _ = (&SecgroupAdmin{}).Create(ctx, SystemDefaultSGName, true, nil)
	}
}
