package services

import (
	"fmt"

	"api/src/dbs"
	"api/src/model"
)

// SDTarget represents a Prometheus http_sd / file_sd target group.
type SDTarget struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// GetPrometheusTargets queries all hypers and returns target groups
// for the specified exporter type ("libvirt_exporter" or "node_exporter").
func GetPrometheusTargets(exporterType string) ([]SDTarget, error) {
	db := dbs.DB()
	var hypers []*model.Hyper
	if err := db.Where("host_ip != '' AND hostid >= 0").Find(&hypers).Error; err != nil {
		return nil, fmt.Errorf("failed to query hypers: %w", err)
	}

	targets := make([]SDTarget, 0, len(hypers))
	for _, h := range hypers {
		if h.HostIP == "" {
			continue
		}

		var port string
		labels := map[string]string{
			"hostname": h.Hostname,
			"hostid":   fmt.Sprintf("%d", h.Hostid),
		}

		switch exporterType {
		case "libvirt_exporter":
			port = "9177"
		case "node_exporter":
			port = "9101"
			labels["node_type"] = "compute"
		default:
			return nil, fmt.Errorf("unknown exporter type: %s", exporterType)
		}

		targets = append(targets, SDTarget{
			Targets: []string{fmt.Sprintf("%s:%s", h.HostIP, port)},
			Labels:  labels,
		})
	}

	return targets, nil
}
