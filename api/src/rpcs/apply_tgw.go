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
	"api/src/services"
)

func init() {
	Add("apply_tgw", ApplyTgw)
}

// ApplyTgw records what a node applied of a transit gateway state. Idempotent: cland retries a callback up to
// three times, and a report older than the recorded one is ignored.
// |:-COMMAND-:| apply_tgw.sh '<tgw ID>' '<generation>' 'ok|error' '<reason or ->' '<attachments>'
func ApplyTgw(ctx context.Context, args []string) (status string, err error) {
	ctx, _, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if len(args) < 6 {
		err = fmt.Errorf("wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	tgwID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || tgwID <= 0 {
		err = fmt.Errorf("invalid transit gateway id %q", args[1])
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	generation, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid generation", err)
		return
	}
	attachments, err := strconv.Atoi(args[5])
	if err != nil {
		logger.Ctx(ctx).Error("Invalid attachment count", err)
		return
	}
	reason := args[4]
	if reason == "-" {
		reason = ""
	}
	// The executing node is the message extra
	hostid, ok := ctx.Value("hostid").(int32)
	if !ok || hostid < 0 {
		err = fmt.Errorf("no executing node")
		return
	}
	err = services.TgwNodeApplied(ctx, tgwID, hostid, generation, args[3] == "ok", reason, attachments)
	return
}
