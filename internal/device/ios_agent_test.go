// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marcelocantos/spyder/internal/agent"
	"github.com/marcelocantos/spyder/internal/goios"
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
		Tethered: true, LockKnown: true, RunnerAlive: true, RunnerPID: 42, Authorized: true,
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

const stockcarsBundle = "com.minicades.stockcars"
const stockcarsExe = "/var/containers/Bundle/Application/GAME/StockCars.app/StockCars"

// Idle must stop the bundle launch_app recorded, not the BouncingBall fixture.
func TestIdle_StopsLaunchedBundleNotFixture(t *testing.T) {
	var killed []string
	ios := NewIOSProcessProbeAdapter(IOSProcessProbe{
		InstalledExecutable: func(_, bundle string) (string, error) {
			switch bundle {
			case alertRunnerBundle:
				return runnerProbeExe, nil
			case stockcarsBundle:
				return stockcarsExe, nil
			default:
				return "", fmt.Errorf("app not installed: %s", bundle)
			}
		},
		MajorVersion: func(string) (int, error) { return 27, nil },
		Acquire: func(string) (IOSProcessListConn, func(), error) {
			return identityProcesses{
				{Pid: 42, Path: runnerProbeExe},
				{Pid: 77, Path: stockcarsExe},
			}, func() {}, nil
		},
		Kill: func(_, bundle string, pid int) error {
			killed = append(killed, fmt.Sprintf("%s:%d", bundle, pid))
			return nil
		},
		Invalidate: func(string) {},
	})
	host := NewAutomationHost(ios)
	if host.AUT != "" {
		t.Fatalf("production AUT default %q", host.AUT)
	}
	sup := agent.NewSupervisor(&agent.SupervisorArgs{
		Effects: host.Effects(),
		Probe:   host.Probe,
		OwnerOf: func(string) (string, bool) { return "alice", true },
	})
	first, err := sup.Ensure("dut", "alice")
	if err != nil || !first.Ready || first.Runner.PID != 42 || first.AUTBundle != "" {
		t.Fatalf("ensure before launch %+v err=%v", first, err)
	}
	host.NoteLaunched("dut", stockcarsBundle)
	seen, err := host.Probe("dut")
	if err != nil || seen.AUTBundle != stockcarsBundle || !seen.AUTRunning {
		t.Fatalf("probe %+v err=%v", seen, err)
	}
	idle, err := sup.Idle("dut", "alice")
	if err != nil || idle.AUTRunning || idle.Ready || idle.Condition != agent.CondIdle || idle.Runner.PID != 42 {
		t.Fatalf("idle %+v err=%v", idle, err)
	}
	if len(killed) != 1 || killed[0] != stockcarsBundle+":77" {
		t.Fatalf("stopped %v", killed)
	}
	if strings.Contains(strings.Join(killed, ","), TestBouncingBallBundleID) {
		t.Fatalf("stopped the fixture: %v", killed)
	}
}

func TestProbe_PositiveLockIsCondLocked(t *testing.T) {
	host := runnerProbeHost(func(int) (IOSProcessListConn, error) {
		return identityProcesses{{Pid: 42, Path: runnerProbeExe}}, nil
	})
	host.lockRead = func(string) (map[string]any, error) {
		return map[string]any{"state": uint64(1)}, nil
	}
	launches := 0
	snap, err := supervisorOver(host, &launches).Ensure("dut", "alice")
	if err != nil || snap.Condition != agent.CondLocked || snap.Ready || launches != 0 {
		t.Fatalf("locked %+v launches=%d err=%v detail=%s", snap, launches, err, host.LockDetail("dut"))
	}
}

func TestProbe_FailedLockReadIsNotReady(t *testing.T) {
	host := runnerProbeHost(func(int) (IOSProcessListConn, error) {
		return identityProcesses{{Pid: 42, Path: runnerProbeExe}}, nil
	})
	host.lockRead = func(string) (map[string]any, error) {
		return nil, io.EOF
	}
	launches := 0
	snap, err := supervisorOver(host, &launches).Ensure("dut", "alice")
	if err != nil || snap.Ready || snap.Condition == agent.CondReady || snap.Condition == agent.CondLocked || launches != 0 {
		t.Fatalf("failed lock %+v launches=%d err=%v detail=%s", snap, launches, err, host.LockDetail("dut"))
	}
}

