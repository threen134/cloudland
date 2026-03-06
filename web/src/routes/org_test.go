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
	"strconv"
	"testing"

	. "web/src/common"
	"web/src/model"
)

func TestRole(t *testing.T) {
	member := &model.Member{}
	role := fmt.Sprint(member.Role)
	if role != "None" {
		t.Fatal(role)
	}
	member.Role = model.Owner
	role = fmt.Sprint(member.Role)
	if role != "Owner" {
		t.Fatal(role)
	}
}

func TestOrgCreate(t *testing.T) {
	ctx := context.Background()
	DB().Where("1=1").Delete(&model.User{})
	DB().Where("1=1").Delete(&model.Organization{})
	username := "admin"
	password := "admin"
	admin, err := userAdmin.Create(ctx, username, password, "")
	if err != nil {
		t.Fatal(err)
	}
	owner := admin.ID
	defer DB().Where("1=1").Delete(&model.User{})
	defer DB().Where("1=1").Delete(&model.Organization{})
	org, err := orgAdmin.Create(ctx, "admin", strconv.FormatInt(owner, 10), "")
	if err != nil {
		t.Fatal(err)
	}
	orgID := org.ID
	db := DB()
	org = &model.Organization{Model: model.Model{ID: orgID}}
	if err = db.Preload("Members.User").Take(org).Error; err != nil {
		t.Fatal(err)
	}
	members := org.Members
	if len(members) != 1 {
		t.Fatal(members)
	}
	member := members[0]
	if member.Role != model.Owner {
		t.Fatal(member)
	}
	if member.UserName != "admin" {
		t.Fatal(member)
	}
}
