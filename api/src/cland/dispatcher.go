/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

Control string parser and message router.
Ported from: src/rpcworker.cpp Execute() + src/filter/scheduler.cpp filter_input()
*/

package cland

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	pb "api/src/proto/cloudlandpb"
	"api/src/utils/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Dispatcher struct {
	registry  *NodeRegistry
	groupMgr  *GroupManager
	scheduler *Scheduler
	callback  *CallbackForwarder
	status    *StatusReporter // answers toall=agent / inter=-1; nil disables topology reports
	running   atomic.Bool
}

func NewDispatcher(registry *NodeRegistry, groupMgr *GroupManager, scheduler *Scheduler, callback *CallbackForwarder) *Dispatcher {
	d := &Dispatcher{
		registry:  registry,
		groupMgr:  groupMgr,
		scheduler: scheduler,
		callback:  callback,
	}
	d.running.Store(true)
	return d
}

func (d *Dispatcher) IsRunning() bool {
	return d.running.Load()
}

var replyOK = &pb.ExecuteReply{Status: "ok"}

// statusNoTargetNode 与 clapi 侧（api/src/common/clients.go 的 statusNoTargetNode）保持一致：
// clapi 收到该状态会让 HyperExecute 返回错误，而不是当作已下发继续往下走
const statusNoTargetNode = "error: no target node"

// Dispatch parses the control string and routes the command to the correct node(s).
// Directive precedence follows rpcworker.cpp Execute() (lines 123-255). As with strstr
// there, a directive matches when its key is present, even if its value is empty.
func (d *Dispatcher) Dispatch(ctx context.Context, req *pb.ExecuteRequest) (*pb.ExecuteReply, error) {
	control := req.Control
	command := req.Command
	msgID := req.Id

	// 命令只记录脚本名，避免把含敏感参数的完整命令写入链路
	ctx, span := tracing.Tracer().Start(ctx, "dispatch", trace.WithAttributes(
		attribute.String("cloudland.control", control),
		attribute.String("cloudland.command", tracing.CommandName(command)),
		attribute.Int("cloudland.msg_id", int(msgID)),
	))
	defer span.End()

	msg := &pb.ClandMessage{
		Payload: &pb.ClandMessage_Command{
			Command: &pb.CommandRequest{
				MsgId:        msgID,
				Extra:        req.Extra,
				Control:      control,
				Command:      command,
				TraceContext: tracing.Inject(ctx),
			},
		},
	}

	if val, found := controlValue(control, "inter="); found {
		return d.handleInter(ctx, val, msgID, msg)
	}
	if val, found := controlValue(control, "group="); found {
		// group= goes through the C++ scheduler filter: one member is picked, not all.
		return d.handleSchedule(ctx, d.resolveTargets(val), control, command, msgID, msg)
	}
	if val, found := controlValue(control, "select="); found {
		return d.handleSchedule(ctx, d.resolveTargets(val), control, command, msgID, msg)
	}
	if val, found := controlValue(control, "toall="); found {
		return d.handleToAll(val, msgID, msg), nil
	}
	if val, found := controlValue(control, "mkgrp="); found {
		if !strings.Contains(val, ":") {
			tracing.Logf(ctx, "dispatcher: invalid group description (missing ':'): %s", val)
			return &pb.ExecuteReply{Status: "error: invalid group description"}, nil
		}
		d.groupMgr.Create(val)
		return replyOK, nil
	}
	if val, found := controlValue(control, "rmgrp="); found {
		d.groupMgr.Delete(val)
		return replyOK, nil
	}
	if strings.Contains(control, "lsgrp=") {
		jsonBytes, _ := json.Marshal(d.groupMgr.List())
		return &pb.ExecuteReply{Status: string(jsonBytes)}, nil
	}
	if strings.Contains(control, "term=") {
		tracing.Logf(ctx, "dispatcher: received term signal")
		d.running.Store(false)
		return replyOK, nil
	}
	if strings.Contains(control, "callback") {
		// Local callback execution (ported from rpcworker.cpp line 233)
		// 保留 trace 上下文，但不继承 gRPC 请求的取消：命令在 Execute 返回后继续执行
		execCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		go func() {
			defer cancel()
			execCtx, span := tracing.StartChild(execCtx, "callback_exec "+tracing.CommandName(command))
			cmd := exec.CommandContext(execCtx, "bash", "-c", command)
			output, err := cmd.CombinedOutput()
			if err != nil {
				tracing.Logf(execCtx, "dispatcher: callback exec failed: %v, output: %s", err, string(output))
			}
			tracing.EndSpan(span, err)
		}()
		return replyOK, nil
	}

	tracing.Logf(ctx, "dispatcher: unrecognized control '%s', dropped", control)
	return &pb.ExecuteReply{Status: "error: unrecognized control"}, nil
}

