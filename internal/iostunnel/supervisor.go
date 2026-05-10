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
// installing a kernel TUN device, eliminating the sudo requirement
// that pmd3-tunneld used to carry. Trade-off: marginally higher
// per-packet overhead in exchange for a vastly simpler deployment
// story (no system LaunchDaemon, no privilege boundary).
//
// The supervisor is intentionally minimal: spawn, log, wait, kill.
// No readiness handshake (the registry is just-an-HTTP-server, comes
// up immediately), no auth tokens (the registry is loopback-only),
// no liveness probes (a wedged tunnel surfaces as call-level errors
// in goios.Resolver.Session, where the cache invalidation already
// drives recovery).
package iostunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Supervisor manages an `ios tunnel start --userspace` subprocess.
type Supervisor struct {
	binPath string

	mu  sync.Mutex
	cmd *exec.Cmd
}

// New returns a Supervisor for the ios binary at binPath. The
// binary isn't invoked until Start.
func New(binPath string) *Supervisor {
	return &Supervisor{binPath: binPath}
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

	// Use plain exec.Command (not exec.CommandContext) so ctx
	// cancellation alone doesn't kill the child — Stop owns the
	// teardown sequence and needs to send SIGTERM first for clean
	// shutdown of the user-space TUN.
	cmd := exec.Command(s.binPath, "tunnel", "start", "--userspace")

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
	slog.Info("iostunnel: started", "binary", s.binPath, "pid", cmd.Process.Pid)

	// Reap the process when it exits so we don't leak zombies.
	// Surface unexpected exits at warn level — a healthy tunnel
	// should run for the lifetime of spyder.
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		started := s.cmd != nil
		s.cmd = nil
		s.mu.Unlock()
		// If Stop nilled cmd already, this is the orderly-shutdown path.
		if !started {
			return
		}
		if err != nil {
			slog.Warn("iostunnel: subprocess exited", "error", err)
		} else {
			slog.Warn("iostunnel: subprocess exited unexpectedly (no error)")
		}
	}()

	return nil
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
		slog.Debug("iostunnel: SIGTERM failed; falling through", "error", err)
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
			slog.Debug("iostunnel: process Wait returned error (likely already reaped)", "error", err)
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
