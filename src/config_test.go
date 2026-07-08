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

// ── getMemCacheSize ─────────────────────────────────────────────────

func TestGetMemCacheSize(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected int
	}{
		{"default", "", 2000},
		{"custom", "500", 500},
		{"invalid", "abc", 2000},
		{"negative", "-1", 2000},
		{"zero", "0", 2000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MEM_CACHE_SIZE", tt.envValue)
			result := getMemCacheSize()
			if result != tt.expected {
				t.Errorf("getMemCacheSize() = %d, want %d", result, tt.expected)
			}
		})
	}
}

// ── getUpstreamBaseURL ──────────────────────────────────────────────

func TestGetUpstreamBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected string
	}{
		{"default", "", "https://galaxy.ansible.com"},
		{"custom valid", "https://my-galaxy.example.com", "https://my-galaxy.example.com"},
		{"trailing slash removed", "https://my-galaxy.example.com/", "https://my-galaxy.example.com"},
		{"http scheme", "http://galaxy.local:8080", "http://galaxy.local:8080"},
		{"invalid scheme", "ftp://galaxy.example.com", "https://galaxy.ansible.com"},
		{"no scheme", "galaxy.example.com", "https://galaxy.ansible.com"},
		{"empty host", "https://", "https://galaxy.ansible.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("UPSTREAM_BASEURL", tt.envValue)
			result := getUpstreamBaseURL()
			if result != tt.expected {
				t.Errorf("getUpstreamBaseURL() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// ── getCacheExpire ──────────────────────────────────────────────────

func TestGetCacheExpire(t *testing.T) {
	tests := []struct {
		name        string
		envValue    string
		defaultDays int
		expected    time.Duration
	}{
		{"default no env", "", 1, 24 * time.Hour},
		{"custom value", "7", 1, 7 * 24 * time.Hour},
		{"zero", "0", 1, 0},
		{"max boundary", "365", 1, 365 * 24 * time.Hour},
		{"over max", "366", 1, 24 * time.Hour},
		{"negative", "-1", 1, 24 * time.Hour},
		{"invalid", "abc", 1, 24 * time.Hour},
		{"default 30", "", 30, 30 * 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const envVar = "TEST_CACHE_EXPIRE"
			t.Setenv(envVar, tt.envValue)
			result := getCacheExpire(envVar, tt.defaultDays)
			if result != tt.expected {
				t.Errorf("getCacheExpire(%q, %d) = %v, want %v", envVar, tt.defaultDays, result, tt.expected)
			}
		})
	}
}

// ── getAPIToken ─────────────────────────────────────────────────────

func TestGetAPIToken(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		t.Setenv("GALAXY_API_TOKEN", "my-secret-token")
		if got := getAPIToken(); got != "my-secret-token" {
			t.Errorf("getAPIToken() = %q, want %q", got, "my-secret-token")
		}
	})
	t.Run("unset", func(t *testing.T) {
		if got := getAPIToken(); got != "" {
			t.Errorf("getAPIToken() = %q, want empty", got)
		}
	})
}

// ── getBaseURL ──────────────────────────────────────────────────────

func TestGetBaseURL(t *testing.T) {
	tests := []struct {
		name      string
		envValue  string
		wantURL   string
		wantError bool
	}{
		{"valid https", "https://proxy.example.com", "https://proxy.example.com", false},
		{"valid http", "http://proxy.local:8080", "http://proxy.local:8080", false},
		{"trailing slash", "https://proxy.example.com/", "https://proxy.example.com", false},
		{"empty", "", "", true},
		{"invalid scheme", "ftp://proxy.example.com", "", true},
		{"no host", "https://", "", true},
		{"no scheme", "proxy.example.com", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("URL", tt.envValue)
			got, err := getBaseURL()
			if (err != nil) != tt.wantError {
				t.Errorf("getBaseURL() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if got != tt.wantURL {
				t.Errorf("getBaseURL() = %q, want %q", got, tt.wantURL)
			}
		})
	}
}

// ── shouldClearCacheOnStart ─────────────────────────────────────────

func TestShouldClearCacheOnStart(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected bool
	}{
		{"unset", "", false},
		{"one", "1", true},
		{"true lower", "true", true},
		{"true upper", "True", true},
		{"TRUE", "TRUE", true},
		{"zero", "0", false},
		{"false", "false", false},
		{"other", "yes", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CLEAR_CACHE_ON_START", tt.envValue)
			result := shouldClearCacheOnStart()
			if result != tt.expected {
				t.Errorf("shouldClearCacheOnStart() = %v, want %v", result, tt.expected)
			}
		})
	}
}
