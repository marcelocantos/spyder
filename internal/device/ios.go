// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// KeepAwakeBundleID is the bundle identifier of the ios/KeepAwake companion
// app. Any iOS device that should hold its screen awake must have this app
// installed; LaunchKeepAwake foregrounds it via devicectl.
const KeepAwakeBundleID = "com.marcelocantos.spyder.KeepAwake"

// ErrLocked is returned when an operation fails specifically because the
// target device is locked. Callers can errors.Is check this to fire a
// targeted notification and retry until the device is unlocked.
var ErrLocked = errors.New("device is locked")

// stateTTL bounds how often we re-query a device. Tools called in quick
// succession (e.g. from an agent reasoning loop) share a snapshot so the
// device isn't hammered.
const stateTTL = 2 * time.Second

// IOSAdapter talks to iOS devices via pymobiledevice3 and devicectl.
type IOSAdapter struct {
	mu    sync.Mutex
	cache map[string]cachedState
}

type cachedState struct {
	state State
	at    time.Time
}

// NewIOSAdapter returns a new iOS adapter.
func NewIOSAdapter() *IOSAdapter { return &IOSAdapter{cache: map[string]cachedState{}} }

// List returns connected iOS devices by fusing two sources:
//
//   - `pymobiledevice3 usbmux list` for USB-connected devices (keyed by
//     hardware UDID; gives device name + product type + iOS version).
//   - `xcrun devicectl list devices` for paired CoreDevice devices
//     (gives marketingName, CoreDevice UUID, and richer metadata; may
//     include devices that aren't currently USB-connected).
//
// Devices present in both sources are fused into a single entry keyed
// by hardware UDID; devices present in only one source are listed
// with whatever metadata is available. If both shell-outs fail, the
// function returns an empty list rather than an error — matching the
// Android adapter's behaviour when adb is absent.
func (a *IOSAdapter) List() ([]Info, error) {
	var devices []Info

	if _, err := exec.LookPath("pymobiledevice3"); err == nil {
		if out, _, err := runCapture("pymobiledevice3", "usbmux", "list"); err == nil {
			if parsed, err := parseUsbmuxList(out); err == nil {
				devices = append(devices, parsed...)
			}
		}
	}

	if _, err := exec.LookPath("xcrun"); err == nil {
		tmp, err := os.MkdirTemp("", "spyder-devctl-*")
		if err == nil {
			defer os.RemoveAll(tmp)
			jsonPath := filepath.Join(tmp, "devices.json")
			_, _, _ = runCapture("xcrun", "devicectl", "list", "devices",
				"--quiet", "--json-output", jsonPath)
			if data, err := os.ReadFile(jsonPath); err == nil {
				if parsed, err := parseDevicectlList(data); err == nil {
					devices = mergeIOSDevices(devices, parsed)
				}
			}
		}
	}

	return devices, nil
}

