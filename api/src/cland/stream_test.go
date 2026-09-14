package cland

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	pb "api/src/proto/cloudlandpb"
	"api/src/utils/grpcauth"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const testToken = "test-token"

// fakeClapi serves the clapi internal endpoints cland calls.
type fakeClapi struct {
	mu    sync.Mutex
	nodes map[int32]validNode
}

func (f *fakeClapi) remove(id int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.nodes, id)
}

func (f *fakeClapi) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/internal/nodes/valid":
		list := make([]validNode, 0, len(f.nodes))
		for _, n := range f.nodes {
			list = append(list, n)
		}
		_ = json.NewEncoder(w).Encode(list)
	case "/internal/node/verify":
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		if _, ok := f.nodes[int32(id)]; !ok {
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

type testEnv struct {
	server *Server
	clapi  *fakeClapi
	conn   *grpc.ClientConn
	dial   func(token string) *grpc.ClientConn
}

// startTestEnv runs cland over an in-memory gRPC listener against a fake clapi that
// lists nodeIDs as active hypervisors.
func startTestEnv(t *testing.T, token string, nodeIDs ...int32) *testEnv {
	t.Helper()
	clapi := &fakeClapi{nodes: make(map[int32]validNode)}
	for _, id := range nodeIDs {
		clapi.nodes[id] = validNode{Hostid: id, Hostname: "node" + strconv.Itoa(int(id)), Status: hyperStatusActive}
	}
	httpSrv := httptest.NewServer(clapi)
	t.Cleanup(httpSrv.Close)

	keyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(keyDir, "cland.key"), []byte("PRIVATE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "cland.key.pub"), []byte("PUBLIC"), 0644); err != nil {
		t.Fatal(err)
	}

	server := NewServer(&Config{ClapiEndpoint: httpSrv.URL, SSHKeyDir: keyDir, AuthToken: token})
	lis := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer(grpcauth.ServerOptions(token)...)
	pb.RegisterClandServiceServer(grpcServer, server)
	pb.RegisterCloudletServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	env := &testEnv{server: server, clapi: clapi}
	env.dial = func(tok string) *grpc.ClientConn {
		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpcauth.DialOption(tok),
		)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	env.conn = env.dial(token)
	return env
}

// openStream starts a command stream and sends the registration for nodeID.
func (e *testEnv) openStream(t *testing.T, nodeID int32) pb.CloudletService_CommandStreamClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := pb.NewCloudletServiceClient(e.conn).CommandStream(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if err := stream.Send(&pb.CloudletMessage{
		Payload: &pb.CloudletMessage_Register{Register: &pb.RegisterRequest{NodeId: nodeID, Hostname: "node"}},
	}); err != nil {
		t.Fatalf("send register: %v", err)
	}
	return stream
}

// register opens a stream, expects a successful RegisterAck and returns the issued session.
func (e *testEnv) register(t *testing.T, nodeID int32) (pb.CloudletService_CommandStreamClient, string) {
	t.Helper()
	stream := e.openStream(t, nodeID)
	msg := recvWithin(t, stream)
	if ack := msg.GetRegisterAck(); ack == nil || !ack.Success {
		t.Fatalf("node %d registration: got %v", nodeID, msg)
	}
	header, err := stream.Header()
	if err != nil {
		t.Fatalf("stream header: %v", err)
	}
	sessions := header.Get(grpcauth.SessionHeader)
	if len(sessions) != 1 {
		t.Fatalf("expected one session header, got %v", sessions)
	}
	return stream, sessions[0]
}

func recvWithin(t *testing.T, stream pb.CloudletService_CommandStreamClient) *pb.ClandMessage {
	t.Helper()
	type result struct {
		msg *pb.ClandMessage
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := stream.Recv()
		ch <- result{msg, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("recv: %v", r.err)
		}
		return r.msg
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a message")
		return nil
	}
}

func recvErrWithin(t *testing.T, stream pb.CloudletService_CommandStreamClient) error {
	t.Helper()
	ch := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				ch <- err
				return
			}
		}
	}()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("stream was not closed")
		return nil
	}
}

func TestCommandStreamRejectsUnknownNode(t *testing.T) {
	env := startTestEnv(t, testToken, 1)
	msg := recvWithin(t, env.openStream(t, 9))
	if ack := msg.GetRegisterAck(); ack == nil || ack.Success {
		t.Fatalf("expected a rejected RegisterAck, got %v", msg)
	}
}

