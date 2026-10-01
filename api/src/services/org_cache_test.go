/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"testing"
	"time"
)

// Cached org mappings expire, so that a change made through another clapi replica (a deleted org, a relinked
// system org) is picked up; zero IDs are never cached.
func TestOrgCacheTTL(t *testing.T) {
	cacheOrgID("ttl-org", 42)
	if id, ok := cachedOrgID("ttl-org"); !ok || id != 42 {
		t.Fatalf("fresh entry: %d %v", id, ok)
	}
	if u, ok := cachedOrgUUID(42); !ok || u != "ttl-org" {
		t.Fatalf("fresh reverse entry: %q %v", u, ok)
	}
	expired := time.Now().Add(-time.Second)
	orgIDByUUID.Store("ttl-org", orgIDEntry{id: 42, expires: expired})
	orgUUIDByID.Store(int64(42), orgUUIDEntry{uuid: "ttl-org", expires: expired})
	if _, ok := cachedOrgID("ttl-org"); ok {
		t.Fatal("expired entry served")
	}
	if _, ok := cachedOrgUUID(42); ok {
		t.Fatal("expired reverse entry served")
	}
	if _, ok := orgIDByUUID.Load("ttl-org"); ok {
		t.Fatal("expired entry not dropped")
	}
	cacheOrgID("zero-org", 0)
	if _, ok := cachedOrgID("zero-org"); ok {
		t.Fatal("zero ID cached")
	}
}
