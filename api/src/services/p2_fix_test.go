/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Permission and behaviour tests of the 2026-09-28 regression fixes (D4, D5, FIP-07, FAIL-7, VPN status_reason).
// They need no database: a refused request must be refused before it reads anything, which noDBContext detects,
// and before it sends a command to a host, which the fake cland detects. The paths that go further are covered
// against PostgreSQL in p2_fix_pg_test.go.

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	. "api/src/common"
	"api/src/model"
	pb "api/src/proto/cloudlandpb"

	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// fakeCland takes the commands clapi sends to the hosts and records them. While failing, it refuses them the way
// cland refuses a command without a target, which HyperExecute reports as an error
type fakeCland struct {
	pb.UnimplementedClandServiceServer
	mu       sync.Mutex
	commands []string
	failing  bool
}

func (f *fakeCland) Execute(_ context.Context, req *pb.ExecuteRequest) (*pb.ExecuteReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return &pb.ExecuteReply{Status: "error: no target node"}, nil
	}
	f.commands = append(f.commands, req.Control+" | "+req.Command)
	return &pb.ExecuteReply{Status: "ok"}, nil
}

func (f *fakeCland) setFailing(failing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing = failing
}

func (f *fakeCland) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmds := f.commands
	f.commands = nil
	return cmds
}

var (
	fakeClandOnce sync.Once
	theFakeCland  *fakeCland
)

// startFakeCland points HyperExecute at a local fake cland and forgets the commands it got so far. The gRPC client
// of clapi is made once per process, so this must run before anything else in the test binary sends a command.
// Outside the test that started it the fake refuses every command, as an unreachable cland would for the other tests
func startFakeCland(t *testing.T) *fakeCland {
	t.Helper()
	fakeClandOnce.Do(func() {
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := grpc.NewServer()
		theFakeCland = &fakeCland{}
		pb.RegisterClandServiceServer(server, theFakeCland)
		go server.Serve(lis)
		viper.Set("cland.endpoint", lis.Addr().String())
	})
	theFakeCland.setFailing(false)
	theFakeCland.take()
	t.Cleanup(func() { theFakeCland.setFailing(true) })
	return theFakeCland
}

// noDBContext returns a context acting as m whose database runs nothing and counts the statements it is given
func noDBContext(t *testing.T, m *MemberShip) (context.Context, *int32) {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=none dbname=none sslmode=disable"}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: gormlogger.Discard})
	must(t, err)
	var touched int32
	count := func(*gorm.DB) { atomic.AddInt32(&touched, 1) }
	cb := db.Callback()
	must(t, cb.Create().Before("gorm:create").Register("p2:create", count))
	must(t, cb.Query().Before("gorm:query").Register("p2:query", count))
	must(t, cb.Update().Before("gorm:update").Register("p2:update", count))
	must(t, cb.Delete().Before("gorm:delete").Register("p2:delete", count))
	must(t, cb.Row().Before("gorm:row").Register("p2:row", count))
	must(t, cb.Raw().Before("gorm:raw").Register("p2:raw", count))
	return SetContextDB(m.SetContext(context.Background()), db), &touched
}

func member(org int64, role model.OrgRole) *MemberShip {
	return &MemberShip{UserID: 7, UserName: "p2-member", OrgID: org, OrgRole: role}
}

// refusedEarly checks that err is a permission error given before any statement and any command
func refusedEarly(t *testing.T, what string, err error, touched *int32, cland *fakeCland) {
	t.Helper()
	if errCode(err) != ErrPermissionDenied {
		t.Errorf("%s: got %v, want permission denied", what, err)
	}
	if n := atomic.LoadInt32(touched); n != 0 {
		t.Errorf("%s: %d statements ran before the refusal", what, n)
	}
	if cmds := cland.take(); len(cmds) > 0 {
		t.Errorf("%s: commands were sent before the refusal: %v", what, cmds)
	}
}

