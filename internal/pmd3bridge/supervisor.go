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

	mu      sync.Mutex
	gen     *generation // current generation; replaced on Restart
	stopped bool        // true once Stop has been called
}

// generation owns the runtime state of one bridge subprocess incarnation.
// Restart (🎯T47) creates a new generation so the watchdog channels and
// process handle from the wedged subprocess can be retired cleanly while
// the next subprocess starts fresh. BaseURL and Token always reflect the
// current generation under the supervisor's mu, so an in-flight Client
// call that retries after a transport error will land on the new bridge.
type generation struct {
	cmd          *exec.Cmd
	port         int
	token        string
	livenessPipe *os.File

	// stopCh is closed by Stop or Restart to signal the watchdog to
	// terminate this subprocess.
	stopCh chan struct{}
	// killCh is closed when the shutdown timeout fires (or immediately
	// during a wedge-restart) so the watchdog escalates to SIGKILL.
	killCh chan struct{}
	// doneCh is closed when the watchdog goroutine exits.
	doneCh chan struct{}
	// restarting is set by Restart before signalling stopCh; the watchdog
	// reads it to suppress the unexpected-exit panic for a planned restart.
	restarting bool
}

// NewSupervisor creates a new Supervisor. Call Start to launch the bridge.
func NewSupervisor(binaryPath string, opts ...Option) *Supervisor {
	s := &Supervisor{
		binaryPath:      binaryPath,
		log:             slog.Default(),
		shutdownTimeout: 5 * time.Second,
		readyTimeout:    timeoutReadyHandshake,
		fatal:           func(err error) { panic(err) },
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// newGeneration constructs the channel set for a fresh subprocess
// generation. Called by launch() before exec.
func newGeneration() *generation {
	return &generation{
		stopCh: make(chan struct{}),
		killCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// BaseURL returns the http://127.0.0.1:NNNN base URL of the running bridge.
// Valid after Start has returned nil; empty otherwise. After Restart it
// reflects the new bridge's port.
func (s *Supervisor) BaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen == nil || s.gen.port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", s.gen.port)
}

// Token returns the bearer token the bridge accepts. Valid after Start has
// returned nil; empty otherwise. After Restart it reflects the new bridge's
// token.
func (s *Supervisor) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen == nil {
		return ""
	}
	return s.gen.token
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
	gen, err := s.launch(ctx)
	if err != nil {
		s.log.Error("bridge supervisor: launch failed", "error", err)
		return err
	}
	s.mu.Lock()
	s.gen = gen
	s.mu.Unlock()
	s.log.Info("bridge supervisor: ready",
		"binary", s.binaryPath,
		"port", gen.port, "pid", gen.cmd.Process.Pid)
	go s.watchdog(gen)
	return nil
}

// Restart kills the current bridge subprocess (SIGKILL — it's wedged) and
// launches a fresh one (🎯T47). On success the supervisor's BaseURL/Token
// reflect the new bridge atomically; in-flight Client calls that retry
// after a transport error will land on the new bridge transparently.
//
// It is an error to call Restart before Start, or after Stop. Concurrent
// Restart calls are serialised via the supervisor mu and the second
// caller observes the new generation.
func (s *Supervisor) Restart(ctx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return fmt.Errorf("pmd3bridge: Restart called after Stop")
	}
	prev := s.gen
	if prev == nil {
		s.mu.Unlock()
		return fmt.Errorf("pmd3bridge: Restart called before Start")
	}
	prev.restarting = true
	s.mu.Unlock()

	if prev.cmd != nil && prev.cmd.Process != nil {
		s.log.Warn("bridge supervisor: SIGKILLing wedged subprocess",
			"pid", prev.cmd.Process.Pid)
		_ = syscall.Kill(-prev.cmd.Process.Pid, syscall.SIGKILL)
	}
	// Cue the watchdog to wind up: closing stopCh tells it the exit is
	// expected, killCh tells it to escalate immediately if the SIGKILL
	// above raced with a planned SIGTERM path.
	close(prev.stopCh)
	select {
	case <-prev.killCh:
	default:
		close(prev.killCh)
	}
	<-prev.doneCh

	gen, err := s.launch(ctx)
	if err != nil {
		s.log.Error("bridge supervisor: restart launch failed", "error", err)
		return err
	}
	s.mu.Lock()
	s.gen = gen
	s.mu.Unlock()
	s.log.Info("bridge supervisor: restart ready",
		"port", gen.port, "pid", gen.cmd.Process.Pid)
	go s.watchdog(gen)
	return nil
}

// Stop signals the watchdog to shut down the bridge process and waits for it
// to finish. It sends SIGTERM, then after the shutdown timeout sends SIGKILL.
// It is safe to call multiple times.
func (s *Supervisor) Stop(shutdownCtx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		gen := s.gen
		s.mu.Unlock()
		if gen != nil {
			<-gen.doneCh
		}
		return nil
	}
	s.stopped = true
	gen := s.gen
	s.mu.Unlock()
	if gen == nil {
		return nil
	}

	s.log.Info("bridge supervisor: stopping", "shutdown_timeout", s.shutdownTimeout)

	// Signal the watchdog to stop. The watchdog will SIGTERM the process
	// and close doneCh when it exits.
	close(gen.stopCh)

	// Arm a timer to close killCh after the shutdown timeout, which tells
	// the watchdog to SIGKILL.
	go func() {
		timer := time.NewTimer(s.shutdownTimeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			close(gen.killCh)
		case <-gen.doneCh:
			// process already exited; no need to kill
		}
	}()

	// Block until the watchdog goroutine finishes.
	select {
	case <-gen.doneCh:
	case <-shutdownCtx.Done():
		// shutdownCtx itself timed out; close killCh to force a SIGKILL and
		// wait for the watchdog regardless (we don't abandon the goroutine).
		select {
		case <-gen.killCh:
		default:
			close(gen.killCh)
		}
		<-gen.doneCh
	}
	return nil
}

