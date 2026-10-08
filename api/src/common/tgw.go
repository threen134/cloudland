/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"context"

	"api/src/model"
)

// TgwLinkCidr holds the /31 of every transit gateway attachment (veth tr-<att> in the VPC router, ta-<att> in
// tgw-<ID>). The router links to router-0 are 169.<a>.<b>.x with a <= 253, and the bridges carry
// 169.254.169.254, so neither falls in it; VPN tunnel link addresses must stay out of it too.
const TgwLinkCidr = "169.254.254.0/24"

// TgwActiveAttachmentStatuses are the attachments whose VPC belongs to the gateway: an attachment being
// detached is already left out of the desired state and of the forwarding scope
var TgwActiveAttachmentStatuses = []string{model.TgwAttachmentAttaching, model.TgwAttachmentAvailable, model.TgwAttachmentError}

// RouterScope returns the VPC routers whose interfaces a router's forwarding entries are exchanged with: the
// router itself, or every member of its transit gateway. A VM of VPC A reaches VPC B through the local router
// of B, so a node hosting A needs the entries of B as well (vpc-transit-gateway-plan.md §2.3).
func RouterScope(ctx context.Context, routerID int64) (routers []int64) {
	routers = []int64{routerID}
	if routerID <= 0 {
		return
	}
	ctx, db := GetContextDB(ctx)
	att := &model.TgwAttachment{}
	if err := db.Select("tgw_id").Where("router_id = ? AND status IN ?", routerID, TgwActiveAttachmentStatuses).Take(att).Error; err != nil {
		return
	}
	members := []int64{}
	if err := db.Model(&model.TgwAttachment{}).Where("tgw_id = ? AND status IN ?", att.TgwID, TgwActiveAttachmentStatuses).
		Order("router_id").Pluck("router_id", &members).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query the members of transit gateway %d: %v", att.TgwID, err)
		return
	}
	for _, m := range members {
		if m != routerID {
			routers = append(routers, m)
		}
	}
	return
}

// OtherRouters returns the routers of scope other than routerID
func OtherRouters(scope []int64, routerID int64) (others []int64) {
	for _, r := range scope {
		if r != routerID {
			others = append(others, r)
		}
	}
	return
}
