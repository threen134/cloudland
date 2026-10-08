/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	. "api/src/common"
	"api/src/model"
	"api/src/utils/tracing"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The desired state of a transit gateway, how it is computed and how it reaches the nodes
// (vpc-transit-gateway-plan.md §2). Every node hosting a member VPC gets the same state and reconciles
// against it (apply_tgw.sh); the generation lets a node drop a state older than the one it applied.

const (
	MaxTgwAttachments    = 10
	MaxTgwRouteTables    = 20
	MaxTgwRoutesPerTable = 100
	// Kernel table of a route table in tgw-<ID>: base + slot. Private to that netns, so not registered by name
	tgwTableBase = 1000
	// An attachment, or a detach, not confirmed by every node by then is marked with the nodes it waits for
	tgwConfirmTimeout = 5 * time.Minute

	TgwRoutePropagated = "propagated"

	// Reason of an attachment some nodes did not confirm within tgwConfirmTimeout, followed by their host IDs
	tgwWaitReason = "waiting for nodes "

	// Resends to a node that did not confirm a state: after a minute, then doubling up to ten minutes
	tgwRetryFirst = time.Minute
	tgwRetryMax   = 10 * time.Minute

	// The command reaches the node in an environment variable of at most 128 KiB: compress above 64 KiB, refuse
	// what is still too large
	tgwCompressAbove = 64 << 10
	tgwStateMax      = 120 << 10
)

// tgwMember is an active attachment with the internal networks of its VPC
type tgwMember struct {
	Att     *model.TgwAttachment
	Subnets []string // normalized, in creation order; the VRRP subnet is never part of it
	// Networks the VPN gateway of the VPC routes (remote networks, BGP summaries, client pool): they stay with the
	// VPN in this VPC's router, whatever a route table says
	VpnPrefixes []string
}

// TgwRouteEntry is one effective route of a route table
type TgwRouteEntry struct {
	Prefix       string
	AttachmentID int64  // 0: dropped (blackhole)
	Source       string // propagated | static | blackhole
}

func normalizeCidr(value string) string {
	_, ipNet, err := net.ParseCIDR(strings.TrimSpace(value))
	if err != nil {
		return value
	}
	return ipNet.String()
}

// cidrContains tells whether inner lies completely inside outer
func cidrContains(outer, inner string) bool {
	_, o, err := net.ParseCIDR(outer)
	if err != nil {
		return false
	}
	_, i, err := net.ParseCIDR(inner)
	if err != nil {
		return false
	}
	oOnes, _ := o.Mask.Size()
	iOnes, _ := i.Mask.Size()
	return oOnes <= iOnes && o.Contains(i.IP)
}

// moreSpecificCidr returns the narrower of two overlapping networks, which is their intersection
func moreSpecificCidr(a, b string) string {
	if cidrContains(a, b) {
		return b
	}
	return a
}

// sortCidrs orders networks by address, then by prefix length, so the output never depends on map order
func sortCidrs(cidrs []string) {
	sort.SliceStable(cidrs, func(i, j int) bool { return cidrLess(cidrs[i], cidrs[j]) })
}

func cidrLess(a, b string) bool {
	_, na, ea := net.ParseCIDR(a)
	_, nb, eb := net.ParseCIDR(b)
	if ea != nil || eb != nil {
		return a < b
	}
	if c := bytes.Compare(na.IP.To4(), nb.IP.To4()); c != 0 {
		return c < 0
	}
	oa, _ := na.Mask.Size()
	ob, _ := nb.Mask.Size()
	return oa < ob
}

// tgwLinkIPs returns the addresses of the /31 of an attachment slot: router side (tr-), gateway side (ta-)
func tgwLinkIPs(slot int32) (routerIP, gatewayIP string) {
	_, link, _ := net.ParseCIDR(TgwLinkCidr)
	base := link.IP.To4()
	gateway := net.IPv4(base[0], base[1], base[2], byte(2*slot))
	router := net.IPv4(base[0], base[1], base[2], byte(2*slot+1))
	return router.String(), gateway.String()
}

// TgwLinkAddresses returns the router side and the gateway side address of an attachment slot
func TgwLinkAddresses(slot int32) (routerIP, gatewayIP string) {
	return tgwLinkIPs(slot)
}

// computeTgwTableRoutes merges the propagations and the static routes of one route table (§2.2). A propagation
// with an allow list contributes the intersection of each subnet with each allowed network; a static route
// replaces a propagated one with the same destination. Routes towards a member that is not active are left out.
func computeTgwTableRoutes(tableID int64, members map[int64]*tgwMember, props []*model.TgwPropagation, statics []*model.TgwRoute) []*TgwRouteEntry {
	byPrefix := map[string]*TgwRouteEntry{}
	for _, p := range props {
		if p.RouteTableID != tableID {
			continue
		}
		m := members[p.AttachmentID]
		if m == nil {
			continue
		}
		allow, _ := ParseCidrList(p.Prefixes)
		for _, s := range m.Subnets {
			if len(allow) == 0 {
				byPrefix[s] = &TgwRouteEntry{Prefix: s, AttachmentID: m.Att.ID, Source: TgwRoutePropagated}
				continue
			}
			for _, e := range allow {
				if cidrsOverlap(e, s) {
					prefix := moreSpecificCidr(e, s)
					byPrefix[prefix] = &TgwRouteEntry{Prefix: prefix, AttachmentID: m.Att.ID, Source: TgwRoutePropagated}
				}
			}
		}
	}
	for _, r := range statics {
		if r.RouteTableID != tableID {
			continue
		}
		dst := normalizeCidr(r.Destination)
		if r.Type == model.TgwRouteBlackhole {
			byPrefix[dst] = &TgwRouteEntry{Prefix: dst, Source: model.TgwRouteBlackhole}
			continue
		}
		if members[r.AttachmentID] == nil {
			continue
		}
		byPrefix[dst] = &TgwRouteEntry{Prefix: dst, AttachmentID: r.AttachmentID, Source: model.TgwRouteStatic}
	}
	prefixes := make([]string, 0, len(byPrefix))
	for p := range byPrefix {
		prefixes = append(prefixes, p)
	}
	sortCidrs(prefixes)
	entries := make([]*TgwRouteEntry, 0, len(prefixes))
	for _, p := range prefixes {
		entries = append(entries, byPrefix[p])
	}
	return entries
}

