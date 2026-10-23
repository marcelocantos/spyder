// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/agent"
)

// 🎯T175 consent handling: a launch waiting on Enable UI Automation is
// owned by its live host process for the whole consent window.

func pendingHost(t *testing.T, pid *atomic.Int64) *AutomationHost {
	t.Helper()
	host := dkHost(t, pid)
	host.launchBudget = 300 * time.Millisecond
	host.pendingPoll = 20 * time.Millisecond
	t.Cleanup(host.Shutdown)
	return host
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Fixes 1, 2, 6: the launch outlives the readiness budget, stays ours
// when the device runner pid changes under approval, is not relaunched
// or re-counted on ticks, and is ready once it answers.
func TestDeviceKit_PendingLaunchStaysOwnedUntilApproved(t *testing.T) {
	var pid atomic.Int64
	host := pendingHost(t, &pid)
	var approved atomic.Bool
	host.readyProbe = func(_ string, p int) bool { return approved.Load() && p == 88 }
	var starts atomic.Int64
	host.holdRun = func(ctx context.Context, _ string, _ io.Writer) error {
		starts.Add(1)
		pid.Store(77)
		<-ctx.Done()
		return nil
	}
	sup := dkSupervisor(host)
	snap, err := sup.Retry("dut", "alice")
	if err != nil || snap.Ready || snap.Condition != agent.CondConsentPending || !snap.ConsentPending || snap.Attempts != 1 || snap.Consent == nil {
		t.Fatalf("pending ensure %+v err=%v", snap, err)
	}
	// Approval restarts the runner under a new device pid. Still ours.
	pid.Store(88)
	for i := 0; i < agent.MaxAttempts+2; i++ {
		snap, err = sup.Tick("dut")
		if err != nil || snap.Condition != agent.CondConsentPending || snap.Attempts != 1 {
			t.Fatalf("tick %d %+v err=%v", i, snap, err)
		}
	}
	// A plain ensure (or an attended retry) does not replace it either.
	if snap, err = sup.Retry("dut", "alice"); err != nil || snap.Condition != agent.CondConsentPending {
		t.Fatalf("retry over pending %+v err=%v", snap, err)
	}
	if starts.Load() != 1 {
		t.Fatalf("pending launch replaced: starts=%d", starts.Load())
	}
	approved.Store(true)
	waitFor(t, "watcher approval", func() bool { _, ok := host.heldPID("dut"); return ok })
	snap, err = sup.Tick("dut")
	if err != nil || !snap.Ready || snap.Runner.PID != 88 || snap.Consent != nil || snap.ConsentPending || snap.Attempts != 0 {
		t.Fatalf("approved %+v err=%v", snap, err)
	}
	if starts.Load() != 1 || host.LaunchCount() != 1 {
		t.Fatalf("starts=%d launches=%d", starts.Load(), host.LaunchCount())
	}
}

// The consent window bounds the wait: the launch is stopped once,
// consent is recorded, and nothing relaunches unattended.
func TestDeviceKit_ConsentWindowStopsLaunchOnce(t *testing.T) {
	var pid atomic.Int64
	host := pendingHost(t, &pid)
	host.DeviceKit.ConsentWindow = 600 * time.Millisecond
	host.readyProbe = func(string, int) bool { return false }
	var starts atomic.Int64
	host.holdRun = func(ctx context.Context, _ string, out io.Writer) error {
		starts.Add(1)
		pid.Store(77)
		<-ctx.Done()
		pid.Store(0)
		return nil
	}
	sup := dkSupervisor(host)
	if snap, err := sup.Ensure("dut", "alice"); err != nil || snap.Condition != agent.CondConsentPending {
		t.Fatalf("pending %+v err=%v", snap, err)
	}
	waitFor(t, "window expiry", func() bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		return host.liveHold("dut") == nil && host.lastExit["dut"] != ""
	})
	var snap agent.Snapshot
	for i := 0; i < agent.MaxAttempts+2; i++ {
		var err error
		snap, err = sup.Tick("dut")
		if err != nil {
			t.Fatal(err)
		}
	}
	if snap.Condition != agent.CondConsentRequired || snap.Consent == nil || !strings.Contains(snap.Consent.Message, "no Enable UI Automation approval") || !strings.Contains(snap.LastExit, "consent window") {
		t.Fatalf("expired %+v", snap)
	}
	if starts.Load() != 1 {
		t.Fatalf("unattended relaunch after window: starts=%d", starts.Load())
	}
}

