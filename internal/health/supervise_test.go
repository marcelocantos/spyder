// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ─── Fake ManagedProcess ─────────────────────────────────────────────────────

// fakeProcess is a controllable ManagedProcess for supervisor tests.
// All fields are safe for concurrent access (called from the supervisor
// goroutine and the test goroutine simultaneously).
type fakeProcess struct {
	name string

	mu         sync.Mutex
	startCount int     // how many times Start was called
	alive      bool    // current Alive() return value
	startErrs  []error // start errors to return in order; last repeated forever
	stopCalled bool
}

func newFakeProcess(name string, startErrs ...error) *fakeProcess {
	return &fakeProcess{name: name, startErrs: startErrs}
}

func (p *fakeProcess) Name() string { return p.name }

func (p *fakeProcess) Start(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.startCount++
	var err error
	if len(p.startErrs) > 0 {
		err = p.startErrs[0]
		p.startErrs = p.startErrs[1:] // consume; empty list → next call succeeds
	}
	if err == nil {
		p.alive = true
	}
	return err
}

func (p *fakeProcess) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.alive
}

func (p *fakeProcess) setAlive(v bool) {
	p.mu.Lock()
	p.alive = v
	p.mu.Unlock()
}

func (p *fakeProcess) Stop(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopCalled = true
	p.alive = false
	return nil
}

func (p *fakeProcess) StartCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.startCount
}

func (p *fakeProcess) WasStopCalled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopCalled
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// instantSleep is a synchronous sleep that returns true immediately (no
// backoff delay). Used in all tests that call startWithRecovery directly
// or run Supervise with a very short interval.
func instantSleep(_ context.Context, _ time.Duration) bool { return true }

// pollCondition polls cond every 5 ms until it returns true or deadline
// elapses. Returns true if cond became true before the deadline.
func pollCondition(cond func() bool, deadline time.Duration) bool {
	const tick = 5 * time.Millisecond
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return true
		}
		time.Sleep(tick)
	}
	return false
}

// ─── Test 1: Healthy process stays healthy ────────────────────────────────────

// TestSupervise_HealthyProcessStaysHealthy verifies that a process that starts
// successfully and remains alive keeps the entity in Healthy state, and that
// Stop is called when ctx is cancelled.
func TestSupervise_HealthyProcessStaysHealthy(t *testing.T) {
	m, _ := newTestModel()
	proc := newFakeProcess("healthy-proc") // no start errors → starts OK immediately

	s := NewSupervisor(m, WithSleep(instantSleep))
	id := ID{Kind: KindSubprocess, Name: proc.Name()}
	policy := Policy{MaxAttempts: 3, BaseBackoff: time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())

	const probeInterval = 20 * time.Millisecond
	done := make(chan struct{})
	go func() {
		s.Supervise(ctx, proc, policy, probeInterval)
		close(done)
	}()

	// Wait for the entity to appear as Healthy (after the initial Start).
	if !pollCondition(func() bool {
		snap, ok := m.Get(id)
		return ok && snap.State == Healthy
	}, 2*time.Second) {
		t.Fatal("entity did not reach Healthy within deadline")
	}

	// Let a couple of probe ticks fire to confirm it stays Healthy.
	time.Sleep(3 * probeInterval)
	if snap, ok := m.Get(id); !ok || snap.State != Healthy {
		t.Errorf("after ticks: want Healthy, got %s", snap.State)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Supervise did not return after ctx cancellation")
	}

	if !proc.WasStopCalled() {
		t.Error("Stop was not called after ctx cancellation")
	}
}

// ─── Test 2: Crash-loop reaches NeedsAttention ────────────────────────────────

// TestStartWithRecovery_CrashLoopReachesNeedsAttention verifies that a process
// that always fails to start exhausts MaxAttempts and leaves the entity in
// NeedsAttention.
func TestStartWithRecovery_CrashLoopReachesNeedsAttention(t *testing.T) {
	const maxAttempts = 3
	errBoom := errors.New("start failed")

	// Build enough start errors so every call fails.
	startErrs := make([]error, maxAttempts+2)
	for i := range startErrs {
		startErrs[i] = errBoom
	}

	m, _ := newTestModel()
	proc := newFakeProcess("crash-loop", startErrs...)
	id := ID{Kind: KindSubprocess, Name: proc.Name()}
	policy := Policy{MaxAttempts: maxAttempts, BaseBackoff: time.Millisecond}

	s := NewSupervisor(m, WithSleep(instantSleep))
	// Register manually as Supervise would do, then drive Observe→Degraded so
	// RecoveryStarted inside startWithRecovery is meaningful.
	m.Register(id, KindSubprocess, policy)
	m.Observe(id, false, "initial failure")

	s.startWithRecovery(context.Background(), proc, id)

	snap, ok := m.Get(id)
	if !ok {
		t.Fatal("entity not registered")
	}
	if snap.State != NeedsAttention {
		t.Errorf("want NeedsAttention, got %s", snap.State)
	}
	if proc.StartCount() != maxAttempts {
		t.Errorf("want %d Start calls, got %d", maxAttempts, proc.StartCount())
	}
	if snap.Attempts != maxAttempts {
		t.Errorf("want attempts=%d, got %d", maxAttempts, snap.Attempts)
	}
}

