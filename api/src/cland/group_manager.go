/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package cland

import (
	"strconv"
	"strings"
	"sync"
)

type GroupManager struct {
	mu     sync.RWMutex
	groups map[string][]int32 // group name → member node IDs
}

func NewGroupManager() *GroupManager {
	return &GroupManager{
		groups: make(map[string][]int32),
	}
}

// ParseDescriptor parses "name:id_list" format where id_list supports
// comma-separated values and ranges. Examples:
//   - "group1:1,2,3"
//   - "group2:1-5,10,20-25"
//
// Ported from netlayer.cpp createGroup()
func ParseDescriptor(desc string) (name string, members []int32) {
	pos := strings.Index(desc, ":")
	if pos == -1 {
		return desc, nil
	}
	name = desc[:pos]
	idList := desc[pos+1:]
	if idList == "" {
		return name, nil
	}

	tokens := strings.Split(idList, ",")
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		rangeParts := strings.SplitN(token, "-", 2)
		if len(rangeParts) == 1 {
			if id, err := strconv.Atoi(rangeParts[0]); err == nil {
				members = append(members, int32(id))
			}
		} else {
			start, err1 := strconv.Atoi(rangeParts[0])
			stop, err2 := strconv.Atoi(rangeParts[1])
			if err1 == nil && err2 == nil && stop-start <= 10000 {
				for j := start; j <= stop; j++ {
					members = append(members, int32(j))
				}
			}
		}
	}
	return name, members
}

func (g *GroupManager) Create(desc string) {
	name, members := ParseDescriptor(desc)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.groups[name] = members
}

func (g *GroupManager) Delete(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.groups, name)
}

func (g *GroupManager) GetMembers(name string) ([]int32, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	members, ok := g.groups[name]
	return members, ok
}

func (g *GroupManager) List() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]string, 0, len(g.groups))
	for name := range g.groups {
		result = append(result, name)
	}
	return result
}
