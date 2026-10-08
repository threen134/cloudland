/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	. "api/src/common"
	"api/src/dbs"
	"api/src/model"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var TransitGatewayAdmin = &TransitGatewayAdminService{}

type TransitGatewayAdminService struct{}

func tgwUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Create makes a gateway with its default route table
func (a *TransitGatewayAdminService) Create(ctx context.Context, name, description string) (tgw *model.TransitGateway, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckOrgPermission(model.OrgWriter) {
		return nil, NewCLError(ErrPermissionDenied, "Not authorized to create transit gateways", nil)
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	var count int64
	if err = db.Model(&model.TransitGateway{}).Where("owner = ? AND name = ?", memberShip.OrgID, name).Count(&count).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to query transit gateways", err)
	}
	if count > 0 {
		return nil, NewCLError(ErrTgwExists, fmt.Sprintf("Transit gateway %s exists already", name), nil)
	}
	tgw = &model.TransitGateway{Model: model.Model{Creater: memberShip.UserID}, Owner: memberShip.OrgID, Name: name,
		Description: description, Status: model.TgwStatusAvailable}
	if err = db.Create(tgw).Error; err != nil {
		if tgwUniqueViolation(err) {
			return nil, NewCLError(ErrTgwExists, fmt.Sprintf("Transit gateway %s exists already", name), nil)
		}
		return nil, NewCLError(ErrDatabaseError, "Failed to create the transit gateway", err)
	}
	table := &model.TgwRouteTable{Model: model.Model{Creater: memberShip.UserID}, Owner: tgw.Owner, TgwID: tgw.ID, Name: "default", IsDefault: true, Slot: 0}
	if err = db.Create(table).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to create the default route table", err)
	}
	return
}

// GetByUUID finds a gateway the caller may read: system admins see every organization's
func (a *TransitGatewayAdminService) GetByUUID(ctx context.Context, uuID string) (tgw *model.TransitGateway, err error) {
	ctx, db := GetContextDB(ctx)
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckOrgPermission(model.OrgReader) {
		return nil, NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
	}
	where, args := memberShip.GetOrgFilter()
	tgw = &model.TransitGateway{}
	if err = db.Where(where, args...).Where("uuid = ?", uuID).Take(tgw).Error; err != nil {
		return nil, NewCLError(ErrTgwNotFound, "Transit gateway not found", err)
	}
	return
}

func (a *TransitGatewayAdminService) List(ctx context.Context, offset, limit int64, order, query string) (total int64, tgws []*model.TransitGateway, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckOrgPermission(model.OrgReader) {
		err = NewCLError(ErrPermissionDenied, "Not authorized for this operation", nil)
		return
	}
	ctx, db := GetContextDB(ctx)
	if limit == 0 {
		limit = 16
	}
	if order == "" {
		order = "-created_at"
	}
	where, args := memberShip.GetOrgFilter()
	filter := func(tx *gorm.DB) *gorm.DB {
		return tx.Where(where, args...).Scopes(dbs.Contains(query, "name"))
	}
	tgws = []*model.TransitGateway{}
	if err = db.Model(&model.TransitGateway{}).Scopes(filter).Count(&total).Error; err != nil {
		err = NewCLError(ErrDatabaseError, "Failed to count transit gateways", err)
		return
	}
	_, db = GetContextDB(ctx)
	db = dbs.Sortby(db.Offset(int(offset)).Limit(int(limit)), order)
	if err = db.Scopes(filter).Find(&tgws).Error; err != nil {
		err = NewCLError(ErrDatabaseError, "Failed to query transit gateways", err)
	}
	return
}

// AttachmentCounts counts the attachments of some gateways in one query
func (a *TransitGatewayAdminService) AttachmentCounts(ctx context.Context, tgws []*model.TransitGateway) (counts map[int64]int, err error) {
	counts = map[int64]int{}
	if len(tgws) == 0 {
		return
	}
	ids := make([]int64, len(tgws))
	for i, t := range tgws {
		ids[i] = t.ID
	}
	_, db := GetContextDB(ctx)
	rows := []struct {
		TgwID int64
		N     int
	}{}
	if err = db.Model(&model.TgwAttachment{}).Select("tgw_id, COUNT(*) AS n").Where("tgw_id IN ?", ids).Group("tgw_id").Scan(&rows).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to count transit gateway attachments", err)
	}
	for _, r := range rows {
		counts[r.TgwID] = r.N
	}
	return
}