func TestCommandStreamRequiresToken(t *testing.T) {
	env := startTestEnv(t, testToken, 1)
	stream, err := pb.NewCloudletServiceClient(env.dial("")).CommandStream(context.Background())
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestRegisterAckPrecedesCommands(t *testing.T) {
	env := startTestEnv(t, testToken, 1)
	clandClient := pb.NewClandServiceClient(env.conn)
	stream := env.openStream(t, 1)

	// Keep dispatching while the node registers; no command may arrive before the ack.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			reply, err := clandClient.Execute(context.Background(), &pb.ExecuteRequest{Id: 100, Control: "inter=1", Command: "true"})
			if err == nil && reply.Status == "ok" {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	if ack := recvWithin(t, stream).GetRegisterAck(); ack == nil || !ack.Success {
		t.Fatal("first message must be a successful RegisterAck")
	}
	if cmd := recvWithin(t, stream).GetCommand(); cmd == nil || cmd.Command != "true" {
		t.Fatal("second message must be the dispatched command")
	}
	<-done
}

func TestReconnectSupersedesOldStream(t *testing.T) {
	env := startTestEnv(t, testToken, 1)
	oldStream, _ := env.register(t, 1)
	newStream, _ := env.register(t, 1)

	if err := recvErrWithin(t, oldStream); status.Code(err) != codes.Aborted {
		t.Fatalf("old stream should be aborted, got %v", err)
	}
	if n := env.server.Registry.Count(); n != 1 {
		t.Fatalf("registered nodes = %d, want 1", n)
	}

	reply, err := pb.NewClandServiceClient(env.conn).Execute(context.Background(),
		&pb.ExecuteRequest{Id: 100, Control: "inter=1", Command: "true"})
	if err != nil || reply.Status != "ok" {
		t.Fatalf("execute after reconnect: reply=%v err=%v", reply, err)
	}
	if recvWithin(t, newStream).GetCommand() == nil {
		t.Fatal("command should reach the new stream")
	}
}

func TestNodeRemoveShutsDownStream(t *testing.T) {
	env := startTestEnv(t, testToken, 1)
	stream, _ := env.register(t, 1)

	env.clapi.remove(1)
	if _, err := pb.NewClandServiceClient(env.conn).NodeRemove(context.Background(), &pb.NodeRemoveRequest{Id: 1}); err != nil {
		t.Fatalf("NodeRemove: %v", err)
	}
	if shutdown := recvWithin(t, stream).GetShutdown(); shutdown == nil || shutdown.Reason != "node_removed" {
		t.Fatal("removed node should receive ShutdownRequest{node_removed}")
	}
	if ack := recvWithin(t, env.openStream(t, 1)).GetRegisterAck(); ack == nil || ack.Success {
		t.Fatal("removed node must not register again")
	}
}

func TestReportHealthRequiresSession(t *testing.T) {
	env := startTestEnv(t, testToken, 1)
	_, session := env.register(t, 1)
	client := pb.NewCloudletServiceClient(env.conn)
	report := &pb.HealthReport{NodeId: 1, CpuAvailable: 4}

	if _, err := client.ReportHealth(context.Background(), report); status.Code(err) != codes.PermissionDenied {
		t.Errorf("without session: got %v, want PermissionDenied", err)
	}
	wrong := metadata.AppendToOutgoingContext(context.Background(), grpcauth.SessionHeader, "wrong")
	if _, err := client.ReportHealth(wrong, report); status.Code(err) != codes.PermissionDenied {
		t.Errorf("wrong session: got %v, want PermissionDenied", err)
	}
	valid := metadata.AppendToOutgoingContext(context.Background(), grpcauth.SessionHeader, session)
	if _, err := client.ReportHealth(valid, report); err != nil {
		t.Fatalf("valid session: %v", err)
	}
	if total, ok := env.server.Scheduler.Total([]int32{1}); !ok || total.CPU != 4 {
		t.Errorf("scheduler total = %+v (ok=%v), want CPU 4", total, ok)
	}
}

func TestNodeAddKeyDistribution(t *testing.T) {
	env := startTestEnv(t, testToken, 1)
	client := pb.NewClandServiceClient(env.conn)

	reply, err := client.NodeAdd(context.Background(), &pb.NodeAddRequest{Hostname: "node1", Id: 1})
	if err != nil || reply.PublicKey != "PUBLIC" || reply.PrivateKey != "PRIVATE" {
		t.Fatalf("NodeAdd: reply=%v err=%v", reply, err)
	}
	if _, err := client.NodeAdd(context.Background(), &pb.NodeAddRequest{Hostname: "node9", Id: 9}); status.Code(err) != codes.NotFound {
		t.Errorf("unknown node: got %v, want NotFound", err)
	}

	noAuth := startTestEnv(t, "", 1)
	reply, err = pb.NewClandServiceClient(noAuth.conn).NodeAdd(context.Background(), &pb.NodeAddRequest{Hostname: "node1", Id: 1})
	if err != nil || reply.PrivateKey != "" {
		t.Errorf("without auth the private key must not be returned: reply=%v err=%v", reply, err)
	}
}

func TestRefreshValidNodesForgetsDeletedNodes(t *testing.T) {
	env := startTestEnv(t, testToken, 1, 2)
	if err := env.server.refreshValidNodes(); err != nil {
		t.Fatal(err)
	}
	if known := env.server.Registry.Known(); len(known) != 2 {
		t.Fatalf("Known() = %+v, want 2 seeded nodes", known)
	}

	env.clapi.remove(2)
	if err := env.server.refreshValidNodes(); err != nil {
		t.Fatal(err)
	}
	if env.server.validNodes.Has(2) {
		t.Error("deleted node should leave the valid node cache")
	}
	if known := env.server.Registry.Known(); len(known) != 1 || known[0].ID != 1 {
		t.Errorf("Known() = %+v, want only node 1", known)
	}
}

func TestStatusReporterGraceForSeededNodes(t *testing.T) {
	requests := make(chan callbackRequest, 10)
	clapi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req callbackRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		requests <- req
	}))
	defer clapi.Close()
	expect := func(want string) {
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

	registry := NewNodeRegistry()
	registry.Seed(3, "node3")
	reporter := NewStatusReporter(registry, NewScheduler(), NewCallbackForwarder(clapi.URL), "5006", "cland")

	reporter.reportTopology(0, false)
	expect("3,node3,1\n")

	reporter.startedAt = time.Now().Add(-startupGrace)
	reporter.reportTopology(0, false)
	expect("3,node3,10\n")
}
