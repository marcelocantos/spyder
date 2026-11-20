// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
)

// SelfRestartLimiter rate-limits daemon self-exit for launchd KeepAlive
// recovery (🎯T99.3). Bare `spyder serve` is not required to re-exec;
// production relies on brew services / launchd.
//
// BeforeExit is invoked (if set) immediately before exit so the caller can
// persist a goroutine dump and wedge snapshot under ~/.spyder/.
type SelfRestartLimiter struct {
	mu             sync.Mutex
	window         time.Duration
	max            int
	attempts       []time.Time
	path           string
	restartPending bool
	exitFn         func(int)           // injectable; production uses os.Exit
	beforeExit     func(reason string) // dump hooks (tests may count calls)
	now            func() time.Time
	snapshots      int // count of dump hooks fired
}

// NewSelfRestartLimiter allows max restarts per window across daemon lifetimes.
func NewSelfRestartLimiter(max int, window time.Duration) *SelfRestartLimiter {
	l := newSelfRestartLimiter(max, window)
	l.path = filepath.Join(paths.Base(), "self-restarts.json")
	return l
}

func newSelfRestartLimiter(max int, window time.Duration) *SelfRestartLimiter {
	if max <= 0 {
		max = 3
	}
	if window <= 0 {
		window = time.Hour
	}
	return &SelfRestartLimiter{
		window: window,
		max:    max,
		exitFn: os.Exit,
		now:    time.Now,
	}
}

// NewSelfRestartLimiterForTest is like NewSelfRestartLimiter but injects
// exitFn so unit tests can assert without terminating the process.
func NewSelfRestartLimiterForTest(max int, window time.Duration, exitFn func(int)) *SelfRestartLimiter {
	l := newSelfRestartLimiter(max, window)
	if exitFn != nil {
		l.exitFn = exitFn
	}
	return l
}

// SetBeforeExit installs a pre-exit dump hook (goroutine dump + wedge snapshot).
func (l *SelfRestartLimiter) SetBeforeExit(fn func(reason string)) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.beforeExit = fn
}

// Request records a self-wedge and exits nonzero when under budget.
// Returns false if rate-limited (caller should needs_attention only).
// On allow: runs BeforeExit (dumps), then exitFn.
func (l *SelfRestartLimiter) Request(reason string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	if l.restartPending {
		l.mu.Unlock()
		return false
	}
	if l.path != "" {
		attempts, err := readSelfRestartAttempts(l.path)
		if err != nil {
			l.mu.Unlock()
			slog.Error("self-restart history unavailable; needs attention", "reason", reason, "error", err)
			return false
		}
		l.attempts = attempts
	}
	now := l.now()
	// Prune old attempts.
	cut := now.Add(-l.window)
	kept := l.attempts[:0]
	for _, t := range l.attempts {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	l.attempts = kept
	if len(l.attempts) >= l.max {
		n := len(l.attempts)
		win := l.window
		l.mu.Unlock()
		slog.Error("self-restart rate-limited; needs attention",
			"reason", reason, "attempts", n, "window", win)
		return false
	}
	l.attempts = append(l.attempts, now)
	if l.path != "" {
		if err := saveSelfRestartAttempts(l.path, l.attempts); err != nil {
			l.mu.Unlock()
			slog.Error("self-restart history could not be persisted; needs attention", "reason", reason, "error", err)
			return false
		}
		l.restartPending = true
	}
	l.snapshots++
	before := l.beforeExit
	exitFn := l.exitFn
	attempt := len(l.attempts)
	max := l.max
	l.mu.Unlock()

	if before != nil {
		before(reason)
	}
	slog.Error("self-restart: exiting for supervised relaunch",
		"reason", reason, "attempt", attempt, "max", max)
	if exitFn != nil {
		exitFn(1)
	}
	return true
}

func readSelfRestartAttempts(path string) ([]time.Time, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var attempts []time.Time
	err = json.Unmarshal(data, &attempts)
	return attempts, err
}

func saveSelfRestartAttempts(path string, attempts []time.Time) error {
	data, err := json.Marshal(attempts)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".self-restarts-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// Snapshots returns how many times Request was allowed (tests).
func (l *SelfRestartLimiter) Snapshots() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.snapshots
}