func (a *TransitGatewayAdminService) Update(ctx context.Context, tgw *model.TransitGateway, name, description *string) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to update the transit gateway", nil)
	}
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	updates := map[string]interface{}{}
	if name != nil && *name != tgw.Name {
		var count int64
		if err = db.Model(&model.TransitGateway{}).Where("owner = ? AND name = ? AND id <> ?", tgw.Owner, *name, tgw.ID).Count(&count).Error; err != nil {
			return NewCLError(ErrDatabaseError, "Failed to query transit gateways", err)
		}
		if count > 0 {
			return NewCLError(ErrTgwExists, fmt.Sprintf("Transit gateway %s exists already", *name), nil)
		}
		updates["name"] = *name
		tgw.Name = *name
	}
	if description != nil && *description != tgw.Description {
		updates["description"] = *description
		tgw.Description = *description
	}
	if len(updates) == 0 {
		return
	}
	if err = db.Model(&model.TransitGateway{}).Where("id = ?", tgw.ID).Updates(updates).Error; err != nil {
		if tgwUniqueViolation(err) {
			return NewCLError(ErrTgwExists, fmt.Sprintf("Transit gateway %s exists already", tgw.Name), nil)
		}
		return NewCLError(ErrDatabaseError, "Failed to update the transit gateway", err)
	}
	return
}

// Delete removes a gateway without attachments, with its route tables, propagations and routes. A detach still
// waiting for the nodes counts as an attachment.
func (a *TransitGatewayAdminService) Delete(ctx context.Context, tgw *model.TransitGateway) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to delete the transit gateway", nil)
	}
	outerCtx := ctx
	tgwID := tgw.ID
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
			if err == nil {
				tgwSendLeaving(outerCtx, tgwID)
			}
		} else if err == nil {
			tgwSendLeaving(ctx, tgwID)
		}
	}()
	if tgw, err = lockTgw(db, tgw.ID); err != nil {
		return
	}
	var count int64
	if err = db.Model(&model.TgwAttachment{}).Where("tgw_id = ?", tgw.ID).Count(&count).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to count the attachments", err)
	}
	if count > 0 {
		return NewCLError(ErrTgwInUse, fmt.Sprintf("Transit gateway %s still has %d attachment(s); detach the VPCs first", tgw.Name, count), nil)
	}
	for _, target := range []interface{}{&model.TgwRoute{}, &model.TgwPropagation{}, &model.TgwRouteTable{}} {
		if err = db.Where("tgw_id = ?", tgw.ID).Delete(target).Error; err != nil {
			return NewCLError(ErrDatabaseError, "Failed to delete the route tables of the transit gateway", err)
		}
	}
	// Nodes that have not confirmed leaving yet keep their rows: StartTgwWatchdog sends them the empty state until
	// they do, the gateway being gone or not
	if err = db.Model(&model.TgwNodeState{}).Where("tgw_id = ? AND status <> ?", tgw.ID, model.TgwNodeLeaving).
		Updates(map[string]interface{}{"status": model.TgwNodeLeaving, "generation": tgw.Generation + 1, "reason": ""}).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to update the node states of the transit gateway", err)
	}
	if err = db.Delete(tgw).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to delete the transit gateway", err)
	}
	return
}

// Resync sends the current state to every node of the gateway again (troubleshooting, or retrying after a node
// failed); nodes that already applied it change nothing
func (a *TransitGatewayAdminService) Resync(ctx context.Context, tgw *model.TransitGateway) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to resync the transit gateway", nil)
	}
	(&tgwPending{tgwID: tgw.ID, syncFdb: true}).run(ctx)
	return
}

// NodeStates returns what each node of the gateway reported, with the nodes that never did
func (a *TransitGatewayAdminService) NodeStates(ctx context.Context, tgw *model.TransitGateway) (nodes []int32, states map[int32]*model.TgwNodeState, err error) {
	if nodes, err = TgwCurrentNodes(ctx, tgw.ID); err != nil {
		return
	}
	_, db := GetContextDB(ctx)
	rows := []*model.TgwNodeState{}
	if err = db.Where("tgw_id = ?", tgw.ID).Find(&rows).Error; err != nil {
		return nil, nil, NewCLError(ErrDatabaseError, "Failed to query the node states", err)
	}
	states = map[int32]*model.TgwNodeState{}
	for _, r := range rows {
		states[r.Hyper] = r
	}
	return
}