// ─── Test 3: Recovers within budget ──────────────────────────────────────────

// TestStartWithRecovery_RecoversWithinBudget verifies that a process that
// fails twice then succeeds leaves the entity Healthy with attempts reset.
func TestStartWithRecovery_RecoversWithinBudget(t *testing.T) {
	errBoom := errors.New("not yet")
	m, _ := newTestModel()
	// Fail twice, succeed on the third call.
	proc := newFakeProcess("flaky-proc", errBoom, errBoom)
	id := ID{Kind: KindSubprocess, Name: proc.Name()}
	policy := Policy{MaxAttempts: 5, BaseBackoff: time.Millisecond}

	s := NewSupervisor(m, WithSleep(instantSleep))
	m.Register(id, KindSubprocess, policy)
	m.Observe(id, false, "initial probe failure")

	s.startWithRecovery(context.Background(), proc, id)

	snap, ok := m.Get(id)
	if !ok {
		t.Fatal("entity not registered")
	}
	if snap.State != Healthy {
		t.Errorf("want Healthy, got %s", snap.State)
	}
	if snap.Attempts != 0 {
		t.Errorf("want attempts=0 after recovery, got %d", snap.Attempts)
	}
	if proc.StartCount() != 3 {
		t.Errorf("want 3 Start calls (2 fails + 1 success), got %d", proc.StartCount())
	}
}

// ─── Test 4: Supervise restarts on death ─────────────────────────────────────

// TestSupervise_RestartsOnDeath verifies that when a process reports not-Alive
// on a probe tick, Supervise calls Start again and returns the entity to Healthy.
func TestSupervise_RestartsOnDeath(t *testing.T) {
	m, _ := newTestModel()
	proc := newFakeProcess("restartable") // starts OK initially, then we kill it
	id := ID{Kind: KindSubprocess, Name: proc.Name()}
	policy := Policy{MaxAttempts: 5, BaseBackoff: time.Millisecond}

	// Use a real (fast) probe interval; the synchronous sleep makes backoff
	// between restarts instantaneous.
	const probeInterval = 20 * time.Millisecond
	s := NewSupervisor(m, WithSleep(instantSleep))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.Supervise(ctx, proc, policy, probeInterval)
		close(done)
	}()

	// Wait for the process to start (entity Healthy, Start count ≥ 1).
	if !pollCondition(func() bool {
		return proc.StartCount() >= 1
	}, 2*time.Second) {
		t.Fatal("process never started")
	}

	// Record Start count before we kill the process.
	startsBefore := proc.StartCount()

	// Kill the process: Alive() will now return false.
	proc.setAlive(false)

	// Wait for the supervisor to detect death and restart.
	if !pollCondition(func() bool {
		return proc.StartCount() > startsBefore
	}, 2*time.Second) {
		t.Fatal("supervisor did not restart the process after death")
	}

	// The entity should return to Healthy after the successful restart.
	if !pollCondition(func() bool {
		snap, ok := m.Get(id)
		return ok && snap.State == Healthy
	}, 2*time.Second) {
		t.Fatal("entity did not return to Healthy after restart")
	}
}

// ─── Test 5: Watchdog stall trips NeedsAttention ─────────────────────────────

