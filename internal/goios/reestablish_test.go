// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/tunnel"
)

// fakeTunnelDaemon models the relevant HTTP surface of `ios tunnel start
// --userspace`'s registry, faithfully enough to be the class-1 oracle
// for the 🎯T89.1 re-establish primitive and 🎯T89.2's attach/detach
// wiring:
//
//   - GET /tunnel/{udid}    → the device's Tunnel as JSON, or (when
//     absent) a 404 with an EMPTY body, exactly like go-ios's
//     ServeTunnelInfo does when FindTunnel comes back empty. That empty
//     body is what makes TunnelInfoForDevice fail with "unexpected end
//     of JSON input" — the real stale symptom.
//   - DELETE /tunnel/{udid} → drops the tunnel and records the call.
//
// onGet runs (under the lock) on every GET, letting a test script the
// daemon's own 1s rebuild loop — e.g. "materialise a fresh tunnel on the
// 2nd poll after the client force-drops it".
type fakeTunnelDaemon struct {
	mu      sync.Mutex
	tunnels map[string]tunnel.Tunnel
	deletes []string
	gets    map[string]int
	onGet   func(d *fakeTunnelDaemon, udid string, getCount int)

	srv  *http.Server
	host string
	port int
}

func newFakeTunnelDaemon(t *testing.T) *fakeTunnelDaemon {
	t.Helper()
	d := &fakeTunnelDaemon{
		tunnels: map[string]tunnel.Tunnel{},
		gets:    map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel/", d.handleTunnel)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	d.srv = &http.Server{Handler: mux}
	go func() { _ = d.srv.Serve(ln) }()
	t.Cleanup(func() { _ = d.srv.Close() })

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	d.host = host
	d.port, _ = strconv.Atoi(portStr)
	return d
}

func (d *fakeTunnelDaemon) handleTunnel(w http.ResponseWriter, r *http.Request) {
	udid := strings.TrimPrefix(r.URL.Path, "/tunnel/")
	d.mu.Lock()
	if r.Method == http.MethodDelete {
		d.deletes = append(d.deletes, udid)
		delete(d.tunnels, udid)
		d.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	}
	d.gets[udid]++
	if d.onGet != nil {
		d.onGet(d, udid, d.gets[udid])
	}
	tun, ok := d.tunnels[udid]
	d.mu.Unlock()
	if !ok {
		// go-ios: empty FindTunnel → http.Error(w, "", 404) → empty body.
		http.Error(w, "", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tun)
}

func (d *fakeTunnelDaemon) deleteCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.deletes)
}

// setFastReestablish shrinks the re-establish timing knobs so timeout
// paths resolve in milliseconds, restoring them after the test.
func setFastReestablish(t *testing.T, timeout, poll time.Duration) {
	t.Helper()
	prevT, prevP := reestablishTimeout, reestablishPollInterval
	reestablishTimeout, reestablishPollInterval = timeout, poll
	t.Cleanup(func() { reestablishTimeout, reestablishPollInterval = prevT, prevP })
}

// ReestablishTunnel force-drops the device's tunnel via DELETE, then
// polls until the daemon rebuilds a live one. Here the daemon starts
// with no tunnel (the observed empty-JSON stale case) and "rebuilds" one
// on the second poll after the DELETE — the primitive must issue exactly
// one DELETE and return nil once a live tunnel appears.
func TestReestablishTunnel_ForcesDropThenWaitsForRebuild(t *testing.T) {
	setFastReestablish(t, 2*time.Second, 5*time.Millisecond)
	d := newFakeTunnelDaemon(t)
	const udid = "FAKE-STALE"

	d.onGet = func(dm *fakeTunnelDaemon, u string, n int) {
		// Simulate the daemon's 1s UpdateTunnels loop finally rebuilding
		// the tunnel two polls into the re-establish wait.
		if u == udid && n == 2 {
			dm.tunnels[udid] = tunnel.Tunnel{Address: "::fresh", RsdPort: 2, Udid: udid}
		}
	}

	r := New(d.host, d.port)
	if err := r.ReestablishTunnel(udid); err != nil {
		t.Fatalf("re-establish should have succeeded once daemon rebuilt: %v", err)
	}
	if got := d.deleteCount(); got != 1 {
		t.Errorf("expected exactly one DELETE to force-drop the zombie; got %d", got)
	}
}

