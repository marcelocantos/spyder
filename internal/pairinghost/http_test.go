// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pairinghost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestHTTPWithoutHost proves the API surfaces fail closed when the
// pairing host is not configured (e.g. init failure).
func TestHTTPWithoutHost(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	h.Mount(mux)

	for _, path := range []string{"/stream/pair", "/stream/pairings"} {
		method := http.MethodGet
		if path == "/stream/pair" {
			method = http.MethodPost
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: got %d want 503", method, path, rr.Code)
		}
	}
}

// TestHTTPPairings lists peers after a store Put via a live Host
// that never dials the relay (New only constructs a Pairer).
func TestHTTPPairings(t *testing.T) {
	dir := t.TempDir()
	host, err := New(t.Context(), Config{
		Relay:        "https://relay.example.com",
		IdentityPath: filepath.Join(dir, "identity.json"),
		RecordsPath:  filepath.Join(dir, "pairings.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })

	h := &Handler{Host: host}
	mux := http.NewServeMux()
	h.Mount(mux)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/stream/pairings", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["relay"] != "https://relay.example.com" {
		t.Fatalf("relay: %v", body["relay"])
	}
	if body["instance_id"] == "" {
		t.Fatal("missing instance_id")
	}
	peers, ok := body["peers"].([]any)
	if !ok || len(peers) != 0 {
		t.Fatalf("peers: %v", body["peers"])
	}
}

// TestQRRouteRendersToken checks that GET …/qr writes a text body
// containing the rendezvous token (QR Unicode art or fallback URL line).
func TestQRRouteRendersToken(t *testing.T) {
	dir := t.TempDir()
	host, err := New(t.Context(), Config{
		Relay:        "https://relay.example.com",
		IdentityPath: filepath.Join(dir, "identity.json"),
		RecordsPath:  filepath.Join(dir, "pairings.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })

	// Inject a fake session without dialing Accept.
	const token = "test-rendezvous-token-for-qr-surface"
	host.mu.Lock()
	host.sessions["pair-test"] = &Session{
		ID:     "pair-test",
		Token:  token,
		Status: "pending",
	}
	host.mu.Unlock()

	h := &Handler{Host: host}
	mux := http.NewServeMux()
	h.Mount(mux)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/stream/pair/pair-test/qr", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type: %s", ct)
	}
	if !strings.Contains(rr.Body.String(), token) {
		t.Fatalf("QR body missing token:\n%s", rr.Body.String())
	}
}
