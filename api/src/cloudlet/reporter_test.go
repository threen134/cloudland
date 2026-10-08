package cloudlet

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "api/src/proto/cloudlandpb"
	"api/src/utils/grpcauth"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeReportClient records the session of every ReportHealth call.
type fakeReportClient struct {
	pb.CloudletServiceClient
	mu       sync.Mutex
	sessions []string
	reject   func(session string) error // optional
}

func (f *fakeReportClient) ReportHealth(ctx context.Context, _ *pb.HealthReport, _ ...grpc.CallOption) (*pb.HealthReportAck, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	session := ""
	if values := md.Get(grpcauth.SessionHeader); len(values) == 1 {
		session = values[0]
	}
	f.mu.Lock()
	f.sessions = append(f.sessions, session)
	reject := f.reject
	f.mu.Unlock()
	if reject != nil {
		if err := reject(session); err != nil {
			return nil, err
		}
	}
	return &pb.HealthReportAck{Accepted: true}, nil
}

func (f *fakeReportClient) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sessions...)
}

func deliverAsync(ctx context.Context, h *HealthReporter) chan error {
	done := make(chan error, 1)
	go func() { done <- h.deliver(ctx, &pb.HealthReport{NodeId: 1}) }()
	return done
}

func waitDelivered(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("deliver did not return")
		return nil
	}
}

// The stream broke while report_rc.sh ran: the report waits for the reconnect and uses the
// new session rather than the one current when the script started.
func TestHealthReportWaitsForReconnect(t *testing.T) {
	client := &fakeReportClient{}
	sender := NewStreamSender()
	oldStream := &fakeClientStream{}
	sender.Attach(oldStream, "s1")
	sender.Detach(oldStream)
	h := NewHealthReporter(client, sender, 1, "node1")

	done := deliverAsync(context.Background(), h)
	select {
	case err := <-done:
		t.Fatalf("deliver returned while disconnected: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	sender.Attach(&fakeClientStream{}, "s2")
	if err := waitDelivered(t, done); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if calls := client.calls(); len(calls) != 1 || calls[0] != "s2" {
		t.Errorf("sessions used = %v, want [s2]", calls)
	}
}

// cland restarted after the session was read: the rejected report is sent again once the
// stream has reconnected with a new session.
func TestHealthReportRetriesOnNewSession(t *testing.T) {
	sender := NewStreamSender()
	oldStream := &fakeClientStream{}
	sender.Attach(oldStream, "s1")
	client := &fakeReportClient{reject: func(session string) error {
		if session != "s1" {
			return nil
		}
		go func() {
			time.Sleep(20 * time.Millisecond)
			sender.Detach(oldStream)
			sender.Attach(&fakeClientStream{}, "s2")
		}()
		return status.Error(codes.PermissionDenied, "session mismatch")
	}}
	h := NewHealthReporter(client, sender, 1, "node1")

	if err := waitDelivered(t, deliverAsync(context.Background(), h)); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if calls := client.calls(); len(calls) != 2 || calls[0] != "s1" || calls[1] != "s2" {
		t.Errorf("sessions used = %v, want [s1 s2]", calls)
	}
}

func TestHealthReportRetriesOnlyOnce(t *testing.T) {
	sender := NewStreamSender()
	sender.Attach(&fakeClientStream{}, "s1")
	client := &fakeReportClient{reject: func(session string) error {
		if session == "s1" {
			go sender.Attach(&fakeClientStream{}, "s2")
		}
		return status.Error(codes.PermissionDenied, "session mismatch")
	}}
	h := NewHealthReporter(client, sender, 1, "node1")

	err := waitDelivered(t, deliverAsync(context.Background(), h))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("deliver error = %v, want PermissionDenied", err)
	}
	if calls := client.calls(); len(calls) != 2 {
		t.Errorf("sessions used = %v, want two attempts", calls)
	}
}

func TestHealthReportStopsOnCancel(t *testing.T) {
	t.Run("disconnected", func(t *testing.T) {
		client := &fakeReportClient{}
		ctx, cancel := context.WithCancel(context.Background())
		done := deliverAsync(ctx, NewHealthReporter(client, NewStreamSender(), 1, "node1"))
		time.Sleep(20 * time.Millisecond)
		cancel()
		if err := waitDelivered(t, done); err != context.Canceled {
			t.Errorf("deliver error = %v, want context.Canceled", err)
		}
		if calls := client.calls(); len(calls) != 0 {
			t.Errorf("sessions used = %v, want none", calls)
		}
	})
	t.Run("rejected", func(t *testing.T) {
		sender := NewStreamSender()
		sender.Attach(&fakeClientStream{}, "s1")
		client := &fakeReportClient{reject: func(string) error {
			return status.Error(codes.PermissionDenied, "session mismatch")
		}}
		ctx, cancel := context.WithCancel(context.Background())
		done := deliverAsync(ctx, NewHealthReporter(client, sender, 1, "node1"))
		time.Sleep(20 * time.Millisecond)
		cancel()
		if err := waitDelivered(t, done); status.Code(err) != codes.PermissionDenied {
			t.Errorf("deliver error = %v, want PermissionDenied", err)
		}
		if calls := client.calls(); len(calls) != 1 {
			t.Errorf("sessions used = %v, want one attempt", calls)
		}
	})
}
