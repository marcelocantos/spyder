// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package iostunnel manages the bundled go-ios tunnel daemon as a
// child process of spyder. The tunnel exposes a TUN endpoint per
// connected iOS-17+ device and a registry HTTP API on
// 127.0.0.1:60105 — the address the in-process goios.Resolver hits
// to look up each device's RSD address.
//
// We start the tunnel in --userspace mode so it doesn't need root —
// userspace mode handles the L3 routing in user space rather than
// installing a kernel TUN device, eliminating any sudo requirement.
// Trade-off: marginally higher per-packet overhead in exchange for
// a vastly simpler deployment story (no system LaunchDaemon, no
// privilege boundary).
//
// The supervisor spawns the subprocess, restarts it on unexpected
// exit, and runs an HTTP liveness probe against the registry to
// catch the "process alive but tunnel info wedged" failure mode
// (🎯T84) — a real symptom seen in the field where the daemon
// responds with `TunnelInfoForDevice: unexpected end of JSON` while
// the subprocess itself stays up indefinitely. On N consecutive
// probe failures the supervisor sends SIGTERM so the existing exit-
// driven restart loop respawns.
package iostunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/danielpaulus/go-ios/ios/tunnel"
	"github.com/marcelocantos/spyder/internal/paths"
)

// Tunables for the health probe. Declared as vars so tests can
// shorten them; production defaults are conservative.
var (
	// HealthProbeInterval is the cadence at which the supervisor pings
	// the tunnel registry to confirm it's responsive.
	HealthProbeInterval = 10 * time.Second
	// HealthProbeFailureThreshold is the number of consecutive failing
	// probes that triggers a forced restart. 3 × 10 s = ~30 s tolerance
	// before we declare the daemon wedged.
	HealthProbeFailureThreshold = 3
)

// Supervisor manages an `ios tunnel start --userspace` subprocess.
type Supervisor struct {
	binPath   string
	probeHost string
	probePort int

	mu        sync.Mutex
	cmd       *exec.Cmd
	probeStop chan struct{} // closed to stop the current probe goroutine
}

// New returns a Supervisor for the ios binary at binPath. The
// binary isn't invoked until Start. probeHost/probePort target the
// tunnel daemon's registry HTTP endpoint (typically 127.0.0.1:60105);
// pass "" / 0 to use the defaults.
func New(binPath, probeHost string, probePort int) *Supervisor {
	if probeHost == "" {
		probeHost = "127.0.0.1"
	}
	if probePort == 0 {
		probePort = 60105
	}
	return &Supervisor{
		binPath:   binPath,
		probeHost: probeHost,
		probePort: probePort,
	}
}

// Start launches `ios tunnel start --userspace` and returns once the
// subprocess is running. The subprocess inherits the parent's stderr
// (which is what go-ios uses for its structured JSON log lines), so
// tunnel logs interleave with spyder's slog output.
//
// If startup itself fails (binary missing, exec error), Start returns
// the error and the Supervisor remains in the un-started state.
// Once Start succeeds, callers should defer Stop to ensure the
// child is reaped on shutdown.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		return errors.New("iostunnel: already started")
	}

	if err := s.startLocked(ctx); err != nil {
		return err
	}

	return nil
}

func (s *Supervisor) startLocked(ctx context.Context) error {
	// Use plain exec.Command (not exec.CommandContext) so ctx
	// cancellation alone doesn't kill the child — Stop owns the
	// teardown sequence and needs to send SIGTERM first for clean
	// shutdown of the user-space TUN.
	cmd := exec.Command(s.binPath, "tunnel", "start", "--userspace")

	// go-ios writes selfIdentity.plist (and the per-device pair
	// records) to its cwd. Under launchctl / brew services the cwd
	// is "/" which is read-only, so the tunnel exits immediately
	// with "open selfIdentity.plist: read-only file system". Pin the
	// cwd to ~/.spyder/iostunnel so the pair records have a stable,
	// writable home that survives across spyder restarts.
	tunnelCwd := filepath.Join(paths.Base(), "iostunnel")
	if err := os.MkdirAll(tunnelCwd, 0o755); err != nil {
		return fmt.Errorf("iostunnel: create cwd %s: %w", tunnelCwd, err)
	}
	cmd.Dir = tunnelCwd

	// Detach from spyder's controlling terminal. Without Setpgid the
	// tunnel inherits spyder's pgid and would receive Ctrl-C / SIGINT
	// directly — racing Stop's structured shutdown.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("iostunnel: start %s: %w", s.binPath, err)
	}
	s.cmd = cmd
	probeStop := make(chan struct{})
	s.probeStop = probeStop
	slog.Info("iostunnel: started", "binary", s.binPath, "pid", cmd.Process.Pid)

	// Reap the process when it exits so we don't leak zombies.
	// Surface unexpected exits at warn level — a healthy tunnel
	// should run for the lifetime of spyder.
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		started := s.cmd != nil
		s.cmd = nil
		// Stop the health probe attached to this incarnation.
		if s.probeStop == probeStop {
			close(s.probeStop)
			s.probeStop = nil
		}
		s.mu.Unlock()
		// If Stop nilled cmd already, this is the orderly-shutdown path.
		if !started {
			return
		}
		if err != nil {
			slog.Error("iostunnel: subprocess exited", "error", err)
		} else {
			slog.Error("iostunnel: subprocess exited unexpectedly (no error)")
		}
		go s.restartLoop(ctx)
	}()

	// Health probe: HTTP-pings the registry; if it stops responding,
	// SIGTERM the daemon so the exit-driven restart loop above takes
	// over. This is the only path that catches the
	// "process up, registry wedged" failure mode (🎯T84).
	go s.healthProbe(ctx, cmd.Process.Pid, probeStop)

	return nil
}

