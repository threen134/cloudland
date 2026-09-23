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
)

func init() {
	Add("capture_image", CaptureImage)
}

// CaptureImage records the result of capture_image.sh
func CaptureImage(ctx context.Context, args []string) (status string, err error) {
	//|:-COMMAND-:| capture_image.sh '<image id>' '<available|error>' '<format>' '<size bytes>' '<message>'
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if len(args) < 4 {
		err = fmt.Errorf("Wrong params")
		logger.Ctx(ctx).Error("Invalid args", err)
		return
	}
	imgID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid image ID", err)
		return
	}
	image := &model.Image{Model: model.Model{ID: imgID}}
	if err = db.Take(image).Error; err != nil {
		logger.Ctx(ctx).Error("Invalid image ID", err)
		return
	}
	updates := map[string]interface{}{"status": args[2], "format": args[3]}
	if len(args) > 4 {
		if size, perr := strconv.ParseInt(args[4], 10, 64); perr == nil && size > 0 {
			updates["size"] = size
		}
	}
	if args[2] == "error" && len(args) > 5 {
		logger.Ctx(ctx).Errorf("Capture image %d failed: %s", imgID, args[5])
	}
	if err = db.Model(&model.Image{}).Where("id = ?", image.ID).Updates(updates).Error; err != nil {
		logger.Ctx(ctx).Error("Update image failed", err)
	}
	return
}