// Attachments returns the attachments of a gateway, the ones being detached included
func (a *TransitGatewayAdminService) Attachments(ctx context.Context, tgw *model.TransitGateway) (atts []*model.TgwAttachment, err error) {
	_, db := GetContextDB(ctx)
	if err = db.Preload("Router").Where("tgw_id = ?", tgw.ID).Order("slot").Find(&atts).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to query the attachments", err)
	}
	return
}

func (a *TransitGatewayAdminService) GetAttachment(ctx context.Context, tgw *model.TransitGateway, uuID string) (att *model.TgwAttachment, err error) {
	_, db := GetContextDB(ctx)
	att = &model.TgwAttachment{}
	if err = db.Preload("Router").Where("tgw_id = ? AND uuid = ?", tgw.ID, uuID).Take(att).Error; err != nil {
		return nil, NewCLError(ErrTgwAttachmentNotFound, "Attachment not found", err)
	}
	return
}

// AttachmentOfRouter returns the attachment of a VPC (with its gateway), nil when it has none
func (a *TransitGatewayAdminService) AttachmentOfRouter(ctx context.Context, routerID int64) (att *model.TgwAttachment, tgw *model.TransitGateway, err error) {
	_, db := GetContextDB(ctx)
	att = &model.TgwAttachment{}
	if err = db.Where("router_id = ?", routerID).Take(att).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil
		}
		return nil, nil, NewCLError(ErrDatabaseError, "Failed to query the attachment of the VPC", err)
	}
	tgw = &model.TransitGateway{}
	if err = db.Where("id = ?", att.TgwID).Take(tgw).Error; err != nil {
		return nil, nil, NewCLError(ErrTgwNotFound, "Transit gateway not found", err)
	}
	return
}

// AttachmentsOfRouters returns the attachment (with its gateway) of each of some VPCs that has one, in two queries
func (a *TransitGatewayAdminService) AttachmentsOfRouters(ctx context.Context, routerIDs []int64) (atts map[int64]*model.TgwAttachment, tgws map[int64]*model.TransitGateway, err error) {
	atts, tgws = map[int64]*model.TgwAttachment{}, map[int64]*model.TransitGateway{}
	if len(routerIDs) == 0 {
		return
	}
	_, db := GetContextDB(ctx)
	rows := []*model.TgwAttachment{}
	if err = db.Where("router_id IN ?", routerIDs).Find(&rows).Error; err != nil {
		return nil, nil, NewCLError(ErrDatabaseError, "Failed to query the attachments of the VPCs", err)
	}
	ids := []int64{}
	for _, r := range rows {
		atts[r.RouterID] = r
		ids = append(ids, r.TgwID)
	}
	if len(ids) == 0 {
		return
	}
	list := []*model.TransitGateway{}
	if err = db.Where("id IN ?", ids).Find(&list).Error; err != nil {
		return nil, nil, NewCLError(ErrDatabaseError, "Failed to query the transit gateways of the VPCs", err)
	}
	for _, t := range list {
		tgws[t.ID] = t
	}
	return
}

func (a *TransitGatewayAdminService) routeTableByID(db *gorm.DB, tgw *model.TransitGateway, id int64) (table *model.TgwRouteTable, err error) {
	table = &model.TgwRouteTable{}
	if err = db.Where("tgw_id = ? AND id = ?", tgw.ID, id).Take(table).Error; err != nil {
		return nil, NewCLError(ErrTgwRouteTableNotFound, "Route table not found", err)
	}
	return
}

