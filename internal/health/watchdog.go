// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// WatchdogOption configures a ProgressWatchdog at construction time.
type WatchdogOption func(*ProgressWatchdog)

// WithWatchdogClock injects a custom clock for deterministic tests.
// The clock is used by Begin, Beat, and Done to timestamp progress marks.
func WithWatchdogClock(now func() time.Time) WatchdogOption {
	return func(w *ProgressWatchdog) {
		w.now = now
	}
}

// ProgressWatchdog watches a single long-running operation for internal
// progress. The operation calls Beat() as it makes progress and Done() when
// it finishes; if more than timeout elapses between Begin() and the next
// Beat()/Done() while work is outstanding, the watchdog marks a daemon-self
// entity NeedsAttention (a wedged-but-alive stall).
//
// Why wedged-but-alive detection: process supervisors (launchd KeepAlive,
// Supervise) catch crash loops. They are blind to a process that is running
// but has stopped making progress — a deadlocked handler or a goroutine leak.
// The ProgressWatchdog fills that gap by tracking heartbeats from inside the
// operation (cf. 🎯T83 Session.Close deadlock).
type ProgressWatchdog struct {
	mu           sync.Mutex
	model        *Model
	id           ID
	timeout      time.Duration
	now          func() time.Time
	outstanding  bool      // work is in progress (Begin called, Done not yet called)
	lastProgress time.Time // time of the last Begin or Beat
	stalled      bool      // a stall has been flagged; prevents re-firing until Done
}

// NewProgressWatchdog constructs a ProgressWatchdog and registers a
// KindDaemon entity with the model. The policy MaxAttempts:1 ensures that a
// single RecoveryFailed call drives the entity to NeedsAttention, which is
// the desired outcome for a wedged-but-alive stall.
func NewProgressWatchdog(m *Model, name string, timeout time.Duration, opts ...WatchdogOption) *ProgressWatchdog {
	w := &ProgressWatchdog{
		model:   m,
		id:      ID{Kind: KindDaemon, Name: name},
		timeout: timeout,
		now:     time.Now,
	}
	for _, o := range opts {
		o(w)
	}
	// Register with MaxAttempts:1 so the first RecoveryFailed → NeedsAttention.
	m.Register(w.id, KindDaemon, Policy{MaxAttempts: 1})
	return w
}

// Begin marks the start of a monitored operation (work now outstanding).
// Resets the progress timestamp and the stall guard so a fresh operation can
// fire a new stall transition if it wedges.
func (w *ProgressWatchdog) Begin() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.outstanding = true
	w.lastProgress = w.now()
	w.stalled = false
}

// Beat records progress on the outstanding operation, deferring the stall
// deadline. Call it at natural checkpoints to prove the operation is
// advancing. Resets the stall guard so a subsequent re-stall (same Begin)
// could fire again (not needed in current usage but keeps the semantics
// consistent with Begin).
func (w *ProgressWatchdog) Beat() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastProgress = w.now()
	// A beat after a stall means the operation resumed; clear the stall flag
	// so a subsequent prolonged silence can re-fire.
	w.stalled = false
}

// Done marks the operation complete (no work outstanding). If the entity is
// currently in a degraded/stalled state, Done recovers it so the health
// surface reflects that the operation eventually finished.
func (w *ProgressWatchdog) Done() {
	w.mu.Lock()
	wasStalled := w.stalled
	w.outstanding = false
	w.stalled = false
	id := w.id
	w.mu.Unlock()

	// If we fired NeedsAttention for this operation, clear it now that the
	// operation completed (even if late). This mirrors RecoverySucceeded
	// semantics: the entity recovered spontaneously.
	if wasStalled {
		w.model.RecoverySucceeded(id)
	}
}

// Check is called by a periodic driver (or a test) with the current time.
// If work is outstanding and (now - lastProgress) > timeout it transitions
// the daemon-self entity to NeedsAttention exactly once per stall and returns
// true. Returns false when no stall is detected or the stall was already
// flagged.
func (w *ProgressWatchdog) Check(now time.Time) bool {
	w.mu.Lock()
	if !w.outstanding || w.stalled || now.Sub(w.lastProgress) <= w.timeout {
		w.mu.Unlock()
		return false
	}
	// Stall detected for the first time on this operation.
	w.stalled = true
	id := w.id
	elapsed := now.Sub(w.lastProgress)
	w.mu.Unlock()

	// Drive the model to NeedsAttention:
	//   Healthy → Degraded (Observe false)
	//   Degraded → Recovering (RecoveryStarted)
	//   Recovering → NeedsAttention (RecoveryFailed, MaxAttempts=1 so attempts(1)>=1)
	//
	// Why three steps: the model's state machine requires a Degraded
	// observation before RecoveryStarted makes sense, and RecoveryFailed
	// requires the entity to be in a Recovering state to record an attempt.
	stallMsg := fmt.Sprintf("no progress for %v: wedged-but-alive", elapsed.Round(time.Millisecond))
	w.model.Observe(id, false, stallMsg)
	w.model.RecoveryStarted(id)
	w.model.RecoveryFailed(id, "wedged-but-alive: "+stallMsg)
	return true
}

// ForceStall marks the outstanding operation stalled immediately (no timeout
// wait) and drives NeedsAttention. Used when a dispatch deadline already
// fired and the handler is still stuck after grace (🎯T99.3) so daemon-self
// reflects the wedge before self-restart dumps + exit.
func (w *ProgressWatchdog) ForceStall(detail string) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	if !w.outstanding || w.stalled {
		w.mu.Unlock()
		return false
	}
	w.stalled = true
	id := w.id
	w.mu.Unlock()
	if detail == "" {
		detail = "wedged-but-alive: forced after dispatch deadline"
	}
	w.model.Observe(id, false, detail)
	w.model.RecoveryStarted(id)
	w.model.RecoveryFailed(id, detail)
	return true
}

// Run drives Check on an interval until ctx is cancelled. This is the
// production driver; tests call Check directly for deterministic control.
func (w *ProgressWatchdog) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			w.Check(t)
		}
	}
}
