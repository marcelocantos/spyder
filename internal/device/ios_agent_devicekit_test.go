// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/agent"
)

const dkUDID = "00008130-0009702E1110001C"

func TestDeviceKitConfigFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name, backend, devices string
		nilCfg, wantErr        bool
		selects, rejects       string
	}{
		{name: "unset", nilCfg: true},
		{name: "alertrunner", backend: "alertrunner", devices: dkUDID, nilCfg: true},
		{name: "unknown", backend: "wda", devices: dkUDID, wantErr: true},
		{name: "empty scope", backend: "devicekit", wantErr: true},
		{name: "alias not udid", backend: "devicekit", devices: "pippa", wantErr: true},
		{name: "list", backend: "devicekit", devices: " " + dkUDID + " ", selects: dkUDID, rejects: "00008130-000000000000000A"},
		{name: "legacy udid", backend: "DeviceKit", devices: strings.Repeat("a1", 20), selects: strings.Repeat("a1", 20), rejects: dkUDID},
		{name: "all", backend: "devicekit", devices: "*", selects: dkUDID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := deviceKitAgentFrom(tc.backend, tc.devices, "/opt/spyder/libexec/spyder/ios")
			if tc.nilCfg {
				if cfg != nil {
					t.Fatalf("got %+v, want AlertRunner default", cfg)
				}
				return
			}
			if (cfg.Err() != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", cfg.Err(), tc.wantErr)
			}
			if tc.wantErr {
				if cfg.Selects(dkUDID) {
					t.Fatal("a bad configuration selected a device")
				}
				return
			}
			if !cfg.Selects(tc.selects) {
				t.Fatalf("%s not selected", tc.selects)
			}
			if tc.rejects != "" && cfg.Selects(tc.rejects) {
				t.Fatalf("%s selected", tc.rejects)
			}
		})
	}
}

// The launcher is the daemon's go-ios, checked when used. There is no
// hash pin: replacing the bundled binary is a spyder upgrade.
func TestDeviceKitCLIMustBeExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ios")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cli string
		ok  bool
	}{{exe, true}, {"", false}, {plain, false}, {dir, false}, {filepath.Join(dir, "missing"), false}} {
		_, err := (&DeviceKitAgent{CLI: tc.cli}).cli()
		if (err == nil) != tc.ok {
			t.Errorf("cli(%q) err=%v want ok=%v", tc.cli, err, tc.ok)
		}
	}
}

