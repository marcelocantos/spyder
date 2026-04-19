// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// AndroidAdapter talks to Android devices via adb. Unlike iOS it does not
// need a KeepAwake companion app — Android offers a native "stay on while
// plugged in" developer setting. keepawake is therefore a gentle no-op
// that points the user at the OS setting.
type AndroidAdapter struct {
	mu    sync.Mutex
	cache map[string]cachedState
}

// NewAndroidAdapter returns a new Android adapter.
func NewAndroidAdapter() *AndroidAdapter {
	return &AndroidAdapter{cache: map[string]cachedState{}}
}

// List returns connected Android devices via `adb devices -l`. Each device
// is queried for its model and OS version via `getprop`. Unauthorized or
// offline devices are included but their Model/OS may be empty.
func (a *AndroidAdapter) List() ([]Info, error) {
	if _, err := exec.LookPath("adb"); err != nil {
		return nil, nil // adb not installed → treat as "no Android devices"
	}
	out, err := exec.Command("adb", "devices", "-l").Output()
	if err != nil {
		return nil, fmt.Errorf("adb devices: %w", err)
	}
	var devices []Info
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		serial := fields[0]
		state := fields[1]
		info := Info{
			UUID:     serial,
			Platform: "android",
			Name:     serial,
		}
		for _, f := range fields[2:] {
			if k, v, ok := strings.Cut(f, ":"); ok {
				switch k {
				case "model":
					info.Model = v
				case "device":
					if info.Model == "" {
						info.Model = v
					}
				}
			}
		}
		if state == "device" {
			// getprop queries require the device to be fully online.
			if m := getprop(serial, "ro.product.model"); m != "" {
				info.Model = m
			}
			if v := getprop(serial, "ro.build.version.release"); v != "" {
				info.OS = "Android " + v
			}
		}
		devices = append(devices, info)
	}
	return devices, nil
}

func getprop(serial, key string) string {
	out, err := exec.Command("adb", "-s", serial, "shell", "getprop", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// State reports Android device state via adb dumpsys. Battery level,
// charging status, and foreground app come from `dumpsys battery` and
// `dumpsys activity activities`. Results are cached per-device for
// stateTTL (shared with the iOS adapter).
func (a *AndroidAdapter) State(id string) (State, error) {
	if id == "" {
		return State{}, errors.New("device identifier is empty")
	}

	a.mu.Lock()
	if c, ok := a.cache[id]; ok && time.Since(c.at) < stateTTL {
		s := c.state
		a.mu.Unlock()
		return s, nil
	}
	a.mu.Unlock()

	if _, err := exec.LookPath("adb"); err != nil {
		return State{}, fmt.Errorf("adb not found in PATH: %w", err)
	}

	var state State

	battOut, battStderr, battErr := runCapture("adb", "-s", id, "shell", "dumpsys", "battery")
	combined := string(battStderr) + " " + string(battOut)
	if isAndroidDeviceNotConnected(combined) {
		return State{}, fmt.Errorf("device not connected: %s", id)
	}
	if battErr != nil {
		state.Notes = append(state.Notes, fmt.Sprintf("battery data unavailable: %s", truncate(string(battStderr), 160)))
	} else if level, charging, err := parseAndroidBattery(battOut); err != nil {
		state.Notes = append(state.Notes, fmt.Sprintf("battery parse error: %v", err))
	} else {
		state.BatteryLevel = &level
		state.Charging = &charging
	}

	if fg, err := androidForegroundApp(id); err != nil {
		state.Notes = append(state.Notes, fmt.Sprintf("foreground app unavailable: %v", err))
	} else {
		state.ForegroundApp = fg
	}

	state.Notes = append(state.Notes, "thermal state not yet wired on Android")

	a.mu.Lock()
	a.cache[id] = cachedState{state: state, at: time.Now()}
	a.mu.Unlock()

	return state, nil
}

// parseAndroidBattery extracts level and charging status from
// `dumpsys battery` output. Charging is true when any of AC/USB/
// Wireless/Dock is powered.
func parseAndroidBattery(data []byte) (level int, charging bool, err error) {
	levelFound := false
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			switch k {
			case "level":
				if n, err := strconv.Atoi(v); err == nil {
					level = n
					levelFound = true
				}
			case "AC powered", "USB powered", "Wireless powered", "Dock powered":
				if v == "true" {
					charging = true
				}
			}
		}
	}
	if !levelFound {
		return 0, false, errors.New("no 'level' field in dumpsys battery output")
	}
	return level, charging, nil
}

