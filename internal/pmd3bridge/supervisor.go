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

// Option configures a Supervisor.
type Option func(*Supervisor)

// WithLogger injects a custom slog.Logger. Defaults to slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(s *Supervisor) { s.log = l }
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

// withFatal injects a test-only fatal hook. Not exported.
func withFatal(f func(error)) Option {
	return func(s *Supervisor) { s.fatal = f }
}

// Supervisor manages a pmd3-bridge subprocess. A single Supervisor must be
// started exactly once; it may be stopped and is not restartable.
//
// Error model (see 🎯T26.2): the bridge is a same-host subprocess whose
// availability is a hard invariant once Start succeeds. If the subprocess
// exits before Stop is called, that is a bug — the supervisor panics via
// its fatal hook and lets the external process supervisor restart the
// whole daemon.
type Supervisor struct {
	binaryPath string
	socketPath string

	log             *slog.Logger
	shutdownTimeout time.Duration
	readyTimeout    time.Duration
	fatal           func(error)

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
		shutdownTimeout: 5 * time.Second,
		readyTimeout:    timeoutReadyHandshake,
		fatal:           func(err error) { panic(err) },
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
	// the process lifetime.
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

// watchdog waits for the subprocess to exit. If Stop has not been called,
// subprocess exit is a bug: the supervisor invokes fatal to crash the
// daemon, which the external process supervisor restarts.
//
// Unlike the older restart-on-crash design, we do NOT attempt to recover
// in-process. A crashed bridge indicates a bug somewhere — in the bridge
// itself, in the transport, or in our use of it — and the only sound
// recovery is to surface the crash (stack trace + bridge stderr, already
// inherited on os.Stderr) and restart the whole daemon cleanly.
func (s *Supervisor) watchdog() {
	defer func() {
		close(s.doneCh)
		_ = os.Remove(s.socketPath)
	}()

	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()

	select {
	case <-s.stopCh:
		// Graceful shutdown requested.
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-exitCh:
		case <-s.killCh:
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-exitCh
		}

	case err := <-exitCh:
		// Subprocess exited without Stop being called.
		// Check once more (race with Stop) — if Stop closed stopCh
		// between cmd.Wait() returning and us selecting, honour that.
		select {
		case <-s.stopCh:
			return
		default:
		}
		s.fatal(fmt.Errorf("pmd3bridge: subprocess exited unexpectedly (this is a bug): %w", err))
	}
}

// LivenessProbe runs in a background goroutine and panics the daemon if the
// bridge becomes unresponsive. It calls client.ListDevices every
// intervalLivenessProbe (30 s); on any non-BridgeError failure, the client
// itself panics via its fatal hook. Returns when ctx is cancelled.
//
// This complements watchdog()'s exit-detection: watchdog catches "process
// died", LivenessProbe catches "process alive but not answering" (the Uvicorn
// wedge observed on 2026-04-22).
func LivenessProbe(ctx context.Context, client *Client) {
	ticker := time.NewTicker(intervalLivenessProbe)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Discard result: we only care that the call returns
			// successfully (or with a structured BridgeError). A
			// transport error panics inside client.post.
			_, _ = client.ListDevices(ctx)
		}
	}
}
