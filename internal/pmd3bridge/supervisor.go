// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	defaultReadyTimeout      = 10 * time.Second
	defaultShutdownTimeout   = 5 * time.Second
	defaultBackoffInitial    = 1 * time.Second
	defaultBackoffCap        = 30 * time.Second
	defaultBackoffResetAfter = 5 * time.Minute // reset backoff after this long without a crash
)

// clock abstracts time so tests can inject a deterministic implementation.
type clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }

// Option configures a Supervisor.
type Option func(*Supervisor)

// WithLogger injects a custom slog.Logger. Defaults to slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(s *Supervisor) { s.log = l }
}

// WithBackoffCap sets the maximum restart backoff duration. Default: 30 s.
func WithBackoffCap(d time.Duration) Option {
	return func(s *Supervisor) { s.backoffCap = d }
}

// WithShutdownTimeout sets how long Stop waits for the watchdog to finish
// after sending SIGTERM before sending SIGKILL. Default: 5 s.
func WithShutdownTimeout(d time.Duration) Option {
	return func(s *Supervisor) { s.shutdownTimeout = d }
}

// WithReadyTimeout sets how long Start waits for the "ready\n" signal on
// stdout before declaring startup failed. Default: 10 s.
func WithReadyTimeout(d time.Duration) Option {
	return func(s *Supervisor) { s.readyTimeout = d }
}

// withClock injects a test clock. Not exported; only for unit tests in this
// package.
func withClock(c clock) Option {
	return func(s *Supervisor) { s.clock = c }
}

// withBackoffInitial sets the initial backoff duration. Not exported; tests
// only.
func withBackoffInitial(d time.Duration) Option {
	return func(s *Supervisor) { s.backoffInitial = d }
}

// Supervisor manages a pmd3-bridge subprocess. A single Supervisor must be
// started exactly once; it may be stopped and is not restartable.
type Supervisor struct {
	binaryPath string
	socketPath string

	log             *slog.Logger
	backoffCap      time.Duration
	backoffInitial  time.Duration
	shutdownTimeout time.Duration
	readyTimeout    time.Duration
	clock           clock

	mu      sync.Mutex
	cmd     *exec.Cmd
	stopped bool // true once Stop has been called

	// stopCh is closed by Stop to signal the watchdog goroutine to exit.
	stopCh chan struct{}
	// killCh is closed by Stop (after stopCh) if the shutdown timeout fires;
	// the watchdog responds by sending SIGKILL.
	killCh chan struct{}
	// doneCh is closed when the watchdog goroutine exits.
	doneCh chan struct{}
}

