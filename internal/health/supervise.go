// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"time"
)

// defaultProbeInterval is the liveness check cadence used when the caller
// passes a non-positive interval to Supervise. Ten seconds balances
// responsiveness against CPU cost for a battery-constrained mobile device.
const defaultProbeInterval = 10 * time.Second

// ManagedProcess is a supervised subprocess. Implementations wrap a real
// child process (the ios tunnel daemon, adb, a pool sim, a recording);
// tests supply a fake. All methods must be safe to call from the
// supervisor's single goroutine.
type ManagedProcess interface {
	Name() string
	Start(ctx context.Context) error // (re)launch the process; nil = running
	Alive() bool                     // cheap liveness probe
	Stop(ctx context.Context) error  // terminate; best-effort
}

// Supervisor owns a *Model and runs one goroutine per supervised process
// plus optional watchdogs. The sleep function is injected so tests can
// drive backoff waits synchronously without real time.
type Supervisor struct {
	model *Model
	// sleep pauses for d and returns false only if ctx is cancelled first.
	// Production code uses a real timer; tests inject a synchronous version.
	sleep func(ctx context.Context, d time.Duration) bool
}

// SupervisorOption configures a Supervisor at construction time.
type SupervisorOption func(*Supervisor)

// WithSleep replaces the default ctx-aware timer sleep. Used in tests to
// make backoff waits instantaneous without racing on real time.
func WithSleep(fn func(ctx context.Context, d time.Duration) bool) SupervisorOption {
	return func(s *Supervisor) {
		s.sleep = fn
	}
}

// NewSupervisor constructs a Supervisor backed by m. The default sleep
// is a real ctx-aware timer that returns false if ctx is cancelled before
// the duration elapses.
func NewSupervisor(m *Model, opts ...SupervisorOption) *Supervisor {
	s := &Supervisor{
		model: m,
		sleep: func(ctx context.Context, d time.Duration) bool {
			// Real production sleep: honour ctx cancellation.
			select {
			case <-ctx.Done():
				return false
			case <-time.After(d):
				return true
			}
		},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Model returns the underlying health model, allowing callers to register
// observers or query state without holding a separate reference.
func (s *Supervisor) Model() *Model {
	return s.model
}

// Supervise registers proc as a KindSubprocess entity governed by policy,
// starts it, then probes liveness every interval, restarting on death with
// the policy's backoff. Blocks until ctx is cancelled (then Stops proc).
// interval<=0 uses defaultProbeInterval.
//
// Why one goroutine per process: process-level concerns (start, probe, restart)
// are serialised so there is never a race between a restart and the next
// liveness tick without coordination overhead.
func (s *Supervisor) Supervise(ctx context.Context, proc ManagedProcess, policy Policy, interval time.Duration) {
	if interval <= 0 {
		interval = defaultProbeInterval
	}

	id := ID{Kind: KindSubprocess, Name: proc.Name()}
	// Register the entity; starts Healthy and resets attempt counter.
	s.model.Register(id, KindSubprocess, policy)

	// Perform the initial launch, retrying with backoff if it fails.
	s.startWithRecovery(ctx, proc, id)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Context cancelled: stop the process best-effort and exit.
			proc.Stop(context.Background()) //nolint:errcheck // best-effort
			return
		case <-ticker.C:
			if proc.Alive() {
				s.model.Observe(id, true, "alive")
			} else {
				// Process died since the last tick; drive the model to Degraded
				// then attempt recovery.
				s.model.Observe(id, false, "process exited")
				s.startWithRecovery(ctx, proc, id)
			}
		}
	}
}

// startWithRecovery attempts proc.Start, retrying with model-driven backoff
// until it succeeds or the model gives up (entity reaches NeedsAttention
// after policy.MaxAttempts). Returns when the process is running again, or
// recovery is exhausted, or ctx is cancelled.
//
// Why drive the model for backoff rather than an inline counter: the model is
// the single source of truth for attempt counts and backoff state. Delegating
// the decision keeps the supervisor stateless and the policy testable.
func (s *Supervisor) startWithRecovery(ctx context.Context, proc ManagedProcess, id ID) {
	// Signal that a recovery action has begun (Degraded/NeedsAttention → Recovering).
	s.model.RecoveryStarted(id)

	for {
		err := proc.Start(ctx)
		if err == nil {
			// Process started successfully; clear the attempt counter.
			s.model.RecoverySucceeded(id)
			return
		}

		// Record the failed attempt; model advances attempts and may move to
		// NeedsAttention when MaxAttempts is exhausted.
		s.model.RecoveryFailed(id, err.Error())

		// Check whether the model has given up on this entity.
		if snap, ok := s.model.Get(id); ok && snap.State == NeedsAttention {
			// Recovery budget exhausted; surface will be handled by T90.4.
			return
		}

		// Prepare for the next attempt (Degraded → Recovering).
		s.model.RecoveryStarted(id)

		// Wait for the model-computed backoff before retrying. Return early
		// if ctx is cancelled during the wait.
		if !s.sleep(ctx, s.model.NextBackoff(id)) {
			return
		}
	}
}
