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

ascender_galaxy_proxy.go

A configurable reverse proxy for galaxy.ansible.com (or any compatible Ansible Galaxy API server)
with on-disk caching of API responses and artifact downloads.

Features:
- Configurable upstream Galaxy server (default: galaxy.ansible.com)
- On-disk caching with automatic expiration (configurable)
- API token authentication with Authorization header validation
- Prometheus metrics endpoint (/metrics) for monitoring cache performance
- Artifact download caching with 40MB size limit
- Trusted proxy support for behind-the-scenes deployments
- Full support for Galaxy API v1, v2, and v3 endpoints
- Optional cache clearing on startup

Environment variables:
- UPSTREAM_BASEURL: Upstream Galaxy server URL
- URL: External URL of this proxy (e.g. https://galaxy-proxy.example.com). Required.
- QUERY_CACHE_EXPIRE: Cache expiration in days for API queries (default: 1 day)
- ARTIFACT_CACHE_EXPIRE: Cache expiration in days for artifact downloads (default: 30 days)
- GALAXY_API_TOKEN: API token for authentication (if set, all requests must include Authorization header)
- TRUSTED_PROXIES: Comma-separated list of trusted proxy IPs
- DEBUG: Enable debug mode ("true" or "1" for debug, default: "false" for release)
- CLEAR_CACHE_ON_START: If set to "1" or "True", clears cache directory on startup
- MEM_CACHE_SIZE: Maximum items in the in-memory LRU cache (default: 2000)
- HTTP_PROXY: HTTP proxy URL for upstream requests (e.g. http://proxy.example.com:8080)

*/

package main

import (
    "bytes"
    "container/list"
    "context"
    "crypto/subtle"
    "encoding/json"
    "flag"
    "fmt"
    "io"
    "log/slog"
    "net"
    "net/http"
    "net/url"
    "os"
    "os/signal"
    "path/filepath"
    "strconv"
    "strings"
    "sync"
    "sync/atomic"
    "syscall"
    "time"

    "github.com/cespare/xxhash/v2"
    "github.com/gin-gonic/gin"
    "golang.org/x/sync/singleflight"
)

var (
    appName    = "Ascender Galaxy Proxy"
    appVersion = "1.0.2"
)

const defaultCacheDir = ".cache"

// redactProxyURLForLogging returns a sanitized proxy URL for logging, redacting userinfo.
// This prevents credentials from leaking into logs.
func redactProxyURLForLogging(proxyURL string) string {
    u, err := url.Parse(proxyURL)
    if err != nil {
        return "<invalid>"
    }
    // Construct scheme://host[:port] without userinfo
    if u.Host == "" {
        return "<invalid>"
    }
    sanitized := u.Scheme + "://" + u.Host
    return sanitized
}

// newHTTPClient creates an HTTP client with transport timeouts
// to prevent hanging on upstream requests.
// If proxyURL is provided, the client will route requests through the specified HTTP proxy.
func newHTTPClient(proxyURL string) *http.Client {
    transport := &http.Transport{
        DialContext: (&net.Dialer{
            Timeout:   30 * time.Second,
            KeepAlive: 30 * time.Second,
        }).DialContext,
        TLSHandshakeTimeout:   30 * time.Second,
        ResponseHeaderTimeout: 30 * time.Second,
        IdleConnTimeout:       90 * time.Second,
        MaxIdleConnsPerHost:   20, // Increase from default 2 since all requests go to one upstream
        MaxConnsPerHost:       50, // Bound total concurrent connections to upstream
    }

    // Configure HTTP proxy if provided
    if proxyURL != "" {
        parsedURL, err := url.Parse(proxyURL)
        if err != nil {
            sanitizedURL := redactProxyURLForLogging(proxyURL)
            slog.Warn("Invalid HTTP_PROXY URL, ignoring proxy configuration", "proxyURL", sanitizedURL, "error", err)
        } else if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
            // Validate proxy scheme
            sanitizedURL := redactProxyURLForLogging(proxyURL)
            slog.Warn("HTTP_PROXY URL has invalid scheme, must be http or https", "proxyURL", sanitizedURL, "scheme", parsedURL.Scheme)
        } else if parsedURL.Host == "" {
            // Validate proxy host
            slog.Warn("HTTP_PROXY URL has no host, ignoring proxy configuration")
        } else {
            // Proxy is valid, configure it
            transport.Proxy = http.ProxyURL(parsedURL)
            sanitizedURL := redactProxyURLForLogging(proxyURL)
            slog.Info("HTTP proxy configured", "proxyURL", sanitizedURL)
        }
    }

    return &http.Client{
        Timeout:   5 * time.Minute, // Overall request timeout to prevent slow-drip attacks
        Transport: transport,
    }
}

// GalaxyProxy holds all mutable state for the proxy, enabling dependency
// injection and testability instead of relying on package-level globals.
type GalaxyProxy struct {
    upstreamBaseURL          string
    upstreamBaseURLBytes     []byte // cached for bytes.Replace performance
    upstreamBaseURLInJSON    []byte // cached as `"<url>` for JSON-context URL rewriting
    baseURL                 string // external URL of this proxy (from URL env var)
    apiToken                 string
    cacheDir             string
    metricsFile          string
    metrics              Metrics
    metricsWriteMu       sync.Mutex
    pendingMetricWrites  atomic.Int64
    requestGroup         singleflight.Group
    memCache             *lruCache
    queryCacheExpire     time.Duration
    artifactCacheExpire  time.Duration
    httpClient           *http.Client
    done                 chan struct{} // signals background goroutines to stop
}

// RawBytes is a []byte type that marshals/unmarshals as a JSON string
// instead of base64, avoiding unnecessary string<->[]byte conversions in the hot path.
type RawBytes []byte

func (r RawBytes) MarshalJSON() ([]byte, error) {
    return json.Marshal(string(r))
}

func (r *RawBytes) UnmarshalJSON(data []byte) error {
    var s string
    if err := json.Unmarshal(data, &s); err != nil {
        return err
    }
    *r = RawBytes(s)
    return nil
}

// GalaxyResponse represents a cached upstream response.
// Headers are stored as http.Header (map[string][]string) to avoid repeated
// JSON marshal/unmarshal overhead on every cache hit.
type GalaxyResponse struct {
    Code    int
    Url     string
    Headers http.Header
    Body    RawBytes
    Fetched string
}

type Metrics struct {
    CacheHits                       atomic.Int64
    CacheExpires                    atomic.Int64
    CacheMisses                     atomic.Int64
    UpstreamSuccess                 atomic.Int64
    UpstreamClientErrors            atomic.Int64
    UpstreamServerErrors            atomic.Int64
    UpstreamNoResponse              atomic.Int64
    TotalUpstreamResponseTimeMicros atomic.Int64
    TotalCacheHitResponseTimeMicros atomic.Int64
}

// metricsSnapshot is a plain struct for JSON serialization of atomic metrics
type metricsSnapshot struct {
    CacheHits                 int64   `json:"CacheHits"`
    CacheExpires              int64   `json:"CacheExpires"`
    CacheMisses               int64   `json:"CacheMisses"`
    UpstreamSuccess           int64   `json:"UpstreamSuccess"`
    UpstreamClientErrors      int64   `json:"UpstreamClientErrors"`
    UpstreamServerErrors      int64   `json:"UpstreamServerErrors"`
    UpstreamNoResponse        int64   `json:"UpstreamNoResponse"`
    TotalUpstreamResponseTime float64 `json:"TotalUpstreamResponseTime"`
    TotalCacheHitResponseTime float64 `json:"TotalCacheHitResponseTime"`
}

func (m *Metrics) snapshot() metricsSnapshot {
    return metricsSnapshot{
        CacheHits:                 m.CacheHits.Load(),
        CacheExpires:              m.CacheExpires.Load(),
        CacheMisses:               m.CacheMisses.Load(),
        UpstreamSuccess:           m.UpstreamSuccess.Load(),
        UpstreamClientErrors:      m.UpstreamClientErrors.Load(),
        UpstreamServerErrors:      m.UpstreamServerErrors.Load(),
        UpstreamNoResponse:        m.UpstreamNoResponse.Load(),
        TotalUpstreamResponseTime: float64(m.TotalUpstreamResponseTimeMicros.Load()) / 1e6,
        TotalCacheHitResponseTime: float64(m.TotalCacheHitResponseTimeMicros.Load()) / 1e6,
    }
}

func (m *Metrics) restore(s metricsSnapshot) {
    m.CacheHits.Store(s.CacheHits)
    m.CacheExpires.Store(s.CacheExpires)
    m.CacheMisses.Store(s.CacheMisses)
    m.UpstreamSuccess.Store(s.UpstreamSuccess)
    m.UpstreamClientErrors.Store(s.UpstreamClientErrors)
    m.UpstreamServerErrors.Store(s.UpstreamServerErrors)
    m.UpstreamNoResponse.Store(s.UpstreamNoResponse)
    m.TotalUpstreamResponseTimeMicros.Store(int64(s.TotalUpstreamResponseTime * 1e6))
    m.TotalCacheHitResponseTimeMicros.Store(int64(s.TotalCacheHitResponseTime * 1e6))
}

func (m *Metrics) reset() {
    m.CacheHits.Store(0)
    m.CacheExpires.Store(0)
    m.CacheMisses.Store(0)
    m.UpstreamSuccess.Store(0)
    m.UpstreamClientErrors.Store(0)
    m.UpstreamServerErrors.Store(0)
    m.UpstreamNoResponse.Store(0)
    m.TotalUpstreamResponseTimeMicros.Store(0)
    m.TotalCacheHitResponseTimeMicros.Store(0)
}

// Package-level hop-by-hop header lookup (immutable, no per-instance state needed)
var hopByHopHeaders = map[string]bool{
    "Connection":           true,
    "Keep-Alive":           true,
    "Proxy-Authenticate":   true,
    "Proxy-Authorization":  true,
    "TE":                   true,
    "Trailer":              true,
    "Transfer-Encoding":    true,
    "Upgrade":              true,
    "Content-Length":       true, // Also filter Content-Length since body is rewritten
}

// In-memory LRU cache for upstream API responses
type lruCache struct {
    mu       sync.Mutex
    maxItems int
    items    map[string]*list.Element
    order    *list.List
}

type lruEntry struct {
    key      string
    value    GalaxyResponse
    cachedAt time.Time
}

func newLRUCache(maxItems int) *lruCache {
    return &lruCache{
        maxItems: maxItems,
        items:    make(map[string]*list.Element),
        order:    list.New(),
    }
}

func (c *lruCache) Get(key string, maxAge time.Duration) (GalaxyResponse, bool) {
    c.mu.Lock()
    defer c.mu.Unlock()
    if elem, ok := c.items[key]; ok {
        entry := elem.Value.(*lruEntry)
        if time.Since(entry.cachedAt) > maxAge {
            // Expired, evict from cache
            c.order.Remove(elem)
            delete(c.items, key)
            return GalaxyResponse{}, false
        }
        c.order.MoveToFront(elem)
        return entry.value, true
    }
    return GalaxyResponse{}, false
}

func (c *lruCache) Put(key string, value GalaxyResponse) {
    c.mu.Lock()
    defer c.mu.Unlock()
    if elem, ok := c.items[key]; ok {
        c.order.MoveToFront(elem)
        entry := elem.Value.(*lruEntry)
        entry.value = value
        entry.cachedAt = time.Now()
        return
    }
    // Evict oldest if at capacity
    if c.order.Len() >= c.maxItems {
        oldest := c.order.Back()
        if oldest != nil {
            c.order.Remove(oldest)
            delete(c.items, oldest.Value.(*lruEntry).key)
        }
    }
    entry := &lruEntry{key: key, value: value, cachedAt: time.Now()}
    elem := c.order.PushFront(entry)
    c.items[key] = elem
}

func (c *lruCache) Clear() {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.items = make(map[string]*list.Element)
    c.order.Init()
}

func getMemCacheSize() int {
    value := os.Getenv("MEM_CACHE_SIZE")
    if value == "" {
        return 2000
    }
    size, err := strconv.Atoi(value)
    if err != nil || size <= 0 {
        return 2000
    }
    return size
}

// ── Configuration helpers ───────────────────────────────────────────

func getUpstreamBaseURL() string {
    fallback := "https://galaxy.ansible.com"
    value := os.Getenv("UPSTREAM_BASEURL")
    if len(value) == 0 {
        return fallback
    }
    parsed, err := url.Parse(value)
    if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
        slog.Warn("Invalid UPSTREAM_BASEURL, using fallback",
            "value", value, "fallback", fallback)
        return fallback
    }
    return strings.TrimRight(value, "/")
}

