/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

Command executor — runs shell scripts and streams stdout back to cland.
Ported from: cloudlet.cpp backHandler lines 103-162
*/

package cloudlet

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "api/src/proto/cloudlandpb"
	"api/src/utils/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	// maxLineSize bounds a single output line; the rest of an over-long output is discarded.
	maxLineSize = 1024 * 1024
	// sendWaitTimeout is how long output waits for a reconnect before it is dropped.
	sendWaitTimeout  = 3 * time.Minute
	commandQueueSize = 4096
)

// StreamSender sends messages on the current command stream. The stream is replaced on
// every reconnect and Send waits for the new one, so output of a command that outlives
// its connection still reaches cland.
type StreamSender struct {
	mu      sync.Mutex
	stream  pb.CloudletService_CommandStreamClient
	session string        // session cland issued for stream
	changed chan struct{} // closed and replaced whenever the attached stream changes
	sendMu  sync.Mutex    // serializes stream.Send
}

func NewStreamSender() *StreamSender {
	return &StreamSender{changed: make(chan struct{})}
}

// notifyLocked wakes goroutines waiting for the attached stream to change.
func (s *StreamSender) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// Attach makes stream, with the session cland issued for it, current and wakes senders
// waiting for a connection.
func (s *StreamSender) Attach(stream pb.CloudletService_CommandStreamClient, session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stream = stream
	s.session = session
	s.notifyLocked()
}

// Detach clears stream if it is still current.
func (s *StreamSender) Detach(stream pb.CloudletService_CommandStreamClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream != nil && s.stream == stream {
		s.stream = nil
		s.session = ""
		s.notifyLocked()
	}
}

// Connected reports whether a stream is attached.
func (s *StreamSender) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream != nil
}

// Session returns the session of the attached stream, or "" while disconnected.
func (s *StreamSender) Session() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session
}

// WaitSession returns the session of the attached stream once it is neither empty nor
// stale, waiting up to timeout for a reconnect. It returns "" on timeout or when ctx is done.
func (s *StreamSender) WaitSession(ctx context.Context, stale string, timeout time.Duration) string {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		session, changed := s.session, s.changed
		s.mu.Unlock()
		if session != "" && session != stale {
			return session
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ""
		case <-timer.C:
			return ""
		}
	}
}

// Send delivers msg on the current stream, waiting up to sendWaitTimeout for a reconnect.
func (s *StreamSender) Send(msg *pb.CloudletMessage) error {
	timer := time.NewTimer(sendWaitTimeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		stream, changed := s.stream, s.changed
		s.mu.Unlock()

		if stream != nil {
			s.sendMu.Lock()
			err := stream.Send(msg)
			s.sendMu.Unlock()
			if err == nil {
				return nil
			}
			// The stream is broken; wait for the reconnect loop to attach a new one.
			s.Detach(stream)
			continue
		}

		select {
		case <-changed:
		case <-timer.C:
			return fmt.Errorf("no stream to cland within %v", sendWaitTimeout)
		}
	}
}

// CommandQueue runs jobs in arrival order. With one worker (the default) a node runs one
// command at a time.
type CommandQueue struct {
	jobs chan func()
}

func NewCommandQueue(workers int) *CommandQueue {
	if workers < 1 {
		workers = 1
	}
	q := &CommandQueue{jobs: make(chan func(), commandQueueSize)}
	for i := 0; i < workers; i++ {
		go func() {
			for job := range q.jobs {
				job()
			}
		}()
	}
	return q
}

// Push queues job, blocking while the queue is full.
func (q *CommandQueue) Push(job func()) {
	q.jobs <- job
}

// ShouldExecute checks if a command should be executed by this cloudlet.
// Ported from cloudlet.cpp backHandler guard logic (lines 39-65).
func ShouldExecute(req *pb.CommandRequest) bool {
	ctl := req.Control

	// toall=agent messages are skipped by cloudlet (used for topology reporting only)
	if strings.Contains(ctl, "toall=agent") {
		return false
	}

	// type=file messages carry file content in C++, never a command to run
	if strings.Contains(ctl, "type=file") {
		return false
	}

	// Reject invalid inter= values
	if idx := strings.Index(ctl, "inter="); idx >= 0 {
		val := ctl[idx+len("inter="):]
		if i := strings.IndexAny(val, " \t"); i >= 0 {
			val = val[:i]
		}
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return false
		}
	}

	return true
}

// CommandEnv returns the environment for scripts run on this node. Scripts name this
// hypervisor in their callbacks with NODE_ID, so it is always set to the registered ID.
func CommandEnv(nodeID int32, extra ...string) []string {
	env := append(os.Environ(), "NODE_ID="+strconv.Itoa(int(nodeID)))
	return append(env, extra...)
}