// healthProbe pings the tunnel registry every HealthProbeInterval.
// After HealthProbeFailureThreshold consecutive failures, SIGTERM the
// subprocess so the Wait-driven goroutine in startLocked fires its
// restartLoop. Returns when stopCh is closed (subprocess exited or
// Stop was called) or ctx is cancelled.
func (s *Supervisor) healthProbe(ctx context.Context, pid int, stopCh <-chan struct{}) {
	t := time.NewTicker(HealthProbeInterval)
	defer t.Stop()
	consecFails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		case <-t.C:
		}
		_, err := tunnel.ListRunningTunnels(s.probeHost, s.probePort)
		if err == nil {
			if consecFails > 0 {
				slog.Info("iostunnel: health probe recovered",
					"pid", pid, "after_failures", consecFails)
			}
			consecFails = 0
			continue
		}
		consecFails++
		slog.Warn("iostunnel: health probe failed",
			"pid", pid, "consec_fails", consecFails,
			"threshold", HealthProbeFailureThreshold, "error", err.Error())
		if consecFails < HealthProbeFailureThreshold {
			continue
		}
		// Wedged. SIGKILL the daemon — a wedged process by definition
		// isn't responding to graceful signals (its trap, if any,
		// would already have let the registry recover). The Wait
		// goroutine will see the exit and the restartLoop will
		// respawn. Take the lock to avoid racing Stop.
		s.mu.Lock()
		cmd := s.cmd
		s.mu.Unlock()
		if cmd == nil || cmd.Process == nil {
			return
		}
		slog.Error("iostunnel: registry wedged; killing for restart",
			"pid", pid, "consec_fails", consecFails)
		_ = cmd.Process.Kill()
		return
	}
}

func (s *Supervisor) restartLoop(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		s.mu.Lock()
		if s.cmd != nil {
			s.mu.Unlock()
			return
		}
		err := s.startLocked(ctx)
		s.mu.Unlock()
		if err == nil {
			return
		}

		slog.Error("iostunnel: restart failed", "error", err, "retry_in", backoff.String())
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

// Stop signals the tunnel subprocess to exit (SIGTERM, then SIGKILL
// after a grace period) and waits for it to reap. Safe to call even
// if Start was never called or has already returned.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	cmd := s.cmd
	s.cmd = nil
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		// Already exited, or no permission. Best-effort.
		slog.Error("iostunnel: SIGTERM failed; falling through", "error", err)
	}

	// Wait up to 3 s for clean shutdown, then SIGKILL. The Wait()
	// call in the Start goroutine may already have run; we need our
	// own bounded wait independent of it.
	done := make(chan error, 1)
	go func() {
		_, err := cmd.Process.Wait()
		done <- err
	}()

	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	select {
	case err := <-done:
		if err != nil && !isAlreadyReaped(err) {
			slog.Info("iostunnel: process Wait returned error (likely already reaped)", "error", err)
		}
		slog.Info("iostunnel: stopped cleanly")
		return nil
	case <-deadline.C:
		_ = cmd.Process.Kill()
		<-done
		slog.Warn("iostunnel: SIGTERM timed out; SIGKILLed")
		return nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
}

// isAlreadyReaped is true when Process.Wait fails because the
// goroutine in Start already reaped the process. ECHILD is what the
// kernel returns in that case on macOS / Linux.
func isAlreadyReaped(err error) bool {
	var pathErr *os.SyscallError
	if errors.As(err, &pathErr) && errors.Is(pathErr.Err, syscall.ECHILD) {
		return true
	}
	// Some platforms return a wrapped error string; do a substring
	// match as a defensive fallback.
	return err == io.EOF
}