// tgwRouterPrefixes is what the VPC router of a member installs (§2.2): reachable prefixes go to the gateway and
// into nonat, blackholes are dropped, throw entries hand the lookup back to the rest of the policy rules. The
// networks of the VPC itself, its VRRP subnet and the networks of its VPN gateway are thrown, so no route of the
// table (a wide blackhole isolating a branch, say) can capture the traffic inside the VPC or to its VPN sites;
// anything the table would send back there is left out. A member network the table does not route anywhere is
// dropped instead of leaking out through the default route of the router.
func tgwRouterPrefixes(self *tgwMember, entries []*TgwRouteEntry, members []*tgwMember) (reachable, blackhole, throw []string) {
	reachable, blackhole = []string{}, []string{}
	throw = append(append(append([]string{}, self.Subnets...), vrrpSubnetCidr), self.VpnPrefixes...)
	sortCidrs(throw)
	inside := func(prefix string) bool {
		for _, s := range throw {
			if cidrContains(s, prefix) {
				return true
			}
		}
		return false
	}
	dropped := map[string]bool{}
	for _, e := range entries {
		if e.AttachmentID == self.Att.ID || inside(e.Prefix) {
			continue
		}
		if e.AttachmentID == 0 {
			blackhole = append(blackhole, e.Prefix)
			dropped[e.Prefix] = true
		} else {
			reachable = append(reachable, e.Prefix)
		}
	}
	for _, m := range members {
		if m.Att.ID == self.Att.ID {
			continue
		}
		for _, s := range m.Subnets {
			routed := false
			for _, e := range entries {
				if e.AttachmentID != 0 && e.AttachmentID != self.Att.ID && cidrContains(e.Prefix, s) {
					routed = true
					break
				}
			}
			if !routed && !dropped[s] && !inside(s) {
				blackhole = append(blackhole, s)
				dropped[s] = true
			}
		}
	}
	sortCidrs(reachable)
	sortCidrs(blackhole)
	return
}

type tgwStateAttachment struct {
	ID        int64    `json:"id"`
	Router    int64    `json:"router"`
	RouterIP  string   `json:"tr"`
	GatewayIP string   `json:"ta"`
	Table     int32    `json:"table"`
	Reachable []string `json:"reachable"`
	Blackhole []string `json:"blackhole"`
	Throw     []string `json:"throw"`
}

type tgwStateRoute struct {
	Prefix string `json:"prefix"`
	Att    int64  `json:"att"` // 0: blackhole
}

type tgwStateTable struct {
	Table  int32            `json:"table"`
	Routes []*tgwStateRoute `json:"routes"`
}

// tgwState is the JSON apply_tgw.sh reconciles a node against. Identical for every node of the gateway; an
// empty attachment list removes everything of the gateway from the node.
type tgwState struct {
	Tgw         int64                 `json:"tgw"`
	Generation  int64                 `json:"generation"`
	Attachments []*tgwStateAttachment `json:"attachments"`
	Tables      []*tgwStateTable      `json:"tables"`
}

// loadTgwMembers returns the active attachments of a gateway with the networks of their VPCs, by attachment ID
// and ordered by slot
func loadTgwMembers(ctx context.Context, tgwID int64) (byID map[int64]*tgwMember, ordered []*tgwMember, err error) {
	ctx, db := GetContextDB(ctx)
	atts := []*model.TgwAttachment{}
	if err = db.Where("tgw_id = ? AND status IN ?", tgwID, TgwActiveAttachmentStatuses).Order("slot").Find(&atts).Error; err != nil {
		return nil, nil, NewCLError(ErrDatabaseError, "Failed to query transit gateway attachments", err)
	}
	byID = map[int64]*tgwMember{}
	for _, att := range atts {
		subnets, serr := vpcInternalCidrs(ctx, att.RouterID)
		if serr != nil {
			return nil, nil, serr
		}
		prefixes, perr := vpnPrefixesOfRouter(ctx, att.RouterID)
		if perr != nil {
			return nil, nil, perr
		}
		m := &tgwMember{Att: att, Subnets: subnets, VpnPrefixes: []string{}}
		for _, p := range prefixes {
			m.VpnPrefixes = append(m.VpnPrefixes, normalizeCidr(p.Cidr))
		}
		byID[att.ID] = m
		ordered = append(ordered, m)
	}
	return
}

// tgwTableData returns the route tables of a gateway with their propagations and static routes
func tgwTableData(ctx context.Context, tgwID int64) (tables []*model.TgwRouteTable, props []*model.TgwPropagation, routes []*model.TgwRoute, err error) {
	ctx, db := GetContextDB(ctx)
	if err = db.Where("tgw_id = ?", tgwID).Order("slot").Find(&tables).Error; err != nil {
		return nil, nil, nil, NewCLError(ErrDatabaseError, "Failed to query transit gateway route tables", err)
	}
	if err = db.Where("tgw_id = ?", tgwID).Order("id").Find(&props).Error; err != nil {
		return nil, nil, nil, NewCLError(ErrDatabaseError, "Failed to query transit gateway propagations", err)
	}
	if err = db.Where("tgw_id = ?", tgwID).Order("id").Find(&routes).Error; err != nil {
		return nil, nil, nil, NewCLError(ErrDatabaseError, "Failed to query transit gateway routes", err)
	}
	return
}

// TgwEffectiveRoutes computes the routes of one table as every node installs them
func TgwEffectiveRoutes(ctx context.Context, tgwID, tableID int64) (entries []*TgwRouteEntry, members map[int64]*tgwMember, err error) {
	members, _, err = loadTgwMembers(ctx, tgwID)
	if err != nil {
		return
	}
	_, props, routes, err := tgwTableData(ctx, tgwID)
	if err != nil {
		return
	}
	entries = computeTgwTableRoutes(tableID, members, props, routes)
	return
}

func buildTgwState(ctx context.Context, tgw *model.TransitGateway) (state *tgwState, ordered []*tgwMember, err error) {
	byID, ordered, err := loadTgwMembers(ctx, tgw.ID)
	if err != nil {
		return
	}
	tables, props, routes, err := tgwTableData(ctx, tgw.ID)
	if err != nil {
		return
	}
	state = assembleTgwState(tgw.ID, tgw.Generation, byID, ordered, tables, props, routes)
	return
}

