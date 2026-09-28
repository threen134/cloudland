/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package cland

import "testing"

func TestLivenessSkipsWhileCallbacksCongested(t *testing.T) {
	// No workers: the queue only fills
	f := &CallbackForwarder{queue: make(chan callbackJob, 4)}
	f.queue <- callbackJob{}
	if f.Congested() {
		t.Fatal("a quarter full queue is not congested")
	}
	f.queue <- callbackJob{}
	if !f.Congested() {
		t.Fatal("a half full queue is congested")
	}
	// The registry is nil: check() may only return before looking at the nodes
	m := &LivenessMonitor{callback: f, pinged: map[int32]bool{}, reported: map[int32]string{}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("check() judged the nodes while the callback queue was congested: %v", r)
		}
	}()
	m.check()
}
