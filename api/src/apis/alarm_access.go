/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

// Alignment 04 (test-items/alignment/04-告警控制台部署与计算节点.md): who may change a VM rule group and which
// instances it may watch.

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"api/src/common"
	"api/src/model"
	"api/src/services"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

// alertWebhookAuth guards the Alertmanager webhooks, which sit outside the JWT group: /alerts/process writes
// alarm events of any organization and sends them to its channels, /alerts/resource-adjustment throttles any
// instance. clapi's port is reachable from the whole management network, so the caller must present the
// token shared with Alertmanager (ALERT_WEBHOOK_TOKEN). Without a configured token every call is refused (O4)
func alertWebhookAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		want := viper.GetString("alert.webhook_token")
		got, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if want == "" || !ok || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid alert webhook token"})
			return
		}
		c.Next()
	}
}

// ruleRegionRe limits region_id, which the rule templates write between double quotes into a Prometheus rule
// file: a quote or a newline there breaks the file, and Prometheus then rejects every rule group (O3)
var ruleRegionRe = regexp.MustCompile(`^[A-Za-z0-9._-]{0,64}$`)

// ruleRegionValid answers the request and returns false when region_id may not go into a rule file
func ruleRegionValid(c *gin.Context, region string) bool {
	if ruleRegionRe.MatchString(region) {
		return true
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid region_id: up to 64 letters, digits, '.', '_' or '-'"})
	return false
}

// ruleWriter answers the request and returns false when the caller may not create rules in their organization
func ruleWriter(c *gin.Context) bool {
	ms := common.GetMemberShip(c.Request.Context())
	if ms.OrgID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid organization context"})
		return false
	}
	if !ms.CheckOrgPermission(model.OrgWriter) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized to create alarm rules"})
		return false
	}
	return true
}

// ruleGroupWritable answers the request and returns false when the caller may not change the rule group
// owned by owner. A group of an organization the caller can not read gets the answer of a missing one
func ruleGroupWritable(c *gin.Context, owner int64) bool {
	ms := common.GetMemberShip(c.Request.Context())
	if ms.CheckResourceOrg(model.OrgWriter, owner) {
		return true
	}
	if ms.CheckResourceOrg(model.OrgReader, owner) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized to change the rule group"})
	} else {
		c.JSON(http.StatusNotFound, gin.H{"error": "Rule group not found"})
	}
	return false
}

// requireSystemAdminJSON answers the request and returns false unless the caller is a system admin. Endpoints
// the gateway whitelist marks admin-only check it here too, as the hypervisor and migration endpoints do (O6)
func requireSystemAdminJSON(c *gin.Context) bool {
	if common.GetMemberShip(c.Request.Context()).IsSystemAdmin() {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "Not authorized for this operation"})
	return false
}

// adjustGroupOwner reads the owner of a resource adjustment rule group, stored as a string
func adjustGroupOwner(group *model.AdjustRuleGroup) int64 {
	owner, _ := strconv.ParseInt(group.Owner, 10, 64)
	return owner
}

// ruleInstancesOwnedBy answers the request and returns false unless every instance belongs to owner, the
// organization of the rule group: the group's alarm events and notifications go to that organization, so another
// organization's instance would leak its metrics there (O1). The caller must also be able to read them; a system
// admin can read every instance, which is why the owner is compared and not only readability
func ruleInstancesOwnedBy(c *gin.Context, uuids []string, owner int64) bool {
	if len(uuids) == 0 {
		return true
	}
	ctx := c.Request.Context()
	ids, err := services.ReadableInstanceIDs(ctx, uuids)
	if err == nil {
		idList := make([]int64, 0, len(ids))
		for _, id := range ids {
			idList = append(idList, id)
		}
		var foreign int64
		_, db := common.GetContextDB(ctx)
		if err = db.Model(&model.Instance{}).Where("id IN ? AND owner <> ?", idList, owner).Count(&foreign).Error; err == nil && foreign > 0 {
			err = common.NewCLError(common.ErrInstanceNotFound, "Instance of another organization", nil)
		}
	}
	if err != nil {
		var clErr *common.CLError
		if errors.As(err, &clErr) && clErr.Code == common.ErrInstanceNotFound {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Some instances do not exist", "code": "INSTANCES_NOT_FOUND"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check the instances"})
		}
		return false
	}
	return true
}