func getCacheExpire(envVar string, defaultDays int) time.Duration {
    fallback := time.Duration(defaultDays) * 24 * time.Hour
    value := os.Getenv(envVar)
    if value == "" {
        return fallback
    }
    result, err := strconv.Atoi(value)
    if err != nil {
        return fallback
    }
    const maxDays = 365
    if result < 0 || result > maxDays {
        slog.Warn("Cache expiry out of range, using default",
            "env", envVar, "value", result, "max", maxDays, "default", defaultDays)
        return fallback
    }
    return time.Duration(result) * 24 * time.Hour
}

func getAPIToken() string {
    return os.Getenv("GALAXY_API_TOKEN")
}

// getBaseURL reads and validates the URL environment variable.
// It must be a valid HTTP or HTTPS URL. Returns the URL with any trailing slash removed.
func getBaseURL() (string, error) {
    value := os.Getenv("URL")
    if value == "" {
        return "", fmt.Errorf("URL environment variable is required but not set")
    }
    parsed, err := url.Parse(value)
    if err != nil {
        return "", fmt.Errorf("URL is not a valid URL: %w", err)
    }
    if parsed.Scheme != "http" && parsed.Scheme != "https" {
        return "", fmt.Errorf("URL must use http or https scheme, got %q", parsed.Scheme)
    }
    if parsed.Host == "" {
        return "", fmt.Errorf("URL must include a host")
    }
    return strings.TrimRight(value, "/"), nil
}

