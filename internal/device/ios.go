// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marcelocantos/spyder/internal/network"
	"github.com/marcelocantos/spyder/internal/pmd3bridge"
)

// ErrLocked is returned when an operation fails specifically because the
// target device is locked. Callers can errors.Is check this to fire a
// targeted notification and retry until the device is unlocked.
var ErrLocked = errors.New("device is locked")

// stateTTL bounds how often we re-query a device. Tools called in quick
// succession (e.g. from an agent reasoning loop) share a snapshot so the
// device isn't hammered.
const stateTTL = 2 * time.Second

// errNoBridge is returned by IOSAdapter methods when no bridge was injected.
var errNoBridge = errors.New("iOS adapter requires the pmd3 bridge — ensure the bridge binary is installed")

// IOSAdapter talks to iOS devices via the pmd3 bridge (for most operations)
// and xcrun devicectl (for install/uninstall and device inventory enrichment).
type IOSAdapter struct {
	bridge *pmd3bridge.Client
	mu     sync.Mutex
	cache  map[string]cachedState
}

type cachedState struct {
	state State
	at    time.Time
}

// NewIOSAdapter returns a new iOS adapter. bridge may be nil when the bridge
// binary is unavailable; every method that requires the bridge returns a clear
// error in that case rather than panicking.
func NewIOSAdapter(bridge *pmd3bridge.Client) *IOSAdapter {
	return &IOSAdapter{bridge: bridge, cache: map[string]cachedState{}}
}

// List returns connected iOS devices by fusing two sources:
//
//   - The pmd3 bridge's /v1/list_devices for USB-connected devices (keyed by
//     hardware UDID; gives device name + product type + iOS version).
//   - `xcrun devicectl list devices` for paired CoreDevice devices
//     (gives marketingName, CoreDevice UUID, and richer metadata; may
//     include devices that aren't currently USB-connected).
//
// Devices present in both sources are fused into a single entry keyed
// by hardware UDID; devices present in only one source are listed
// with whatever metadata is available. If both sources fail, the
// function returns an empty list rather than an error — matching the
// Android adapter's behaviour when adb is absent.
func (a *IOSAdapter) List() ([]Info, error) {
	var devices []Info

	if a.bridge != nil {
		if bridgeDevices, err := a.bridge.ListDevices(context.Background()); err == nil {
			for _, d := range bridgeDevices {
				info := Info{
					UUID:     d.UDID,
					Name:     d.Name,
					Platform: "ios",
					Model:    d.ProductType,
				}
				if d.OSVersion != "" {
					info.OS = "iOS " + d.OSVersion
				}
				devices = append(devices, info)
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

// mergeIOSDevices overlays devicectl-sourced entries onto a base list,
// keyed by hardware UDID. devicectl's marketingName and name typically beat
// the bridge's ProductType/Name for human-readability, so they win on conflict.
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

// State reports iOS device state via the bridge for battery/charging data.
// Thermal state and foreground app detection are surfaced as notes.
// Results are cached per-device for stateTTL to avoid hammering the device.
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

	if a.bridge == nil {
		return State{}, errNoBridge
	}

	var state State

	// Per-endpoint timeout is owned by the bridge client (🎯T26.2).
	batt, err := a.bridge.Battery(context.Background(), id)
	if err != nil {
		if pmd3bridge.IsDeviceNotPaired(err) {
			return State{}, fmt.Errorf("device not paired: %s", id)
		}
		state.Notes = append(state.Notes, fmt.Sprintf("battery data unavailable: %v", err))
	} else {
		if batt.Level != nil {
			level := int(*batt.Level * 100)
			state.BatteryLevel = &level
		}
		state.Charging = batt.Charging
	}

	state.Notes = append(state.Notes,
		"thermal state unavailable on iOS 17.4+ (MobileGestalt deprecated)",
		"foreground app detection unavailable via bridge today",
	)

	a.mu.Lock()
	a.cache[id] = cachedState{state: state, at: time.Now()}
	a.mu.Unlock()

	return state, nil
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

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Screenshot captures a PNG via the bridge.
func (a *IOSAdapter) Screenshot(id string) ([]byte, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	if a.bridge == nil {
		return nil, errNoBridge
	}
	ctx := context.Background() // per-endpoint timeouts are owned by the bridge client (🎯T26.2)
	data, err := a.bridge.Screenshot(ctx, id)
	if err != nil {
		if pmd3bridge.IsDeviceNotPaired(err) {
			return nil, fmt.Errorf("device not connected: %s", id)
		}
		return nil, fmt.Errorf("screenshot on %s: %v", id, err)
	}
	return data, nil
}

// ListApps returns installed user apps via the bridge.
func (a *IOSAdapter) ListApps(id string) ([]AppInfo, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	if a.bridge == nil {
		return nil, errNoBridge
	}
	ctx := context.Background() // per-endpoint timeouts are owned by the bridge client (🎯T26.2)
	bridgeApps, err := a.bridge.ListApps(ctx, id)
	if err != nil {
		if pmd3bridge.IsDeviceNotPaired(err) {
			return nil, fmt.Errorf("device not connected: %s", id)
		}
		return nil, fmt.Errorf("list_apps on %s: %v", id, err)
	}
	apps := make([]AppInfo, 0, len(bridgeApps))
	for _, ba := range bridgeApps {
		ai := AppInfo{BundleID: ba.BundleID}
		if ba.Name != nil {
			ai.Name = *ba.Name
		}
		if ba.Version != nil {
			ai.Version = *ba.Version
		}
		apps = append(apps, ai)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].BundleID < apps[j].BundleID })
	return apps, nil
}

// LaunchApp foregrounds an arbitrary app via the bridge.
func (a *IOSAdapter) LaunchApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	if a.bridge == nil {
		return errNoBridge
	}
	ctx := context.Background() // per-endpoint timeouts are owned by the bridge client (🎯T26.2)
	_, err := a.bridge.LaunchApp(ctx, id, bundleID)
	if err != nil {
		if pmd3bridge.IsDeviceNotPaired(err) {
			return fmt.Errorf("device not connected: %s", id)
		}
		if pmd3bridge.IsBundleNotInstalled(err) {
			return fmt.Errorf("app not installed: %s", bundleID)
		}
		return fmt.Errorf("launch %s on %s: %v", bundleID, id, err)
	}
	return nil
}

// TerminateApp stops an app via the bridge.
func (a *IOSAdapter) TerminateApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	if a.bridge == nil {
		return errNoBridge
	}
	ctx := context.Background() // per-endpoint timeouts are owned by the bridge client (🎯T26.2)
	err := a.bridge.KillApp(ctx, id, bundleID)
	if err != nil {
		if pmd3bridge.IsDeviceNotPaired(err) {
			return fmt.Errorf("device not connected: %s", id)
		}
		if pmd3bridge.IsBundleNotInstalled(err) {
			return fmt.Errorf("app not installed: %s", bundleID)
		}
		return fmt.Errorf("terminate %s on %s: %v", bundleID, id, err)
	}
	return nil
}

