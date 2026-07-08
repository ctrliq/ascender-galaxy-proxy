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
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ── clearCacheDirectory ─────────────────────────────────────────────

func TestClearCacheDirectory(t *testing.T) {
	g := newTestProxy(t)

	// Create some files and subdirectories
	subdir := filepath.Join(g.cacheDir, "abc")
	if err := os.MkdirAll(subdir, 0700); err != nil {
		t.Fatalf("MkdirAll(%q) error: %v", subdir, err)
	}
	if err := os.WriteFile(filepath.Join(g.cacheDir, "file1.json"), []byte("data"), 0600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "file2.json"), []byte("data"), 0600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	if err := g.clearCacheDirectory(); err != nil {
		t.Fatalf("clearCacheDirectory() error: %v", err)
	}

	entries, err := os.ReadDir(g.cacheDir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty cache dir, got %d entries", len(entries))
	}
}

func TestClearCacheDirectory_NonExistent(t *testing.T) {
	g := newTestProxy(t)
	g.cacheDir = filepath.Join(t.TempDir(), "nonexistent")

	if err := g.clearCacheDirectory(); err != nil {
		t.Errorf("clearCacheDirectory() should not error for non-existent dir: %v", err)
	}
}

// ── fetchAndCache ───────────────────────────────────────────────────

func TestFetchAndCache_DiskCacheHit(t *testing.T) {
	g := newTestProxy(t)

	// Pre-populate disk cache
	fhash := "abcd1234abcd1234"
	fdir := filepath.Join(g.cacheDir, fhash[0:3])
	if err := os.MkdirAll(fdir, 0700); err != nil {
		t.Fatalf("MkdirAll(%q) error: %v", fdir, err)
	}
	fname := filepath.Join(fdir, fhash+".json")

	cached := GalaxyResponse{
		Code: 200,
		Url:  "https://galaxy.ansible.com/api/v1/roles/",
		Headers: http.Header{
			"Content-Type": {"application/json"},
		},
		Body:    RawBytes(`{"cached":true}`),
		Fetched: time.Now().Format(time.RFC3339),
	}
	data, err := json.Marshal(cached)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	if err := os.WriteFile(fname, data, 0600); err != nil {
		t.Fatalf("WriteFile(%q) error: %v", fname, err)
	}

	result := g.fetchAndCache("https://galaxy.ansible.com/api/v1/roles/", fhash, fdir, fname)

	if result.Code != 200 {
		t.Errorf("Code = %d, want 200", result.Code)
	}
	if !strings.Contains(string(result.Body), `"cached":true`) {
		t.Errorf("unexpected body: %s", string(result.Body))
	}
}

func TestFetchAndCache_DiskCacheExpired(t *testing.T) {
	var callCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = fmt.Fprintf(w, `{"fresh":true}`)
	}))
	defer upstream.Close()

	g := newTestProxy(t)
	setUpstream(g, upstream.URL, upstream.Client())
	g.queryCacheExpire = 0 // Expire immediately

	// Pre-populate disk cache with old data
	fhash := "expire12345abcde"
	fdir := filepath.Join(g.cacheDir, fhash[0:3])
	if err := os.MkdirAll(fdir, 0700); err != nil {
		t.Fatalf("MkdirAll(%q) error: %v", fdir, err)
	}
	fname := filepath.Join(fdir, fhash+".json")

	cached := GalaxyResponse{
		Code: 200,
		Body: RawBytes(`{"old":true}`),
	}
	data, err := json.Marshal(cached)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	if err := os.WriteFile(fname, data, 0600); err != nil {
		t.Fatalf("WriteFile(%q) error: %v", fname, err)
	}

	// Set mod time to past to ensure expiration
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(fname, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes error: %v", err)
	}

	result := g.fetchAndCache(upstream.URL+"/api/v1/roles/", fhash, fdir, fname)

	if result.Code != 200 {
		t.Errorf("Code = %d, want 200", result.Code)
	}
	if !strings.Contains(string(result.Body), `"fresh":true`) {
		t.Errorf("expected fresh data, got: %s", string(result.Body))
	}
	if callCount.Load() != 1 {
		t.Errorf("upstream called %d times, want 1", callCount.Load())
	}
}

func TestFetchAndCache_UpstreamDown(t *testing.T) {
	// Suppress expected ERROR log from hitting an unreachable upstream
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(orig)

	g := newTestProxy(t)
	g.httpClient = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("connection refused")
		}),
	}

	fhash := "downtest12345abc"
	fdir := filepath.Join(g.cacheDir, fhash[0:3])
	if err := os.MkdirAll(fdir, 0700); err != nil {
		t.Fatalf("MkdirAll(%q) error: %v", fdir, err)
	}
	fname := filepath.Join(fdir, fhash+".json")

	result := g.fetchAndCache(g.upstreamBaseURL+"/api/v1/", fhash, fdir, fname)

	if result.Code != 502 {
		t.Errorf("Code = %d, want 502 for unreachable upstream", result.Code)
	}
}
