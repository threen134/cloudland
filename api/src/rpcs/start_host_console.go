/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"net"
	"strconv"

	. "api/src/common"
	"api/src/model"
)

func init() {
	Add("start_host_console", StartHostConsole)
}

// StartHostConsole records the address start_host_console.sh exposes a root shell of the reporting node on
// |:-COMMAND-:| start_host_console.sh '<session>' '<port>' '<hypervisor IP>'
func StartHostConsole(ctx context.Context, args []string) (status string, err error) {
	ctx, db := GetContextDB(ctx)
	if len(args) < 4 {
		logger.Ctx(ctx).Error("Invalid args for host console", args)
		return
	}
	session := args[1]
	port, portErr := strconv.Atoi(args[2])
	if !serialSessionPattern.MatchString(session) || portErr != nil || port <= 0 || port > 65535 || net.ParseIP(args[3]) == nil {
		logger.Ctx(ctx).Error("Invalid host console address", args)
		return
	}
	// The record belongs to the node that ran the script; clapi waits for the node it asked
	hostid, ok := ctx.Value("hostid").(int32)
	if !ok {
		logger.Ctx(ctx).Error("Host console reported without a node ID", args)
		return
	}
	serial := &model.SerialConsole{HostID: hostid, Session: session, LocalAddress: args[3], LocalPort: int32(port)}
	// Callbacks are retried: one record per session
	err = db.Where("host_id = ? AND instance_id = 0 AND session = ?", hostid, session).Assign(serial).FirstOrCreate(&model.SerialConsole{}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to record host console", err)
	}
	return
}
