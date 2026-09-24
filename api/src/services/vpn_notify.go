/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"fmt"
	"strconv"
	"time"

	. "api/src/common"
	"api/src/model"
)

// VpnBgpReport is the BGP snapshot reported by the master node for one connection, kept as JSON on the
// connection for display only: it never feeds a dispatch
type VpnBgpReport struct {
	Name             string   `json:"name"`
	State            string   `json:"state"`
	Uptime           string   `json:"uptime"`
	PrefixesReceived int      `json:"prefixes_received"`
	PrefixesSent     int      `json:"prefixes_sent"`
	Accepted         []string `json:"accepted"`
	Rejected         []string `json:"rejected"`
	Advertised       []string `json:"advertised"`
	Truncated        bool     `json:"truncated"`
}

const vpnAlertName = "VpnConnectionDown"

// NotifyVpnConnectionState raises an alarm event when a tunnel goes from up to down and resolves it when
// the tunnel comes back, and pushes both to every enabled notification channel of the organization.
// Tunnel state lives in the database, not in Prometheus, so this bypasses the rule/binding machinery of
// the metric alarms; the events still show up in the alarm event list and carry delivery logs.
// Runs in the background: channel calls can take seconds and the caller is a heartbeat callback.
func NotifyVpnConnectionState(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection, up bool, at time.Time) {
	bg := SetContextDB(context.WithoutCancel(ctx), DB())
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Ctx(bg).Errorf("VPN notification panic: %v", r)
			}
		}()
		notifyVpnConnectionState(bg, gateway, conn, up, at)
	}()
}

func notifyVpnConnectionState(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection, up bool, at time.Time) {
	ctx, db := GetContextDB(ctx)
	admin := &NotificationAdmin{}
	labels := map[string]string{
		"alertname":      vpnAlertName,
		"owner":          strconv.FormatInt(gateway.Owner, 10),
		"severity":       "warning",
		"summary":        fmt.Sprintf("VPN connection %s of gateway %s is down", conn.Name, gateway.Name),
		"vm_name":        fmt.Sprintf("%s / %s", gateway.Name, conn.Name),
		"vm_uuid":        gateway.UUID,
		"vpn_gateway":    gateway.UUID,
		"vpn_connection": conn.UUID,
		"source":         "vpn",
	}
	type pending struct {
		event      *model.AlarmEvent
		notifyType string
	}
	todo := []pending{}
	if !up {
		// A fresh fingerprint per outage: the upsert only re-fires events it has never seen
		fingerprint := fmt.Sprintf("vpn-connection-%d-%d", conn.ID, at.Unix())
		event, notifyType, err := admin.UpsertAlarmEvent(ctx, fingerprint, labels, "firing", at)
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to record VPN alarm event: %v", err)
			return
		}
		todo = append(todo, pending{event, notifyType})
	} else {
		firing := []*model.AlarmEvent{}
		if err := db.Where("alert_name = ? and status = ? and labels like ?", vpnAlertName, "firing", "%"+conn.UUID+"%").Find(&firing).Error; err != nil {
			logger.Ctx(ctx).Errorf("Failed to query VPN alarm events: %v", err)
			return
		}
		for _, existing := range firing {
			event, notifyType, err := admin.UpsertAlarmEvent(ctx, existing.Fingerprint, labels, "resolved", at)
			if err != nil {
				logger.Ctx(ctx).Errorf("Failed to resolve VPN alarm event: %v", err)
				continue
			}
			todo = append(todo, pending{event, notifyType})
		}
	}
	if len(todo) == 0 {
		return
	}
	channels := []*model.NotificationChannel{}
	if err := db.Where("org_id = ? and enabled = ?", gateway.Owner, true).Find(&channels).Error; err != nil {
		logger.Ctx(ctx).Errorf("Failed to query notification channels: %v", err)
		return
	}
	notifier := NewAlarmNotifier()
	for _, item := range todo {
		for _, channel := range channels {
			result := notifier.SendNotification(ctx, channel, item.event, item.notifyType)
			entry := &model.AlarmDeliveryLog{
				EventUUID: item.event.UUID, ChannelUUID: result.ChannelUUID, ChannelName: result.ChannelName, ChannelType: result.ChannelType,
				NotifyType: item.notifyType, Status: result.Status, ErrorMessage: result.Error, SentAt: time.Now(),
			}
			if err := admin.CreateDeliveryLog(ctx, entry); err != nil {
				logger.Ctx(ctx).Errorf("Failed to record VPN notification delivery: %v", err)
			}
		}
	}
}
