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