// androidForegroundApp returns the foreground activity's package id
// parsed from `dumpsys activity activities`.
var fgActivityRE = regexp.MustCompile(`ActivityRecord\{[^}]*\s([a-zA-Z0-9_.]+)/[^}]+\}`)

func androidForegroundApp(id string) (string, error) {
	out, stderr, err := runCapture("adb", "-s", id, "shell", "dumpsys", "activity", "activities")
	if err != nil {
		return "", fmt.Errorf("%s", truncate(string(stderr), 120))
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if !strings.Contains(line, "mFocusedApp=") && !strings.Contains(line, "mResumedActivity") {
			continue
		}
		if m := fgActivityRE.FindStringSubmatch(line); len(m) >= 2 {
			return m[1], nil
		}
	}
	return "", errors.New("no focused or resumed activity found")
}

// isAndroidDeviceNotConnected recognises adb's "not found"/"offline"/
// "unauthorized" error messages from combined stdout+stderr.
func isAndroidDeviceNotConnected(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "device offline") ||
		strings.Contains(l, "device unauthorized") ||
		strings.Contains(l, "no devices/emulators found") ||
		(strings.Contains(l, "device") && strings.Contains(l, "not found"))
}

// LaunchKeepAwake is a no-op on Android: the OS provides a native
// "Stay awake while plugged in" developer setting. Returns nil to signal
// success; the tool handler surfaces a helpful message pointing the user
// at the setting.
func (a *AndroidAdapter) LaunchKeepAwake(id string) error {
	return nil
}

// ListApps returns third-party packages via `adb shell pm list packages -3`.
// Only bundle ids are populated; per-app names/versions would need a
// dumpsys pass per package and are deferred until a use case lands.
func (a *AndroidAdapter) ListApps(id string) ([]AppInfo, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return nil, fmt.Errorf("adb not found in PATH: %w", err)
	}
	out, stderr, err := runCapture("adb", "-s", id, "shell", "pm", "list", "packages", "-3")
	combined := string(stderr) + " " + string(out)
	if isAndroidDeviceNotConnected(combined) {
		return nil, fmt.Errorf("device not connected: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("adb pm list packages: %w", err)
	}
	apps := []AppInfo{}
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if pkg, ok := strings.CutPrefix(line, "package:"); ok {
			apps = append(apps, AppInfo{BundleID: pkg})
		}
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].BundleID < apps[j].BundleID })
	return apps, nil
}

// LaunchApp foregrounds an app via `adb shell monkey -p <pkg> -c LAUNCHER 1`.
func (a *AndroidAdapter) LaunchApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	out, stderr, err := runCapture("adb", "-s", id, "shell", "monkey", "-p", bundleID, "-c", "android.intent.category.LAUNCHER", "1")
	combined := string(stderr) + " " + string(out)
	if isAndroidDeviceNotConnected(combined) {
		return fmt.Errorf("device not connected: %s", id)
	}
	if strings.Contains(strings.ToLower(combined), "no activities found") ||
		strings.Contains(strings.ToLower(combined), "no packages found") {
		return fmt.Errorf("app not installed or has no launcher activity: %s", bundleID)
	}
	if err != nil {
		return fmt.Errorf("adb monkey: %v\n%s", err, truncate(string(stderr), 200))
	}
	return nil
}

