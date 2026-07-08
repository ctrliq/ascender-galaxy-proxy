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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/gin-gonic/gin"
)

// ── Api handler ──────────────────────────────────────────────────────

func TestApiHandler(t *testing.T) {
	g := newTestProxy(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	g.Api(c)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse body: %v", err)
	}
	if body["description"] != "Galaxy Proxy" {
		t.Errorf("description = %v, want %q", body["description"], "Galaxy Proxy")
	}
	if body["current_version"] != "v1" {
		t.Errorf("current_version = %v, want %q", body["current_version"], "v1")
	}
	versions, ok := body["available_versions"].(map[string]interface{})
	if !ok {
		t.Fatal("available_versions is not a map")
	}
	if versions["v1"] != "v1/" || versions["v2"] != "v2/" || versions["v3"] != "v3/" {
		t.Errorf("unexpected available_versions: %v", versions)
	}
}

// ── MetricsHandler ───────────────────────────────────────────────────

func TestMetricsHandler(t *testing.T) {
	g := newTestProxy(t)
	g.metrics.CacheHits.Store(100)
	g.metrics.CacheMisses.Store(50)
	g.metrics.UpstreamSuccess.Store(45)
	g.metrics.TotalUpstreamResponseTimeMicros.Store(9000000) // 9 seconds
	g.metrics.TotalCacheHitResponseTimeMicros.Store(100000)  // 0.1 seconds

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	g.MetricsHandler(c)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}

	body := w.Body.String()

	expectedMetrics := []string{
		"galaxy_proxy_cache_hits_total 100",
		"galaxy_proxy_cache_misses_total 50",
		"galaxy_proxy_upstream_success_total 45",
		"galaxy_proxy_upstream_avg_response_seconds 0.2000",
		"galaxy_proxy_cache_hit_avg_response_seconds 0.0010",
	}
	for _, exp := range expectedMetrics {
		if !strings.Contains(body, exp) {
			t.Errorf("metrics body missing %q\nbody:\n%s", exp, body)
		}
	}
}

func TestMetricsHandler_ZeroCounts(t *testing.T) {
	g := newTestProxy(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	g.MetricsHandler(c)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}

	body := w.Body.String()
	// Should contain 0.0000 averages when no data
	if !strings.Contains(body, "galaxy_proxy_upstream_avg_response_seconds 0.0000") {
		t.Errorf("expected zero avg upstream time\nbody:\n%s", body)
	}
}

// ── GalaxyHandler auth ─────────────────────────────────────────────

func TestGalaxyHandler_Unauthorized(t *testing.T) {
	g := newTestProxy(t)
	g.apiToken = "secret-token"

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/roles/", nil)

	g.GalaxyHandler(c)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Unauthorized") {
		t.Error("expected Unauthorized in body")
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header")
	}
}

