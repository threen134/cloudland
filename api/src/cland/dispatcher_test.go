package cland

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	pb "api/src/proto/cloudlandpb"
)

func newTestDispatcher(t *testing.T, ids ...int32) (*Dispatcher, map[int32]*fakeStream) {
	t.Helper()
	registry := NewNodeRegistry()
	streams := make(map[int32]*fakeStream)
	for _, id := range ids {
		stream := &fakeStream{}
		streams[id] = stream
		registry.Register(&ConnectedNode{ID: id, Stream: stream, Cancel: func() {}})
	}
	d := NewDispatcher(registry, NewGroupManager(), NewScheduler(), NewCallbackForwarder("http://127.0.0.1:0"))
	return d, streams
}

func dispatch(t *testing.T, d *Dispatcher, control string) string {
	t.Helper()
	reply, err := d.Dispatch(context.Background(), &pb.ExecuteRequest{
		Id:      100,
		Control: control,
		Command: "/opt/cloudland/scripts/backend/test.sh",
	})
	if err != nil {
		t.Fatalf("Dispatch(%q) error: %v", control, err)
	}
	return reply.Status
}

func sentCounts(streams map[int32]*fakeStream) map[int32]int {
	counts := make(map[int32]int)
	for id, stream := range streams {
		counts[id] = len(stream.sent)
	}
	return counts
}

func totalSent(streams map[int32]*fakeStream) int {
	total := 0
	for _, stream := range streams {
		total += len(stream.sent)
	}
	return total
}

func TestDispatchEmptyToAllBroadcastsToAllNodes(t *testing.T) {
	d, streams := newTestDispatcher(t, 1, 2, 3)
	if status := dispatch(t, d, "toall="); status != "ok" {
		t.Fatalf("status = %q", status)
	}
	for id, n := range sentCounts(streams) {
		if n != 1 {
			t.Errorf("node %d got %d messages, want 1", id, n)
		}
	}
}

func TestDispatchToAllDescriptorBroadcastsMembers(t *testing.T) {
	d, streams := newTestDispatcher(t, 1, 2, 3)
	dispatch(t, d, "toall=router-5:1,3")
	want := map[int32]int{1: 1, 2: 0, 3: 1}
	for id, n := range sentCounts(streams) {
		if n != want[id] {
			t.Errorf("node %d got %d messages, want %d", id, n, want[id])
		}
	}
}

func TestDispatchToAllEmptyMemberListFallsBackToAll(t *testing.T) {
	d, streams := newTestDispatcher(t, 1, 2)
	dispatch(t, d, "toall=group-vrrp-1:")
	if got := totalSent(streams); got != 2 {
		t.Errorf("sent %d messages, want 2", got)
	}
}

func TestDispatchToAllAgentDoesNotSendCommand(t *testing.T) {
	d, streams := newTestDispatcher(t, 1, 2)
	dispatch(t, d, "toall=agent")
	if got := totalSent(streams); got != 0 {
		t.Errorf("sent %d messages, want 0", got)
	}
}

func TestDispatchSelectSkipsDisconnectedNodes(t *testing.T) {
	d, streams := newTestDispatcher(t, 1, 2)
	for _, id := range []int32{1, 2, 9} {
		d.scheduler.UpdateResource(id, &Resource{CPU: 8, Memory: 8192, Disk: 102400})
	}
	// Node 9 has resources but no stream; node 1 is not a candidate.
	for i := 0; i < 4; i++ {
		if status := dispatch(t, d, "select=group-zone-1:9,2 cpu=1 memory=1 disk=1"); status != "ok" {
			t.Fatalf("status = %q", status)
		}
	}
	if counts := sentCounts(streams); counts[2] != 4 || counts[1] != 0 {
		t.Errorf("sent counts = %v, want all 4 on node 2", counts)
	}
}

func TestDispatchEmptySelectSchedulesAmongAllNodes(t *testing.T) {
	d, streams := newTestDispatcher(t, 1, 2)
	d.scheduler.UpdateResource(2, &Resource{CPU: 8, Memory: 8192, Disk: 102400})
	if status := dispatch(t, d, "select="); status != "ok" {
		t.Fatalf("status = %q", status)
	}
	if counts := sentCounts(streams); counts[2] != 1 || counts[1] != 0 {
		t.Errorf("sent counts = %v, want one message on node 2", counts)
	}
}

func TestDispatchGroupSchedulesOneMember(t *testing.T) {
	d, streams := newTestDispatcher(t, 1, 2, 3)
	for _, id := range []int32{1, 2, 3} {
		d.scheduler.UpdateResource(id, &Resource{CPU: 8, Memory: 8192, Disk: 102400})
	}
	if status := dispatch(t, d, "mkgrp=g1:1,2"); status != "ok" {
		t.Fatalf("mkgrp status = %q", status)
	}
	dispatch(t, d, "group=g1 cpu=1")
	counts := sentCounts(streams)
	if total := totalSent(streams); total != 1 || counts[3] != 0 {
		t.Errorf("sent counts = %v, want exactly one message on node 1 or 2", counts)
	}
}

