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
	"fmt"
	"testing"

	. "web/src/common"
	"web/src/model"
)

func TestOrgRole(t *testing.T) {
	member := &model.Member{}
	role := fmt.Sprint(member.OrgRole)
	if role != "OrgNone" {
		t.Fatal(role)
	}
	member.OrgRole = model.OrgAdmin
	role = fmt.Sprint(member.OrgRole)
	if role != "OrgAdmin" {
		t.Fatal(role)
	}
}

func TestOrgCreate(t *testing.T) {
	ctx := context.Background()
	DB().Where("1=1").Delete(&model.User{})
	DB().Where("1=1").Delete(&model.Organization{})
	DB().Where("1=1").Delete(&model.Member{})
	email := "admin@test.local"
	password := "admin"
	admin, err := userAdmin.Create(ctx, email, password, "")
	if err != nil {
		t.Fatal(err)
	}
	ownerID := admin.ID
	defer DB().Where("1=1").Delete(&model.User{})
	defer DB().Where("1=1").Delete(&model.Organization{})
	defer DB().Where("1=1").Delete(&model.Member{})
	org, err := orgAdmin.Create(ctx, "admin", ownerID, "")
	if err != nil {
		t.Fatal(err)
	}
	orgID := org.ID
	db := DB()
	org = &model.Organization{Model: model.Model{ID: orgID}}
	if err = db.Preload("Members").Take(org).Error; err != nil {
		t.Fatal(err)
	}
	members := org.Members
	if len(members) != 1 {
		t.Fatal(members)
	}
	member := members[0]
	if member.OrgRole != model.OrgAdmin {
		t.Fatal(member)
	}
	if member.UserID != ownerID {
		t.Fatal(member)
	}
}