// Attach connects a VPC of the gateway's organization (system admins included: members and their gateway are
// always in one organization). Everything is checked before anything is sent to a node. propagate adds the
// VPC's subnets to the default route table.
func (a *TransitGatewayAdminService) Attach(ctx context.Context, tgw *model.TransitGateway, router *model.Router, table *model.TgwRouteTable, propagate bool) (att *model.TgwAttachment, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return nil, NewCLError(ErrPermissionDenied, "Not authorized to change the transit gateway", nil)
	}
	if router.Owner != tgw.Owner {
		return nil, NewCLError(ErrPermissionDenied, "The VPC and the transit gateway belong to different organizations", nil)
	}
	outerCtx := ctx
	var pending *tgwPending
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
			if err == nil {
				pending.run(outerCtx)
			}
		} else if err == nil {
			pending.run(ctx)
		}
	}()
	// The VPC first, then the gateway (lockTgwRouter): a subnet being added to the VPC or a delete of it waits
	if err = lockTgwRouter(db, router.ID); err != nil {
		return
	}
	if tgw, err = lockTgw(db, tgw.ID); err != nil {
		return
	}
	var count int64
	if err = db.Model(&model.TgwAttachment{}).Where("router_id = ?", router.ID).Count(&count).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to query the attachments", err)
	}
	if count > 0 {
		return nil, NewCLError(ErrTgwAttachmentExists, fmt.Sprintf("VPC %s is attached to a transit gateway already", router.Name), nil)
	}
	atts := []*model.TgwAttachment{}
	if err = db.Where("tgw_id = ?", tgw.ID).Find(&atts).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to query the attachments", err)
	}
	if len(atts) >= MaxTgwAttachments {
		return nil, NewCLError(ErrTgwAttachmentLimit, fmt.Sprintf("A transit gateway can have at most %d attachments", MaxTgwAttachments), nil)
	}
	used := map[int32]bool{}
	others := []int64{}
	for _, x := range atts {
		used[x.Slot] = true
		if x.Status != model.TgwAttachmentDetaching {
			others = append(others, x.RouterID)
		}
	}
	if err = tgwCheckAttach(ctx, router.ID, others); err != nil {
		return
	}
	if table == nil {
		table = &model.TgwRouteTable{}
		if err = db.Where("tgw_id = ? AND is_default = ?", tgw.ID, true).Take(table).Error; err != nil {
			return nil, NewCLError(ErrTgwRouteTableNotFound, "Default route table not found", err)
		}
	} else if table.TgwID != tgw.ID {
		return nil, NewCLError(ErrTgwRouteTableNotFound, "Route table not found", nil)
	}
	slot := int32(0)
	for used[slot] {
		slot++
	}
	previous, err := tgwNodes(ctx, others)
	if err != nil {
		return
	}
	if err = bumpTgwGeneration(db, tgw); err != nil {
		return
	}
	att = &model.TgwAttachment{Model: model.Model{Creater: memberShip.UserID}, Owner: tgw.Owner, TgwID: tgw.ID, RouterID: router.ID,
		RouteTableID: table.ID, Slot: slot, Status: model.TgwAttachmentAttaching, Generation: tgw.Generation}
	if err = db.Create(att).Error; err != nil {
		if tgwUniqueViolation(err) {
			return nil, NewCLError(ErrTgwAttachmentExists, fmt.Sprintf("VPC %s is attached to a transit gateway already", router.Name), nil)
		}
		return nil, NewCLError(ErrDatabaseError, "Failed to create the attachment", err)
	}
	if propagate {
		defaultTable := table
		if !table.IsDefault {
			defaultTable = &model.TgwRouteTable{}
			if err = db.Where("tgw_id = ? AND is_default = ?", tgw.ID, true).Take(defaultTable).Error; err != nil {
				return nil, NewCLError(ErrTgwRouteTableNotFound, "Default route table not found", err)
			}
		}
		prop := &model.TgwPropagation{Model: model.Model{Creater: memberShip.UserID}, Owner: tgw.Owner, TgwID: tgw.ID, RouteTableID: defaultTable.ID, AttachmentID: att.ID}
		if err = db.Create(prop).Error; err != nil {
			return nil, NewCLError(ErrDatabaseError, "Failed to create the propagation", err)
		}
	}
	att.Router = router
	pending = &tgwPending{tgwID: tgw.ID, previous: previous, syncFdb: true}
	return
}

// UpdateAttachment associates an attachment with another route table
func (a *TransitGatewayAdminService) UpdateAttachment(ctx context.Context, tgw *model.TransitGateway, att *model.TgwAttachment, table *model.TgwRouteTable) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to change the transit gateway", nil)
	}
	if table.TgwID != tgw.ID {
		return NewCLError(ErrTgwRouteTableNotFound, "Route table not found", nil)
	}
	outerCtx := ctx
	var pending *tgwPending
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
			if err == nil {
				pending.run(outerCtx)
			}
		} else if err == nil {
			pending.run(ctx)
		}
	}()
	if tgw, err = lockTgw(db, tgw.ID); err != nil {
		return
	}
	if err = db.Where("id = ?", att.ID).Take(att).Error; err != nil {
		return NewCLError(ErrTgwAttachmentNotFound, "Attachment not found", err)
	}
	if att.Status == model.TgwAttachmentDetaching {
		return NewCLError(ErrTgwAttachmentBusy, "The attachment is being detached", nil)
	}
	if att.RouteTableID == table.ID {
		return
	}
	if err = bumpTgwGeneration(db, tgw); err != nil {
		return
	}
	if err = db.Model(&model.TgwAttachment{}).Where("id = ?", att.ID).Update("route_table_id", table.ID).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to update the attachment", err)
	}
	att.RouteTableID = table.ID
	pending = &tgwPending{tgwID: tgw.ID}
	return
}