// assembleTgwState computes the node state from the members and the route tables of a gateway
func assembleTgwState(tgwID, generation int64, byID map[int64]*tgwMember, ordered []*tgwMember, tables []*model.TgwRouteTable,
	props []*model.TgwPropagation, routes []*model.TgwRoute) (state *tgwState) {
	state = &tgwState{Tgw: tgwID, Generation: generation, Attachments: []*tgwStateAttachment{}, Tables: []*tgwStateTable{}}
	slotOf := map[int64]int32{}
	entriesOf := map[int64][]*TgwRouteEntry{}
	for _, t := range tables {
		slotOf[t.ID] = t.Slot
		entries := computeTgwTableRoutes(t.ID, byID, props, routes)
		entriesOf[t.ID] = entries
		st := &tgwStateTable{Table: tgwTableBase + t.Slot, Routes: []*tgwStateRoute{}}
		for _, e := range entries {
			st.Routes = append(st.Routes, &tgwStateRoute{Prefix: e.Prefix, Att: e.AttachmentID})
		}
		state.Tables = append(state.Tables, st)
	}
	for _, m := range ordered {
		routerIP, gatewayIP := tgwLinkIPs(m.Att.Slot)
		reachable, blackhole, throw := tgwRouterPrefixes(m, entriesOf[m.Att.RouteTableID], ordered)
		state.Attachments = append(state.Attachments, &tgwStateAttachment{ID: m.Att.ID, Router: m.Att.RouterID, RouterIP: routerIP,
			GatewayIP: gatewayIP, Table: tgwTableBase + slotOf[m.Att.RouteTableID], Reachable: reachable, Blackhole: blackhole, Throw: throw})
	}
	return
}

// tgwNodes is S(T) for some member routers: the nodes with an instance NIC of one of them, and the targets of the
// running migrations of their instances (a target routes for the instance as soon as it switches over), except
// the migration ignoreMigration (one being rolled back)
func tgwNodes(ctx context.Context, routers []int64, ignoreMigration ...int64) (nodes []int32, err error) {
	if len(routers) == 0 {
		return []int32{}, nil
	}
	ctx, db := GetContextDB(ctx)
	hypers := []int32{}
	if err = db.Model(&model.Interface{}).Distinct("hyper").Where("router_id IN ? AND type = 'instance' AND hyper >= 0", routers).
		Pluck("hyper", &hypers).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to query the nodes of the transit gateway", err)
	}
	targets := []int32{}
	q := db.Model(&model.Migration{}).Distinct("target_hyper").
		Where("status IN ? AND target_hyper >= 0 AND updated_at > ? AND instance_id IN (?)",
			[]string{"in_progress", "target_prepared", "source_prepared"}, time.Now().Add(-24*time.Hour),
			db.Model(&model.Instance{}).Select("id").Where("router_id IN ?", routers))
	if len(ignoreMigration) > 0 && ignoreMigration[0] > 0 {
		q = q.Where("id <> ?", ignoreMigration[0])
	}
	if err = q.Pluck("target_hyper", &targets).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to query the migrations of the transit gateway", err)
	}
	seen := map[int32]bool{}
	for _, h := range append(hypers, targets...) {
		if !seen[h] {
			seen[h] = true
			nodes = append(nodes, h)
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i] < nodes[j] })
	return
}

func tgwMemberRouters(members []*tgwMember) []int64 {
	routers := make([]int64, 0, len(members))
	for _, m := range members {
		routers = append(routers, m.Att.RouterID)
	}
	return routers
}

// TgwCurrentNodes is S(T) of a gateway as it stands
func TgwCurrentNodes(ctx context.Context, tgwID int64, ignoreMigration ...int64) (nodes []int32, err error) {
	_, members, err := loadTgwMembers(ctx, tgwID)
	if err != nil {
		return
	}
	return tgwNodes(ctx, tgwMemberRouters(members), ignoreMigration...)
}

