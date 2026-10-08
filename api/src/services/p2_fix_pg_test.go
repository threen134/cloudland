/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// The 2026-09-28 regression fixes on PostgreSQL (CLAPI_TEST_DB_URI, see placement_pg_test.go): the checks that need
// rows, and the commands the allowed requests send to the fake cland of p2_fix_test.go. Every test uses its own
// organization IDs, host IDs and names, so it shares the database with the other PG tests.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

// p2Org makes an organization ID unique to this run for the tests that create named rows
func p2Org(base int64) int64 {
	return base*1000 + time.Now().Unix()%1000
}

func p2Name(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func asMember(org int64, role model.OrgRole) context.Context {
	return member(org, role).SetContext(context.Background())
}

// D14: the public address of a VPN gateway is a floating IP of the router, so the floating IP check answered first
// with 400 131307 instead of 409 132033
func TestRouterDeleteWithVpnGatewayPG(t *testing.T) {
	_, db := pgContext(t, 1)
	cland := startFakeCland(t)
	org := p2Org(9201)
	router := &model.Router{Owner: org, Name: p2Name("p2-vpc"), Status: "available"}
	must(t, db.Create(router).Error)
	fip := &model.FloatingIp{Owner: org, Name: p2Name("p2-gw-ip"), RouterID: router.ID, Type: "vpngateway"}
	must(t, db.Create(fip).Error)
	gw := &model.VpnGateway{Owner: org, Name: p2Name("p2-gw"), RouterID: router.ID, Status: model.VpnGatewayStatusAvailable}
	must(t, db.Create(gw).Error)

	err := routerAdmin.Delete(asMember(org, model.OrgWriter), router)
	if errCode(err) != ErrRouterHasVpnGateway {
		t.Fatalf("got %v, want the VPN gateway error", err)
	}
	if status := ErrRouterHasVpnGateway.ToHTTPStatus(); status != http.StatusConflict {
		t.Errorf("VPN gateway error maps to %d, want 409", status)
	}
	if cmds := cland.take(); len(cmds) > 0 {
		t.Errorf("commands sent for a refused delete: %v", cmds)
	}
	// Without the gateway the floating IP still keeps the VPC
	must(t, db.Delete(gw).Error)
	if err = routerAdmin.Delete(asMember(org, model.OrgWriter), router); errCode(err) != ErrRouterHasFloatingIPs {
		t.Fatalf("got %v, want the floating ip error", err)
	}
	must(t, db.Delete(fip).Error)
	must(t, db.Delete(router).Error)
}

// D5 / NET-08: a reader could rename a VPC
func TestRouterUpdateNeedsWriterPG(t *testing.T) {
	_, db := pgContext(t, 1)
	org := p2Org(9202)
	router := &model.Router{Owner: org, Name: p2Name("p2-vpc"), Status: "available"}
	must(t, db.Create(router).Error)
	desc := "changed"
	for _, ctx := range []context.Context{asMember(org, model.OrgReader), asMember(org+1, model.OrgWriter)} {
		if _, err := routerAdmin.Update(ctx, router.ID, "p2-renamed", &desc, 0); errCode(err) != ErrPermissionDenied {
			t.Errorf("got %v, want permission denied", err)
		}
	}
	stored := &model.Router{}
	must(t, db.Where("id = ?", router.ID).Take(stored).Error)
	if stored.Name != router.Name || stored.Description != "" {
		t.Fatalf("refused updates changed the VPC: %q %q", stored.Name, stored.Description)
	}
	newName := p2Name("p2-renamed")
	if _, err := routerAdmin.Update(asMember(org, model.OrgWriter), router.ID, newName, &desc, 0); err != nil {
		t.Fatal(err)
	}
	must(t, db.Where("id = ?", router.ID).Take(stored).Error)
	if stored.Name != newName || stored.Description != desc {
		t.Fatalf("writer update not stored: %q %q", stored.Name, stored.Description)
	}
	must(t, db.Delete(stored).Error)
}

// D5 / SG-08: security group updates need a writer; making a group the default one switches the default of its
// own VPC or organization, and no longer clears is_default of whatever group comes first in the table
func TestSecgroupUpdatePG(t *testing.T) {
	_, db := pgContext(t, 1)
	org := p2Org(9203)
	orgRow := &model.Organization{Name: p2Name("p2-org"), Slug: p2Name("p2-org"), OwnerUserID: 1}
	must(t, db.Create(orgRow).Error)
	org = orgRow.ID
	router := &model.Router{Owner: org, Name: p2Name("p2-vpc"), Status: "available"}
	must(t, db.Create(router).Error)
	native := &model.SecurityGroup{Owner: org, Name: p2Name("p2-native"), IsDefault: true, RouterID: router.ID}
	must(t, db.Create(native).Error)
	must(t, db.Model(router).Update("default_sg", native.ID).Error)
	other := &model.SecurityGroup{Owner: org, Name: p2Name("p2-sg"), RouterID: router.ID}
	must(t, db.Create(other).Error)
	load := func(ctx context.Context, id string) *model.SecurityGroup {
		sg, err := secgroupAdmin.GetSecgroupByUUID(ctx, id)
		must(t, err)
		return sg
	}
	yes, desc := true, "p2"

	reader := asMember(org, model.OrgReader)
	if err := secgroupAdmin.Update(reader, load(reader, other.UUID), nil, &desc, &yes); errCode(err) != ErrPermissionDenied {
		t.Fatalf("reader: got %v", err)
	}
	stored := &model.SecurityGroup{}
	must(t, db.Where("id = ?", other.ID).Take(stored).Error)
	if stored.IsDefault || stored.Description != "" {
		t.Fatalf("refused update stored: %+v", stored)
	}

	writer := asMember(org, model.OrgWriter)
	must(t, secgroupAdmin.Update(writer, load(writer, other.UUID), nil, &desc, &yes))
	must(t, db.Where("id = ?", router.ID).Take(router).Error)
	must(t, db.Where("id = ?", native.ID).Take(native).Error)
	must(t, db.Where("id = ?", other.ID).Take(stored).Error)
	if router.DefaultSG != other.ID || native.IsDefault || !stored.IsDefault || stored.Description != desc {
		t.Fatalf("switch in the VPC: default_sg %d (want %d), native default %v, new default %v, description %q",
			router.DefaultSG, other.ID, native.IsDefault, stored.IsDefault, stored.Description)
	}

	// A group outside any VPC becomes the default of the organization owning it: org admins only, and a system
	// admin acting from another organization switches the owner's default, not their own
	orgSg := &model.SecurityGroup{Owner: org, Name: p2Name("p2-orgsg")}
	must(t, db.Create(orgSg).Error)
	if err := secgroupAdmin.Update(writer, load(writer, orgSg.UUID), nil, nil, &yes); errCode(err) != ErrPermissionDenied {
		t.Fatalf("writer switching the organization default: got %v", err)
	}
	admin := (&MemberShip{UserID: 1, OrgID: 1, OrgRole: model.OrgAdmin, SystemRole: model.SystemAdmin, AllOrgs: true}).SetContext(context.Background())
	var before int64
	must(t, db.Model(&model.Organization{}).Where("id = ?", 1).Select("default_sg").Scan(&before).Error)
	must(t, secgroupAdmin.Update(admin, load(admin, orgSg.UUID), nil, nil, &yes))
	must(t, db.Where("id = ?", org).Take(orgRow).Error)
	if orgRow.DefaultSG != orgSg.ID {
		t.Fatalf("organization default is %d, want %d", orgRow.DefaultSG, orgSg.ID)
	}
	var after int64
	must(t, db.Model(&model.Organization{}).Where("id = ?", 1).Select("default_sg").Scan(&after).Error)
	if after != before {
		t.Fatalf("the admin's own organization default changed from %d to %d", before, after)
	}
}

// fipFixture is a floating IP of org attached to a running instance on host 9204 in a VPC
func fipFixture(t *testing.T, org int64) (*model.FloatingIp, *model.Instance) {
	_, db := pgContext(t, 1)
	stamp := time.Now().UnixNano()
	router := &model.Router{Owner: org, Name: p2Name("p2-vpc"), Status: "available"}
	must(t, db.Create(router).Error)
	pub := &model.Subnet{Owner: org, Name: p2Name("p2-pub"), Network: "52.117.101.144/28", Gateway: "52.117.101.145/28", Vlan: 756, Type: "public"}
	must(t, db.Create(pub).Error)
	internal := &model.Subnet{Owner: org, Name: p2Name("p2-int"), Network: "192.168.77.0/24", Gateway: "192.168.77.1/24", Vlan: 5077, Type: "internal", RouterID: router.ID}
	must(t, db.Create(internal).Error)
	inst := &model.Instance{Owner: org, Hostname: p2Name("p2-vm"), Status: model.InstanceStatusRunning, Hyper: 9204, RouterID: router.ID}
	must(t, db.Create(inst).Error)
	primary := &model.Interface{Owner: org, Name: "eth0", Instance: inst.ID, PrimaryIf: true, Hyper: 9204}
	must(t, db.Create(primary).Error)
	must(t, db.Create(&model.Address{Owner: org, Address: fmt.Sprintf("192.168.77.%d/24", 2+stamp%200), SubnetID: internal.ID, Interface: primary.ID, Allocated: true}).Error)
	fip := &model.FloatingIp{Owner: org, Name: p2Name("p2-fip"), FipAddress: "52.117.101.158/28", IntAddress: "192.168.77.2/24",
		InstanceID: inst.ID, RouterID: router.ID, Type: string(PublicFloating), SubnetID: pub.ID, Inbound: 10, Outbound: 10}
	must(t, db.Create(fip).Error)
	fipIface := &model.Interface{Owner: org, Name: "fip", FloatingIp: fip.ID, Type: "floating"}
	must(t, db.Create(fipIface).Error)
	must(t, db.Create(&model.Address{Owner: org, Address: fmt.Sprintf("52.117.101.%d/28", 1000+stamp%1000), SubnetID: pub.ID, Interface: fipIface.ID, Allocated: true}).Error)
	return fip, inst
}

func loadFip(t *testing.T, ctx context.Context, uuid string) *model.FloatingIp {
	fip, err := FloatingIpAdmin.GetFloatingIpByUUID(ctx, uuid)
	must(t, err)
	return fip
}

// FIP-07: a PATCH changing only the bandwidth detached the floating IP and never stored or applied the new limits
func TestFloatingIpBandwidthOnlyPG(t *testing.T) {
	_, db := pgContext(t, 1)
	cland := startFakeCland(t)
	org := p2Org(9204)
	fip, inst := fipFixture(t, org)
	writer := asMember(org, model.OrgWriter)
	inbound, outbound := int32(100), int32(50)

	updated, err := FloatingIpAdmin.Update(writer, loadFip(t, writer, fip.UUID), &FloatingIpChange{Inbound: &inbound, Outbound: &outbound})
	must(t, err)
	stored := &model.FloatingIp{}
	must(t, db.Where("id = ?", fip.ID).Take(stored).Error)
	if stored.InstanceID != inst.ID || stored.Inbound != 100 || stored.Outbound != 50 || updated.Inbound != 100 || updated.InstanceID != inst.ID {
		t.Fatalf("stored instance %d inbound %d outbound %d; response instance %d inbound %d", stored.InstanceID, stored.Inbound,
			stored.Outbound, updated.InstanceID, updated.Inbound)
	}
	cmds := cland.take()
	if len(cmds) != 1 || !strings.HasPrefix(cmds[0], "inter=9204 | ") || strings.Contains(cmds[0], "clear_floating") ||
		strings.Contains(cmds[0], "create_floating") || !strings.Contains(cmds[0], "set_floating_bandwidth.sh") || !strings.HasSuffix(cmds[0], "'100' '50'") {
		t.Fatalf("commands %v", cmds)
	}

	// The same limits again: nothing to do on the host
	_, err = FloatingIpAdmin.Update(writer, loadFip(t, writer, fip.UUID), &FloatingIpChange{Inbound: &inbound})
	must(t, err)
	if cmds = cland.take(); len(cmds) != 0 {
		t.Fatalf("unchanged limits sent %v", cmds)
	}

	// A reader is refused before anything happens on the host
	reader := asMember(org, model.OrgReader)
	if _, err = FloatingIpAdmin.Update(reader, loadFip(t, reader, fip.UUID), &FloatingIpChange{Retarget: true}); errCode(err) != ErrPermissionDenied {
		t.Fatalf("reader detach: got %v", err)
	}
	must(t, db.Where("id = ?", fip.ID).Take(stored).Error)
	if cmds = cland.take(); len(cmds) != 0 || stored.InstanceID != inst.ID {
		t.Fatalf("reader detach: commands %v, instance %d", cmds, stored.InstanceID)
	}

	// "instance": null detaches
	_, err = FloatingIpAdmin.Update(writer, loadFip(t, writer, fip.UUID), &FloatingIpChange{Retarget: true})
	must(t, err)
	must(t, db.Where("id = ?", fip.ID).Take(stored).Error)
	if cmds = cland.take(); len(cmds) != 1 || !strings.Contains(cmds[0], "clear_floating.sh") || stored.InstanceID != 0 {
		t.Fatalf("detach: commands %v, instance %d", cmds, stored.InstanceID)
	}

	// Limits of a detached floating IP are only stored, and used by the next attach
	inbound = 200
	_, err = FloatingIpAdmin.Update(writer, loadFip(t, writer, fip.UUID), &FloatingIpChange{Inbound: &inbound})
	must(t, err)
	must(t, db.Where("id = ?", fip.ID).Take(stored).Error)
	if cmds = cland.take(); len(cmds) != 0 || stored.Inbound != 200 {
		t.Fatalf("detached: commands %v, inbound %d", cmds, stored.Inbound)
	}
}

// FAIL-5: an instance deleted while provisioning kept the room reserved for its boot disk for a day
func TestInstanceDeleteReleasesBootReservationPG(t *testing.T) {
	cland := startFakeCland(t)
	f := newPGFixture(t, p2Org(9205))
	inst := f.member(nil, "p2-stuck", -1, 0, model.InstanceStatusProvisioning, 2, 2048, 10, 2*time.Hour)
	boot := &model.Volume{}
	must(t, f.db.Where("instance_id = ? AND booting = ?", inst.ID, true).Take(boot).Error)
	reserve := func() {
		must(t, f.db.Create(&model.StorageReservation{Hostid: 9205, PoolID: f.builtin.ID, VolumeID: boot.ID, Kind: model.ReservationBoot,
			SizeGB: 10, ExpiresAt: time.Now().Add(24 * time.Hour)}).Error)
	}
	reserved := func() (n int64) {
		must(t, f.db.Model(&model.StorageReservation{}).Where("volume_id = ? AND kind = ?", boot.ID, model.ReservationBoot).Count(&n).Error)
		return
	}
	reserve()
	writer := asMember(f.org, model.OrgWriter)

	// The delete command can not be sent: the instance stays, and so does its reservation
	cland.setFailing(true)
	err := instanceAdmin.Delete(writer, inst)
	cland.setFailing(false)
	if err == nil {
		t.Fatal("delete succeeded without its command")
	}
	if n := reserved(); n != 1 {
		t.Fatalf("%d reservations after a failed delete, want 1", n)
	}
	cland.take()

	must(t, instanceAdmin.Delete(writer, inst))
	if n := reserved(); n != 0 {
		t.Fatalf("%d reservations after the delete, want 0", n)
	}
	cmds := cland.take()
	if len(cmds) != 1 || !strings.HasPrefix(cmds[0], "toall= | ") || !strings.Contains(cmds[0], fmt.Sprintf("clear_vm.sh '%d'", inst.ID)) {
		t.Fatalf("commands %v", cmds)
	}
}

// The stored root password used to change when the command was sent, so a guest that never took it (guest agent
// not running yet) left the wrong password in the database. It now waits aside until the node reports back
func TestSetUserPasswordWaitsForNodePG(t *testing.T) {
	_, db := pgContext(t, 1)
	cland := startFakeCland(t)
	org := p2Org(9206)
	image := &model.Image{Owner: org, Name: p2Name("p2-qa"), Status: "available", QAEnabled: true}
	must(t, db.Create(image).Error)
	inst := &model.Instance{Owner: org, Hostname: p2Name("p2-pw"), Status: model.InstanceStatusRunning, Hyper: 9206, ImageID: image.ID,
		RootPasswd: "old-pass-0"}
	must(t, db.Create(inst).Error)
	writer := asMember(org, model.OrgWriter)
	stored := func() (root, pending string) {
		row := &model.Instance{}
		must(t, db.Where("id = ?", inst.ID).Take(row).Error)
		return row.RootPasswd, row.PendingRootPasswd
	}
	expect := func(what, wantRoot, wantPending string) {
		t.Helper()
		if root, pending := stored(); root != wantRoot || pending != wantPending {
			t.Fatalf("%s: root %q pending %q, want %q %q", what, root, pending, wantRoot, wantPending)
		}
	}

	must(t, instanceAdmin.SetUserPassword(writer, inst.ID, "root", "new-pass-1"))
	expect("sent", "old-pass-0", "new-pass-1")
	if cmds := cland.take(); len(cmds) != 1 || !strings.HasPrefix(cmds[0], "inter=9206 | ") || !strings.Contains(cmds[0], "set_user_passwd.sh") {
		t.Fatalf("commands %v", cmds)
	}
	must(t, SettleUserPassword(context.Background(), inst.ID, false))
	expect("failed on the node", "old-pass-0", "")

	must(t, instanceAdmin.SetUserPassword(writer, inst.ID, "root", "new-pass-2"))
	must(t, SettleUserPassword(context.Background(), inst.ID, true))
	expect("confirmed", "new-pass-2", "")
	// cland retries a callback: the second one finds nothing pending
	must(t, SettleUserPassword(context.Background(), inst.ID, true))
	expect("confirmed twice", "new-pass-2", "")

	// Passwords of other users are not stored
	must(t, instanceAdmin.SetUserPassword(writer, inst.ID, "ubuntu", "user-pass"))
	must(t, SettleUserPassword(context.Background(), inst.ID, true))
	expect("other user", "new-pass-2", "")

	// Not sent at all: nothing is left pending
	cland.setFailing(true)
	if err := instanceAdmin.SetUserPassword(writer, inst.ID, "root", "new-pass-3"); err == nil {
		t.Fatal("no error when the command was not sent")
	}
	cland.setFailing(false)
	expect("not sent", "new-pass-2", "")

	if err := instanceAdmin.SetUserPassword(asMember(org, model.OrgReader), inst.ID, "root", "new-pass-4"); errCode(err) != ErrPermissionDenied {
		t.Fatalf("reader: got %v", err)
	}
	expect("reader", "new-pass-2", "")
	cland.take()
}

// A floating IP a system admin attached from another organization no longer makes the instance undeletable
func TestInstanceDeleteWithForeignFloatingIpPG(t *testing.T) {
	_, db := pgContext(t, 1)
	cland := startFakeCland(t)
	org := p2Org(9207)
	fip, inst := fipFixture(t, org)
	must(t, db.Model(&model.FloatingIp{}).Where("id = ?", fip.ID).Update("owner", org+1).Error)
	writer := asMember(org, model.OrgWriter)
	loaded, err := instanceAdmin.GetInstanceByUUID(writer, inst.UUID)
	must(t, err)
	must(t, instanceAdmin.Delete(writer, loaded))
	stored := &model.FloatingIp{}
	must(t, db.Where("id = ?", fip.ID).Take(stored).Error)
	cmds := cland.take()
	if stored.InstanceID != 0 || len(cmds) != 2 || !strings.Contains(cmds[0], "clear_floating.sh") || !strings.Contains(cmds[1], "clear_vm.sh") {
		t.Fatalf("floating ip instance %d, commands %v", stored.InstanceID, cmds)
	}
}
