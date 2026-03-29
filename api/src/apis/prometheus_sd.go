package apis

import (
	"net/http"

	"api/src/services"

	"github.com/gin-gonic/gin"
)

// PrometheusSDAPI provides HTTP service-discovery endpoints for Prometheus.
type PrometheusSDAPI struct{}

var prometheusSDAPI = &PrometheusSDAPI{}

// GetTargets returns Prometheus http_sd compatible target list.
// GET /api/v1/prometheus/sd/:exporter
// :exporter = "libvirt_exporter" | "node_exporter"
func (api *PrometheusSDAPI) GetTargets(c *gin.Context) {
	exporter := c.Param("exporter")
	targets, err := services.GetPrometheusTargets(exporter)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, targets)
}