// sendTgwState sends one state to some nodes in a single command. The member list is never empty: cland sends
// a command whose list is empty to every node. The command reaches the node in an environment variable (128 KiB
// per string), so a large state travels gzip compressed and base64 encoded ("gz:" prefix, apply_tgw.sh decodes it).
func sendTgwState(ctx context.Context, tgwID, generation int64, payload []byte, hypers []int32) (err error) {
	if len(hypers) == 0 {
		return
	}
	sorted := append([]int32{}, hypers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	body := string(payload)
	if len(payload) > tgwCompressAbove {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(payload)
		_ = zw.Close()
		body = "gz:" + base64.StdEncoding.EncodeToString(buf.Bytes())
	}
	if len(body) > tgwStateMax {
		logger.Ctx(ctx).Errorf("The state of transit gateway %d is %d bytes even compressed, more than a node command can carry", tgwID, len(body))
		return NewCLError(ErrInvalidParameter, "The transit gateway state is too large: remove static routes or route tables", nil)
	}
	control := fmt.Sprintf("inter=%d", sorted[0])
	if len(sorted) > 1 {
		control = fmt.Sprintf("toall=group-tgw-%d:%d", tgwID, sorted[0])
		for _, h := range sorted[1:] {
			control = fmt.Sprintf("%s,%d", control, h)
		}
	}
	command := fmt.Sprintf("/opt/cloudland/scripts/backend/apply_tgw.sh '%d' '%d' <<'EOF'\n%s\nEOF", tgwID, generation, body)
	if err = HyperExecute(ctx, control, command); err != nil {
		logger.Ctx(ctx).Errorf("Failed to dispatch the state of transit gateway %d to nodes %v: %v", tgwID, sorted, err)
		return
	}
	tgwNoteSent(tgwID, sorted)
	return
}

func emptyTgwPayload(tgwID, generation int64) []byte {
	payload, _ := json.Marshal(&tgwState{Tgw: tgwID, Generation: generation, Attachments: []*tgwStateAttachment{}, Tables: []*tgwStateTable{}})
	return payload
}

// Pacing of the resends (StartTgwWatchdog): a node that did not confirm a state is sent it again a minute after,
// then at doubling intervals up to ten minutes. In memory: after a restart of clapi every pending node is due.
type tgwSendMark struct {
	at   time.Time
	wait time.Duration
}

var (
	tgwSendMu sync.Mutex
	tgwSends  = map[[2]int64]*tgwSendMark{}
)

// tgwNoteSent records a send to some nodes; a new state starts the backoff over
func tgwNoteSent(tgwID int64, hypers []int32) {
	tgwSendMu.Lock()
	defer tgwSendMu.Unlock()
	for _, h := range hypers {
		tgwSends[[2]int64{tgwID, int64(h)}] = &tgwSendMark{at: time.Now(), wait: tgwRetryFirst}
	}
}

// tgwRetryDue tells whether a node that has not confirmed may be sent the state again, and if so counts the resend
func tgwRetryDue(tgwID int64, hostid int32) bool {
	tgwSendMu.Lock()
	defer tgwSendMu.Unlock()
	key := [2]int64{tgwID, int64(hostid)}
	mark := tgwSends[key]
	if mark != nil && time.Since(mark.at) < mark.wait {
		return false
	}
	wait := tgwRetryFirst
	if mark != nil {
		wait = mark.wait * 2
		if wait > tgwRetryMax {
			wait = tgwRetryMax
		}
	}
	tgwSends[key] = &tgwSendMark{at: time.Now(), wait: wait}
	return true
}

func tgwSettled(tgwID int64, hostid int32) {
	tgwSendMu.Lock()
	defer tgwSendMu.Unlock()
	delete(tgwSends, [2]int64{tgwID, int64(hostid)})
}

// tgwMarkLeaving records that nodes left the gateway and were sent the empty state of generation: their rows stay,
// as leaving, until they confirm it (TgwNodeApplied), and StartTgwWatchdog sends it again meanwhile
func tgwMarkLeaving(db *gorm.DB, tgwID, generation int64, hypers []int32) {
	for _, h := range hypers {
		if err := db.Exec(`INSERT INTO tgw_node_states (tgw_id, hyper, generation, status, reason, updated_at) VALUES (?, ?, ?, ?, '', ?)
			ON CONFLICT (tgw_id, hyper) DO UPDATE SET generation = EXCLUDED.generation, status = EXCLUDED.status, reason = '',
			updated_at = EXCLUDED.updated_at`, tgwID, h, generation, model.TgwNodeLeaving, time.Now()).Error; err != nil {
			logger.Errorf("Failed to mark node %d leaving transit gateway %d: %v", h, tgwID, err)
		}
	}
}

// tgwDispatch pushes the desired state of a gateway to the nodes of its members, and an empty one to the nodes
// that are no longer among them: those of previous (§2.4: the dispatch set of a change is S(T) before and after
// it) and every node that still has a state row
func tgwDispatch(ctx context.Context, tgwID int64, previous []int32) (err error) {
	ctx, db := GetContextDB(ctx)
	tgw := &model.TransitGateway{}
	if err = db.Where("id = ?", tgwID).Take(tgw).Error; err != nil {
		return NewCLError(ErrTgwNotFound, "Transit gateway not found", err)
	}
	state, members, err := buildTgwState(ctx, tgw)
	if err != nil {
		return
	}
	nodes, err := tgwNodes(ctx, tgwMemberRouters(members))
	if err != nil {
		return
	}
	payload, _ := json.Marshal(state)
	if err = sendTgwState(ctx, tgw.ID, tgw.Generation, payload, nodes); err != nil {
		return
	}
	in := map[int32]bool{}
	for _, n := range nodes {
		in[n] = true
	}
	rows := []int32{}
	if err = db.Model(&model.TgwNodeState{}).Where("tgw_id = ?", tgwID).Pluck("hyper", &rows).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to query the node states", err)
	}
	leavers := []int32{}
	for _, n := range append(append([]int32{}, previous...), rows...) {
		if !in[n] {
			leavers = append(leavers, n)
			in[n] = true
		}
	}
	if len(leavers) > 0 {
		tgwMarkLeaving(db, tgwID, tgw.Generation, leavers)
		for _, n := range leavers {
			tgwForgetResync(tgwID, n)
		}
		if err = sendTgwState(ctx, tgwID, tgw.Generation, emptyTgwPayload(tgwID, tgw.Generation), leavers); err != nil {
			return
		}
	}
	return
}

// tgwPending collects the dispatch of a change made in a transaction; run sends it after the commit, so a node
// confirming quickly finds the change in the database. syncFdb also exchanges the forwarding entries of the
// members (a VPC joined, or a resync)
type tgwPending struct {
	tgwID    int64
	previous []int32
	syncFdb  bool
}

func (p *tgwPending) run(ctx context.Context) {
	if p == nil {
		return
	}
	if err := tgwDispatch(ctx, p.tgwID, p.previous); err != nil {
		logger.Ctx(ctx).Errorf("Failed to dispatch transit gateway %d: %v", p.tgwID, err)
	}
	if p.syncFdb {
		if err := tgwSyncFdb(ctx, p.tgwID); err != nil {
			logger.Ctx(ctx).Errorf("Failed to exchange the forwarding entries of transit gateway %d: %v", p.tgwID, err)
		}
	}
	tgwEvaluate(ctx, p.tgwID)
}

// tgwSyncFdb gives every node of a gateway the VXLAN forwarding and neighbor entries of the instance NICs of all
// the members that sit on other nodes; add_fwrule.sh builds the router, the gateway port and the VXLAN link of
// each member VPC on the way. Needed when a VPC joins: until then a node only had the entries of the VPCs it
// hosts, and common.SendFdbRules (which covers the whole gateway) only runs when a NIC lands on a node.
func tgwSyncFdb(ctx context.Context, tgwID int64) (err error) {
	_, members, err := loadTgwMembers(ctx, tgwID)
	if err != nil {
		return
	}
	routers := tgwMemberRouters(members)
	nodes, err := tgwNodes(ctx, routers)
	if err != nil || len(nodes) == 0 {
		return
	}
	ctx, db := GetContextDB(ctx)
	ifaces := []*model.Interface{}
	if err = db.Preload("Address").Preload("Address.Subnet").Where("router_id IN ? AND type = 'instance' AND hyper >= 0", routers).
		Order("id").Find(&ifaces).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to query the NICs of the transit gateway members", err)
	}
	hypers := []*model.Hyper{}
	if err = db.Where("hostid >= 0").Find(&hypers).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to query the nodes", err)
	}
	hostIP := map[int32]string{}
	for _, h := range hypers {
		hostIP[h.Hostid] = h.HostIP
	}
	for _, n := range nodes {
		rules := []*FdbRule{}
		for _, iface := range ifaces {
			if iface.Hyper == n || iface.Address == nil || iface.Address.Subnet == nil || hostIP[iface.Hyper] == "" {
				continue
			}
			subnet := iface.Address.Subnet
			if subnet.Type == string(Public) || subnet.Type == string(Private) {
				continue
			}
			rules = append(rules, &FdbRule{Instance: iface.Name, Vni: subnet.Vlan, InnerIP: iface.Address.Address, InnerMac: iface.MacAddr,
				OuterIP: hostIP[iface.Hyper], Gateway: subnet.Gateway, Router: subnet.RouterID})
		}
		if len(rules) == 0 {
			continue
		}
		payload, _ := json.Marshal(rules)
		command := fmt.Sprintf("/opt/cloudland/scripts/backend/add_fwrule.sh <<'EOF'\n%s\nEOF", payload)
		if eerr := HyperExecute(ctx, fmt.Sprintf("inter=%d", n), command); eerr != nil {
			logger.Ctx(ctx).Errorf("Failed to send the forwarding entries of transit gateway %d to node %d: %v", tgwID, n, eerr)
		}
	}
	return
}