func TestDeviceKitActualHTTPStateRequiresExplicitIdentity(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":"spyder-agent-health","result":{"bundleId":"` + DeviceKitRunnerBundle + `","state":"runningBackground","pid":0}}`
	fg := strings.ReplaceAll(valid, "runningBackground", "runningForeground")
	for _, test := range []struct {
		name, body string
		status     int
		want       bool
	}{
		{"background", valid, 200, true},
		{"foreground", strings.ReplaceAll(fg, `"pid":0`, `"pid":123`), 200, true},
		{"wrong_pid", strings.ReplaceAll(fg, `"pid":0`, `"pid":999`), 200, false},
		{"missing_pid", strings.ReplaceAll(valid, `,"pid":0`, ``), 200, false},
		{"wrong_bundle", strings.ReplaceAll(valid, DeviceKitRunnerBundle, "foreign"), 200, false},
		{"wrong_id", strings.ReplaceAll(valid, "spyder-agent-health", "foreign"), 200, false},
		{"wrong_version", strings.ReplaceAll(valid, `"2.0"`, `"1.0"`), 200, false},
		{"rpc_error", strings.ReplaceAll(valid, `"result"`, `"error":{"code":1},"result"`), 200, false},
		{"not_running", strings.ReplaceAll(valid, "runningBackground", "notRunning"), 200, false},
		{"http_error", valid, 500, false},
		{"malformed", "{", 200, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := fmt.Sprintf("HTTP/1.1 %d X\r\nContent-Length: %d\r\n\r\n%s", test.status, len(test.body), test.body)
			if got := deviceKitStateReady(strings.NewReader(wire), 123); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestHeldPID_DeadChannelCannotBeReused(t *testing.T) {
	done := make(chan struct{})
	h := NewAutomationHost(nil)
	h.holds["ipad"] = &runnerHold{pid: 123, done: done, cancel: func() {}}
	if pid, ok := h.heldPID("ipad"); !ok || pid != 123 || !h.ChannelOpen("ipad") {
		t.Fatal("live owned channel rejected")
	}
	close(done)
	if _, ok := h.heldPID("ipad"); ok || h.ChannelOpen("ipad") {
		t.Fatal("dead channel reported open")
	}
}

// dkHost is a DeviceKit-backed host over the hermetic process probe.
// pid is what the device process list reports for the runner (0 = none).
func dkHost(t *testing.T, pid *atomic.Int64) *AutomationHost {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "ios")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := runnerProbeHost(func(int) (IOSProcessListConn, error) {
		if p := pid.Load(); p > 0 {
			return identityProcesses{{Pid: int(p), Path: runnerProbeExe}}, nil
		}
		return identityProcesses{}, nil
	})
	host.DeviceKit = &DeviceKitAgent{CLI: exe, devices: map[string]bool{"dut": true}}
	// No runwda hosts on this "Mac" unless a test says so.
	host.hostScan = func(string) []runwdaProc { return nil }
	host.hostAlive = func(int) bool { return false }
	host.hostSignal = func(int, syscall.Signal) error { return nil }
	host.IOS.pidProbe.Kill = func(_, _ string, _ int) error {
		pid.Store(0)
		return nil
	}
	return host
}

func dkSupervisor(host *AutomationHost) *agent.Supervisor {
	return agent.NewSupervisor(&agent.SupervisorArgs{
		RunnerBundleFor: host.RunnerBundleFor,
		Probe:           host.Probe,
		OwnerOf:         func(string) (string, bool) { return "alice", true },
		Effects:         host.Effects(),
	})
}

func TestDeviceKit_BackendIsPerDevice(t *testing.T) {
	var pid atomic.Int64
	host := dkHost(t, &pid)
	if host.RunnerBundleFor("dut") != DeviceKitRunnerBundle || host.RunnerBundleFor("other") != alertRunnerBundle {
		t.Fatal("wrong supervised runner identity")
	}
	if err := host.TapButton("dut", "Allow"); err == nil {
		t.Fatal("DeviceKit runner accepted the AlertRunner command channel")
	}
	if _, err := host.Install("dut"); err == nil {
		t.Fatal("supervision installed DeviceKit")
	}
	if err := host.IOS.runAlertRunner("dut", "Allow"); err == nil || !strings.Contains(err.Error(), "DeviceKit") {
		t.Fatalf("one-shot AlertRunner on a DeviceKit device: %v", err)
	}
	host.NoteLaunched("dut", DeviceKitRunnerBundle)
	host.NoteLaunched("dut", deviceKitHostBundle)
	if b := host.LaunchedBundle("dut"); b != "" {
		t.Fatalf("idle would stop the agent bundle %q", b)
	}
}

func TestDeviceKit_BadConfigFailsClosedForEveryDevice(t *testing.T) {
	var pid atomic.Int64
	host := dkHost(t, &pid)
	host.DeviceKit = deviceKitAgentFrom("devicekit", "", "/x/ios")
	for _, id := range []string{"dut", "other"} {
		if _, err := host.Probe(id); err == nil {
			t.Fatalf("probe %s accepted a bad backend setting", id)
		}
	}
}

// A runner this daemon did not start (Stock Cars' own runwda, a
// previous daemon) is never adopted or replaced.
func TestDeviceKit_ForeignRunnerIsNotAdopted(t *testing.T) {
	var pid atomic.Int64
	pid.Store(42)
	host := dkHost(t, &pid)
	host.readyProbe = func(string, int) bool { return true }
	started := 0
	host.holdRun = func(ctx context.Context, _ string, _ io.Writer) error {
		started++
		<-ctx.Done()
		return nil
	}
	signals := 0
	host.hostSignal = func(int, syscall.Signal) error { signals++; return nil }
	parentAlive := true
	host.hostAlive = func(pid int) bool { return pid == 900 && parentAlive }
	// Stock Cars' own harness, and a live spyder daemon's runwda (spyder's
	// bundled ios, parent alive), are both foreign.
	for _, foreign := range []runwdaProc{
		{PID: 501, PPID: 1, Binary: "/Users/x/stockcars/bin/ios", Bundle: DeviceKitRunnerBundle},
		{PID: 502, PPID: 900, Binary: "/opt/homebrew/Cellar/spyder/0.100.0/libexec/spyder/ios", Bundle: DeviceKitRunnerBundle},
	} {
		host.hostScan = func(string) []runwdaProc { return []runwdaProc{foreign} }
		if _, err := host.Probe("dut"); err == nil || !strings.Contains(err.Error(), "not started by this daemon") {
			t.Fatalf("probe over foreign host %+v: %v", foreign, err)
		}
	}
	if _, err := dkSupervisor(host).Ensure("dut", "alice"); err == nil || !strings.Contains(err.Error(), "not started by this daemon") {
		t.Fatalf("ensure over a foreign runner: %v", err)
	}
	if _, err := host.Launch("dut"); err == nil {
		t.Fatal("launch adopted a foreign runner")
	}
	if started != 0 || host.LaunchCount() != 0 || signals != 0 || pid.Load() != 42 {
		t.Fatalf("started=%d launches=%d signals=%d pid=%d", started, host.LaunchCount(), signals, pid.Load())
	}
}

// A live pid is not readiness: only a JSON-RPC answer is.
func TestDeviceKit_OwnedRunnerNeedsReadiness(t *testing.T) {
	var pid atomic.Int64
	pid.Store(42)
	host := dkHost(t, &pid)
	var ready atomic.Bool
	host.readyProbe = func(_ string, p int) bool { return ready.Load() && p == 42 }
	host.holds["dut"] = &runnerHold{cancel: func() {}, done: make(chan struct{}), pid: 42, pending: true, since: time.Now(), msg: "waiting for approval"}
	sup := dkSupervisor(host)
	snap, err := sup.Tick("dut")
	if err != nil || snap.Ready || snap.Condition != agent.CondConsentPending || !snap.ConsentPending || host.LaunchCount() != 0 {
		t.Fatalf("pending %+v err=%v launches=%d", snap, err, host.LaunchCount())
	}
	// The owner approves on the device after the launch budget ran out.
	ready.Store(true)
	snap, err = sup.Tick("dut")
	if err != nil || !snap.Ready || snap.Runner.PID != 42 || snap.Runner.BundleID != DeviceKitRunnerBundle || snap.Consent != nil || snap.ConsentPending || host.LaunchCount() != 1 {
		t.Fatalf("approved %+v err=%v launches=%d", snap, err, host.LaunchCount())
	}
}

func TestDeviceKit_LaunchWaitsForReadiness(t *testing.T) {
	var pid atomic.Int64
	host := dkHost(t, &pid)
	var probes atomic.Int64
	host.readyProbe = func(_ string, p int) bool { return probes.Add(1) > 2 && p == 77 }
	host.holdRun = func(ctx context.Context, _ string, _ io.Writer) error {
		pid.Store(77)
		<-ctx.Done()
		pid.Store(0)
		return nil
	}
	snap, err := dkSupervisor(host).Ensure("dut", "alice")
	if err != nil || !snap.Ready || !snap.Relaunched || snap.Runner.PID != 77 || host.LaunchCount() != 1 || probes.Load() < 3 {
		t.Fatalf("ensure %+v err=%v launches=%d probes=%d", snap, err, host.LaunchCount(), probes.Load())
	}
	start := time.Now()
	host.Shutdown()
	if host.ChannelOpen("dut") || time.Since(start) > 2*time.Second {
		t.Fatal("shutdown left the runner session open")
	}
}

// Apple's approval refusal is consent, recorded once; the daemon does
// not relaunch into a second prompt until reboot or an attended retry.
func TestDeviceKit_ConsentFailureIsAskedOncePerBudget(t *testing.T) {
	var pid atomic.Int64
	host := dkHost(t, &pid)
	host.readyProbe = func(string, int) bool { return false }
	starts := 0
	host.holdRun = func(_ context.Context, _ string, out io.Writer) error {
		starts++
		fmt.Fprintln(out, "level=error msg=\"Failed enabling automation mode\"")
		return errors.New("exit status 1")
	}
	sup := dkSupervisor(host)
	snap, err := sup.Ensure("dut", "alice")
	if err != nil || snap.Ready || snap.Condition != agent.CondConsentRequired || snap.Consent == nil {
		t.Fatalf("consent %+v err=%v", snap, err)
	}
	for i := 0; i < agent.MaxAttempts+2; i++ {
		if _, err := sup.Tick("dut"); err != nil {
			t.Fatal(err)
		}
	}
	if starts != 1 {
		t.Fatalf("unattended relaunches: starts=%d", starts)
	}
	if _, err := sup.Retry("dut", "alice"); err != nil {
		t.Fatal(err)
	}
	if starts != 2 {
		t.Fatalf("attended retry did not relaunch: starts=%d", starts)
	}
}

// Shutdown stops sessions in parallel so supervisord's stop window is
// not spent one device at a time.
func TestShutdown_StopsHoldsInParallel(t *testing.T) {
	h := NewAutomationHost(nil)
	for _, id := range []string{"a", "b", "c"} {
		done := make(chan struct{})
		h.holds[id] = &runnerHold{pid: 1, done: done, cancel: func() {
			go func() { time.Sleep(time.Second); close(done) }()
		}}
	}
	start := time.Now()
	h.Shutdown()
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Fatalf("shutdown took %s", d)
	}
}