// D4 / C-FAIL-2: the list of migrations named instances and hosts of every organization to any member
func TestMigrationReadsNeedSystemAdmin(t *testing.T) {
	cland := startFakeCland(t)
	for _, m := range []*MemberShip{member(5, model.OrgReader), member(5, model.OrgWriter), member(5, model.OrgAdmin),
		{UserID: 7, OrgID: 5, OrgRole: model.OrgAdmin, IsOrgOwner: true}} {
		ctx, touched := noDBContext(t, m)
		_, _, err := migrationAdmin.List(ctx, 0, 50, "", "")
		refusedEarly(t, "list", err, touched, cland)
		_, err = migrationAdmin.GetMigrationByUUID(ctx, "0b1c6e0e-6f4e-4b44-9d59-4d7bd6a3a2a1")
		refusedEarly(t, "get by uuid", err, touched, cland)
		_, err = migrationAdmin.GetMigrationByName(ctx, "m")
		refusedEarly(t, "get by name", err, touched, cland)
		_, err = migrationAdmin.Get(ctx, 1)
		refusedEarly(t, "get", err, touched, cland)
	}
	ctx, touched := noDBContext(t, &MemberShip{UserID: 1, OrgID: 1, SystemRole: model.SystemAdmin})
	if _, _, err := migrationAdmin.List(ctx, 0, 50, "", ""); err != nil {
		t.Errorf("system admin refused: %v", err)
	}
	if atomic.LoadInt32(touched) == 0 {
		t.Error("system admin list did not query")
	}
}

// D5 / FAIL-N1: a reader deleting a VPC was stopped only by the security group check at the very end, after
// clear_local_router.sh had gone to every host
func TestRouterDeleteRefusesBeforeAnything(t *testing.T) {
	cland := startFakeCland(t)
	router := &model.Router{Model: model.Model{ID: 64}, Owner: 5, Name: "rvpc"}
	for _, m := range []*MemberShip{member(5, model.OrgReader), member(6, model.OrgWriter), member(6, model.OrgAdmin)} {
		ctx, touched := noDBContext(t, m)
		refusedEarly(t, "delete vpc", routerAdmin.Delete(ctx, router), touched, cland)
	}
}

// D5 / SG-08: any member could rename a security group or make it the default one
func TestSecgroupUpdateNeedsWriter(t *testing.T) {
	cland := startFakeCland(t)
	name, yes := "renamed", true
	vpcGroup := &model.SecurityGroup{Model: model.Model{ID: 11}, Owner: 5, Name: "sg", RouterID: 3}
	for _, m := range []*MemberShip{member(5, model.OrgReader), member(6, model.OrgWriter)} {
		ctx, touched := noDBContext(t, m)
		refusedEarly(t, "rename", secgroupAdmin.Update(ctx, vpcGroup, &name, nil, nil), touched, cland)
		refusedEarly(t, "make default", secgroupAdmin.Update(ctx, vpcGroup, nil, nil, &yes), touched, cland)
	}
	if vpcGroup.Name != "sg" || vpcGroup.IsDefault {
		t.Errorf("refused updates changed the group: %+v", vpcGroup)
	}
	// The default group of the organization is chosen by its admins, who alone can create such groups
	orgGroup := &model.SecurityGroup{Model: model.Model{ID: 12}, Owner: 5, Name: "org-sg"}
	ctx, touched := noDBContext(t, member(5, model.OrgWriter))
	refusedEarly(t, "make org default", secgroupAdmin.Update(ctx, orgGroup, nil, nil, &yes), touched, cland)
}

// D5 / FIP-07: a reader could detach (PATCH {}) and, through the detach that came first, even take a floating IP
// off the host when the attach was then refused
func TestFloatingIpChangesNeedWriter(t *testing.T) {
	cland := startFakeCland(t)
	instance := &model.Instance{Model: model.Model{ID: 21}, Owner: 5, Hyper: 3, RouterID: 4, Status: model.InstanceStatusRunning}
	fip := func() *model.FloatingIp {
		return &model.FloatingIp{Model: model.Model{ID: 31}, Owner: 5, Type: string(PublicFloating), FipAddress: "52.117.101.158/28",
			IntAddress: "192.168.1.2/24", InstanceID: instance.ID, Instance: instance, RouterID: 4}
	}
	inbound := int32(100)
	for _, m := range []*MemberShip{member(5, model.OrgReader), member(6, model.OrgWriter)} {
		ctx, touched := noDBContext(t, m)
		refusedEarly(t, "detach", FloatingIpAdmin.Detach(ctx, fip()), touched, cland)
		refusedEarly(t, "delete", FloatingIpAdmin.Delete(ctx, fip()), touched, cland)
		_, err := FloatingIpAdmin.Update(ctx, fip(), &FloatingIpChange{Retarget: true})
		refusedEarly(t, "patch null instance", err, touched, cland)
		_, err = FloatingIpAdmin.Update(ctx, fip(), &FloatingIpChange{Inbound: &inbound})
		refusedEarly(t, "patch bandwidth", err, touched, cland)
		_, err = FloatingIpAdmin.Update(ctx, fip(), &FloatingIpChange{Retarget: true, Instance: instance})
		refusedEarly(t, "patch instance", err, touched, cland)
	}
	// A writer of the floating IP moving it to an instance of another organization (a system admin's view): the
	// refusal comes before the detach
	other := &model.Instance{Model: model.Model{ID: 22}, Owner: 6, Hyper: 3, RouterID: 4, Status: model.InstanceStatusRunning}
	ctx, touched := noDBContext(t, member(5, model.OrgWriter))
	_, err := FloatingIpAdmin.Update(ctx, fip(), &FloatingIpChange{Retarget: true, Instance: other})
	refusedEarly(t, "patch to another organization's instance", err, touched, cland)
}