// TerminateApp stops an app via `adb shell am force-stop <pkg>`.
func (a *AndroidAdapter) TerminateApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	_, stderr, err := runCapture("adb", "-s", id, "shell", "am", "force-stop", bundleID)
	if isAndroidDeviceNotConnected(string(stderr)) {
		return fmt.Errorf("device not connected: %s", id)
	}
	if err != nil {
		return fmt.Errorf("adb force-stop: %v\n%s", err, truncate(string(stderr), 200))
	}
	return nil
}

// androidRotationValues maps canonical orientation names to the Android
// user_rotation setting value. Android's user_rotation is a clockwise
// rotation index: 0=portrait, 1=landscape-left (90° CW), 2=portrait-upside-down
// (180°), 3=landscape-right (270° CW / 90° CCW).
var androidRotationValues = map[string]int{
	"portrait":             0,
	"landscape-left":       1,
	"portrait-upside-down": 2,
	"landscape-right":      3,
}

// isEmulatorSerial returns true when serial matches the `emulator-*`
// pattern that `adb devices` uses for running Android emulators.
func isEmulatorSerial(serial string) bool {
	return strings.HasPrefix(serial, "emulator-")
}

// androidCurrentRotation reads the emulator's current user_rotation
// via `adb shell settings get system user_rotation`. Returns 0..3.
func androidCurrentRotation(serial string) (int, error) {
	out, stderr, err := runCapture("adb", "-s", serial, "shell", "settings", "get", "system", "user_rotation")
	if err != nil {
		return 0, fmt.Errorf("adb settings get user_rotation: %v\n%s", err, truncate(string(stderr), 160))
	}
	val, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse user_rotation %q: %w", strings.TrimSpace(string(out)), err)
	}
	return val, nil
}

// Rotate drives an Android emulator to the specified orientation by
// reading its current rotation and issuing the required number of
// `adb emu rotate` (90° CW) commands. Physical Android devices return
// an error — only emulator serials (matching "emulator-*") are supported.
func (a *AndroidAdapter) Rotate(id, orientation string) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	if !isEmulatorSerial(id) {
		return errors.New("rotation on real Android devices is not supported; only Android emulators (serial prefix 'emulator-') support programmatic rotation")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return fmt.Errorf("adb not found in PATH: %w", err)
	}
	target, ok := androidRotationValues[orientation]
	if !ok {
		return fmt.Errorf("unsupported orientation %q; valid values: portrait, landscape-left, landscape-right, portrait-upside-down", orientation)
	}
	current, err := androidCurrentRotation(id)
	if err != nil {
		return fmt.Errorf("read current rotation: %w", err)
	}
	// adb emu rotate toggles 90° CW each call. Calculate how many rotations
	// are needed to reach the target orientation from the current one.
	steps := (target - current + 4) % 4
	for range steps {
		_, stderr, err := runCapture("adb", "-s", id, "emu", "rotate")
		if err != nil {
			return fmt.Errorf("adb emu rotate: %v\n%s", err, truncate(string(stderr), 160))
		}
	}
	return nil
}

// Crashes fetches crash reports from an Android device. It first attempts
// to pull tombstones from /data/tombstones/ via adb (root-capable devices
// only). When that fails or returns nothing, it falls back to parsing
// `adb logcat -b crash` output.
//
// The since filter is applied to tombstone mtimes (when available) and
// to the logcat timestamp. The process filter matches the process tag
// embedded in logcat lines. Reports are returned newest-first.
func (a *AndroidAdapter) Crashes(id string, since time.Time, process string) ([]CrashReport, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return nil, fmt.Errorf("adb not found in PATH: %w", err)
	}

	// Attempt tombstone pull first.
	reports, tombErr := androidTombstoneCrashes(id, since, process)
	if tombErr == nil && len(reports) > 0 {
		return reports, nil
	}

	// Fall back to logcat crash buffer.
	return androidLogcatCrashes(id, since, process)
}