// Detach takes a VPC out of the gateway. Its propagations and the static routes towards it go at once; the
// attachment stays as detaching until every node applied the state without it. Detaching again retries.
func (a *TransitGatewayAdminService) Detach(ctx context.Context, tgw *model.TransitGateway, att *model.TgwAttachment) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to change the transit gateway", nil)
	}
	outerCtx := ctx
	var pending *tgwPending
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
			if err == nil {
				pending.run(outerCtx)
			}
		} else if err == nil {
			pending.run(ctx)
		}
	}()
	if tgw, err = lockTgw(db, tgw.ID); err != nil {
		return
	}
	if err = db.Where("id = ? AND tgw_id = ?", att.ID, tgw.ID).Take(att).Error; err != nil {
		return NewCLError(ErrTgwAttachmentNotFound, "Attachment not found", err)
	}
	_, members, err := loadTgwMembers(ctx, tgw.ID)
	if err != nil {
		return
	}
	routers := tgwMemberRouters(members)
	if att.Status == model.TgwAttachmentDetaching {
		routers = append(routers, att.RouterID)
	}
	previous, err := tgwNodes(ctx, routers)
	if err != nil {
		return
	}
	if err = bumpTgwGeneration(db, tgw); err != nil {
		return
	}
	if err = db.Model(&model.TgwAttachment{}).Where("id = ?", att.ID).Updates(map[string]interface{}{
		"status": model.TgwAttachmentDetaching, "status_reason": "", "generation": tgw.Generation}).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to update the attachment", err)
	}
	if err = db.Where("attachment_id = ?", att.ID).Delete(&model.TgwPropagation{}).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to delete the propagations of the attachment", err)
	}
	if err = db.Where("tgw_id = ? AND attachment_id = ? AND type = ?", tgw.ID, att.ID, model.TgwRouteStatic).Delete(&model.TgwRoute{}).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to delete the routes towards the attachment", err)
	}
	pending = &tgwPending{tgwID: tgw.ID, previous: previous}
	return
}

// RouteTables returns the route tables of a gateway
func (a *TransitGatewayAdminService) RouteTables(ctx context.Context, tgw *model.TransitGateway) (tables []*model.TgwRouteTable, props []*model.TgwPropagation, routes []*model.TgwRoute, err error) {
	return tgwTableData(ctx, tgw.ID)
}

func (a *TransitGatewayAdminService) GetRouteTable(ctx context.Context, tgw *model.TransitGateway, uuID string) (table *model.TgwRouteTable, err error) {
	_, db := GetContextDB(ctx)
	table = &model.TgwRouteTable{}
	if err = db.Where("tgw_id = ? AND uuid = ?", tgw.ID, uuID).Take(table).Error; err != nil {
		return nil, NewCLError(ErrTgwRouteTableNotFound, "Route table not found", err)
	}
	return
}

// CreateRouteTable adds an empty table; it routes nothing until propagations or routes fill it
func (a *TransitGatewayAdminService) CreateRouteTable(ctx context.Context, tgw *model.TransitGateway, name string) (table *model.TgwRouteTable, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return nil, NewCLError(ErrPermissionDenied, "Not authorized to change the transit gateway", nil)
	}
	outerCtx := ctx
	var pending *tgwPending
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
			if err == nil {
				pending.run(outerCtx)
			}
		} else if err == nil {
			pending.run(ctx)
		}
	}()
	if tgw, err = lockTgw(db, tgw.ID); err != nil {
		return
	}
	tables := []*model.TgwRouteTable{}
	if err = db.Where("tgw_id = ?", tgw.ID).Find(&tables).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to query the route tables", err)
	}
	if len(tables) >= MaxTgwRouteTables {
		return nil, NewCLError(ErrTgwRouteTableExists, fmt.Sprintf("A transit gateway can have at most %d route tables", MaxTgwRouteTables), nil)
	}
	used := map[int32]bool{}
	for _, t := range tables {
		if t.Name == name {
			return nil, NewCLError(ErrTgwRouteTableExists, fmt.Sprintf("Route table %s exists already", name), nil)
		}
		used[t.Slot] = true
	}
	slot := int32(0)
	for used[slot] {
		slot++
	}
	if err = bumpTgwGeneration(db, tgw); err != nil {
		return
	}
	table = &model.TgwRouteTable{Model: model.Model{Creater: memberShip.UserID}, Owner: tgw.Owner, TgwID: tgw.ID, Name: name, Slot: slot}
	if err = db.Create(table).Error; err != nil {
		if tgwUniqueViolation(err) {
			return nil, NewCLError(ErrTgwRouteTableExists, fmt.Sprintf("Route table %s exists already", name), nil)
		}
		return nil, NewCLError(ErrDatabaseError, "Failed to create the route table", err)
	}
	pending = &tgwPending{tgwID: tgw.ID}
	return
}