// launch starts the bridge process and waits for the structured
// `ready port=NNNN token=XXXX` line on stdout. Returns a fresh
// generation populated with cmd/port/token/livenessPipe and unsignalled
// channels; the caller is responsible for storing it on the supervisor
// and starting the watchdog goroutine.
func (s *Supervisor) launch(ctx context.Context) (*generation, error) {
	gen := newGeneration()
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
		return nil, fmt.Errorf("pmd3bridge supervisor: liveness pipe: %w", err)
	}
	cmd.Stdin = livenessReadEnd

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		livenessReadEnd.Close()
		livenessWriteEnd.Close()
		return nil, fmt.Errorf("pmd3bridge supervisor: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		livenessReadEnd.Close()
		livenessWriteEnd.Close()
		return nil, fmt.Errorf("pmd3bridge supervisor: start %q: %w", s.binaryPath, err)
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
			return nil, fmt.Errorf("pmd3bridge supervisor: %w", result.err)
		}
	case <-timer.C:
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		livenessWriteEnd.Close()
		return nil, fmt.Errorf("pmd3bridge supervisor: timed out waiting for bridge ready signal after %s", s.readyTimeout)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		livenessWriteEnd.Close()
		return nil, ctx.Err()
	}

	gen.cmd = cmd
	gen.port = result.port
	gen.token = result.token
	gen.livenessPipe = livenessWriteEnd
	return gen, nil
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

