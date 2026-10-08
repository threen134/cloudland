/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var transitGatewayAPI = &TransitGatewayAPI{}
var tgwAdmin = services.TransitGatewayAdmin

type TransitGatewayAPI struct{}

type TransitGatewayResponse struct {
	*ResourceReference
	Description     string `json:"description"`
	Status          string `json:"status"`
	AttachmentCount int    `json:"attachment_count"`
	// Detail only. synced: every node of the gateway applied the latest change; syncing: some did not yet; error:
	// a node failed (see nodes)
	SyncStatus string             `json:"sync_status,omitempty"`
	Generation int64              `json:"generation,omitempty"`
	Nodes      []*TgwNodeResponse `json:"nodes,omitempty"`
	// Detail only: members that reach another member which has no route back (replies are dropped)
	AsymmetricRoutes []*TgwAsymmetryResponse `json:"asymmetric_routes,omitempty"`
}

type TgwAsymmetryResponse struct {
	From *TgwAttachmentRef `json:"from"`
	To   *TgwAttachmentRef `json:"to"`
}

// TgwNodeResponse is one node of the gateway: a node hosting an instance of a member VPC
type TgwNodeResponse struct {
	// Number of the node in this list (1, 2, …); the reasons of the attachments name the nodes by it for members
	// who are not system admins
	Index      int    `json:"index"`
	Hypervisor string `json:"hypervisor,omitempty"` // system admins only
	Generation int64  `json:"generation"`
	Status     string `json:"status"` // ok | error | pending | leaving (left the gateway, removal not confirmed yet)
	Reason     string `json:"reason,omitempty"`
}

type TransitGatewayListResponse struct {
	Offset          int                       `json:"offset"`
	Total           int                       `json:"total"`
	Limit           int                       `json:"limit"`
	TransitGateways []*TransitGatewayResponse `json:"transit_gateways"`
}

type TransitGatewayPayload struct {
	Name        string `json:"name" binding:"required,min=2,max=32"`
	Description string `json:"description" binding:"omitempty,max=255"`
}

type TransitGatewayPatchPayload struct {
	Name        *string `json:"name" binding:"omitempty,min=2,max=32"`
	Description *string `json:"description" binding:"omitempty,max=255"`
}

type TgwNamedRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TgwAttachmentRef names an attachment by its VPC
type TgwAttachmentRef struct {
	ID  string       `json:"id"`
	VPC *TgwNamedRef `json:"vpc"`
}

type TgwAttachmentResponse struct {
	ID           string       `json:"id"`
	VPC          *TgwNamedRef `json:"vpc"`
	RouteTable   *TgwNamedRef `json:"route_table"`
	Status       string       `json:"status"` // attaching | available | detaching | error
	StatusReason string       `json:"status_reason,omitempty"`
	Subnets      []string     `json:"subnets"`
	// The /31 of the attachment: the address in the VPC router (tr-) and in the gateway (ta-)
	RouterAddress  string `json:"router_address"`
	GatewayAddress string `json:"gateway_address"`
	CreatedAt      string `json:"created_at"`
}

type TgwAttachmentListResponse struct {
	Attachments []*TgwAttachmentResponse `json:"attachments"`
}

type TgwAttachmentPayload struct {
	VPC *BaseReference `json:"vpc" binding:"required"`
	// The route table the traffic of the VPC is routed by, the default table when left out
	RouteTable *BaseID `json:"route_table" binding:"omitempty"`
	// Add the subnets of the VPC to the default route table (true when left out)
	Propagate *bool `json:"propagate"`
}

type TgwAttachmentPatchPayload struct {
	RouteTable *BaseID `json:"route_table" binding:"required"`
}

type TgwPropagationResponse struct {
	ID         string            `json:"id"`
	Attachment *TgwAttachmentRef `json:"attachment"`
	Prefixes   []string          `json:"prefixes"` // empty: every subnet of the VPC
}

type TgwRouteResponse struct {
	ID          string            `json:"id"`
	Destination string            `json:"destination"`
	Type        string            `json:"type"` // static | blackhole
	Attachment  *TgwAttachmentRef `json:"attachment,omitempty"`
}