func TestDispatchSelectWithoutResourcesFails(t *testing.T) {
	d, streams := newTestDispatcher(t, 1)
	if status := dispatch(t, d, "select=group-zone-1:1 cpu=1"); status != "error: no resource" {
		t.Errorf("status = %q, want error: no resource", status)
	}
	if got := totalSent(streams); got != 0 {
		t.Errorf("sent %d messages, want 0", got)
	}
}

func TestDispatchNegativeInterDoesNotSendCommand(t *testing.T) {
	d, streams := newTestDispatcher(t, 1)
	if status := dispatch(t, d, "inter=-1"); status != "error: no target node" {
		t.Fatalf("status = %q, want error: no target node", status)
	}
	if got := totalSent(streams); got != 0 {
		t.Errorf("sent %d messages, want 0", got)
	}
}

func TestDispatchInterTakesPrecedenceOverCallback(t *testing.T) {
	d, streams := newTestDispatcher(t, 1)
	dispatch(t, d, "inter=1 callback")
	if got := len(streams[1].sent); got != 1 {
		t.Errorf("node 1 got %d messages, want 1", got)
	}
}

func TestDispatchUnrecognizedControl(t *testing.T) {
	d, _ := newTestDispatcher(t, 1)
	if status := dispatch(t, d, "bogus=1"); status != "error: unrecognized control" {
		t.Errorf("status = %q", status)
	}
}

func TestRegistrySupersededStreamDoesNotRemoveReplacement(t *testing.T) {
	registry := NewNodeRegistry()
	cancelled := false
	oldNode := &ConnectedNode{ID: 1, Hostname: "n1", Stream: &fakeStream{}, Cancel: func() { cancelled = true }}
	newNode := &ConnectedNode{ID: 1, Hostname: "n1", Stream: &fakeStream{}, Cancel: func() {}}

	registry.Register(oldNode)
	registry.Register(newNode)
	if !cancelled {
		t.Error("registering a replacement should cancel the old stream")
	}
	if registry.Unregister(oldNode) {
		t.Error("superseded stream must not unregister its replacement")
	}
	if got, ok := registry.Get(1); !ok || got != newNode {
		t.Error("replacement stream should stay registered")
	}
}

func TestRegistryKnownTracksOfflineNodes(t *testing.T) {
	registry := NewNodeRegistry()
	node := &ConnectedNode{ID: 7, Hostname: "n7", Stream: &fakeStream{}, Cancel: func() {}}
	registry.Register(node)
	registry.Unregister(node)

	known := registry.Known()
	if len(known) != 1 || known[0].ID != 7 || known[0].Online {
		t.Fatalf("Known() = %+v, want node 7 offline", known)
	}
	registry.Remove(7)
	if known := registry.Known(); len(known) != 0 {
		t.Errorf("Known() after Remove = %+v, want empty", known)
	}
}

func TestRegistryRefusesRemovedNode(t *testing.T) {
	registry := NewNodeRegistry()
	registry.Remove(5)
	if registry.Register(&ConnectedNode{ID: 5, Hostname: "n5", Stream: &fakeStream{}, Cancel: func() {}}) {
		t.Error("a removed node must not register")
	}
	registry.Seed(5, "n5")
	if _, ok := registry.Get(5); ok || len(registry.Known()) != 0 {
		t.Errorf("removed node left in registry: known=%+v", registry.Known())
	}
}

func TestRegistryReadmit(t *testing.T) {
	registry := NewNodeRegistry()
	staleFetch := registry.RemovalSeq()
	registry.Remove(1)

	// A list fetched before the removal may still contain the node.
	if ids := registry.Readmit([]int32{1, 2}, staleFetch); len(ids) != 1 || ids[0] != 2 {
		t.Errorf("Readmit with a stale list = %v, want [2]", ids)
	}
	if !registry.Removed(1) {
		t.Fatal("a stale list must not clear the tombstone")
	}

	// clapi still lists the node after the removal: its row was kept.
	if ids := registry.Readmit([]int32{1, 2}, registry.RemovalSeq()); len(ids) != 2 {
		t.Errorf("Readmit = %v, want [1 2]", ids)
	}
	if !registry.Register(&ConnectedNode{ID: 1, Hostname: "n1", Stream: &fakeStream{}, Cancel: func() {}}) {
		t.Error("a node listed again after its removal should register")
	}
}

// topologyClapi serves /internal/execute and records its requests; status, if not nil,
// gives the HTTP status of the n-th request.
func topologyClapi(t *testing.T, status func(n int) int) (chan callbackRequest, string) {
	t.Helper()
	requests := make(chan callbackRequest, 10)
	var mu sync.Mutex
	count := 0
	clapi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req callbackRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		count++
		n := count
		mu.Unlock()
		requests <- req
		if status != nil {
			w.WriteHeader(status(n))
		}
	}))
	t.Cleanup(clapi.Close)
	return requests, clapi.URL
}