// AppPID returns the process id of a running app by bundle id via the bridge.
// Returns 0 and an "app not running" error if the app is not running.
func (a *IOSAdapter) AppPID(id, bundleID string) (int, error) {
	if id == "" || bundleID == "" {
		return 0, errors.New("device id and bundle_id are required")
	}
	if a.bridge == nil {
		return 0, errNoBridge
	}
	ctx := context.Background() // per-endpoint timeouts are owned by the bridge client (🎯T26.2)
	pidPtr, err := a.bridge.PIDForBundle(ctx, id, bundleID)
	if err != nil {
		if pmd3bridge.IsDeviceNotPaired(err) {
			return 0, fmt.Errorf("device not connected: %s", id)
		}
		return 0, fmt.Errorf("resolve pid for %s on %s: %v", bundleID, id, err)
	}
	if pidPtr == nil {
		return 0, fmt.Errorf("app not running: %s", bundleID)
	}
	return *pidPtr, nil
}

// Crashes fetches crash reports from the device via the bridge.
// Reports are returned newest-first.
func (a *IOSAdapter) Crashes(id string, since time.Time, process string) ([]CrashReport, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	if a.bridge == nil {
		return nil, errNoBridge
	}
	// TODO(🎯T26.3): streaming will replace this aggregate pattern. For now
	// per-endpoint timeouts are owned by the bridge client (🎯T26.2).
	ctx := context.Background()

	bridgeReports, err := a.bridge.CrashReportsList(ctx, id, since, process)
	if err != nil {
		if pmd3bridge.IsDeviceNotPaired(err) {
			return nil, fmt.Errorf("device not connected: %s", id)
		}
		return nil, fmt.Errorf("crash_reports_list on %s: %v", id, err)
	}

	reports := make([]CrashReport, 0, len(bridgeReports))
	for _, br := range bridgeReports {
		ts, _ := time.Parse(time.RFC3339, br.Timestamp)
		// Pull the raw content for each report.
		raw, pullErr := a.bridge.CrashReportsPull(ctx, id, br.Name)
		if pullErr != nil {
			// Include the report with metadata but no raw content.
			reports = append(reports, CrashReport{
				Process:   br.Process,
				Timestamp: ts,
			})
			continue
		}
		cr := CrashReport{
			Process:   br.Process,
			Timestamp: ts,
			Raw:       raw,
		}
		// Parse the first-line JSON header for structured fields.
		firstLine := raw
		if i := strings.IndexByte(raw, '\n'); i >= 0 {
			firstLine = raw[:i]
		}
		var hdr ipsHeader
		if err := json.Unmarshal([]byte(strings.TrimSpace(firstLine)), &hdr); err == nil {
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
		reports = append(reports, cr)
	}

	// Sort newest-first.
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Timestamp.After(reports[j].Timestamp)
	})
	return reports, nil
}