// watchdog waits for the subprocess to exit. If neither Stop nor Restart
// is in progress, subprocess exit is a bug: the supervisor invokes fatal
// to crash the daemon, which the external process supervisor restarts.
//
// We do not attempt restart-on-crash in this layer. A crashed bridge
// indicates a bug somewhere — in the bridge itself, in the transport,
// or in our use of it — and the only sound recovery for an unexpected
// crash is to surface it and let the external supervisor restart the
// whole daemon. The Restart() path (🎯T47) is for the orthogonal case
// of "process listening but unresponsive": the parent decides to kill,
// and the watchdog must not panic on the resulting exit.
func (s *Supervisor) watchdog(gen *generation) {
	defer close(gen.doneCh)

	cmd := gen.cmd
	pipe := gen.livenessPipe
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
	case <-gen.stopCh:
		// Graceful shutdown or planned restart requested. Signal the whole
		// process group (negative PID) so child uv/python processes from
		// the dev wrapper are included in the SIGTERM.
		if cmd.Process != nil {
			s.log.Info("bridge supervisor: sending SIGTERM", "pid", cmd.Process.Pid)
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
		select {
		case err := <-exitCh:
			s.log.Info("bridge supervisor: subprocess exited cleanly", "error", err)
		case <-gen.killCh:
			if cmd.Process != nil {
				s.log.Warn("bridge supervisor: shutdown timeout fired, sending SIGKILL",
					"pid", cmd.Process.Pid)
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			<-exitCh
		}

	case err := <-exitCh:
		// Subprocess exited without Stop or Restart being called.
		// Check once more (race with Stop / Restart) — if either closed
		// stopCh between cmd.Wait() returning and us selecting, honour
		// that.
		select {
		case <-gen.stopCh:
			return
		default:
		}
		s.mu.Lock()
		stopped := s.stopped
		restarting := gen.restarting
		s.mu.Unlock()
		if stopped || restarting {
			return
		}
		s.log.Error("bridge supervisor: subprocess exited unexpectedly",
			"wait_error", err, "pid", cmd.Process.Pid)
		s.fatal(fmt.Errorf("pmd3bridge: subprocess exited unexpectedly (this is a bug): %w", err))
	}
}

// LivenessProbeInterval reports the cadence of LivenessProbe checks. Exported
// for tests; production paths just call LivenessProbe.
func LivenessProbeInterval() time.Duration { return intervalLivenessProbe }

// LivenessProbe runs in a background goroutine and triggers a supervisor
// restart if the bridge becomes unresponsive. It calls client.Health every
// intervalLivenessProbe (30 s) — a no-op endpoint that touches no device
// state (🎯T50), so a wedged device path does not look like a wedged
// bridge. Returns when ctx is cancelled.
//
// Wedge detection (🎯T47): if Health fails with a transport error
// livenessWedgeThreshold consecutive times, the bridge is presumed wedged
// (process listening but not answering) and the supervisor's Restart() is
// invoked, which SIGKILLs the wedged subprocess and respawns a fresh one.
// In-flight callers see one or two transport errors during the restart
// window; subsequent calls connect to the new bridge transparently.
//
// This complements watchdog()'s exit-detection: watchdog catches "process
// died", LivenessProbe catches "process alive but not answering" (the Uvicorn
// wedge observed on 2026-04-22 and again on 2026-04-28).
//
// Logging (🎯T26.5): emits an INFO `bridge liveness: started` on entry, a
// periodic INFO heartbeat every livenessHeartbeatEvery ticks (≈1 h at the
// 30 s cadence) including consecutive-ok count and last-call duration, and
// a WARN per failure with the running consecutive-failure count.
func LivenessProbe(ctx context.Context, client *Client) {
	livenessProbe(ctx, client, client.sup)
}

// livenessProbe is the testable core. The exported LivenessProbe wires in
// the Client's Supervisor at the production interval; tests pass a custom
// restarter and a shorter interval to assert wedge-detection without
// spawning a real subprocess or waiting tens of seconds per tick.
func livenessProbe(ctx context.Context, client *Client, restarter livenessRestarter) {
	livenessProbeAt(ctx, client, restarter, intervalLivenessProbe)
}

// livenessProbeAt is the test-only entry point; it accepts an interval
// override so tests can drive the loop at millisecond cadence.
func livenessProbeAt(ctx context.Context, client *Client, restarter livenessRestarter,
	interval time.Duration,
) {
	slog.Info("bridge liveness: started",
		"interval", interval,
		"heartbeat_every", livenessHeartbeatEvery,
		"wedge_threshold", livenessWedgeThreshold)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var consecutiveOk int
	var consecutiveFail int
	var lastDuration time.Duration

	for {
		select {
		case <-ctx.Done():
			slog.Info("bridge liveness: stopped",
				"consecutive_ok", consecutiveOk,
				"consecutive_fail", consecutiveFail)
			return
		case <-ticker.C:
			started := time.Now()
			_, err := client.Health(ctx)
			lastDuration = time.Since(started)

			if err != nil {
				if errors.Is(err, context.Canceled) {
					continue
				}
				consecutiveOk = 0
				consecutiveFail++
				slog.Warn("bridge liveness: probe failed",
					"error", err,
					"consecutive_fail", consecutiveFail,
					"wedge_threshold", livenessWedgeThreshold,
					"last_duration_ms", lastDuration.Milliseconds())
				if consecutiveFail >= livenessWedgeThreshold && restarter != nil {
					slog.Error("bridge liveness: wedge detected — restarting bridge",
						"consecutive_fail", consecutiveFail)
					if rerr := restarter.Restart(ctx); rerr != nil {
						slog.Error("bridge liveness: restart failed",
							"error", rerr)
					} else {
						slog.Info("bridge liveness: restart complete")
						consecutiveFail = 0
					}
				}
				continue
			}
			consecutiveFail = 0
			consecutiveOk++
			if consecutiveOk%livenessHeartbeatEvery == 0 {
				slog.Info("bridge liveness: healthy",
					"consecutive_ok", consecutiveOk,
					"last_duration_ms", lastDuration.Milliseconds())
			}
		}
	}
}

// livenessRestarter is the subset of Supervisor that LivenessProbe uses.
// Extracted so tests can inject a fake without spinning up a real bridge.
type livenessRestarter interface {
	Restart(ctx context.Context) error
}

// livenessHeartbeatEvery is the number of consecutive successful probes
// between INFO heartbeat logs. With intervalLivenessProbe=30s, 120 ≈ 1 h.
const livenessHeartbeatEvery = 120

// livenessWedgeThreshold is the number of consecutive Health-probe
// failures that triggers a SIGKILL+respawn of the bridge subprocess
// (🎯T47). At intervalLivenessProbe=30 s, 3 = ~90 s of unresponsiveness
// before recovery — long enough to ride out a transient network blip
// or a tunneld restart, short enough that an MCP user does not give
// up before the bridge comes back.
const livenessWedgeThreshold = 3