// FIP-07: new limits of an attached floating IP are applied again by create_floating.sh, without clear_floating.sh
func TestSetBandwidthCommand(t *testing.T) {
	pub := &model.Subnet{Gateway: "52.117.101.145/28", Vlan: 756}
	internal := &model.Subnet{Vlan: 5012}
	fip := &model.FloatingIp{Model: model.Model{ID: 31}, FipAddress: "52.117.101.158/28", RouterID: 4, Inbound: 100, Outbound: 50,
		Interface: &model.Interface{Address: &model.Address{Address: "52.117.101.158/28", Subnet: pub}},
		Instance: &model.Instance{Model: model.Model{ID: 21}, Hyper: 3, Interfaces: []*model.Interface{
			{Name: "eth1"}, {PrimaryIf: true, Address: &model.Address{Address: "192.168.1.2/24", Subnet: internal}}}}}
	control, command, err := setBandwidthCommand(fip)
	if err != nil {
		t.Fatal(err)
	}
	if control != "inter=3" {
		t.Errorf("control %q", control)
	}
	// Only the limits: create_floating.sh again would add the policy rules a second time
	want := "/opt/cloudland/scripts/backend/set_floating_bandwidth.sh '4' '52.117.101.158/28' '756' '5012' '31' '100' '50'"
	if command != want {
		t.Errorf("command\n got %s\nwant %s", command, want)
	}
	fip.Instance.Interfaces = fip.Instance.Interfaces[:1]
	if _, _, err = setBandwidthCommand(fip); err == nil {
		t.Error("no error without a primary interface")
	}
}

// FAIL-7: the flavor is recorded only when the instance has its size
func TestInstanceFlavorID(t *testing.T) {
	small := &model.Flavor{Model: model.Model{ID: 9}, Name: "small", Cpu: 2, Memory: 2048, Disk: 10}
	cases := []struct {
		flavor            *model.Flavor
		cpu, memory, disk int32
		want              int64
	}{
		{small, 2, 2048, 10, 9},
		{nil, 2, 2048, 10, 0},
		{small, 4, 2048, 10, 0}, // cpu given with the flavor
		{small, 2, 4096, 10, 0},
		{small, 2, 2048, 20, 0},
		{&model.Flavor{Cpu: 2, Memory: 2048, Disk: 10}, 2, 2048, 10, 0}, // not stored
	}
	for i, c := range cases {
		if got := instanceFlavorID(c.flavor, c.cpu, c.memory, c.disk); got != c.want {
			t.Errorf("case %d: got %d, want %d", i, got, c.want)
		}
	}
}

// A gateway rescued from error by a PATCH answered with its old reason (vpn.go, "status_reason")
func TestVpnApplyStatus(t *testing.T) {
	gw := &model.VpnGateway{Status: model.VpnGatewayStatusError, StatusReason: "No node for the gateway: zone0"}
	vpnApplyStatus(gw, model.VpnGatewayStatusPending, "")
	if gw.Status != model.VpnGatewayStatusPending || gw.StatusReason != "" {
		t.Errorf("got %q %q", gw.Status, gw.StatusReason)
	}
	vpnApplyStatus(gw, model.VpnGatewayStatusAvailable, "stale")
	if gw.StatusReason != "" {
		t.Errorf("a reason kept without an error: %q", gw.StatusReason)
	}
	vpnApplyStatus(gw, model.VpnGatewayStatusError, strings.Repeat("é", 300))
	if n := len([]rune(gw.StatusReason)); n != 255 {
		t.Errorf("reason of %d runes, want 255", n)
	}
}

// The root password waiting for the node never leaves clapi
func TestPendingRootPasswordNotSerialized(t *testing.T) {
	out, err := json.Marshal(&model.Instance{RootPasswd: "stored", PendingRootPasswd: "waiting-secret"})
	must(t, err)
	if strings.Contains(string(out), "waiting-secret") || strings.Contains(strings.ToLower(string(out)), "pending") {
		t.Fatalf("pending password serialized: %s", out)
	}
}