func shouldClearCacheOnStart() bool {
    value := os.Getenv("CLEAR_CACHE_ON_START")
    return value == "1" || strings.EqualFold(value, "true")
}

// ── Cache / metrics methods on GalaxyProxy ──────────────────────────

func (g *GalaxyProxy) clearCacheDirectory() error {
    entries, err := os.ReadDir(g.cacheDir)
    if err != nil {
        if os.IsNotExist(err) {
            return nil
        }
        return err
    }

    for _, entry := range entries {
        path := filepath.Join(g.cacheDir, entry.Name())
        if entry.IsDir() {
            if err := os.RemoveAll(path); err != nil {
                return err
            }
        } else {
            if err := os.Remove(path); err != nil {
                return err
            }
        }
    }
    return nil
}

func (g *GalaxyProxy) loadMetrics() {
    data, err := os.ReadFile(g.metricsFile)
    if err != nil {
        return
    }
    var s metricsSnapshot
    if err := json.Unmarshal(data, &s); err != nil {
        slog.Error("Error loading metrics", "error", err)
        return
    }
    g.metrics.restore(s)
}

func (g *GalaxyProxy) saveMetrics() {
    g.metricsWriteMu.Lock()
    defer g.metricsWriteMu.Unlock()

    s := g.metrics.snapshot()
    data, err := json.Marshal(s)
    if err != nil {
        slog.Error("Error marshaling metrics", "error", err)
        return
    }
    if err := os.WriteFile(g.metricsFile, data, 0600); err != nil {
        slog.Error("Error writing metrics", "error", err)
    }
    g.pendingMetricWrites.Store(0)
}

// flushMetricsToDisk writes current metrics to disk, using a write mutex
// to prevent concurrent file writes while keeping metric increments lock-free.
func (g *GalaxyProxy) flushMetricsToDisk() {
    if g.pendingMetricWrites.Load() == 0 {
        return
    }
    g.metricsWriteMu.Lock()
    defer g.metricsWriteMu.Unlock()

    s := g.metrics.snapshot()
    g.pendingMetricWrites.Store(0)
    data, err := json.Marshal(s)
    if err != nil {
        slog.Error("Error marshaling metrics", "error", err)
        return
    }
    if err := os.WriteFile(g.metricsFile, data, 0600); err != nil {
        slog.Error("Error writing metrics", "error", err)
    }
}

// incrementMetric increments the named metric counter.
// Metrics are flushed to disk by a background goroutine; no synchronous
// disk I/O occurs in the request hot path.
func (g *GalaxyProxy) incrementMetric(metricType string) {
    switch metricType {
    case "hit":
        g.metrics.CacheHits.Add(1)
    case "expire":
        g.metrics.CacheExpires.Add(1)
    case "miss":
        g.metrics.CacheMisses.Add(1)
    case "upstream_success":
        g.metrics.UpstreamSuccess.Add(1)
    case "upstream_client_error":
        g.metrics.UpstreamClientErrors.Add(1)
    case "upstream_server_error":
        g.metrics.UpstreamServerErrors.Add(1)
    case "upstream_no_response":
        g.metrics.UpstreamNoResponse.Add(1)
    }
    g.pendingMetricWrites.Add(1)
}

func (g *GalaxyProxy) addResponseTime(metricType string, responseTime float64) {
    micros := int64(responseTime * 1e6)
    switch metricType {
    case "upstream":
        g.metrics.TotalUpstreamResponseTimeMicros.Add(micros)
    case "cache_hit":
        g.metrics.TotalCacheHitResponseTimeMicros.Add(micros)
    }
    g.pendingMetricWrites.Add(1)
}