type TgwRouteTableResponse struct {
	ID           string                    `json:"id"`
	Name         string                    `json:"name"`
	IsDefault    bool                      `json:"is_default"`
	CreatedAt    string                    `json:"created_at"`
	Associations []*TgwAttachmentRef       `json:"associations"`
	Propagations []*TgwPropagationResponse `json:"propagations"`
	Routes       []*TgwRouteResponse       `json:"routes"`
}

type TgwRouteTableListResponse struct {
	RouteTables []*TgwRouteTableResponse `json:"route_tables"`
}

type TgwRouteTablePayload struct {
	Name string `json:"name" binding:"required,min=2,max=32"`
}

type TgwPropagationPayload struct {
	Attachment *BaseID `json:"attachment" binding:"required"`
	// Allow list: only the parts of the subnets inside these networks are propagated; empty propagates them all
	Prefixes []string `json:"prefixes" binding:"omitempty,max=16"`
}

type TgwRoutePayload struct {
	Destination string `json:"destination" binding:"required,max=64"`
	// Where the destination goes; leave out with blackhole true to drop it
	Attachment *BaseID `json:"attachment" binding:"omitempty"`
	Blackhole  bool    `json:"blackhole"`
}

type TgwEffectiveRouteResponse struct {
	Destination string            `json:"destination"`
	Type        string            `json:"type"` // propagated | static | blackhole
	Attachment  *TgwAttachmentRef `json:"attachment,omitempty"`
}

type TgwEffectiveRouteListResponse struct {
	Routes []*TgwEffectiveRouteResponse `json:"routes"`
}

// VPCTransitGatewayRef is the gateway a VPC is attached to
type VPCTransitGatewayRef struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	AttachmentID     string `json:"attachment_id"`
	AttachmentStatus string `json:"attachment_status"`
}

func (v *TransitGatewayAPI) getResponse(ctx context.Context, tgw *model.TransitGateway, attachments int) *TransitGatewayResponse {
	return &TransitGatewayResponse{
		ResourceReference: &ResourceReference{
			ID: tgw.UUID, Name: tgw.Name, Owner: orgAdmin.GetOrgName(ctx, tgw.Owner), OwnerUUID: orgAdmin.GetOrgUUID(ctx, tgw.Owner),
			CreatedAt: tgw.CreatedAt.Format(TimeStringForMat), UpdatedAt: tgw.UpdatedAt.Format(TimeStringForMat),
		},
		Description: tgw.Description, Status: tgw.Status, AttachmentCount: attachments,
	}
}

func (v *TransitGatewayAPI) getDetailResponse(ctx context.Context, tgw *model.TransitGateway) (resp *TransitGatewayResponse, err error) {
	counts, err := tgwAdmin.AttachmentCounts(ctx, []*model.TransitGateway{tgw})
	if err != nil {
		return
	}
	resp = v.getResponse(ctx, tgw, counts[tgw.ID])
	resp.Generation = tgw.Generation
	nodes, states, err := tgwAdmin.NodeStates(ctx, tgw)
	if err != nil {
		return
	}
	admin := GetMemberShip(ctx).CheckSystemPermission()
	hyperNames := map[int32]string{}
	if admin {
		if names, nerr := hyperAdmin.GetHyperNames(ctx); nerr == nil {
			hyperNames = names
		}
	}
	resp.SyncStatus = "synced"
	resp.Nodes = []*TgwNodeResponse{}
	// The nodes of the gateway, then the nodes that left it and have not confirmed the removal yet
	inGateway := map[int32]bool{}
	for _, n := range nodes {
		inGateway[n] = true
	}
	left := []int32{}
	for _, s := range states {
		if !inGateway[s.Hyper] {
			left = append(left, s.Hyper)
		}
	}
	sort.Slice(left, func(i, j int) bool { return left[i] < left[j] })
	nodes = append(nodes, left...)
	for i, n := range nodes {
		item := &TgwNodeResponse{Index: i + 1, Status: "pending"}
		if s := states[n]; s != nil {
			item.Generation, item.Status, item.Reason = s.Generation, s.Status, s.Reason
			if s.Status == model.TgwNodeOK && s.Generation < tgw.Generation {
				item.Status = "pending"
			}
		}
		if admin {
			item.Hypervisor = hyperNames[n]
		}
		switch {
		case item.Status == model.TgwNodeError:
			resp.SyncStatus = "error"
		case (item.Status == "pending" || item.Status == model.TgwNodeLeaving) && resp.SyncStatus != "error":
			resp.SyncStatus = "syncing"
		}
		resp.Nodes = append(resp.Nodes, item)
	}
	pairs, err := services.TgwAsymmetries(ctx, tgw)
	if err != nil {
		return
	}
	if len(pairs) > 0 {
		atts, aerr := tgwAdmin.Attachments(ctx, tgw)
		if aerr != nil {
			return nil, aerr
		}
		refs := v.attachmentRefs(atts)
		for _, p := range pairs {
			resp.AsymmetricRoutes = append(resp.AsymmetricRoutes, &TgwAsymmetryResponse{From: refs[p.From.ID], To: refs[p.To.ID]})
		}
	}
	return
}

