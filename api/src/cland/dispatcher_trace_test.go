package cland

import (
	"context"
	"testing"

	pb "api/src/proto/cloudlandpb"
	"api/src/utils/tracing"

	"go.opentelemetry.io/otel/trace"
)

// fakeStream 记录 cland 下发给 cloudlet 的消息
type fakeStream struct {
	pb.CloudletService_CommandStreamServer
	sent []*pb.ClandMessage
}

func (f *fakeStream) Send(msg *pb.ClandMessage) error {
	f.sent = append(f.sent, msg)
	return nil
}

func TestDispatchPropagatesTraceContext(t *testing.T) {
	flush := tracing.Init(context.Background(), "test", "dev")
	defer flush()

	registry := NewNodeRegistry()
	stream := &fakeStream{}
	registry.Register(&ConnectedNode{ID: 1, Hostname: "node1", Stream: stream, Cancel: func() {}})
	d := NewDispatcher(registry, NewGroupManager(), NewScheduler(), NewCallbackForwarder("http://127.0.0.1:0"))

	ctx, span := tracing.Tracer().Start(context.Background(), "clapi")
	defer span.End()
	reply, err := d.Dispatch(ctx, &pb.ExecuteRequest{
		Id:      100,
		Control: "inter=1",
		Command: "/opt/cloudland/scripts/backend/launch_vm.sh '1' 'secret'",
	})
	if err != nil || reply.Status != "ok" {
		t.Fatalf("dispatch failed: reply=%v err=%v", reply, err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("expected 1 message sent to cloudlet, got %d", len(stream.sent))
	}

	carrier := stream.sent[0].GetCommand().GetTraceContext()
	got := trace.SpanContextFromContext(tracing.Extract(context.Background(), carrier))
	if got.TraceID() != span.SpanContext().TraceID() {
		t.Errorf("command trace id = %s, want %s", got.TraceID(), span.SpanContext().TraceID())
	}
	if got.SpanID() == span.SpanContext().SpanID() {
		t.Error("command should carry the dispatch child span, not the caller span")
	}
}
