/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

// Recovering the instances of a host that is down on other hosts, and letting the host back into its storage clusters
// (shared-storage-design.md §11)

import (
	"net/http"

	. "api/src/common"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

type HyperEvacuatePayload struct {
	// Host (hostid) to start the instances on; nil or -1: any active host of their zone that reaches their pools
	TargetHyper *int32 `json:"target_hyper" binding:"omitempty,gte=-1,lte=65535"`
	// The host is powered off (through IPMI, the provider): stands in for a fence CloudLand can not run, and the grace
	// period (5 minutes offline) is not waited for. Recorded in the audit log
	ConfirmFenced bool `json:"confirm_fenced"`
	// UUIDs of the instances to recover; every instance of the host when empty
	Instances []string `json:"instances" binding:"omitempty,dive,uuid"`
}

type HyperEvacuateResponse struct {
	Instances []*services.EvacuateResult `json:"instances"`
}

type HyperUnfencePayload struct {
	// Forget the fences instead of lifting them: after an admin lifted them by hand (the blocklist entry of an
	// imported Ceph cluster, which the CloudLand client can not remove)
	Forget bool `json:"forget"`
}

// @Summary evacuate a hypervisor that is down
// @Description Recover the instances of a host that cland has had offline for at least 5 minutes on other hosts. Only instances whose disks are all in shared storage pools can be recovered; the others are listed as not_doing with the reason. The host is first fenced on every storage cluster their disks are on (GPFS expels it, Ceph blocklists its address), then each instance is defined again from its record on a host that reaches its pools and started when it was running. A host that can not be fenced (no other admin host online, an imported GPFS cluster) needs confirm_fenced. The copies left on the host are removed when it comes back, and the fences lifted after that.
// @tags Administration,Hypervisor
// @Accept json
// @Produce json
// @Param uuid path string true "Hypervisor UUID"
// @Param body body HyperEvacuatePayload false "Evacuation options"
// @Success 200 {object} HyperEvacuateResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 404 {object} common.APIError "Hypervisor not found"
// @Failure 409 {object} common.APIError "The host is not offline long enough, or can not be fenced"
// @Router /hypers/{uuid}/evacuate [post]
func (v *HyperAPI) Evacuate(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &HyperEvacuatePayload{}
	if err := c.ShouldBindJSON(payload); err != nil && c.Request.ContentLength > 0 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	hyper, err := hyperAdmin.GetHyperByUUID(ctx, c.Param("uuid"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Hypervisor not found", err)
		return
	}
	req := &services.EvacuateRequest{TargetHyper: -1, ConfirmFenced: payload.ConfirmFenced}
	if payload.TargetHyper != nil {
		req.TargetHyper = *payload.TargetHyper
	}
	for _, id := range payload.Instances {
		inst, ierr := instanceAdmin.GetInstanceByUUID(ctx, id)
		if ierr != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid instance "+id, ierr)
			return
		}
		req.Instances = append(req.Instances, inst.ID)
	}
	if payload.ConfirmFenced {
		SetAuditAction(c, "hyper.evacuate_confirmed")
	}
	results, err := hyperAdmin.Evacuate(ctx, hyper.Hostid, req)
	if err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to evacuate the hypervisor", err)
		return
	}
	c.JSON(http.StatusOK, &HyperEvacuateResponse{Instances: results})
}

// @Summary let a hypervisor back into its storage clusters
// @Description Lift the fences of a host now, once it came back and removed the instances recovered elsewhere (this also happens by itself); or forget them after lifting them by hand.
// @tags Administration,Hypervisor
// @Accept json
// @Produce json
// @Param uuid path string true "Hypervisor UUID"
// @Param body body HyperUnfencePayload false "Options"
// @Success 204 "No content"
// @Failure 404 {object} common.APIError "Hypervisor not found"
// @Failure 409 {object} common.APIError "The host is not fenced, or can not be let back in yet"
// @Router /hypers/{uuid}/unfence [post]
func (v *HyperAPI) Unfence(c *gin.Context) {
	ctx := c.Request.Context()
	payload := &HyperUnfencePayload{}
	if err := c.ShouldBindJSON(payload); err != nil && c.Request.ContentLength > 0 {
		ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
		return
	}
	hyper, err := hyperAdmin.GetHyperByUUID(ctx, c.Param("uuid"))
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Hypervisor not found", err)
		return
	}
	if err = services.UnfenceHost(ctx, hyper.Hostid, payload.Forget); err != nil {
		ErrorResponse(c, http.StatusBadRequest, "Failed to let the hypervisor back in", err)
		return
	}
	c.Status(http.StatusNoContent)
}