func TestGalaxyHandler_WrongToken(t *testing.T) {
	g := newTestProxy(t)
	g.apiToken = "secret-token"

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/roles/", nil)
	c.Request.Header.Set("Authorization", "Token wrong-token")

	g.GalaxyHandler(c)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// ── ArtifactHandler auth / validation ───────────────────────────────

func TestArtifactHandler_Unauthorized(t *testing.T) {
	g := newTestProxy(t)
	g.apiToken = "secret-token"

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/download/test-artifact.tar.gz", nil)

	g.ArtifactHandler(c)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestArtifactHandler_InvalidFilename(t *testing.T) {
	g := newTestProxy(t)

	tests := []struct {
		name string
		path string
	}{
		{"dot", "/."},
		{"double dot", "/.."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", tt.path, nil)
			g.ArtifactHandler(c)
			if w.Code != 400 {
				t.Errorf("status = %d, want 400", w.Code)
			}
		})
	}
}

func TestArtifactHandler_CachedFile(t *testing.T) {
	g := newTestProxy(t)

	// Create artifact download cache directory and a cached file
	downloadDir := filepath.Join(g.cacheDir, "download")
	if err := os.MkdirAll(downloadDir, 0700); err != nil {
		t.Fatalf("MkdirAll(%q) error: %v", downloadDir, err)
	}

	// Build the expected cache filename for path "/download/test.tar.gz"
	urlPath := "/download/test.tar.gz"
	baseName := filepath.Base(urlPath)
	artifactFilename := formatHashKey(xxhash.Sum64String(urlPath)) + "_" + baseName
	fpath := filepath.Join(downloadDir, artifactFilename)

	content := []byte("fake-artifact-content")
	if err := os.WriteFile(fpath, content, 0600); err != nil {
		t.Fatalf("WriteFile(%q) error: %v", fpath, err)
	}

	w := httptest.NewRecorder()
	c, engine := gin.CreateTestContext(w)
	// Gin's c.File needs the engine to be set up properly
	engine.GET("/download/:artifact", g.ArtifactHandler)
	c.Request = httptest.NewRequest("GET", urlPath, nil)
	engine.ServeHTTP(w, c.Request)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != string(content) {
		t.Errorf("body = %q, want %q", w.Body.String(), string(content))
	}
}

// ── GalaxyHandler with mock upstream ──────────────────────────────

func TestGalaxyHandler_Success(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Header", "test-value")
		w.WriteHeader(200)
		_, _ = fmt.Fprintf(w, `{"count":1,"results":[]}`)
	}))
	defer upstream.Close()

	g := newTestProxy(t)
	setUpstream(g, upstream.URL, upstream.Client())

	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.GET("/api/v1/roles/", g.GalaxyHandler)

	req := httptest.NewRequest("GET", "/api/v1/roles/", nil)
	engine.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"count":1`) {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestGalaxyHandler_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = fmt.Fprintf(w, `{"error":"internal server error"}`)
	}))
	defer upstream.Close()

	g := newTestProxy(t)
	setUpstream(g, upstream.URL, upstream.Client())

	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.GET("/api/v1/roles/", g.GalaxyHandler)

	req := httptest.NewRequest("GET", "/api/v1/roles/", nil)
	engine.ServeHTTP(w, req)

	if w.Code != 500 {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestGalaxyHandler_WithAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = fmt.Fprintf(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	g := newTestProxy(t)
	setUpstream(g, upstream.URL, upstream.Client())
	g.apiToken = "secret-token"

	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.GET("/api/v1/roles/", g.GalaxyHandler)

	req := httptest.NewRequest("GET", "/api/v1/roles/", nil)
	req.Header.Set("Authorization", "Token secret-token")
	engine.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// ── ArtifactHandler with mock upstream ──────────────────────────────

func TestArtifactHandler_DownloadFromUpstream(t *testing.T) {
	artifactContent := "fake-tar-gz-content-for-testing"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(artifactContent)))
		w.WriteHeader(200)
		_, _ = w.Write([]byte(artifactContent))
	}))
	defer upstream.Close()

	g := newTestProxy(t)
	setUpstream(g, upstream.URL, upstream.Client())

	// Ensure download cache dir exists
	if err := os.MkdirAll(filepath.Join(g.cacheDir, "download"), 0700); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}

	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.GET("/download/:artifact", g.ArtifactHandler)

	req := httptest.NewRequest("GET", "/download/my-collection-1.0.0.tar.gz", nil)
	engine.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != artifactContent {
		t.Errorf("body = %q, want %q", w.Body.String(), artifactContent)
	}
}

func TestArtifactHandler_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer upstream.Close()

	g := newTestProxy(t)
	setUpstream(g, upstream.URL, upstream.Client())
	if err := os.MkdirAll(filepath.Join(g.cacheDir, "download"), 0700); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}

	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.GET("/download/:artifact", g.ArtifactHandler)

	req := httptest.NewRequest("GET", "/download/nonexistent.tar.gz", nil)
	engine.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestArtifactHandler_ContentLengthTooLarge(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "50000000") // 50MB > 40MB limit
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	g := newTestProxy(t)
	setUpstream(g, upstream.URL, upstream.Client())
	if err := os.MkdirAll(filepath.Join(g.cacheDir, "download"), 0700); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}

	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.GET("/download/:artifact", g.ArtifactHandler)

	req := httptest.NewRequest("GET", "/download/big-artifact.tar.gz", nil)
	engine.ServeHTTP(w, req)

	if w.Code != 413 {
		t.Errorf("status = %d, want 413", w.Code)
	}
}

// ── URL rewriting with upstream mock ────────────────────────────────

func TestGalaxyHandler_URLRewriting(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		// Response contains the upstream's own URL
		_, _ = fmt.Fprintf(w, `{"next":"http://%s/api/v1/roles/?page=2"}`, r.Host)
	}))
	defer upstream.Close()

	g := newTestProxy(t)
	setUpstream(g, upstream.URL, upstream.Client())
	g.baseURL = "https://proxy.example.com"

	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.GET("/api/v1/roles/", g.GalaxyHandler)

	req := httptest.NewRequest("GET", "/api/v1/roles/", nil)
	engine.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, upstream.URL) {
		t.Errorf("body still contains upstream URL: %s", body)
	}
	if !strings.Contains(body, "https://proxy.example.com") {
		t.Errorf("body missing proxy URL: %s", body)
	}
}

// ── Cache behavior with upstream mock ───────────────────────────────

func TestGalaxyHandler_CacheHitOnSecondRequest(t *testing.T) {
	var callCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = fmt.Fprintf(w, `{"call":%d}`, n)
	}))
	defer upstream.Close()

	g := newTestProxy(t)
	setUpstream(g, upstream.URL, upstream.Client())

	makeRequest := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		_, engine := gin.CreateTestContext(w)
		engine.GET("/api/v1/roles/", g.GalaxyHandler)
		req := httptest.NewRequest("GET", "/api/v1/roles/", nil)
		engine.ServeHTTP(w, req)
		return w
	}

	// First request should hit upstream
	w1 := makeRequest()
	if w1.Code != 200 {
		t.Fatalf("first request status = %d, want 200", w1.Code)
	}
	if !strings.Contains(w1.Body.String(), `"call":1`) {
		t.Errorf("first request body unexpected: %s", w1.Body.String())
	}

	// Second request should hit cache (upstream only called once)
	w2 := makeRequest()
	if w2.Code != 200 {
		t.Fatalf("second request status = %d, want 200", w2.Code)
	}
	// Should get same response from cache
	if !strings.Contains(w2.Body.String(), `"call":1`) {
		t.Errorf("second request should return cached response: %s", w2.Body.String())
	}
	if callCount.Load() != 1 {
		t.Errorf("upstream called %d times, want 1 (second request should be cached)", callCount.Load())
	}
}

// ── ArtifactHandler with no Content-Length (unknown size) ───────────

func TestArtifactHandler_NoContentLength(t *testing.T) {
	artifactContent := "artifact-data-without-content-length"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately don't set Content-Length
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(artifactContent))
	}))
	defer upstream.Close()

	g := newTestProxy(t)
	setUpstream(g, upstream.URL, upstream.Client())
	if err := os.MkdirAll(filepath.Join(g.cacheDir, "download"), 0700); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}

	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.GET("/download/:artifact", g.ArtifactHandler)

	req := httptest.NewRequest("GET", "/download/collection-1.0.0.tar.gz", nil)
	engine.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// ── Query parameters forwarded correctly ────────────────────────────

func TestGalaxyHandler_QueryParams(t *testing.T) {
	queryCh := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queryCh <- r.URL.RawQuery
		w.WriteHeader(200)
		_, _ = fmt.Fprintf(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	g := newTestProxy(t)
	setUpstream(g, upstream.URL, upstream.Client())

	w := httptest.NewRecorder()
	_, engine := gin.CreateTestContext(w)
	engine.GET("/api/v1/roles/", g.GalaxyHandler)

	req := httptest.NewRequest("GET", "/api/v1/roles/?page=2&page_size=10", nil)
	engine.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var receivedQuery string
	select {
	case receivedQuery = <-queryCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream to receive query")
	}
	if !strings.Contains(receivedQuery, "page=2") {
		t.Errorf("expected query params forwarded, got: %s", receivedQuery)
	}
	if !strings.Contains(receivedQuery, "page_size=10") {
		t.Errorf("expected page_size forwarded, got: %s", receivedQuery)
	}
}
