/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// The error text of a tunnel status report reaches last_error (it used to be dropped: the node always sent "")
func TestVpnTunnelLastError(t *testing.T) {
	payload := `[{"name":"c22-t1","state":"down","established_at":0,"bytes_in":0,"bytes_out":0,"error":"establishing IKE_SA failed, peer not responding"},` +
		`{"name":"c22-t2","state":"up","established_at":1790000000,"bytes_in":10,"bytes_out":20,"error":"stale text"}]`
	reports := []*vpnConnReport{}
	if err := vpnDecodePayload(base64.StdEncoding.EncodeToString([]byte(payload)), &reports); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("got %d reports", len(reports))
	}
	if got := vpnTunnelLastError(reports[0]); got != "establishing IKE_SA failed, peer not responding" {
		t.Errorf("down tunnel: got %q", got)
	}
	// a tunnel that is up has no error, whatever the report carries
	if got := vpnTunnelLastError(reports[1]); got != "" {
		t.Errorf("up tunnel: got %q", got)
	}
}

// A text longer than the column is cut to it (in characters), otherwise the update fails and the whole
// report with it
func TestVpnTunnelLastErrorTruncated(t *testing.T) {
	long := strings.Repeat("é", vpnTunnelLastErrorMax+40)
	got := vpnTunnelLastError(&vpnConnReport{State: "down", Error: "  " + long + "  "})
	if n := len([]rune(got)); n != vpnTunnelLastErrorMax {
		t.Fatalf("got %d characters, want %d", n, vpnTunnelLastErrorMax)
	}
	if !json.Valid([]byte(`"` + got + `"`)) {
		t.Fatalf("truncation broke a character")
	}
}