// NewSupervisor creates a new Supervisor. Call Start to launch the bridge.
func NewSupervisor(binaryPath, socketPath string, opts ...Option) *Supervisor {
	s := &Supervisor{
		binaryPath:      binaryPath,
		socketPath:      socketPath,
		log:             slog.Default(),
		backoffCap:      defaultBackoffCap,
		backoffInitial:  defaultBackoffInitial,
		shutdownTimeout: defaultShutdownTimeout,
		readyTimeout:    defaultReadyTimeout,
		clock:           realClock{},
		stopCh:          make(chan struct{}),
		killCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start launches the bridge subprocess, waits for the "ready\n" signal, and
// then starts the background watchdog goroutine. It returns once the bridge is
// ready or if startup fails.
func (s *Supervisor) Start(ctx context.Context) error {
	if err := s.launch(ctx); err != nil {
		close(s.doneCh)
		return err
	}
	go s.watchdog()
	return nil
}

// Stop signals the watchdog to shut down the bridge process and waits for it
// to finish. It sends SIGTERM, then after the shutdown timeout sends SIGKILL.
// It is safe to call multiple times.
func (s *Supervisor) Stop(shutdownCtx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		// doneCh may already be closed; just drain it.
		<-s.doneCh
		return nil
	}
	s.stopped = true
	s.mu.Unlock()

	// Signal the watchdog to stop. The watchdog will SIGTERM the process
	// and close doneCh when it exits.
	close(s.stopCh)

	// Arm a timer to close killCh after the shutdown timeout, which tells
	// the watchdog to SIGKILL.
	go func() {
		timer := time.NewTimer(s.shutdownTimeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			close(s.killCh)
		case <-s.doneCh:
			// process already exited; no need to kill
		}
	}()

	// Block until the watchdog goroutine finishes.
	select {
	case <-s.doneCh:
	case <-shutdownCtx.Done():
		// shutdownCtx itself timed out; close killCh to force a SIGKILL and
		// wait for the watchdog regardless (we don't abandon the goroutine).
		select {
		case <-s.killCh:
		default:
			close(s.killCh)
		}
		<-s.doneCh
	}

	_ = os.Remove(s.socketPath)
	return nil
}

// launch starts the bridge process and waits for "ready\n" on stdout.
// It does NOT start the watchdog goroutine.
func (s *Supervisor) launch(ctx context.Context) error {
	// Clean up any leftover socket from a previous run.
	_ = os.Remove(s.socketPath)

	// Use a plain exec.Cmd so the watchdog — not Go's exec framework — owns
	// the process lifetime. exec.CommandContext would kill the process when
	// ctx is cancelled, which conflicts with the watchdog's restart logic.
	cmd := exec.Command(s.binaryPath, "--socket", s.socketPath) //nolint:forbidigo
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pmd3bridge supervisor: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("pmd3bridge supervisor: start %q: %w", s.binaryPath, err)
	}

	readyCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "ready" {
				readyCh <- nil
				return
			}
		}
		if err := scanner.Err(); err != nil {
			readyCh <- fmt.Errorf("reading bridge stdout: %w", err)
			return
		}
		readyCh <- fmt.Errorf("bridge stdout closed before 'ready' signal")
	}()

	timer := time.NewTimer(s.readyTimeout)
	defer timer.Stop()

	select {
	case err := <-readyCh:
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("pmd3bridge supervisor: %w", err)
		}
	case <-timer.C:
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("pmd3bridge supervisor: timed out waiting for bridge ready signal after %s", s.readyTimeout)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return ctx.Err()
	}

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	return nil
}

// watchdog waits for the current process to exit and restarts it on unexpected
// failure. It exits when stopCh is closed.
func (s *Supervisor) watchdog() {
	defer func() {
		close(s.doneCh)
		_ = os.Remove(s.socketPath)
	}()

	backoff := s.backoffInitial
	restartCount := 0

	for {
		// Snapshot the current cmd under lock.
		s.mu.Lock()
		cmd := s.cmd
		s.mu.Unlock()

		// Wait for the process to exit in a goroutine so we can also select
		// on stopCh.
		exitCh := make(chan error, 1)
		if cmd != nil {
			go func() { exitCh <- cmd.Wait() }()
		} else {
			close(exitCh)
		}

		select {
		case <-s.stopCh:
			// Graceful shutdown requested: SIGTERM the process.
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
			// Wait for the process to exit, but respect SIGKILL if the
			// shutdown timeout fires.
			select {
			case <-exitCh:
			case <-s.killCh:
				if cmd != nil && cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				<-exitCh
			}
			return

		case err := <-exitCh:
			// Process exited. Check whether Stop was called concurrently.
			select {
			case <-s.stopCh:
				return
			default:
			}

			restartCount++
			s.log.Warn("pmd3bridge: subprocess exited unexpectedly; will restart",
				"restart_count", restartCount,
				"exit_error", err,
				"backoff", backoff,
			)

			_ = os.Remove(s.socketPath)

			// Sleep with interruptibility: honour a stop request during backoff.
			backoffDone := make(chan struct{})
			go func() {
				s.clock.Sleep(backoff)
				close(backoffDone)
			}()
			select {
			case <-s.stopCh:
				return
			case <-backoffDone:
			}

			// Record when this restart attempt starts for backoff-reset logic.
			startTime := s.clock.Now()

			if err := s.launch(context.Background()); err != nil {
				s.log.Error("pmd3bridge: restart failed",
					"restart_count", restartCount, "error", err)
			} else {
				s.log.Info("pmd3bridge: restarted successfully",
					"restart_count", restartCount)
				elapsed := s.clock.Now().Sub(startTime)
				if elapsed >= defaultBackoffResetAfter {
					backoff = s.backoffInitial
				}
			}

			// Grow backoff for the next potential crash.
			backoff *= 2
			if backoff > s.backoffCap {
				backoff = s.backoffCap
			}
		}
	}
}
