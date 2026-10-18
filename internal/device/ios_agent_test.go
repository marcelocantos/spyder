// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/agent"
)

func TestNotifyTether_ReachesHook(t *testing.T) {
	a := &IOSAdapter{}
	var got []string
	a.SetTetherNotify(func(udid string, up bool) {
		got = append(got, udid+":"+boolWord(up))
	})
	a.notifyTether("phone", false)
	a.notifyTether("phone", true)
	a.notifyTether("", true)
	if strings.Join(got, ",") != "phone:down,phone:up" {
		t.Fatalf("hook saw %v", got)
	}
}

func boolWord(up bool) string {
	if up {
		return "up"
	}
	return "down"
}

// A healthy resident runner must absorb the alert tap. Falling through
// would start a second XCTest and kill the process go-ios is holding.
func TestRunAlertRunner_ResidentFailClosed(t *testing.T) {
	const exe = "/var/containers/Bundle/Application/EXPECTED/AlertRunner.app/AlertRunner"
	ios := NewIOSProcessProbeAdapter(IOSProcessProbe{
		InstalledExecutable: func(string, string) (string, error) { return exe, nil },
		MajorVersion:        func(string) (int, error) { return 27, nil },
		Acquire: func(string) (IOSProcessListConn, func(), error) {
			return identityProcesses{{Pid: 77, Path: exe}}, func() {}, nil
		},
		Invalidate: func(string) {},
	})
	host := NewAutomationHost(ios)
	host.holds["dut"] = &runnerHold{cancel: func() {}, done: make(chan struct{}), pid: 77}
	err := ios.runAlertRunner("dut", "Allow")
	if err == nil || !strings.Contains(err.Error(), "resident alert agent") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "not installed") {
		t.Fatalf("fell through to a second runner: %v", err)
	}
}

// The shipped supervisor, driven through the iOS host's effect surface,
// reuses one runner identity and rejects a non-owner mutation.
func TestAutomationHostEffects_SupervisorReusesAndRejects(t *testing.T) {
	holder := "alice"
	var launches int
	obs := agent.Observation{
		Tethered: true, RunnerAlive: true, RunnerPID: 42, Authorized: true,
		Installed: true, InstallGen: "gen-1", AUTBundle: TestBouncingBallBundleID,
		AUTRunning: true, HostSession: true, LeaseHeld: true,
	}
	sup := agent.NewSupervisor(&agent.SupervisorArgs{
		Probe: func(string) (agent.Observation, error) { return obs, nil },
		OwnerOf: func(string) (string, bool) {
			if holder == "" {
				return "", false
			}
			return holder, true
		},
		Effects: agent.Effects{
			Launch: func(string) (int, error) {
				launches++
				return 42, nil
			},
			StopAUT: func(_, bundle string) error {
				if bundle != TestBouncingBallBundleID {
					t.Errorf("stopped %s", bundle)
				}
				obs.AUTRunning = false
				return nil
			},
			DropHostSession: func(string) error {
				obs.HostSession = false
				return nil
			},
			DropLease: func(string) error {
				obs.LeaseHeld = false
				return nil
			},
		},
	})
	first, err := sup.Ensure("dut", "alice")
	if err != nil || !first.Ready || first.Runner.PID != 42 || launches != 0 {
		t.Fatalf("first ensure %+v err=%v launches=%d", first, err, launches)
	}
	holder = "bob"
	second, err := sup.Ensure("dut", "bob")
	if err != nil || second.Runner.PID != first.Runner.PID || second.Runner.InstallGen != "gen-1" || launches != 0 {
		t.Fatalf("second ensure %+v err=%v launches=%d", second, err, launches)
	}
	idle, err := sup.Idle("dut", "bob")
	if err != nil || idle.AUTRunning || idle.LeaseHeld || idle.HostSession || idle.Ready || idle.Runner.PID != 42 {
		t.Fatalf("idle %+v err=%v", idle, err)
	}
	if err := sup.Mutate("dut", "carol", agent.MutLifecycle); err == nil || !strings.Contains(err.Error(), agent.ErrNotOwner.Error()) {
		t.Fatalf("non-owner mutate err = %v", err)
	}
	if err := sup.Mutate("dut", "bob", agent.MutInput); err != nil {
		t.Fatalf("owner mutate: %v", err)
	}
}

const runnerProbeExe = "/var/containers/Bundle/Application/EXPECTED/AlertRunner.app/AlertRunner"

func runnerProbeHost(acquire func(call int) (IOSProcessListConn, error)) *AutomationHost {
	calls := 0
	ios := NewIOSProcessProbeAdapter(IOSProcessProbe{
		InstalledExecutable: func(string, string) (string, error) { return runnerProbeExe, nil },
		MajorVersion:        func(string) (int, error) { return 27, nil },
		Acquire: func(string) (IOSProcessListConn, func(), error) {
			calls++
			conn, err := acquire(calls)
			if err != nil {
				return nil, func() {}, err
			}
			return conn, func() {}, nil
		},
		Invalidate: func(string) {},
	})
	return NewAutomationHost(ios)
}

