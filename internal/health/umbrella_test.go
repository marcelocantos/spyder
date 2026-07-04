// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package health — T90 umbrella integration tests.
//
// These are class-1 injection integration tests: they wire the real pieces
// (Model + Classify + ApplyAssessment + AttentionNotifier) together with a
// fake Notifier and a fake clock, then reproduce the three umbrella scenarios
// end-to-end without any real I/O or sleeps.
//
// Scenario key:
//
//	SelfHeal   — a recoverable fault self-heals; zero Notify calls expected.
//	BenignAway — a non-pinned device disconnects; zero Notify, but state visible.
//	GenuineProblem — three genuine faults each fire exactly one Notify + one Clear.
package health

import (
	"testing"
	"time"
)

// baseEvidence returns a pre-populated Evidence for a device UDID whose
// layers and oracles are all healthy. Tests mutate the returned value to
// simulate failure scenarios.
func baseEvidence(udid string, now time.Time) Evidence {
	return Evidence{
		UDID: udid,
		Layers: []LayerState{
			{Layer: "usbmux", OK: true, Observed: true},
			{Layer: "tunnel", OK: true, Observed: true},
		},
		Oracles: []OracleReading{
			{Source: "usbmux", Present: true, Observed: true},
			{Source: "devicectl", Present: true, Observed: true},
		},
		Now:             now,
		ExpectedPresent: false,
	}
}

// ─── Scenario 1: Self-heal, no human notification ────────────────────────────

// TestUmbrella_SelfHealNoHuman simulates a recoverable stale tunnel that
// self-heals before any NeedsAttention threshold is crossed.
//
// Sequence:
//  1. Classify evidence of a tunnel-down (parent up) → Degraded.
//  2. ApplyAssessment → model in Degraded.
//  3. Simulate the T89.1 self-heal succeeding: Observe(true).
//  4. Assert ZERO Notify calls across the entire sequence.
func TestUmbrella_SelfHealNoHuman(t *testing.T) {
	clk := newFakeClock()
	m := New(WithClock(clk.Now))
	fn := &fakeNotifier{}
	an := NewAttentionNotifier(fn, WithNotifyClock(clk.Now))
	an.Attach(m)

	const udid = "AA-BB-CC-DD"
	deviceID := ID{Kind: KindDevice, Name: udid, Layer: "tunnel"}

	// Register so the entity is known (MaxAttempts left at default 0 = unlimited;
	// we never hit NeedsAttention via exhaustion in this scenario).
	m.Register(deviceID, KindDevice, Policy{})

	// ── Step 1-2: tunnel is down, parent (usbmux) is up → Degraded. ──────────
	clk.Advance(time.Second)
	ev := baseEvidence(udid, clk.Now())
	ev.Layers[1].OK = false // tunnel layer down

	a := Classify(ev)
	if a.SuggestedState != Degraded {
		t.Fatalf("classify: want Degraded for tunnel-down, got %s (class %s)", a.SuggestedState, a.Class)
	}
	ApplyAssessment(m, deviceID, a)

	snap, _ := m.Get(deviceID)
	if snap.State != Degraded {
		t.Fatalf("after ApplyAssessment: want Degraded, got %s", snap.State)
	}

	// ── Step 3: self-heal succeeds (T89.1 supervisor re-dials tunnel). ───────
	clk.Advance(time.Second)
	m.Observe(deviceID, true, "tunnel re-established")

	snap, _ = m.Get(deviceID)
	if snap.State != Healthy {
		t.Fatalf("after self-heal Observe(true): want Healthy, got %s", snap.State)
	}

	// ── Step 4: assert zero Notify calls. ────────────────────────────────────
	if n := len(fn.notifyCalls()); n != 0 {
		t.Errorf("self-healing fault must never surface: want 0 Notify, got %d: %v",
			n, fn.notifyCalls())
	}
}

// ─── Scenario 2: Benign absence — no alarm ───────────────────────────────────

