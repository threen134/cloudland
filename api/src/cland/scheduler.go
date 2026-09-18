/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0

Ported from: src/filter/rcmanager.cpp
*/

package cland

import (
	"fmt"
	"sync"
)

type Resource struct {
	CPU, CPUTotal         int64
	Memory, MemoryTotal   int64
	Disk, DiskTotal       int64
	Network, NetworkTotal int64
	Load, LoadTotal       int64
}

type Scheduler struct {
	mu        sync.RWMutex
	resources map[int32]*Resource // nodeID → resource
	counter   int                 // round-robin counter
}

func NewScheduler() *Scheduler {
	return &Scheduler{
		resources: make(map[int32]*Resource),
	}
}

// UpdateResource updates the resource info for a node.
// Ported from rcmanager.cpp setAvailibility()
func (s *Scheduler) UpdateResource(nodeID int32, r *Resource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[nodeID] = r
}

// RemoveResource removes a node from the scheduler.
func (s *Scheduler) RemoveResource(nodeID int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.resources, nodeID)
}

// Total sums the resources reported by nodeIDs; ok is false when none of them has reported.
// Ported from rcmanager.cpp totalResource(), restricted to the given (connected) nodes.
func (s *Scheduler) Total(nodeIDs []int32) (total Resource, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range nodeIDs {
		r, found := s.resources[id]
		if !found {
			continue
		}
		ok = true
		total.CPU += r.CPU
		total.CPUTotal += r.CPUTotal
		total.Memory += r.Memory
		total.MemoryTotal += r.MemoryTotal
		total.Disk += r.Disk
		total.DiskTotal += r.DiskTotal
		total.Network += r.Network
		total.NetworkTotal += r.NetworkTotal
		total.Load += r.Load
		total.LoadTotal += r.LoadTotal
	}
	return
}

// testResource calculates a fitness score for a node given resource requirements.
// Returns > 0 if the node can satisfy the requirements, 0 or negative otherwise.
// Division uses (available+1) as denominator so available=0 yields score near 0 but never div-by-zero.
// Negative available (overcommitted) is rejected upfront.
// Ported from rcmanager.cpp testResource()
func testResource(resc *Resource, cpu, memory, disk, network int64) float64 {
	if resc.CPU < 0 || resc.Memory < 0 || resc.Disk < 0 {
		return 0.0
	}
	current := 1 - float64(cpu)/float64(resc.CPU+1)
	if current <= 0.0 {
		return current
	}
	current *= 1 - float64(memory)/float64(resc.Memory+1)
	if current <= 0.0 {
		return current
	}
	current *= 1 - float64(disk)/float64(resc.Disk+1)
	return current
}

// GetBestNode selects the best node from candidates based on resource availability.
// Algorithm: round-robin start, first node passing testResource() wins.
// Returns error if no suitable node found (triggers error=resource callback).
// Ported from rcmanager.cpp getBestBranch()
func (s *Scheduler) GetBestNode(cpu, memory, disk, network int64, candidates []int32) (int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	num := len(candidates)
	if num == 0 {
		return -1, fmt.Errorf("no candidates provided")
	}

	startIdx := s.counter % num
	s.counter++

	for i := 0; i < num; i++ {
		idx := (startIdx + i) % num
		nodeID := candidates[idx]
		resc, ok := s.resources[nodeID]
		if !ok {
			continue // no resource info yet
		}
		score := testResource(resc, cpu, memory, disk, network)
		if score > 0.0 {
			return nodeID, nil
		}
	}
	return -1, fmt.Errorf("no node has sufficient resources")
}
