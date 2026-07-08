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
	"net/http"
	"testing"
	"time"
)

// ── redactProxyURLForLogging ────────────────────────────────────────

func TestRedactProxyURLForLogging(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple http", "http://proxy.example.com:8080", "http://proxy.example.com:8080"},
		{"with userinfo", "http://user:pass@proxy.example.com:8080", "http://proxy.example.com:8080"},
		{"https", "https://proxy.example.com", "https://proxy.example.com"},
		{"no host", "http://", "<invalid>"},
		{"invalid url", "://bad", "<invalid>"},
		{"empty string", "", "<invalid>"},
		{"just path", "/some/path", "<invalid>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := redactProxyURLForLogging(tt.input)
			if result != tt.expected {
				t.Errorf("redactProxyURLForLogging(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// ── newHTTPClient ───────────────────────────────────────────────────

func TestNewHTTPClient_NoProxy(t *testing.T) {
	client := newHTTPClient("")
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Timeout != 5*time.Minute {
		t.Errorf("expected 5m timeout, got %v", client.Timeout)
	}
}

func TestNewHTTPClient_ValidProxy(t *testing.T) {
	client := newHTTPClient("http://proxy.example.com:8080")
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected client.Transport to be *http.Transport, got %T", client.Transport)
	}
	if transport.Proxy == nil {
		t.Error("expected proxy function to be set")
	}
}

func TestNewHTTPClient_InvalidProxy(t *testing.T) {
	// Invalid scheme - should not panic, should return client without proxy
	client := newHTTPClient("ftp://proxy.example.com")
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

// ── RawBytes JSON marshaling ────────────────────────────────────────

func TestRawBytes_MarshalJSON(t *testing.T) {
	rb := RawBytes("hello world")
	data, err := json.Marshal(rb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != `"hello world"` {
		t.Errorf("got %s, want %s", data, `"hello world"`)
	}
}

func TestRawBytes_UnmarshalJSON(t *testing.T) {
	var rb RawBytes
	err := json.Unmarshal([]byte(`"test data"`), &rb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(rb) != "test data" {
		t.Errorf("got %q, want %q", string(rb), "test data")
	}
}

func TestRawBytes_UnmarshalJSON_Invalid(t *testing.T) {
	var rb RawBytes
	err := json.Unmarshal([]byte(`123`), &rb)
	if err == nil {
		t.Error("expected error for non-string JSON")
	}
}

func TestRawBytes_RoundTrip(t *testing.T) {
	original := RawBytes(`{"key":"value"}`)
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var decoded RawBytes
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if string(decoded) != string(original) {
		t.Errorf("round-trip mismatch: got %q, want %q", string(decoded), string(original))
	}
}

// ── isHopByHopHeader ────────────────────────────────────────────────

func TestIsHopByHopHeader(t *testing.T) {
	hopHeaders := []string{
		"Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "TE", "Trailer",
		"Transfer-Encoding", "Upgrade", "Content-Length",
	}
	for _, h := range hopHeaders {
		if !isHopByHopHeader(h) {
			t.Errorf("isHopByHopHeader(%q) = false, want true", h)
		}
	}

	nonHopHeaders := []string{
		"Content-Type", "Accept", "Authorization",
		"X-Custom-Header", "Cache-Control",
	}
	for _, h := range nonHopHeaders {
		if isHopByHopHeader(h) {
			t.Errorf("isHopByHopHeader(%q) = true, want false", h)
		}
	}
}

// ── formatHashKey ───────────────────────────────────────────────────

func TestFormatHashKey(t *testing.T) {
	tests := []struct {
		name     string
		input    uint64
		expected string
	}{
		{"zero", 0, "0000000000000000"},
		{"small", 255, "00000000000000ff"},
		{"large", 0xFFFFFFFFFFFFFFFF, "ffffffffffffffff"},
		{"mid", 0x123456789ABCDEF0, "123456789abcdef0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatHashKey(tt.input)
			if result != tt.expected {
				t.Errorf("formatHashKey(%d) = %q, want %q", tt.input, result, tt.expected)
			}
			if len(result) != 16 {
				t.Errorf("formatHashKey(%d) length = %d, want 16", tt.input, len(result))
			}
		})
	}
}

// ── rewriteBodyURLs ─────────────────────────────────────────────────

func TestRewriteBodyURLs(t *testing.T) {
	g := newTestProxy(t)

	tests := []struct {
		name     string
		body     string
		rhost    string
		expected string
	}{
		{
			"replaces upstream URL in JSON string context",
			`{"href":"https://galaxy.ansible.com/api/v3/collections/"}`,
			"https://proxy.example.com",
			`{"href":"https://proxy.example.com/api/v3/collections/"}`,
		},
		{
			"no upstream URL present",
			`{"href":"https://other.example.com/api/v3/"}`,
			"https://proxy.example.com",
			`{"href":"https://other.example.com/api/v3/"}`,
		},
		{
			"multiple replacements",
			`{"a":"https://galaxy.ansible.com/x","b":"https://galaxy.ansible.com/y"}`,
			"https://proxy.example.com",
			`{"a":"https://proxy.example.com/x","b":"https://proxy.example.com/y"}`,
		},
		{
			"empty body",
			"",
			"https://proxy.example.com",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := g.rewriteBodyURLs(RawBytes(tt.body), tt.rhost)
			if string(result) != tt.expected {
				t.Errorf("rewriteBodyURLs() = %q, want %q", string(result), tt.expected)
			}
		})
	}
}

// ── GalaxyResponse JSON serialization ───────────────────────────────

func TestGalaxyResponse_JSONRoundTrip(t *testing.T) {
	original := GalaxyResponse{
		Code: 200,
		Url:  "https://galaxy.ansible.com/api/v1/roles/",
		Headers: http.Header{
			"Content-Type": {"application/json"},
			"X-Custom":     {"val1", "val2"},
		},
		Body:    RawBytes(`{"count":1}`),
		Fetched: "2026-01-01T00:00:00Z",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded GalaxyResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.Code != original.Code {
		t.Errorf("Code = %d, want %d", decoded.Code, original.Code)
	}
	if decoded.Url != original.Url {
		t.Errorf("Url = %q, want %q", decoded.Url, original.Url)
	}
	if string(decoded.Body) != string(original.Body) {
		t.Errorf("Body = %q, want %q", string(decoded.Body), string(original.Body))
	}
	if decoded.Fetched != original.Fetched {
		t.Errorf("Fetched = %q, want %q", decoded.Fetched, original.Fetched)
	}
	if decoded.Headers.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type header = %q, want %q", decoded.Headers.Get("Content-Type"), "application/json")
	}
}
