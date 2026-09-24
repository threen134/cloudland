/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"net"
	"regexp"
	"strconv"

	. "api/src/common"
	"api/src/model"
)

func init() {
	Add("start_serial_console", StartSerialConsole)
}

var serialSessionPattern = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// StartSerialConsole records the address start_serial_console.sh exposes the serial console on
// |:-COMMAND-:| start_serial_console.sh '<instance ID>' '<session>' '<port>' '<hypervisor IP>'
func StartSerialConsole(ctx context.Context, args []string) (status string, err error) {
	ctx, db := GetContextDB(ctx)
	if len(args) < 5 {
		logger.Ctx(ctx).Error("Invalid args for serial console", args)
		return
	}
	instID, perr := strconv.ParseInt(args[1], 10, 64)
	session := args[2]
	port, portErr := strconv.Atoi(args[3])
	if perr != nil || !serialSessionPattern.MatchString(session) || portErr != nil || port <= 0 || port > 65535 || net.ParseIP(args[4]) == nil {
		logger.Ctx(ctx).Error("Invalid serial console address", args)
		return
	}
	instance := &model.Instance{}
	if err = db.Where("id = ?", instID).Take(instance).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query instance for serial console", instID, err)
		return
	}
	// Only the hypervisor running the instance may publish its console address
	if hostid, _ := ctx.Value("hostid").(int32); hostid != instance.Hyper {
		logger.Ctx(ctx).Errorf("Serial console of instance %d reported by node %d, instance is on %d", instID, hostid, instance.Hyper)
		return
	}
	serial := &model.SerialConsole{InstanceID: instID, HostID: instance.Hyper, Session: session, LocalAddress: args[4], LocalPort: int32(port)}
	// Callbacks are retried: one record per session
	err = db.Where("instance_id = ? AND session = ?", instID, session).Assign(serial).FirstOrCreate(&model.SerialConsole{}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to record serial console", err)
	}
	return
}
