// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
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
// Transport (🎯T26.1): the bridge binds an ephemeral port on 127.0.0.1 and
// emits a `ready port=NNNN token=XXXX` line on stdout. The supervisor reads
// that line, stores the port + token, and exposes them via BaseURL() and
// Token() so the daemon can construct an authenticated Client.
//
// Error model (🎯T26.2): the bridge is a same-host subprocess whose
// availability is a hard invariant once Start succeeds. If the subprocess
// exits before Stop is called, that is a bug — the supervisor panics via
// its fatal hook and lets the external process supervisor restart the
// whole daemon.
//
// Liveness pipe (🎯T44): the supervisor passes the read end of an os.Pipe()
// as the bridge's stdin (fd 0) and keeps the write end open for its own
// lifetime. When the parent process exits — clean shutdown, panic, SIGKILL,
// OOM — the kernel closes the write end and the child's read on fd 0 returns
// EOF. The bridge's _watch_parent_via_stdin thread calls os._exit(0) on
// that EOF, preventing orphaned processes.
type Supervisor struct {
	binaryPath string

	log             *slog.Logger
	shutdownTimeout time.Duration
	readyTimeout    time.Duration
	fatal           func(error)

	mu           sync.Mutex
	cmd          *exec.Cmd
	port         int      // bridge's listening port, set during launch
	token        string   // bridge's bearer token, set during launch
	stopped      bool     // true once Stop has been called
	livenessPipe *os.File // write end of the stdin-EOF liveness pipe (🎯T44)

	// stopCh is closed by Stop to signal the watchdog goroutine to exit.
	stopCh chan struct{}
	// killCh is closed by Stop (after stopCh) if the shutdown timeout fires;
	// the watchdog responds by sending SIGKILL.
	killCh chan struct{}
	// doneCh is closed when the watchdog goroutine exits.
	doneCh chan struct{}
}

// NewSupervisor creates a new Supervisor. Call Start to launch the bridge.
func NewSupervisor(binaryPath string, opts ...Option) *Supervisor {
	s := &Supervisor{
		binaryPath:      binaryPath,
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

// BaseURL returns the http://127.0.0.1:NNNN base URL of the running bridge.
// Valid after Start has returned nil; empty otherwise.
func (s *Supervisor) BaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}

// Token returns the bearer token the bridge accepts. Valid after Start has
// returned nil; empty otherwise.
func (s *Supervisor) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// Stopped reports whether Stop has been called on this supervisor. Used
// by the Client to swallow transport errors during the bridge's
// shutdown window — the daemon's own SIGINT/SIGTERM handler is the
// authoritative termination path; the bridge subprocess dying mid-
// request as a consequence of that is not a fatal client bug.
func (s *Supervisor) Stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

// Client constructs a new Client that reads the bridge's base URL and bearer
// token from this Supervisor on every request. It is safe to call before
// Start — requests issued before Start will fail their auth check (empty
// token) or fail to connect (zero port), both of which are bugs.
func (s *Supervisor) Client() *Client {
	return &Client{
		http:  &http.Client{},
		sup:   s,
		fatal: func(err error) { panic(err) },
	}
}

// Start launches the bridge subprocess, waits for the `ready port=... token=...`
// signal on stdout, and then starts the background watchdog goroutine. It
// returns once the bridge is ready or if startup fails.
func (s *Supervisor) Start(ctx context.Context) error {
	s.log.Info("bridge supervisor: launching",
		"binary", s.binaryPath,
		"ready_timeout", s.readyTimeout)
	if err := s.launch(ctx); err != nil {
		close(s.doneCh)
		s.log.Error("bridge supervisor: launch failed", "error", err)
		return err
	}
	s.log.Info("bridge supervisor: ready",
		"binary", s.binaryPath,
		"port", s.port, "pid", s.cmd.Process.Pid)
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

	s.log.Info("bridge supervisor: stopping", "shutdown_timeout", s.shutdownTimeout)

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
	return nil
}

// launch starts the bridge process and waits for the structured
// `ready port=NNNN token=XXXX` line on stdout. It does NOT start the
// watchdog goroutine.
func (s *Supervisor) launch(ctx context.Context) error {
	// Use a plain exec.Cmd so the watchdog — not Go's exec framework — owns
	// the process lifetime. Setpgid = true puts the bridge in its own
	// process group so signalling the group tears down any child uv/python
	// processes the bridge spawned (e.g. when launched via the dev
	// wrapper script through uv run).
	cmd := exec.Command(s.binaryPath) //nolint:forbidigo
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Liveness pipe (🎯T44): create a pipe whose read end becomes the
	// bridge's stdin (fd 0). We keep the write end open for our lifetime
	// and never write to it. When the parent process exits for any reason
	// — clean shutdown, panic, SIGKILL, OOM — the kernel closes our copy
	// of the write end and the bridge's blocking read on fd 0 returns EOF,
	// triggering its _watch_parent_via_stdin thread to call os._exit(0).
	livenessReadEnd, livenessWriteEnd, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("pmd3bridge supervisor: liveness pipe: %w", err)
	}
	cmd.Stdin = livenessReadEnd

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		livenessReadEnd.Close()
		livenessWriteEnd.Close()
		return fmt.Errorf("pmd3bridge supervisor: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		livenessReadEnd.Close()
		livenessWriteEnd.Close()
		return fmt.Errorf("pmd3bridge supervisor: start %q: %w", s.binaryPath, err)
	}
	// The child now has its own copy of the read end via fork+exec.
	// Close the parent's copy so only the child holds it — this ensures
	// the child sees EOF when we close our write end, not when it closes
	// its own read end.
	livenessReadEnd.Close()

	type readyResult struct {
		port  int
		token string
		err   error
	}
	readyCh := make(chan readyResult, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "ready") {
				port, token, perr := parseReadyLine(line)
				readyCh <- readyResult{port: port, token: token, err: perr}
				return
			}
		}
		if err := scanner.Err(); err != nil {
			readyCh <- readyResult{err: fmt.Errorf("reading bridge stdout: %w", err)}
			return
		}
		readyCh <- readyResult{err: fmt.Errorf("bridge stdout closed before 'ready' signal")}
	}()

	timer := time.NewTimer(s.readyTimeout)
	defer timer.Stop()

	var result readyResult
	select {
	case result = <-readyCh:
		if result.err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			livenessWriteEnd.Close()
			return fmt.Errorf("pmd3bridge supervisor: %w", result.err)
		}
	case <-timer.C:
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		livenessWriteEnd.Close()
		return fmt.Errorf("pmd3bridge supervisor: timed out waiting for bridge ready signal after %s", s.readyTimeout)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		livenessWriteEnd.Close()
		return ctx.Err()
	}

	s.mu.Lock()
	s.cmd = cmd
	s.port = result.port
	s.token = result.token
	s.livenessPipe = livenessWriteEnd
	s.mu.Unlock()
	return nil
}