func lockTgw(db *gorm.DB, tgwID int64) (tgw *model.TransitGateway, err error) {
	tgw = &model.TransitGateway{}
	if err = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", tgwID).Take(tgw).Error; err != nil {
		return nil, NewCLError(ErrTgwNotFound, "Transit gateway not found", err)
	}
	return
}

// lockTgwRouter locks a VPC row. Attaching a VPC, adding a subnet to it and deleting it serialize on it; the lock
// order is the VPC first, then its gateway.
func lockTgwRouter(db *gorm.DB, routerID int64) (err error) {
	if err = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", routerID).Take(&model.Router{}).Error; err != nil {
		return NewCLError(ErrRouterNotFound, "VPC not found", err)
	}
	return
}

// bumpTgwGeneration marks a change of the desired state; the gateway row must be locked
func bumpTgwGeneration(db *gorm.DB, tgw *model.TransitGateway) (err error) {
	if err = db.Model(&model.TransitGateway{}).Where("id = ?", tgw.ID).Update("generation", gorm.Expr("generation + 1")).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to update the transit gateway", err)
	}
	if err = db.Select("generation").Where("id = ?", tgw.ID).Take(tgw).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to read the transit gateway", err)
	}
	return
}

// tgwOfRouter returns the active attachment of a VPC, nil when it has none
func tgwOfRouter(ctx context.Context, routerID int64) (att *model.TgwAttachment, err error) {
	if routerID <= 0 {
		return nil, nil
	}
	ctx, db := GetContextDB(ctx)
	att = &model.TgwAttachment{}
	if err = db.Where("router_id = ? AND status IN ?", routerID, TgwActiveAttachmentStatuses).Take(att).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, NewCLError(ErrDatabaseError, "Failed to query the transit gateway of the VPC", err)
	}
	return
}

// TgwRouterChanged marks a change of the networks of a VPC (a subnet added or deleted) in the caller's transaction
// and returns the dispatch to run once it is committed (nil when the VPC belongs to no gateway): a node must not
// apply a state the database may still roll back.
func TgwRouterChanged(ctx context.Context, routerID int64) (after func(context.Context), err error) {
	att, err := tgwOfRouter(ctx, routerID)
	if err != nil || att == nil {
		return
	}
	_, db := GetContextDB(ctx)
	tgw, err := lockTgw(db, att.TgwID)
	if err != nil {
		return
	}
	if err = bumpTgwGeneration(db, tgw); err != nil {
		return
	}
	pending := &tgwPending{tgwID: tgw.ID}
	return pending.run, nil
}

var (
	tgwResyncMu   sync.Mutex
	tgwResyncLast = map[[2]int64]time.Time{}
)

// tgwResyncDue limits the full push to one node to once per 10 s per gateway: a node rebuilding thirty instances
// of member VPCs reports them one by one
func tgwResyncDue(tgwID int64, hostid int32) bool {
	tgwResyncMu.Lock()
	defer tgwResyncMu.Unlock()
	last, ok := tgwResyncLast[[2]int64{tgwID, int64(hostid)}]
	return !ok || time.Since(last) >= 10*time.Second
}

func tgwResyncSent(tgwID int64, hostid int32) {
	tgwResyncMu.Lock()
	defer tgwResyncMu.Unlock()
	tgwResyncLast[[2]int64{tgwID, int64(hostid)}] = time.Now()
}

// tgwForgetResync drops the dedup of a node that left: it may join again within 10 s and must get the state
func tgwForgetResync(tgwID int64, hostid int32) {
	tgwResyncMu.Lock()
	defer tgwResyncMu.Unlock()
	delete(tgwResyncLast, [2]int64{tgwID, int64(hostid)})
}

// TgwResyncNode gives a node the state of the gateway of a VPC it (re)joined: a first instance of a member VPC on
// it, a migration target, or a node rebuilding its routers after a reboot. The state is the same everywhere,
// so the other nodes need nothing.
func TgwResyncNode(ctx context.Context, routerID int64, hostid int32) (err error) {
	if hostid < 0 {
		return
	}
	att, err := tgwOfRouter(ctx, routerID)
	if err != nil || att == nil {
		return
	}
	if !tgwResyncDue(att.TgwID, hostid) {
		return
	}
	ctx, db := GetContextDB(ctx)
	tgw := &model.TransitGateway{}
	if err = db.Where("id = ?", att.TgwID).Take(tgw).Error; err != nil {
		return NewCLError(ErrTgwNotFound, "Transit gateway not found", err)
	}
	state, _, err := buildTgwState(ctx, tgw)
	if err != nil {
		return
	}
	payload, _ := json.Marshal(state)
	if err = sendTgwState(ctx, tgw.ID, tgw.Generation, payload, []int32{hostid}); err == nil {
		tgwResyncSent(tgw.ID, hostid)
	}
	return
}

// TgwNodeCheckLeave takes a node out of the gateway of a VPC when no member is on it any more (an instance deleted
// or migrated away, a NIC detached, a migration that failed or was rolled back: ignoreMigration). The leave is a
// change of its own: the generation goes up, so a state still in flight to the node is older than the empty one it
// gets and dropped there. apply_tgw.sh then removes tgw-<ID> and hands the routers that were only there for the
// gateway to clear_local_router.sh.
func TgwNodeCheckLeave(ctx context.Context, routerID int64, hostid int32, ignoreMigration ...int64) (err error) {
	if hostid < 0 {
		return
	}
	att, err := tgwOfRouter(ctx, routerID)
	if err != nil || att == nil {
		return
	}
	nodes, err := TgwCurrentNodes(ctx, att.TgwID, ignoreMigration...)
	if err != nil {
		return
	}
	for _, n := range nodes {
		if n == hostid {
			return
		}
	}
	ctx, db := GetContextDB(ctx)
	tgw, err := lockTgw(db, att.TgwID)
	if err != nil {
		return
	}
	if err = bumpTgwGeneration(db, tgw); err != nil {
		return
	}
	return tgwDispatch(ctx, tgw.ID, []int32{hostid})
}