// handleInter sends the command to node N for inter=N. A negative or empty value went
// through the C++ scheduler filter: inter=-1 (the front end's own ID) triggered a
// topology report, and any other negative target was dropped by every cloudlet.
func (d *Dispatcher) handleInter(ctx context.Context, val string, msgID int32, msg *pb.ClandMessage) (*pb.ExecuteReply, error) {
	nodeID := -1
	if val != "" {
		n, err := strconv.Atoi(val)
		if err != nil {
			return nil, fmt.Errorf("invalid inter= value: %s", val)
		}
		nodeID = n
	}

	if nodeID < 0 {
		if nodeID == -1 && d.status != nil {
			d.status.ReportTopology(msgID)
		}
		// 命令没有目标节点，一定没送出去：必须回错误。静默回 ok 会让 clapi 以为下发成功，
		// 调用方（如 target_hyper 未落库的迁移）就此卡死，任务表里还显示各阶段全部完成
		tracing.Logf(ctx, "dispatcher: inter=%d has no target node, command dropped", nodeID)
		return &pb.ExecuteReply{Status: statusNoTargetNode}, nil
	}

	trace.SpanFromContext(ctx).SetAttributes(attribute.Int("cloudland.target_node", nodeID))
	if err := d.registry.SendTo(int32(nodeID), msg); err != nil {
		tracing.Logf(ctx, "dispatcher: inter=%d send failed: %v", nodeID, err)
		return &pb.ExecuteReply{Status: "error: node not connected"}, nil
	}
	return replyOK, nil
}

// handleSchedule picks one connected candidate that satisfies the cpu=/memory=/disk=/network=
// requirements (scheduler.cpp filter_input). If none can take the command, clapi receives
// an error=resource callback so the request fails instead of hanging.
func (d *Dispatcher) handleSchedule(ctx context.Context, candidates []int32, control, command string, msgID int32, msg *pb.ClandMessage) (*pb.ExecuteReply, error) {
	cpu := parseControlInt(control, "cpu=")
	mem := parseControlInt(control, "memory=")
	disk := parseControlInt(control, "disk=")
	network := parseControlInt(control, "network=")

	bestID, err := d.scheduler.GetBestNode(cpu, mem, disk, network, d.registry.Connected(candidates))
	if err != nil {
		tracing.Logf(ctx, "dispatcher: no node for msg_id=%d (cpu=%d memory=%d disk=%d): %v", msgID, cpu, mem, disk, err)
		d.callback.Enqueue(ctx, msgID, -1, "error=resource", command)
		return &pb.ExecuteReply{Status: "error: no resource"}, nil
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.Int("cloudland.target_node", int(bestID)))
	if err := d.registry.SendTo(bestID, msg); err != nil {
		tracing.Logf(ctx, "dispatcher: send to scheduled node %d failed: %v", bestID, err)
		d.callback.Enqueue(ctx, msgID, -1, "error=resource", command)
		return &pb.ExecuteReply{Status: "error: send failed"}, nil
	}
	return replyOK, nil
}

// handleToAll broadcasts the command to the target group. toall=agent only requested a
// topology report in C++; cloudlets drop the command itself, so it is not sent.
func (d *Dispatcher) handleToAll(target string, msgID int32, msg *pb.ClandMessage) *pb.ExecuteReply {
	if target == "agent" {
		if d.status != nil {
			d.status.ReportTopology(msgID)
		}
		return replyOK
	}
	d.registry.Broadcast(d.resolveTargets(target), msg)
	return replyOK
}

// resolveTargets resolves a group description like NetLayer::createGroup/sendMessage:
// "name:ids" selects its members, a bare name looks up a group created by mkgrp=, and an
// empty member list or an unknown (including empty) name means SCI_GROUP_ALL.
func (d *Dispatcher) resolveTargets(desc string) []int32 {
	var members []int32
	if strings.Contains(desc, ":") {
		_, members = ParseDescriptor(desc)
	} else {
		members, _ = d.groupMgr.GetMembers(desc)
	}
	if len(members) == 0 {
		return d.registry.AllNodeIDs()
	}
	return members
}

// controlValue returns the value after key (up to the next space/tab) and whether key
// occurs in control at all, matching the strstr checks in rpcworker.cpp.
func controlValue(control, key string) (string, bool) {
	idx := strings.Index(control, key)
	if idx == -1 {
		return "", false
	}
	val := control[idx+len(key):]
	if i := strings.IndexAny(val, " \t"); i >= 0 {
		val = val[:i]
	}
	return val, true
}

// extractValue extracts the value after "key=" from a control string.
// Returns empty string if key not found.
func extractValue(control, key string) string {
	val, _ := controlValue(control, key)
	return val
}

// parseControlInt extracts an integer value for "key=N" from the control string.
func parseControlInt(control, key string) int64 {
	val := extractValue(control, key)
	if val == "" {
		return 0
	}
	n, _ := strconv.ParseInt(val, 10, 64)
	return n
}
