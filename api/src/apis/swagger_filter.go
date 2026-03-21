/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/swaggo/swag"
)

var (
	swaggerOnce    sync.Once
	swaggerInitErr error
	tenantDocJSON  string
	adminDocJSON   string
	swaggerTpl     *template.Template
)

const swaggerUITemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>{{.Title}}</title>
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>body { margin: 0; padding: 0; }</style>
</head>
<body>
    <redoc spec-url='./doc.json'></redoc>
    <script src="https://cdn.redoc.ly/redoc/v2.1.5/bundles/redoc.standalone.js"></script>
</body>
</html>`

type swaggerPageConfig struct {
	Title string
}

func initSwaggerDocs() {
	fullDoc, err := swag.ReadDoc("v1")
	if err != nil {
		swaggerInitErr = err
		logger.Errorf("Failed to read swagger doc: %v", err)
		return
	}

	tenantDocJSON, err = filterSwaggerByTags(fullDoc, nil, []string{"Administration"}, "CloudLand Tenant API")
	if err != nil {
		swaggerInitErr = err
		logger.Errorf("Failed to filter tenant swagger doc: %v", err)
		return
	}
	adminDocJSON, err = filterSwaggerByTags(fullDoc, []string{"Administration"}, nil, "CloudLand Admin API")
	if err != nil {
		swaggerInitErr = err
		logger.Errorf("Failed to filter admin swagger doc: %v", err)
		return
	}
	swaggerTpl = template.Must(template.New("swagger").Parse(swaggerUITemplate))
}

func filterSwaggerByTags(swaggerJSON string, includeTags, excludeTags []string, title string) (string, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(swaggerJSON), &doc); err != nil {
		return "", err
	}

	// Update title
	if info, ok := doc["info"].(map[string]interface{}); ok {
		info["title"] = title
	}

	// Filter paths
	paths, ok := doc["paths"].(map[string]interface{})
	if !ok {
		return swaggerJSON, nil
	}

	filteredPaths := make(map[string]interface{})
	activeTagSet := make(map[string]bool)

	for apiPath, methods := range paths {
		methodMap, ok := methods.(map[string]interface{})
		if !ok {
			continue
		}

		filteredMethods := make(map[string]interface{})
		for method, operation := range methodMap {
			op, ok := operation.(map[string]interface{})
			if !ok {
				continue
			}

			tags, _ := op["tags"].([]interface{})
			if shouldIncludeOperation(tags, includeTags, excludeTags) {
				filteredMethods[method] = operation
				for _, t := range tags {
					if s, ok := t.(string); ok {
						activeTagSet[s] = true
					}
				}
			}
		}

		if len(filteredMethods) > 0 {
			filteredPaths[apiPath] = filteredMethods
		}
	}

	doc["paths"] = filteredPaths

	// Filter top-level tags array
	if topTags, ok := doc["tags"].([]interface{}); ok {
		var filtered []interface{}
		for _, tag := range topTags {
			if tagMap, ok := tag.(map[string]interface{}); ok {
				if name, ok := tagMap["name"].(string); ok && activeTagSet[name] {
					filtered = append(filtered, tag)
				}
			}
		}
		doc["tags"] = filtered
	}

	// Inject x-tagGroups for Redoc nested category display
	doc["x-tagGroups"] = buildTagGroups(activeTagSet)

	result, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func shouldIncludeOperation(tags []interface{}, includeTags, excludeTags []string) bool {
	if len(includeTags) > 0 {
		for _, t := range tags {
			s, ok := t.(string)
			if !ok {
				continue
			}
			for _, inc := range includeTags {
				if s == inc {
					return true
				}
			}
		}
		return false
	}

	if len(excludeTags) > 0 {
		for _, t := range tags {
			s, ok := t.(string)
			if !ok {
				continue
			}
			for _, exc := range excludeTags {
				if s == exc {
					return false
				}
			}
		}
	}

	return true
}

func swaggerHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		swaggerOnce.Do(initSwaggerDocs)
		if swaggerInitErr != nil {
			c.String(http.StatusInternalServerError, "swagger docs unavailable: %v", swaggerInitErr)
			return
		}

		path := c.Param("any")

		switch {
		case strings.HasPrefix(path, "/tenant"):
			subPath := strings.TrimPrefix(path, "/tenant")
			if subPath == "" || subPath == "/" {
				c.Redirect(http.StatusFound, "./tenant/index.html")
				return
			}
			serveSwaggerAsset(c, subPath, tenantDocJSON, "CloudLand Tenant API")

		case strings.HasPrefix(path, "/admin"):
			subPath := strings.TrimPrefix(path, "/admin")
			if subPath == "" || subPath == "/" {
				c.Redirect(http.StatusFound, "./admin/index.html")
				return
			}
			serveSwaggerAsset(c, subPath, adminDocJSON, "CloudLand Admin API")

		default:
			// Redirect bare /swagger/api/v1/ to tenant docs
			c.Redirect(http.StatusFound, "./tenant/index.html")
		}
	}
}

func serveSwaggerAsset(c *gin.Context, path, docJSON, title string) {
	switch {
	case path == "/index.html":
		c.Header("Content-Type", "text/html; charset=utf-8")
		if err := swaggerTpl.Execute(c.Writer, swaggerPageConfig{Title: title}); err != nil {
			logger.Errorf("Failed to render swagger template: %v", err)
		}

	case path == "/doc.json":
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.String(http.StatusOK, docJSON)

	default:
		c.Status(http.StatusNotFound)
	}
}

type tagGroup struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

var allTagGroups = []tagGroup{
	{
		Name: "计算",
		Tags: []string{"Instance", "Image", "Flavor", "Key", "Console", "Backup"},
	},
	{
		Name: "网络",
		Tags: []string{"VPC", "Subnet", "Floating IP", "IP Group", "Security Group", "Interface", "Load Balancer"},
	},
	{
		Name: "存储",
		Tags: []string{"Volume", "Consistency Group"},
	},
	{
		Name: "监控告警",
		Tags: []string{"Alarm", "Monitoring", "Notification"},
	},
	{
		Name: "弹性伸缩",
		Tags: []string{"Auto Scaling"},
	},
	{
		Name: "计量计费",
		Tags: []string{"OpenMeter"},
	},
	{
		Name: "系统管理",
		Tags: []string{"Administration", "Zone", "Dictionary", "Address", "System Info", "Task"},
	},
}

// buildTagGroups returns x-tagGroups filtered to only include tags present in activeTagSet.
func buildTagGroups(activeTagSet map[string]bool) []tagGroup {
	var groups []tagGroup
	for _, g := range allTagGroups {
		var activeTags []string
		for _, t := range g.Tags {
			if activeTagSet[t] {
				activeTags = append(activeTags, t)
			}
		}
		if len(activeTags) > 0 {
			groups = append(groups, tagGroup{Name: g.Name, Tags: activeTags})
		}
	}
	return groups
}