func expectTopology(t *testing.T, requests chan callbackRequest, want string) {
	t.Helper()
	select {
	case req := <-requests:
		if req.Command != want {
			t.Errorf("topology = %q, want %q", req.Command, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("topology %q not reported", want)
	}
}

func expectNoTopology(t *testing.T, requests chan callbackRequest) {
	t.Helper()
	select {
	case req := <-requests:
		t.Errorf("unexpected topology report: %+v", req)
	case <-time.After(200 * time.Millisecond):
	}
}

// waitTopologyIdle waits until no topology report is in flight.
func waitTopologyIdle(t *testing.T, r *StatusReporter) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		busy := r.topologyBusy
		r.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("topology report still in flight")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStatusReporterTopology(t *testing.T) {
	requests, clapiURL := topologyClapi(t, nil)

	registry := NewNodeRegistry()
	online := &ConnectedNode{ID: 1, Hostname: "n1", Stream: &fakeStream{}, Cancel: func() {}}
	offline := &ConnectedNode{ID: 2, Hostname: "n2", Stream: &fakeStream{}, Cancel: func() {}}
	registry.Register(online)
	registry.Register(offline)
	registry.Unregister(offline)

	reporter := NewStatusReporter(registry, NewScheduler(), NewCallbackForwarder(clapiURL), "5006", "cland")
	reporter.reportTopology(0, false)

	select {
	case req := <-requests:
		if req.Control != "callback=agent id=-1 port=5006 num=2 hostname=cland" {
			t.Errorf("control = %q", req.Control)
		}
		if req.Command != "1,n1,1\n2,n2,10\n" {
			t.Errorf("command = %q", req.Command)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("topology report not delivered")
	}

	// Like C++ during SCI recovery, the topology is repeated while a node is offline.
	waitTopologyIdle(t, reporter)
	reporter.reportTopology(0, false)
	expectTopology(t, requests, "1,n1,1\n2,n2,10\n")

	// Once every node is online, an unchanged topology is reported only once.
	registry.Remove(2)
	waitTopologyIdle(t, reporter)
	reporter.reportTopology(0, false)
	expectTopology(t, requests, "1,n1,1\n")
	waitTopologyIdle(t, reporter)
	reporter.reportTopology(0, false)
	expectNoTopology(t, requests)
}

// A changed topology that clapi did not accept is reported again on the next round.
func TestStatusReporterResendsUndeliveredTopology(t *testing.T) {
	requests, clapiURL := topologyClapi(t, func(n int) int {
		if n == 1 {
			return http.StatusBadRequest // not retried by the forwarder
		}
		return http.StatusOK
	})
	registry := NewNodeRegistry()
	registry.Register(&ConnectedNode{ID: 1, Hostname: "n1", Stream: &fakeStream{}, Cancel: func() {}})
	reporter := NewStatusReporter(registry, NewScheduler(), NewCallbackForwarder(clapiURL), "5006", "cland")

	reporter.reportTopology(0, false)
	expectTopology(t, requests, "1,n1,1\n")
	waitTopologyIdle(t, reporter)

	reporter.reportTopology(0, false)
	expectTopology(t, requests, "1,n1,1\n")
	waitTopologyIdle(t, reporter)

	reporter.reportTopology(0, false)
	expectNoTopology(t, requests)
}

func TestStatusReporterQueueFull(t *testing.T) {
	registry := NewNodeRegistry()
	registry.Register(&ConnectedNode{ID: 1, Hostname: "n1", Stream: &fakeStream{}, Cancel: func() {}})
	full := &CallbackForwarder{queue: make(chan callbackJob)} // no workers: nothing can be queued
	reporter := NewStatusReporter(registry, NewScheduler(), full, "5006", "cland")

	done := make(chan struct{})
	go func() {
		reporter.reportTopology(0, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reportTopology blocked on a full callback queue")
	}
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if reporter.topologyBusy || reporter.lastTopology != "" {
		t.Errorf("busy=%v lastTopology=%q; the next round should report again", reporter.topologyBusy, reporter.lastTopology)
	}
}

// A topology that changes while a report is in flight goes out after it, never before.
func TestStatusReporterChangeWhileInFlight(t *testing.T) {
	release := make(chan struct{})
	requests, clapiURL := topologyClapi(t, func(n int) int {
		if n == 1 {
			<-release
		}
		return http.StatusOK
	})
	registry := NewNodeRegistry()
	registry.Register(&ConnectedNode{ID: 1, Hostname: "n1", Stream: &fakeStream{}, Cancel: func() {}})
	reporter := NewStatusReporter(registry, NewScheduler(), NewCallbackForwarder(clapiURL), "5006", "cland")

	reporter.reportTopology(0, false)
	expectTopology(t, requests, "1,n1,1\n")

	registry.Register(&ConnectedNode{ID: 2, Hostname: "n2", Stream: &fakeStream{}, Cancel: func() {}})
	reporter.reportTopology(0, false)
	expectNoTopology(t, requests)

	close(release)
	waitTopologyIdle(t, reporter)
	reporter.reportTopology(0, false)
	expectTopology(t, requests, "1,n1,1\n2,n2,1\n")
}
