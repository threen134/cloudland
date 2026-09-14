package cloudlet

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	pb "api/src/proto/cloudlandpb"
)

type fakeClientStream struct {
	pb.CloudletService_CommandStreamClient
	mu   sync.Mutex
	err  error
	sent []*pb.CloudletMessage
}

func (f *fakeClientStream) Send(msg *pb.CloudletMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeClientStream) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func TestStreamSenderWaitsForReconnect(t *testing.T) {
	sender := NewStreamSender()
	sender.Attach(&fakeClientStream{err: io.EOF}, "s1")

	done := make(chan error, 1)
	go func() { done <- sender.Send(&pb.CloudletMessage{}) }()

	select {
	case err := <-done:
		t.Fatalf("Send returned before a working stream was attached: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if sender.Connected() {
		t.Error("broken stream should have been detached")
	}

	healthy := &fakeClientStream{}
	sender.Attach(healthy, "s2")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Send error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Send did not resume after reconnect")
	}
	if healthy.count() != 1 {
		t.Errorf("new stream got %d messages, want 1", healthy.count())
	}
}

func TestStreamSenderDetachIgnoresStaleStream(t *testing.T) {
	sender := NewStreamSender()
	oldStream := &fakeClientStream{}
	newStream := &fakeClientStream{}
	sender.Attach(oldStream, "old")
	sender.Attach(newStream, "new")
	sender.Detach(oldStream)
	if sender.Session() != "new" {
		t.Errorf("session = %q; detaching a stale stream must keep the current one", sender.Session())
	}
}

func TestCommandQueueRunsInOrder(t *testing.T) {
	queue := NewCommandQueue(1)
	var (
		mu    sync.Mutex
		order []int
		wg    sync.WaitGroup
	)
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		queue.Push(func() {
			defer wg.Done()
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
		})
	}
	wg.Wait()
	for i, v := range order {
		if v != i {
			t.Fatalf("job %d ran at position %d", v, i)
		}
	}
}

func TestShouldExecute(t *testing.T) {
	cases := map[string]bool{
		"inter=3":                       true,
		"select=group-zone-1:1,2 cpu=1": true,
		"toall=":                        true,
		"toall=agent":                   false,
		"inter=-1":                      false,
		"inter=abc":                     false,
		"inter=3 type=file":             false,
	}
	for control, want := range cases {
		if got := ShouldExecute(&pb.CommandRequest{Control: control}); got != want {
			t.Errorf("ShouldExecute(%q) = %v, want %v", control, got, want)
		}
	}
}

func TestScanLinesDrainsOverlongLine(t *testing.T) {
	input := "first\n" + strings.Repeat("x", maxLineSize+10) + "\nlast\n"
	r := strings.NewReader(input)
	var lines []string
	scanLines(r, func(line string) { lines = append(lines, line) })
	if len(lines) != 1 || lines[0] != "first" {
		t.Errorf("lines = %d entries, want only %q", len(lines), "first")
	}
	if r.Len() != 0 {
		t.Errorf("%d bytes left unread", r.Len())
	}
}

func TestCommandEnvSetsSCIClientID(t *testing.T) {
	env := CommandEnv(12, "TRACEPARENT=x")
	var found bool
	for _, kv := range env {
		if kv == "SCI_CLIENT_ID=12" {
			found = true
		}
	}
	if !found || env[len(env)-1] != "TRACEPARENT=x" {
		t.Errorf("env missing SCI_CLIENT_ID or extra vars: %v", env[len(env)-2:])
	}
}
