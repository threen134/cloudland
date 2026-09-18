/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

*/

package apis

import (
	"fmt"
	"github.com/spf13/viper"
	"net/http"

	. "api/src/common"
	"api/src/services"

	"github.com/gin-gonic/gin"
)

var consoleAPI = &ConsoleAPI{}

type ConsoleAPI struct{}

type ConsoleResponse struct {
	Instance    *ResourceReference `json:"instance"`
	Token       string             `json:"token"`
	ConsoleURL  string             `json:"console_url"`
	ConsoleHost string             `json:"console_host"`
	ConsolePort int                `json:"console_port"`
	ConsolePath string             `json:"console_path"`
}

type ConsolePayload struct {
	// vnc (graphical, default) or serial (text console on the first serial port)
	Type string `json:"type" binding:"omitempty,oneof=vnc serial"`
}

// @Summary create a console
// @Description create a console access token; type vnc (graphical, default) or serial (text)
// @tags Console
// @Accept  json
// @Produce json
// @Param   id  path  int  true  "Instance ID"
// @Param   message	body   ConsolePayload  false   "Console type"
// @Success 200 {object} ConsoleResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 401 {object} common.APIError "Not authorized"
// @Router /instances/:id/console [post]
func (v *ConsoleAPI) Create(c *gin.Context) {
	ctx := c.Request.Context()
	uuID := c.Param("id")
	logger.Ctx(ctx).Debugf("Create console for instance %s", uuID)
	instance, err := instanceAdmin.GetInstanceByUUID(ctx, uuID)
	if err != nil {
		logger.Ctx(ctx).Errorf("Not able to get instance %s", uuID)
		ErrorResponse(c, http.StatusBadRequest, "Invalid instance query", err)
		return
	}
	payload := &ConsolePayload{}
	// The body is optional: no body means a VNC console
	if c.Request.ContentLength > 0 {
		if err = c.ShouldBindJSON(payload); err != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
			return
		}
	}
	consoleType := payload.Type
	if consoleType == "" {
		consoleType = ConsoleTypeVNC
	}
	token, err := services.MakeToken(ctx, instance, consoleType)
	if err != nil {
		logger.Ctx(ctx).Errorf("Not able to create token for instance %s", uuID)
		ErrorResponse(c, http.StatusBadRequest, "Not able to create", err)
		return
	}
	owner := orgAdmin.GetOrgName(ctx, instance.Owner)
	accessAddr := viper.GetString("console.host")
	accessPort := viper.GetInt("console.port")
	if accessAddr == "" {
		accessAddr = c.Request.Host
	} else if accessPort != 0 && accessPort != 443 && accessPort != 80 {
		accessAddr = fmt.Sprintf("%s:%d", accessAddr, accessPort)
	}
	consoleURL := fmt.Sprintf("wss://%s/websockify?token=%s", accessAddr, token)
	consoleResp := &ConsoleResponse{
		Instance: &ResourceReference{
			ID:    instance.UUID,
			Owner: owner,
		},
		Token:       token,
		ConsoleURL:  consoleURL,
		ConsoleHost: accessAddr,
		ConsolePort: accessPort,
		ConsolePath: "websockify",
	}
	logger.Ctx(ctx).Debugf("Console URL for instance %s : %s", uuID, consoleURL)
	c.JSON(http.StatusOK, consoleResp)
}

type HostConsoleResponse struct {
	Hyper      *ResourceReference `json:"hyper"`
	Token      string             `json:"token"`
	ConsoleURL string             `json:"console_url"`
	// Seconds without traffic after which the node closes the session
	IdleTimeout int `json:"idle_timeout"`
}

type HostConsolePayload struct {
	// Terminal size the shell starts with
	Rows int `json:"rows" binding:"omitempty,min=1,max=500"`
	Cols int `json:"cols" binding:"omitempty,min=1,max=1000"`
}

// @Summary create a host console
// @Description create a single-use token for a root shell on a hypervisor (system admins only, when enabled in system settings). Through the gateway the body must also carry "password", the caller's login password, which the gateway checks and removes
// @tags Administration,Hypervisor
// @Accept  json
// @Produce json
// @Param   uuid  path  string  true  "Hypervisor UUID"
// @Param   message	body   HostConsolePayload  false   "Terminal size"
// @Success 200 {object} HostConsoleResponse
// @Failure 400 {object} common.APIError "Bad request"
// @Failure 403 {object} common.APIError "Not authorized or host console disabled"
// @Router /hypers/{uuid}/console [post]
func (v *ConsoleAPI) CreateHost(c *gin.Context) {
	ctx := c.Request.Context()
	uuid := c.Param("uuid")
	payload := &HostConsolePayload{}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(payload); err != nil {
			ErrorResponse(c, http.StatusBadRequest, "Invalid input JSON", err)
			return
		}
	}
	hyper, err := hyperAdmin.GetHyperByUUID(ctx, uuid)
	if err != nil {
		ErrorResponse(c, http.StatusNotFound, "Hypervisor not found", err)
		return
	}
	// Nodes being deployed or disconnected cannot run the console script
	switch hyper.Status {
	case 0, 1, 2:
	default:
		ErrorResponse(c, http.StatusBadRequest, "Hypervisor is not connected", nil)
		return
	}
	token, err := services.MakeHostToken(ctx, hyper, payload.Rows, payload.Cols)
	if err != nil {
		ErrorResponse(c, http.StatusInternalServerError, "Not able to create host console", err)
		return
	}
	logger.Ctx(ctx).Infof("Host console token issued for hypervisor %s (hostid %d) to %s", hyper.Hostname, hyper.Hostid, GetMemberShip(ctx).UserName)
	accessAddr := viper.GetString("console.host")
	accessPort := viper.GetInt("console.port")
	if accessAddr == "" {
		accessAddr = c.Request.Host
	} else if accessPort != 0 && accessPort != 443 && accessPort != 80 {
		accessAddr = fmt.Sprintf("%s:%d", accessAddr, accessPort)
	}
	c.JSON(http.StatusOK, &HostConsoleResponse{
		Hyper:       &ResourceReference{ID: hyper.UUID, Name: hyper.Hostname},
		Token:       token,
		ConsoleURL:  fmt.Sprintf("wss://%s/websockify?token=%s", accessAddr, token),
		IdleTimeout: services.HostConsoleIdleSeconds(),
	})
}
