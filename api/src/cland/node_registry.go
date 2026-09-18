/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package cland

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	pb "api/src/proto/cloudlandpb"
)

var errNodeClosed = errors.New("node stream closed")

type ConnectedNode struct {
	ID        int32
	Hostname  string
	SessionID string // issued at registration; ReportHealth must present it
	Stream    pb.CloudletService_CommandStreamServer
	Cancel    context.CancelFunc
	sendMu    sync.Mutex // serialize Stream.Send() calls
	closed    bool
}

// Send serializes writes to the gRPC stream (Send is not goroutine-safe).
// Once the CommandStream handler has returned, the stream is no longer written.
func (n *ConnectedNode) Send(msg *pb.ClandMessage) error {
	n.sendMu.Lock()
	defer n.sendMu.Unlock()
	if n.closed {
		return errNodeClosed
	}
	return n.Stream.Send(msg)
}

// markClosed must be called before the CommandStream handler returns.
func (n *ConnectedNode) markClosed() {
	n.sendMu.Lock()
	n.closed = true
	n.sendMu.Unlock()
}

// KnownNode is a node included in topology reports, whether or not it is connected.
type KnownNode struct {
	ID       int32
	Hostname string
	Online   bool
	Seen     bool // connected at least once since cland started
}

type knownNode struct {
	hostname string
	seen     bool
}

type NodeRegistry struct {
	mu    sync.RWMutex
	nodes map[int32]*ConnectedNode
	known map[int32]*knownNode // registered or seeded nodes, kept after disconnect for topology reports
	// removed holds a tombstone, the removal sequence number, for each node removed from
	// the cloud. Hostids come from a database sequence and are not reused, so a tombstone
	// is only cleared when clapi lists the node again after the removal (see Readmit).
	removed  map[int32]uint64
	removals uint64
}

func NewNodeRegistry() *NodeRegistry {
	return &NodeRegistry{
		nodes:   make(map[int32]*ConnectedNode),
		known:   make(map[int32]*knownNode),
		removed: make(map[int32]uint64),
	}
}

// Register adds a node, cancelling any previous stream registered with the same ID.
// It refuses a removed node: the check and the insert share the lock with Remove, so a
// node verified against clapi just before its removal cannot stay registered.
func (r *NodeRegistry) Register(node *ConnectedNode) bool {
	r.mu.Lock()
	if _, removed := r.removed[node.ID]; removed {
		r.mu.Unlock()
		return false
	}
	old := r.nodes[node.ID]
	r.nodes[node.ID] = node
	r.known[node.ID] = &knownNode{hostname: node.Hostname, seen: true}
	r.mu.Unlock()
	if old != nil && old.Cancel != nil {
		old.Cancel()
	}
	return true
}

// Unregister removes node only while it is still the registered stream for its ID,
// so a superseded stream that exits late cannot remove its replacement.
func (r *NodeRegistry) Unregister(node *ConnectedNode) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.nodes[node.ID]; ok && cur == node {
		delete(r.nodes, node.ID)
		return true
	}
	return false
}

// Remove detaches and forgets a node that was removed from the cloud and leaves a
// tombstone that keeps it from registering again.
// It returns the connected stream, or nil if the node was offline.
func (r *NodeRegistry) Remove(nodeID int32) *ConnectedNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	node := r.nodes[nodeID]
	delete(r.nodes, nodeID)
	delete(r.known, nodeID)
	r.removals++
	r.removed[nodeID] = r.removals
	return node
}

// Removed reports whether the node has a tombstone.
func (r *NodeRegistry) Removed(nodeID int32) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, removed := r.removed[nodeID]
	return removed
}

// RemovalSeq returns the sequence number of the latest removal. Take it before fetching
// the node list from clapi and pass it to Readmit.
func (r *NodeRegistry) RemovalSeq() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.removals
}

// Readmit takes the node IDs clapi listed in a fetch that started at removal sequence
// since. A node removed before the fetch that clapi still lists (its hypers row was kept)
// loses its tombstone. A node removed during the fetch may be listed from stale data, so it
// keeps its tombstone and is left out of the returned IDs.
func (r *NodeRegistry) Readmit(listed []int32, since uint64) []int32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]int32, 0, len(listed))
	for _, id := range listed {
		if seq, removed := r.removed[id]; removed {
			if seq > since {
				continue
			}
			delete(r.removed, id)
		}
		ids = append(ids, id)
	}
	return ids
}

// Seed records a node clapi lists as active, so it is reported offline if it does not
// connect after cland starts. Nodes that are already known or removed are left unchanged.
func (r *NodeRegistry) Seed(nodeID int32, hostname string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, removed := r.removed[nodeID]; removed {
		return
	}
	if _, ok := r.known[nodeID]; !ok {
		r.known[nodeID] = &knownNode{hostname: hostname}
	}
}

// Forget drops a node from topology reports without touching its stream. Used for nodes
// clapi no longer lists: reporting them would recreate their hypers rows.
func (r *NodeRegistry) Forget(nodeID int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.known, nodeID)
}

func (r *NodeRegistry) Get(nodeID int32) (*ConnectedNode, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	node, ok := r.nodes[nodeID]
	return node, ok
}

func (r *NodeRegistry) GetAll() []*ConnectedNode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*ConnectedNode, 0, len(r.nodes))
	for _, node := range r.nodes {
		result = append(result, node)
	}
	return result
}

func (r *NodeRegistry) SendTo(nodeID int32, msg *pb.ClandMessage) error {
	r.mu.RLock()
	node, ok := r.nodes[nodeID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("node %d not connected", nodeID)
	}
	return node.Send(msg)
}

// Broadcast sends msg to each connected node in nodeIDs. Sends happen outside the
// registry lock so a node blocked on flow control cannot stall registration or dispatch.
func (r *NodeRegistry) Broadcast(nodeIDs []int32, msg *pb.ClandMessage) {
	seen := make(map[int32]bool, len(nodeIDs))
	targets := make([]*ConnectedNode, 0, len(nodeIDs))
	r.mu.RLock()
	for _, id := range nodeIDs {
		if node, ok := r.nodes[id]; ok && !seen[id] {
			seen[id] = true
			targets = append(targets, node)
		}
	}
	r.mu.RUnlock()
	for _, node := range targets {
		_ = node.Send(msg)
	}
}

func (r *NodeRegistry) BroadcastAll(msg *pb.ClandMessage) {
	for _, node := range r.GetAll() {
		_ = node.Send(msg)
	}
}

func (r *NodeRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.nodes)
}

// AllNodeIDs returns the IDs of connected nodes in ascending order.
func (r *NodeRegistry) AllNodeIDs() []int32 {
	r.mu.RLock()
	ids := make([]int32, 0, len(r.nodes))
	for id := range r.nodes {
		ids = append(ids, id)
	}
	r.mu.RUnlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Connected filters ids down to connected nodes, preserving order.
func (r *NodeRegistry) Connected(ids []int32) []int32 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]int32, 0, len(ids))
	for _, id := range ids {
		if _, ok := r.nodes[id]; ok {
			result = append(result, id)
		}
	}
	return result
}

// Known returns every registered or seeded node that has not been removed, sorted by ID.
func (r *NodeRegistry) Known() []KnownNode {
	r.mu.RLock()
	result := make([]KnownNode, 0, len(r.known))
	for id, k := range r.known {
		_, online := r.nodes[id]
		result = append(result, KnownNode{ID: id, Hostname: k.hostname, Online: online, Seen: k.seen})
	}
	r.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
