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
	"fmt"
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

// Legacy probe tunables retained for test code that still sets them;
// production liveness is health.Supervisor.Alive via ListRunningTunnels.
var (
	HealthProbeInterval         = 10 * time.Second
	HealthProbeFailureThreshold = 3
)

// Supervisor manages an `ios tunnel start --userspace` subprocess.
// Restart/liveness is owned by health.Supervisor.Supervise (🎯T90.5.1) —
// this type does not run its own restartLoop or healthProbe.
type Supervisor struct {
	binPath   string
	probeHost string
	probePort int

	mu  sync.Mutex
	cmd *exec.Cmd
	// exited is closed by cmd's reaper once cmd has exited and s.cmd no
	// longer names it. Every Stop waits on it.
	exited chan struct{}
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

// Name implements health.ManagedProcess (🎯T90.5.1).
func (s *Supervisor) Name() string { return "ios-tunnel" }

// RegistryListening reports whether a tunnel registry is already serving
// this supervisor's probe address. It does not say this supervisor owns
// that process.
func (s *Supervisor) RegistryListening() bool {
	if s == nil {
		return false
	}
	_, err := tunnel.ListRunningTunnels(s.probeHost, s.probePort)
	return err == nil
}

// Running reports whether the tunnel child is running, whether or not
// its registry answers yet. health.Supervise uses it to adopt a tunnel
// the daemon has already started instead of restarting it.
func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd != nil && s.cmd.Process != nil
}

// Alive implements health.ManagedProcess: process running AND registry responsive.
func (s *Supervisor) Alive() bool {
	s.mu.Lock()
	running := s.cmd != nil && s.cmd.Process != nil
	s.mu.Unlock()
	if !running {
		return false
	}
	_, err := tunnel.ListRunningTunnels(s.probeHost, s.probePort)
	return err == nil
}

// Start launches `ios tunnel start --userspace` and returns once the
// subprocess is running. The subprocess inherits the parent's stderr
// (which is what go-ios uses for its structured JSON log lines), so
// tunnel logs interleave with spyder's slog output.
//
// If already running, Start kills and respawns (health.Supervisor
// restart path, 🎯T90.5.1). If startup itself fails (binary missing,
// exec error), Start returns the error.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.cmd != nil {
		s.mu.Unlock()
		_ = s.Stop(ctx)
	} else {
		s.mu.Unlock()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		// Another Start won the race.
		return nil
	}
	return s.startLocked(ctx)
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
	exited := make(chan struct{})
	s.cmd, s.exited = cmd, exited
	slog.Info("iostunnel: started", "binary", s.binPath, "pid", cmd.Process.Pid)

	// Reap the process when it exits so we don't leak zombies.
	// Restart is owned by health.Supervisor.Supervise (🎯T90.5.1): when
	// Alive() goes false, Supervise calls Start again. Do not restart here.
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		// Only clear if this Wait matches the current cmd (not a newer Start).
		if s.cmd == cmd {
			s.cmd, s.exited = nil, nil
		}
		s.mu.Unlock()
		close(exited)
		if err != nil {
			slog.Error("iostunnel: subprocess exited", "error", err)
		} else {
			slog.Info("iostunnel: subprocess exited")
		}
	}()

	return nil
}

// Stop signals the tunnel subprocess to exit (SIGTERM, then SIGKILL
// after a grace period) and returns once it has exited. Safe to call
// even if Start was never called or has already returned. Concurrent
// Stops all wait for the exit: at shutdown health.Supervise and the
// daemon both stop the tunnel, and a Stop that returned early let the
// daemon exit before a tunnel slow to honour SIGTERM was killed (DH-38).
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	cmd, exited := s.cmd, s.exited
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		// Already exited, or no permission. Best-effort.
		slog.Error("iostunnel: SIGTERM failed; falling through", "error", err)
	}

	// Wait up to 3 s for clean shutdown, then SIGKILL.
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	select {
	case <-exited:
		slog.Info("iostunnel: stopped cleanly")
		return nil
	case <-deadline.C:
		_ = cmd.Process.Kill()
		<-exited
		slog.Warn("iostunnel: SIGTERM timed out; SIGKILLed")
		return nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-exited
		return ctx.Err()
	}
}
