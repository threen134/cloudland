/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

Replaces C++ FrontBack::Execute / ExecuteAsync HTTP POST to clapi.
*/

package cland

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"api/src/utils/tracing"
)

const (
	callbackWorkers   = 100 // same as the C++ FrontBack ThreadPool<100>
	callbackQueueSize = 10000
	// callbackAttempts covers a clapi restart. A request that timed out may already have
	// been handled, so callback handlers must tolerate an occasional duplicate.
	callbackAttempts   = 3
	callbackRetryDelay = time.Second
)

var errCallbackQueueFull = errors.New("callback queue full")

type callbackRequest struct {
	Id      int32  `json:"Id"`
	Extra   int32  `json:"Extra"`
	Control string `json:"Control"`
	Command string `json:"Command"`
}

type callbackJob struct {
	ctx     context.Context
	msgID   int32
	nodeID  int32
	control string
	command string
	done    func(error) // optional; called with the delivery result
}

type CallbackForwarder struct {
	clapiEndpoint string
	httpClient    *http.Client
	queue         chan callbackJob
}

func NewCallbackForwarder(clapiEndpoint string) *CallbackForwarder {
	f := &CallbackForwarder{
		clapiEndpoint: clapiEndpoint,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		queue: make(chan callbackJob, callbackQueueSize),
	}
	for i := 0; i < callbackWorkers; i++ {
		go f.worker()
	}
	return f
}

func (f *CallbackForwarder) worker() {
	for job := range f.queue {
		err := f.Forward(job.ctx, job.msgID, job.nodeID, job.control, job.command)
		if job.done != nil {
			job.done(err)
		}
	}
}

// Enqueue forwards a callback asynchronously, like C++ FrontBack::ExecuteAsync.
// Callbacks raised while serving an Execute RPC must not be posted synchronously:
// clapi may still hold the DB transaction the callback handler needs.
// Enqueue blocks when the queue is full, applying backpressure to the caller.
func (f *CallbackForwarder) Enqueue(ctx context.Context, msgID, nodeID int32, control, command string) {
	// Keep only the span context: the caller's ctx is usually cancelled once its RPC returns.
	ctx = trace.ContextWithSpanContext(context.Background(), trace.SpanContextFromContext(ctx))
	f.queue <- callbackJob{ctx: ctx, msgID: msgID, nodeID: nodeID, control: control, command: command}
}

// TryEnqueue queues a callback without blocking, for periodic reports that must not stall
// their ticker. done, if not nil, is called from a worker with the delivery result.
// It returns false, without calling done, when the queue is full.
func (f *CallbackForwarder) TryEnqueue(ctx context.Context, msgID, nodeID int32, control, command string, done func(error)) bool {
	ctx = trace.ContextWithSpanContext(context.Background(), trace.SpanContextFromContext(ctx))
	select {
	case f.queue <- callbackJob{ctx: ctx, msgID: msgID, nodeID: nodeID, control: control, command: command, done: done}:
		return true
	default:
		return false
	}
}

// Forward sends a callback to clapi via HTTP POST /internal/execute, retrying while clapi
// is unreachable or answers 5xx. This replaces C++ FrontBack::Execute.
// JSON field names use uppercase first letter — Go json.Unmarshal in
// frontback.go is case-insensitive so both cases are accepted.
// The trace context in ctx is propagated via the W3C traceparent header
// (fixing the C++ bug where trace_id was not forwarded).
func (f *CallbackForwarder) Forward(ctx context.Context, msgID, nodeID int32, control, command string) error {
	body, err := json.Marshal(callbackRequest{
		Id:      msgID,
		Extra:   nodeID,
		Control: control,
		Command: command,
	})
	if err != nil {
		tracing.Logf(ctx, "callback: failed to marshal: %v", err)
		return err
	}

	delay := callbackRetryDelay
	for attempt := 1; ; attempt++ {
		retry, err := f.post(ctx, body)
		if err == nil {
			return nil
		}
		if !retry || attempt == callbackAttempts {
			tracing.Logf(ctx, "callback: msg_id=%d node=%d control=%s failed after %d attempt(s): %v",
				msgID, nodeID, control, attempt, err)
			return err
		}
		time.Sleep(delay)
		delay *= 2
	}
}

// post sends one callback. retry reports whether the failure may be transient (clapi
// unreachable or answering 5xx); any other status means clapi rejected the callback.
func (f *CallbackForwarder) post(ctx context.Context, body []byte) (retry bool, err error) {
	req, err := http.NewRequest("POST", f.clapiEndpoint+"/internal/execute", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return true, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	switch {
	case resp.StatusCode >= http.StatusInternalServerError:
		return true, fmt.Errorf("clapi returned %d", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return false, fmt.Errorf("clapi returned %d", resp.StatusCode)
	}
	return false, nil
}