// ipsHeader is the subset of .ips first-line JSON we care about.
type ipsHeader struct {
	ProcName      string `json:"procName"`
	ProcessName   string `json:"process_name"`
	CapturedTime  string `json:"captured_time"`
	ExceptionType string `json:"exception_type"`
	ExceptionInfo string `json:"exception_info"`
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

// iosSimOrientations maps canonical orientation names to the xcrun simctl
// rotate argument. simctl uses camelCase orientation identifiers.
var iosSimOrientations = map[string]string{
	"portrait":             "portrait",
	"landscape-left":       "landscapeLeft",
	"landscape-right":      "landscapeRight",
	"portrait-upside-down": "portraitUpsideDown",
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

// ApplyNetwork is not yet implemented for iOS.
//
// # iOS simulator
//
// Apple does not expose a first-class CLI for Link Conditioner. The
// Network Link Conditioner preference pane (com.apple.Network-Link-Conditioner)
// and its backing daemon (nslookupd / nlcd) are host-level — they affect
// all network traffic on the Mac, not just a single simulator instance.
// Some third-party tools (e.g. `nlct`) wrap the pane, but they are not
// reliably available and the API is private. Contributions that implement
// per-simulator shaping (e.g. via `simctl` future flags or the private
// CoreSimulator framework) are welcome.
//
// # Physical iOS devices
//
// Physical iOS devices do not expose a programmable network-shaping
// interface to the host. Apple's Developer Settings → Network Link
// Conditioner feature can be toggled on-device, but there is no
// host-side CLI or protocol to drive it remotely.
func (a *IOSAdapter) ApplyNetwork(_ string, _ network.NetworkProfile) error {
	return errors.New(
		"network condition shaping is not supported on iOS: " +
			"iOS simulator — no public CLI for Link Conditioner (contributions welcome); " +
			"physical iOS devices — no remote interface to Developer Settings",
	)
}

// ClearNetwork is not yet implemented for iOS (same limitations as ApplyNetwork).
func (a *IOSAdapter) ClearNetwork(_ string) error {
	return errors.New(
		"network condition clearing is not supported on iOS: " +
			"iOS simulator — no public CLI for Link Conditioner (contributions welcome); " +
			"physical iOS devices — no remote interface to Developer Settings",
	)
}

// iosSyslogLineRE matches a line produced by `pymobiledevice3 syslog live`
// in its default text format:
//
//	<Timestamp> <Device> <Process>(<subsystem>) [<level>] <Message>
//
// Example:
//
//	Mar 15 14:23:01.123 Pippa MyApp(com.example.app)[1234] <Error>: crash happened
//
// The regex is intentionally permissive to handle variations (missing
// subsystem, different bracket styles, etc.).
var iosSyslogLineRE = regexp.MustCompile(
	`^(\w{3}\s+\d+\s+[\d:.]+)\s+\S+\s+(\S+?)\[` + // timestamp + device + process[pid
		`\d+\]\s+<(\w+)>:\s+(.*)$`, // level: message
)

// iosSyslogTimestampLayouts are tried in order when parsing timestamps from
// `pymobiledevice3 syslog live` output. The tool emits dates without a year,
// so we parse them relative to the current year.
var iosSyslogTimestampLayouts = []string{
	"Jan  2 15:04:05.000",
	"Jan _2 15:04:05.000",
	"Jan 2 15:04:05.000",
	"Jan  2 15:04:05",
	"Jan _2 15:04:05",
	"Jan 2 15:04:05",
}

// ParseIOSSyslogLine parses a single line from `pymobiledevice3 syslog live`
// output. Exported for testing; internal callers use parseIOSSyslogLine.
func ParseIOSSyslogLine(line string) (LogLine, bool) {
	m := iosSyslogLineRE.FindStringSubmatch(line)
	if m == nil {
		return LogLine{}, false
	}
	ts := parseIOSSyslogTimestamp(m[1])
	return LogLine{
		Timestamp: ts,
		Process:   m[2],
		Level:     m[3],
		Message:   m[4],
	}, true
}

// parseIOSSyslogTimestamp parses a syslog timestamp string, appending the
// current year since pymobiledevice3 does not include it.
func parseIOSSyslogTimestamp(s string) time.Time {
	year := time.Now().Year()
	s = strings.TrimSpace(s)
	for _, layout := range iosSyslogTimestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.AddDate(year, 0, 0)
		}
	}
	return time.Time{}
}

