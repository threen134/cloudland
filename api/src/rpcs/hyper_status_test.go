/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import "testing"

// Alignment 04 review: a host an admin enabled or disabled while it was offline comes back in that status, and its
// disabled marker is rewritten when it disagrees with what the host reports
func TestOfflineReturnStatus(t *testing.T) {
	p := func(v int32) *int32 { return &v }
	for _, tc := range []struct {
		prior    *int32
		reported int
		want     int32
		sync     bool
	}{
		{p(0), 1, 0, true},  // disabled while away, the host still runs without the marker
		{p(0), 0, 0, false}, // disabled before going away
		{p(1), 0, 1, true},  // enabled while away, the host still has the marker
		{p(1), 1, 1, false},
		{p(2), 1, 2, false}, // maintaining: not disabled on the host
		{p(2), 0, 2, true},
	} {
		status, sync := offlineReturnStatus(tc.prior, tc.reported)
		if status == nil || *status != tc.want || sync != tc.sync {
			t.Errorf("prior %d reported %d: got %v %v, want %d %v", *tc.prior, tc.reported, status, sync, tc.want, tc.sync)
		}
	}
	for _, prior := range []*int32{nil, p(4), p(5)} {
		if status, sync := offlineReturnStatus(prior, 1); status != nil || sync {
			t.Errorf("prior %v: got %v %v, want the reported status", prior, status, sync)
		}
	}
}
