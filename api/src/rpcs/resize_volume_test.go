/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package rpcs

import "testing"

func TestActualSize(t *testing.T) {
	cases := []struct {
		args []string
		want int32
	}{
		{[]string{"resize_volume", "5", "error", "20"}, 20},
		{[]string{"resize_volume", "5", "error"}, 0},        // older script: no size reported
		{[]string{"resize_volume", "5", "error", ""}, 0},    // empty
		{[]string{"resize_volume", "5", "error", "abc"}, 0}, // garbage
		{[]string{"resize_volume", "5", "error", "0"}, 0},   // qemu-img could not read the image
		{[]string{"resize_volume", "5", "error", "-3"}, 0},
	}
	for _, c := range cases {
		if got := actualSize(c.args); got != c.want {
			t.Errorf("actualSize(%q) = %d, want %d", c.args, got, c.want)
		}
	}
}
