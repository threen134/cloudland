/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

Node validation endpoints for cland-go, served on the internal port next to
/internal/execute (cland reaches clapi via CLAPI_ENDPOINT, not the public REST API).
*/

package rpcs

import (
	"net/http"
	"strconv"

	. "api/src/common"
	"api/src/model"

	"gopkg.in/macaron.v1"
)

type validNode struct {
	Hostid   int32  `json:"hostid"`
	Hostname string `json:"hostname"`
	Status   int32  `json:"status"`
}

// ListValidNodes returns every hypervisor cland may admit, with its hostname and status.
// cland-go loads it on startup and refreshes it periodically.
// GET /internal/nodes/valid → [{"hostid":1,"hostname":"hyper01","status":1}, ...]
func ListValidNodes(c *macaron.Context) {
	ctx, db := GetContextDB(c.Req.Context())
	nodes := []validNode{}
	if err := db.Model(&model.Hyper{}).Select("hostid, hostname, status").Where("hostid >= 0").Scan(&nodes).Error; err != nil {
		logger.Ctx(ctx).Error("Failed to query hypervisors", err)
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to query hypervisors"})
		return
	}
	c.JSON(http.StatusOK, nodes)
}

// VerifyNode checks whether a single hostid exists in the hypers table.
// GET /internal/node/verify?id=<hostid> → 200 or 404
func VerifyNode(c *macaron.Context) {
	id, err := strconv.Atoi(c.Query("id"))
	if err != nil || id < 0 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	_, db := GetContextDB(c.Req.Context())
	hyper := &model.Hyper{}
	if err := db.Where("hostid = ?", id).Take(hyper).Error; err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"status": "not_found"})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "ok", "hostname": hyper.Hostname})
}