func (v *TransitGatewayAPI) getTgw(c *gin.Context) (tgw *model.TransitGateway, ok bool) {
	tgw, err := tgwAdmin.GetByUUID(c.Request.Context(), c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid transit gateway query", err)
		return nil, false
	}
	return tgw, true
}

// attachmentRefs maps the attachments of a gateway (the ones being detached included) to their references
func (v *TransitGatewayAPI) attachmentRefs(atts []*model.TgwAttachment) map[int64]*TgwAttachmentRef {
	refs := map[int64]*TgwAttachmentRef{}
	for _, att := range atts {
		ref := &TgwAttachmentRef{ID: att.UUID, VPC: &TgwNamedRef{}}
		if att.Router != nil {
			ref.VPC.ID, ref.VPC.Name = att.Router.UUID, att.Router.Name
		}
		refs[att.ID] = ref
	}
	return refs
}

var tgwReasonNodeRe = regexp.MustCompile(`node (\d+):|waiting for nodes ([\d,]+)`)

// nodeLabels returns how the host IDs in the reasons of a gateway's attachments are shown: system admins read the
// host names, the other members the numbers of the node list of the gateway (which hides the names from them)
func (v *TransitGatewayAPI) nodeLabels(ctx context.Context, tgw *model.TransitGateway) func(string) string {
	admin := GetMemberShip(ctx).CheckSystemPermission()
	names := map[int32]string{}
	if admin {
		names, _ = hyperAdmin.GetHyperNames(ctx)
	}
	index := map[int32]int{}
	if nodes, err := services.TgwCurrentNodes(ctx, tgw.ID); err == nil {
		for i, n := range nodes {
			index[n] = i + 1
		}
	}
	label := func(id string) string {
		hostid, err := strconv.Atoi(id)
		if err != nil {
			return id
		}
		if admin && names[int32(hostid)] != "" {
			return names[int32(hostid)]
		}
		if i, ok := index[int32(hostid)]; ok && !admin {
			return strconv.Itoa(i)
		}
		if admin {
			return id
		}
		return "?"
	}
	return func(reason string) string { return rewriteTgwReason(reason, label) }
}

// rewriteTgwReason replaces the host IDs of the two reason shapes clapi writes ("node <id>: <message>" segments,
// "waiting for nodes <id>,<id>") with label(<id>)
func rewriteTgwReason(reason string, label func(string) string) string {
	return tgwReasonNodeRe.ReplaceAllStringFunc(reason, func(m string) string {
		sub := tgwReasonNodeRe.FindStringSubmatch(m)
		if sub[1] != "" {
			return "node " + label(sub[1]) + ":"
		}
		ids := strings.Split(sub[2], ",")
		for i, id := range ids {
			ids[i] = label(id)
		}
		return "waiting for nodes " + strings.Join(ids, ", ")
	})
}

