/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"context"
	"fmt"
	"strconv"

	. "api/src/common"
	"api/src/model"
	"api/src/services"
)

// InstanceReasonPasswordFailed is the reason of an instance whose last guest password change did not reach
// the guest (set_user_passwd.sh, usually because the guest agent was not running yet). The request itself
// answered 200 before the node tried; the next successful change clears it.
const InstanceReasonPasswordFailed = "password_failed"

func init() {
	Add("set_user_passwd", SetUserPasswd)
}

// passwordReason tells the reason set_user_passwd.sh leaves on the instance: InstanceReasonPasswordFailed when
// it failed, "" when it succeeded and the instance still shows an earlier failure, and keep = true when the
// reason stays as it is
func passwordReason(result, current string) (reason string, keep bool) {
	if result == "success" {
		if current == InstanceReasonPasswordFailed {
			return "", false
		}
		return current, true
	}
	if current == InstanceReasonPasswordFailed {
		return current, true
	}
	return InstanceReasonPasswordFailed, false
}

// SetUserPasswd keeps the result of a guest password change on the instance
// |:-COMMAND-:| set_user_passwd.sh '<instance ID>' 'success'
// |:-COMMAND-:| set_user_passwd.sh '<instance ID>' 'error' '<agent_unavailable|failed>'
func SetUserPasswd(ctx context.Context, args []string) (status string, err error) {
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if len(args) < 3 {
		err = fmt.Errorf("wrong params %q", args)
		logger.Ctx(ctx).Error("Invalid set_user_passwd callback", err)
		return
	}
	instID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid instance ID", err)
		return
	}
	result := args[2]
	instance := &model.Instance{}
	if err = db.Where("id = ?", instID).Take(instance).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query instance for password", instID, err)
		return
	}
	if hostid, _ := ctx.Value("hostid").(int32); hostid != instance.Hyper {
		logger.Ctx(ctx).Errorf("Password of instance %d reported by node %d, instance is on %d", instID, hostid, instance.Hyper)
		return
	}
	// The stored root password only changes once the node confirms the guest has it
	if err = services.SettleUserPassword(ctx, instID, result == "success"); err != nil {
		logger.Ctx(ctx).Error("Failed to settle the root password", err)
		return
	}
	if result != "success" {
		cause := ""
		if len(args) > 3 {
			cause = args[3]
		}
		logger.Ctx(ctx).Errorf("Guest password of instance %d not set on node %d: %s", instID, instance.Hyper, cause)
	}
	reason, keep := passwordReason(result, instance.Reason)
	if keep {
		return
	}
	err = db.Model(&model.Instance{}).Where("id = ?", instID).Update("reason", reason).Error
	if err != nil {
		logger.Ctx(ctx).Error("Failed to update instance reason", err)
	}
	return
}
