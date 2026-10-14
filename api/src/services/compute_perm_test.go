/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Alignment 02 (test-items/alignment/02-虚拟机生命周期与存储.md): writes on instances, images and interfaces need
// the writer role of the owning organization, and power actions are refused while another domain or a node
// script owns the disks. The refusals come before any statement and any command.

import (
	"testing"

	. "api/src/common"
	"api/src/model"
)

// V1, V2: readers, and writers of another organization, are refused
func TestComputeWritesNeedWriter(t *testing.T) {
	cland := startFakeCland(t)
	const org = 5
	for _, m := range []*MemberShip{member(org, model.OrgReader), member(org+1, model.OrgWriter)} {
		inst := &model.Instance{Model: model.Model{ID: 41}, Owner: org, Hostname: "vm", Status: model.InstanceStatusRescuing, Hyper: 3}

		ctx, touched := noDBContext(t, m)
		refusedEarly(t, "rescue", instanceAdmin.Rescue(ctx, &model.Instance{Model: model.Model{ID: 41}, Owner: org, Status: model.InstanceStatusShutoff}, nil, "pw"), touched, cland)
		refusedEarly(t, "end rescue", instanceAdmin.EndRescue(ctx, inst), touched, cland)

		_, err := InterfaceAdmin.Create(ctx, inst, "", "", 0, 0, false, nil, nil, 0)
		refusedEarly(t, "create interface", err, touched, cland)
		_, err = InterfaceAdmin.Update(ctx, inst, &model.Interface{Model: model.Model{ID: 9}, Name: "eth1"}, "eth1", 0, 0, true, nil, nil, nil, 0, nil)
		refusedEarly(t, "update interface", err, touched, cland)
	}

	// Creating needs the writer role in the caller's own organization
	ctx, touched := noDBContext(t, member(org, model.OrgReader))
	_, err := instanceAdmin.Create(ctx, 1, "vm-reader", "", "", "", "", &model.Image{Status: "available"}, &model.Zone{},
		0, &InterfaceInfo{}, nil, nil, "", 0, -1, 1, 1024, 10, false, nil, nil, nil)
	refusedEarly(t, "create instance", err, touched, cland)
	_, err = ImageAdmin.Create(ctx, "linux", "img-reader", "24.04", "kvm-x86_64", "ubuntu", "https://example.com/i.qcow2",
		"x86_64", "bios", false, 0, "", nil, "linux")
	refusedEarly(t, "create image", err, touched, cland)
}

// V4: power actions are refused while rescuing, reinstalling, resizing or migrating, before the database
func TestPowerActionsRefusedWhileDisksBusy(t *testing.T) {
	cland := startFakeCland(t)
	for _, status := range []model.InstanceStatus{model.InstanceStatusRescuing, model.InstanceStatusReinstalling,
		model.InstanceStatusResizing, model.InstanceStatusMigrating} {
		ctx, touched := noDBContext(t, member(5, model.OrgWriter))
		inst := &model.Instance{Model: model.Model{ID: 42}, Owner: 5, Hostname: "vm", Status: status, Hyper: 3}
		for _, action := range []PowerAction{"start", "stop", "hard_stop", "restart"} {
			if err := instanceAdmin.Update(ctx, inst, inst.Hostname, action, false); errCode(err) != ErrInstanceInvalidState {
				t.Errorf("%s while %s: got %v, want invalid state", action, status, err)
			}
		}
		if n := *touched; n != 0 {
			t.Errorf("%s: %d statements ran before the refusal", status, n)
		}
		if cmds := cland.take(); len(cmds) > 0 {
			t.Errorf("%s: commands sent: %v", status, cmds)
		}
	}
}

// V4: rescue, reinstall and resize take over the disks, so each is refused while another one, or a migration,
// owns them
func TestDiskOperationsExcludeEachOther(t *testing.T) {
	cland := startFakeCland(t)
	busy := []model.InstanceStatus{model.InstanceStatusRescuing, model.InstanceStatusReinstalling,
		model.InstanceStatusResizing, model.InstanceStatusMigrating}
	for _, status := range busy {
		ctx, touched := noDBContext(t, member(5, model.OrgWriter))
		inst := func() *model.Instance {
			return &model.Instance{Model: model.Model{ID: 43}, Owner: 5, Hostname: "vm", Status: status, Hyper: 3,
				Volumes: []*model.Volume{{Model: model.Model{ID: 7}, Booting: true}}}
		}
		if status != model.InstanceStatusRescuing {
			if err := instanceAdmin.Rescue(ctx, inst(), nil, "pw"); errCode(err) != ErrInstanceInvalidState {
				t.Errorf("rescue while %s: got %v", status, err)
			}
		}
		if err := instanceAdmin.Resize(ctx, inst(), 2, 2048); errCode(err) != ErrInstanceInvalidState {
			t.Errorf("resize while %s: got %v", status, err)
		}
		if err := instanceAdmin.Reinstall(ctx, inst(), &model.Image{}, "pw", nil, 1, 1024, 10, 0); errCode(err) != ErrInstanceInvalidState {
			t.Errorf("reinstall while %s: got %v", status, err)
		}
		if n := *touched; n != 0 {
			t.Errorf("%s: %d statements ran before the refusal", status, n)
		}
		if cmds := cland.take(); len(cmds) > 0 {
			t.Errorf("%s: commands sent: %v", status, cmds)
		}
	}
}

// A stuck instance is recovered by a system admin's forced power action; nobody else may force, and force
// needs a power action
func TestForcedPowerActionOnlyForSystemAdmins(t *testing.T) {
	cland := startFakeCland(t)
	inst := &model.Instance{Model: model.Model{ID: 45}, Owner: 5, Hostname: "vm", Status: model.InstanceStatusResizing, Hyper: 3}
	for _, m := range []*MemberShip{member(5, model.OrgWriter), member(5, model.OrgAdmin)} {
		ctx, touched := noDBContext(t, m)
		refusedEarly(t, "force by an org member", instanceAdmin.Update(ctx, inst, inst.Hostname, "start", true), touched, cland)
	}
	admin := &MemberShip{UserID: 1, OrgID: 5, OrgRole: model.OrgAdmin, SystemRole: model.SystemAdmin}
	ctx, touched := noDBContext(t, admin)
	refusedEarly(t, "force without a power action", instanceAdmin.Update(ctx, inst, "renamed", "", true), touched, cland)
	// Forced, the busy state no longer refuses it (the dry-run database lets it through to the command)
	if err := instanceAdmin.Update(ctx, inst, inst.Hostname, "start", true); errCode(err) == ErrInstanceInvalidState || errCode(err) == ErrPermissionDenied {
		t.Fatalf("forced start by a system admin: got %v", err)
	}
}

// Readers are refused before the state is looked at: a busy instance does not tell them its state
func TestBusyStateNotShownToReaders(t *testing.T) {
	cland := startFakeCland(t)
	ctx, touched := noDBContext(t, member(5, model.OrgReader))
	inst := &model.Instance{Model: model.Model{ID: 44}, Owner: 5, Hostname: "vm", Status: model.InstanceStatusRescuing, Hyper: 3,
		Volumes: []*model.Volume{{Model: model.Model{ID: 8}, Booting: true}}}
	refusedEarly(t, "power action", instanceAdmin.Update(ctx, inst, inst.Hostname, "start", false), touched, cland)
	refusedEarly(t, "resize", instanceAdmin.Resize(ctx, inst, 2, 2048), touched, cland)
	refusedEarly(t, "reinstall", instanceAdmin.Reinstall(ctx, inst, &model.Image{}, "pw", nil, 1, 1024, 10, 0), touched, cland)
}