// isHopByHopHeader checks if a header is a hop-by-hop header that should not be forwarded
func isHopByHopHeader(name string) bool {
    return hopByHopHeaders[name]
}

// formatHashKey formats a uint64 hash as a zero-padded 16-character hex string
// without the overhead of fmt.Sprintf.
func formatHashKey(h uint64) string {
    const width = 16
    s := strconv.FormatUint(h, 16)
    if len(s) >= width {
        return s
    }
    var buf [width]byte
    for i := range buf {
        buf[i] = '0'
    }
    copy(buf[width-len(s):], s)
    return string(buf[:])
}

// ── Core proxy logic ────────────────────────────────────────────────

// rewriteBodyURLs replaces the upstream base URL with rhost in a JSON response body.
// Replacement is scoped to JSON string contexts (the upstream URL must be immediately
// preceded by a double-quote character) so documentation strings, example text, and
// other non-URL fields are left untouched. A json.Valid check acts as a safety net:
// if the replacement somehow produced malformed JSON the original body is returned.
func (g *GalaxyProxy) rewriteBodyURLs(body RawBytes, rhost string) RawBytes {
    if !bytes.Contains(body, g.upstreamBaseURLInJSON) {
        return body // fast path: nothing to rewrite
    }
    // Prepend '"' to rhost so the replacement stays within the JSON string context.
    rhostInJSON := make([]byte, 0, 1+len(rhost))
    rhostInJSON = append(rhostInJSON, '"')
    rhostInJSON = append(rhostInJSON, rhost...)
    result := bytes.ReplaceAll(body, g.upstreamBaseURLInJSON, rhostInJSON)
    if !json.Valid(result) {
        slog.Warn("URL rewrite produced invalid JSON; returning original body", "rhost", rhost)
        return body
    }
    return result
}

func (g *GalaxyProxy) getUpstreamURL(rhost string, urlPath string, queryParams url.Values) GalaxyResponse {

    /*****************************************************
     *  Get a request from cache or forward to upstream
     *  Uses in-memory LRU cache -> disk cache -> upstream
     *  with singleflight to deduplicate concurrent fetches
     ****************************************************/

    // assemble the url
    upstreamURL := g.upstreamBaseURL + urlPath
    paramString := queryParams.Encode()
    if len(paramString) > 0 {
        upstreamURL += "?" + paramString
    }

    // make a hash of this url using xxhash (non-cryptographic, ~20x faster than SHA-256)
    fhash := formatHashKey(xxhash.Sum64String(upstreamURL))

    // Fast path: check in-memory LRU cache (avoids all disk I/O)
    t := time.Now()
    if cached, ok := g.memCache.Get(fhash, g.queryCacheExpire); ok {
        slog.Info("Memory cache hit", "url", upstreamURL)
        g.incrementMetric("hit")
        resp := cached
        resp.Body = g.rewriteBodyURLs(resp.Body, rhost)
        g.addResponseTime("cache_hit", time.Since(t).Seconds())
        return resp
    }

    // Slow path: use singleflight to deduplicate concurrent requests for the same URL
    fprefix := fhash[0:3]
    fdir := filepath.Join(g.cacheDir, fprefix)
    fname := filepath.Join(fdir, fhash+".json")

    result, err, _ := g.requestGroup.Do(upstreamURL, func() (interface{}, error) {
        return g.fetchAndCache(upstreamURL, fhash, fdir, fname), nil
    })
    if err != nil {
        slog.Error("Unexpected error in singleflight group", "url", upstreamURL, "error", err)
        return GalaxyResponse{Code: 502}
    }

    resp := result.(GalaxyResponse)
    resp.Body = g.rewriteBodyURLs(resp.Body, rhost)
    return resp
}

