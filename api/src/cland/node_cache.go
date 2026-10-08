/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package cland

import "sync"

// nodeCache holds the hostids clapi knows about. Cloudlets not in it are verified
// against clapi before being admitted.
type nodeCache struct {
	mu  sync.RWMutex
	ids map[int32]bool
}

func newNodeCache() *nodeCache {
	return &nodeCache{ids: make(map[int32]bool)}
}

func (c *nodeCache) Has(id int32) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ids[id]
}

func (c *nodeCache) Add(id int32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids[id] = true
}

func (c *nodeCache) Delete(id int32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.ids, id)
}

// Replace swaps in a fresh list of IDs and returns the IDs that are no longer present.
func (c *nodeCache) Replace(ids []int32) (removed []int32) {
	fresh := make(map[int32]bool, len(ids))
	for _, id := range ids {
		fresh[id] = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.ids {
		if !fresh[id] {
			removed = append(removed, id)
		}
	}
	c.ids = fresh
	return removed
}
