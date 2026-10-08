/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package cland

import (
	"context"
	"fmt"
	"log"
	"time"
)

// Node liveness for fast VPN gateway takeover (docs/architecture/plan/vpn-gateway-plan.md §8.2, F5).
//
// The command stream only breaks when a node closes it; a node that lost power or its network sends
// nothing, and the gRPC keepalive needs 30-40 s to notice. cloudlet-go therefore sends AliveCallback on
// its stream every second. A node that has done so and then stays silent for livenessTimeout, or whose
// stream closed, is reported to clapi as "gone"; "ok" follows when it is back. clapi uses this only as
// evidence for a VPN master takeover: the node's availability (topology status) is not touched, so a short
// network hiccup does not take a compute node out of scheduling.

// AliveCallback is the command of the per-second liveness message of cloudlet-go (cloudlet.AliveCallback).
// It is consumed here and never forwarded to clapi.
const AliveCallback = "__cloudlet_alive__"

const (
	livenessTimeout  = 3 * time.Second
	livenessInterval = 500 * time.Millisecond
)

// touch records that a message arrived from the node
func (n *ConnectedNode) touch() {
	n.lastSeen.Store(time.Now().UnixNano())
}

// LivenessMonitor turns missing liveness messages into node_liveness callbacks to clapi
type LivenessMonitor struct {
	registry *NodeRegistry
	callback *CallbackForwarder
	pinged   map[int32]bool   // nodes that sent AliveCallback at least once
	reported map[int32]string // last state sent to clapi
}

func NewLivenessMonitor(registry *NodeRegistry, callback *CallbackForwarder) *LivenessMonitor {
	return &LivenessMonitor{registry: registry, callback: callback, pinged: map[int32]bool{}, reported: map[int32]string{}}
}

func (m *LivenessMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(livenessInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.check()
		}
	}
}

func (m *LivenessMonitor) check() {
	// A stream reader hands every message to the callback queue and blocks when it is full (a slow or
	// restarting clapi): its node goes quiet here although it keeps sending. Judging then would report
	// every node gone at once; the round is skipped instead, a real outage is noticed once the queue drains
	if m.callback.Congested() {
		return
	}
	now := time.Now().UnixNano()
	connected := map[int32]bool{}
	for _, node := range m.registry.GetAll() {
		if node.alive.Load() {
			m.pinged[node.ID] = true
		}
		if !m.pinged[node.ID] {
			continue
		}
		fresh := time.Duration(now-node.lastSeen.Load()) < livenessTimeout
		connected[node.ID] = fresh
	}
	for id := range m.pinged {
		state := "gone"
		if connected[id] {
			state = "ok"
		}
		if m.reported[id] == state || (m.reported[id] == "" && state == "ok") {
			m.reported[id] = state
			continue
		}
		m.reported[id] = state
		log.Printf("Node %d liveness: %s", id, state)
		command := fmt.Sprintf("node_liveness.sh '%d' '%s'", id, state)
		if !m.callback.TryEnqueue(context.Background(), 0, id, "callback", command, nil) {
			// Queue full: send it again on the next round
			m.reported[id] = "retry"
		}
	}
}