func supervisorOver(host *AutomationHost, launches *int) *agent.Supervisor {
	return agent.NewSupervisor(&agent.SupervisorArgs{
		Probe:   host.Probe,
		OwnerOf: func(string) (string, bool) { return "alice", true },
		Effects: agent.Effects{
			Launch: func(string) (int, error) {
				*launches++
				return 99, nil
			},
		},
	})
}

// A stuck process list must not make Ensure kill and replace the hold.
func TestProbe_TransportErrorReusesHold(t *testing.T) {
	host := runnerProbeHost(func(int) (IOSProcessListConn, error) {
		return nil, context.DeadlineExceeded
	})
	host.holds["dut"] = &runnerHold{cancel: func() {}, done: make(chan struct{}), pid: 42}
	launches := 0
	snap, err := supervisorOver(host, &launches).Ensure("dut", "alice")
	if err != nil || !snap.Ready || snap.Relaunched || snap.Runner.PID != 42 || launches != 0 {
		t.Fatalf("ensure %+v err=%v launches=%d note=%s", snap, err, launches, host.ProbeNote("dut"))
	}
	if !strings.Contains(host.ProbeNote("dut"), "reusing open hold pid 42") {
		t.Fatalf("note %q", host.ProbeNote("dut"))
	}
}

// The same lookup, with no hold, fails closed instead of starting a runner.
func TestProbe_TransportErrorWithoutHoldFailsClosed(t *testing.T) {
	host := runnerProbeHost(func(int) (IOSProcessListConn, error) {
		return nil, context.DeadlineExceeded
	})
	launches := 0
	snap, err := supervisorOver(host, &launches).Ensure("dut", "alice")
	if err == nil || snap.Ready || launches != 0 {
		t.Fatalf("ensure %+v err=%v launches=%d", snap, err, launches)
	}
}

// One failed read followed by a live pid is the same runner, not a relaunch.
func TestProbe_RetrySeesLiveRunner(t *testing.T) {
	host := runnerProbeHost(func(call int) (IOSProcessListConn, error) {
		if call == 1 {
			return nil, context.DeadlineExceeded
		}
		return identityProcesses{{Pid: 42, Path: runnerProbeExe}}, nil
	})
	launches := 0
	snap, err := supervisorOver(host, &launches).Ensure("dut", "alice")
	if err != nil || !snap.Ready || snap.Relaunched || snap.Runner.PID != 42 || launches != 0 {
		t.Fatalf("ensure %+v err=%v launches=%d note=%s", snap, err, launches, host.ProbeNote("dut"))
	}
	if !strings.Contains(host.ProbeNote("dut"), "after retry") {
		t.Fatalf("note %q", host.ProbeNote("dut"))
	}
}

// "app not running" is the only probe result that replaces the runner.
func TestProbe_AbsentRunnerRelaunches(t *testing.T) {
	host := runnerProbeHost(func(int) (IOSProcessListConn, error) {
		return identityProcesses{}, nil
	})
	launches := 0
	snap, err := supervisorOver(host, &launches).Ensure("dut", "alice")
	if err != nil || !snap.Ready || !snap.Relaunched || snap.Runner.PID != 99 || launches != 1 {
		t.Fatalf("ensure %+v err=%v launches=%d note=%s", snap, err, launches, host.ProbeNote("dut"))
	}
}

// Launch itself must not cancel a hold when the process list is only stuck.
func TestLaunch_DoesNotKillLiveHold(t *testing.T) {
	host := runnerProbeHost(func(int) (IOSProcessListConn, error) {
		return nil, context.DeadlineExceeded
	})
	cancelled := false
	host.holds["dut"] = &runnerHold{
		cancel: func() { cancelled = true },
		done:   make(chan struct{}),
		pid:    42,
	}
	pid, err := host.Launch("dut")
	if err != nil || pid != 42 || cancelled || host.LaunchCount() != 0 {
		t.Fatalf("pid=%d err=%v cancelled=%v launches=%d", pid, err, cancelled, host.LaunchCount())
	}
}

// Idle's terminate must carry a deadline and retry a stuck process list
// instead of pinning the supervisor on context.Background.
func TestStopAUT_RetriesStuckLookup(t *testing.T) {
	calls := 0
	ios := NewIOSProcessProbeAdapter(IOSProcessProbe{
		InstalledExecutable: func(string, string) (string, error) { return runnerProbeExe, nil },
		MajorVersion:        func(string) (int, error) { return 27, nil },
		AcquireContext: func(ctx context.Context, _ string) (IOSProcessListConn, func(), func(), error) {
			calls++
			if _, ok := ctx.Deadline(); !ok {
				t.Errorf("StopAUT passed a context with no deadline")
			}
			if calls == 1 {
				return nil, func() {}, func() {}, context.DeadlineExceeded
			}
			return identityProcesses{}, func() {}, func() {}, nil
		},
		Invalidate: func(string) {},
	})
	host := NewAutomationHost(ios)
	if err := host.StopAUT("dut", TestBouncingBallBundleID); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}
