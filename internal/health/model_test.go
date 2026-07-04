// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for deterministic tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// newTestModel returns a Model wired to a fake clock, plus the clock.
func newTestModel() (*Model, *fakeClock) {
	clk := newFakeClock()
	m := New(WithClock(clk.Now))
	return m, clk
}

// idFor is a convenience constructor for test IDs.
func idFor(kind Kind, name string) ID {
	return ID{Kind: kind, Name: name}
}

// stateOf returns the current state of id in m or "" if unknown.
func stateOf(m *Model, id ID) State {
	snap, ok := m.Get(id)
	if !ok {
		return ""
	}
	return snap.State
}

// TestModel_HealthyToDegradedToRecoveringToHealthy drives the golden path
// through the main recovery cycle.
func TestModel_HealthyToDegradedToRecoveringToHealthy(t *testing.T) {
	m, clk := newTestModel()
	id := idFor(KindDevice, "test-device")
	policy := Policy{MaxAttempts: 3, BaseBackoff: time.Second}

	m.Register(id, KindDevice, policy)
	if got := stateOf(m, id); got != Healthy {
		t.Fatalf("after Register: want Healthy, got %s", got)
	}

	clk.Advance(time.Second)
	m.Observe(id, false, "probe failed")
	if got := stateOf(m, id); got != Degraded {
		t.Fatalf("after Observe(false): want Degraded, got %s", got)
	}

	m.RecoveryStarted(id)
	if got := stateOf(m, id); got != Recovering {
		t.Fatalf("after RecoveryStarted: want Recovering, got %s", got)
	}

	m.RecoverySucceeded(id)
	if got := stateOf(m, id); got != Healthy {
		t.Fatalf("after RecoverySucceeded: want Healthy, got %s", got)
	}

	snap, _ := m.Get(id)
	if snap.Attempts != 0 {
		t.Fatalf("after RecoverySucceeded: want attempts=0, got %d", snap.Attempts)
	}
}

// TestModel_RecoveryExhaustionToNeedsAttention verifies that exhausting
// MaxAttempts reaches NeedsAttention, and that RecoverySucceeded heals it.
func TestModel_RecoveryExhaustionToNeedsAttention(t *testing.T) {
	m, clk := newTestModel()
	id := idFor(KindSubprocess, "ios-tunnel")
	policy := Policy{MaxAttempts: 2, BaseBackoff: 500 * time.Millisecond}
	m.Register(id, KindSubprocess, policy)

	clk.Advance(time.Second)
	m.Observe(id, false, "exit code 1")
	if got := stateOf(m, id); got != Degraded {
		t.Fatalf("want Degraded, got %s", got)
	}

	m.RecoveryStarted(id)
	clk.Advance(time.Second)
	m.RecoveryFailed(id, "restart failed #1")
	// attempts(1) < MaxAttempts(2) → back to Degraded
	if got := stateOf(m, id); got != Degraded {
		t.Fatalf("after RecoveryFailed #1: want Degraded, got %s", got)
	}
	snap, _ := m.Get(id)
	if snap.Attempts != 1 {
		t.Fatalf("want attempts=1, got %d", snap.Attempts)
	}

	m.RecoveryStarted(id)
	clk.Advance(time.Second)
	m.RecoveryFailed(id, "restart failed #2")
	// attempts(2) >= MaxAttempts(2) → NeedsAttention
	if got := stateOf(m, id); got != NeedsAttention {
		t.Fatalf("after RecoveryFailed #2: want NeedsAttention, got %s", got)
	}

	// RecoverySucceeded must heal even from NeedsAttention.
	m.RecoverySucceeded(id)
	if got := stateOf(m, id); got != Healthy {
		t.Fatalf("after RecoverySucceeded from NeedsAttention: want Healthy, got %s", got)
	}
	snap, _ = m.Get(id)
	if snap.Attempts != 0 {
		t.Fatalf("want attempts=0, got %d", snap.Attempts)
	}
}

// TestModel_AbsentStates verifies both absent transitions and the return
// to Healthy when the entity comes back with a passing observation.
func TestModel_AbsentStates(t *testing.T) {
	m, clk := newTestModel()
	id := idFor(KindDevice, "my-ipad")
	m.Register(id, KindDevice, Policy{})

	clk.Advance(time.Second)
	m.MarkAbsent(id, true, "user disconnected")
	if got := stateOf(m, id); got != AbsentExpected {
		t.Fatalf("want AbsentExpected, got %s", got)
	}

	clk.Advance(time.Second)
	m.MarkAbsent(id, false, "vanished mid-test")
	if got := stateOf(m, id); got != AbsentUnexpected {
		t.Fatalf("want AbsentUnexpected, got %s", got)
	}

	// Observe(ok=true) brings the entity back to Healthy.
	clk.Advance(time.Second)
	m.Observe(id, true, "device reconnected")
	if got := stateOf(m, id); got != Healthy {
		t.Fatalf("after reconnect Observe(true): want Healthy, got %s", got)
	}
}