func (v *TransitGatewayAPI) attachmentResponse(ctx context.Context, att *model.TgwAttachment, tables map[int64]*model.TgwRouteTable, labels func(string) string) *TgwAttachmentResponse {
	resp := &TgwAttachmentResponse{ID: att.UUID, VPC: &TgwNamedRef{}, RouteTable: &TgwNamedRef{}, Status: att.Status, StatusReason: labels(att.StatusReason),
		Subnets: []string{}, CreatedAt: att.CreatedAt.Format(TimeStringForMat)}
	if att.Router != nil {
		resp.VPC.ID, resp.VPC.Name = att.Router.UUID, att.Router.Name
	}
	if t := tables[att.RouteTableID]; t != nil {
		resp.RouteTable.ID, resp.RouteTable.Name = t.UUID, t.Name
	}
	if subnets, err := services.VpcInternalCidrs(ctx, att.RouterID); err == nil && subnets != nil {
		resp.Subnets = subnets
	}
	resp.RouterAddress, resp.GatewayAddress = services.TgwLinkAddresses(att.Slot)
	return resp
}

func (v *TransitGatewayAPI) tableMap(ctx context.Context, tgw *model.TransitGateway) (tables []*model.TgwRouteTable, byID map[int64]*model.TgwRouteTable, props []*model.TgwPropagation, routes []*model.TgwRoute, err error) {
	tables, props, routes, err = tgwAdmin.RouteTables(ctx, tgw)
	if err != nil {
		return
	}
	byID = map[int64]*model.TgwRouteTable{}
	for _, t := range tables {
		byID[t.ID] = t
	}
	return
}

