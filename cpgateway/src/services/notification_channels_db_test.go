//go:build cgo

package services

// Channel sync against the test database (SQLite, so cgo only; see TC-01 BUILD-07b)

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"cpgateway/src/dbs"
	"cpgateway/src/model"
)

// fakeRegion records the channel sync payloads a region receives
type fakeRegion struct {
	mu       sync.Mutex
	payloads []map[string]interface{}
}

func (f *fakeRegion) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/notification-channels/sync" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("bad sync payload: %v", err)
		}
		f.mu.Lock()
		f.payloads = append(f.payloads, payload)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
}

// channelsIn returns the channels of the last payload, by UUID
func (f *fakeRegion) channelsIn(t *testing.T) (string, map[string]map[string]interface{}) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.payloads) == 0 {
		t.Fatal("the region received no channel sync")
	}
	last := f.payloads[len(f.payloads)-1]
	byUUID := map[string]map[string]interface{}{}
	if ch, ok := last["channel"].(map[string]interface{}); ok {
		byUUID[ch["uuid"].(string)] = ch
	}
	if list, ok := last["channels"].([]interface{}); ok {
		for _, item := range list {
			ch := item.(map[string]interface{})
			byUUID[ch["uuid"].(string)] = ch
		}
	}
	return last["action"].(string), byUUID
}

// Regions get a channel's owner as the organization UUID, never this gateway's organization ID; a full sync
// leaves out the channels of deleted organizations
func TestChannelSyncSendsOrgUUID(t *testing.T) {
	ctx := context.Background()
	db := dbs.DB()
	suffix := time.Now().UnixNano()
	org := model.Organization{Name: "chan-org", Slug: fmt.Sprintf("chan-org-%d", suffix), OwnerUserID: 1}
	gone := model.Organization{Name: "chan-gone", Slug: fmt.Sprintf("chan-gone-%d", suffix), OwnerUserID: 1}
	if err := db.Create(&org).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&gone).Error; err != nil {
		t.Fatal(err)
	}
	ch := model.NotificationChannel{OrgID: org.ID, Name: "c", Type: "webhook", Config: `{"url":"https://example.com"}`, Enabled: false}
	orphan := model.NotificationChannel{OrgID: gone.ID, Name: "o", Type: "webhook", Config: `{"url":"https://example.com"}`, Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&orphan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&gone).Error; err != nil {
		t.Fatal(err)
	}

	fake := &fakeRegion{}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	region := model.Region{UUID: fmt.Sprintf("r-%d", suffix), Name: fmt.Sprintf("chan-region-%d", suffix), InternalEndpoint: srv.URL,
		InternalSecret: "s", IsAvailable: true}
	if err := db.Create(&region).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Delete(&region)
		db.Delete(&ch)
		db.Delete(&orphan)
	})

	PushAllChannelsToRegion(ctx, db, &region)
	action, channels := fake.channelsIn(t)
	got, ok := channels[ch.UUID]
	if action != "bulk_sync" || !ok {
		t.Fatalf("bulk sync: action %s, channel missing from %v", action, channels)
	}
	if got["org_uuid"] != org.UUID || got["enabled"] != false || got["org_id"] != nil {
		t.Fatalf("bulk sync sent %v, want org_uuid %s, enabled false, no org_id", got, org.UUID)
	}
	if _, ok := channels[orphan.UUID]; ok {
		t.Fatal("bulk sync sent the channel of a deleted organization")
	}

	PushChannelUpsertToAllRegions(ctx, ch.UUID)
	action, channels = fake.channelsIn(t)
	if got := channels[ch.UUID]; action != "upsert" || got == nil || got["org_uuid"] != org.UUID || got["org_id"] != nil {
		t.Fatalf("upsert: action %s, channel %v", action, got)
	}
}