// TestModel_ObserveDoesNotEscalatePastDegraded asserts that repeated
// Observe(false) calls from Degraded never advance to NeedsAttention —
// only recovery exhaustion does that.
func TestModel_ObserveDoesNotEscalatePastDegraded(t *testing.T) {
	m, clk := newTestModel()
	id := idFor(KindDevice, "flaky-device")
	m.Register(id, KindDevice, Policy{MaxAttempts: 2, BaseBackoff: time.Second})

	clk.Advance(time.Second)
	m.Observe(id, false, "fail 1")
	// Must be Degraded now.
	if got := stateOf(m, id); got != Degraded {
		t.Fatalf("want Degraded, got %s", got)
	}

	// Fire many more failing probes.
	for i := 2; i <= 10; i++ {
		clk.Advance(time.Second)
		m.Observe(id, false, "fail repeated")
		if got := stateOf(m, id); got != Degraded {
			t.Fatalf("after observe #%d: want Degraded, got %s", i, got)
		}
	}
	snap, _ := m.Get(id)
	if snap.Attempts != 0 {
		t.Fatalf("raw observations must not increment attempts; got %d", snap.Attempts)
	}
}

// pair captures a (From, To) pair for observer assertions.
type pair struct{ from, to State }

// TestModel_TransitionObserversFire verifies observer delivery order and
// that no spurious transitions fire when state does not change.
func TestModel_TransitionObserversFire(t *testing.T) {
	m, clk := newTestModel()
	id := idFor(KindDaemon, "spyder")

	var mu sync.Mutex
	var got []pair
	m.OnTransition(func(tr Transition) {
		mu.Lock()
		got = append(got, pair{tr.From, tr.To})
		mu.Unlock()
	})

	// Register fires a synthetic From="" → Healthy.
	m.Register(id, KindDaemon, Policy{MaxAttempts: 1, BaseBackoff: time.Second})
	clk.Advance(time.Second)
	m.Observe(id, false, "bad") // Healthy → Degraded
	m.RecoveryStarted(id)       // Degraded → Recovering
	m.RecoverySucceeded(id)     // Recovering → Healthy

	// All callbacks are synchronous and fired outside the model lock, so
	// by the time the model methods above return, all transitions are
	// already recorded. Snapshot under mu and check without holding mu
	// across model calls (that would deadlock if a callback fires).
	snapshot := func() []pair {
		mu.Lock()
		cp := make([]pair, len(got))
		copy(cp, got)
		mu.Unlock()
		return cp
	}

	want := []pair{
		{"", Healthy},
		{Healthy, Degraded},
		{Degraded, Recovering},
		{Recovering, Healthy},
	}
	got1 := snapshot()
	if len(got1) != len(want) {
		t.Fatalf("observer got %d transitions, want %d: %v", len(got1), len(want), got1)
	}
	for i, w := range want {
		if got1[i] != w {
			t.Errorf("transition[%d]: got (%q→%q), want (%q→%q)",
				i, got1[i].from, got1[i].to, w.from, w.to)
		}
	}

	// From Healthy, make it Degraded first (fires 1 transition), then call
	// Observe(false) again from Degraded — that second call must be silent.
	clk.Advance(time.Second)
	m.Observe(id, false, "make degraded") // Healthy → Degraded
	afterDegraded := len(snapshot())

	m.Observe(id, false, "still degraded, no transition expected")
	afterNoOp := len(snapshot())
	if afterNoOp != afterDegraded {
		t.Errorf("Observe(false) from Degraded fired %d extra transition(s)", afterNoOp-afterDegraded)
	}
}

// TestModel_EvidenceBounded verifies the ring cap at maxEvidence.
func TestModel_EvidenceBounded(t *testing.T) {
	m, clk := newTestModel()
	id := idFor(KindDevice, "cap-test")
	m.Register(id, KindDevice, Policy{})

	total := maxEvidence + 5
	for range total {
		clk.Advance(time.Second)
		m.Observe(id, false, "detail")
	}

	snap, _ := m.Get(id)
	if len(snap.Evidence) != maxEvidence {
		t.Fatalf("evidence length: got %d, want %d", len(snap.Evidence), maxEvidence)
	}

	// The most recent observations must be retained; we can verify by
	// checking that lastProbe matches the last observation's At.
	last := snap.Evidence[maxEvidence-1]
	if !last.At.Equal(snap.LastProbe) {
		t.Fatalf("last evidence.At %v != LastProbe %v", last.At, snap.LastProbe)
	}
}