// @Summary list transit gateways
// @Description list the transit gateways of the organization with the number of attached VPCs
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} TransitGatewayListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways [get]
func (v *TransitGatewayAPI) List(c *gin.Context) {
	ctx := c.Request.Context()
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query offset", err)
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 0 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid query limit", err)
		return
	}
	total, tgws, err := tgwAdmin.List(ctx, int64(offset), int64(limit), c.DefaultQuery("order", "-created_at"), c.DefaultQuery("query", ""))
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list transit gateways", err)
		return
	}
	counts, err := tgwAdmin.AttachmentCounts(ctx, tgws)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	resp := &TransitGatewayListResponse{Total: int(total), Offset: offset, Limit: len(tgws), TransitGateways: make([]*TransitGatewayResponse, len(tgws))}
	for i, tgw := range tgws {
		resp.TransitGateways[i] = v.getResponse(ctx, tgw, counts[tgw.ID])
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary get a transit gateway
// @Description get a transit gateway with the state of its nodes; host names are for system admins only
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} TransitGatewayResponse
// @Failure 404 {object} common.APIError "Not found"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id} [get]
func (v *TransitGatewayAPI) Get(c *gin.Context) {
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	resp, err := v.getDetailResponse(c.Request.Context(), tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary create a transit gateway
// @Description create a transit gateway with a default route table; VPCs attached to it reach each other
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Param   message	body   TransitGatewayPayload  true   "Transit gateway create payload"
// @Success 200 {object} TransitGatewayResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 409 {object} common.APIError "Name taken (133002)"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways [post]
func (v *TransitGatewayAPI) Create(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &TransitGatewayPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	tgw, err := tgwAdmin.Create(ctx, payload.Name, payload.Description)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to create", err)
		return
	}
	c.JSON(http.StatusOK, v.getResponse(ctx, tgw, 0))
}

// @Summary patch a transit gateway
// @Description change the name or the description of a transit gateway
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Param   message	body   TransitGatewayPatchPayload  true   "Transit gateway patch payload"
// @Success 200 {object} TransitGatewayResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id} [patch]
func (v *TransitGatewayAPI) Patch(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	payload := &TransitGatewayPatchPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	if err := tgwAdmin.Update(ctx, tgw, payload.Name, payload.Description); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Patch transit gateway failed", err)
		return
	}
	resp, err := v.getDetailResponse(ctx, tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary delete a transit gateway
// @Description delete a transit gateway without attachments (409 while VPCs are attached or being detached)
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 204
// @Failure 409 {object} common.APIError "VPCs are attached (133003)"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id} [delete]
func (v *TransitGatewayAPI) Delete(c *gin.Context) {
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	if err := tgwAdmin.Delete(c.Request.Context(), tgw); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to delete", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// @Summary resync a transit gateway
// @Description send the current state of the transit gateway to all of its nodes again
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} TransitGatewayResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/resync [post]
func (v *TransitGatewayAPI) Resync(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	if err := tgwAdmin.Resync(ctx, tgw); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Resync failed", err)
		return
	}
	resp, err := v.getDetailResponse(ctx, tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary list the attachments of a transit gateway
// @Description list the VPCs attached to a transit gateway, the ones being detached included
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} TgwAttachmentListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/attachments [get]
func (v *TransitGatewayAPI) ListAttachments(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	atts, err := tgwAdmin.Attachments(ctx, tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	_, tables, _, _, err := v.tableMap(ctx, tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	labels := v.nodeLabels(ctx, tgw)
	resp := &TgwAttachmentListResponse{Attachments: []*TgwAttachmentResponse{}}
	for _, att := range atts {
		resp.Attachments = append(resp.Attachments, v.attachmentResponse(ctx, att, tables, labels))
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary get an attachment of a transit gateway
// @Description get one VPC attachment of a transit gateway
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} TgwAttachmentResponse
// @Failure 404 {object} common.APIError "Not found"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/attachments/{att_id} [get]
func (v *TransitGatewayAPI) GetAttachment(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	att, err := tgwAdmin.GetAttachment(ctx, tgw, c.Param("att_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid attachment query", err)
		return
	}
	_, tables, _, _, err := v.tableMap(ctx, tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, v.attachmentResponse(ctx, att, tables, v.nodeLabels(ctx, tgw)))
}

// @Summary attach a VPC to a transit gateway
// @Description attach a VPC of the same organization. Its internal subnets may not overlap those of the other members, 192.168.196.0/24 (VRRP subnet) or 169.254.254.0/24 (gateway links), nor the networks of the members' VPN gateways (400, 133014). The attachment is available once every node applied it
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Param   message	body   TgwAttachmentPayload  true   "Attachment payload"
// @Success 200 {object} TgwAttachmentResponse
// @Failure 400 {object} common.APIError "Bad request, overlapping networks (133014)"
// @Failure 409 {object} common.APIError "VPC attached already (133012) or too many attachments (133013)"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/attachments [post]
func (v *TransitGatewayAPI) CreateAttachment(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	payload := &TgwAttachmentPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	router, err := routerAdmin.GetRouter(ctx, payload.VPC)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid VPC", err)
		return
	}
	var table *model.TgwRouteTable
	if payload.RouteTable != nil {
		if table, err = tgwAdmin.GetRouteTable(ctx, tgw, payload.RouteTable.ID); err != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid route table", err)
			return
		}
	}
	propagate := payload.Propagate == nil || *payload.Propagate
	att, err := tgwAdmin.Attach(ctx, tgw, router, table, propagate)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to attach", err)
		return
	}
	if fresh, gerr := tgwAdmin.GetAttachment(ctx, tgw, att.UUID); gerr == nil {
		att = fresh
	}
	_, tables, _, _, err := v.tableMap(ctx, tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, v.attachmentResponse(ctx, att, tables, v.nodeLabels(ctx, tgw)))
}

// @Summary change the route table of an attachment
// @Description associate the attachment with another route table of the gateway
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Param   message	body   TgwAttachmentPatchPayload  true   "Attachment patch payload"
// @Success 200 {object} TgwAttachmentResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/attachments/{att_id} [patch]
func (v *TransitGatewayAPI) PatchAttachment(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	att, err := tgwAdmin.GetAttachment(ctx, tgw, c.Param("att_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid attachment query", err)
		return
	}
	payload := &TgwAttachmentPatchPayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	table, err := tgwAdmin.GetRouteTable(ctx, tgw, payload.RouteTable.ID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid route table", err)
		return
	}
	if err = tgwAdmin.UpdateAttachment(ctx, tgw, att, table); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to update the attachment", err)
		return
	}
	if fresh, gerr := tgwAdmin.GetAttachment(ctx, tgw, att.UUID); gerr == nil {
		att = fresh
	}
	_, tables, _, _, err := v.tableMap(ctx, tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, v.attachmentResponse(ctx, att, tables, v.nodeLabels(ctx, tgw)))
}

// @Summary detach a VPC from a transit gateway
// @Description detach a VPC; its propagations and the static routes towards it are removed. The attachment stays as detaching until every node applied the change; detaching again retries
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 204
// @Failure 404 {object} common.APIError "Not found"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/attachments/{att_id} [delete]
func (v *TransitGatewayAPI) DeleteAttachment(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	att, err := tgwAdmin.GetAttachment(ctx, tgw, c.Param("att_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid attachment query", err)
		return
	}
	if err = tgwAdmin.Detach(ctx, tgw, att); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to detach", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

func (v *TransitGatewayAPI) routeTableResponse(table *model.TgwRouteTable, atts []*model.TgwAttachment, refs map[int64]*TgwAttachmentRef, props []*model.TgwPropagation, routes []*model.TgwRoute) *TgwRouteTableResponse {
	resp := &TgwRouteTableResponse{ID: table.UUID, Name: table.Name, IsDefault: table.IsDefault, CreatedAt: table.CreatedAt.Format(TimeStringForMat),
		Associations: []*TgwAttachmentRef{}, Propagations: []*TgwPropagationResponse{}, Routes: []*TgwRouteResponse{}}
	for _, att := range atts {
		if att.RouteTableID == table.ID && att.Status != model.TgwAttachmentDetaching {
			resp.Associations = append(resp.Associations, refs[att.ID])
		}
	}
	for _, p := range props {
		if p.RouteTableID != table.ID {
			continue
		}
		prefixes := []string{}
		if p.Prefixes != "" {
			prefixes = strings.Split(p.Prefixes, ",")
		}
		resp.Propagations = append(resp.Propagations, &TgwPropagationResponse{ID: p.UUID, Attachment: refs[p.AttachmentID], Prefixes: prefixes})
	}
	for _, r := range routes {
		if r.RouteTableID != table.ID {
			continue
		}
		item := &TgwRouteResponse{ID: r.UUID, Destination: r.Destination, Type: r.Type}
		if r.Type == model.TgwRouteStatic {
			item.Attachment = refs[r.AttachmentID]
		}
		resp.Routes = append(resp.Routes, item)
	}
	return resp
}

func (v *TransitGatewayAPI) tableResponse(c *gin.Context, tgw *model.TransitGateway, table *model.TgwRouteTable) (resp *TgwRouteTableResponse, ok bool) {
	ctx := c.Request.Context()
	atts, err := tgwAdmin.Attachments(ctx, tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return nil, false
	}
	_, _, props, routes, err := v.tableMap(ctx, tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return nil, false
	}
	return v.routeTableResponse(table, atts, v.attachmentRefs(atts), props, routes), true
}

// @Summary list the route tables of a transit gateway
// @Description list the route tables with their associations, propagations and static routes
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} TgwRouteTableListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/route_tables [get]
func (v *TransitGatewayAPI) ListRouteTables(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	atts, err := tgwAdmin.Attachments(ctx, tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	tables, _, props, routes, err := v.tableMap(ctx, tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	refs := v.attachmentRefs(atts)
	resp := &TgwRouteTableListResponse{RouteTables: []*TgwRouteTableResponse{}}
	for _, t := range tables {
		resp.RouteTables = append(resp.RouteTables, v.routeTableResponse(t, atts, refs, props, routes))
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary get a route table of a transit gateway
// @Description get a route table with its associations, propagations and static routes
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} TgwRouteTableResponse
// @Failure 404 {object} common.APIError "Not found"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/route_tables/{rt_id} [get]
func (v *TransitGatewayAPI) GetRouteTable(c *gin.Context) {
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	table, err := tgwAdmin.GetRouteTable(c.Request.Context(), tgw, c.Param("rt_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid route table query", err)
		return
	}
	if resp, ok := v.tableResponse(c, tgw, table); ok {
		c.JSON(http.StatusOK, resp)
	}
}

// @Summary create a route table
// @Description create an empty route table; attachments associated with it reach what its propagations and routes give
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Param   message	body   TgwRouteTablePayload  true   "Route table payload"
// @Success 200 {object} TgwRouteTableResponse
// @Failure 409 {object} common.APIError "Name taken or too many tables (133022)"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/route_tables [post]
func (v *TransitGatewayAPI) CreateRouteTable(c *gin.Context) {
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	payload := &TgwRouteTablePayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	table, err := tgwAdmin.CreateRouteTable(c.Request.Context(), tgw, payload.Name)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to create the route table", err)
		return
	}
	if resp, ok := v.tableResponse(c, tgw, table); ok {
		c.JSON(http.StatusOK, resp)
	}
}

// @Summary rename a route table
// @Description rename a route table
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Param   message	body   TgwRouteTablePayload  true   "Route table payload"
// @Success 200 {object} TgwRouteTableResponse
// @Failure 409 {object} common.APIError "Name taken (133022)"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/route_tables/{rt_id} [patch]
func (v *TransitGatewayAPI) PatchRouteTable(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	table, err := tgwAdmin.GetRouteTable(ctx, tgw, c.Param("rt_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid route table query", err)
		return
	}
	payload := &TgwRouteTablePayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	if err = tgwAdmin.RenameRouteTable(ctx, tgw, table, payload.Name); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to rename the route table", err)
		return
	}
	if resp, ok := v.tableResponse(c, tgw, table); ok {
		c.JSON(http.StatusOK, resp)
	}
}

// @Summary delete a route table
// @Description delete a route table no attachment is associated with; the default table can not be deleted (409, 133023)
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 204
// @Failure 409 {object} common.APIError "In use (133023)"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/route_tables/{rt_id} [delete]
func (v *TransitGatewayAPI) DeleteRouteTable(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	table, err := tgwAdmin.GetRouteTable(ctx, tgw, c.Param("rt_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid route table query", err)
		return
	}
	if err = tgwAdmin.DeleteRouteTable(ctx, tgw, table); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to delete the route table", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// @Summary effective routes of a route table
// @Description the routes of a route table as the nodes install them: propagated subnets (filtered by the allow lists), static routes and blackholes
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 200 {object} TgwEffectiveRouteListResponse
// @Failure 404 {object} common.APIError "Not found"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/route_tables/{rt_id}/effective_routes [get]
func (v *TransitGatewayAPI) EffectiveRoutes(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	table, err := tgwAdmin.GetRouteTable(ctx, tgw, c.Param("rt_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid route table query", err)
		return
	}
	entries, _, err := services.TgwEffectiveRoutes(ctx, tgw.ID, table.ID)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	atts, err := tgwAdmin.Attachments(ctx, tgw)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	refs := v.attachmentRefs(atts)
	resp := &TgwEffectiveRouteListResponse{Routes: []*TgwEffectiveRouteResponse{}}
	for _, e := range entries {
		item := &TgwEffectiveRouteResponse{Destination: e.Prefix, Type: e.Source}
		if e.AttachmentID > 0 {
			item.Attachment = refs[e.AttachmentID]
		}
		resp.Routes = append(resp.Routes, item)
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary add a propagation
// @Description make the internal subnets of an attachment's VPC appear in a route table; with prefixes only the parts of the subnets inside them. Each prefix must overlap a subnet of that VPC
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Param   message	body   TgwPropagationPayload  true   "Propagation payload"
// @Success 200 {object} TgwRouteTableResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 409 {object} common.APIError "Exists already (133042)"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/route_tables/{rt_id}/propagations [post]
func (v *TransitGatewayAPI) CreatePropagation(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	table, err := tgwAdmin.GetRouteTable(ctx, tgw, c.Param("rt_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid route table query", err)
		return
	}
	payload := &TgwPropagationPayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	att, err := tgwAdmin.GetAttachment(ctx, tgw, payload.Attachment.ID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid attachment", err)
		return
	}
	if _, err = tgwAdmin.CreatePropagation(ctx, tgw, table, att, strings.Join(payload.Prefixes, ",")); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to add the propagation", err)
		return
	}
	if resp, ok := v.tableResponse(c, tgw, table); ok {
		c.JSON(http.StatusOK, resp)
	}
}

// @Summary remove a propagation
// @Description stop propagating the subnets of an attachment into a route table
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 204
// @Failure 404 {object} common.APIError "Not found"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/route_tables/{rt_id}/propagations/{prop_id} [delete]
func (v *TransitGatewayAPI) DeletePropagation(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	table, err := tgwAdmin.GetRouteTable(ctx, tgw, c.Param("rt_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid route table query", err)
		return
	}
	prop, err := tgwAdmin.GetPropagation(ctx, table, c.Param("prop_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid propagation query", err)
		return
	}
	if err = tgwAdmin.DeletePropagation(ctx, tgw, prop); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to remove the propagation", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// @Summary add a static route
// @Description add a static route to a route table: a destination towards an attachment, or dropped with blackhole. It replaces a propagated route with the same destination. A default route is refused, and so is a destination overlapping the networks of a member's VPN gateway
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Param   message	body   TgwRoutePayload  true   "Route payload"
// @Success 200 {object} TgwRouteTableResponse
// @Failure 400 {object} common.APIError "Bad request (133014)"
// @Failure 409 {object} common.APIError "Exists already (133032)"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/route_tables/{rt_id}/routes [post]
func (v *TransitGatewayAPI) CreateRoute(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	table, err := tgwAdmin.GetRouteTable(ctx, tgw, c.Param("rt_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid route table query", err)
		return
	}
	payload := &TgwRoutePayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	if payload.Blackhole == (payload.Attachment != nil) {
		ErrorResponse(c, http.StatusBadRequest, "Give either an attachment or blackhole true", nil)
		return
	}
	var att *model.TgwAttachment
	if payload.Attachment != nil {
		if att, err = tgwAdmin.GetAttachment(ctx, tgw, payload.Attachment.ID); err != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid attachment", err)
			return
		}
	}
	if _, err = tgwAdmin.CreateRoute(ctx, tgw, table, payload.Destination, att); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to add the route", err)
		return
	}
	if resp, ok := v.tableResponse(c, tgw, table); ok {
		c.JSON(http.StatusOK, resp)
	}
}

// @Summary delete a static route
// @Description delete a static route of a route table
// @tags Transit Gateway
// @Accept  json
// @Produce json
// @Success 204
// @Failure 404 {object} common.APIError "Not found"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /transit_gateways/{id}/route_tables/{rt_id}/routes/{route_id} [delete]
func (v *TransitGatewayAPI) DeleteRoute(c *gin.Context) {
	ctx := c.Request.Context()
	tgw, ok := v.getTgw(c)
	if !ok {
		return
	}
	table, err := tgwAdmin.GetRouteTable(ctx, tgw, c.Param("rt_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid route table query", err)
		return
	}
	route, err := tgwAdmin.GetRoute(ctx, table, c.Param("route_id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid route query", err)
		return
	}
	if err = tgwAdmin.DeleteRoute(ctx, tgw, route); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to delete the route", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// vpcTransitGateway is the gateway of a VPC for its detail, nil when it has none
func vpcTransitGateway(ctx context.Context, routerID int64) *VPCTransitGatewayRef {
	att, tgw, err := tgwAdmin.AttachmentOfRouter(ctx, routerID)
	if err != nil || att == nil {
		return nil
	}
	return &VPCTransitGatewayRef{ID: tgw.UUID, Name: tgw.Name, AttachmentID: att.UUID, AttachmentStatus: att.Status}
}
