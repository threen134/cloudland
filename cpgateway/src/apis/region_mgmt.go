package apis

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"cpgateway/src/common"
	"cpgateway/src/dbs"
	"cpgateway/src/model"
	"cpgateway/src/services"
)

var regionNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// generateSecret mirrors secrets.token_urlsafe(64).
func generateSecret() string {
	b := make([]byte, 64)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func validEndpoint(v string) bool {
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
}

func getRegionOr404(c *gin.Context) (*model.Region, bool) {
	var region model.Region
	if err := dbs.DBContext(c.Request.Context()).Where("uuid = ?", c.Param("uuid")).First(&region).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, "Region not found")
		return nil, false
	}
	return &region, true
}

// POST /regions (superuser) — the secret is only returned here.
func CreateRegion(c *gin.Context) {
	var in struct {
		Name             string  `json:"name" binding:"required"`
		DisplayName      *string `json:"display_name"`
		InternalEndpoint string  `json:"internal_endpoint" binding:"required"`
		InternalSecret   *string `json:"internal_secret"`
		Description      *string `json:"description"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if !validEndpoint(in.InternalEndpoint) {
		common.AbortValidation(c, "body", errors.New("Internal endpoint must start with http:// or https://"))
		return
	}
	if !regionNamePattern.MatchString(in.Name) {
		common.AbortWithDetail(c, http.StatusBadRequest, "Region name must match [a-z0-9-], min 2 chars")
		return
	}

	db := dbs.DBContext(c.Request.Context())
	var count int64
	db.Model(&model.Region{}).Where("name = ?", in.Name).Count(&count)
	if count > 0 {
		common.AbortWithDetail(c, http.StatusBadRequest, fmt.Sprintf("Region '%s' already exists", in.Name))
		return
	}

	secret := "auto-generate"
	if in.InternalSecret != nil {
		secret = *in.InternalSecret
	}
	if secret == "auto-generate" {
		secret = generateSecret()
	}
	displayName := in.Name
	if in.DisplayName != nil && *in.DisplayName != "" {
		displayName = *in.DisplayName
	}

	region := model.Region{
		Name:             in.Name,
		DisplayName:      &displayName,
		InternalEndpoint: strings.TrimSpace(in.InternalEndpoint),
		InternalSecret:   secret,
		IsAvailable:      false,
		Description:      in.Description,
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&region).Error; err != nil {
			return err
		}
		return services.InitializeRegionQuotas(tx, region.ID)
	})
	if err != nil {
		internalServerError(c, err)
		return
	}

	log.WithContext(c).Infof("Region '%s' created by %s", region.Name, currentUser(c).Username)
	go services.ProvisionRegion(context.WithoutCancel(c.Request.Context()), region.ID, region.Name)
	c.JSON(http.StatusCreated, regionCreatedOut{toRegionAdmin(&region), region.InternalSecret})
}

// GET /regions?skip=&limit= — public, no internal endpoint or secret.
func ListRegions(c *gin.Context) {
	p, ok := parseListParams(c)
	if !ok {
		return
	}
	var regions []model.Region
	q := dbs.DBContext(c.Request.Context()).Model(&model.Region{}).
		Scopes(searchScope(p.Query, "name", "description"))
	total, ok := countAndPage(c, q.Order("id ASC"), p, &regions)
	if !ok {
		return
	}
	out := make([]regionPublicOut, 0, len(regions))
	for i := range regions {
		out = append(out, toRegionPublic(&regions[i]))
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "regions": out})
}

// GET /regions/:uuid (superuser)
func GetRegion(c *gin.Context) {
	region, ok := getRegionOr404(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, toRegionAdmin(region))
}

func decodeOptional(raw json.RawMessage, dst interface{}) (isNull bool, err error) {
	if string(raw) == "null" {
		return true, nil
	}
	return false, json.Unmarshal(raw, dst)
}

// PATCH /regions/:uuid (superuser) — entering maintenance takes the region offline; bringing it
// online exits maintenance and re-provisions it.
func UpdateRegion(c *gin.Context) {
	var raw map[string]json.RawMessage
	if err := c.ShouldBindJSON(&raw); err != nil {
		common.AbortValidation(c, "body", err)
		return
	}
	region, ok := getRegionOr404(c)
	if !ok {
		return
	}

	updates := map[string]interface{}{}
	for _, field := range []string{"display_name", "internal_endpoint", "description"} {
		v, set := raw[field]
		if !set {
			continue
		}
		var s string
		isNull, err := decodeOptional(v, &s)
		if err != nil {
			common.AbortValidation(c, "body", fmt.Errorf("%s must be a string", field))
			return
		}
		if isNull {
			updates[field] = nil
			continue
		}
		if field == "internal_endpoint" {
			if s != "" && !validEndpoint(s) {
				common.AbortValidation(c, "body", errors.New("Internal endpoint must start with http:// or https://"))
				return
			}
			s = strings.TrimSpace(s)
		}
		updates[field] = s
	}

	flags := map[string]*bool{}
	for _, field := range []string{"is_available", "maintenance_mode"} {
		v, set := raw[field]
		if !set {
			continue
		}
		var b bool
		isNull, err := decodeOptional(v, &b)
		if err != nil {
			common.AbortValidation(c, "body", fmt.Errorf("%s must be a boolean", field))
			return
		}
		if !isNull {
			flags[field] = &b
			updates[field] = b
		}
	}

	maintenance, available := flags["maintenance_mode"], flags["is_available"]
	if maintenance != nil && *maintenance {
		updates["is_available"] = false
	} else if available != nil && *available {
		updates["maintenance_mode"] = false
	}
	broughtOnline := available != nil && *available && !(maintenance != nil && *maintenance)

	db := dbs.DBContext(c.Request.Context())
	if len(updates) > 0 {
		if err := db.Model(region).Updates(updates).Error; err != nil {
			internalServerError(c, err)
			return
		}
	}
	db.Where("id = ?", region.ID).First(region)

	if broughtOnline {
		go services.ProvisionRegion(context.WithoutCancel(c.Request.Context()), region.ID, region.Name)
	}
	log.WithContext(c).Infof("Region '%s' updated by %s", region.Name, currentUser(c).Username)
	c.JSON(http.StatusOK, toRegionAdmin(region))
}

// DELETE /regions/:uuid (superuser) — requires maintenance mode and zero consumption.
func DeleteRegion(c *gin.Context) {
	region, ok := getRegionOr404(c)
	if !ok {
		return
	}
	if !region.MaintenanceMode {
		common.AbortWithDetail(c, http.StatusBadRequest, fmt.Sprintf(
			"Cannot delete region '%s': The region must be put into maintenance mode first.", region.Name))
		return
	}

	db := dbs.DBContext(c.Request.Context())
	var usage struct {
		CPU           float64 `gorm:"column:cpu"`
		RAM           float64 `gorm:"column:ram"`
		Disk          float64 `gorm:"column:disk"`
		IPs           float64 `gorm:"column:ips"`
		VPCs          float64 `gorm:"column:vpcs"`
		LoadBalancers float64 `gorm:"column:load_balancers"`
		Images        float64 `gorm:"column:images"`
	}
	db.Model(&model.OrgResourceConsumption{}).
		Select("COALESCE(SUM(cpu_cores),0) AS cpu, COALESCE(SUM(ram_gb),0) AS ram, COALESCE(SUM(disk_gb),0) AS disk, COALESCE(SUM(public_ips),0) AS ips, "+
			"COALESCE(SUM(vpcs),0) AS vpcs, COALESCE(SUM(load_balancers),0) AS load_balancers, COALESCE(SUM(images),0) AS images").
		Where("region_id = ?", region.ID).Scan(&usage)
	if usage.CPU > 0 || usage.RAM > 0 || usage.Disk > 0 || usage.IPs > 0 || usage.VPCs > 0 || usage.LoadBalancers > 0 || usage.Images > 0 {
		common.AbortWithDetail(c, http.StatusBadRequest, fmt.Sprintf(
			"Cannot delete region '%s': There are still active resources (VMs, volumes, floating IPs, VPCs, load balancers or images). Please delete all resources in this region first.", region.Name))
		return
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("region_id = ?", region.ID).Delete(&model.OrgResourceQuota{}).Error; err != nil {
			return err
		}
		if err := tx.Where("region_id = ?", region.ID).Delete(&model.OrgResourceConsumption{}).Error; err != nil {
			return err
		}
		return tx.Delete(region).Error
	})
	if err != nil {
		internalServerError(c, err)
		return
	}
	log.WithContext(c).Infof("Region '%s' deleted by %s", region.Name, currentUser(c).Username)
	c.Status(http.StatusNoContent)
}

// POST /regions/:uuid/rotate-secret (superuser)
func RotateRegionSecret(c *gin.Context) {
	region, ok := getRegionOr404(c)
	if !ok {
		return
	}
	newSecret := generateSecret()
	if err := dbs.DBContext(c.Request.Context()).Model(region).Update("internal_secret", newSecret).Error; err != nil {
		internalServerError(c, err)
		return
	}
	log.WithContext(c).Infof("Region '%s' secret rotated by %s", region.Name, currentUser(c).Username)
	c.JSON(http.StatusOK, gin.H{"region_uuid": region.UUID, "name": region.Name, "new_secret": newSecret})
}