// parseUsbmuxList parses `pymobiledevice3 usbmux list` output (JSON
// array of USB-connected device records). Returns one Info per entry.
func parseUsbmuxList(data []byte) ([]Info, error) {
	var raw []struct {
		UniqueDeviceID string `json:"UniqueDeviceID"`
		DeviceName     string `json:"DeviceName"`
		DeviceClass    string `json:"DeviceClass"`
		ProductType    string `json:"ProductType"`
		ProductVersion string `json:"ProductVersion"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("usbmux list JSON: %w", err)
	}
	out := make([]Info, 0, len(raw))
	for _, r := range raw {
		if r.UniqueDeviceID == "" {
			continue
		}
		info := Info{
			UUID:     r.UniqueDeviceID,
			Name:     r.DeviceName,
			Platform: "ios",
			Model:    r.ProductType,
		}
		if r.ProductVersion != "" {
			info.OS = "iOS " + r.ProductVersion
		}
		out = append(out, info)
	}
	return out, nil
}

// parseDevicectlList parses the `xcrun devicectl list devices
// --json-output` document. devicectl emits a nested structure
// (result.devices[]); we flatten to []Info and pick the richest
// human-friendly fields available (marketingName over productType,
// device.name over the CoreDevice identifier).
func parseDevicectlList(data []byte) ([]Info, error) {
	var doc struct {
		Result struct {
			Devices []struct {
				Identifier         string `json:"identifier"`
				HardwareProperties struct {
					UDID          string `json:"udid"`
					MarketingName string `json:"marketingName"`
					ProductType   string `json:"productType"`
				} `json:"hardwareProperties"`
				DeviceProperties struct {
					Name            string `json:"name"`
					OSVersionNumber string `json:"osVersionNumber"`
				} `json:"deviceProperties"`
			} `json:"devices"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("devicectl list JSON: %w", err)
	}
	out := make([]Info, 0, len(doc.Result.Devices))
	for _, d := range doc.Result.Devices {
		udid := d.HardwareProperties.UDID
		if udid == "" {
			udid = d.Identifier // fall back to CoreDevice UUID
		}
		info := Info{
			UUID:     udid,
			Name:     d.DeviceProperties.Name,
			Platform: "ios",
		}
		if d.HardwareProperties.MarketingName != "" {
			info.Model = d.HardwareProperties.MarketingName
		} else {
			info.Model = d.HardwareProperties.ProductType
		}
		if d.DeviceProperties.OSVersionNumber != "" {
			info.OS = "iOS " + d.DeviceProperties.OSVersionNumber
		}
		out = append(out, info)
	}
	return out, nil
}

// mergeIOSDevices overlays devicectl-sourced entries onto a usbmux-
// sourced baseline, keyed by hardware UDID. devicectl's marketingName
// and name typically beat usbmux's ProductType/DeviceName for
// human-readability, so they win on conflict.
func mergeIOSDevices(base, overlay []Info) []Info {
	byUDID := make(map[string]int, len(base))
	for i, b := range base {
		byUDID[b.UUID] = i
	}
	for _, o := range overlay {
		if idx, ok := byUDID[o.UUID]; ok {
			if o.Name != "" {
				base[idx].Name = o.Name
			}
			if o.Model != "" {
				base[idx].Model = o.Model
			}
			if o.OS != "" {
				base[idx].OS = o.OS
			}
			continue
		}
		base = append(base, o)
		byUDID[o.UUID] = len(base) - 1
	}
	return base
}

// State reports iOS device state. Shells out to pymobiledevice3 for
// battery/charging data; thermal state and foreground app are currently
// returned as notes (MobileGestalt deprecated on iOS 17.4+; foreground
// app requires additional tooling). Results are cached per-device for
// stateTTL to avoid hammering the device.
func (a *IOSAdapter) State(id string) (State, error) {
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

	if _, err := exec.LookPath("pymobiledevice3"); err != nil {
		return State{}, fmt.Errorf("pymobiledevice3 not found in PATH: %w", err)
	}

	var state State

	batteryOut, batteryStderr, batteryErr := runCapture("pymobiledevice3", "diagnostics", "battery", "single", "--udid", id)
	// pymobiledevice3 sometimes exits 0 even when the device isn't
	// connected, logging the failure to stderr. Check stderr regardless
	// of exit code.
	batteryStderrStr := string(batteryStderr)
	if isDeviceNotConnected(batteryStderrStr) {
		return State{}, fmt.Errorf("device not connected: %s", id)
	}
	if batteryErr != nil || len(bytes.TrimSpace(batteryOut)) == 0 {
		state.Notes = append(state.Notes, fmt.Sprintf("battery data unavailable: %s", truncate(batteryStderrStr, 160)))
	} else if level, charging, err := parseBattery(batteryOut); err != nil {
		state.Notes = append(state.Notes, fmt.Sprintf("battery parse error: %v", err))
	} else {
		state.BatteryLevel = &level
		state.Charging = &charging
	}

	state.Notes = append(state.Notes,
		"thermal state unavailable on iOS 17.4+ (MobileGestalt deprecated)",
		"foreground app detection pending — not yet wired",
	)

	a.mu.Lock()
	a.cache[id] = cachedState{state: state, at: time.Now()}
	a.mu.Unlock()

	return state, nil
}

func parseBattery(data []byte) (level int, charging bool, err error) {
	var b struct {
		AppleRawCurrentCapacity   int  `json:"AppleRawCurrentCapacity"`
		AppleRawMaxCapacity       int  `json:"AppleRawMaxCapacity"`
		AppleRawExternalConnected bool `json:"AppleRawExternalConnected"`
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return 0, false, fmt.Errorf("battery JSON: %w", err)
	}
	if b.AppleRawMaxCapacity == 0 {
		return 0, false, errors.New("AppleRawMaxCapacity is zero")
	}
	level = int(float64(b.AppleRawCurrentCapacity) / float64(b.AppleRawMaxCapacity) * 100)
	charging = b.AppleRawExternalConnected
	return level, charging, nil
}

// runCapture runs a command and returns stdout, stderr, and the run error.
// Unlike exec.Cmd.Output/CombinedOutput, this keeps stdout and stderr
// separate so callers can parse JSON from stdout while still inspecting
// human-readable diagnostics from stderr (pymobiledevice3 sometimes logs
// errors to stderr with exit code 0).
func runCapture(name string, args ...string) (stdout, stderr []byte, err error) {
	var outBuf, errBuf bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}

func isDeviceNotConnected(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "nodeviceconnectederror") ||
		strings.Contains(s, "device not found") ||
		strings.Contains(s, "no devices connected")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Screenshot captures a PNG via `pymobiledevice3 developer dvt screenshot`.
// Requires a live tunneld session on iOS 17+; callers should gate on
// tunneld health before invoking.
func (a *IOSAdapter) Screenshot(id string) ([]byte, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	tmp, err := os.MkdirTemp("", "spyder-screenshot-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "screen.png")

	_, stderr, runErr := runCapture("pymobiledevice3", "developer", "dvt", "screenshot", out, "--udid", id)
	stderrStr := string(stderr)
	// pymobiledevice3 sometimes exits 0 without writing the file
	// (e.g. unknown UDID logged as ERROR to stderr). Inspect stderr
	// regardless of exit code.
	if isDeviceNotConnected(stderrStr) {
		return nil, fmt.Errorf("device not connected: %s", id)
	}
	if runErr != nil {
		return nil, fmt.Errorf("pymobiledevice3 dvt screenshot: %v\n%s", runErr, truncate(stderrStr, 240))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		// File wasn't created despite exit 0 — surface stderr so the
		// caller can see why (ERROR lines typically explain).
		return nil, fmt.Errorf("capture did not produce a file: %s", truncate(stderrStr, 240))
	}
	return data, nil
}

// ListApps returns installed user apps via `pymobiledevice3 apps list`.
// Does not need DDI/tunneld.
func (a *IOSAdapter) ListApps(id string) ([]AppInfo, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	out, stderr, err := runCapture("pymobiledevice3", "apps", "list", "--type", "User", "--udid", id)
	if isDeviceNotConnected(string(stderr)) {
		return nil, fmt.Errorf("device not connected: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("pymobiledevice3 apps list: %v\n%s", err, truncate(string(stderr), 200))
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parsing apps list: %w", err)
	}
	apps := make([]AppInfo, 0, len(raw))
	for bid, meta := range raw {
		apps = append(apps, AppInfo{
			BundleID: bid,
			Name:     firstNonEmpty(stringOf(meta["CFBundleDisplayName"]), stringOf(meta["CFBundleName"])),
			Version:  stringOf(meta["CFBundleShortVersionString"]),
		})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].BundleID < apps[j].BundleID })
	return apps, nil
}

// LaunchApp foregrounds an arbitrary app via `pymobiledevice3 developer dvt launch`.
// Requires tunneld.
func (a *IOSAdapter) LaunchApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	_, stderr, err := runCapture("pymobiledevice3", "developer", "dvt", "launch", bundleID, "--udid", id)
	stderrStr := string(stderr)
	// Classify known failure modes against the full stderr (the
	// DvtException marker can be tens of KB into a rich-console
	// traceback), then collapse to a short error for logging.
	if isDeviceNotConnected(stderrStr) {
		return fmt.Errorf("device not connected: %s", id)
	}
	if isIOSAppNotFound(stderrStr) {
		return fmt.Errorf("app not installed: %s", bundleID)
	}
	if isDeviceLocked(stderrStr) {
		return fmt.Errorf("launch %s on %s: %w", bundleID, id, ErrLocked)
	}
	if err != nil {
		return fmt.Errorf("dvt launch: %v\n%s", err, tailTruncate(stderrStr, 400))
	}
	return nil
}

// isDeviceLocked recognises pymobiledevice3's DvtException footer when
// the device's lock screen blocks a launch.
func isDeviceLocked(stderr string) bool {
	return strings.Contains(stderr, "could not be, unlocked") ||
		strings.Contains(stderr, "'Locked'") ||
		strings.Contains(stderr, "was not, or could not be")
}

// tailTruncate returns the last n characters of s with an ellipsis
// prefix. Useful when the signal is at the end of a long traceback.
func tailTruncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// TerminateApp stops an app via dvt: look up PID for bundle id, then kill.
// Requires tunneld.
func (a *IOSAdapter) TerminateApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	pidOut, pidStderr, pidErr := runCapture("pymobiledevice3", "developer", "dvt", "process-id-for-bundle-id", bundleID, "--udid", id)
	if isDeviceNotConnected(string(pidStderr)) {
		return fmt.Errorf("device not connected: %s", id)
	}
	if pidErr != nil {
		return fmt.Errorf("resolve pid: %v\n%s", pidErr, truncate(string(pidStderr), 200))
	}
	pid, err := parseIOSPID(pidOut)
	if err != nil {
		return fmt.Errorf("app not running: %s", bundleID)
	}
	_, killStderr, killErr := runCapture("pymobiledevice3", "developer", "dvt", "kill", strconv.Itoa(pid), "--udid", id)
	if killErr != nil {
		return fmt.Errorf("kill pid %d: %v\n%s", pid, killErr, truncate(string(killStderr), 200))
	}
	return nil
}

// parseIOSPID extracts a PID from `dvt process-id-for-bundle-id` output.
// The command can emit either a plain integer line or a structured
// "bundle_id -> PID" / "bundle_id: PID" form; we tolerate both. Bundle
// ids can contain '-', so we match the separators as substrings rather
// than a character class.
func parseIOSPID(out []byte) (int, error) {
	s := strings.TrimSpace(string(out))
	if i := strings.LastIndex(s, "->"); i >= 0 {
		s = strings.TrimSpace(s[i+2:])
	} else if i := strings.LastIndex(s, ":"); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	pid, err := strconv.Atoi(s)
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("no PID in %q", strings.TrimSpace(string(out)))
	}
	return pid, nil
}

// isIOSAppNotFound matches pymobiledevice3's stderr when a bundle id
// isn't installed on the device.
func isIOSAppNotFound(stderr string) bool {
	l := strings.ToLower(stderr)
	return strings.Contains(l, "not installed") ||
		strings.Contains(l, "application is not installed") ||
		(strings.Contains(l, "bundle") && strings.Contains(l, "not found"))
}

func stringOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// iosSimOrientations maps canonical orientation names to the xcrun simctl
// rotate argument. simctl uses camelCase orientation identifiers.
var iosSimOrientations = map[string]string{
	"portrait":             "portrait",
	"landscape-left":       "landscapeLeft",
	"landscape-right":      "landscapeRight",
	"portrait-upside-down": "portraitUpsideDown",
}

// isSimulatorID returns true when id looks like an iOS simulator UUID
// rather than a hardware device UDID.
//
// Hardware UDID format: exactly 8 hex + "-" + 16 hex, e.g.
//
//	00008103-000D39301A6A201E
//
// Simulator UUIDs follow the standard UUID4 shape (8-4-4-4-12 hex groups),
// matching devicectl / xcrun simctl output, e.g.
//
//	C6F6FA50-30B5-4E4C-B7A1-8E0F5D1E1FA8
//
// We detect the hardware pattern (single hyphen, groups of 8+16) and treat
// everything else as a simulator candidate.
func isSimulatorID(id string) bool {
	// Hardware UDID: exactly one hyphen, 8 chars before and 16 after.
	parts := strings.SplitN(id, "-", 2)
	if len(parts) == 2 && len(parts[0]) == 8 && len(parts[1]) == 16 {
		return false // physical device UDID
	}
	return true
}

// Rotate sets the screen orientation of an iOS simulator via
// `xcrun simctl io <udid> rotate <orientation>`. Physical iOS devices
// return an error — rotation requires physical movement and is not
// programmatically supported.
func (a *IOSAdapter) Rotate(id, orientation string) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	if !isSimulatorID(id) {
		return errors.New("rotation on real iOS devices is not supported; only iOS simulators support programmatic rotation")
	}
	arg, ok := iosSimOrientations[orientation]
	if !ok {
		return fmt.Errorf("unsupported orientation %q; valid values: portrait, landscape-left, landscape-right, portrait-upside-down", orientation)
	}
	_, stderr, err := runCapture("xcrun", "simctl", "io", id, "rotate", arg)
	if err != nil {
		return fmt.Errorf("simctl rotate: %v\n%s", err, truncate(string(stderr), 240))
	}
	return nil
}

// Crashes fetches .ips crash reports from the device via
// `pymobiledevice3 crash-reports pull`. Reports are filtered by the
// timestamp embedded in the filename and optionally by process name.
// Each .ips file's first line is a JSON header with structured metadata;
// we parse it for the summary and include the full file content in Raw.
// Reports are returned newest-first.
func (a *IOSAdapter) Crashes(id string, since time.Time, process string) ([]CrashReport, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	if _, err := exec.LookPath("pymobiledevice3"); err != nil {
		return nil, fmt.Errorf("pymobiledevice3 not found in PATH: %w", err)
	}

	// List available crash reports.
	listOut, listStderr, listErr := runCapture("pymobiledevice3", "crash-reports", "list", "--udid", id)
	if isDeviceNotConnected(string(listStderr)) {
		return nil, fmt.Errorf("device not connected: %s", id)
	}
	if listErr != nil {
		return nil, fmt.Errorf("crash-reports list: %v\n%s", listErr, truncate(string(listStderr), 200))
	}

	filenames, err := parseCrashReportList(listOut)
	if err != nil {
		return nil, fmt.Errorf("crash-reports list parse: %w", err)
	}

	// Filter by filename-embedded timestamp and optional process name.
	var filtered []ipsFileMeta
	for _, m := range filenames {
		if !since.IsZero() && m.ts.Before(since) {
			continue
		}
		if process != "" && !strings.EqualFold(m.process, process) {
			continue
		}
		filtered = append(filtered, m)
	}
	if len(filtered) == 0 {
		return []CrashReport{}, nil
	}

	// Pull reports into a temporary directory.
	tmp, err := os.MkdirTemp("", "spyder-crashes-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	_, pullStderr, pullErr := runCapture("pymobiledevice3", "crash-reports", "pull", tmp, "--udid", id)
	if pullErr != nil {
		return nil, fmt.Errorf("crash-reports pull: %v\n%s", pullErr, truncate(string(pullStderr), 200))
	}

	var reports []CrashReport
	for _, m := range filtered {
		path := filepath.Join(tmp, m.filename)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			// File may not be present if pull raced or the filter was
			// approximate. Include a minimal record without raw content.
			reports = append(reports, CrashReport{
				Process:   m.process,
				Timestamp: m.ts,
			})
			continue
		}
		cr := parseIPSReport(data, m)
		reports = append(reports, cr)
	}

	// Sort newest-first.
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Timestamp.After(reports[j].Timestamp)
	})
	return reports, nil
}

// ipsFileMeta holds metadata extracted from a crash report filename.
// iOS crash report names follow patterns such as:
//
//	<process>-<YYYY-MM-DD-HHmmss>[-<pid>].ips
type ipsFileMeta struct {
	filename string
	process  string
	ts       time.Time
}

// parseCrashReportList parses `pymobiledevice3 crash-reports list` output.
// The command emits a JSON array of filename strings. Some versions emit
// one filename per line instead; we handle both.
func parseCrashReportList(data []byte) ([]ipsFileMeta, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		// Fall back to newline-delimited plain text.
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				names = append(names, line)
			}
		}
	}
	out := make([]ipsFileMeta, 0, len(names))
	for _, name := range names {
		out = append(out, parseIPSFilename(name))
	}
	return out, nil
}

// ipsTimestampFormats are the datetime patterns we try when parsing the
// timestamp embedded in a crash report filename.
var ipsTimestampFormats = []string{
	"2006-01-02-150405",
	"2006-01-02-150405-0700",
}

// parseIPSFilename extracts process name and timestamp from an .ips filename.
// Returns zero time if no recognisable timestamp is found.
func parseIPSFilename(name string) ipsFileMeta {
	base := strings.TrimSuffix(filepath.Base(name), ".ips")
	parts := strings.SplitN(base, "-", 2)
	proc := parts[0]
	// Strip any trailing underscore-separated token (some tools append uuid).
	if i := strings.Index(proc, "_"); i > 0 {
		proc = proc[:i]
	}

	var ts time.Time
	if len(parts) > 1 {
		suffix := parts[1]
		for _, layout := range ipsTimestampFormats {
			// Try the full suffix, then sub-segments separated by hyphens.
			candidates := append([]string{suffix}, strings.Split(suffix, "-")...)
			for _, c := range candidates {
				if t, err := time.Parse(layout, c); err == nil {
					ts = t.UTC()
					break
				}
			}
			if !ts.IsZero() {
				break
			}
		}
	}
	return ipsFileMeta{filename: filepath.Base(name), process: proc, ts: ts}
}

// ipsHeader is the subset of .ips first-line JSON we care about.
type ipsHeader struct {
	ProcName      string `json:"procName"`
	ProcessName   string `json:"process_name"`
	CapturedTime  string `json:"captured_time"`
	ExceptionType string `json:"exception_type"`
	ExceptionInfo string `json:"exception_info"`
}

// parseIPSReport reads an .ips file, parses its first-line JSON header,
// and returns a CrashReport. The full raw content is stored in Raw.
func parseIPSReport(data []byte, meta ipsFileMeta) CrashReport {
	cr := CrashReport{
		Process:   meta.process,
		Timestamp: meta.ts,
		Raw:       string(data),
	}

	// The first line of an .ips file is a single JSON object.
	firstLine := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		firstLine = data[:i]
	}
	var hdr ipsHeader
	if err := json.Unmarshal(bytes.TrimSpace(firstLine), &hdr); err == nil {
		if hdr.ProcName != "" {
			cr.Process = hdr.ProcName
		} else if hdr.ProcessName != "" {
			cr.Process = hdr.ProcessName
		}
		if hdr.CapturedTime != "" {
			for _, layout := range []string{
				time.RFC3339,
				"2006-01-02 15:04:05.000 -0700",
				"2006-01-02T15:04:05Z",
			} {
				if t, err := time.Parse(layout, hdr.CapturedTime); err == nil {
					cr.Timestamp = t.UTC()
					break
				}
			}
		}
		reason := hdr.ExceptionType
		if hdr.ExceptionInfo != "" {
			if reason != "" {
				reason += ": " + hdr.ExceptionInfo
			} else {
				reason = hdr.ExceptionInfo
			}
		}
		cr.Reason = reason
	}
	return cr
}

// StartRecording is not supported on iOS physical devices. Use an iOS
// simulator (platform "ios-sim") for screen recording.
//
// The error message is structured so agents can detect the unsupported case:
// it contains the literal phrase "not supported on iOS physical devices".
func (a *IOSAdapter) StartRecording(id, dest string) (func() error, int, error) {
	return nil, 0, fmt.Errorf("screen recording is not supported on iOS physical devices; use a simulator — run `xcrun simctl list devices` to pick one, then pass its UDID directly")
}

// StopRecording is a no-op on iOS physical devices because StartRecording
// always errors. It exists only to satisfy the Adapter interface.
func (a *IOSAdapter) StopRecording(id string, pid int) error {
	return fmt.Errorf("screen recording is not supported on iOS physical devices")
}

// InstallApp installs a .app or .ipa bundle via `xcrun devicectl device
// install app`. The device id may be the hardware UDID, CoreDevice UUID,
// or any other identifier that devicectl --device accepts.
func (a *IOSAdapter) InstallApp(id, path string) error {
	if id == "" || path == "" {
		return errors.New("device id and path are required")
	}
	_, stderr, err := runCapture("xcrun", "devicectl", "device", "install", "app",
		"--device", id, path)
	if err != nil {
		return fmt.Errorf("devicectl install app: %v\n%s", err, truncate(string(stderr), 300))
	}
	return nil
}

// UninstallApp removes an app by bundle identifier via
// `xcrun devicectl device uninstall app --bundle-identifier`.
func (a *IOSAdapter) UninstallApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	_, stderr, err := runCapture("xcrun", "devicectl", "device", "uninstall", "app",
		"--device", id, "--bundle-identifier", bundleID)
	if err != nil {
		return fmt.Errorf("devicectl uninstall app: %v\n%s", err, truncate(string(stderr), 300))
	}
	return nil
}

// AppPID returns the process id of a running app by bundle id.
// Uses `pymobiledevice3 developer dvt process-id-for-bundle-id`.
// Returns an error if the app is not running. Requires tunneld.
func (a *IOSAdapter) AppPID(id, bundleID string) (int, error) {
	if id == "" || bundleID == "" {
		return 0, errors.New("device id and bundle_id are required")
	}
	pidOut, pidStderr, pidErr := runCapture("pymobiledevice3", "developer", "dvt",
		"process-id-for-bundle-id", bundleID, "--udid", id)
	if isDeviceNotConnected(string(pidStderr)) {
		return 0, fmt.Errorf("device not connected: %s", id)
	}
	if pidErr != nil {
		return 0, fmt.Errorf("resolve pid: %v\n%s", pidErr, truncate(string(pidStderr), 200))
	}
	pid, err := parseIOSPID(pidOut)
	if err != nil {
		return 0, fmt.Errorf("app not running: %s", bundleID)
	}
	return pid, nil
}

// LaunchKeepAwake brings the KeepAwake app to foreground via devicectl.
// The id may be the hardware UDID, CoreDevice UUID, device name, or any
// other identifier `devicectl --device` accepts.
func (a *IOSAdapter) LaunchKeepAwake(id string) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	cmd := exec.Command("xcrun", "devicectl", "device", "process", "launch",
		"--device", id,
		KeepAwakeBundleID,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("devicectl launch: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