// TestUmbrella_BenignNoAlarm simulates a NON-pinned device being physically
// unplugged. The classifier should produce AbsentUnexpected; the notifier
// must stay silent; the state must be visible in the pull surface.
func TestUmbrella_BenignNoAlarm(t *testing.T) {
	clk := newFakeClock()
	m := New(WithClock(clk.Now))
	fn := &fakeNotifier{}
	an := NewAttentionNotifier(fn, WithNotifyClock(clk.Now))
	an.Attach(m)

	const udid = "11-22-33-44"
	deviceID := ID{Kind: KindDevice, Name: udid, Layer: "tunnel"}

	m.Register(deviceID, KindDevice, Policy{})

	// Device unplugs: a detach happened, no re-attach, all oracles see it gone.
	// ExpectedPresent is false (non-pinned device).
	clk.Advance(time.Second)
	now := clk.Now()
	detachTime := now.Add(-2 * time.Second) // detach 2 s ago, no re-attach
	ev := Evidence{
		UDID: udid,
		Layers: []LayerState{
			{Layer: "usbmux", OK: false, Observed: true},
		},
		Oracles: []OracleReading{
			{Source: "usbmux", Present: false, Observed: true},
			{Source: "devicectl", Present: false, Observed: true},
		},
		LastDetach:      detachTime,
		Now:             now,
		ExpectedPresent: false, // NOT pinned
	}

	a := Classify(ev)
	if a.SuggestedState != AbsentUnexpected {
		t.Fatalf("classify: want AbsentUnexpected for unpinned removal, got %s (class %s)",
			a.SuggestedState, a.Class)
	}
	ApplyAssessment(m, deviceID, a)

	// State must be AbsentUnexpected — visible to the pull surface.
	snap, ok := m.Get(deviceID)
	if !ok {
		t.Fatal("entity not found after ApplyAssessment")
	}
	if snap.State != AbsentUnexpected {
		t.Fatalf("want AbsentUnexpected, got %s", snap.State)
	}

	// The notifier must stay silent — unplugging a non-pinned device is informational.
	if n := len(fn.notifyCalls()); n != 0 {
		t.Errorf("benign removal must not notify: want 0, got %d: %v", n, fn.notifyCalls())
	}
}

// ─── Scenario 3: Genuine problems — each surfaces exactly once ───────────────

