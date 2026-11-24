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
	"strconv"
	"strings"
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
	// adoptedPID is a previous daemon's tunnel this supervisor took over
	// (🎯T236). It is not our child, so it is watched and signalled by pid.
	adoptedPID int
	// released means shutdown left the tunnel running for the next
	// daemon; Stop is then a no-op.
	released bool
}

// Process seams for tests.
var (
	listProcs = psProcs
	pidAlive  = func(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }
	signalPID = func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
)

type proc struct {
	PID, PPID int
	Args      []string
}

func psProcs() ([]proc, error) {
	out, err := exec.Command("ps", "-axww", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, err
	}
	var ps []proc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		ps = append(ps, proc{PID: pid, PPID: ppid, Args: f[2:]})
	}
	return ps, nil
}

// isOrphanTunnel reports whether p is an `ios tunnel start --userspace`
// whose parent has exited (reparented to launchd), i.e. one a previous
// spyder daemon left behind. A tunnel with a live parent belongs to a
// running daemon (the :3030 daemon, seen from a :3131 dev daemon) and is
// never taken over.
func isOrphanTunnel(p proc) bool {
	if p.PPID != 1 || len(p.Args) < 4 || filepath.Base(p.Args[0]) != "ios" {
		return false
	}
	return p.Args[1] == "tunnel" && p.Args[2] == "start" && contains(p.Args[3:], "--userspace")
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// AdoptOrphan takes over a tunnel a previous daemon left running for
// its resident runners (🎯T236): exactly one orphaned `ios tunnel start
// --userspace` with the registry answering. From then on Alive, Start
// and Stop treat it as this supervisor's tunnel, so an unhealthy one is
// replaced. Its binary may be an older release's; it stays until it
// fails or a shutdown with no resident runners stops it.
func (s *Supervisor) AdoptOrphan() (pid int, ok bool) {
	if s == nil || !s.RegistryListening() {
		return 0, false
	}
	ps, err := listProcs()
	if err != nil {
		return 0, false
	}
	var found []proc
	for _, p := range ps {
		if isOrphanTunnel(p) {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil || s.adoptedPID != 0 {
		return 0, false
	}
	s.adoptedPID = found[0].PID
	slog.Info("iostunnel: adopted the previous daemon's tunnel", "pid", found[0].PID, "binary", found[0].Args[0])
	return found[0].PID, true
}

// Release leaves the tunnel running when the daemon exits, because
// resident runners talk to their devices through it (🎯T236). Stop is
// a no-op afterwards.
func (s *Supervisor) Release() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = true
	pid := s.adoptedPID
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	if pid > 0 {
		slog.Info("iostunnel: leaving the tunnel running for the next daemon", "pid", pid)
	}
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

// Alive implements health.ManagedProcess: process running AND registry responsive.
func (s *Supervisor) Alive() bool {
	s.mu.Lock()
	running := s.cmd != nil && s.cmd.Process != nil
	adopted := s.adoptedPID
	s.mu.Unlock()
	if adopted > 0 {
		running = pidAlive(adopted)
	}
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
	// A healthy tunnel, ours or adopted, is already what Start asks for;
	// replacing it would drop every resident runner riding on it.
	if s.Alive() {
		return nil
	}
	s.mu.Lock()
	have := s.cmd != nil || s.adoptedPID != 0
	s.mu.Unlock()
	if have {
		_ = s.Stop(ctx)
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
	s.cmd = cmd
	slog.Info("iostunnel: started", "binary", s.binPath, "pid", cmd.Process.Pid)

	// Reap the process when it exits so we don't leak zombies.
	// Restart is owned by health.Supervisor.Supervise (🎯T90.5.1): when
	// Alive() goes false, Supervise calls Start again. Do not restart here.
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		// Only clear if this Wait matches the current cmd (not a newer Start).
		if s.cmd == cmd {
			s.cmd = nil
		}
		s.mu.Unlock()
		if err != nil {
			slog.Error("iostunnel: subprocess exited", "error", err)
		} else {
			slog.Info("iostunnel: subprocess exited")
		}
	}()

	return nil
}

// Stop signals the tunnel subprocess to exit (SIGTERM, then SIGKILL
// after a grace period) and waits for it to reap. Safe to call even
// if Start was never called or has already returned.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return nil
	}
	cmd := s.cmd
	s.cmd = nil
	adopted := s.adoptedPID
	s.adoptedPID = 0
	s.mu.Unlock()
	if adopted > 0 {
		return stopAdopted(ctx, adopted)
	}
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

	deadline := time.NewTimer(stopGrace)
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

// stopAdopted ends an adopted tunnel, which is not our child: SIGTERM,
// then SIGKILL if it is still up after the grace period.
func stopAdopted(ctx context.Context, pid int) error {
	_ = signalPID(pid, syscall.SIGTERM)
	deadline := time.Now().Add(stopGrace)
	for time.Now().Before(deadline) && pidAlive(pid) {
		select {
		case <-ctx.Done():
			_ = signalPID(pid, syscall.SIGKILL)
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if pidAlive(pid) {
		_ = signalPID(pid, syscall.SIGKILL)
		slog.Warn("iostunnel: adopted tunnel ignored SIGTERM; SIGKILLed", "pid", pid)
		return nil
	}
	slog.Info("iostunnel: stopped adopted tunnel", "pid", pid)
	return nil
}

// stopGrace is how long Stop waits after SIGTERM before SIGKILL.
var stopGrace = 3 * time.Second

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
