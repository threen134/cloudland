/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import "testing"

func TestRewriteTgwReason(t *testing.T) {
	label := func(id string) string {
		return map[string]string{"4": "work-02", "3": "work-03"}[id]
	}
	cases := map[string]string{
		"node 4: veth tr-12; node 3: router not available": "node work-02: veth tr-12; node work-03: router not available",
		"waiting for nodes 3,4":                            "waiting for nodes work-03, work-02",
		"":                                                 "",
		"something else 4":                                 "something else 4",
	}
	for in, want := range cases {
		if got := rewriteTgwReason(in, label); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