func (a *TransitGatewayAdminService) RenameRouteTable(ctx context.Context, tgw *model.TransitGateway, table *model.TgwRouteTable, name string) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to change the transit gateway", nil)
	}
	if name == table.Name {
		return
	}
	_, db := GetContextDB(ctx)
	var count int64
	if err = db.Model(&model.TgwRouteTable{}).Where("tgw_id = ? AND name = ? AND id <> ?", tgw.ID, name, table.ID).Count(&count).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to query the route tables", err)
	}
	if count > 0 {
		return NewCLError(ErrTgwRouteTableExists, fmt.Sprintf("Route table %s exists already", name), nil)
	}
	if err = db.Model(&model.TgwRouteTable{}).Where("id = ?", table.ID).Update("name", name).Error; err != nil {
		if tgwUniqueViolation(err) {
			return NewCLError(ErrTgwRouteTableExists, fmt.Sprintf("Route table %s exists already", name), nil)
		}
		return NewCLError(ErrDatabaseError, "Failed to rename the route table", err)
	}
	table.Name = name
	return
}

// DeleteRouteTable removes a table no attachment is associated with; the default table stays
func (a *TransitGatewayAdminService) DeleteRouteTable(ctx context.Context, tgw *model.TransitGateway, table *model.TgwRouteTable) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to change the transit gateway", nil)
	}
	if table.IsDefault {
		return NewCLError(ErrTgwRouteTableInUse, "The default route table can not be deleted", nil)
	}
	outerCtx := ctx
	var pending *tgwPending
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
			if err == nil {
				pending.run(outerCtx)
			}
		} else if err == nil {
			pending.run(ctx)
		}
	}()
	if tgw, err = lockTgw(db, tgw.ID); err != nil {
		return
	}
	var count int64
	if err = db.Model(&model.TgwAttachment{}).Where("route_table_id = ?", table.ID).Count(&count).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to count the attachments", err)
	}
	if count > 0 {
		return NewCLError(ErrTgwRouteTableInUse, fmt.Sprintf("Route table %s is associated with %d attachment(s); associate them with another table first", table.Name, count), nil)
	}
	if err = bumpTgwGeneration(db, tgw); err != nil {
		return
	}
	for _, target := range []interface{}{&model.TgwRoute{}, &model.TgwPropagation{}} {
		if err = db.Where("route_table_id = ?", table.ID).Delete(target).Error; err != nil {
			return NewCLError(ErrDatabaseError, "Failed to delete the route table", err)
		}
	}
	if err = db.Delete(table).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to delete the route table", err)
	}
	pending = &tgwPending{tgwID: tgw.ID}
	return
}

