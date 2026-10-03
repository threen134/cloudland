/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"fmt"
	"net"
	"strconv"

	. "api/src/common"
	"api/src/model"
)

func init() {
	Add("set_vnc_passwd", SetVncPasswd)
}

// vncPasswdResult is what set_vnc_passwd.sh reports: the VNC address once the password is in effect, or why
// it is not
type vncPasswdResult struct {
	instanceID int64
	failed     bool
	reason     string
	address    string
	port       int32
}

// parseVncPasswd reads
// |:-COMMAND-:| set_vnc_passwd.sh '<instance ID>' '<port>' '<hypervisor IP>'
// |:-COMMAND-:| set_vnc_passwd.sh '<instance ID>' 'error' '<reason>'
func parseVncPasswd(args []string) (result *vncPasswdResult, err error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("wrong params %q", args)
	}
	result = &vncPasswdResult{}
	if result.instanceID, err = strconv.ParseInt(args[1], 10, 64); err != nil {
		return nil, fmt.Errorf("invalid instance ID %q", args[1])
	}
	if args[2] == "error" {
		result.failed = true
		if len(args) > 3 {
			result.reason = args[3]
		}
		return
	}
	port, err := strconv.Atoi(args[2])
	if err != nil || port <= 0 || port > 65535 || len(args) < 4 || net.ParseIP(args[3]) == nil {
		return nil, fmt.Errorf("invalid VNC address %q", args[2:])
	}
	result.port, result.address = int32(port), args[3]
	return
}

// SetVncPasswd records where the console proxy connects to the VNC of an instance, once set_vnc_passwd.sh set
// the password of the console. When the node could not set it, the record has no address: the console
// resolver waiting for it gives up at once instead of handing out a password that is not in effect.
func SetVncPasswd(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	result, err := parseVncPasswd(args)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid set_vnc_passwd callback", err)
		return
	}
	instance := &model.Instance{}
	if err = db.Where("id = ?", result.instanceID).Take(instance).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query instance for VNC", result.instanceID, err)
		return
	}
	// Only the hypervisor running the instance may publish its console address
	if hostid, _ := ctx.Value("hostid").(int32); hostid != instance.Hyper {
		logger.Ctx(ctx).Errorf("VNC of instance %d reported by node %d, instance is on %d", instance.ID, hostid, instance.Hyper)
		return
	}
	address := map[string]interface{}{"local_address": result.address, "local_port": result.port}
	if result.failed {
		logger.Ctx(ctx).Errorf("VNC password of instance %d not set on node %d: %s", instance.ID, instance.Hyper, result.reason)
	}
	err = db.Where("instance_id = ?", instance.ID).Assign(address).FirstOrCreate(&model.Vnc{InstanceID: instance.ID}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to update vnc", err)
		return
	}
	return
}