// androidTombstoneCrashes tries to pull tombstone files from
// /data/tombstones/ via adb pull. Only works on rooted or
// adb-root-enabled devices.
func androidTombstoneCrashes(id string, since time.Time, process string) ([]CrashReport, error) {
	tmp, err := os.MkdirTemp("", "spyder-tombstones-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	out, stderr, err := runCapture("adb", "-s", id, "pull", "/data/tombstones/.", tmp)
	if err != nil {
		// Not root or directory doesn't exist — expected on non-rooted devices.
		return nil, fmt.Errorf("adb pull /data/tombstones: %v\n%s",
			err, truncate(string(stderr)+string(out), 200))
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		return nil, fmt.Errorf("read pulled tombstones: %w", err)
	}

	var reports []CrashReport
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fpath := filepath.Join(tmp, e.Name())
		fi, err := e.Info()
		if err != nil {
			continue
		}
		mtime := fi.ModTime().UTC()
		if !since.IsZero() && mtime.Before(since) {
			continue
		}
		data, err := os.ReadFile(fpath)
		if err != nil {
			continue
		}
		cr := parseTombstone(data, mtime, process)
		if cr == nil {
			continue
		}
		reports = append(reports, *cr)
	}

	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Timestamp.After(reports[j].Timestamp)
	})
	return reports, nil
}

// parseTombstone extracts a CrashReport from a tombstone file. Returns
// nil when the process filter is set and the tombstone doesn't match.
func parseTombstone(data []byte, mtime time.Time, processFilter string) *CrashReport {
	cr := CrashReport{Timestamp: mtime, Raw: string(data)}
	// Tombstone header lines look like:
	//   pid: 1234, tid: 1234, name: my_process  >>> com.example.app <<<
	// or just:
	//   Cmd line: com.example.app
	for _, line := range strings.SplitN(string(data), "\n", 30) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "pid:") {
			// Extract name: field.
			if i := strings.Index(line, "name: "); i >= 0 {
				rest := line[i+len("name: "):]
				if j := strings.Index(rest, " "); j > 0 {
					cr.Process = rest[:j]
				} else {
					cr.Process = strings.TrimSpace(rest)
				}
			}
		} else if strings.HasPrefix(line, "Cmd line:") {
			cr.Process = strings.TrimSpace(strings.TrimPrefix(line, "Cmd line:"))
		} else if strings.HasPrefix(line, "signal ") || strings.HasPrefix(line, "Abort message:") {
			if cr.Reason == "" {
				cr.Reason = line
			}
		}
		if cr.Process != "" && cr.Reason != "" {
			break
		}
	}

	if processFilter != "" && !strings.EqualFold(cr.Process, processFilter) {
		return nil
	}
	return &cr
}

// logcatCrashRE matches the start of an AndroidRuntime crash block:
//
//	MM-DD HH:MM:SS.mmm  PID  TID E AndroidRuntime: FATAL EXCEPTION: <thread>
var logcatCrashRE = regexp.MustCompile(`^(\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d+)\s+\d+\s+\d+\s+\w\s+(\S+)\s*:\s+(.*)`)

