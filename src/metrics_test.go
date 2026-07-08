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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── Metrics struct ──────────────────────────────────────────────────

func TestMetrics_Snapshot(t *testing.T) {
	var m Metrics
	m.CacheHits.Store(10)
	m.CacheMisses.Store(5)
	m.UpstreamSuccess.Store(3)
	m.TotalUpstreamResponseTimeMicros.Store(2000000) // 2 seconds

	s := m.snapshot()
	if s.CacheHits != 10 {
		t.Errorf("CacheHits = %d, want 10", s.CacheHits)
	}
	if s.CacheMisses != 5 {
		t.Errorf("CacheMisses = %d, want 5", s.CacheMisses)
	}
	if s.UpstreamSuccess != 3 {
		t.Errorf("UpstreamSuccess = %d, want 3", s.UpstreamSuccess)
	}
	if s.TotalUpstreamResponseTime != 2.0 {
		t.Errorf("TotalUpstreamResponseTime = %f, want 2.0", s.TotalUpstreamResponseTime)
	}
}

func TestMetrics_Restore(t *testing.T) {
	var m Metrics
	s := metricsSnapshot{
		CacheHits:                 42,
		CacheExpires:              7,
		CacheMisses:               3,
		UpstreamSuccess:           100,
		UpstreamClientErrors:      2,
		UpstreamServerErrors:      1,
		UpstreamNoResponse:        0,
		TotalUpstreamResponseTime: 1.5,
		TotalCacheHitResponseTime: 0.01,
	}
	m.restore(s)

	if m.CacheHits.Load() != 42 {
		t.Errorf("CacheHits = %d, want 42", m.CacheHits.Load())
	}
	if m.CacheExpires.Load() != 7 {
		t.Errorf("CacheExpires = %d, want 7", m.CacheExpires.Load())
	}
	if m.UpstreamSuccess.Load() != 100 {
		t.Errorf("UpstreamSuccess = %d, want 100", m.UpstreamSuccess.Load())
	}
	if m.TotalUpstreamResponseTimeMicros.Load() != 1500000 {
		t.Errorf("TotalUpstreamResponseTimeMicros = %d, want 1500000", m.TotalUpstreamResponseTimeMicros.Load())
	}
}

func TestMetrics_Reset(t *testing.T) {
	var m Metrics
	m.CacheHits.Store(99)
	m.CacheMisses.Store(50)
	m.UpstreamSuccess.Store(25)

	m.reset()

	if m.CacheHits.Load() != 0 {
		t.Errorf("CacheHits = %d after reset, want 0", m.CacheHits.Load())
	}
	if m.CacheMisses.Load() != 0 {
		t.Errorf("CacheMisses = %d after reset, want 0", m.CacheMisses.Load())
	}
	if m.UpstreamSuccess.Load() != 0 {
		t.Errorf("UpstreamSuccess = %d after reset, want 0", m.UpstreamSuccess.Load())
	}
}

func TestMetrics_SnapshotRestoreRoundTrip(t *testing.T) {
	var m1 Metrics
	m1.CacheHits.Store(10)
	m1.CacheExpires.Store(20)
	m1.CacheMisses.Store(30)
	m1.UpstreamSuccess.Store(40)
	m1.UpstreamClientErrors.Store(50)
	m1.UpstreamServerErrors.Store(60)
	m1.UpstreamNoResponse.Store(70)
	m1.TotalUpstreamResponseTimeMicros.Store(1000000)
	m1.TotalCacheHitResponseTimeMicros.Store(500000)

	s := m1.snapshot()

	var m2 Metrics
	m2.restore(s)

	if m2.CacheHits.Load() != m1.CacheHits.Load() {
		t.Error("round-trip mismatch for CacheHits")
	}
	if m2.CacheExpires.Load() != m1.CacheExpires.Load() {
		t.Error("round-trip mismatch for CacheExpires")
	}
	if m2.CacheMisses.Load() != m1.CacheMisses.Load() {
		t.Error("round-trip mismatch for CacheMisses")
	}
}

// ── Metrics persistence ─────────────────────────────────────────────

func TestSaveAndLoadMetrics(t *testing.T) {
	g := newTestProxy(t)

	g.metrics.CacheHits.Store(42)
	g.metrics.CacheMisses.Store(7)
	g.metrics.UpstreamSuccess.Store(100)

	g.saveMetrics()

	// Create a new proxy pointing to the same metrics file
	g2 := newTestProxy(t)
	g2.metricsFile = g.metricsFile
	g2.loadMetrics()

	if g2.metrics.CacheHits.Load() != 42 {
		t.Errorf("CacheHits = %d, want 42", g2.metrics.CacheHits.Load())
	}
	if g2.metrics.CacheMisses.Load() != 7 {
		t.Errorf("CacheMisses = %d, want 7", g2.metrics.CacheMisses.Load())
	}
	if g2.metrics.UpstreamSuccess.Load() != 100 {
		t.Errorf("UpstreamSuccess = %d, want 100", g2.metrics.UpstreamSuccess.Load())
	}
}

