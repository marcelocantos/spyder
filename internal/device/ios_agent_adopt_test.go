// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
)

// 🎯T233: a previous daemon's runwda whose runner still answers is
// adopted, not killed. On 2026-11-22 a self-restart SIGTERMed two such
// hosts holding 55 h of approved automation; the fresh launches then
// timed out enabling automation (iOS 27.2.1).
func TestDeviceKit_HealthyOrphanIsAdopted(t *testing.T) {
	var pid atomic.Int64
	pid.Store(6624)
	host := dkHost(t, &pid)
	orphan := runwdaProc{PID: 85959, PPID: 1, Binary: "/opt/homebrew/Cellar/spyder/0.111.0/libexec/spyder/ios", Bundle: DeviceKitRunnerBundle}
	var mu sync.Mutex
	hostUp := true
	var sigs []syscall.Signal
	host.hostScan = func(string) []runwdaProc { return []runwdaProc{orphan} }
	host.hostAlive = func(p int) bool {
		mu.Lock()
		defer mu.Unlock()
		return p == orphan.PID && hostUp
	}
	host.hostSignal = func(_ int, sig syscall.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		sigs = append(sigs, sig)
		return nil
	}
	host.readyProbe = func(_ string, p int) bool { return p == 6624 }
	started := 0
	host.holdRun = func(ctx context.Context, _ string, _ io.Writer) error { started++; <-ctx.Done(); return nil }

	snap, err := dkSupervisor(host).Tick("dut")
	if err != nil || !snap.Ready || snap.Relaunched || snap.Runner.PID != 6624 {
		t.Fatalf("startup tick %+v err=%v", snap, err)
	}
	mu.Lock()
	n := len(sigs)
	mu.Unlock()
	if started != 0 || host.LaunchCount() != 0 || n != 0 || pid.Load() != 6624 {
		t.Fatalf("adoption touched the runner: started=%d launches=%d signals=%d pid=%d", started, host.LaunchCount(), n, pid.Load())
	}
	if !host.ownsHost(orphan.PID) {
		t.Fatal("adopted host is not owned")
	}
	// Later ticks keep it: no relaunch.
	if snap, err = dkSupervisor(host).Tick("dut"); err != nil || !snap.Ready || snap.Relaunched {
		t.Fatalf("second tick %+v err=%v", snap, err)
	}
	// The adopted host exits on its own: the hold ends.
	mu.Lock()
	hostUp = false
	mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for host.ownsHost(orphan.PID) {
		if time.Now().After(deadline) {
			t.Fatal("hold outlived its adopted host")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// An orphan whose runner does not answer is still replaced.
func TestDeviceKit_UnhealthyOrphanIsReplaced(t *testing.T) {
	var pid atomic.Int64
	pid.Store(3740)
	host := dkHost(t, &pid)
	orphan := runwdaProc{PID: 82857, PPID: 1, Binary: "/opt/homebrew/Cellar/spyder/0.111.0/libexec/spyder/ios", Bundle: DeviceKitRunnerBundle}
	var gone atomic.Bool
	host.hostScan = func(string) []runwdaProc {
		if gone.Load() {
			return nil
		}
		return []runwdaProc{orphan}
	}
	host.hostAlive = func(p int) bool { return p == orphan.PID && !gone.Load() }
	host.hostSignal = func(int, syscall.Signal) error { gone.Store(true); return nil }
	host.readyProbe = func(_ string, p int) bool { return p == 91 }
	host.holdRun = func(ctx context.Context, _ string, _ io.Writer) error { pid.Store(91); <-ctx.Done(); return nil }
	t.Cleanup(host.Shutdown)
	snap, err := dkSupervisor(host).Tick("dut")
	if err != nil || !snap.Ready || !snap.Relaunched || snap.Runner.PID != 91 || !gone.Load() {
		t.Fatalf("tick %+v err=%v gone=%v", snap, err, gone.Load())
	}
}

// Shutdown leaves an established DeviceKit runwda running for the next
// daemon, and still stops launches that are not established.
func TestShutdown_LeavesEstablishedDeviceKitHosts(t *testing.T) {
	var pid atomic.Int64
	host := dkHost(t, &pid)
	host.DeviceKit.devices["pending"] = true
	var keptCancelled, pendingCancelled atomic.Bool
	kept := &runnerHold{pid: 6624, hostPID: 85959, done: make(chan struct{}), cancel: func() { keptCancelled.Store(true) }}
	pendingDone := make(chan struct{})
	pending := &runnerHold{pid: 0, hostPID: 777, pending: true, done: pendingDone, cancel: func() {
		pendingCancelled.Store(true)
		close(pendingDone)
	}}
	host.holds["dut"], host.holds["pending"] = kept, pending
	host.Shutdown()
	if keptCancelled.Load() {
		t.Fatal("shutdown stopped an established DeviceKit runwda")
	}
	if !pendingCancelled.Load() {
		t.Fatal("shutdown left a launch waiting on consent running")
	}
}

// runwda writes to a file, not a pipe to the daemon: with a pipe, its
// first write after the daemon exits kills it with SIGPIPE, and no
// restart could keep it.
func TestRunDeviceKit_OutputGoesToAFile(t *testing.T) {
	t.Setenv(paths.DataDirEnv, t.TempDir())
	exe := filepath.Join(t.TempDir(), "ios")
	script := "#!/bin/sh\nif [ -p /dev/stdout ]; then echo PIPE; else echo FILE; fi\nexit 3\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	h := NewAutomationHost(nil)
	h.DeviceKit = &DeviceKitAgent{CLI: exe, devices: map[string]bool{"dut": true}}
	out := &tailBuf{}
	err := h.runDeviceKit(context.Background(), "dut", out, nil)
	if err == nil {
		t.Fatal("exit 3 reported as success")
	}
	if got := strings.TrimSpace(out.string()); got != "FILE" {
		t.Fatalf("runwda output = %q (err %v); want it written to a file", got, err)
	}
	if _, statErr := os.Stat(runwdaLogPath("dut")); statErr != nil {
		t.Fatal(statErr)
	}
}
