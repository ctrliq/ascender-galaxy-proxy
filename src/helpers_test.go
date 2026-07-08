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
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTestProxy creates a minimal GalaxyProxy backed by a temp directory.
func newTestProxy(t *testing.T) *GalaxyProxy {
	t.Helper()
	cacheDir := t.TempDir()
	upstream := "https://galaxy.ansible.com"
	return &GalaxyProxy{
		upstreamBaseURL:       upstream,
		upstreamBaseURLBytes:  []byte(upstream),
		upstreamBaseURLInJSON: append([]byte(`"`), []byte(upstream)...),
		baseURL:               "https://proxy.example.com",
		cacheDir:              cacheDir,
		metricsFile:           filepath.Join(cacheDir, ".metrics"),
		memCache:              newLRUCache(100),
		queryCacheExpire:      24 * time.Hour,
		artifactCacheExpire:   30 * 24 * time.Hour,
		httpClient:            &http.Client{Timeout: 10 * time.Second},
		done:                  make(chan struct{}),
	}
}

// roundTripFunc is a test helper that implements http.RoundTripper with a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// setUpstream points g at a mock upstream server, keeping all URL-derived
// fields in sync.
func setUpstream(g *GalaxyProxy, url string, client *http.Client) {
	g.upstreamBaseURL = url
	g.upstreamBaseURLBytes = []byte(url)
	g.upstreamBaseURLInJSON = append([]byte(`"`), []byte(url)...)
	g.httpClient = client
}
