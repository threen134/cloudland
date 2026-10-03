/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

// Transit gateway flows against PostgreSQL (CLAPI_TEST_DB_URI) with the fake cland of p2_fix_test.go, which records
// what clapi sends:
// the dispatch sets and control strings, the states, the forwarding entries exchanged on attach, node reports and
// attachment statuses, the validations and the partial unique indexes. With TGW_SIM_DIR set, the states sent are
// also written there for the netns simulation of apply_tgw.sh.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	. "api/src/common"
	"api/src/model"
)

var tgwApplyRe = regexp.MustCompile(`(?s)apply_tgw\.sh '(\d+)' '(\d+)' <<'EOF'\n(.*)\nEOF`)

type tgwSent struct {
	control string
	state   *tgwState
	fdb     []*FdbRule
}

// parseSent reads the "<control> | <command>" records of the fake cland (p2_fix_test.go)
func parseSent(t *testing.T, records []string) (applies, fdbs []*tgwSent) {
	t.Helper()
	for _, rec := range records {
		control, command, _ := strings.Cut(rec, " | ")
		if m := tgwApplyRe.FindStringSubmatch(command); m != nil {
			st := &tgwState{}
			if err := json.Unmarshal([]byte(m[3]), st); err != nil {
				t.Fatalf("bad state json: %v", err)
			}
			applies = append(applies, &tgwSent{control: control, state: st})
			continue
		}
		if strings.Contains(command, "add_fwrule.sh") {
			body := command[strings.Index(command, "\n")+1 : strings.LastIndex(command, "\nEOF")]
			rules := []*FdbRule{}
			if err := json.Unmarshal([]byte(body), &rules); err != nil {
				t.Fatalf("bad fdb json: %v", err)
			}
			fdbs = append(fdbs, &tgwSent{control: control, fdb: rules})
		}
	}
	return
}

type tgwPGFixture struct {
	t    *testing.T
	ctx  context.Context
	org  int64
	tag  string
	vlan int64
}

func (f *tgwPGFixture) router(name string) *model.Router {
	_, db := GetContextDB(f.ctx)
	r := &model.Router{Owner: f.org, Name: fmt.Sprintf("%s-%s", name, f.tag), Status: "available"}
	must(f.t, db.Create(r).Error)
	return r
}

func (f *tgwPGFixture) subnet(r *model.Router, cidr, typ string) *model.Subnet {
	_, db := GetContextDB(f.ctx)
	f.vlan++
	ip, ipNet, _ := net.ParseCIDR(cidr)
	ones, _ := ipNet.Mask.Size()
	gw := make(net.IP, 4)
	copy(gw, ip.To4())
	gw[3]++
	s := &model.Subnet{Owner: f.org, Name: fmt.Sprintf("%s-%s", cidr, f.tag), Network: ipNet.String(), Gateway: fmt.Sprintf("%s/%d", gw, ones),
		Vlan: f.vlan, Type: typ, RouterID: r.ID}
	must(f.t, db.Create(s).Error)
	return s
}

// nic puts an instance NIC of subnet s with address ip on host hostid
func (f *tgwPGFixture) nic(s *model.Subnet, ip string, hostid int32) *model.Interface {
	_, db := GetContextDB(f.ctx)
	iface := &model.Interface{Owner: f.org, Name: "eth0", MacAddr: fmt.Sprintf("52:54:%02x:%02x:%02x:%02x", byte(s.Vlan>>8), byte(s.Vlan), byte(hostid), time.Now().Nanosecond()%250),
		Instance: 1, RouterID: s.RouterID, Hyper: hostid, Type: "instance", Subnet: s.ID}
	must(f.t, db.Create(iface).Error)
	ones, _ := net.IPMask(net.ParseIP(s.Netmask).To4()).Size()
	if ones == 0 {
		_, n, _ := net.ParseCIDR(s.Network)
		ones, _ = n.Mask.Size()
	}
	must(f.t, db.Create(&model.Address{Owner: f.org, Address: fmt.Sprintf("%s/%d", ip, ones), SubnetID: s.ID, Interface: iface.ID, Allocated: true}).Error)
	return iface
}

func (f *tgwPGFixture) attStatus(att *model.TgwAttachment) (status, reason string) {
	_, db := GetContextDB(f.ctx)
	a := &model.TgwAttachment{}
	if err := db.Unscoped().Where("id = ?", att.ID).Take(a).Error; err != nil {
		f.t.Fatal(err)
	}
	if a.DeletedAt.Valid {
		return "deleted", a.StatusReason
	}
	return a.Status, a.StatusReason
}

