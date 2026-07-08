/*

Copyright (c) 2026, Ctrl IQ, Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

*/

package main

import (
	"testing"
	"time"
)

func TestLRUCache_PutAndGet(t *testing.T) {
	cache := newLRUCache(10)
	resp := GalaxyResponse{Code: 200, Body: RawBytes("test")}
	cache.Put("key1", resp)

	got, ok := cache.Get("key1", time.Hour)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Code != 200 {
		t.Errorf("Code = %d, want 200", got.Code)
	}
	if string(got.Body) != "test" {
		t.Errorf("Body = %q, want %q", string(got.Body), "test")
	}
}

func TestLRUCache_Miss(t *testing.T) {
	cache := newLRUCache(10)
	_, ok := cache.Get("nonexistent", time.Hour)
	if ok {
		t.Error("expected cache miss for nonexistent key")
	}
}

func TestLRUCache_Expiration(t *testing.T) {
	cache := newLRUCache(10)
	resp := GalaxyResponse{Code: 200, Body: RawBytes("old")}
	cache.Put("key1", resp)

	// Get with negative maxAge guarantees expiration (time.Since >= 0 > -1ns)
	_, ok := cache.Get("key1", -time.Nanosecond)
	if ok {
		t.Error("expected cache miss for expired entry")
	}

	// Entry should have been evicted
	_, ok = cache.Get("key1", time.Hour)
	if ok {
		t.Error("expected evicted entry to be gone")
	}
}

func TestLRUCache_Eviction(t *testing.T) {
	cache := newLRUCache(2)

	cache.Put("key1", GalaxyResponse{Code: 1})
	cache.Put("key2", GalaxyResponse{Code: 2})
	cache.Put("key3", GalaxyResponse{Code: 3}) // should evict key1

	_, ok := cache.Get("key1", time.Hour)
	if ok {
		t.Error("expected key1 to be evicted")
	}

	got, ok := cache.Get("key2", time.Hour)
	if !ok {
		t.Fatal("expected key2 to be present")
	}
	if got.Code != 2 {
		t.Errorf("key2 Code = %d, want 2", got.Code)
	}

	got, ok = cache.Get("key3", time.Hour)
	if !ok {
		t.Fatal("expected key3 to be present")
	}
	if got.Code != 3 {
		t.Errorf("key3 Code = %d, want 3", got.Code)
	}
}

func TestLRUCache_LRUOrder(t *testing.T) {
	cache := newLRUCache(2)

	cache.Put("key1", GalaxyResponse{Code: 1})
	cache.Put("key2", GalaxyResponse{Code: 2})

	// Access key1 to make it recently used
	cache.Get("key1", time.Hour)

	// key2 is now LRU, should be evicted
	cache.Put("key3", GalaxyResponse{Code: 3})

	_, ok := cache.Get("key2", time.Hour)
	if ok {
		t.Error("expected key2 to be evicted (LRU)")
	}

	_, ok = cache.Get("key1", time.Hour)
	if !ok {
		t.Error("expected key1 to be present (recently accessed)")
	}
}

func TestLRUCache_UpdateExisting(t *testing.T) {
	cache := newLRUCache(10)
	cache.Put("key1", GalaxyResponse{Code: 200})
	cache.Put("key1", GalaxyResponse{Code: 201})

	got, ok := cache.Get("key1", time.Hour)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Code != 201 {
		t.Errorf("Code = %d, want 201", got.Code)
	}
}

func TestLRUCache_Clear(t *testing.T) {
	cache := newLRUCache(10)
	cache.Put("key1", GalaxyResponse{Code: 200})
	cache.Put("key2", GalaxyResponse{Code: 201})

	cache.Clear()

	_, ok1 := cache.Get("key1", time.Hour)
	_, ok2 := cache.Get("key2", time.Hour)
	if ok1 || ok2 {
		t.Error("expected all entries cleared")
	}
}