// TestWatchdog_StallTripsNeedsAttention verifies that a stall (no Beat for
// longer than timeout) drives the entity to NeedsAttention exactly once, and
// that a Beat within timeout does NOT trigger a stall.
func TestWatchdog_StallTripsNeedsAttention(t *testing.T) {
	clk := newFakeClock()
	m, _ := newTestModel()

	const timeout = time.Second
	w := NewProgressWatchdog(m, "stall-test", timeout, WithWatchdogClock(clk.Now))
	id := ID{Kind: KindDaemon, Name: "stall-test"}

	// Begin at t0.
	t0 := clk.Now()
	w.Begin()

	// Check at t0+500ms: no stall yet.
	if w.Check(t0.Add(500 * time.Millisecond)) {
		t.Error("Check at t0+500ms should return false (within timeout)")
	}
	if snap, _ := m.Get(id); snap.State == NeedsAttention {
		t.Error("entity should not be NeedsAttention before stall")
	}

	// Check at t0+2s: stall should fire.
	if !w.Check(t0.Add(2 * time.Second)) {
		t.Error("Check at t0+2s should return true (stall)")
	}
	snap, ok := m.Get(id)
	if !ok {
		t.Fatal("entity not registered")
	}
	if snap.State != NeedsAttention {
		t.Errorf("after stall: want NeedsAttention, got %s", snap.State)
	}

	// A second Check with the same stall must NOT re-fire (stall guard).
	if w.Check(t0.Add(3 * time.Second)) {
		t.Error("second Check after stall should return false (already stalled)")
	}

	// ── Beat-resets-stall sub-test ────────────────────────────────────────────
	// Fresh watchdog: Begin, Beat within timeout, Check → false.
	clk2 := newFakeClock()
	m2, _ := newTestModel()
	w2 := NewProgressWatchdog(m2, "beat-test", timeout, WithWatchdogClock(clk2.Now))
	t2 := clk2.Now()

	w2.Begin()
	clk2.Advance(500 * time.Millisecond)
	w2.Beat() // progress recorded at t2+500ms
	// Check at t2+1200ms: only 700ms since last Beat, within timeout.
	if w2.Check(t2.Add(1200 * time.Millisecond)) {
		t.Error("Check after recent Beat should return false (within timeout)")
	}
	id2 := ID{Kind: KindDaemon, Name: "beat-test"}
	if snap, _ := m2.Get(id2); snap.State == NeedsAttention {
		t.Error("entity should not be NeedsAttention after Beat within timeout")
	}
}

// ─── Test 6: Progress prevents stall ─────────────────────────────────────────

// TestWatchdog_ProgressPreventsStall verifies that repeated Beat() calls, each
// advancing the clock by less than timeout, keep the entity Healthy and Check
// always returns false.
func TestWatchdog_ProgressPreventsStall(t *testing.T) {
	clk := newFakeClock()
	m, _ := newTestModel()

	const timeout = time.Second
	w := NewProgressWatchdog(m, "progress-test", timeout, WithWatchdogClock(clk.Now))
	id := ID{Kind: KindDaemon, Name: "progress-test"}

	w.Begin()
	t0 := clk.Now()

	// Ten beats, each 400ms apart — always within the 1s timeout.
	for i := range 10 {
		clk.Advance(400 * time.Millisecond)
		w.Beat()
		if w.Check(clk.Now()) {
			t.Errorf("iteration %d: Check returned true unexpectedly", i)
		}
	}

	// Total elapsed: 4000ms, but no stall because each beat was <1s apart.
	_ = t0
	snap, ok := m.Get(id)
	if !ok {
		t.Fatal("entity not registered")
	}
	if snap.State == NeedsAttention {
		t.Errorf("entity should not be NeedsAttention after regular beats; got %s", snap.State)
	}
}

// ─── Test 7: Done clears stall ────────────────────────────────────────────────

// TestWatchdog_DoneClearsStall verifies that after a stall trips NeedsAttention,
// Done() calls RecoverySucceeded and returns the entity to Healthy.
func TestWatchdog_DoneClearsStall(t *testing.T) {
	clk := newFakeClock()
	m, _ := newTestModel()

	const timeout = time.Second
	w := NewProgressWatchdog(m, "done-test", timeout, WithWatchdogClock(clk.Now))
	id := ID{Kind: KindDaemon, Name: "done-test"}

	t0 := clk.Now()
	w.Begin()

	// Trip the stall.
	if !w.Check(t0.Add(2 * time.Second)) {
		t.Fatal("Check should have detected a stall")
	}
	if snap, _ := m.Get(id); snap.State != NeedsAttention {
		t.Fatalf("want NeedsAttention after stall, got %s", snap.State)
	}

	// Done should clear the stall → entity returns to Healthy.
	w.Done()

	snap, ok := m.Get(id)
	if !ok {
		t.Fatal("entity not registered")
	}
	if snap.State != Healthy {
		t.Errorf("after Done: want Healthy, got %s", snap.State)
	}
}

// ─── Concurrency sanity: Supervisor.Model() accessor ─────────────────────────

// TestSupervisor_ModelAccessor is a quick smoke-test that Model() returns the
// same model passed to NewSupervisor — no data race under -race.
func TestSupervisor_ModelAccessor(t *testing.T) {
	m, _ := newTestModel()
	s := NewSupervisor(m)
	if s.Model() != m {
		t.Error("Model() did not return the constructor model")
	}
}
