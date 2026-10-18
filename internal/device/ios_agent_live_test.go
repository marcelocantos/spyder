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
	"sync"
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
		runAgentJourney(t, iphone, ipad, filepath.Join(evidence, "journey-iphone.txt"), filepath.Join(evidence, "iphone-screen.png"))
	})
	t.Run("ipad", func(t *testing.T) {
		runAgentJourney(t, ipad, iphone, filepath.Join(evidence, "journey-ipad.txt"), filepath.Join(evidence, "ipad-screen.png"))
	})
}

func runAgentJourney(t *testing.T, udid, other, logPath, shotPath string) {
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
	logf("Disconnect resets this USB device only. The real usbmux listener owns the tether edge. go-ios ListDevices serials are logged inside List before enrichment. Ready requires a fresh AppPID match.")

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
	ios.BindSupervisor(sup)

	installsBefore := host.InstallCount()
	first, err := sup.Ensure(udid, holder)
	logf("ensure session-a: condition=%s ready=%v pid=%d install_gen=%s relaunched=%v reinstalled=%v attempts=%d installs=%d launches=%d boot=%s lock=%q err=%v",
		first.Condition, first.Ready, first.Runner.PID, first.Runner.InstallGen, first.Relaunched, first.Reinstalled, first.Attempts, host.InstallCount(), host.LaunchCount(), first.BootID, host.LockDetail(udid), err)
	logf("runner probe: %s", host.ProbeNote(udid))
	logConsent(logf, "session-a", first)
	if err != nil || !first.Ready || first.Runner.PID <= 0 {
		saveShot(logf, ios, udid, shotPath)
		t.Fatalf("session-a did not reach a healthy runner: %+v err=%v", first, err)
	}
	if first.BootID == "" {
		t.Fatal("probe did not observe a device boot id")
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
	host.NoteLaunched(udid, TestBouncingBallBundleID)
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

	var serialMu sync.Mutex
	var latest, gap []string
	ios.SetUsbmuxSeen(func(serials []string) {
		serialMu.Lock()
		defer serialMu.Unlock()
		latest = append([]string(nil), serials...)
		if gap == nil && !udidIn(serials, udid) && udidIn(serials, other) {
			gap = append([]string(nil), serials...)
		}
	})
	listenCtx, listenCancel := context.WithCancel(context.Background())
	defer listenCancel()
	go ios.StartTunnelListener(listenCtx)
	// Startup attaches are probed on the listener thread, one device at
	// a time. Reset only after both probes have landed, or the other
	// device's probe keeps the detach event unread.
	attachDeadline := time.Now().Add(120 * time.Second)
	for {
		snap, ok := sup.Snapshot(udid)
		_, otherOK := sup.Snapshot(other)
		if ok && otherOK && (snap.Condition == agent.CondReconnected || snap.Condition == agent.CondReady) {
			logf("usbmux listener finished the startup snapshot: condition=%s ready=%v", snap.Condition, snap.Ready)
			break
		}
		if time.Now().After(attachDeadline) {
			t.Fatal("real usbmux listener did not finish the startup snapshot")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := ios.List(); err != nil {
		t.Fatal(err)
	}
	serialMu.Lock()
	beforeIDs := append([]string(nil), latest...)
	serialMu.Unlock()
	logf("ios usbmux devices before detach: %v", beforeIDs)
	if !udidIn(beforeIDs, udid) || !udidIn(beforeIDs, other) {
		t.Fatalf("ListDevices serials before detach: %v", beforeIDs)
	}
	if err := resetUSBDevice(udid); err != nil {
		t.Fatal(err)
	}
	gapDeadline := time.Now().Add(3 * time.Second)
	for {
		_, _ = ios.List()
		serialMu.Lock()
		gotGap := gap != nil
		serialMu.Unlock()
		if gotGap {
			break
		}
		if time.Now().After(gapDeadline) {
			serialMu.Lock()
			logf("ios usbmux devices after USB reset: %v", latest)
			serialMu.Unlock()
			t.Fatal("ListDevices still contains the reset device")
		}
	}
	serialMu.Lock()
	gapIDs := append([]string(nil), gap...)
	serialMu.Unlock()
	logf("ios usbmux devices during detach: %v", gapIDs)
	downDeadline := time.Now().Add(15 * time.Second)
	var down agent.Snapshot
	var downOK bool
	for {
		down, downOK = sup.Snapshot(udid)
		if downOK && !down.Ready && down.Condition == agent.CondDisconnected {
			break
		}
		if time.Now().After(downDeadline) {
			t.Fatalf("disconnect serials=%v snap=%+v", gapIDs, down)
		}
		time.Sleep(100 * time.Millisecond)
	}
	logf("listener detach: serials=%v condition=%s ready=%v recovered=%v",
		gapIDs, down.Condition, down.Ready, down.Recovered)

	upDeadline := time.Now().Add(60 * time.Second)
	var up agent.Snapshot
	var upIDs []string
	var upOK bool
	for {
		_, _ = ios.List()
		serialMu.Lock()
		upIDs = append([]string(nil), latest...)
		serialMu.Unlock()
		up, upOK = sup.Snapshot(udid)
		if udidIn(upIDs, udid) && udidIn(upIDs, other) && upOK && up.Condition == agent.CondReconnected {
			break
		}
		if time.Now().After(upDeadline) {
			t.Fatalf("reconnect serials=%v snap=%+v", upIDs, up)
		}
		time.Sleep(200 * time.Millisecond)
	}
	rePID, reErr := probeApp(ios, udid, alertRunnerBundle, appProbeAttempts)
	logf("listener reattach: serials=%v condition=%s ready=%v recovered=%v snapshot=%v runner_pid=%d fresh_pid=%d fresh_err=%v lock=%q boot=%s",
		upIDs, up.Condition, up.Ready, up.Recovered, upOK, up.Runner.PID, rePID, reErr, host.LockDetail(udid), up.BootID)
	if up.Ready && (reErr != nil || rePID <= 0 || rePID != up.Runner.PID) {
		t.Fatalf("ready without a matching AppPID: fresh=%d snap=%d err=%v", rePID, up.Runner.PID, reErr)
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

func udidIn(ids []string, udid string) bool {
	for _, id := range ids {
		if id == udid {
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

// usbResetPy asks libusb to reset one Apple device. The reset drops
// that device off usbmux for a short interval and the kernel brings
// it back. Entity-not-found is the device leaving the bus.
const usbResetPy = `
import sys
import usb.core
import usb.util
import usb.backend.libusb1

serial = sys.argv[1]
backend = None
for path in ("/opt/homebrew/lib/libusb-1.0.dylib", "/usr/local/lib/libusb-1.0.dylib"):
    backend = usb.backend.libusb1.get_backend(find_library=lambda _name, path=path: path)
    if backend is not None:
        break
if backend is None:
    backend = usb.backend.libusb1.get_backend()
dev = None
for d in usb.core.find(find_all=True, idVendor=0x05AC, backend=backend):
    sn = usb.util.get_string(d, d.iSerialNumber)
    if not sn:
        continue
    sn = sn.split("\x00", 1)[0]
    if sn == serial:
        dev = d
        break
if dev is None:
    raise SystemExit("usb device not found for " + serial)
try:
    dev.reset()
except usb.core.USBError as exc:
    text = str(exc)
    if getattr(exc, "errno", None) == 2 or "Entity not found" in text or "No such entity" in text:
        raise SystemExit(0)
    raise
`

func pythonWithUSB() (string, error) {
	var last error
	for _, bin := range []string{"python3", "/Users/marcelo/.py/bin/python3"} {
		cmd := exec.Command(bin, "-c", "import usb")
		if err := cmd.Run(); err == nil {
			return bin, nil
		} else {
			last = err
		}
	}
	return "", fmt.Errorf("python3 with pyusb: %w", last)
}

func resetUSBDevice(udid string) error {
	py, err := pythonWithUSB()
	if err != nil {
		return err
	}
	serial := strings.ReplaceAll(udid, "-", "")
	cmd := exec.Command(py, "-c", usbResetPy, serial)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("usb reset %s: %v: %s", serial, err, out)
	}
	return nil
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
