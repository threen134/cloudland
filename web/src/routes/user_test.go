/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

History:
   Date     Who ID    Description
   -------- --- ---   -----------
   01/13/19 nanjj  Initial code

*/

package routes

import (
	"context"
	"testing"

	. "web/src/common"
	"web/src/model"
)

func TestUserAdminCreate(t *testing.T) {
	ctx := context.Background()
	DB().Where("1=1").Delete(&model.User{})
	defer DB().Where("1=1").Delete(&model.User{}) // delete all
	email := "admin@test.local"
	password := "admin"
	user, err := userAdmin.Create(ctx, email, password, "")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID == 0 {
		t.Fatal(user)
	}
	if user.Password == password {
		t.Fatal(user)
	}
}

func TestUserAdminValidate(t *testing.T) {
	ctx := context.Background()
	DB().Where("1=1").Delete(&model.User{})
	defer DB().Where("1=1").Delete(&model.User{}) // delete all
	email := "admin@test.local"
	password := "admin"
	user, err := userAdmin.Create(ctx, email, password, "")
	if err != nil {
		t.Fatal(err)
	}
	userID := user.ID
	if userID == 0 {
		t.Fatal(user)
	}
	user, err = userAdmin.Validate(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != userID {
		t.Fatal(user)
	}
}

func TestUserAdminAccessToken(t *testing.T) {
	ctx := context.Background()
	DB().Where("1=1").Delete(&model.User{})
	DB().Where("1=1").Delete(&model.Organization{})
	DB().Where("1=1").Delete(&model.Member{})
	defer DB().Where("1=1").Delete(&model.User{})         // delete all users
	defer DB().Where("1=1").Delete(&model.Organization{}) // delete all orgs
	defer DB().Where("1=1").Delete(&model.Member{})       // delete all members

	// Use CreateWithOrg for atomic user + org creation
	user, _, err := userAdmin.CreateWithOrg(ctx, "admin@test.local", "admin", "admin", "")
	if err != nil {
		t.Fatal(err)
	}

	oid, _, orgRole, _, accessToken, _, _, err := userAdmin.AccessToken(ctx, user.ID)
	if err != nil || oid == 0 || orgRole == model.OrgNone {
		t.Fatal(err, oid, orgRole)
	}
	t.Log(accessToken)
}
