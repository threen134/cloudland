/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	. "api/src/common"
	"api/src/model"
	"context"
	"fmt"
	"strconv"
)

func init() {
	Add("create_image", CreateImage)
}

// CreateImage records the result of create_image.sh (legacy local mode without S3)
func CreateImage(ctx context.Context, args []string) (status string, err error) {
	//|:-COMMAND-:| create_image.sh '<image id>' '<available|error>' '<format>' '<size bytes>'
	ctx, db, newTransaction := StartTransaction(ctx)
	defer func() {
		if newTransaction {
			EndTransaction(ctx, err)
		}
	}()
	if len(args) < 5 {
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
	imageSize, err := strconv.ParseInt(args[4], 10, 64)
	if err != nil {
		logger.Ctx(ctx).Error("Invalid image size", err)
		return
	}
	err = db.Model(&model.Image{}).Where("id = ?", image.ID).Updates(map[string]interface{}{
		"status": args[2],
		"format": args[3],
		"size":   imageSize,
	}).Error
	if err != nil {
		logger.Ctx(ctx).Error("Update image failed", err)
	}
	return
}
