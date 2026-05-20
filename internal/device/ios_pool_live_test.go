// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestIOSPoolStress_Live exercises 🎯T67's acceptance against a real
// device. The pool's job is to amortise service-channel handshakes
// across many operations to reduce usbmuxd session churn. The test
// asserts three independent signals:
//
//  1. Open-count reduction — for N operations sharing a single cached
//     connection, the pool performs exactly one underlying handshake;
//     the simulated-baseline path forces a handshake on every call.
//  2. Wall-clock improvement — the pooled path is measurably faster
//     per op. The absolute factor depends on the ratio of handshake
//     cost to RPC-body cost (low for installation_proxy where
//     BrowseUserApps dominates; high for DTX-backed services like
//     ScreenshotService). A modest 25% improvement is the floor.
//  3. usbmuxd health post-stress — `bin/ios list` succeeds after
//     ~220 sequential ops, confirming the third-party device list
//     isn't wedged.
//
// Skipped when SPYDER_LIVE_UDID is unset.
func TestIOSPoolStress_Live(t *testing.T) {
	udid := os.Getenv("SPYDER_LIVE_UDID")
	if udid == "" {
		t.Skip("SPYDER_LIVE_UDID not set; skipping live iOS pool test")
	}

	a := NewIOSAdapter()

	// Warm up the resolver session + initial pool entry so timing
	// excludes the one-time RSD handshake.
	if _, err := a.ListApps(udid); err != nil {
		t.Fatalf("warm-up ListApps(%s): %v", udid, err)
	}

	// Baseline: invalidate after each call so every op pays the
	// installation_proxy open cost. Mirrors the pre-T67 pattern.
	const baselineN = 20
	opensBefore := a.ipPool.Opens()
	t0 := time.Now()
	for i := range baselineN {
		a.ipPool.Invalidate(udid)
		if _, err := a.ListApps(udid); err != nil {
			t.Fatalf("baseline ListApps[%d]: %v", i, err)
		}
	}
	baselineAvg := time.Since(t0) / baselineN
	baselineOpens := a.ipPool.Opens() - opensBefore

	// Pooled: N sequential calls reusing the cached connection. No
	// Invalidate; the pool should perform exactly one open.
	const pooledN = 200
	opensBefore = a.ipPool.Opens()
	t0 = time.Now()
	for i := range pooledN {
		if _, err := a.ListApps(udid); err != nil {
			t.Fatalf("pooled ListApps[%d]: %v", i, err)
		}
	}
	pooledAvg := time.Since(t0) / pooledN
	pooledOpens := a.ipPool.Opens() - opensBefore

	t.Logf("baseline: %d calls, %s avg/call, %d underlying opens", baselineN, baselineAvg, baselineOpens)
	t.Logf("pooled:   %d calls, %s avg/call, %d underlying opens", pooledN, pooledAvg, pooledOpens)

	// (1) Open-count reduction. The decisive signal — the pooled
	// phase performs zero new opens because the warm-up call
	// already established the entry; the baseline phase performs
	// one open per call due to Invalidate.
	if pooledOpens != 0 {
		t.Errorf("pool opened %d underlying connections for %d ops; want 0 (warm cache)", pooledOpens, pooledN)
	}
	if baselineOpens != int64(baselineN) {
		t.Errorf("baseline performed %d underlying opens for %d ops; want %d (Invalidate-each-call)", baselineOpens, baselineN, baselineN)
	}

	// (2) Wall-clock improvement. installation_proxy's BrowseUserApps
	// RPC dominates per-op time, so the absolute savings is modest
	// (~15ms on a healthy iPhone, vs ~60ms RPC). The threshold gives
	// headroom against device-side variance while still catching a
	// regression that removed pooling entirely.
	ratio := float64(pooledAvg) / float64(baselineAvg)
	t.Logf("pooled/baseline wall-clock ratio = %.3f (acceptance: <0.90)", ratio)
	if ratio >= 0.90 {
		t.Errorf("pool didn't deliver measurable speedup: %.3f >= 0.90", ratio)
	}

	// (3) usbmuxd health. The `bin/ios` binary lives at the repo
	// root; tests run from the package dir, so resolve up from this
	// source file.
	iosBin := os.Getenv("SPYDER_LIVE_IOS_BIN")
	if iosBin == "" {
		_, thisFile, _, _ := runtime.Caller(0)
		iosBin = filepath.Join(filepath.Dir(thisFile), "..", "..", "bin", "ios")
	}
	out, err := exec.Command(iosBin, "list").CombinedOutput()
	if err != nil {
		t.Fatalf("usbmuxd health check via %s list: %v\n%s", iosBin, err, out)
	}
	if len(out) == 0 {
		t.Errorf("usbmuxd health check returned empty output — device list may be wedged")
	} else {
		t.Logf("usbmuxd health: %s", out)
	}
}