// androidLogcatCrashes reads the crash logcat buffer and parses crash
// blocks. When since is non-zero, it passes the timestamp to logcat via
// -T; otherwise it reads the full crash buffer.
func androidLogcatCrashes(id string, since time.Time, processFilter string) ([]CrashReport, error) {
	args := []string{"-s", id, "logcat", "-b", "crash", "-d", "-v", "threadtime"}
	if !since.IsZero() {
		// adb logcat -T accepts "MM-DD HH:MM:SS.mmm" or a Unix timestamp.
		args = append(args, "-T", since.Format("01-02 15:04:05.000"))
	}
	out, stderr, err := runCapture("adb", args...)
	combined := string(stderr) + " " + string(out)
	if isAndroidDeviceNotConnected(combined) {
		return nil, fmt.Errorf("device not connected: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("adb logcat: %v\n%s", err, truncate(string(stderr), 200))
	}
	return parseLogcatCrashBuffer(string(out), since, processFilter), nil
}

// parseLogcatCrashBuffer splits logcat output into per-process crash
// blocks and returns one CrashReport per block.
func parseLogcatCrashBuffer(output string, since time.Time, processFilter string) []CrashReport {
	var reports []CrashReport
	var curProc, curReason string
	var curTS time.Time
	var curLines []string

	flush := func() {
		if curProc == "" {
			return
		}
		if processFilter != "" && !strings.EqualFold(curProc, processFilter) {
			curProc, curReason, curTS, curLines = "", "", time.Time{}, nil
			return
		}
		reports = append(reports, CrashReport{
			Process:   curProc,
			Reason:    curReason,
			Timestamp: curTS,
			Raw:       strings.Join(curLines, "\n"),
		})
		curProc, curReason, curTS, curLines = "", "", time.Time{}, nil
	}

	for _, line := range strings.Split(output, "\n") {
		m := logcatCrashRE.FindStringSubmatch(line)
		if m == nil {
			if curProc != "" {
				curLines = append(curLines, line)
			}
			continue
		}
		tsStr, tag, msg := m[1], m[2], m[3]
		// Logcat timestamp has no year; assume current year.
		ts, err := time.Parse("2006 01-02 15:04:05.000", fmt.Sprintf("%d %s", time.Now().Year(), tsStr))
		if err != nil {
			ts = time.Time{}
		} else {
			ts = ts.UTC()
		}
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			continue
		}
		if strings.Contains(msg, "FATAL EXCEPTION") {
			// A FATAL EXCEPTION line starts a new crash block.
			flush()
			curProc = tag
			curTS = ts
			curReason = msg
			curLines = []string{line}
		} else if curProc != "" {
			// Continuation line within an open crash block.
			curLines = append(curLines, line)
		}
	}
	flush()

	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Timestamp.After(reports[j].Timestamp)
	})
	return reports
}

// androidDeviceRecordPath is where screenrecord writes on the device.
const androidDeviceRecordPath = "/sdcard/spyder-recording.mp4"

// StartRecording starts `adb shell screenrecord` on the device. The
// screenrecord binary writes to androidDeviceRecordPath on the device;
// dest is the local path the file will be pulled to by the stopFn.
//
// The command runs in the background; stopFn sends SIGINT so the mp4 is
// properly finalised before we pull it.
func (a *AndroidAdapter) StartRecording(id, dest string) (func() error, int, error) {
	if id == "" {
		return nil, 0, errors.New("adb: device identifier is empty")
	}
	if dest == "" {
		return nil, 0, errors.New("adb: dest path is required")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return nil, 0, fmt.Errorf("adb not found in PATH: %w", err)
	}

	// Remove stale recording from a previous interrupted session.
	_, _, _ = runCapture("adb", "-s", id, "shell", "rm", "-f", androidDeviceRecordPath)

	cmd := exec.Command("adb", "-s", id, "shell", "screenrecord",
		"--bit-rate", "4000000",
		androidDeviceRecordPath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, 0, fmt.Errorf("adb screenrecord: %w", err)
	}

	pid := cmd.Process.Pid
	stopFn := func() error {
		// SIGINT the local adb shell; it propagates to screenrecord on device.
		if sigErr := cmd.Process.Signal(syscall.SIGINT); sigErr != nil && !errors.Is(sigErr, os.ErrProcessDone) {
			// Fall back to killing screenrecord on device via adb killall.
			_, _, _ = runCapture("adb", "-s", id, "shell", "killall", "-SIGINT", "screenrecord")
		}
		// Wait for the local adb shell to exit.
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		case <-done:
		}
		// Pull the recorded file to dest.
		if _, _, pullErr := runCapture("adb", "-s", id, "pull", androidDeviceRecordPath, dest); pullErr != nil {
			return fmt.Errorf("adb pull %s → %s: %w", androidDeviceRecordPath, dest, pullErr)
		}
		// Clean up on device.
		_, _, _ = runCapture("adb", "-s", id, "shell", "rm", "-f", androidDeviceRecordPath)
		return nil
	}

	return stopFn, pid, nil
}

