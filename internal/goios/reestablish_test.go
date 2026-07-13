// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTunnelDaemon implements the go-ios tunnel registry HTTP surface
// used by T89.1: GET /tunnel/{udid} and DELETE /tunnel/{udid}.
type fakeTunnelDaemon struct {
	mu      sync.Mutex
	live    bool // after DELETE, flips true on next rebuild
	deletes atomic.Int32
	gets    atomic.Int32
	// rebuildAfterDeletes: tunnel becomes live after this many DELETEs.
	rebuildAfterDeletes int32
}

func (f *fakeTunnelDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	udid := strings.TrimPrefix(r.URL.Path, "/tunnel/")
	if udid == "" || udid == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		f.deletes.Add(1)
		f.mu.Lock()
		// After DELETE, tunnel is gone until rebuild threshold.
		f.live = f.deletes.Load() >= f.rebuildAfterDeletes && f.rebuildAfterDeletes > 0
		// Default: become live after first DELETE (daemon UpdateTunnels).
		if f.rebuildAfterDeletes == 0 {
			f.live = true
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	case http.MethodGet:
		f.gets.Add(1)
		f.mu.Lock()
		live := f.live
		f.mu.Unlock()
		if !live {
			// Match go-ios: 404 + empty body → "unexpected end of JSON".
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"address":          "::1",
			"rsdPort":          1234,
			"udid":             udid,
			"userspaceTun":     false,
			"userspaceTunPort": 0,
		})
		return
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func startFake(t *testing.T, f *fakeTunnelDaemon) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: f}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	h, pStr, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(pStr)
	return h, p
}

func TestReestablishTunnel_StaleThenLive(t *testing.T) {
	prevI, prevT := ReestablishPollInterval, ReestablishTimeout
	ReestablishPollInterval = 5 * time.Millisecond
	ReestablishTimeout = 2 * time.Second
	t.Cleanup(func() {
		ReestablishPollInterval = prevI
		ReestablishTimeout = prevT
	})

	f := &fakeTunnelDaemon{live: false, rebuildAfterDeletes: 0}
	host, port := startFake(t, f)
	r := New(host, port)

	if err := r.ReestablishTunnel("UDID-A"); err != nil {
		t.Fatalf("re-establish: %v", err)
	}
	if f.deletes.Load() < 1 {
		t.Fatalf("expected DELETE; got %d", f.deletes.Load())
	}
	// Tunnel-info should now succeed.
	info, err := r.tunnelInfoWithRetry("UDID-A")
	if err != nil {
		t.Fatalf("tunnel info after re-establish: %v", err)
	}
	if info.Address != "::1" || info.RsdPort != 1234 {
		t.Fatalf("unexpected tunnel: %+v", info)
	}
}

func TestReestablishTunnel_TimeoutWhenNeverRebuilds(t *testing.T) {
	prevI, prevT := ReestablishPollInterval, ReestablishTimeout
	ReestablishPollInterval = 5 * time.Millisecond
	ReestablishTimeout = 40 * time.Millisecond
	t.Cleanup(func() {
		ReestablishPollInterval = prevI
		ReestablishTimeout = prevT
	})

	// Never goes live: rebuildAfterDeletes = 999, only one DELETE happens.
	f := &fakeTunnelDaemon{live: false, rebuildAfterDeletes: 999}
	host, port := startFake(t, f)
	r := New(host, port)

	err := r.ReestablishTunnel("UDID-B")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout wording; got %v", err)
	}
	// Must not spin forever: finite DELETEs (exactly one).
	if f.deletes.Load() != 1 {
		t.Fatalf("expected exactly 1 DELETE; got %d", f.deletes.Load())
	}
}

// oracle: consumer path would call tunnelInfo → fail → reestablish → retry.
func TestTunnelInfoPath_StaleThenReestablishThenOK(t *testing.T) {
	prevI, prevT := ReestablishPollInterval, ReestablishTimeout
	prevAttempts := TunnelInfoRetryAttempts
	prevBackoff := tunnelInfoBackoffs
	ReestablishPollInterval = 5 * time.Millisecond
	ReestablishTimeout = 2 * time.Second
	TunnelInfoRetryAttempts = 1 // force single attempt so first fail is immediate
	tunnelInfoBackoffs = []time.Duration{time.Millisecond}
	t.Cleanup(func() {
		ReestablishPollInterval = prevI
		ReestablishTimeout = prevT
		TunnelInfoRetryAttempts = prevAttempts
		tunnelInfoBackoffs = prevBackoff
	})

	f := &fakeTunnelDaemon{live: false, rebuildAfterDeletes: 0}
	host, port := startFake(t, f)
	r := New(host, port)

	// First: stale
	_, err := r.tunnelInfoWithRetry("UDID-C")
	if err == nil {
		t.Fatal("expected initial tunnel-info failure")
	}
	// Re-establish
	if err := r.ReestablishTunnel("UDID-C"); err != nil {
		t.Fatalf("re-establish: %v", err)
	}
	// Retry
	info, err := r.tunnelInfoWithRetry("UDID-C")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if info.RsdPort != 1234 {
		t.Fatalf("got %+v", info)
	}
}
