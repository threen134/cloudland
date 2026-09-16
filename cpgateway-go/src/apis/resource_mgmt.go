package apis

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"cpgateway-go/src/common"
	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
	"cpgateway-go/src/services"
)

// resolveResourceOrg loads the org and checks the caller is a SystemAdmin or a member.
func resolveResourceOrg(c *gin.Context) (*model.Organization, bool) {
	org, ok := getOrgOr404(c, c.Param("org_uuid"))
	if !ok {
		return nil, false
	}
	if me := currentUser(c); !me.IsAdmin() && !isMember(me.ID, org.ID) {
		common.AbortWithDetail(c, http.StatusForbidden, "You are not a member of this organization")
		return nil, false
	}
	return org, true
}

func getRegionByParamOr404(c *gin.Context) (*model.Region, bool) {
	uuid := c.Param("region_uuid")
	var region model.Region
	if err := dbs.DBContext(c.Request.Context()).Where("uuid = ?", uuid).First(&region).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, fmt.Sprintf("Region '%s' not found", uuid))
		return nil, false
	}
	return &region, true
}

func loadRegionsByID() map[int64]*model.Region {
	var regions []model.Region
	dbs.DB().Find(&regions)
	m := make(map[int64]*model.Region, len(regions))
	for i := range regions {
		m[regions[i].ID] = &regions[i]
	}
	return m
}

// GET /resources/quota/:org_uuid
func GetOrgQuotas(c *gin.Context) {
	org, ok := resolveResourceOrg(c)
	if !ok {
		return
	}
	var quotas []model.OrgResourceQuota
	dbs.DBContext(c.Request.Context()).Where("org_id = ?", org.ID).Order("id ASC").Find(&quotas)
	regions := loadRegionsByID()
	out := make([]quotaOut, 0, len(quotas))
	for i := range quotas {
		if r, found := regions[quotas[i].RegionID]; found {
			out = append(out, toQuotaOut(&quotas[i], org.UUID, r))
		}
	}
	c.JSON(http.StatusOK, out)
}

// GET /resources/quota/:org_uuid/:region_uuid
func GetOrgRegionQuota(c *gin.Context) {
	org, ok := resolveResourceOrg(c)
	if !ok {
		return
	}
	region, ok := getRegionByParamOr404(c)
	if !ok {
		return
	}
	var quota model.OrgResourceQuota
	if err := dbs.DBContext(c.Request.Context()).Where("org_id = ? AND region_id = ?", org.ID, region.ID).First(&quota).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, "Quota record not found for this org-region")
		return
	}
	c.JSON(http.StatusOK, toQuotaOut(&quota, org.UUID, region))
}

// PUT /resources/quota/:org_uuid/:region_uuid (superuser) — partial update, values must be >= 0.
func UpdateOrgRegionQuota(c *gin.Context) {
	var raw map[string]json.RawMessage
	if err := c.ShouldBindJSON(&raw); err != nil {
		common.AbortValidation(c, "body", err)
		return
	}
	updates := map[string]interface{}{}
	for _, resource := range services.QuotaResourceFields {
		field := "max_" + resource
		v, set := raw[field]
		if !set || string(v) == "null" {
			continue
		}
		var n float64
		if err := json.Unmarshal(v, &n); err != nil {
			common.AbortValidation(c, "body", fmt.Errorf("%s must be a number", field))
			return
		}
		if n < 0 {
			common.AbortValidation(c, "body", fmt.Errorf("%s: Input should be greater than or equal to 0", field))
			return
		}
		if services.IsIntegerQuotaField(resource) {
			if n != float64(int(n)) {
				common.AbortValidation(c, "body", fmt.Errorf("%s must be an integer", field))
				return
			}
			updates[field] = int(n)
		} else {
			updates[field] = n
		}
	}

	org, ok := getOrgOr404(c, c.Param("org_uuid"))
	if !ok {
		return
	}
	region, ok := getRegionByParamOr404(c)
	if !ok {
		return
	}
	db := dbs.DBContext(c.Request.Context())
	var quota model.OrgResourceQuota
	if err := db.Where("org_id = ? AND region_id = ?", org.ID, region.ID).First(&quota).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, "Quota record not found for this org-region")
		return
	}
	if len(updates) > 0 {
		if err := db.Model(&quota).Updates(updates).Error; err != nil {
			internalServerError(c, err)
			return
		}
	}
	db.Where("id = ?", quota.ID).First(&quota)
	c.JSON(http.StatusOK, toQuotaOut(&quota, org.UUID, region))
}