func TestProbe_UnlockedStateStaysReady(t *testing.T) {
	host := runnerProbeHost(func(int) (IOSProcessListConn, error) {
		return identityProcesses{{Pid: 42, Path: runnerProbeExe}}, nil
	})
	host.lockRead = func(string) (map[string]any, error) {
		return map[string]any{"state": float64(0)}, nil
	}
	launches := 0
	snap, err := supervisorOver(host, &launches).Ensure("dut", "alice")
	if err != nil || !snap.Ready || snap.Condition != agent.CondReady || launches != 0 {
		t.Fatalf("unlocked %+v launches=%d err=%v detail=%s", snap, launches, err, host.LockDetail("dut"))
	}
}

func TestProbe_ChangedBootIDStaysRebooted(t *testing.T) {
	host := runnerProbeHost(func(int) (IOSProcessListConn, error) {
		return identityProcesses{{Pid: 42, Path: runnerProbeExe}}, nil
	})
	boot := "boot-a"
	host.bootSource = func(string) string { return boot }
	launches := 0
	sup := supervisorOver(host, &launches)
	first, err := sup.Ensure("dut", "alice")
	if err != nil || !first.Ready || first.BootID != "boot-a" || launches != 0 {
		t.Fatalf("first %+v launches=%d err=%v", first, launches, err)
	}
	boot = "boot-b"
	second, err := sup.Ensure("dut", "alice")
	if err != nil || second.Condition != agent.CondRebooted || second.Ready || second.Relaunched || second.BootID != "boot-b" || launches != 0 {
		t.Fatalf("reboot %+v launches=%d err=%v", second, launches, err)
	}
}

func TestInterpretLock_StateAndFailure(t *testing.T) {
	locked, known, detail := interpretLock(map[string]any{"state": uint64(3)}, nil)
	if !locked || !known || !strings.Contains(detail, "state=3") {
		t.Fatalf("locked=%v known=%v detail=%s", locked, known, detail)
	}
	locked, known, _ = interpretLock(nil, io.EOF)
	if locked || known {
		t.Fatalf("eof locked=%v known=%v", locked, known)
	}
	if _, ok := lockStateValue(struct{ N int }{N: 1}); ok {
		t.Fatal("non-numeric state was accepted")
	}
}

// Captured from `xcrun devicectl device info lockState` on the unlocked
// iPhone 13 (UDID 00008110-0014182E0AC2801E) on 2026-10-18.
const devicectlLockUnlocked = `{
  "info": {"outcome": "success", "commandType": "devicectl.device.info.lockState"},
  "result": {
    "deviceIdentifier": "CD2E3380-F1AB-5D03-BBA8-E5A68ADB3261",
    "passcodeRequired": false,
    "unlockedSinceBoot": true
  }
}`

func TestProbe_DevicectlPasscodeRequiredIsCondLocked(t *testing.T) {
	host := runnerProbeHost(func(int) (IOSProcessListConn, error) {
		return identityProcesses{{Pid: 42, Path: runnerProbeExe}}, nil
	})
	doc := strings.Replace(devicectlLockUnlocked, `"passcodeRequired": false`, `"passcodeRequired": true`, 1)
	host.lockExec = func(context.Context, string) ([]byte, error) {
		return []byte(doc), nil
	}
	launches := 0
	snap, err := supervisorOver(host, &launches).Ensure("dut", "alice")
	if err != nil || snap.Condition != agent.CondLocked || snap.Ready || launches != 0 {
		t.Fatalf("locked %+v launches=%d err=%v detail=%s", snap, launches, err, host.LockDetail("dut"))
	}
	if !strings.Contains(host.LockDetail("dut"), "passcodeRequired=true") {
		t.Fatalf("detail %s", host.LockDetail("dut"))
	}
}

func TestProbe_DevicectlUnlockedStaysReady(t *testing.T) {
	host := runnerProbeHost(func(int) (IOSProcessListConn, error) {
		return identityProcesses{{Pid: 42, Path: runnerProbeExe}}, nil
	})
	host.lockExec = func(context.Context, string) ([]byte, error) {
		return []byte(devicectlLockUnlocked), nil
	}
	launches := 0
	snap, err := supervisorOver(host, &launches).Ensure("dut", "alice")
	if err != nil || !snap.Ready || snap.Condition != agent.CondReady || launches != 0 {
		t.Fatalf("unlocked %+v launches=%d err=%v detail=%s", snap, launches, err, host.LockDetail("dut"))
	}
}