// Fix 3: every launch exit is recorded with status and output tail and
// surfaced in agent_status. A real exit counts as an attempt; a pending
// launch that exits (not on consent) may be relaunched within budget.
func TestDeviceKit_LaunchExitIsSurfacedAndCounted(t *testing.T) {
	var pid atomic.Int64
	host := pendingHost(t, &pid)
	host.readyProbe = func(string, int) bool { return false }
	var starts atomic.Int64
	exit := make(chan struct{}, 4)
	host.holdRun = func(ctx context.Context, _ string, out io.Writer) error {
		n := starts.Add(1)
		pid.Store(77)
		select {
		case <-ctx.Done():
			return nil
		case <-exit:
		}
		pid.Store(0)
		fmt.Fprintf(out, "runwda: device said no %d\n", n)
		return errors.New("exit status 3")
	}
	sup := dkSupervisor(host)
	snap, err := sup.Ensure("dut", "alice")
	if err != nil || snap.Condition != agent.CondConsentPending || snap.Attempts != 1 {
		t.Fatalf("pending %+v err=%v", snap, err)
	}
	exit <- struct{}{}
	waitFor(t, "launch exit", func() bool { return !host.ChannelOpen("dut") })
	snap, err = sup.Tick("dut")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snap.LastExit, "exit status 3") || !strings.Contains(snap.LastExit, "device said no 1") {
		t.Fatalf("last exit not surfaced: %+v", snap)
	}
	if starts.Load() != 2 || snap.Attempts != 2 || snap.Condition != agent.CondConsentPending {
		t.Fatalf("exit was not one attempt: starts=%d %+v", starts.Load(), snap)
	}
}

// Fix 4: a runner left on the device with no host on this Mac (the
// iPhone's pid 8487) is killed on the device, then launched fresh.
func TestDeviceKit_StaleRunnerWithoutHostIsTerminated(t *testing.T) {
	var pid atomic.Int64
	pid.Store(8487)
	host := dkHost(t, &pid)
	var killed []int
	host.IOS.pidProbe.Kill = func(_, bundle string, p int) error {
		if bundle != DeviceKitRunnerBundle {
			t.Errorf("killed %s", bundle)
		}
		killed = append(killed, p)
		pid.Store(0)
		return nil
	}
	host.readyProbe = func(_ string, p int) bool { return p == 90 }
	host.holdRun = func(ctx context.Context, _ string, _ io.Writer) error {
		if pid.Load() != 0 {
			return errors.New("launched over the stale runner")
		}
		pid.Store(90)
		<-ctx.Done()
		return nil
	}
	t.Cleanup(host.Shutdown)
	obs, err := host.Probe("dut")
	if err != nil || obs.Authorized || !obs.RunnerAlive {
		t.Fatalf("stale probe %+v err=%v", obs, err)
	}
	snap, err := dkSupervisor(host).Ensure("dut", "alice")
	if err != nil || !snap.Ready || snap.Runner.PID != 90 || len(killed) != 1 || killed[0] != 8487 {
		t.Fatalf("ensure %+v err=%v killed=%v", snap, err, killed)
	}
}

// Fix 5: a previous spyder daemon's runwda (its bundled ios, parent
// gone) is terminated at startup and the runner relaunched.
func TestDeviceKit_OrphanedSpyderHostIsReplaced(t *testing.T) {
	var pid atomic.Int64
	pid.Store(6838)
	host := dkHost(t, &pid)
	orphan := runwdaProc{PID: 31213, PPID: 1, Binary: "/opt/homebrew/Cellar/spyder/0.100.0/libexec/spyder/ios", Bundle: DeviceKitRunnerBundle}
	var mu sync.Mutex
	var sigs []syscall.Signal
	gone := false
	host.hostScan = func(string) []runwdaProc {
		mu.Lock()
		defer mu.Unlock()
		if gone {
			return nil
		}
		return []runwdaProc{orphan}
	}
	host.hostSignal = func(p int, sig syscall.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		if p != orphan.PID {
			t.Errorf("signalled pid %d", p)
		}
		sigs = append(sigs, sig)
		gone = true
		return nil
	}
	host.hostAlive = func(p int) bool {
		mu.Lock()
		defer mu.Unlock()
		return p == orphan.PID && !gone
	}
	host.readyProbe = func(_ string, p int) bool { return p == 91 }
	host.holdRun = func(ctx context.Context, _ string, _ io.Writer) error {
		pid.Store(91)
		<-ctx.Done()
		return nil
	}
	t.Cleanup(host.Shutdown)
	snap, err := dkSupervisor(host).Tick("dut")
	if err != nil || !snap.Ready || !snap.Relaunched || snap.Runner.PID != 91 {
		t.Fatalf("startup tick %+v err=%v", snap, err)
	}
	if len(sigs) != 1 || sigs[0] != syscall.SIGTERM {
		t.Fatalf("signals %v", sigs)
	}
}

func TestParseRunwdaProcs(t *testing.T) {
	ps := `  101     1 /opt/homebrew/Cellar/spyder/0.100.0/libexec/spyder/ios runwda --udid=` + dkUDID + ` --bundleid=` + DeviceKitRunnerBundle + ` --testrunnerbundleid=x
  102   555 /Users/x/bin/ios runwda --udid=OTHER --bundleid=y
  103   556 /usr/bin/ssh host
`
	got := parseRunwdaProcs(ps, dkUDID)
	if len(got) != 1 || got[0].PID != 101 || got[0].PPID != 1 || got[0].Bundle != DeviceKitRunnerBundle || !strings.HasSuffix(got[0].Binary, "/libexec/spyder/ios") {
		t.Fatalf("got %+v", got)
	}
}

func TestConsentWindowConfig(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{{"", DefaultConsentWindow, true}, {"15m", 15 * time.Minute, true}, {"10s", 0, false}, {"3h", 0, false}, {"soon", 0, false}} {
		got, err := parseConsentWindow(tc.in)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("%q: got %s err=%v", tc.in, got, err)
		}
	}
}
