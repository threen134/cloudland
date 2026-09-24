/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"strconv"
	"strings"

	. "api/src/common"
	"api/src/model"
)

func init() {
	Add("lb_health", LoadBalancerHealth)
}

// LoadBalancerHealth records backend health check results, reported by report_lb_health.sh on the node
// that currently holds the load balancer floating IP (the VRRP master)
// |:-COMMAND-:| lb_health.sh '<load balancer ID>' '<backend ID>:<up|down|unknown> ...'
func LoadBalancerHealth(ctx context.Context, args []string) (status string, err error) {
	ctx, db := GetContextDB(ctx)
	if len(args) < 3 {
		logger.Ctx(ctx).Error("Invalid args for load balancer health", args)
		return
	}
	lbID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || lbID <= 0 {
		logger.Ctx(ctx).Error("Invalid load balancer ID", args[1], err)
		err = nil
		return
	}
	loadBalancer := &model.LoadBalancer{Model: model.Model{ID: lbID}}
	if err = db.Preload("VrrpInstance").Take(loadBalancer).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query load balancer", lbID, err)
		err = nil
		return
	}
	// Ignore reports from nodes that do not host this load balancer
	hostid, _ := ctx.Value("hostid").(int32)
	vrrp := loadBalancer.VrrpInstance
	if vrrp == nil || (hostid != vrrp.Hyper && hostid != vrrp.Peer) {
		logger.Ctx(ctx).Errorf("Load balancer %d health reported by unexpected node %d", lbID, hostid)
		return
	}
	health := map[string][]int64{}
	for _, item := range strings.Fields(args[2]) {
		parts := strings.SplitN(item, ":", 2)
		if len(parts) != 2 {
			continue
		}
		backendID, perr := strconv.ParseInt(parts[0], 10, 64)
		if perr != nil || backendID <= 0 {
			continue
		}
		state := parts[1]
		switch state {
		case "up", "down":
		default:
			state = ""
		}
		health[state] = append(health[state], backendID)
	}
	listenerIDs := db.Model(&model.Listener{}).Select("id").Where("load_balancer_id = ?", lbID)
	for state, backendIDs := range health {
		err = db.Model(&model.Backend{}).Where("id in ? and listener_id in (?)", backendIDs, listenerIDs).
			Update("health", state).Error
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to update backend health of load balancer %d, %v", lbID, err)
			return
		}
	}
	return
}
