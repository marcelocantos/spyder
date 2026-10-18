//go:build agentlive

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/agent"
)

// TestAgentJourney_Live drives the shipped supervisor on the connected
// iPhone and iPad. It is behind the agentlive tag so the hermetic
// -count=2 run does not compile or skip it.
func TestAgentJourney_Live(t *testing.T) {
	iphone := os.Getenv("SPYDER_LIVE_UDID")
	ipad := os.Getenv("SPYDER_LIVE_UDID_OTHER")
	evidence := os.Getenv("SPYDER_AGENT_EVIDENCE")
	if iphone == "" || ipad == "" || evidence == "" {
		t.Fatal("SPYDER_LIVE_UDID, SPYDER_LIVE_UDID_OTHER, and SPYDER_AGENT_EVIDENCE are required")
	}
	if err := os.MkdirAll(evidence, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Run("iphone", func(t *testing.T) {
		runAgentJourney(t, iphone, filepath.Join(evidence, "journey-iphone.txt"), filepath.Join(evidence, "iphone-screen.png"))
	})
	t.Run("ipad", func(t *testing.T) {
		runAgentJourney(t, ipad, filepath.Join(evidence, "journey-ipad.txt"), filepath.Join(evidence, "ipad-screen.png"))
	})
}

func runAgentJourney(t *testing.T, udid, logPath, shotPath string) {
	t.Helper()
	var b strings.Builder
	logf := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
		t.Logf(format, args...)
	}
	defer func() {
		if err := os.WriteFile(logPath, []byte(b.String()), 0o644); err != nil {
			t.Errorf("write log: %v", err)
		}
	}()

	logf("device %s", udid)
	logf("Past prompt recurrence is not attributed to runner restart alone; earlier evidence showed actionable OS-consent timing as well.")
	logf("Physical USB cable was not unplugged. Disconnect is Supervisor.NoteTether(false), the usbmux-listener edge, then NoteTether(true) re-probes List and AppPID.")

	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	products := filepath.Join(root, "ios", "AlertRunner", "build", "Build", "Products", "Debug-iphoneos")
	bin := filepath.Join(products, "AlertRunner-Runner.app", "PlugIns", "AlertRunner.xctest", "AlertRunner")
	out, err := exec.Command("strings", "-a", bin).Output()
	if err != nil || !bytes.Contains(out, []byte("testHoldAgent")) {
		t.Fatalf("built runner missing testHoldAgent: %v", err)
	}
	logf("built runner contains testHoldAgent")
	t.Setenv("SPYDER_ALERT_RUNNER_DIR", products)

	ios := NewIOSAdapter()
	before := quoteState(ios, udid)
	logf("battery before: %s", before)

	if err := ios.InstallApp(udid, filepath.Join(products, "AlertHost.app")); err != nil {
		logf("preinstall host failed: %v", err)
		t.Fatal(err)
	}
	if err := ios.InstallApp(udid, filepath.Join(products, "AlertRunner-Runner.app")); err != nil {
		logf("preinstall runner failed: %v", err)
		t.Fatal(err)
	}
	logf("preinstall finished outside the measured ensure pair")
	if !bundlesInstalled(t, ios, udid) {
		t.Fatal("alert host and runner are not both installed after preinstall")
	}
	terminateBounded(ios, udid, alertRunnerBundle)
	waitGone(ios, udid, alertRunnerBundle, 8*time.Second)
	terminateBounded(ios, udid, TestBouncingBallBundleID)

	host := NewAutomationHost(ios)
	host.AUT = TestBouncingBallBundleID
	defer host.Shutdown()
	holder := "session-a"
	sup := agent.NewSupervisor(&agent.SupervisorArgs{
		Effects: host.Effects(),
		Probe:   host.Probe,
		OwnerOf: func(id string) (string, bool) {
			if id != udid || holder == "" {
				return "", false
			}
			return holder, true
		},
	})

	installsBefore := host.InstallCount()
	first, err := sup.Ensure(udid, holder)
	logf("ensure session-a: condition=%s ready=%v pid=%d install_gen=%s relaunched=%v reinstalled=%v attempts=%d installs=%d launches=%d err=%v",
		first.Condition, first.Ready, first.Runner.PID, first.Runner.InstallGen, first.Relaunched, first.Reinstalled, first.Attempts, host.InstallCount(), host.LaunchCount(), err)
	logf("runner probe: %s", host.ProbeNote(udid))
	logConsent(logf, "session-a", first)
	if err != nil || !first.Ready || first.Runner.PID <= 0 {
		saveShot(logf, ios, udid, shotPath)
		t.Fatalf("session-a did not reach a healthy runner: %+v err=%v", first, err)
	}
	if host.InstallCount() != installsBefore {
		t.Fatalf("session-a reinstalled the runner: installs %d -> %d", installsBefore, host.InstallCount())
	}
	pid1 := first.Runner.PID
	launches1 := host.LaunchCount()

	if err := ios.LaunchApp(udid, TestBouncingBallBundleID, nil); err != nil {
		logf("launch AUT failed: %v", err)
		t.Fatal(err)
	}
	autPID, autErr := appPIDBounded(ios, udid, TestBouncingBallBundleID)
	logf("AUT after launch: pid=%d err=%v", autPID, autErr)
	if autErr != nil || autPID <= 0 {
		t.Fatal("AUT did not start")
	}

	holder = "session-b"
	second, err := sup.Ensure(udid, holder)
	logf("ensure session-b: condition=%s ready=%v pid=%d install_gen=%s relaunched=%v reinstalled=%v attempts=%d installs=%d launches=%d err=%v",
		second.Condition, second.Ready, second.Runner.PID, second.Runner.InstallGen, second.Relaunched, second.Reinstalled, second.Attempts, host.InstallCount(), host.LaunchCount(), err)
	logf("runner probe: %s", host.ProbeNote(udid))
	logConsent(logf, "session-b", second)
	if err != nil || !second.Ready || second.Runner.PID != pid1 || host.LaunchCount() != launches1 || host.InstallCount() != installsBefore {
		t.Fatalf("session-b did not reuse pid %d (launches %d installs %d): %+v err=%v", pid1, launches1, installsBefore, second, err)
	}
	onDevice, onErr := probeApp(ios, udid, alertRunnerBundle, appProbeAttempts)
	logf("on-device runner pid during reuse: %d err=%v (ensure pid %d)", onDevice, onErr, pid1)
	if onErr != nil || onDevice != pid1 {
		t.Fatalf("on-device runner pid %d err=%v, ensure pid %d", onDevice, onErr, pid1)
	}

	host.NoteClientSession(udid)
	host.NoteLease(udid, true)
	idle, err := sup.Idle(udid, holder)
	idleAUT, idleAUTErr := probeApp(ios, udid, TestBouncingBallBundleID, appProbeAttempts)
	idleRunner, idleRunnerErr := probeApp(ios, udid, alertRunnerBundle, appProbeAttempts)
	logf("idle: condition=%s ready=%v aut_running=%v lease=%v host_session=%v runner_pid=%d runner_healthy=%v installs=%d launches=%d err=%v",
		idle.Condition, idle.Ready, idle.AUTRunning, idle.LeaseHeld, idle.HostSession, idle.Runner.PID, idle.RunnerHealthy, host.InstallCount(), host.LaunchCount(), err)
	logf("idle AUT AppPID: pid=%d err=%v", idleAUT, idleAUTErr)
	logf("idle runner AppPID: pid=%d err=%v (may remain)", idleRunner, idleRunnerErr)
	logf("idle client testmanagerd sessions=%d host_drops=%d lease_drops=%d agent_xctest_channel_open=%v",
		host.ClientSessions(udid), host.HostDrops(), host.LeaseDrops(), host.ChannelOpen(udid))
	logf("xcodebuild: %s", quoteProcesses("xcodebuild"))
	if err != nil || idle.AUTRunning || idle.LeaseHeld || idle.HostSession || idle.Ready || idle.Condition != agent.CondIdle {
		t.Fatalf("idle did not drop the AUT and host session: %+v err=%v", idle, err)
	}
	if idleAUT > 0 || idleAUTErr == nil || !strings.Contains(idleAUTErr.Error(), "app not running") {
		t.Fatalf("AUT not observed absent: pid=%d err=%v", idleAUT, idleAUTErr)
	}
	if host.ClientSessions(udid) != 0 {
		t.Fatalf("client sessions still held: %d", host.ClientSessions(udid))
	}

	down, err := sup.NoteTether(udid, false)
	logf("note tether down: condition=%s ready=%v recovered=%v err=%v", down.Condition, down.Ready, down.Recovered, err)
	if err != nil || down.Ready || down.Condition != agent.CondDisconnected {
		t.Fatalf("disconnect %+v err=%v", down, err)
	}
	up, err := sup.NoteTether(udid, true)
	listed := deviceListed(ios, udid)
	rePID, reErr := probeApp(ios, udid, alertRunnerBundle, appProbeAttempts)
	logf("note tether up: condition=%s ready=%v recovered=%v err=%v listed=%v runner_pid=%d runner_err=%v lock=%q",
		up.Condition, up.Ready, up.Recovered, err, listed, rePID, reErr, host.LockDetail(udid))
	if err != nil || up.Condition != agent.CondReconnected {
		t.Fatalf("reconnect %+v err=%v", up, err)
	}
	alive := reErr == nil && rePID > 0
	if up.Ready && (!listed || !alive) {
		t.Fatalf("ready lie: listed=%v alive=%v snap=%+v", listed, alive, up)
	}
	if up.Ready {
		logf("reconnect recovered a live authorized runner")
	} else {
		logf("reconnect is not ready; the probe did not show a healthy authorized runner")
	}

	mutErr := sup.Mutate(udid, "intruder", agent.MutLifecycle)
	logf("non-owner mutate: %v", mutErr)
	if !errors.Is(mutErr, agent.ErrNotOwner) {
		t.Fatalf("non-owner mutate: %v", mutErr)
	}
	if err := sup.Mutate(udid, holder, agent.MutInput); err != nil {
		t.Fatalf("owner mutate: %v", err)
	}

	logf("battery after: %s", quoteState(ios, udid))
	saveShot(logf, ios, udid, shotPath)
	logf("lock probe: %s", host.LockDetail(udid))
}