// StopRecording signals the local adb shell (which propagates to screenrecord
// on device) via SIGINT. For full cleanup including the device-side pull,
// use the stopFn returned by StartRecording directly. This method exists to
// satisfy the Adapter interface.
func (a *AndroidAdapter) StopRecording(id string, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("adb StopRecording: invalid pid %d", pid)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("adb StopRecording: find process %d: %w", pid, err)
	}
	if sigErr := proc.Signal(syscall.SIGINT); sigErr != nil && !errors.Is(sigErr, os.ErrProcessDone) {
		_, _, _ = runCapture("adb", "-s", id, "shell", "killall", "-SIGINT", "screenrecord")
	}
	return nil
}

// InstallApp installs an APK via `adb -s <serial> install -r <path>`.
// The -r flag replaces an existing installation if present.
func (a *AndroidAdapter) InstallApp(id, path string) error {
	if id == "" || path == "" {
		return errors.New("device id and path are required")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return fmt.Errorf("adb not found in PATH: %w", err)
	}
	out, stderr, err := runCapture("adb", "-s", id, "install", "-r", path)
	combined := string(stderr) + " " + string(out)
	if isAndroidDeviceNotConnected(combined) {
		return fmt.Errorf("device not connected: %s", id)
	}
	if err != nil {
		return fmt.Errorf("adb install: %v\n%s", err, truncate(combined, 300))
	}
	// adb install may exit 0 but include FAILURE in stdout.
	if strings.Contains(strings.ToUpper(string(out)), "FAILURE") {
		return fmt.Errorf("adb install failed: %s", truncate(string(out), 300))
	}
	return nil
}

// UninstallApp removes a package via `adb -s <serial> uninstall <package>`.
func (a *AndroidAdapter) UninstallApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return fmt.Errorf("adb not found in PATH: %w", err)
	}
	out, stderr, err := runCapture("adb", "-s", id, "uninstall", bundleID)
	combined := string(stderr) + " " + string(out)
	if isAndroidDeviceNotConnected(combined) {
		return fmt.Errorf("device not connected: %s", id)
	}
	if err != nil {
		return fmt.Errorf("adb uninstall: %v\n%s", err, truncate(combined, 300))
	}
	return nil
}

// AppPID returns the process id of a running app via `adb shell pidof <pkg>`.
// Returns an error if the app is not running or pidof output is empty.
func (a *AndroidAdapter) AppPID(id, bundleID string) (int, error) {
	if id == "" || bundleID == "" {
		return 0, errors.New("device id and bundle_id are required")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return 0, fmt.Errorf("adb not found in PATH: %w", err)
	}
	out, stderr, err := runCapture("adb", "-s", id, "shell", "pidof", bundleID)
	if isAndroidDeviceNotConnected(string(stderr)) {
		return 0, fmt.Errorf("device not connected: %s", id)
	}
	if err != nil {
		return 0, fmt.Errorf("app not running: %s", bundleID)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0, fmt.Errorf("app not running: %s", bundleID)
	}
	// pidof may return multiple PIDs; we take the first.
	fields := strings.Fields(s)
	pid, perr := strconv.Atoi(fields[0])
	if perr != nil || pid <= 0 {
		return 0, fmt.Errorf("unexpected pidof output for %s: %q", bundleID, s)
	}
	return pid, nil
}

// Screenshot captures a PNG via `adb shell screencap -p`. The subcommand
// writes the PNG to stdout, which we return unchanged.
func (a *AndroidAdapter) Screenshot(id string) ([]byte, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return nil, fmt.Errorf("adb not found in PATH: %w", err)
	}
	out, stderr, err := runCapture("adb", "-s", id, "shell", "screencap", "-p")
	combined := string(stderr) + " " + string(out[:min(len(out), 512)])
	if isAndroidDeviceNotConnected(combined) {
		return nil, fmt.Errorf("device not connected: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("adb screencap: %v\n%s", err, truncate(string(stderr), 200))
	}
	if len(out) == 0 {
		return nil, errors.New("adb screencap returned empty output")
	}
	return out, nil
}
