package services

import (
	"testing"

	"cpgateway/src/model"
)

// Regions number their organizations themselves, so a channel is pushed with its organization's UUID. The
// gateway's organization ID (org_id) used to be sent and stored as the channel's owner in the region: a
// non-admin organization could not bind its own channels (403 channel_not_owned) and VPN alarms went to the
// channels of whichever organization had that number in the region.
func TestChannelSyncDataCarriesOrgUUID(t *testing.T) {
	ch := &model.NotificationChannel{ID: 9, UUID: "c1", OrgID: 40, Name: "n", Type: "webhook", Config: `{"url":"https://x"}`, Enabled: false}
	data := channelSyncData(ch, "org-uuid-40")
	if data["org_uuid"] != "org-uuid-40" {
		t.Fatalf("org_uuid = %v", data["org_uuid"])
	}
	if _, ok := data["org_id"]; ok {
		t.Fatal("the gateway's organization ID must not be sent")
	}
	if data["uuid"] != "c1" || data["enabled"] != false || data["config"].(map[string]interface{})["url"] != "https://x" {
		t.Fatalf("unexpected payload %v", data)
	}
}

// A full sync names every channel with its organization's UUID and leaves out the channels of deleted
// organizations (the region then drops them)
func TestChannelSyncListSkipsDeletedOrgs(t *testing.T) {
	channels := []model.NotificationChannel{
		{UUID: "c1", OrgID: 1},
		{UUID: "c2", OrgID: 40},
		{UUID: "c3", OrgID: 41},
	}
	items, skipped := channelSyncList(channels, map[int64]string{1: "u1", 40: "u40"})
	if len(items) != 2 || items[0]["uuid"] != "c1" || items[0]["org_uuid"] != "u1" || items[1]["org_uuid"] != "u40" {
		t.Fatalf("items %v", items)
	}
	if len(skipped) != 1 || skipped[0] != "c3" {
		t.Fatalf("skipped %v", skipped)
	}
}