// fetchAndCache checks disk cache, then fetches from upstream if needed.
// Results are stored in both the disk cache and the in-memory LRU cache.
func (g *GalaxyProxy) fetchAndCache(upstreamURL string, fhash string, fdir string, fname string) GalaxyResponse {

    // make cache directory if not exists (0700 to restrict access to proxy user)
    if err := os.MkdirAll(fdir, 0700); err != nil {
        slog.Error("Error creating cache directory", "path", fdir, "error", err)
        return GalaxyResponse{Code: 500}
    }

    // check disk cache
    if fileInfo, err := os.Stat(fname); err == nil {
        fileAge := time.Since(fileInfo.ModTime())
        if fileAge > g.queryCacheExpire {
            slog.Info("Disk cache expired", "url", upstreamURL, "path", fname)
            g.incrementMetric("expire")
            _ = os.Remove(fname)
        } else {
            t := time.Now()
            if byteValue, err := os.ReadFile(fname); err != nil {
                slog.Error("Error reading cache file", "path", fname, "error", err)
                _ = os.Remove(fname)
            } else {
                var resp GalaxyResponse
                if err := json.Unmarshal(byteValue, &resp); err != nil {
                    // Incompatible cache format (e.g. old headers-as-string format); re-fetch
                    slog.Warn("Incompatible cache file, re-fetching", "path", fname, "error", err)
                    _ = os.Remove(fname)
                } else {
                    slog.Info("Disk cache hit", "url", upstreamURL, "path", fname)
                    g.incrementMetric("hit")
                    g.addResponseTime("cache_hit", time.Since(t).Seconds())
                    g.memCache.Put(fhash, resp)
                    return resp
                }
            }
        }
    }

    // fetch from upstream
    slog.Info("Cache miss", "url", upstreamURL, "path", fname)
    g.incrementMetric("miss")
    t1 := time.Now()
    uresp, err := g.httpClient.Get(upstreamURL)
    if err != nil {
        slog.Error("Error fetching upstream", "url", upstreamURL, "error", err)
        g.addResponseTime("upstream", time.Since(t1).Seconds())
        g.incrementMetric("upstream_no_response")
        return GalaxyResponse{Code: 502}
    }
    defer func() { _ = uresp.Body.Close() }()

    // check HTTP status - accept any 2XX as success.
    // Note: the http.Client follows redirects automatically (up to 10), so 3XX responses
    // are transparent in normal operation. A 3XX here would only occur in an edge case
    // (e.g. a redirect loop is caught as an error above), but is handled explicitly below.
    if uresp.StatusCode < 200 || uresp.StatusCode >= 300 {
        slog.Warn("Upstream returned non-2XX status", "status", uresp.StatusCode, "url", upstreamURL)
        if uresp.StatusCode >= 500 {
            g.incrementMetric("upstream_server_error")
        } else if uresp.StatusCode >= 400 {
            g.incrementMetric("upstream_client_error")
        } else if uresp.StatusCode >= 300 {
            // Unexpected redirect not followed by the HTTP client
            g.incrementMetric("upstream_client_error")
        }
        const maxErrorBodySize = 1 * 1024 * 1024 // 1MB limit for error responses
        errBody, readErr := io.ReadAll(io.LimitReader(uresp.Body, maxErrorBodySize))
        g.addResponseTime("upstream", time.Since(t1).Seconds())
        if readErr != nil {
            slog.Error("Error reading upstream error body", "url", upstreamURL, "error", readErr)
            return GalaxyResponse{Code: uresp.StatusCode}
        }
        return GalaxyResponse{Code: uresp.StatusCode, Body: errBody}
    }
    g.incrementMetric("upstream_success")

    // read the body with a size limit to prevent OOM from oversized upstream responses
    const maxAPIResponseSize = 40 * 1024 * 1024 // 40MB
    body, err := io.ReadAll(io.LimitReader(uresp.Body, maxAPIResponseSize+1))
    g.addResponseTime("upstream", time.Since(t1).Seconds())
    if err != nil {
        slog.Error("Error reading response body", "error", err)
        return GalaxyResponse{Code: 502}
    }
    if int64(len(body)) > maxAPIResponseSize {
        slog.Warn("Upstream response exceeds size limit", "url", upstreamURL, "limit", maxAPIResponseSize)
        return GalaxyResponse{Code: 502}
    }

    // sanitize headers before caching - remove Authorization and hop-by-hop headers
    headersForCache := uresp.Header.Clone()
    headersForCache.Del("Authorization")
    for key := range headersForCache {
        if isHopByHopHeader(key) {
            headersForCache.Del(key)
        }
    }

    // construct the response for caching (store with upstreamBaseURL intact for stability)
    resp := GalaxyResponse{
        Code:    uresp.StatusCode,
        Headers: headersForCache,
        Body:    body,
        Url:     upstreamURL,
        Fetched: time.Now().Format(time.RFC3339),
    }

    // store response on disk atomically via temp file + rename
    fileJson, err := json.Marshal(resp)
    if err != nil {
        slog.Error("Error marshaling response", "error", err)
    } else {
        tmpFile, tmpErr := os.CreateTemp(fdir, ".tmp-")
        if tmpErr != nil {
            slog.Error("Error creating temp cache file", "error", tmpErr)
        } else {
            tmpPath := tmpFile.Name()
            if _, writeErr := tmpFile.Write(fileJson); writeErr != nil {
                _ = tmpFile.Close()
                if removeErr := os.Remove(tmpPath); removeErr != nil {
                    slog.Warn("Failed to remove temp cache file", "path", tmpPath, "error", removeErr)
                }
                slog.Error("Error writing temp cache file", "path", tmpPath, "error", writeErr)
            } else {
                if closeErr := tmpFile.Close(); closeErr != nil {
                    if removeErr := os.Remove(tmpPath); removeErr != nil {
                        slog.Warn("Failed to remove temp cache file", "path", tmpPath, "error", removeErr)
                    }
                    slog.Error("Error closing temp cache file", "path", tmpPath, "error", closeErr)
                } else if renameErr := os.Rename(tmpPath, fname); renameErr != nil {
                    if removeErr := os.Remove(tmpPath); removeErr != nil {
                        slog.Warn("Failed to remove temp cache file", "path", tmpPath, "error", removeErr)
                    }
                    slog.Error("Error renaming cache file", "path", fname, "error", renameErr)
                }
            }
        }
    }

    // Store in memory cache
    g.memCache.Put(fhash, resp)
    return resp
}

// ── HTTP handlers ───────────────────────────────────────────────────

func (g *GalaxyProxy) Api(c *gin.Context) {
    c.JSON(200, gin.H{
        "available_versions": gin.H{
            "v1": "v1/",
            "v2": "v2/",
            "v3": "v3/",
        },
        "current_version": "v1",
        "description":     "Galaxy Proxy",
    })
}