// parseReadyLine parses a `ready port=NNNN token=XXXX` line from the bridge's
// stdout. Returns (0, "", err) on malformed input.
func parseReadyLine(line string) (int, string, error) {
	fields := strings.Fields(line)
	if len(fields) < 3 || fields[0] != "ready" {
		return 0, "", fmt.Errorf("malformed ready line: %q", line)
	}
	var port int
	var token string
	for _, kv := range fields[1:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return 0, "", fmt.Errorf("malformed ready line entry: %q", kv)
		}
		switch k {
		case "port":
			p, err := parseUint(v)
			if err != nil {
				return 0, "", fmt.Errorf("ready line: invalid port %q: %w", v, err)
			}
			port = p
		case "token":
			token = v
		}
	}
	if port == 0 {
		return 0, "", fmt.Errorf("ready line missing port: %q", line)
	}
	if token == "" {
		return 0, "", fmt.Errorf("ready line missing token: %q", line)
	}
	return port, token, nil
}

func parseUint(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-digit %q", r)
		}
		n = n*10 + int(r-'0')
		if n > 65535 {
			return 0, fmt.Errorf("out of range")
		}
	}
	return n, nil
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
	defer close(s.doneCh)

	s.mu.Lock()
	cmd := s.cmd
	pipe := s.livenessPipe
	s.mu.Unlock()
	// Close the liveness write end on shutdown so the child sees EOF promptly
	// even during a clean Stop (SIGTERM path). This is hygiene: the child exits
	// via SIGTERM anyway, but closing the pipe removes the fd from the parent.
	defer func() {
		if pipe != nil {
			pipe.Close()
		}
	}()

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()

	select {
	case <-s.stopCh:
		// Graceful shutdown requested. Signal the whole process group
		// (negative PID) so child uv/python processes from the dev
		// wrapper are included in the SIGTERM.
		if cmd.Process != nil {
			s.log.Info("bridge supervisor: sending SIGTERM", "pid", cmd.Process.Pid)
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
		select {
		case err := <-exitCh:
			s.log.Info("bridge supervisor: subprocess exited cleanly", "error", err)
		case <-s.killCh:
			if cmd.Process != nil {
				s.log.Warn("bridge supervisor: shutdown timeout fired, sending SIGKILL",
					"pid", cmd.Process.Pid)
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
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
		s.log.Error("bridge supervisor: subprocess exited unexpectedly",
			"wait_error", err, "pid", cmd.Process.Pid)
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
//
// Logging (🎯T26.5): emits an INFO `bridge liveness: started` on entry, a
// periodic INFO heartbeat every livenessHeartbeatEvery ticks (≈1 h at the
// 30 s cadence) including consecutive-ok count and last-call duration, and
// a WARN per structured BridgeError (rare in practice — list_devices does
// not return device-scoped errors).
func LivenessProbe(ctx context.Context, client *Client) {
	slog.Info("bridge liveness: started",
		"interval", intervalLivenessProbe,
		"heartbeat_every", livenessHeartbeatEvery)
	ticker := time.NewTicker(intervalLivenessProbe)
	defer ticker.Stop()

	var consecutiveOk int
	var lastDuration time.Duration

	for {
		select {
		case <-ctx.Done():
			slog.Info("bridge liveness: stopped",
				"consecutive_ok", consecutiveOk)
			return
		case <-ticker.C:
			started := time.Now()
			_, err := client.ListDevices(ctx)
			lastDuration = time.Since(started)

			if err != nil {
				// BridgeError or context.Canceled only; transport
				// errors already panicked inside client.post.
				if !errors.Is(err, context.Canceled) {
					slog.Warn("bridge liveness: structured error",
						"error", err,
						"consecutive_ok_before", consecutiveOk)
				}
				consecutiveOk = 0
				continue
			}
			consecutiveOk++
			if consecutiveOk%livenessHeartbeatEvery == 0 {
				slog.Info("bridge liveness: healthy",
					"consecutive_ok", consecutiveOk,
					"last_duration_ms", lastDuration.Milliseconds())
			}
		}
	}
}

// livenessHeartbeatEvery is the number of consecutive successful probes
// between INFO heartbeat logs. With intervalLivenessProbe=30s, 120 ≈ 1 h.
const livenessHeartbeatEvery = 120