// TestModel_SnapshotIsSerializableAndSorted registers entities across kinds
// and verifies sort order and JSON round-trip.
func TestModel_SnapshotIsSerializableAndSorted(t *testing.T) {
	m, _ := newTestModel()

	ids := []struct {
		id   ID
		kind Kind
	}{
		{ID{Kind: KindDevice, Name: "z-device", Layer: "usbmux"}, KindDevice},
		{ID{Kind: KindDevice, Name: "a-device", Layer: "tunnel"}, KindDevice},
		{ID{Kind: KindDaemon, Name: "spyder"}, KindDaemon},
		{ID{Kind: KindSubprocess, Name: "ios-tunnel"}, KindSubprocess},
		{ID{Kind: KindSubprocess, Name: "adb"}, KindSubprocess},
	}
	for _, e := range ids {
		m.Register(e.id, e.kind, Policy{})
	}

	snap := m.Snapshot()

	// Verify sorted by (Kind, Name, Layer).
	for i := 1; i < len(snap.Entities); i++ {
		a, b := snap.Entities[i-1].ID, snap.Entities[i].ID
		less := a.Kind < b.Kind ||
			(a.Kind == b.Kind && a.Name < b.Name) ||
			(a.Kind == b.Kind && a.Name == b.Name && a.Layer < b.Layer)
		notGreater := a.Kind < b.Kind ||
			(a.Kind == b.Kind && a.Name < b.Name) ||
			(a.Kind == b.Kind && a.Name == b.Name && a.Layer <= b.Layer)
		_ = less
		if !notGreater {
			t.Errorf("entities[%d] %v > entities[%d] %v (not sorted)", i-1, a, i, b)
		}
	}

	// JSON round-trip.
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	var snap2 Snapshot
	if err := json.Unmarshal(data, &snap2); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if len(snap2.Entities) != len(snap.Entities) {
		t.Fatalf("round-trip entity count: got %d, want %d", len(snap2.Entities), len(snap.Entities))
	}
}

// TestModel_NextBackoffDoublesPerAttempt verifies the exponential formula.
func TestModel_NextBackoffDoublesPerAttempt(t *testing.T) {
	m, _ := newTestModel()
	id := idFor(KindSubprocess, "backoff-test")
	base := 100 * time.Millisecond
	m.Register(id, KindSubprocess, Policy{MaxAttempts: 10, BaseBackoff: base})

	// attempts==0 → base
	if got := m.NextBackoff(id); got != base {
		t.Fatalf("attempts=0: want %v, got %v", base, got)
	}

	// Simulate failed recoveries and assert 2^(n-1) growth.
	m.Observe(id, false, "start degraded")
	for n := 1; n <= 5; n++ {
		m.RecoveryStarted(id)
		m.RecoveryFailed(id, "nope")
		want := base * (1 << uint(n-1))
		if got := m.NextBackoff(id); got != want {
			t.Errorf("attempts=%d: want %v, got %v", n, want, got)
		}
	}
}

// fakeProber is a scripted Prober for TestRunPoll_AppliesProberResults.
// It closes notifyCh after the first Probe() so the test can synchronise
// on at least one applied cycle without racing on time.
type fakeProber struct {
	results  []ProbeResult
	notifyCh chan struct{}
	once     sync.Once
}

func newFakeProber(results []ProbeResult) *fakeProber {
	return &fakeProber{results: results, notifyCh: make(chan struct{})}
}

func (p *fakeProber) Probe() []ProbeResult {
	p.once.Do(func() { close(p.notifyCh) })
	return p.results
}

// TestRunPoll_AppliesProberResults verifies that RunPoll drives the model
// correctly and returns when ctx is cancelled.
func TestRunPoll_AppliesProberResults(t *testing.T) {
	m, _ := newTestModel()

	healthyID := idFor(KindDevice, "present-device")
	absentID := idFor(KindDevice, "absent-device")

	prober := newFakeProber([]ProbeResult{
		{ID: healthyID, OK: true, Detail: "alive"},
		{ID: absentID, Absent: true, Expected: false, Detail: "gone"},
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		m.RunPoll(ctx, 5*time.Millisecond, prober)
		close(done)
	}()

	// Wait for at least one probe cycle via the notifyCh handshake.
	select {
	case <-prober.notifyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first Probe() call")
	}

	// notifyCh closes INSIDE Probe(), before RunPoll's apply() mutates the
	// model, so poll (bounded) for the expected states rather than reading
	// once — a single read here would race the apply().
	deadline := time.Now().Add(2 * time.Second)
	for {
		if stateOf(m, healthyID) == Healthy && stateOf(m, absentID) == AbsentUnexpected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("RunPoll did not apply probe results in time: healthy=%s absent=%s",
				stateOf(m, healthyID), stateOf(m, absentID))
		}
		time.Sleep(time.Millisecond)
	}

	// Cancel and wait for RunPoll to exit.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPoll did not return after context cancellation")
	}
}