func (g *GalaxyProxy) MetricsHandler(c *gin.Context) {
    snap := g.metrics.snapshot()

    upstreamCount := snap.UpstreamSuccess + snap.UpstreamClientErrors + snap.UpstreamServerErrors + snap.UpstreamNoResponse
    var avgUpstreamTime float64
    if upstreamCount > 0 {
        avgUpstreamTime = snap.TotalUpstreamResponseTime / float64(upstreamCount)
    }

    var avgCacheHitTime float64
    if snap.CacheHits > 0 {
        avgCacheHitTime = snap.TotalCacheHitResponseTime / float64(snap.CacheHits)
    }

    // Build Prometheus-format metrics using strings.Builder (avoids large fmt.Sprintf allocation)
    var b strings.Builder
    b.Grow(2048)

    writeCounter := func(name, help string, value int64) {
        b.WriteString("# HELP ")
        b.WriteString(name)
        b.WriteByte(' ')
        b.WriteString(help)
        b.WriteByte('\n')
        b.WriteString("# TYPE ")
        b.WriteString(name)
        b.WriteString(" counter\n")
        b.WriteString(name)
        b.WriteByte(' ')
        b.WriteString(strconv.FormatInt(value, 10))
        b.WriteByte('\n')
    }

    writeGauge := func(name, help string, value float64) {
        b.WriteString("# HELP ")
        b.WriteString(name)
        b.WriteByte(' ')
        b.WriteString(help)
        b.WriteByte('\n')
        b.WriteString("# TYPE ")
        b.WriteString(name)
        b.WriteString(" gauge\n")
        b.WriteString(name)
        b.WriteByte(' ')
        b.WriteString(strconv.FormatFloat(value, 'f', 4, 64))
        b.WriteByte('\n')
    }

    writeCounter("galaxy_proxy_cache_hits_total", "Total number of cache hits", snap.CacheHits)
    writeCounter("galaxy_proxy_cache_expires_total", "Total number of expired cache entries", snap.CacheExpires)
    writeCounter("galaxy_proxy_cache_misses_total", "Total number of cache misses", snap.CacheMisses)
    writeCounter("galaxy_proxy_upstream_success_total", "Total number of successful upstream responses (HTTP 200)", snap.UpstreamSuccess)
    writeCounter("galaxy_proxy_upstream_server_4XX_errors_total", "Total number of upstream client errors (HTTP 400-499)", snap.UpstreamClientErrors)
    writeCounter("galaxy_proxy_upstream_server_5XX_errors_total", "Total number of upstream server errors (HTTP 500-599)", snap.UpstreamServerErrors)
    writeCounter("galaxy_proxy_upstream_no_response_total", "Total number of failed upstream requests (no response)", snap.UpstreamNoResponse)
    writeGauge("galaxy_proxy_upstream_avg_response_seconds", "Average response time for upstream requests in seconds", avgUpstreamTime)
    writeGauge("galaxy_proxy_cache_hit_avg_response_seconds", "Average response time for cache hits in seconds", avgCacheHitTime)

    c.String(200, b.String())
}

func (g *GalaxyProxy) GalaxyHandler(c *gin.Context) {

    /*************************************
     * Handle api/v1/roles/*
     ************************************/

    // validate api token if configured
    if len(g.apiToken) > 0 {
        authHeader := c.GetHeader("Authorization")
        expectedAuth := "Token " + g.apiToken
        if subtle.ConstantTimeCompare([]byte(authHeader), []byte(expectedAuth)) != 1 {
            c.Header("WWW-Authenticate", `Token realm="Ascender Galaxy Proxy"`)
            c.JSON(401, gin.H{"error": "Unauthorized"})
            return
        }
    }

    // Use the configured URL as the external host for URL rewriting
    rhost := g.baseURL

    // get the upstream response
    urlPath := c.Request.URL.Path
    slog.Info("Upstream request", "rhost", rhost, "path", urlPath)
    uresp := g.getUpstreamURL(rhost, urlPath, c.Request.URL.Query())

    // set response headers directly from http.Header (no JSON deserialization needed)
    for k, values := range uresp.Headers {
        if k != "Authorization" && !isHopByHopHeader(k) {
            for _, v := range values {
                c.Writer.Header().Add(k, v)
            }
        }
    }

    // return the body
    c.Data(uresp.Code, "application/json", uresp.Body)
}