// CreatePropagation makes the subnets of an attachment's VPC appear in a table. Every network of the allow list
// must overlap a subnet of that VPC: a subnet, a part of one or a network grouping some (§T3).
func (a *TransitGatewayAdminService) CreatePropagation(ctx context.Context, tgw *model.TransitGateway, table *model.TgwRouteTable, att *model.TgwAttachment, prefixes string) (prop *model.TgwPropagation, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return nil, NewCLError(ErrPermissionDenied, "Not authorized to change the transit gateway", nil)
	}
	allow, err := ParseCidrList(prefixes)
	if err != nil {
		return
	}
	outerCtx := ctx
	var pending *tgwPending
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
			if err == nil {
				pending.run(outerCtx)
			}
		} else if err == nil {
			pending.run(ctx)
		}
	}()
	if tgw, err = lockTgw(db, tgw.ID); err != nil {
		return
	}
	if err = db.Where("id = ?", att.ID).Take(att).Error; err != nil || att.TgwID != tgw.ID {
		return nil, NewCLError(ErrTgwAttachmentNotFound, "Attachment not found", err)
	}
	if att.Status == model.TgwAttachmentDetaching {
		return nil, NewCLError(ErrTgwAttachmentBusy, "The attachment is being detached", nil)
	}
	subnets, err := vpcInternalCidrs(ctx, att.RouterID)
	if err != nil {
		return
	}
	for _, e := range allow {
		match := false
		for _, s := range subnets {
			if cidrsOverlap(e, s) {
				match = true
				break
			}
		}
		if !match {
			return nil, NewCLError(ErrInvalidParameter, fmt.Sprintf("Network %s overlaps no subnet of the VPC of the attachment", e), nil)
		}
	}
	var count int64
	if err = db.Model(&model.TgwPropagation{}).Where("route_table_id = ? AND attachment_id = ?", table.ID, att.ID).Count(&count).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to query the propagations", err)
	}
	if count > 0 {
		return nil, NewCLError(ErrTgwPropagationExists, "The attachment propagates into this route table already", nil)
	}
	if err = bumpTgwGeneration(db, tgw); err != nil {
		return
	}
	prop = &model.TgwPropagation{Model: model.Model{Creater: memberShip.UserID}, Owner: tgw.Owner, TgwID: tgw.ID, RouteTableID: table.ID,
		AttachmentID: att.ID, Prefixes: joinCidrs(allow)}
	if err = db.Create(prop).Error; err != nil {
		if tgwUniqueViolation(err) {
			return nil, NewCLError(ErrTgwPropagationExists, "The attachment propagates into this route table already", nil)
		}
		return nil, NewCLError(ErrDatabaseError, "Failed to create the propagation", err)
	}
	pending = &tgwPending{tgwID: tgw.ID}
	return
}

func (a *TransitGatewayAdminService) GetPropagation(ctx context.Context, table *model.TgwRouteTable, uuID string) (prop *model.TgwPropagation, err error) {
	_, db := GetContextDB(ctx)
	prop = &model.TgwPropagation{}
	if err = db.Where("route_table_id = ? AND uuid = ?", table.ID, uuID).Take(prop).Error; err != nil {
		return nil, NewCLError(ErrTgwPropagationNotFound, "Propagation not found", err)
	}
	return
}

func (a *TransitGatewayAdminService) DeletePropagation(ctx context.Context, tgw *model.TransitGateway, prop *model.TgwPropagation) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to change the transit gateway", nil)
	}
	outerCtx := ctx
	var pending *tgwPending
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
			if err == nil {
				pending.run(outerCtx)
			}
		} else if err == nil {
			pending.run(ctx)
		}
	}()
	if tgw, err = lockTgw(db, tgw.ID); err != nil {
		return
	}
	if err = bumpTgwGeneration(db, tgw); err != nil {
		return
	}
	if err = db.Delete(prop).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to delete the propagation", err)
	}
	pending = &tgwPending{tgwID: tgw.ID}
	return
}

// validateTgwDestination checks the destination of a static route. A default route is refused: in a member's
// router the gateway table is looked up first, so it would take the VPC's internet traffic
func validateTgwDestination(destination string) (cidr string, err error) {
	ip, ipNet, perr := net.ParseCIDR(strings.TrimSpace(destination))
	if perr != nil || ip.To4() == nil {
		return "", NewCLError(ErrInvalidCIDR, "Invalid CIDR: "+destination, perr)
	}
	if ones, _ := ipNet.Mask.Size(); ones == 0 {
		return "", NewCLError(ErrTgwCidrConflict, "A default route can not go through a transit gateway: it would take the internet traffic of the VPCs", nil)
	}
	cidr = ipNet.String()
	if cidrsOverlap(cidr, routerLinkCidr) {
		return "", NewCLError(ErrTgwCidrConflict, fmt.Sprintf("Destination %s overlaps the router link range %s", cidr, routerLinkCidr), nil)
	}
	return
}