// scanLines calls fn for every line of r and always drains r, so an over-long line
// cannot leave the child process blocked on a full pipe.
func scanLines(r io.Reader, fn func(line string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxLineSize)
	for scanner.Scan() {
		fn(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		log.Printf("executor: reading output: %v; discarding the rest", err)
		_, _ = io.Copy(io.Discard, r)
	}
}

// ExecuteCommand runs a command via sudo -E and streams |:-COMMAND-:| callback
// lines back through the gRPC stream as CallbackLine messages. Other output
// (including stderr, which is merged like the C++ "2>&1") is only logged locally:
// clapi cannot parse it and answered 400 for every such line.
// Ported from cloudlet.cpp backHandler lines 103-162.
func ExecuteCommand(sender *StreamSender, req *pb.CommandRequest, nodeID int32) {
	if !ShouldExecute(req) {
		return
	}

	ctx, span := tracing.Tracer().Start(tracing.Extract(context.Background(), req.TraceContext),
		"execute "+tracing.CommandName(req.Command),
		trace.WithAttributes(
			attribute.Int("cloudland.node_id", int(nodeID)),
			attribute.Int("cloudland.msg_id", int(req.MsgId)),
		))
	defer span.End()
	// 回传消息携带本 span 的上下文，clapi 的回调处理成为脚本执行的子 span
	traceContext := tracing.Inject(ctx)

	send := func(msg *pb.CloudletMessage) {
		if err := sender.Send(msg); err != nil {
			tracing.Logf(ctx, "executor: dropped output of msg_id=%d: %v", req.MsgId, err)
		}
	}

	cmd := exec.Command("sudo", "-E", "bash", "-c", req.Command)
	cmd.Env = CommandEnv(nodeID, "TRACEPARENT="+traceContext["traceparent"])

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		tracing.Logf(ctx, "executor: pipe failed: %v", err)
		span.SetStatus(codes.Error, err.Error())
		return
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout (matching "2>&1" in C++)

	if err := cmd.Start(); err != nil {
		tracing.Logf(ctx, "executor: start failed: %v", err)
		span.SetStatus(codes.Error, err.Error())
		return
	}

	scanLines(stdout, func(line string) {
		if command, isCallback := ParseCallbackLine(line); isCallback {
			send(&pb.CloudletMessage{
				Payload: &pb.CloudletMessage_Callback{
					Callback: &pb.CallbackLine{
						MsgId:        req.MsgId,
						NodeId:       nodeID,
						Command:      command,
						TraceContext: traceContext,
					},
				},
			})
		} else if strings.TrimSpace(line) != "" {
			tracing.Logf(ctx, "executor: msg_id=%d output: %s", req.MsgId, line)
		}
	})

	if err := cmd.Wait(); err != nil {
		span.SetStatus(codes.Error, err.Error())
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode := exitErr.ExitCode()
			span.SetAttributes(attribute.Int("process.exit_code", exitCode))
			send(&pb.CloudletMessage{
				Payload: &pb.CloudletMessage_Error{
					Error: &pb.ErrorReport{
						MsgId:        req.MsgId,
						NodeId:       nodeID,
						Command:      req.Command,
						ExitCode:     int32(exitCode),
						TraceContext: traceContext,
					},
				},
			})
		}
	}
}

// allowedFileDirs lists directories that file transfers are allowed to write to.
var allowedFileDirs = []string{
	"/opt/cloudland/",
	"/tmp/",
}

// validateFilePath checks that the path is within allowed directories.
func validateFilePath(path string) error {
	cleaned := filepath.Clean(path)
	for _, dir := range allowedFileDirs {
		if strings.HasPrefix(cleaned, dir) {
			return nil
		}
	}
	return fmt.Errorf("path %q is outside allowed directories", path)
}

// ReceiveFile handles incoming file transfer from cland.
// Ported from cloudlet.cpp backHandler lines 67-101.
func ReceiveFile(ft *pb.FileTransfer) {
	ctx, span := tracing.StartChild(tracing.Extract(context.Background(), ft.TraceContext), "receive_file",
		trace.WithAttributes(
			attribute.String("cloudland.file.path", ft.Filepath),
			attribute.Int64("cloudland.file.seek", ft.Fileseek),
			attribute.Int("cloudland.file.chunk_bytes", len(ft.Content)),
		))
	tracing.EndSpan(span, receiveFile(ctx, ft))
}

func receiveFile(ctx context.Context, ft *pb.FileTransfer) error {
	if err := validateFilePath(ft.Filepath); err != nil {
		tracing.Logf(ctx, "file: rejected path traversal attempt: %v", err)
		return err
	}
	if ft.Fileseek < 0 || (ft.Filesize > 0 && ft.Fileseek+int64(len(ft.Content)) > ft.Filesize) {
		err := fmt.Errorf("chunk for %s outside declared size (seek=%d len=%d size=%d)",
			ft.Filepath, ft.Fileseek, len(ft.Content), ft.Filesize)
		tracing.Logf(ctx, "file: %v, rejected", err)
		return err
	}

	var f *os.File
	var err error

	if ft.Fileseek == 0 {
		f, err = os.Create(ft.Filepath)
	} else {
		f, err = os.OpenFile(ft.Filepath, os.O_RDWR, 0644)
	}
	if err != nil {
		tracing.Logf(ctx, "file: cannot open %s: %v", ft.Filepath, err)
		return err
	}
	defer f.Close()

	if ft.Fileseek > 0 {
		if _, err := f.Seek(ft.Fileseek, io.SeekStart); err != nil {
			tracing.Logf(ctx, "file: seek failed on %s: %v", ft.Filepath, err)
			return err
		}
	}

	if _, err := f.Write(ft.Content); err != nil {
		tracing.Logf(ctx, "file: write failed on %s: %v", ft.Filepath, err)
		return err
	}
	return nil
}