// TestUmbrella_GenuineProblemSurfacedOnce exercises three independent
// genuine-problem sub-cases in a single model so we can assert the cumulative
// Notify count across them. Each genuine problem must produce exactly one Notify;
// when the problem resolves the notifier must produce exactly one Clear.
//
//	3a. Pinned device absent (classifier → NeedsAttention directly via bridge).
//	3b. Tunnel un-buildable after recovery exhaustion.
//	3c. Subprocess won't restart after recovery exhaustion.
//
// Invariant across 3a–3c: total Notify count == 3, total Clear count == 3.
func TestUmbrella_GenuineProblemSurfacedOnce(t *testing.T) {
	clk := newFakeClock()
	m := New(WithClock(clk.Now))
	fn := &fakeNotifier{}
	// Use a zero cooldown so that each of the three independent entities can
	// fire even if they are processed in quick succession. Each entity has its
	// own key; cooldown only gates SAME-entity repeats, so the default 5 min
	// would not actually block here — but zero makes the intent clear.
	an := NewAttentionNotifier(fn,
		WithNotifyClock(clk.Now),
		WithNotifyCooldown(0),
	)
	an.Attach(m)

	// ── 3a: pinned device absent → NeedsAttention via classifier→bridge ──────

	const pinnedUDID = "PINNED-DEVICE"
	pinnedID := ID{Kind: KindDevice, Name: pinnedUDID} // no layer — whole-device absence

	m.Register(pinnedID, KindDevice, Policy{})

	clk.Advance(time.Second)
	now3a := clk.Now()
	detach3a := now3a.Add(-3 * time.Second)
	ev3a := Evidence{
		UDID: pinnedUDID,
		Layers: []LayerState{
			{Layer: "usbmux", OK: false, Observed: true},
		},
		Oracles: []OracleReading{
			{Source: "usbmux", Present: false, Observed: true},
			{Source: "devicectl", Present: false, Observed: true},
		},
		LastDetach:      detach3a,
		Now:             now3a,
		ExpectedPresent: true, // PINNED
	}

	a3a := Classify(ev3a)
	if a3a.SuggestedState != NeedsAttention {
		t.Fatalf("3a: classify pinned absent: want NeedsAttention, got %s (class %s)",
			a3a.SuggestedState, a3a.Class)
	}
	ApplyAssessment(m, pinnedID, a3a)

	if n := len(fn.notifyCalls()); n != 1 {
		t.Fatalf("3a: want exactly 1 Notify after pinned-absent, got %d", n)
	}

	// Device returns.
	clk.Advance(time.Second)
	m.Observe(pinnedID, true, "reconnected")
	if n := len(fn.clearCalls()); n != 1 {
		t.Fatalf("3a: want exactly 1 Clear after reconnect, got %d", n)
	}

	// ── 3b: tunnel un-buildable after retry exhaustion ─────────────────────────

	const tunnelUDID = "TUNNEL-DEVICE"
	tunnelID := ID{Kind: KindDevice, Name: tunnelUDID, Layer: "tunnel"}

	m.Register(tunnelID, KindDevice, Policy{MaxAttempts: 2})

	clk.Advance(time.Second)
	m.Observe(tunnelID, false, "tunnel dial failed: connection refused")
	m.RecoveryStarted(tunnelID)

	clk.Advance(time.Second)
	m.RecoveryFailed(tunnelID, "restart failed #1")

	clk.Advance(time.Second)
	// One more RecoveryFailed to exhaust MaxAttempts=2 → NeedsAttention.
	m.RecoveryFailed(tunnelID, "restart failed #2")

	snap3b, _ := m.Get(tunnelID)
	if snap3b.State != NeedsAttention {
		t.Fatalf("3b: want NeedsAttention after exhaustion, got %s", snap3b.State)
	}
	if n := len(fn.notifyCalls()); n != 2 {
		t.Fatalf("3b: want 2 Notify total (3a+3b), got %d", n)
	}

	// Tunnel is manually recovered.
	clk.Advance(time.Second)
	m.RecoverySucceeded(tunnelID)
	if n := len(fn.clearCalls()); n != 2 {
		t.Fatalf("3b: want 2 Clear total (3a+3b), got %d", n)
	}

	// ── 3c: subprocess won't restart after exhaustion ─────────────────────────

	subprocID := ID{Kind: KindSubprocess, Name: "ios-tunnel-daemon"}
	m.Register(subprocID, KindSubprocess, Policy{MaxAttempts: 2})

	clk.Advance(time.Second)
	m.Observe(subprocID, false, "exit code 1")
	m.RecoveryStarted(subprocID)

	clk.Advance(time.Second)
	m.RecoveryFailed(subprocID, "relaunch failed #1")

	clk.Advance(time.Second)
	m.RecoveryFailed(subprocID, "relaunch failed #2")

	snap3c, _ := m.Get(subprocID)
	if snap3c.State != NeedsAttention {
		t.Fatalf("3c: want NeedsAttention after exhaustion, got %s", snap3c.State)
	}
	if n := len(fn.notifyCalls()); n != 3 {
		t.Fatalf("3c: want 3 Notify total (3a+3b+3c), got %d", n)
	}

	// Subprocess is manually restarted.
	clk.Advance(time.Second)
	m.RecoverySucceeded(subprocID)
	if n := len(fn.clearCalls()); n != 3 {
		t.Fatalf("3c: want 3 Clear total (3a+3b+3c), got %d", n)
	}

	// ── Final invariant: exactly 3 Notify and 3 Clear, no spurious extras. ───

	totalNotify := len(fn.notifyCalls())
	totalClear := len(fn.clearCalls())
	if totalNotify != 3 {
		t.Errorf("final: want 3 Notify total, got %d", totalNotify)
	}
	if totalClear != 3 {
		t.Errorf("final: want 3 Clear total, got %d", totalClear)
	}
}
