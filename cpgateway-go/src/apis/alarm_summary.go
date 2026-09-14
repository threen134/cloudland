package apis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"cpgateway-go/src/common"
	"cpgateway-go/src/dbs"
	"cpgateway-go/src/model"
	"cpgateway-go/src/services"
)

type alarmSummaryRegion struct {
	RegionUUID  string `json:"region_uuid"`
	RegionName  string `json:"region_name"`
	FiringCount int    `json:"firing_count"`
}

// firingCount returns the firing alarm count for an org in a region, or -1 if unreachable.
func firingCount(ctx context.Context, region *model.Region, orgUUID string) int {
	q := url.Values{"status": {"firing"}, "count_only": {"true"}, "org_uuid": {orgUUID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, services.RegionInternalURL(region, "/internal/alarm/events")+"?"+q.Encode(), nil)
	if err != nil {
		return -1
	}
	req.Header.Set("X-Forwarded-Secret", region.InternalSecret)
	req.Header.Set("X-User-ID", "0")
	req.Header.Set("X-System-Role", "1")

	resp, err := services.InsecureClient(2 * time.Second).Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return -1
	}
	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return -1
	}
	count, _ := services.ToFloat(data["count"])
	return int(count)
}

// GET /alarm/summary — firing alarms of the current org across all active regions.
func GetAlarmSummary(c *gin.Context) {
	orgUUID := currentClaims(c).OrgID
	if orgUUID == "" {
		common.AbortWithDetail(c, http.StatusBadRequest, "No active organization in token")
		return
	}

	regions := services.SyncTargetRegions(dbs.DBContext(c.Request.Context()))
	counts := make([]int, len(regions))
	// 在启动 goroutine 前取出请求 ctx：超时后 handler 先返回，gin 会复用 *gin.Context，goroutine 内不能再读 c
	ctx := c.Request.Context()
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := range regions {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				counts[i] = firingCount(ctx, &regions[i], orgUUID)
			}(i)
		}
		wg.Wait()
		close(done)
	}()

	timedOut := false
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		timedOut = true
	}

	summaries := make([]alarmSummaryRegion, 0, len(regions))
	total := 0
	for i := range regions {
		count := -1
		if !timedOut {
			count = counts[i]
		}
		name := regions[i].Name
		if regions[i].DisplayName != nil && *regions[i].DisplayName != "" {
			name = *regions[i].DisplayName
		}
		summaries = append(summaries, alarmSummaryRegion{regions[i].UUID, name, count})
		if count > 0 {
			total += count
		}
	}
	c.JSON(http.StatusOK, gin.H{"regions": summaries, "total_firing": total})
}
