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
	Bfd              string   `json:"bfd,omitempty"` // BFD session state (up, down, init) when BFD is on
}

const (
	vpnAlertName       = "VpnConnectionDown"
	vpnTunnelAlertName = "VpnTunnelDown"
)

// NotifyVpnConnectionState raises an alarm event when a connection goes from up (or degraded) to down, i.e.
// no tunnel carries its traffic any more, and resolves it when a tunnel comes back; both are pushed to
// every enabled notification channel of the organization.
// Tunnel state lives in the database, not in Prometheus, so this bypasses the rule/binding machinery of
// the metric alarms; the events still show up in the alarm event list and carry delivery logs.
// Runs in the background: channel calls can take seconds and the caller is a heartbeat callback.
func NotifyVpnConnectionState(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection, up bool, at time.Time) {
	labels := map[string]string{
		"alertname":      vpnAlertName,
		"owner":          strconv.FormatInt(gateway.Owner, 10),
		"severity":       "critical",
		"summary":        fmt.Sprintf("VPN connection %s of gateway %s is down", conn.Name, gateway.Name),
		"vm_name":        fmt.Sprintf("%s / %s", gateway.Name, conn.Name),
		"vm_uuid":        gateway.UUID,
		"vpn_gateway":    gateway.UUID,
		"vpn_connection": conn.UUID,
		"source":         "vpn",
	}
	vpnNotifyAsync(ctx, gateway, vpnAlertName, fmt.Sprintf("vpn-connection-%d-%d", conn.ID, at.Unix()), conn.UUID, labels, up, at)
}

// NotifyVpnTunnelState raises a warning when one tunnel of a connection with several goes down while
// another still carries the traffic (the connection is degraded, the redundancy is gone), and resolves it
// when that tunnel is back
func NotifyVpnTunnelState(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection, tunnel *model.VpnTunnel, up bool, at time.Time) {
	labels := map[string]string{
		"alertname":      vpnTunnelAlertName,
		"owner":          strconv.FormatInt(gateway.Owner, 10),
		"severity":       "warning",
		"summary":        fmt.Sprintf("Tunnel %d of VPN connection %s (gateway %s) is down, the connection runs on its other tunnels", tunnel.Slot, conn.Name, gateway.Name),
		"vm_name":        fmt.Sprintf("%s / %s / t%d", gateway.Name, conn.Name, tunnel.Slot),
		"vm_uuid":        gateway.UUID,
		"vpn_gateway":    gateway.UUID,
		"vpn_connection": conn.UUID,
		"vpn_tunnel":     tunnel.UUID,
		"source":         "vpn",
	}
	vpnNotifyAsync(ctx, gateway, vpnTunnelAlertName, fmt.Sprintf("vpn-tunnel-%d-%d", tunnel.ID, at.Unix()), tunnel.UUID, labels, up, at)
}

// resolveVpnAlarmsOf resolves the alarms of tunnels that are going away, and of their connection when it
// goes too: a firing event is only resolved by an up report, which never comes for a tunnel or a connection
// that no longer exists. Resolving what is not firing is a no-op. Called once the change is done.
func resolveVpnAlarmsOf(ctx context.Context, gateway *model.VpnGateway, conn *model.VpnConnection, tunnels []*model.VpnTunnel, connectionGone bool) {
	now := time.Now()
	for _, t := range tunnels {
		NotifyVpnTunnelState(ctx, gateway, conn, t, true, now)
	}
	if connectionGone {
		NotifyVpnConnectionState(ctx, gateway, conn, true, now)
	}
}

func vpnNotifyAsync(ctx context.Context, gateway *model.VpnGateway, alertName, fingerprint, key string, labels map[string]string, up bool, at time.Time) {
	bg := SetContextDB(context.WithoutCancel(ctx), DB())
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Ctx(bg).Errorf("VPN notification panic: %v", r)
			}
		}()
		notifyVpnState(bg, gateway, alertName, fingerprint, key, labels, up, at)
	}()
}

// notifyVpnState records a firing event (a fresh fingerprint per outage: the upsert only re-fires events it
// has never seen) or resolves every firing event of that alert whose labels carry key, then notifies
func notifyVpnState(ctx context.Context, gateway *model.VpnGateway, alertName, fingerprint, key string, labels map[string]string, up bool, at time.Time) {
	ctx, db := GetContextDB(ctx)
	admin := &NotificationAdmin{}
	type pending struct {
		event      *model.AlarmEvent
		notifyType string
	}
	todo := []pending{}
	if !up {
		event, notifyType, err := admin.UpsertAlarmEvent(ctx, fingerprint, labels, "firing", at)
		if err != nil {
			logger.Ctx(ctx).Errorf("Failed to record VPN alarm event: %v", err)
			return
		}
		todo = append(todo, pending{event, notifyType})
	} else {
		firing := []*model.AlarmEvent{}
		if err := db.Where("alert_name = ? and status = ? and labels like ?", alertName, "firing", "%"+key+"%").Find(&firing).Error; err != nil {
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