func (g *GalaxyProxy) ArtifactHandler(c *gin.Context) {

    // validate api token if configured
    if len(g.apiToken) > 0 {
        authHeader := c.GetHeader("Authorization")
        expectedAuth := "Token " + g.apiToken
        if subtle.ConstantTimeCompare([]byte(authHeader), []byte(expectedAuth)) != 1 {
            c.Header("WWW-Authenticate", `Token realm="Ascender Galaxy Proxy"`)
            c.JSON(401, gin.H{"error": "Unauthorized"})
            return
        }
    }

    // Extract filename safely using filepath.Base to prevent path traversal attacks
    baseName := filepath.Base(c.Request.URL.Path)

    // Validate filename - should not be empty or special directory references
    if baseName == "" || baseName == "." || baseName == ".." {
        slog.Warn("Invalid artifact filename", "path", c.Request.URL.Path)
        c.JSON(400, gin.H{"error": "Invalid artifact filename"})
        return
    }

    // Include path hash in cache filename to prevent collisions across routes
    artifactFilename := formatHashKey(xxhash.Sum64String(c.Request.URL.Path)) + "_" + baseName

    // define the cache filename
    fdir := filepath.Join(g.cacheDir, "download")
    fpath := filepath.Join(fdir, artifactFilename)

    // check cache file
    if fileInfo, err := os.Stat(fpath); err == nil {
        fileAge := time.Since(fileInfo.ModTime())
        if fileAge > g.artifactCacheExpire {
            slog.Info("Artifact cache expired", "path", fpath)
            _ = os.Remove(fpath)
        } else {
            slog.Info("Artifact cache hit", "path", fpath)
            c.File(fpath)
            return
        }
    }

    // download the file
    downloadURL := g.upstreamBaseURL + c.Request.URL.Path
    slog.Info("Artifact cache miss", "url", downloadURL, "path", fpath)

    resp, err := g.httpClient.Get(downloadURL)
    if err != nil {
        slog.Error("Error downloading artifact", "url", downloadURL, "error", err)
        c.JSON(500, gin.H{"error": "Failed to download artifact"})
        return
    }
    defer func() { _ = resp.Body.Close() }()

    // check HTTP status
    if resp.StatusCode != 200 {
        slog.Warn("Upstream returned error for artifact", "status", resp.StatusCode, "url", downloadURL)
        c.JSON(resp.StatusCode, gin.H{"error": "Upstream error"})
        return
    }

    // Max size of Galaxy artifacts is currently 20MB; limit to 40MB as headroom
    const maxFileSize int64 = 40 * 1024 * 1024

    // Check Content-Length for early size rejection when available
    contentLength := int64(-1)
    if cl := resp.Header.Get("Content-Length"); cl != "" {
        if parsed, parseErr := strconv.ParseInt(cl, 10, 64); parseErr == nil {
            contentLength = parsed
        }
    }
    if contentLength > maxFileSize {
        slog.Warn("Artifact exceeds size limit (Content-Length)",
            "url", downloadURL, "size", contentLength, "maxSize", maxFileSize)
        c.JSON(413, gin.H{"error": "Artifact exceeds maximum size limit"})
        return
    }

    // create the cache file - write to temp file first for atomic rename
    tmpFile, err := os.CreateTemp(fdir, ".tmp-")
    if err != nil {
        slog.Error("Error creating temp cache file", "dir", fdir, "error", err)
        c.JSON(500, gin.H{"error": "Failed to create cache file"})
        return
    }
    tmpPath := tmpFile.Name()

    if contentLength >= 0 {
        // Known size within limits: stream to client while writing to cache via TeeReader.
        // Wrap with LimitReader to enforce the bound even if the upstream sends more bytes
        // than advertised in Content-Length.
        tee := io.TeeReader(io.LimitReader(resp.Body, contentLength), tmpFile)

        if ct := resp.Header.Get("Content-Type"); ct != "" {
            c.Header("Content-Type", ct)
        }
        c.Header("Content-Length", strconv.FormatInt(contentLength, 10))
        c.Status(200)

        if _, copyErr := io.Copy(c.Writer, tee); copyErr != nil {
            _ = tmpFile.Close()
            if removeErr := os.Remove(tmpPath); removeErr != nil {
                slog.Warn("Failed to remove temp cache file", "path", tmpPath, "error", removeErr)
            }
            slog.Error("Error streaming artifact", "url", downloadURL, "error", copyErr)
            return
        }
        if closeErr := tmpFile.Close(); closeErr != nil {
            if removeErr := os.Remove(tmpPath); removeErr != nil {
                slog.Warn("Failed to remove temp cache file", "path", tmpPath, "error", removeErr)
            }
            slog.Error("Error closing temp cache file", "path", tmpPath, "error", closeErr)
            return
        }

        if renameErr := os.Rename(tmpPath, fpath); renameErr != nil {
            if removeErr := os.Remove(tmpPath); removeErr != nil {
                slog.Warn("Failed to remove temp cache file", "path", tmpPath, "error", removeErr)
            }
            slog.Error("Error caching artifact", "path", fpath, "error", renameErr)
        }
        return
    }

    // Unknown Content-Length: buffer to temp file, verify size, then serve
    written, copyErr := io.Copy(tmpFile, io.LimitReader(resp.Body, maxFileSize+1))
    closeErr := tmpFile.Close()

    if copyErr != nil {
        if removeErr := os.Remove(tmpPath); removeErr != nil {
            slog.Warn("Failed to remove temp cache file", "path", tmpPath, "error", removeErr)
        }
        slog.Error("Error downloading artifact", "url", downloadURL, "error", copyErr)
        c.JSON(502, gin.H{"error": "Failed to download artifact"})
        return
    }
    if closeErr != nil {
        if removeErr := os.Remove(tmpPath); removeErr != nil {
            slog.Warn("Failed to remove temp cache file", "path", tmpPath, "error", removeErr)
        }
        slog.Error("Error closing temp cache file", "path", tmpPath, "error", closeErr)
        c.JSON(500, gin.H{"error": "Failed to cache artifact"})
        return
    }

    if written > maxFileSize {
        if removeErr := os.Remove(tmpPath); removeErr != nil {
            slog.Warn("Failed to remove temp cache file", "path", tmpPath, "error", removeErr)
        }
        slog.Warn("Artifact exceeds size limit", "url", downloadURL, "maxSize", maxFileSize)
        c.JSON(413, gin.H{"error": "Artifact exceeds maximum size limit"})
        return
    }

    // Atomically rename temp file to final cache path
    if err := os.Rename(tmpPath, fpath); err != nil {
        slog.Error("Error caching artifact", "path", fpath, "error", err)
        if removeErr := os.Remove(tmpPath); removeErr != nil {
            slog.Warn("Failed to remove temp cache file", "path", tmpPath, "error", removeErr)
        }
        c.JSON(500, gin.H{"error": "Failed to cache artifact"})
        return
    }

    // return the file
    c.File(fpath)
}

// flushMetricsBackground periodically flushes metrics to disk.
// It exits cleanly when the done channel is closed.
func (g *GalaxyProxy) flushMetricsBackground() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            g.flushMetricsToDisk()
        case <-g.done:
            return
        }
    }
}