// TgwNodeApplied records what a node reported applying (apply_tgw.sh callback) and settles the attachments that
// waited for it. attachments is the number of attachments of the state: 0 is the empty state of a node that left,
// whose row goes once it confirmed (it stays as leaving, to be sent again, when the removal failed).
func TgwNodeApplied(ctx context.Context, tgwID int64, hostid int32, generation int64, ok bool, reason string, attachments int) (err error) {
	ctx, db := GetContextDB(ctx)
	if len(reason) > 500 {
		reason = reason[:500]
	}
	if attachments == 0 {
		if !ok {
			logger.Ctx(ctx).Warningf("Node %d failed to remove transit gateway %d: %s", hostid, tgwID, reason)
			return db.Model(&model.TgwNodeState{}).Where("tgw_id = ? AND hyper = ? AND status = ?", tgwID, hostid, model.TgwNodeLeaving).
				Update("reason", reason).Error
		}
		tgwSettled(tgwID, hostid)
		return db.Where("tgw_id = ? AND hyper = ? AND generation <= ?", tgwID, hostid, generation).Delete(&model.TgwNodeState{}).Error
	}
	var count int64
	if err = db.Model(&model.TransitGateway{}).Where("id = ?", tgwID).Count(&count).Error; err != nil || count == 0 {
		return
	}
	status := model.TgwNodeOK
	if !ok {
		status = model.TgwNodeError
		logger.Ctx(ctx).Warningf("Node %d failed to apply generation %d of transit gateway %d: %s", hostid, generation, tgwID, reason)
	}
	// A late report of an older generation never replaces a newer one
	err = db.Exec(`INSERT INTO tgw_node_states (tgw_id, hyper, generation, status, reason, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (tgw_id, hyper) DO UPDATE SET generation = EXCLUDED.generation, status = EXCLUDED.status,
		reason = EXCLUDED.reason, updated_at = EXCLUDED.updated_at WHERE tgw_node_states.generation <= EXCLUDED.generation`,
		tgwID, hostid, generation, status, reason, time.Now()).Error
	if err != nil {
		return NewCLError(ErrDatabaseError, "Failed to record the node state of the transit gateway", err)
	}
	tgwEvaluate(ctx, tgwID)
	return
}

// tgwOnlineNodes keeps the nodes clapi can reach: a node cland reports gone (status 10) or still being deployed
// is resynced when it comes back and does not hold an attachment up
func tgwOnlineNodes(ctx context.Context, nodes []int32) (online []int32, err error) {
	if len(nodes) == 0 {
		return []int32{}, nil
	}
	ctx, db := GetContextDB(ctx)
	err = db.Model(&model.Hyper{}).Where("hostid IN ? AND status IN ?", nodes, []int32{0, 1, 2}).Order("hostid").Pluck("hostid", &online).Error
	return
}

// tgwEvaluate settles the attachments of a gateway waiting for the nodes: attaching (or in error) becomes
// available once every node of the gateway applied its generation, detaching is completed. A node that failed
// puts an attachment in error; nodes silent for too long are named in the reason. Every write is conditional on
// the status and generation it was decided on: a detach or another change in between wins.
func tgwEvaluate(ctx context.Context, tgwID int64) {
	ctx, db := GetContextDB(ctx)
	atts := []*model.TgwAttachment{}
	if err := db.Where("tgw_id = ? AND status IN ?", tgwID, []string{model.TgwAttachmentAttaching, model.TgwAttachmentDetaching, model.TgwAttachmentError}).
		Find(&atts).Error; err != nil || len(atts) == 0 {
		return
	}
	nodes, err := TgwCurrentNodes(ctx, tgwID)
	if err != nil {
		return
	}
	online, err := tgwOnlineNodes(ctx, nodes)
	if err != nil {
		return
	}
	states := []*model.TgwNodeState{}
	if err = db.Where("tgw_id = ?", tgwID).Find(&states).Error; err != nil {
		return
	}
	stateOf := map[int32]*model.TgwNodeState{}
	for _, s := range states {
		stateOf[s.Hyper] = s
	}
	for _, att := range atts {
		pending, failed := []string{}, []string{}
		for _, n := range online {
			s := stateOf[n]
			switch {
			case s == nil || s.Generation < att.Generation || s.Status == model.TgwNodeLeaving:
				pending = append(pending, fmt.Sprintf("%d", n))
			case s.Status == model.TgwNodeError:
				failed = append(failed, fmt.Sprintf("node %d: %s", n, s.Reason))
			}
		}
		updates := map[string]interface{}{}
		detaching := att.Status == model.TgwAttachmentDetaching
		// A detach keeps its status and only carries the reason; it is completed or retried
		set := func(status, reason string) {
			if len(reason) > 500 {
				reason = reason[:500]
			}
			if !detaching && att.Status != status {
				updates["status"] = status
			}
			if att.StatusReason != reason {
				updates["status_reason"] = reason
			}
		}
		same := db.Where("id = ? AND status = ? AND generation = ?", att.ID, att.Status, att.Generation)
		switch {
		case len(failed) > 0:
			set(model.TgwAttachmentError, strings.Join(failed, "; "))
		case len(pending) == 0:
			if detaching {
				if err := same.Delete(&model.TgwAttachment{}).Error; err != nil {
					logger.Ctx(ctx).Errorf("Failed to complete the detach of attachment %d: %v", att.ID, err)
				}
				continue
			}
			set(model.TgwAttachmentAvailable, "")
		case time.Since(att.UpdatedAt) > tgwConfirmTimeout:
			set(model.TgwAttachmentError, tgwWaitReason+strings.Join(pending, ","))
		case att.StatusReason != "" && !strings.HasPrefix(att.StatusReason, tgwWaitReason):
			// The node that failed applied it since, others have yet to confirm: back to waiting. A reason naming the
			// silent nodes stays (reverting it would restart the timeout and flip the status every few minutes)
			set(model.TgwAttachmentAttaching, "")
		}
		if len(updates) > 0 {
			if err := db.Model(&model.TgwAttachment{}).Where("id = ? AND status = ? AND generation = ?", att.ID, att.Status, att.Generation).
				Updates(updates).Error; err != nil {
				logger.Ctx(ctx).Errorf("Failed to update attachment %d: %v", att.ID, err)
			}
		}
	}
}

