/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Alignment 03 on PostgreSQL (CLAPI_TEST_DB_URI, see placement_pg_test.go).

import (
	"testing"

	. "api/src/common"
	"api/src/model"
)

// N3: an IP group still named by a subnet or a floating IP is not deleted
func TestIpGroupDeleteInUsePG(t *testing.T) {
	_, db := pgContext(t, 1)
	org := p2Org(9303)
	writer := asMember(org, model.OrgWriter)
	for _, user := range []string{"subnet", "floating"} {
		group := &model.IpGroup{Owner: org, Name: p2Name("ipg-" + user), Type: "resource"}
		must(t, db.Create(group).Error)
		if user == "subnet" {
			must(t, db.Create(&model.Subnet{Owner: org, Name: p2Name("ipg-sn"), Network: "192.168.78.0/24", Gateway: "192.168.78.1/24",
				Vlan: 5078, Type: "internal", GroupID: group.ID}).Error)
		} else {
			must(t, db.Create(&model.FloatingIp{Owner: org, Name: p2Name("ipg-fip"), FipAddress: "52.117.101.157/28", GroupID: group.ID,
				Type: string(PublicFloating)}).Error)
		}
		if err := ipGroupAdmin.Delete(writer, group); errCode(err) != ErrIpGroupInUse {
			t.Fatalf("%s still uses the group: got %v, want in use", user, err)
		}
		stored := &model.IpGroup{}
		must(t, db.Where("id = ?", group.ID).Take(stored).Error) // still there, not soft-deleted
	}
	unused := &model.IpGroup{Owner: org, Name: p2Name("ipg-free"), Type: "resource"}
	must(t, db.Create(unused).Error)
	must(t, ipGroupAdmin.Delete(writer, unused))
}

// Review: a floating IP of another organization attached to this organization's instance (a system admin did
// it) is detached and released by the delete that cascades from the owner's side (Delete), while the
// standalone DELETE /floating_ips/:id (DeleteOwned) stays with the floating IP's organization
func TestFloatingIpCascadeDeletePG(t *testing.T) {
	cland := startFakeCland(t)
	_, db := pgContext(t, 1)
	org := p2Org(9304)
	fip, _ := fipFixture(t, org)
	must(t, db.Model(&model.FloatingIp{}).Where("id = ?", fip.ID).Update("owner", org+1).Error)
	writer := asMember(org, model.OrgWriter)
	ownerSide := asMember(org+1, model.OrgReader)
	if err := FloatingIpAdmin.DeleteOwned(writer, loadFip(t, ownerSide, fip.UUID)); errCode(err) != ErrPermissionDenied {
		t.Fatalf("standalone delete by the instance's organization: got %v, want permission denied", err)
	}
	cland.take()
	// loaded through its own organization: the instance side reaches it through the cascade, not by listing
	must(t, FloatingIpAdmin.Delete(writer, loadFip(t, ownerSide, fip.UUID)))
	if n := db.Where("id = ?", fip.ID).Take(&model.FloatingIp{}).RowsAffected; n != 0 {
		t.Fatal("the floating ip is still there after the cascading delete")
	}
	cland.take()
}
