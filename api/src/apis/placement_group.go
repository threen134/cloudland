/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	. "api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var placementGroupAPI = &PlacementGroupAPI{}
var placementGroupAdmin = services.PlacementGroupAdmin

type PlacementGroupAPI struct{}

type PlacementGroupResponse struct {
	*ResourceReference
	Description string `json:"description"`
	Policy      string `json:"policy"` // spread | pack
	Strict      bool   `json:"strict"`
	Zone        string `json:"zone"`
	MemberCount int    `json:"member_count"`
	// Number of distinct hosts the members are on (or are being created on)
	HostCount int  `json:"host_count"`
	Compliant bool `json:"compliant"`
	// Detail only
	Members []*PlacementGroupMemberResponse `json:"members,omitempty"`
}

type PlacementGroupMemberResponse struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Status   string `json:"status"`
	// Number of the host inside the group: members on the same host have the same number, 0 when it has no host yet.
	// Everyone sees it; the host name is for system admins only
	HostSlot int `json:"host_slot"`
	// Host of an in-flight migration, 0 when none
	TargetSlot int    `json:"target_slot"`
	Hypervisor string `json:"hypervisor,omitempty"`
	// Still being created after an hour: it keeps holding its host, deleting it frees the host
	StaleProvisioning bool `json:"stale_provisioning"`
	// A migration of it has not moved for an hour: it keeps holding its target until an admin repairs it
	StaleMigration bool   `json:"stale_migration"`
	MigrationID    string `json:"migration_id,omitempty"`
	// Its latest migration failed, or was done ignoring the rules: why a strict group may be split
	LastMigrationFailed bool `json:"last_migration_failed"`
	IgnoredPlacement    bool `json:"ignored_placement"`
}

type PlacementGroupListResponse struct {
	Offset          int                       `json:"offset"`
	Total           int                       `json:"total"`
	Limit           int                       `json:"limit"`
	PlacementGroups []*PlacementGroupResponse `json:"placement_groups"`
}

type PlacementGroupPayload struct {
	Name        string `json:"name" binding:"required,min=2,max=32"`
	Description string `json:"description" binding:"omitempty,max=255"`
	Policy      string `json:"policy" binding:"required,oneof=spread pack"`
	// Defaults to true for spread and false for pack
	Strict *bool  `json:"strict"`
	Zone   string `json:"zone" binding:"omitempty,min=1,max=32"` // zone name, the default zone when left out
}

// PlacementGroupPatchPayload changes the name and the description. policy, strict and zone are declared so that a
// request carrying them is refused instead of silently ignored: they can not change once the group exists
type PlacementGroupPatchPayload struct {
	Name        *string `json:"name" binding:"omitempty,min=2,max=32"`
	Description *string `json:"description" binding:"omitempty,max=255"`
	Policy      *string `json:"policy" swaggerignore:"true"`
	Strict      *bool   `json:"strict" swaggerignore:"true"`
	Zone        *string `json:"zone" swaggerignore:"true"`
}

// PlacementGroupRef is the placement group of an instance
type PlacementGroupRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Policy string `json:"policy"`
	Strict bool   `json:"strict"`
}

func placementGroupRef(group *model.PlacementGroup) *PlacementGroupRef {
	if group == nil {
		return nil
	}
	return &PlacementGroupRef{ID: group.UUID, Name: group.Name, Policy: group.Policy, Strict: group.Strict}
}

func (v *PlacementGroupAPI) getResponse(ctx context.Context, group *model.PlacementGroup, stats *services.PlacementGroupStats) *PlacementGroupResponse {
	resp := &PlacementGroupResponse{
		ResourceReference: &ResourceReference{
			ID: group.UUID, Name: group.Name, Owner: orgAdmin.GetOrgName(ctx, group.Owner), OwnerUUID: orgAdmin.GetOrgUUID(ctx, group.Owner),
			CreatedAt: group.CreatedAt.Format(TimeStringForMat), UpdatedAt: group.UpdatedAt.Format(TimeStringForMat),
		},
		Description: group.Description, Policy: group.Policy, Strict: group.Strict, Compliant: true,
	}
	if group.Zone != nil {
		resp.Zone = group.Zone.Name
	}
	if stats != nil {
		resp.MemberCount, resp.HostCount, resp.Compliant = stats.MemberCount, stats.HostCount, stats.Compliant
	}
	return resp
}

func (v *PlacementGroupAPI) getDetailResponse(ctx context.Context, group *model.PlacementGroup) (resp *PlacementGroupResponse, err error) {
	members, stats, err := placementGroupAdmin.Members(ctx, group)
	if err != nil {
		return
	}
	resp = v.getResponse(ctx, group, stats)
	resp.Members = []*PlacementGroupMemberResponse{}
	admin := GetMemberShip(ctx).CheckSystemPermission()
	// Host names for system admins, all in one query
	hyperNames := map[int32]string{}
	if admin {
		if names, nerr := hyperAdmin.GetHyperNames(ctx); nerr == nil {
			hyperNames = names
		}
	}
	for _, m := range members {
		item := &PlacementGroupMemberResponse{ID: m.Instance.UUID, Hostname: m.Instance.Hostname, Status: m.Instance.Status.String(),
			HostSlot: m.HostSlot, TargetSlot: m.TargetSlot, StaleProvisioning: m.StaleProvisioning, StaleMigration: m.StaleMigration,
			LastMigrationFailed: m.LastMigrationFailed, IgnoredPlacement: m.IgnoredPlacement}
		if admin {
			if m.Hostid >= 0 {
				item.Hypervisor = hyperNames[m.Hostid]
			}
			if m.Migration != nil {
				item.MigrationID = m.Migration.UUID
			}
		}
		resp.Members = append(resp.Members, item)
	}
	return
}