// Deleting an instance takes its floating IPs off with the deleting member's rights: a floating IP a system admin
// attached from another organization must not make the instance undeletable
func TestFloatingIpCascadeDetach(t *testing.T) {
	cland := startFakeCland(t)
	instance := &model.Instance{Model: model.Model{ID: 23}, Owner: 6, Hyper: 3, RouterID: 4, Status: model.InstanceStatusRunning,
		Interfaces: []*model.Interface{{PrimaryIf: true, Address: &model.Address{Address: "192.168.1.3/24", Subnet: &model.Subnet{Vlan: 5012}}}}}
	fip := func() *model.FloatingIp {
		return &model.FloatingIp{Model: model.Model{ID: 33}, Owner: 1, Type: string(PublicFloating), FipAddress: "52.117.101.157/28",
			IntAddress: "192.168.1.3/24", InstanceID: instance.ID, Instance: instance, RouterID: 4}
	}
	for _, m := range []*MemberShip{member(6, model.OrgReader), member(7, model.OrgWriter)} {
		ctx, touched := noDBContext(t, m)
		refusedEarly(t, "detach", FloatingIpAdmin.Detach(ctx, fip()), touched, cland)
	}
	ctx, _ := noDBContext(t, member(6, model.OrgWriter))
	if err := FloatingIpAdmin.Detach(ctx, fip()); err != nil {
		t.Fatalf("writer of the instance's organization: %v", err)
	}
	if cmds := cland.take(); len(cmds) != 1 || !strings.Contains(cmds[0], "clear_floating.sh") {
		t.Fatalf("commands %v", cmds)
	}
	// Changing the floating IP itself stays with its own organization
	ctx, touched := noDBContext(t, member(6, model.OrgWriter))
	_, err := FloatingIpAdmin.Update(ctx, fip(), &FloatingIpChange{Retarget: true})
	refusedEarly(t, "patch another organization's floating ip", err, touched, cland)
}

// The new limits of a load balancer's or a native address would be stored and shown without reaching the host
func TestBandwidthChangeAllowed(t *testing.T) {
	in, same := int32(100), int32(10)
	lb := &model.LoadBalancer{Model: model.Model{ID: 5}}
	cases := []struct {
		fipType string
		change  FloatingIpChange
		ok      bool
	}{
		{string(PublicFloating), FloatingIpChange{Inbound: &in}, true},
		{string(PublicFloating), FloatingIpChange{Retarget: true, Instance: &model.Instance{}, Inbound: &in}, true},
		{string(PublicFloating), FloatingIpChange{Retarget: true, Inbound: &in}, true}, // detached, applied at the next attach
		{string(PublicFloating), FloatingIpChange{Retarget: true, LoadBalancer: lb, Inbound: &in}, false},
		{string(PublicLoadBalancer), FloatingIpChange{Inbound: &in}, false},
		{string(PublicNative), FloatingIpChange{Outbound: &in}, false},
		{string(PublicNative), FloatingIpChange{Inbound: &same}, true}, // nothing changes
		{string(PublicLoadBalancer), FloatingIpChange{}, true},
	}
	for i, c := range cases {
		fip := &model.FloatingIp{Type: c.fipType, Inbound: 10, Outbound: 10}
		err := bandwidthChangeAllowed(fip, &c.change)
		if (err == nil) != c.ok || (err != nil && errCode(err) != ErrInvalidParameter) {
			t.Errorf("case %d (%s): got %v", i, c.fipType, err)
		}
	}
	// Refused before anything is stored or sent
	cland := startFakeCland(t)
	ctx, touched := noDBContext(t, member(5, model.OrgWriter))
	_, err := FloatingIpAdmin.Update(ctx, &model.FloatingIp{Model: model.Model{ID: 34}, Owner: 5, Type: string(PublicLoadBalancer),
		LoadBalancer: lb}, &FloatingIpChange{Inbound: &in})
	if errCode(err) != ErrInvalidParameter || atomic.LoadInt32(touched) != 0 || len(cland.take()) != 0 {
		t.Fatalf("load balancer address: err %v, statements %d", err, atomic.LoadInt32(touched))
	}
}

// Batches are named <hostname>-1 .. <hostname>-N, and every name must fit the limit a rename checks
func TestBatchHostnameValid(t *testing.T) {
	name := func(n int) string { return strings.Repeat("a", n) }
	for _, c := range []struct {
		prefix string
		count  int
		ok     bool
	}{
		{name(32), 1, true}, {name(30), 9, true}, {name(31), 9, false}, {name(29), 16, true}, {name(30), 16, false},
		{name(2), 16, true},
	} {
		err := batchHostnameValid(c.prefix, c.count)
		if (err == nil) != c.ok || (err != nil && errCode(err) != ErrInvalidParameter) {
			t.Errorf("%d characters x %d: got %v", len(c.prefix), c.count, err)
		}
	}
}
