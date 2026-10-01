package apis

import (
	"strings"
	"testing"
)

// GET /notification-channels?order=: a whitelisted field sorts, anything else keeps the default (newest first)
// and never reaches the SQL
func TestChannelListOrder(t *testing.T) {
	const byDefault = "created_at DESC, id DESC"
	for order, wantPrefix := range map[string]string{
		"":                   byDefault,
		"name":               "name ASC",
		"-created_at":        "created_at DESC",
		"type":               "type ASC",
		"-enabled":           "enabled DESC",
		"config":             byDefault,
		"name; DROP TABLE x": byDefault,
	} {
		if got := channelListOrder(order); !strings.HasPrefix(got, wantPrefix) {
			t.Errorf("order %q: got %q, want it to start with %q", order, got, wantPrefix)
		}
	}
}