func (f *tgwPGFixture) gen(tgw *model.TransitGateway) int64 {
	_, db := GetContextDB(f.ctx)
	g := &model.TransitGateway{}
	must(f.t, db.Where("id = ?", tgw.ID).Take(g).Error)
	return g.Generation
}

func wantCode(t *testing.T, err error, code ErrCode, what string) {
	t.Helper()
	var clErr *CLError
	if !errors.As(err, &clErr) || clErr.Code != code {
		t.Fatalf("%s: want code %d, got %v", what, code, err)
	}
}

func writeSim(t *testing.T, name string, st *tgwState) {
	dir := os.Getenv("TGW_SIM_DIR")
	if dir == "" || st == nil {
		return
	}
	data, _ := json.Marshal(st)
	must(t, os.WriteFile(filepath.Join(dir, name), data, 0644))
}

func TestTgwPG(t *testing.T) {
	org := int64(900000 + time.Now().Unix()%90000)
	ctx, db := pgContext(t, org)
	cland := startFakeCland(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	f := &tgwPGFixture{t: t, ctx: ctx, org: org, tag: tag, vlan: 7000000 + time.Now().Unix()%1000000*3}

	// Three hosts
	hosts := []int32{9101, 9102, 9103}
	for i, h := range hosts {
		must(t, db.Unscoped().Where("hostid = ?", h).Delete(&model.Hyper{}).Error)
		must(t, db.Create(&model.Hyper{Hostid: h, Hostname: fmt.Sprintf("tgw-h%d", i+1), Status: 1, HostIP: fmt.Sprintf("10.9.9.%d", i+1)}).Error)
	}
	ra, rb, rc := f.router("tgw-a"), f.router("tgw-b"), f.router("tgw-c")
	sa := f.subnet(ra, "172.31.1.0/24", "internal")
	sb := f.subnet(rb, "172.31.2.0/24", "internal")
	sc := f.subnet(rc, "172.31.3.0/24", "internal")
	f.subnet(rb, "192.168.196.0/24", "vrrp") // a load balancer's VRRP subnet: never propagated
	f.nic(sa, "172.31.1.10", 9101)
	f.nic(sb, "172.31.2.10", 9102)
	f.nic(sc, "172.31.3.10", 9101)

	tgw, err := TransitGatewayAdmin.Create(ctx, "tgw-"+tag[len(tag)-6:], "")
	must(t, err)
	if _, err = TransitGatewayAdmin.Create(ctx, tgw.Name, ""); err == nil {
		t.Fatal("duplicate gateway name accepted")
	}
	wantCode(t, err, ErrTgwExists, "duplicate name")

	// Attach A: only host 9101 hosts a member
	attA, err := TransitGatewayAdmin.Attach(ctx, tgw, ra, nil, true)
	must(t, err)
	applies, fdbs := parseSent(t, cland.take())
	if len(applies) != 1 || applies[0].control != "inter=9101" || len(applies[0].state.Attachments) != 1 {
		t.Fatalf("attach A dispatch: %+v", applies)
	}
	if len(fdbs) != 0 {
		t.Fatalf("attach A should exchange no entries (one host), got %d", len(fdbs))
	}
	if s, _ := f.attStatus(attA); s != model.TgwAttachmentAttaching {
		t.Fatalf("attach A status %s", s)
	}
	must(t, TgwNodeApplied(ctx, tgw.ID, 9101, f.gen(tgw), true, "", 1))
	if s, _ := f.attStatus(attA); s != model.TgwAttachmentAvailable {
		t.Fatalf("A after the node confirmed: %s", s)
	}

	// Attach B and C
	attB, err := TransitGatewayAdmin.Attach(ctx, tgw, rb, nil, true)
	must(t, err)
	applies, fdbs = parseSent(t, cland.take())
	if len(applies) != 1 || applies[0].control != fmt.Sprintf("toall=group-tgw-%d:9101,9102", tgw.ID) {
		t.Fatalf("attach B dispatch: %+v", applies[0].control)
	}
	// 9101 gets B's NIC, 9102 gets A's
	got := map[string]string{}
	for _, s := range fdbs {
		for _, r := range s.fdb {
			got[s.control] += r.InnerIP + "@" + r.OuterIP + " "
		}
	}
	if got["inter=9101"] != "172.31.2.10/24@10.9.9.2 " || got["inter=9102"] != "172.31.1.10/24@10.9.9.1 " {
		t.Fatalf("forwarding entries exchanged on attach: %v", got)
	}
	attC, err := TransitGatewayAdmin.Attach(ctx, tgw, rc, nil, true)
	must(t, err)
	applies, _ = parseSent(t, cland.take())
	st := applies[0].state
	writeSim(t, "pg-mesh.json", st)
	byRouter := map[int64]*tgwStateAttachment{}
	for _, a := range st.Attachments {
		byRouter[a.Router] = a
	}
	if a := byRouter[ra.ID]; a == nil || strings.Join(a.Reachable, ",") != "172.31.2.0/24,172.31.3.0/24" || len(a.Blackhole) != 0 ||
		strings.Join(a.Throw, ",") != "172.31.1.0/24,192.168.196.0/24" {
		t.Fatalf("state of A: %+v", a)
	}
	if b := byRouter[rb.ID]; b == nil || strings.Join(b.Reachable, ",") != "172.31.1.0/24,172.31.3.0/24" || b.RouterIP != "169.254.254.3" || b.GatewayIP != "169.254.254.2" {
		t.Fatalf("state of B (the VRRP subnet must not be propagated): %+v", b)
	}
	if len(st.Tables) != 1 || st.Tables[0].Table != 1000 || len(st.Tables[0].Routes) != 3 {
		t.Fatalf("tables: %+v", st.Tables)
	}
	// Both nodes confirm the latest generation: every attachment available
	g := f.gen(tgw)
	must(t, TgwNodeApplied(ctx, tgw.ID, 9101, g, true, "", 3))
	must(t, TgwNodeApplied(ctx, tgw.ID, 9102, g, true, "", 3))
	for _, a := range []*model.TgwAttachment{attA, attB, attC} {
		if s, _ := f.attStatus(a); s != model.TgwAttachmentAvailable {
			t.Fatalf("attachment %d: %s", a.ID, s)
		}
	}
	// The VPC list reads the gateway of each VPC of a page in two queries
	listAtts, listTgws, err := TransitGatewayAdmin.AttachmentsOfRouters(ctx, []int64{ra.ID, rb.ID, ra.ID + 100000})
	must(t, err)
	if len(listAtts) != 2 || listAtts[ra.ID] == nil || listTgws[listAtts[ra.ID].TgwID] == nil || listTgws[listAtts[ra.ID].TgwID].Name != tgw.Name {
		t.Fatalf("attachments of the VPC page: %v %v", listAtts, listTgws)
	}
	// An older report never replaces a newer one
	must(t, TgwNodeApplied(ctx, tgw.ID, 9101, g-1, false, "late", 3))
	ns := &model.TgwNodeState{}
	must(t, db.Where("tgw_id = ? AND hyper = ?", tgw.ID, 9101).Take(ns).Error)
	if ns.Generation != g || ns.Status != model.TgwNodeOK {
		t.Fatalf("an older report replaced the state: %+v", ns)
	}

	// Validations, all before anything is sent
	rd := f.router("tgw-d")
	f.subnet(rd, "172.31.2.128/25", "internal")
	_, err = TransitGatewayAdmin.Attach(ctx, tgw, rd, nil, true)
	wantCode(t, err, ErrTgwCidrConflict, "overlapping VPC")
	if n := len(cland.take()); n != 0 {
		t.Fatalf("%d commands sent for a refused attach", n)
	}
	_, err = TransitGatewayAdmin.Attach(ctx, tgw, ra, nil, true)
	wantCode(t, err, ErrTgwAttachmentExists, "attached twice")
	err = ValidateTgwNewSubnet(ctx, ra.ID, "172.31.3.0/25")
	wantCode(t, err, ErrTgwCidrConflict, "subnet overlapping another member")
	err = ValidateTgwNewSubnet(ctx, ra.ID, "192.168.196.0/25")
	wantCode(t, err, ErrTgwCidrConflict, "subnet in the VRRP range")
	must(t, ValidateTgwNewSubnet(ctx, ra.ID, "172.31.9.0/24"))
	// VPN: a member's VPN networks against the other members, both ways
	gw := &model.VpnGateway{Owner: org, Name: "vpn-" + tag, RouterID: rb.ID, Status: model.VpnGatewayStatusAvailable}
	must(t, db.Create(gw).Error)
	must(t, db.Create(&model.VpnRemotePrefix{VpnGatewayID: gw.ID, Cidr: "10.250.0.0/24", Source: model.VpnPrefixSourceConnectionStatic}).Error)
	err = ValidateTgwNewSubnet(ctx, ra.ID, "10.250.0.0/25")
	wantCode(t, err, ErrTgwCidrConflict, "subnet overlapping a member's VPN network")
	err = validateVpnPrefixes(ctx, rb.ID, gw.ID, []string{"172.31.1.0/26"}, 0)
	wantCode(t, err, ErrVpnCidrConflict, "VPN network overlapping another member")
	err = validateTunnelLink(ctx, rb.ID, "169.254.254.9", "169.254.254.10")
	wantCode(t, err, ErrVpnCidrConflict, "tunnel link in the gateway link range")
	defaultTable := &model.TgwRouteTable{}
	must(t, db.Where("tgw_id = ? AND is_default = ?", tgw.ID, true).Take(defaultTable).Error)
	_, err = TransitGatewayAdmin.CreateRoute(ctx, tgw, defaultTable, "10.250.0.0/16", attA)
	wantCode(t, err, ErrTgwCidrConflict, "static route over a member's VPN network")
	must(t, db.Where("vpn_gateway_id = ?", gw.ID).Delete(&model.VpnRemotePrefix{}).Error)
	must(t, db.Delete(gw).Error)

	// Deleting a member VPC or the gateway is refused
	err = (&RouterAdmin{}).Delete(ctx, ra)
	wantCode(t, err, ErrRouterHasTgwAttachment, "delete a member VPC")
	err = TransitGatewayAdmin.Delete(ctx, tgw)
	wantCode(t, err, ErrTgwInUse, "delete a gateway with attachments")
	cland.take()

	// A node failing a change puts attaching attachments in error; a later ok brings them back
	rt, err := TransitGatewayAdmin.CreateRouteTable(ctx, tgw, "spokes")
	must(t, err)
	cland.take()
	must(t, TransitGatewayAdmin.UpdateAttachment(ctx, tgw, attB, rt))
	applies, _ = parseSent(t, cland.take())
	if len(applies) != 1 {
		t.Fatalf("association change dispatch: %d", len(applies))
	}
	for _, a := range applies[0].state.Attachments {
		if a.Router == rb.ID && (a.Table != 1001 || len(a.Reachable) != 0 || strings.Join(a.Blackhole, ",") != "172.31.1.0/24,172.31.3.0/24") {
			t.Fatalf("B on an empty table must drop the members: %+v", a)
		}
	}
	writeSim(t, "pg-b-empty-table.json", applies[0].state)
	_, err = TransitGatewayAdmin.CreatePropagation(ctx, tgw, rt, attA, "")
	must(t, err)
	_, err = TransitGatewayAdmin.CreatePropagation(ctx, tgw, rt, attA, "")
	wantCode(t, err, ErrTgwPropagationExists, "duplicate propagation")
	_, err = TransitGatewayAdmin.CreatePropagation(ctx, tgw, rt, attC, "10.0.0.0/24")
	if err == nil {
		t.Fatal("a prefix overlapping no subnet of the VPC was accepted")
	}
	cland.take()
	err = TransitGatewayAdmin.DeleteRouteTable(ctx, tgw, rt)
	wantCode(t, err, ErrTgwRouteTableInUse, "delete an associated table")

	// Detach C: detaching until the remaining node confirmed, then gone; the VPC can attach again (partial index)
	prevGen := f.gen(tgw)
	must(t, TransitGatewayAdmin.Detach(ctx, tgw, attC))
	applies, _ = parseSent(t, cland.take())
	if s, _ := f.attStatus(attC); s != model.TgwAttachmentDetaching {
		t.Fatalf("C after detach: %s", s)
	}
	for _, a := range applies[0].state.Attachments {
		if a.Router == rc.ID {
			t.Fatal("a detaching VPC is still in the state")
		}
	}
	if f.gen(tgw) <= prevGen {
		t.Fatal("detach did not bump the generation")
	}
	g = f.gen(tgw)
	must(t, TgwNodeApplied(ctx, tgw.ID, 9101, g, true, "", 2))
	must(t, TgwNodeApplied(ctx, tgw.ID, 9102, g, true, "", 2))
	if s, _ := f.attStatus(attC); s != "deleted" {
		t.Fatalf("C after the nodes confirmed: %s", s)
	}
	attC2, err := TransitGatewayAdmin.Attach(ctx, tgw, rc, nil, true)
	must(t, err)
	if attC2.Slot != attC.Slot {
		t.Fatalf("the freed slot %d was not reused (%d)", attC.Slot, attC2.Slot)
	}
	cland.take()

	// A node error marks an attaching attachment; nodes silent for too long are named
	g = f.gen(tgw)
	must(t, TgwNodeApplied(ctx, tgw.ID, 9101, g, false, "boom", 3))
	if s, reason := f.attStatus(attC2); s != model.TgwAttachmentError || !strings.Contains(reason, "boom") {
		t.Fatalf("C2 after a node error: %s %q", s, reason)
	}
	must(t, TgwNodeApplied(ctx, tgw.ID, 9101, g, true, "", 3))
	must(t, db.Model(&model.TgwAttachment{}).Where("id = ?", attC2.ID).UpdateColumn("updated_at", time.Now().Add(-10*time.Minute)).Error)
	tgwEvaluate(ctx, tgw.ID)
	if s, reason := f.attStatus(attC2); s != model.TgwAttachmentError || !strings.Contains(reason, "9102") {
		t.Fatalf("C2 waiting for 9102 too long: %s %q", s, reason)
	}
	must(t, TgwNodeApplied(ctx, tgw.ID, 9102, g, true, "", 3))
	if s, _ := f.attStatus(attC2); s != model.TgwAttachmentAvailable {
		t.Fatalf("C2 once every node confirmed: %s", s)
	}

	// Node join / leave
	cland.take()
	tgwResyncLast = map[[2]int64]time.Time{}
	must(t, TgwResyncNode(ctx, rb.ID, 9103))
	applies, _ = parseSent(t, cland.take())
	if len(applies) != 1 || applies[0].control != "inter=9103" || len(applies[0].state.Attachments) != 3 {
		t.Fatalf("resync of a joining node: %+v", applies)
	}
	must(t, TgwResyncNode(ctx, rb.ID, 9103))
	if n := len(cland.take()); n != 0 {
		t.Fatal("a second resync within 10 s was sent")
	}
	// 9101 keeps A and C: no leave; once both NICs are gone it gets the empty state
	must(t, TgwNodeCheckLeave(ctx, ra.ID, 9101))
	if n := len(cland.take()); n != 0 {
		t.Fatal("a node still hosting a member was told to leave")
	}
	must(t, db.Where("router_id IN ? AND hyper = ?", []int64{ra.ID, rc.ID}, 9101).Delete(&model.Interface{}).Error)
	before := f.gen(tgw)
	must(t, TgwNodeCheckLeave(ctx, ra.ID, 9101))
	if f.gen(tgw) != before+1 {
		t.Fatal("a leave did not bump the generation")
	}
	applies, _ = parseSent(t, cland.take())
	var toLeaver, toOthers *tgwSent
	for _, a := range applies {
		if a.control == "inter=9101" {
			toLeaver = a
		} else {
			toOthers = a
		}
	}
	if toLeaver == nil || len(toLeaver.state.Attachments) != 0 || toLeaver.state.Generation != before+1 {
		t.Fatalf("empty state to the node that left: %+v", toLeaver)
	}
	if toOthers == nil || toOthers.control != "inter=9102" || len(toOthers.state.Attachments) != 3 || toOthers.state.Generation != before+1 {
		t.Fatalf("new generation to the remaining node: %+v", toOthers)
	}
	nodeRow := func(h int32) *model.TgwNodeState {
		r := &model.TgwNodeState{}
		if db.Where("tgw_id = ? AND hyper = ?", tgw.ID, h).Take(r).Error != nil {
			return nil
		}
		return r
	}
	if r := nodeRow(9101); r == nil || r.Status != model.TgwNodeLeaving || r.Generation != before+1 {
		t.Fatalf("row of the node that left: %+v", r)
	}
	// Neither confirmed: once due, the watchdog sends the empty state again to the one that left and the current
	// generation to the one behind; not again before the backoff
	tgwSends = map[[2]int64]*tgwSendMark{}
	tgwRetry(ctx, tgw.ID)
	applies, _ = parseSent(t, cland.take())
	resent := map[string]int{}
	for _, a := range applies {
		resent[a.control] = len(a.state.Attachments)
	}
	if n, ok := resent["inter=9101"]; !ok || n != 0 {
		t.Fatalf("empty state sent again: %v", resent)
	}
	if n, ok := resent["inter=9102"]; !ok || n != 3 {
		t.Fatalf("current state sent again to the node behind: %v", resent)
	}
	tgwRetry(ctx, tgw.ID)
	if n := len(cland.take()); n != 0 {
		t.Fatalf("%d resends before the backoff elapsed", n)
	}
	// A failed removal keeps the row; a confirmed one drops it
	must(t, TgwNodeApplied(ctx, tgw.ID, 9101, before+1, false, "busy", 0))
	if r := nodeRow(9101); r == nil || r.Status != model.TgwNodeLeaving || r.Reason != "busy" {
		t.Fatalf("row after a failed removal: %+v", r)
	}
	must(t, TgwNodeApplied(ctx, tgw.ID, 9101, before+1, true, "", 0))
	if r := nodeRow(9101); r != nil {
		t.Fatalf("the row of a node that confirmed leaving is still there: %+v", r)
	}
	must(t, TgwNodeApplied(ctx, tgw.ID, 9102, f.gen(tgw), true, "", 3))

	// A subnet added to a member: a new generation in the transaction, the dispatch after it
	before = f.gen(tgw)
	after, err := TgwRouterChanged(ctx, rb.ID)
	must(t, err)
	if f.gen(tgw) != before+1 || after == nil || len(cland.take()) != 0 {
		t.Fatal("a member subnet change must bump the generation and leave the dispatch to after the commit")
	}
	after(ctx)
	if len(cland.take()) == 0 {
		t.Fatal("a member subnet change was not dispatched")
	}

	// One-way pairs: B's table holds only A, so C reaches B (default table) but B has no route back
	pairs, err := TgwAsymmetries(ctx, tgw)
	must(t, err)
	if len(pairs) != 1 || pairs[0].From.ID != attC2.ID || pairs[0].To.ID != attB.ID {
		got := []string{}
		for _, p := range pairs {
			got = append(got, fmt.Sprintf("%d->%d", p.From.ID, p.To.ID))
		}
		t.Fatalf("one-way pairs: %v (want %d->%d)", got, attC2.ID, attB.ID)
	}

	// Detach everything and delete the gateway
	for _, a := range []*model.TgwAttachment{attA, attB, attC2} {
		must(t, TransitGatewayAdmin.Detach(ctx, tgw, a))
	}
	cland.take()
	g = f.gen(tgw)
	if r := nodeRow(9102); r == nil || r.Status != model.TgwNodeLeaving {
		t.Fatalf("the last node is not leaving after the last detach: %+v", r)
	}
	must(t, TgwNodeApplied(ctx, tgw.ID, 9102, g, true, "", 0))
	tgwEvaluate(ctx, tgw.ID)
	for _, a := range []*model.TgwAttachment{attA, attB, attC2} {
		if s, _ := f.attStatus(a); s != "deleted" {
			t.Fatalf("attachment %d after the last detach: %s", a.ID, s)
		}
	}
	// A node that never confirmed keeps getting the empty state after the gateway is deleted
	must(t, db.Create(&model.TgwNodeState{TgwID: tgw.ID, Hyper: 9103, Generation: 1, Status: model.TgwNodeOK, UpdatedAt: time.Now()}).Error)
	g = f.gen(tgw)
	must(t, TransitGatewayAdmin.Delete(ctx, tgw))
	applies, _ = parseSent(t, cland.take())
	if len(applies) != 1 || applies[0].control != "inter=9103" || len(applies[0].state.Attachments) != 0 || applies[0].state.Generation != g+1 {
		t.Fatalf("empty state after the delete: %+v", applies)
	}
	if r := nodeRow(9103); r == nil || r.Status != model.TgwNodeLeaving {
		t.Fatalf("row of an unconfirmed node after the delete: %+v", r)
	}
	tgwSends = map[[2]int64]*tgwSendMark{}
	tgwRetry(ctx, tgw.ID)
	if applies, _ = parseSent(t, cland.take()); len(applies) != 1 || applies[0].control != "inter=9103" {
		t.Fatalf("the watchdog does not resend for a deleted gateway: %+v", applies)
	}
	must(t, TgwNodeApplied(ctx, tgw.ID, 9103, g+1, true, "", 0))
	if r := nodeRow(9103); r != nil {
		t.Fatal("row kept after the node confirmed")
	}
	var rows int64
	must(t, db.Model(&model.TgwRouteTable{}).Where("tgw_id = ?", tgw.ID).Count(&rows).Error)
	if rows != 0 {
		t.Fatal("route tables survived the gateway")
	}
	// The name is free again (partial unique index)
	again, err := TransitGatewayAdmin.Create(ctx, tgw.Name, "")
	must(t, err)
	must(t, TransitGatewayAdmin.Delete(ctx, again))
}
