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
	fullDocJSON    string
	swaggerTpl     *template.Template
)

const swaggerUITemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <title>{{.Title}}</title>
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800&display=swap" rel="stylesheet">
    <style>
        *, *::before, *::after { box-sizing: border-box; }
        html {
            font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
        }
        body {
            margin: 0;
            padding: 0;
            background: linear-gradient(180deg, #e0f2fe 0%, #f0f9ff 35%, #f8fafc 100%) fixed;
            min-height: 100vh;
            color: #1a2332;
        }

        /* ── Top Nav — matches Login page .pl-nav ── */
        .pl-nav {
            display: flex;
            align-items: center;
            justify-content: space-between;
            padding: 0 48px;
            height: 64px;
            background: rgba(255, 255, 255, 0.8);
            backdrop-filter: blur(12px);
            -webkit-backdrop-filter: blur(12px);
            border-bottom: 1px solid rgba(226, 232, 240, 0.6);
            position: sticky;
            top: 0;
            z-index: 9999;
        }

        .pl-nav-brand {
            display: flex;
            align-items: center;
            gap: 12px;
            text-decoration: none;
            font-weight: 700;
            font-size: 1.1875rem;
            color: #1a2332;
            letter-spacing: -0.01em;
        }

        .pl-logo-wrapper {
            background: #0ea5e9;
            color: white;
            width: 32px;
            height: 32px;
            border-radius: 8px;
            display: flex;
            align-items: center;
            justify-content: center;
            flex-shrink: 0;
        }

        .pl-nav-actions {
            display: flex;
            align-items: center;
            gap: 10px;
        }

        .pl-api-badge {
            display: inline-flex;
            align-items: center;
            padding: 5px 12px;
            background: #f0f9ff;
            border: 1px solid #bae6fd;
            border-radius: 100px;
            font-size: 0.8125rem;
            font-weight: 600;
            color: #0284c7;
        }

        .pl-back-link {
            display: inline-flex;
            align-items: center;
            gap: 6px;
            padding: 8px 16px;
            border: 1px solid #e2e8f0;
            border-radius: 10px;
            background: rgba(255, 255, 255, 0.7);
            color: #475569;
            text-decoration: none;
            font-size: 0.8125rem;
            font-weight: 600;
            transition: all 0.2s;
        }

        .pl-back-link:hover {
            background: #fff;
            border-color: #cbd5e1;
            color: #0ea5e9;
        }

        /* ── Scalar CSS variable overrides ── */
        body,
        .scalar-app,
        #api-reference {
            --scalar-color-1: #1a2332;
            --scalar-color-2: #475569;
            --scalar-color-3: #64748b;
            --scalar-color-accent: #0ea5e9;
            --scalar-font: 'Inter', -apple-system, BlinkMacSystemFont, sans-serif;
            --scalar-radius: 12px;
            --scalar-radius-lg: 16px;
            --scalar-background-1: #ffffff;
            --scalar-background-2: #f8fafc;
            --scalar-background-3: #f0f9ff;
            --scalar-border-color: #e2e8f0;
            --scalar-sidebar-background-1: #ffffff;
            --scalar-sidebar-color-1: #1a2332;
            --scalar-sidebar-color-2: #64748b;
            --scalar-sidebar-color-active: #0ea5e9;
            --scalar-sidebar-background-active: #f0f9ff;
        }
    </style>
</head>
<body>
    <!-- Top Nav (Login page style) -->
    <header class="pl-nav">
        <a href="/" class="pl-nav-brand">
            <div class="pl-logo-wrapper">
                <svg width="18" height="18" viewBox="0 0 24 24" fill="white">
                    <path d="M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9Z"/>
                </svg>
            </div>
            <span>CloudLand</span>
        </a>
        <div class="pl-nav-actions">
            <span class="pl-api-badge">API Reference</span>
            <a href="/" class="pl-back-link">← 返回控制台</a>
        </div>
    </header>

    <script id="api-reference" data-url="{{.SpecURL}}"></script>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.125.12/dist/browser/standalone.min.js"></script>
    <script>
        const reference = document.getElementById('api-reference')
        reference.setAttribute('data-configuration', JSON.stringify({
            theme: 'default',
            showSidebar: true,
            layout: 'modern',
            hideDownloadButton: false,
            metaData: {
                title: '{{.Title}}',
                description: 'APIs for CloudLand Functions'
            },
            customCss: [
                '.sidebar { background: linear-gradient(180deg, #e8f4fd 0%, #ffffff 40%) !important; border-right: 1px solid #e2e8f0 !important; }',
                '.sidebar-group-item.active > .sidebar-group-item-link, .sidebar-item.active { color: #0ea5e9 !important; background: #f0f9ff !important; border-radius: 8px; }',
                'a { color: #0ea5e9; }',
                'button[class*="run"], .run-request-button { background: linear-gradient(135deg, #0ea5e9 0%, #06b6d4 100%) !important; border-radius: 10px !important; border: none !important; }',
                '.scalar-card { border-radius: 12px !important; border-color: #e2e8f0 !important; }'
            ].join(' ')
        }))
    </script>
</body>
</html>`

type swaggerPageConfig struct {
	Title   string
	SpecURL string
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
	fullDocJSON, err = filterSwaggerByTags(fullDoc, nil, nil, "CloudLand API")
	if err != nil {
		swaggerInitErr = err
		logger.Errorf("Failed to filter full swagger doc: %v", err)
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
		view := c.Query("view")
		if view != "" && view != "full" {
			view = ""
		}

		// Determine which doc to serve based on view param
		docJSON := tenantDocJSON
		title := "CloudLand Tenant API"
		if view == "full" {
			docJSON = fullDocJSON
			title = "CloudLand API"
		}

		switch {
		// Legacy paths: redirect to unified URL with absolute path
		case strings.HasPrefix(path, "/tenant"):
			c.Redirect(http.StatusFound, "/swagger/api/v1/index.html")
			return
		case strings.HasPrefix(path, "/admin"):
			c.Redirect(http.StatusFound, "/swagger/api/v1/index.html?view=full")
			return

		default:
			if path == "" || path == "/" {
				c.Redirect(http.StatusFound, "./index.html")
				return
			}
			serveSwaggerAsset(c, path, docJSON, title, view)
		}
	}
}

func serveSwaggerAsset(c *gin.Context, path, docJSON, title, view string) {
	switch {
	case path == "/index.html":
		specURL := "./doc.json"
		if view != "" {
			specURL = "./doc.json?view=" + view
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		if err := swaggerTpl.Execute(c.Writer, swaggerPageConfig{Title: title, SpecURL: specURL}); err != nil {
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
		Tags: []string{"Zone", "Dictionary", "Address", "System Info", "Task"},
	},
	{
		Name: "宿主机管理",
		Tags: []string{"Hypervisor"},
	},
	{
		Name: "可用区管理",
		Tags: []string{"Zone Admin"},
	},
	{
		Name: "规格管理",
		Tags: []string{"Flavor Admin"},
	},
	{
		Name: "字典管理",
		Tags: []string{"Dictionary Admin"},
	},
	{
		Name: "地址管理",
		Tags: []string{"Address Admin"},
	},
	{
		Name: "迁移管理",
		Tags: []string{"Migration"},
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