// LogRange returns log lines from `pymobiledevice3 syslog live` between
// since and until. Because pymobiledevice3 does not support archived-log
// timestamp queries in a stable CLI way, we run the live stream briefly,
// collecting lines within the window. For bounded historic queries, callers
// should provide a reasonable since/until window; this implementation
// falls back to returning the recent live output.
//
// Filter fields: Process (--procname), Subsystem (--subsystem), Regex
// (applied client-side to Message).
func (a *IOSAdapter) LogRange(id string, filter LogFilter, since, until time.Time) ([]LogLine, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	if _, err := exec.LookPath("pymobiledevice3"); err != nil {
		return nil, fmt.Errorf("pymobiledevice3 not found: %w", err)
	}

	args := []string{"syslog", "live", "--udid", id}
	if filter.Process != "" {
		args = append(args, "--procname", filter.Process)
	}
	if filter.Subsystem != "" {
		args = append(args, "--subsystem", filter.Subsystem)
	}

	// For range queries we need to drain until `until` passes. Cap at 30s
	// to avoid hanging forever when until is zero (no upper bound).
	deadline := until
	if deadline.IsZero() || deadline.After(time.Now().Add(30*time.Second)) {
		deadline = time.Now().Add(5 * time.Second)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	cmd := exec.CommandContext(ctx, "pymobiledevice3", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start syslog live: %w", err)
	}

	var regexFilter *regexp.Regexp
	if filter.Regex != "" {
		regexFilter, err = regexp.Compile(filter.Regex)
		if err != nil {
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
	}

	var lines []LogLine
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		ll, ok := ParseIOSSyslogLine(line)
		if !ok {
			continue
		}
		if !since.IsZero() && ll.Timestamp.Before(since) {
			continue
		}
		if !until.IsZero() && ll.Timestamp.After(until) {
			continue
		}
		if regexFilter != nil && !regexFilter.MatchString(ll.Message) {
			continue
		}
		lines = append(lines, ll)
	}

	// Deadline-based cancellation is expected; suppress context errors.
	_ = cmd.Wait()
	return lines, nil
}

// LogStream pumps live syslog lines from the device into out until ctx is
// cancelled. Uses `pymobiledevice3 syslog live`. Filter fields are applied
// server-side (pymobiledevice3 flags) and client-side (Regex).
func (a *IOSAdapter) LogStream(ctx context.Context, id string, filter LogFilter, out chan<- LogLine) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	if _, err := exec.LookPath("pymobiledevice3"); err != nil {
		return fmt.Errorf("pymobiledevice3 not found: %w", err)
	}

	args := []string{"syslog", "live", "--udid", id}
	if filter.Process != "" {
		args = append(args, "--procname", filter.Process)
	}
	if filter.Subsystem != "" {
		args = append(args, "--subsystem", filter.Subsystem)
	}

	var regexFilter *regexp.Regexp
	if filter.Regex != "" {
		var err error
		regexFilter, err = regexp.Compile(filter.Regex)
		if err != nil {
			return fmt.Errorf("invalid regex: %w", err)
		}
	}

	cmd := exec.CommandContext(ctx, "pymobiledevice3", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start syslog live: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		ll, ok := ParseIOSSyslogLine(scanner.Text())
		if !ok {
			continue
		}
		if regexFilter != nil && !regexFilter.MatchString(ll.Message) {
			continue
		}
		select {
		case out <- ll:
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil
		}
	}
	_ = cmd.Wait()
	return nil
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