// CreateRoute adds a static route towards an attachment, or a blackhole when att is nil
func (a *TransitGatewayAdminService) CreateRoute(ctx context.Context, tgw *model.TransitGateway, table *model.TgwRouteTable, destination string, att *model.TgwAttachment) (route *model.TgwRoute, err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return nil, NewCLError(ErrPermissionDenied, "Not authorized to change the transit gateway", nil)
	}
	cidr, err := validateTgwDestination(destination)
	if err != nil {
		return
	}
	outerCtx := ctx
	var pending *tgwPending
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
			if err == nil {
				pending.run(outerCtx)
			}
		} else if err == nil {
			pending.run(ctx)
		}
	}()
	if tgw, err = lockTgw(db, tgw.ID); err != nil {
		return
	}
	routeType, attID := model.TgwRouteBlackhole, int64(0)
	if att != nil {
		if err = db.Where("id = ?", att.ID).Take(att).Error; err != nil || att.TgwID != tgw.ID {
			return nil, NewCLError(ErrTgwAttachmentNotFound, "Attachment not found", err)
		}
		if att.Status == model.TgwAttachmentDetaching {
			return nil, NewCLError(ErrTgwAttachmentBusy, "The attachment is being detached", nil)
		}
		routeType, attID = model.TgwRouteStatic, att.ID
		// The networks of a member's VPN gateway stay with that member: a route of the gateway table would
		// take them in the routers of the members associated with this table
		members, _, merr := loadTgwMembers(ctx, tgw.ID)
		if merr != nil {
			return nil, merr
		}
		for _, m := range members {
			prefixes, perr := vpnPrefixesOfRouter(ctx, m.Att.RouterID)
			if perr != nil {
				return nil, perr
			}
			for _, p := range prefixes {
				if cidrsOverlap(cidr, p.Cidr) {
					return nil, NewCLError(ErrTgwCidrConflict, fmt.Sprintf("Destination %s overlaps %s, routed by the VPN gateway of VPC %s", cidr, p.Cidr, routerName(ctx, m.Att.RouterID)), nil)
				}
			}
		}
	}
	var count int64
	if err = db.Model(&model.TgwRoute{}).Where("route_table_id = ?", table.ID).Count(&count).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to query the routes", err)
	}
	if count >= MaxTgwRoutesPerTable {
		return nil, NewCLError(ErrTgwRouteExists, fmt.Sprintf("A route table can have at most %d static routes", MaxTgwRoutesPerTable), nil)
	}
	if err = db.Model(&model.TgwRoute{}).Where("route_table_id = ? AND destination = ?", table.ID, cidr).Count(&count).Error; err != nil {
		return nil, NewCLError(ErrDatabaseError, "Failed to query the routes", err)
	}
	if count > 0 {
		return nil, NewCLError(ErrTgwRouteExists, fmt.Sprintf("Route table %s has a static route to %s already", table.Name, cidr), nil)
	}
	if err = bumpTgwGeneration(db, tgw); err != nil {
		return
	}
	route = &model.TgwRoute{Model: model.Model{Creater: memberShip.UserID}, Owner: tgw.Owner, TgwID: tgw.ID, RouteTableID: table.ID,
		Destination: cidr, AttachmentID: attID, Type: routeType}
	if err = db.Create(route).Error; err != nil {
		if tgwUniqueViolation(err) {
			return nil, NewCLError(ErrTgwRouteExists, fmt.Sprintf("Route table %s has a static route to %s already", table.Name, cidr), nil)
		}
		return nil, NewCLError(ErrDatabaseError, "Failed to create the route", err)
	}
	pending = &tgwPending{tgwID: tgw.ID}
	return
}

func (a *TransitGatewayAdminService) GetRoute(ctx context.Context, table *model.TgwRouteTable, uuID string) (route *model.TgwRoute, err error) {
	_, db := GetContextDB(ctx)
	route = &model.TgwRoute{}
	if err = db.Where("route_table_id = ? AND uuid = ?", table.ID, uuID).Take(route).Error; err != nil {
		return nil, NewCLError(ErrTgwRouteNotFound, "Route not found", err)
	}
	return
}

func (a *TransitGatewayAdminService) DeleteRoute(ctx context.Context, tgw *model.TransitGateway, route *model.TgwRoute) (err error) {
	memberShip := GetMemberShip(ctx)
	if !memberShip.CheckResourceOrg(model.OrgWriter, tgw.Owner) {
		return NewCLError(ErrPermissionDenied, "Not authorized to change the transit gateway", nil)
	}
	outerCtx := ctx
	var pending *tgwPending
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
			if err == nil {
				pending.run(outerCtx)
			}
		} else if err == nil {
			pending.run(ctx)
		}
	}()
	if tgw, err = lockTgw(db, tgw.ID); err != nil {
		return
	}
	if err = bumpTgwGeneration(db, tgw); err != nil {
		return
	}
	if err = db.Delete(route).Error; err != nil {
		return NewCLError(ErrDatabaseError, "Failed to delete the route", err)
	}
	pending = &tgwPending{tgwID: tgw.ID}
	return
}
