package apis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"cpgateway/src/common"
	"cpgateway/src/dbs"
	"cpgateway/src/model"
	"cpgateway/src/services"
)

func resolveQueryRegion(c *gin.Context) (*model.Region, bool) {
	uuid, ok := requireQuery(c, "region")
	if !ok {
		return nil, false
	}
	var region model.Region
	if err := dbs.DBContext(c.Request.Context()).Where("uuid = ?", uuid).First(&region).Error; err != nil {
		common.AbortWithDetail(c, http.StatusNotFound, fmt.Sprintf("Region '%s' not found", uuid))
		return nil, false
	}
	return &region, true
}

// clapiRequest calls a region's clapi internal endpoint authenticated with the region secret.
func clapiRequest(ctx context.Context, region *model.Region, method, path string) (map[string]interface{}, *common.HTTPError) {
	req, err := http.NewRequestWithContext(ctx, method, services.RegionInternalURL(region, path), nil)
	if err != nil {
		return nil, common.NewHTTPError(http.StatusBadGateway, fmt.Sprintf("Region clapi unreachable: %v", err))
	}
	req.Header.Set("X-Forwarded-Secret", region.InternalSecret)

	resp, err := services.InsecureClient(15 * time.Second).Do(req)
	if err != nil {
		log.WithContext(ctx).Errorf("infrastructure proxy to '%s' failed: %v", region.Name, err)
		return nil, common.NewHTTPError(http.StatusBadGateway, fmt.Sprintf("Region clapi unreachable: %v", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		log.WithContext(ctx).Errorf("clapi '%s' returned %d for %s %s: %s", region.Name, resp.StatusCode, method, path, truncate(string(body), 2000))
		return nil, common.NewHTTPError(http.StatusBadGateway,
			fmt.Sprintf("Region clapi returned %d: %s", resp.StatusCode, truncate(string(body), 200)))
	}
	data := map[string]interface{}{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, common.NewHTTPError(http.StatusBadGateway, "Region clapi returned non-JSON body")
	}
	return data, nil
}

// GET /system/infrastructure?region= (superuser)
func GetInfrastructure(c *gin.Context) {
	region, ok := resolveQueryRegion(c)
	if !ok {
		return
	}
	data, herr := clapiRequest(c.Request.Context(), region, http.MethodGet, "/internal/runtime-config")
	if herr != nil {
		common.AbortWithError(c, herr)
		return
	}
	data["region_name"] = region.Name
	if region.DisplayName != nil && *region.DisplayName != "" {
		data["region_name"] = *region.DisplayName
	}
	data["region_uuid"] = region.UUID
	c.JSON(http.StatusOK, data)
}

// POST /system/infrastructure/test-s3?region= (superuser)
func TestS3(c *gin.Context) {
	region, ok := resolveQueryRegion(c)
	if !ok {
		return
	}
	data, herr := clapiRequest(c.Request.Context(), region, http.MethodPost, "/internal/runtime-config/test-s3")
	if herr != nil {
		common.AbortWithError(c, herr)
		return
	}
	c.JSON(http.StatusOK, data)
}