func main() {
    var port string
    flag.StringVar(&port, "port", "80", "Port")
    flag.Parse()

    // Validate URL
    baseURL, err := getBaseURL()
    if err != nil {
        slog.Error("Invalid URL configuration", "error", err)
        os.Exit(1)
    }
    slog.Info("Base URL configured", "baseURL", baseURL)

    // Set Gin mode based on DEBUG env var
    debugMode := os.Getenv("DEBUG")
    if debugMode == "1" || strings.EqualFold(debugMode, "true") {
        gin.SetMode(gin.DebugMode)
        slog.Info("Running in debug mode")
    } else {
        gin.SetMode(gin.ReleaseMode)
        slog.Info("Running in release mode")
    }

    // Build the GalaxyProxy with all dependencies injected explicitly
    cacheDir := defaultCacheDir
    upstreamBase := getUpstreamBaseURL()
    galaxyProxy := &GalaxyProxy{
        upstreamBaseURL:       upstreamBase,
        upstreamBaseURLBytes:  []byte(upstreamBase),
        upstreamBaseURLInJSON: append([]byte(`"`), []byte(upstreamBase)...),
        baseURL:              baseURL,
        apiToken:              getAPIToken(),
        cacheDir:             cacheDir,
        metricsFile:          filepath.Join(cacheDir, ".metrics"),
        memCache:             newLRUCache(getMemCacheSize()),
        queryCacheExpire:     getCacheExpire("QUERY_CACHE_EXPIRE", 1),
        artifactCacheExpire:  getCacheExpire("ARTIFACT_CACHE_EXPIRE", 30),
        httpClient:           newHTTPClient(os.Getenv("HTTP_PROXY")),
        done:                 make(chan struct{}),
    }

    r := gin.Default()

    // load metrics from file
    galaxyProxy.loadMetrics()

    // set trusted proxies from environment variable, or default to localhost
    trustedProxies := []string{"127.0.0.1"} // Always trust localhost
    if proxies := os.Getenv("TRUSTED_PROXIES"); len(proxies) > 0 {
        for _, proxy := range strings.Split(proxies, ",") {
            if trimmedProxy := strings.TrimSpace(proxy); trimmedProxy != "" {
                trustedProxies = append(trustedProxies, trimmedProxy)
            }
        }
        slog.Info("Trusting proxies from TRUSTED_PROXIES", "proxies", trustedProxies)
    }
    if err := r.SetTrustedProxies(trustedProxies); err != nil {
        slog.Error("Invalid trusted proxies configuration", "error", err)
        os.Exit(1)
    }

    r.RedirectTrailingSlash = true

    // Limit request body size to 1MB (defensive; this proxy handles GET requests)
    r.Use(func(c *gin.Context) {
        c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
        c.Next()
    })

    // clear cache on startup if requested
    if shouldClearCacheOnStart() {
        slog.Info("Clearing cache directory...")
        if err := galaxyProxy.clearCacheDirectory(); err != nil {
            slog.Warn("Failed to clear cache directory", "error", err)
        } else {
            slog.Info("Cache directory cleared")
        }
        // reset metrics when cache is cleared
        galaxyProxy.metrics.reset()
        galaxyProxy.saveMetrics()
        galaxyProxy.memCache.Clear()
    }

    // ensure cache directories exist (0700 to restrict access to proxy user)
    if err := os.MkdirAll(cacheDir, 0700); err != nil {
        slog.Error("Failed to create cache directory", "path", cacheDir, "error", err)
        os.Exit(1)
    }
    downloadDir := filepath.Join(cacheDir, "download")
    if err := os.MkdirAll(downloadDir, 0700); err != nil {
        slog.Error("Failed to create download cache directory", "path", downloadDir, "error", err)
        os.Exit(1)
    }
    slog.Info("Cache directories initialized", "cache", cacheDir, "download", downloadDir)

    // start background metrics flush goroutine (after optional cache/metrics reset to avoid logical races)
    go galaxyProxy.flushMetricsBackground()

    // root
    r.GET("/api/", galaxyProxy.Api)
    r.GET("/metrics", galaxyProxy.MetricsHandler)
    r.GET("/healthz", func(c *gin.Context) {
        c.String(200, "OK")
    })

    // Register upstream handler routes
    galaxyRoutes := []string{
        // v1
        "/api/v1/",
        "/api/v1/users/",
        "/api/v1/users/:userid/",
        "/api/v1/namespaces/",
        "/api/v1/namespaces/:namespaceid/",
        "/api/v1/namespaces/:namespaceid/content/",
        "/api/v1/namespaces/:namespaceid/owners/",
        "/api/v1/roles/",
        "/api/v1/roles/:roleid/",
        "/api/v1/roles/:roleid/versions/",
        // v2
        "/api/v2/",
        "/api/v2/collections/",
        "/api/v2/collections/:namespace/:name/",
        "/api/v2/collections/:namespace/:name/versions/",
        "/api/v2/collections/:namespace/:name/versions/:version/",
        // v3
        "/api/v3/",
        "/api/v3/collections/",
        "/api/v3/collections/:namespace/:name",
        "/api/v3/collections/:namespace/:name/",
        "/api/v3/collections/:namespace/:name/versions/",
        "/api/v3/collections/:namespace/:name/versions/:version/",
        "/api/v3/plugin/ansible/content/published/collections/index/:namespace/:name",
        "/api/v3/plugin/ansible/content/published/collections/index/:namespace/:name/",
        "/api/v3/plugin/ansible/content/published/collections/index/:namespace/:name/versions/",
        "/api/v3/plugin/ansible/content/published/collections/index/:namespace/:name/versions/:version/",
        "/api/v3/plugin/ansible/search/collection-versions/",
    }

    for _, route := range galaxyRoutes {
        r.GET(route, galaxyProxy.GalaxyHandler)
    }

    // Register artifact handler routes
    artifactRoutes := []string{
        "/api/v3/plugin/ansible/content/published/collections/artifacts/:artifact",
        "/download/:artifact",
    }

    for _, route := range artifactRoutes {
        r.GET(route, galaxyProxy.ArtifactHandler)
    }

    // Catch-all route for unproxied paths
    r.NoRoute(func(c *gin.Context) {
        slog.Info("Route not proxied", "method", c.Request.Method, "path", c.Request.URL.Path)
        c.JSON(404, gin.H{
            "error": "Route not proxied",
        })
    })

    slog.Info("Server starting",
        "app", appName, "version", appVersion, "addr", "0.0.0.0:"+port)

    srv := &http.Server{
        Addr:              "0.0.0.0:" + port,
        Handler:           r,
        ReadHeaderTimeout: 10 * time.Second,
        IdleTimeout:       120 * time.Second,
    }

    // Start server in a goroutine; send errors via channel instead of os.Exit
    serverErr := make(chan error, 1)
    go func() {
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            serverErr <- err
        }
    }()

    // Wait for interrupt signal or server error for graceful shutdown
    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

    select {
    case err := <-serverErr:
        slog.Error("Server error", "error", err)
    case sig := <-quit:
        slog.Info("Received signal, shutting down", "signal", sig)
    }

    // Signal background goroutines to stop
    close(galaxyProxy.done)

    // Give outstanding requests 30 seconds to complete
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    if err := srv.Shutdown(ctx); err != nil {
        slog.Error("Server forced to shutdown", "error", err)
    }

    // Flush pending metrics to disk before exit
    galaxyProxy.flushMetricsToDisk()
    slog.Info("Server exited")
}
