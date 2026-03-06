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
	"strconv"
	"testing"

	. "web/src/common"
	"web/src/model"
)

func TestUserAdminCreate(t *testing.T) {
	ctx := context.Background()
	DB().Where("1=1").Delete(&model.User{})
	defer DB().Where("1=1").Delete(&model.User{}) // delete all
	username := "admin"
	password := "admin"
	user, err := userAdmin.Create(ctx, username, password, "")
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
	username := "admin"
	password := "admin"
	user, err := userAdmin.Create(ctx, username, password, "")
	if err != nil {
		t.Fatal(err)
	}
	userID := user.ID
	if userID == 0 {
		t.Fatal(user)
	}
	user, err = userAdmin.Validate(ctx, username, password)
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
	defer DB().Where("1=1").Delete(&model.User{})            // delete all users
	defer DB().Where("1=1").Delete(&model.Organization{})    // delete all orgs
	user, err := userAdmin.Create(ctx, "admin", "admin", "") // create admin user
	if err != nil {
		t.Fatal(err)
	}

	_, err = orgAdmin.Create(ctx, "admin", strconv.FormatInt(user.ID, 10), "")
	if err != nil {
		t.Fatal(err)
	}
	oid, role, accessToken, _, _, err := userAdmin.AccessToken(ctx, user.ID, "admin", "admin")
	if err != nil || oid == 0 || role == model.None {
		t.Fatal(err, oid, role)
	}
	t.Log(accessToken)
}