// tgwRetry sends the state again to the nodes that did not confirm it: the nodes of the gateway behind its
// generation or in error (a dispatch lost while a node was disconnected, a cland restart, a failed apply), and the
// nodes that left and did not confirm the empty state (an offline node is skipped until it is back). A gateway that
// was deleted only has leaving nodes left.
func tgwRetry(ctx context.Context, tgwID int64) {
	ctx, db := GetContextDB(ctx)
	rows := []*model.TgwNodeState{}
	if err := db.Where("tgw_id = ?", tgwID).Find(&rows).Error; err != nil {
		return
	}
	rowOf := map[int32]*model.TgwNodeState{}
	hosts := []int32{}
	for _, r := range rows {
		rowOf[r.Hyper] = r
		hosts = append(hosts, r.Hyper)
	}
	tgw := &model.TransitGateway{}
	nodes := []int32{}
	exists := db.Where("id = ?", tgwID).Take(tgw).Error == nil
	if exists {
		var err error
		if nodes, err = TgwCurrentNodes(ctx, tgwID); err != nil {
			return
		}
	}
	online, err := tgwOnlineNodes(ctx, append(append([]int32{}, nodes...), hosts...))
	if err != nil {
		return
	}
	isOnline := map[int32]bool{}
	for _, n := range online {
		isOnline[n] = true
	}
	inS := map[int32]bool{}
	behind := []int32{}
	for _, n := range nodes {
		inS[n] = true
		r := rowOf[n]
		if r != nil && r.Status == model.TgwNodeOK && r.Generation >= tgw.Generation {
			tgwSettled(tgwID, n)
			continue
		}
		if isOnline[n] && tgwRetryDue(tgwID, n) {
			behind = append(behind, n)
		}
	}
	if len(behind) > 0 {
		state, _, berr := buildTgwState(ctx, tgw)
		if berr == nil {
			payload, _ := json.Marshal(state)
			logger.Ctx(ctx).Infof("Sending generation %d of transit gateway %d again to nodes %v", tgw.Generation, tgwID, behind)
			_ = sendTgwState(ctx, tgwID, tgw.Generation, payload, behind)
		}
	}
	for _, r := range rows {
		if inS[r.Hyper] {
			continue
		}
		generation := r.Generation
		if r.Status != model.TgwNodeLeaving {
			// A node outside the gateway that reported a full state (a migration target that never got the instance,
			// a stale state applied late): it leaves now
			if exists && tgw.Generation > generation {
				generation = tgw.Generation
			}
			tgwMarkLeaving(db, tgwID, generation, []int32{r.Hyper})
		} else if !isOnline[r.Hyper] || !tgwRetryDue(tgwID, r.Hyper) {
			continue
		}
		logger.Ctx(ctx).Infof("Sending the empty state of transit gateway %d (generation %d) again to node %d", tgwID, generation, r.Hyper)
		_ = sendTgwState(ctx, tgwID, generation, emptyTgwPayload(tgwID, generation), []int32{r.Hyper})
	}
}

// tgwSendLeaving sends the empty state to every node of a gateway that has not confirmed leaving it (a gateway
// just deleted)
func tgwSendLeaving(ctx context.Context, tgwID int64) {
	_, db := GetContextDB(ctx)
	rows := []*model.TgwNodeState{}
	if err := db.Where("tgw_id = ? AND status = ?", tgwID, model.TgwNodeLeaving).Find(&rows).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query the leaving nodes of transit gateway %d: %v", tgwID, err)
		return
	}
	for _, r := range rows {
		_ = sendTgwState(ctx, tgwID, r.Generation, emptyTgwPayload(tgwID, r.Generation), []int32{r.Hyper})
	}
}

// StartTgwWatchdog sends states again to the nodes that did not confirm them, settles the attachments whose
// confirmation raced with the request that changed them, and names the nodes an attachment still waits for after
// tgwConfirmTimeout
func StartTgwWatchdog() {
	go func() {
		time.Sleep(30 * time.Second)
		for {
			ctx, span := tracing.StartBackground(context.Background(), "tgw.watchdog")
			_, db := GetContextDB(ctx)
			ids, extra := []int64{}, []int64{}
			if err := db.Model(&model.TransitGateway{}).Pluck("id", &ids).Error; err != nil {
				logger.Ctx(ctx).Errorf("Failed to query the transit gateways: %v", err)
			}
			if err := db.Model(&model.TgwNodeState{}).Distinct("tgw_id").Pluck("tgw_id", &extra).Error; err != nil {
				logger.Ctx(ctx).Errorf("Failed to query the transit gateway node states: %v", err)
			}
			seen := map[int64]bool{}
			for _, id := range append(ids, extra...) {
				if seen[id] {
					continue
				}
				seen[id] = true
				tgwRetry(ctx, id)
				tgwEvaluate(ctx, id)
			}
			span.End()
			time.Sleep(15 * time.Second)
		}
	}()
}

// TgwAsymmetry is a pair of members where one reaches the other but the other has no route back: replies are
// dropped and nothing gets through (§2.5)
type TgwAsymmetry struct {
	From *model.TgwAttachment
	To   *model.TgwAttachment
}

// TgwAsymmetries lists the one-way member pairs of a gateway
func TgwAsymmetries(ctx context.Context, tgw *model.TransitGateway) (pairs []*TgwAsymmetry, err error) {
	state, members, err := buildTgwState(ctx, tgw)
	if err != nil {
		return
	}
	reach := map[int64][]string{}
	for _, a := range state.Attachments {
		reach[a.ID] = a.Reachable
	}
	covers := func(from *tgwMember, to *tgwMember) bool {
		for _, r := range reach[from.Att.ID] {
			for _, s := range to.Subnets {
				if cidrsOverlap(r, s) {
					return true
				}
			}
		}
		return false
	}
	for _, x := range members {
		for _, y := range members {
			if x.Att.ID != y.Att.ID && covers(x, y) && !covers(y, x) {
				pairs = append(pairs, &TgwAsymmetry{From: x.Att, To: y.Att})
			}
		}
	}
	return
}

// tgwReservedConflict reports a network of a member VPC that may not take part in a gateway
func tgwReservedConflict(cidr string) error {
	if cidrsOverlap(cidr, vrrpSubnetCidr) {
		return NewCLError(ErrTgwCidrConflict, fmt.Sprintf("Network %s overlaps the VRRP subnet %s, which every VPC of a transit gateway keeps for load balancers and VPN gateways", cidr, vrrpSubnetCidr), nil)
	}
	if cidrsOverlap(cidr, TgwLinkCidr) {
		return NewCLError(ErrTgwCidrConflict, fmt.Sprintf("Network %s overlaps the transit gateway link range %s", cidr, TgwLinkCidr), nil)
	}
	return nil
}

func routerName(ctx context.Context, routerID int64) string {
	_, db := GetContextDB(ctx)
	r := &model.Router{}
	if db.Unscoped().Select("name").Where("id = ?", routerID).Take(r).Error != nil {
		return fmt.Sprintf("%d", routerID)
	}
	return r.Name
}