func TestProbe_DevicectlFailureIsNotReady(t *testing.T) {
	host := runnerProbeHost(func(int) (IOSProcessListConn, error) {
		return identityProcesses{{Pid: 42, Path: runnerProbeExe}}, nil
	})
	host.lockExec = func(context.Context, string) ([]byte, error) {
		return nil, fmt.Errorf("devicectl lockState: exit")
	}
	launches := 0
	snap, err := supervisorOver(host, &launches).Ensure("dut", "alice")
	if err != nil || snap.Ready || snap.Condition == agent.CondReady || snap.Condition == agent.CondLocked || launches != 0 {
		t.Fatalf("failed lock %+v launches=%d err=%v detail=%s", snap, launches, err, host.LockDetail("dut"))
	}
}

func TestInterpretDevicectlLock_NeverUnlockedIsLocked(t *testing.T) {
	doc := strings.Replace(devicectlLockUnlocked, `"unlockedSinceBoot": true`, `"unlockedSinceBoot": false`, 1)
	locked, known, detail := interpretDevicectlLock([]byte(doc))
	if !locked || !known || !strings.Contains(detail, "unlockedSinceBoot=false") {
		t.Fatalf("locked=%v known=%v detail=%s", locked, known, detail)
	}
	locked, known, _ = interpretDevicectlLock([]byte(`{"info":{"outcome":"failed"}}`))
	if locked || known {
		t.Fatalf("failed outcome locked=%v known=%v", locked, known)
	}
	locked, known, _ = interpretDevicectlLock([]byte(`not-json`))
	if locked || known {
		t.Fatalf("garbage locked=%v known=%v", locked, known)
	}
}

// tunnelAdapter points a real adapter's resolver at srv. Pools stay
// non-nil so recovery can invalidate them; the resolver is not the
// shared userspace tunnel.
func tunnelAdapter(t *testing.T, srv *httptest.Server) *IOSAdapter {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	a := NewIOSAdapter()
	a.goios = goios.New(u.Hostname(), port)
	return a
}

func TestListenerDetach_RemovesUDIDUntilReattach(t *testing.T) {
	var deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"address":"::1","rsdPort":1,"udid":"phone"}`)
	}))
	t.Cleanup(srv.Close)

	a := tunnelAdapter(t, srv)
	var got []string
	a.SetTetherNotify(func(udid string, up bool) {
		got = append(got, udid+":"+boolWord(up))
	})
	// The kernel list still contains the UDID. The enumeration List
	// builds from loses it only after DropTunnel's DELETE succeeds.
	kernel := []string{"phone", "pad"}
	l := goios.NewListener(iosTunnelRecovery{a})
	l.RunSync(context.Background(), &goios.ScriptedSource{Events: []goios.DeviceEvent{
		{DeviceID: 1, UDID: "phone", Attached: true},
		{DeviceID: 1, Attached: false},
	}})
	if deletes.Load() != 1 {
		t.Fatalf("detach deletes=%d, want the listener's DropTunnel", deletes.Load())
	}
	hidden := a.muxEnumeration(kernel)
	if len(hidden) != 1 || hidden[0] != "pad" {
		t.Fatalf("after detach enumeration %v kernel %v", hidden, kernel)
	}

	l.RunSync(context.Background(), &goios.ScriptedSource{Events: []goios.DeviceEvent{
		{DeviceID: 2, UDID: "phone", Attached: true},
	}})
	if deletes.Load() != 2 {
		t.Fatalf("reattach deletes=%d, want DropTunnel then ReestablishTunnel", deletes.Load())
	}
	shown := a.muxEnumeration(kernel)
	if len(shown) != 2 || shown[0] != "phone" || shown[1] != "pad" {
		t.Fatalf("after reattach enumeration %v", shown)
	}
	if strings.Join(got, ",") != "phone:up,phone:down,phone:up" {
		t.Fatalf("hook saw %v", got)
	}
}

func TestListenerDetach_FailedDropKeepsUDID(t *testing.T) {
	var deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	a := tunnelAdapter(t, srv)
	var got []string
	a.SetTetherNotify(func(udid string, up bool) {
		got = append(got, udid+":"+boolWord(up))
	})
	kernel := []string{"phone", "pad"}
	l := goios.NewListener(iosTunnelRecovery{a})
	l.RunSync(context.Background(), &goios.ScriptedSource{Events: []goios.DeviceEvent{
		{DeviceID: 1, UDID: "phone", Attached: true},
		{DeviceID: 1, Attached: false},
	}})
	if deletes.Load() != 1 {
		t.Fatalf("failed detach deletes=%d", deletes.Load())
	}
	kept := a.muxEnumeration(kernel)
	if len(kept) != 2 || kept[0] != "phone" || kept[1] != "pad" {
		t.Fatalf("failed DELETE still changed enumeration %v", kept)
	}
	if strings.Join(got, ",") != "phone:up" {
		t.Fatalf("hook saw %v", got)
	}
}