func saveShot(logf func(string, ...any), ios *IOSAdapter, udid, shotPath string) {
	png, shotErr := ios.Screenshot(udid)
	if shotErr != nil {
		logf("prompt: not observed (screenshot failed: %v)", shotErr)
		return
	}
	if werr := os.WriteFile(shotPath, png, 0o644); werr != nil {
		logf("prompt: not observed (could not save screenshot: %v)", werr)
		return
	}
	logf("prompt: not observed in the supervision result; screenshot saved at %s for inspection", shotPath)
}

func logConsent(logf func(string, ...any), label string, snap agent.Snapshot) {
	if snap.Consent == nil {
		logf("%s prompt: not observed", label)
		return
	}
	logf("%s prompt: actionable=%v message=%q", label, snap.Consent.Actionable, snap.Consent.Message)
}

func quoteState(ios *IOSAdapter, udid string) string {
	st, err := ios.State(udid)
	if err != nil {
		return fmt.Sprintf("not observed (%v)", err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return fmt.Sprintf("battery_level=%v charging=%v", st.BatteryLevel, st.Charging)
	}
	return string(raw)
}

func quoteProcesses(name string) string {
	out, err := exec.Command("pgrep", "-l", name).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil || text == "" {
		return "none"
	}
	return text
}

func bundlesInstalled(t *testing.T, ios *IOSAdapter, udid string) bool {
	t.Helper()
	apps, err := ios.ListApps(udid)
	if err != nil {
		t.Logf("list apps: %v", err)
		return false
	}
	hostOK, runnerOK := false, false
	for _, app := range apps {
		switch app.BundleID {
		case alertHostBundle:
			hostOK = true
		case alertRunnerBundle:
			runnerOK = true
		}
	}
	return hostOK && runnerOK
}

func deviceListed(ios *IOSAdapter, udid string) bool {
	list, err := ios.List()
	if err != nil {
		return false
	}
	for _, info := range list {
		if info.UUID == udid {
			return true
		}
	}
	return false
}

// appProbeAttempts bounds a flaky process-list read. A deadline is not
// evidence the app is gone, so absence checks retry until the sentinel
// or a live pid comes back.
const appProbeAttempts = 3

func probeApp(ios *IOSAdapter, udid, bundle string, attempts int) (int, error) {
	var pid int
	var err error
	for i := 0; i < attempts; i++ {
		pid, err = appPIDBounded(ios, udid, bundle)
		if err == nil || strings.Contains(err.Error(), "app not running") || strings.Contains(err.Error(), "app not installed") {
			return pid, err
		}
	}
	return pid, err
}

func terminateBounded(ios *IOSAdapter, id, bundle string) {
	ctx, cancel := context.WithTimeout(context.Background(), agentProbeBudget)
	defer cancel()
	_ = ios.TerminateAppContext(ctx, id, bundle)
}

func appPIDBounded(ios *IOSAdapter, udid, bundle string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentProbeBudget)
	defer cancel()
	return ios.AppPIDContext(ctx, udid, bundle)
}

func waitGone(ios *IOSAdapter, udid, bundle string, budget time.Duration) {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		pid, err := appPIDBounded(ios, udid, bundle)
		if err != nil || pid <= 0 {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}
