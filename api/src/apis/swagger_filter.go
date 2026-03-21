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
	swaggerFiles "github.com/swaggo/files"
	"github.com/swaggo/swag"
)

var (
	swaggerOnce   sync.Once
	tenantDocJSON string
	adminDocJSON  string
	swaggerTpl    *template.Template
)

const swaggerUITemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>{{.Title}}</title>
    <link rel="stylesheet" type="text/css" href="./swagger-ui.css" >
    <link rel="icon" type="image/png" href="./favicon-32x32.png" sizes="32x32" />
    <link rel="icon" type="image/png" href="./favicon-16x16.png" sizes="16x16" />
    <style>
        html { box-sizing: border-box; overflow: -moz-scrollbars-vertical; overflow-y: scroll; }
        *, *:before, *:after { box-sizing: inherit; }
        body { margin:0; background: #fafafa; }
    </style>
</head>
<body>
<div id="swagger-ui"></div>
<script src="./swagger-ui-bundle.js"></script>
<script src="./swagger-ui-standalone-preset.js"></script>
<script>
window.onload = function() {
    SwaggerUIBundle({
        url: "./doc.json",
        dom_id: '#swagger-ui',
        deepLinking: true,
        presets: [
            SwaggerUIBundle.presets.apis,
            SwaggerUIStandalonePreset
        ],
        plugins: [
            SwaggerUIBundle.plugins.DownloadUrl
        ],
        layout: "StandaloneLayout"
    })
}
</script>
</body>
</html>`

type swaggerPageConfig struct {
	Title string
}

func initSwaggerDocs() {
	fullDoc, err := swag.ReadDoc("v1")
	if err != nil {
		logger.Errorf("Failed to read swagger doc: %v", err)
		return
	}

	tenantDocJSON, _ = filterSwaggerByTags(fullDoc, nil, []string{"Administration"}, "CloudLand Tenant API")
	adminDocJSON, _ = filterSwaggerByTags(fullDoc, []string{"Administration"}, nil, "CloudLand Admin API")
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
	fileServer := http.FileServer(swaggerFiles.HTTP)

	return func(c *gin.Context) {
		swaggerOnce.Do(initSwaggerDocs)

		path := c.Param("any")

		switch {
		case strings.HasPrefix(path, "/tenant"):
			subPath := strings.TrimPrefix(path, "/tenant")
			if subPath == "" || subPath == "/" {
				c.Redirect(http.StatusFound, "./tenant/index.html")
				return
			}
			serveSwaggerAsset(c, subPath, tenantDocJSON, "CloudLand Tenant API", fileServer)

		case strings.HasPrefix(path, "/admin"):
			subPath := strings.TrimPrefix(path, "/admin")
			if subPath == "" || subPath == "/" {
				c.Redirect(http.StatusFound, "./admin/index.html")
				return
			}
			serveSwaggerAsset(c, subPath, adminDocJSON, "CloudLand Admin API", fileServer)

		default:
			// Redirect bare /swagger/api/v1/ to tenant docs
			c.Redirect(http.StatusFound, "./v1/tenant/index.html")
		}
	}
}

func serveSwaggerAsset(c *gin.Context, path, docJSON, title string, fileServer http.Handler) {
	switch {
	case path == "/index.html":
		c.Header("Content-Type", "text/html; charset=utf-8")
		swaggerTpl.Execute(c.Writer, swaggerPageConfig{Title: title})

	case path == "/doc.json":
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.String(http.StatusOK, docJSON)

	default:
		// Serve static assets (CSS, JS, PNG) from embedded swagger files
		c.Request.URL.Path = path
		fileServer.ServeHTTP(c.Writer, c.Request)
	}
}