func TestLoadMetrics_MissingFile(t *testing.T) {
	g := newTestProxy(t)
	g.metricsFile = filepath.Join(t.TempDir(), "nonexistent")

	// Should not panic, just silently skip
	g.loadMetrics()

	if g.metrics.CacheHits.Load() != 0 {
		t.Error("expected zero metrics when file missing")
	}
}

func TestFlushMetricsToDisk_NoPending(t *testing.T) {
	g := newTestProxy(t)

	// No pending writes, should be a no-op
	g.flushMetricsToDisk()

	_, err := os.Stat(g.metricsFile)
	if !os.IsNotExist(err) {
		t.Error("expected no metrics file when no pending writes")
	}
}

func TestFlushMetricsToDisk_WithPending(t *testing.T) {
	g := newTestProxy(t)
	g.metrics.CacheHits.Store(5)
	g.pendingMetricWrites.Store(1)

	g.flushMetricsToDisk()

	data, err := os.ReadFile(g.metricsFile)
	if err != nil {
		t.Fatalf("expected metrics file to exist: %v", err)
	}
	if !strings.Contains(string(data), `"CacheHits":5`) {
		t.Errorf("metrics file content unexpected: %s", data)
	}
}

// ── incrementMetric / addResponseTime ───────────────────────────────

func TestIncrementMetric(t *testing.T) {
	g := newTestProxy(t)

	g.incrementMetric("hit")
	g.incrementMetric("hit")
	g.incrementMetric("miss")
	g.incrementMetric("expire")
	g.incrementMetric("upstream_success")
	g.incrementMetric("upstream_client_error")
	g.incrementMetric("upstream_server_error")
	g.incrementMetric("upstream_no_response")

	if g.metrics.CacheHits.Load() != 2 {
		t.Errorf("CacheHits = %d, want 2", g.metrics.CacheHits.Load())
	}
	if g.metrics.CacheMisses.Load() != 1 {
		t.Errorf("CacheMisses = %d, want 1", g.metrics.CacheMisses.Load())
	}
	if g.metrics.CacheExpires.Load() != 1 {
		t.Errorf("CacheExpires = %d, want 1", g.metrics.CacheExpires.Load())
	}
	if g.metrics.UpstreamSuccess.Load() != 1 {
		t.Errorf("UpstreamSuccess = %d, want 1", g.metrics.UpstreamSuccess.Load())
	}
	if g.metrics.UpstreamClientErrors.Load() != 1 {
		t.Errorf("UpstreamClientErrors = %d, want 1", g.metrics.UpstreamClientErrors.Load())
	}
	if g.metrics.UpstreamServerErrors.Load() != 1 {
		t.Errorf("UpstreamServerErrors = %d, want 1", g.metrics.UpstreamServerErrors.Load())
	}
	if g.metrics.UpstreamNoResponse.Load() != 1 {
		t.Errorf("UpstreamNoResponse = %d, want 1", g.metrics.UpstreamNoResponse.Load())
	}
	if g.pendingMetricWrites.Load() != 8 {
		t.Errorf("pendingMetricWrites = %d, want 8", g.pendingMetricWrites.Load())
	}
}

func TestIncrementMetric_Unknown(t *testing.T) {
	g := newTestProxy(t)
	g.incrementMetric("unknown_metric")
	// Should still increment pending writes counter
	if g.pendingMetricWrites.Load() != 1 {
		t.Errorf("pendingMetricWrites = %d, want 1", g.pendingMetricWrites.Load())
	}
}

func TestAddResponseTime(t *testing.T) {
	g := newTestProxy(t)

	g.addResponseTime("upstream", 1.5)
	g.addResponseTime("cache_hit", 0.001)

	if g.metrics.TotalUpstreamResponseTimeMicros.Load() != 1500000 {
		t.Errorf("TotalUpstreamResponseTimeMicros = %d, want 1500000",
			g.metrics.TotalUpstreamResponseTimeMicros.Load())
	}
	if g.metrics.TotalCacheHitResponseTimeMicros.Load() != 1000 {
		t.Errorf("TotalCacheHitResponseTimeMicros = %d, want 1000",
			g.metrics.TotalCacheHitResponseTimeMicros.Load())
	}
}

// ── flushMetricsBackground ──────────────────────────────────────────

func TestFlushMetricsBackground_StopsOnDone(t *testing.T) {
	g := newTestProxy(t)

	done := make(chan struct{})
	go func() {
		g.flushMetricsBackground()
		close(done)
	}()

	// Close the done channel to stop the background goroutine
	close(g.done)

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("flushMetricsBackground did not stop within timeout")
	}
}