// GET /resources/consumption/:org_uuid
func GetOrgConsumptions(c *gin.Context) {
	org, ok := resolveResourceOrg(c)
	if !ok {
		return
	}
	var consumptions []model.OrgResourceConsumption
	dbs.DBContext(c.Request.Context()).Where("org_id = ?", org.ID).Order("id ASC").Find(&consumptions)
	regions := loadRegionsByID()
	out := make([]consumptionOut, 0, len(consumptions))
	for i := range consumptions {
		if r, found := regions[consumptions[i].RegionID]; found {
			out = append(out, consumptionOut{toConsumptionFields(&consumptions[i]), org.UUID, r.UUID, r.Name})
		}
	}
	c.JSON(http.StatusOK, out)
}

// GET /resources/consumption/:org_uuid/:region_uuid
func GetOrgRegionConsumption(c *gin.Context) {
	org, ok := resolveResourceOrg(c)
	if !ok {
		return
	}
	region, ok := getRegionByParamOr404(c)
	if !ok {
		return
	}
	var consumption model.OrgResourceConsumption
	if err := dbs.DBContext(c.Request.Context()).Where("org_id = ? AND region_id = ?", org.ID, region.ID).First(&consumption).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, "Consumption record not found for this org-region")
		return
	}
	c.JSON(http.StatusOK, consumptionOut{toConsumptionFields(&consumption), org.UUID, region.UUID, region.Name})
}

// resourceInfos joins quota, consumption and region rows (inner join semantics).
func resourceInfos(orgID int64, regionID *int64) []resourceInfoOut {
	db := dbs.DB()
	q := db.Where("org_id = ?", orgID)
	if regionID != nil {
		q = q.Where("region_id = ?", *regionID)
	}
	var quotas []model.OrgResourceQuota
	q.Order("id ASC").Find(&quotas)

	var consumptions []model.OrgResourceConsumption
	db.Where("org_id = ?", orgID).Find(&consumptions)
	consByRegion := make(map[int64]*model.OrgResourceConsumption, len(consumptions))
	for i := range consumptions {
		consByRegion[consumptions[i].RegionID] = &consumptions[i]
	}
	regions := loadRegionsByID()

	out := make([]resourceInfoOut, 0, len(quotas))
	for i := range quotas {
		cons, hasCons := consByRegion[quotas[i].RegionID]
		region, hasRegion := regions[quotas[i].RegionID]
		if !hasCons || !hasRegion {
			continue
		}
		out = append(out, resourceInfoOut{region.UUID, region.Name, toConsumptionFields(cons), toQuotaFields(&quotas[i])})
	}
	return out
}

// GET /resources/info/:org_uuid
func GetOrgResourceSummary(c *gin.Context) {
	org, ok := resolveResourceOrg(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"org_uuid": org.UUID, "regions": resourceInfos(org.ID, nil)})
}

// GET /resources/info/:org_uuid/:region_uuid
func GetOrgRegionResourceInfo(c *gin.Context) {
	org, ok := resolveResourceOrg(c)
	if !ok {
		return
	}
	region, ok := getRegionByParamOr404(c)
	if !ok {
		return
	}
	infos := resourceInfos(org.ID, &region.ID)
	if len(infos) == 0 {
		common.AbortWithDetail(c, http.StatusNotFound, "Quota/consumption record not found for this org-region")
		return
	}
	c.JSON(http.StatusOK, infos[0])
}