// vpnPrefixesOfRouter returns the networks the VPN gateway of a VPC routes (remote networks, BGP summaries, the
// client pool), empty when it has none
func vpnPrefixesOfRouter(ctx context.Context, routerID int64) (prefixes []*model.VpnRemotePrefix, err error) {
	ctx, db := GetContextDB(ctx)
	gateway := &model.VpnGateway{}
	if err = db.Select("id").Where("router_id = ?", routerID).Take(gateway).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, NewCLError(ErrDatabaseError, "Failed to query the VPN gateway of the VPC", err)
	}
	if err = db.Where("vpn_gateway_id = ?", gateway.ID).Find(&prefixes).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to query VPN prefixes", err)
	}
	return
}

// tgwCheckNetworks checks the networks a VPC brings into a gateway (all of its subnets on attach, one new subnet
// later) against the other members: their subnets and the networks of their VPN gateways must not overlap
// (§2.5). others are the routers of the other members.
func tgwCheckNetworks(ctx context.Context, routerID int64, cidrs []string, others []int64) (err error) {
	for _, c := range cidrs {
		if err = tgwReservedConflict(c); err != nil {
			return
		}
	}
	for _, other := range others {
		subnets, serr := vpcInternalCidrs(ctx, other)
		if serr != nil {
			return serr
		}
		for _, c := range cidrs {
			for _, s := range subnets {
				if cidrsOverlap(c, s) {
					return NewCLError(ErrTgwCidrConflict, fmt.Sprintf("Network %s overlaps %s of VPC %s on the same transit gateway", c, s, routerName(ctx, other)), nil)
				}
			}
		}
		prefixes, perr := vpnPrefixesOfRouter(ctx, other)
		if perr != nil {
			return perr
		}
		for _, c := range cidrs {
			for _, p := range prefixes {
				if cidrsOverlap(c, p.Cidr) {
					return NewCLError(ErrTgwCidrConflict, fmt.Sprintf("Network %s overlaps %s, routed by the VPN gateway of VPC %s on the same transit gateway", c, p.Cidr, routerName(ctx, other)), nil)
				}
			}
		}
	}
	return
}

// tgwCheckAttach checks a VPC joining a gateway: its networks against the members and the reserved ranges, the
// networks of its own VPN gateway against the members' subnets, and its VPN tunnel link addresses against the
// link range of the gateway
func tgwCheckAttach(ctx context.Context, routerID int64, others []int64) (err error) {
	cidrs, err := vpcInternalCidrs(ctx, routerID)
	if err != nil {
		return
	}
	if err = tgwCheckNetworks(ctx, routerID, cidrs, others); err != nil {
		return
	}
	prefixes, err := vpnPrefixesOfRouter(ctx, routerID)
	if err != nil {
		return
	}
	for _, other := range others {
		subnets, serr := vpcInternalCidrs(ctx, other)
		if serr != nil {
			return serr
		}
		for _, p := range prefixes {
			for _, s := range subnets {
				if cidrsOverlap(p.Cidr, s) {
					return NewCLError(ErrTgwCidrConflict, fmt.Sprintf("Network %s of the VPN gateway of this VPC overlaps %s of VPC %s on the transit gateway", p.Cidr, s, routerName(ctx, other)), nil)
				}
			}
		}
	}
	ctx, db := GetContextDB(ctx)
	tunnels := []*model.VpnTunnel{}
	if err = db.Where("vpn_gateway_id IN (?)", db.Model(&model.VpnGateway{}).Select("id").Where("router_id = ?", routerID)).Find(&tunnels).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to query VPN tunnels", err)
	}
	_, link, _ := net.ParseCIDR(TgwLinkCidr)
	for _, t := range tunnels {
		for _, a := range []string{t.TunnelLocalIP, t.TunnelPeerIP} {
			if ip := net.ParseIP(ipOnly(a)); ip != nil && link.Contains(ip) {
				return NewCLError(ErrTgwCidrConflict, fmt.Sprintf("VPN tunnel address %s of this VPC is in the transit gateway link range %s", a, TgwLinkCidr), nil)
			}
		}
	}
	return
}

// ValidateTgwNewSubnet checks a subnet about to be created in a VPC that belongs to a gateway. The gateway row is
// locked, so two members adding overlapping subnets at the same time can not both pass.
func ValidateTgwNewSubnet(ctx context.Context, routerID int64, network string) (err error) {
	// The VPC row first: an attach running at the same time checks the subnets of the VPC under the same lock
	_, db := GetContextDB(ctx)
	if err = lockTgwRouter(db, routerID); err != nil {
		return
	}
	att, err := tgwOfRouter(ctx, routerID)
	if err != nil || att == nil {
		return
	}
	ctx, db = GetContextDB(ctx)
	if _, err = lockTgw(db, att.TgwID); err != nil {
		return
	}
	others := []int64{}
	if err = db.Model(&model.TgwAttachment{}).Where("tgw_id = ? AND status IN ? AND router_id <> ?", att.TgwID, TgwActiveAttachmentStatuses, routerID).
		Pluck("router_id", &others).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to query the transit gateway members", err)
	}
	return tgwCheckNetworks(ctx, routerID, []string{normalizeCidr(network)}, others)
}

// tgwCheckVpnPrefixes rejects VPN networks of a gateway member that overlap the subnets of the other members:
// in the member's router the transit gateway table is looked up first and would take that traffic
func tgwCheckVpnPrefixes(ctx context.Context, routerID int64, cidrs []string) (err error) {
	att, err := tgwOfRouter(ctx, routerID)
	if err != nil || att == nil {
		return
	}
	ctx, db := GetContextDB(ctx)
	others := []int64{}
	if err = db.Model(&model.TgwAttachment{}).Where("tgw_id = ? AND status IN ? AND router_id <> ?", att.TgwID, TgwActiveAttachmentStatuses, routerID).
		Pluck("router_id", &others).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to query the transit gateway members", err)
	}
	for _, other := range others {
		subnets, serr := vpcInternalCidrs(ctx, other)
		if serr != nil {
			return serr
		}
		for _, c := range cidrs {
			for _, s := range subnets {
				if cidrsOverlap(c, s) {
					return NewCLError(ErrVpnCidrConflict, fmt.Sprintf("Network %s overlaps %s of VPC %s, attached to the same transit gateway", c, s, routerName(ctx, other)), nil)
				}
			}
		}
	}
	return
}