// @Summary list placement groups
// @Description list the placement groups of the organization, each with its member count, the number of hosts they are on and whether the group keeps its rule
// @tags Placement Group
// @Accept  json
// @Produce json
// @Param   zone  query  string  false  "Zone name"
// @Success 200 {object} PlacementGroupListResponse
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /placement_groups [get]
func (v *PlacementGroupAPI) List(c *gin.Context) {
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
	var zoneID int64
	if zoneName := strings.TrimSpace(c.DefaultQuery("zone", "")); zoneName != "" {
		zone, zerr := zoneAdmin.GetZoneByName(ctx, zoneName)
		if zerr != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid zone", zerr)
			return
		}
		zoneID = zone.ID
	}
	total, groups, stats, err := placementGroupAdmin.List(ctx, int64(offset), int64(limit), c.DefaultQuery("order", "-created_at"), c.DefaultQuery("query", ""), zoneID)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to list placement groups", err)
		return
	}
	resp := &PlacementGroupListResponse{Total: int(total), Offset: offset, Limit: len(groups), PlacementGroups: make([]*PlacementGroupResponse, len(groups))}
	for i, group := range groups {
		resp.PlacementGroups[i] = v.getResponse(ctx, group, stats[group.ID])
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary get a placement group
// @Description get a placement group with its members; host_slot numbers the hosts inside the group, the host names are for system admins only
// @tags Placement Group
// @Accept  json
// @Produce json
// @Success 200 {object} PlacementGroupResponse
// @Failure 404 {object} common.APIError "Not found"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /placement_groups/{id} [get]
func (v *PlacementGroupAPI) Get(c *gin.Context) {
	ctx := c.Request.Context()
	group, err := placementGroupAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid placement group query", err)
		return
	}
	resp, err := v.getDetailResponse(ctx, group)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary create a placement group
// @Description create a placement group in a zone. spread keeps the members on different hosts, pack on one host; a strict group refuses what breaks its rule, a best-effort one relaxes it. An organization has at most 50 groups
// @tags Placement Group
// @Accept  json
// @Produce json
// @Param   message	body   PlacementGroupPayload  true   "Placement group create payload"
// @Success 200 {object} PlacementGroupResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 409 {object} common.APIError "Name taken (111702) or too many groups (111707)"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /placement_groups [post]
func (v *PlacementGroupAPI) Create(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &PlacementGroupPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	var zone *model.Zone
	var err error
	if payload.Zone != "" {
		zone, err = zoneAdmin.GetZoneByName(ctx, payload.Zone)
	} else {
		zone, err = zoneAdmin.GetDefaultZone(ctx)
	}
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid zone", err)
		return
	}
	strict := payload.Policy == model.PlacementPolicySpread
	if payload.Strict != nil {
		strict = *payload.Strict
	}
	group, err := placementGroupAdmin.Create(ctx, payload.Name, payload.Description, payload.Policy, strict, zone)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to create", err)
		return
	}
	c.JSON(http.StatusOK, v.getResponse(ctx, group, &services.PlacementGroupStats{Compliant: true}))
}

// @Summary patch a placement group
// @Description change the name or the description of a placement group; its policy, strictness and zone can not change (400)
// @tags Placement Group
// @Accept  json
// @Produce json
// @Param   message	body   PlacementGroupPatchPayload  true   "Placement group patch payload"
// @Success 200 {object} PlacementGroupResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /placement_groups/{id} [patch]
func (v *PlacementGroupAPI) Patch(c *gin.Context) {
	ctx := c.Request.Context()
	group, err := placementGroupAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid placement group query", err)
		return
	}
	payload := &PlacementGroupPatchPayload{}
	if err = c.ShouldBindJSON(payload); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	if payload.Policy != nil || payload.Strict != nil || payload.Zone != nil {
		ErrorResponse(c, http.StatusBadRequest, "The policy, strictness and zone of a placement group can not change; create another group", nil)
		return
	}
	if err = placementGroupAdmin.Update(ctx, group, payload.Name, payload.Description); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Patch placement group failed", err)
		return
	}
	resp, err := v.getDetailResponse(ctx, group)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Internal error", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// @Summary delete a placement group
// @Description delete an empty placement group (409 while it has members, being deleted ones included)
// @tags Placement Group
// @Accept  json
// @Produce json
// @Success 204
// @Failure 409 {object} common.APIError "The group has members (111703)"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /placement_groups/{id} [delete]
func (v *PlacementGroupAPI) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	group, err := placementGroupAdmin.GetByUUID(ctx, c.Param("id"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Invalid placement group query", err)
		return
	}
	if err = placementGroupAdmin.Delete(ctx, group); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Not able to delete", err)
		return
	}
	c.JSON(http.StatusNoContent, nil)
}