// When the daemon never rebuilds (device attached but tunnel un-buildable),
// ReestablishTunnel must give up after its bounded wait and surface an
// actionable error — having still attempted the force-drop.
func TestReestablishTunnel_TimesOutWhenDaemonNeverRebuilds(t *testing.T) {
	setFastReestablish(t, 40*time.Millisecond, 5*time.Millisecond)
	d := newFakeTunnelDaemon(t)
	const udid = "FAKE-DEAD"

	err := New(d.host, d.port).ReestablishTunnel(udid)
	if err == nil {
		t.Fatal("expected error when daemon never rebuilds the tunnel")
	}
	if !strings.Contains(err.Error(), "did not rebuild") {
		t.Errorf("error should explain the daemon never rebuilt; got %v", err)
	}
	if got := d.deleteCount(); got != 1 {
		t.Errorf("expected the force-drop DELETE to have been attempted; got %d", got)
	}
}

// resolveWithRecovery is the detect → re-establish → retry-once wiring.
// Driven through the injectable seams: a stale-tunnel failure on the
// first resolve must trigger exactly one re-establish and exactly one
// retry, and the retry's success is returned.
func TestResolveWithRecovery_StaleRecoversOnSingleRetry(t *testing.T) {
	r := New("", 0)
	var resolveCalls, reCalls int32
	r.resolveFn = func(udid string) (ios.DeviceEntry, int, error) {
		if atomic.AddInt32(&resolveCalls, 1) == 1 {
			return ios.DeviceEntry{}, 17, fmt.Errorf("%w: empty tunnel-info", ErrStaleTunnel)
		}
		return ios.DeviceEntry{}, 17, nil
	}
	r.reestablishFn = func(udid string) error { atomic.AddInt32(&reCalls, 1); return nil }

	_, major, err := r.resolveWithRecovery("UDID")
	if err != nil {
		t.Fatalf("stale tunnel should recover on the automatic retry; got %v", err)
	}
	if major != 17 {
		t.Errorf("expected major 17 from the successful retry; got %d", major)
	}
	if got := atomic.LoadInt32(&resolveCalls); got != 2 {
		t.Errorf("expected exactly 2 resolves (initial + one retry); got %d", got)
	}
	if got := atomic.LoadInt32(&reCalls); got != 1 {
		t.Errorf("expected exactly 1 re-establish; got %d", got)
	}
}

// Only ErrStaleTunnel triggers the recovery path. Any other failure
// (daemon unreachable, RSD handshake error, timeout) is surfaced
// immediately with no re-establish and no retry.
func TestResolveWithRecovery_NonStaleErrorNotRetried(t *testing.T) {
	r := New("", 0)
	var resolveCalls, reCalls int32
	sentinel := errors.New("rsd handshake failed")
	r.resolveFn = func(udid string) (ios.DeviceEntry, int, error) {
		atomic.AddInt32(&resolveCalls, 1)
		return ios.DeviceEntry{}, 0, sentinel
	}
	r.reestablishFn = func(udid string) error { atomic.AddInt32(&reCalls, 1); return nil }

	_, _, err := r.resolveWithRecovery("UDID")
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the original non-stale error; got %v", err)
	}
	if got := atomic.LoadInt32(&resolveCalls); got != 1 {
		t.Errorf("non-stale error must not retry; resolves=%d (want 1)", got)
	}
	if got := atomic.LoadInt32(&reCalls); got != 0 {
		t.Errorf("non-stale error must not re-establish; re-establishes=%d (want 0)", got)
	}
}

// If re-establish itself fails, the original stale error is surfaced and
// the resolve is NOT retried a second time — we don't loop.
func TestResolveWithRecovery_ReestablishFailureSurfacesOriginal(t *testing.T) {
	r := New("", 0)
	var resolveCalls int32
	r.resolveFn = func(udid string) (ios.DeviceEntry, int, error) {
		atomic.AddInt32(&resolveCalls, 1)
		return ios.DeviceEntry{}, 0, fmt.Errorf("%w: empty tunnel-info", ErrStaleTunnel)
	}
	r.reestablishFn = func(udid string) error { return errors.New("daemon did not rebuild") }

	_, _, err := r.resolveWithRecovery("UDID")
	if !errors.Is(err, ErrStaleTunnel) {
		t.Fatalf("expected the original stale error to be surfaced; got %v", err)
	}
	if got := atomic.LoadInt32(&resolveCalls); got != 1 {
		t.Errorf("re-establish failure must not trigger a retry; resolves=%d (want 1)", got)
	}
}
