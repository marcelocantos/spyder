// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	goios_ios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/appservice"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"github.com/danielpaulus/go-ios/ios/instruments"
	"github.com/danielpaulus/go-ios/ios/zipconduit"
	"github.com/marcelocantos/spyder/internal/goios"
	"github.com/marcelocantos/spyder/internal/network"
	"github.com/marcelocantos/spyder/internal/pmd3bridge"
)

// ErrLocked is returned when an operation fails specifically because the
// target device is locked. Callers can errors.Is check this to fire a
// targeted notification and retry until the device is unlocked.
var ErrLocked = errors.New("device is locked")

// KeepAwakeBundleID is the bundle identifier of the ios/KeepAwake companion
// app. The app's only job is to set UIApplication.isIdleTimerDisabled=true
// while foregrounded, which is the sole iOS mechanism that reliably prevents
// display auto-lock (🎯T31). pmd3's PowerAssertionService looked like a
// replacement but turned out to be a no-op for display sleep; see T31's
// context for the investigation.
const KeepAwakeBundleID = "com.marcelocantos.spyder.KeepAwake"

// AppState* are the values returned by KeepAwakeState (and by the
// underlying bridge AppState endpoint). The bridge collapses iOS's
// fine-grained BackBoard taxonomy onto these three buckets — enough
// for autoawake to decide between converged / opt-out / launch.
const (
	AppStateRunning      = "running"
	AppStateBackgrounded = "backgrounded"
	AppStateTerminated   = "terminated"
)

// keepAwakeLaunchLockedPattern matches devicectl output indicating the
// device is locked / passcode-protected. The exact message varies across
// iOS / macOS versions; we keep the matcher generous.
var keepAwakeLaunchLockedPattern = regexp.MustCompile(
	`(?i)locked|passcode.*required|device must be unlocked|user must unlock`)

// keepAwakeLaunchTrustPattern matches devicectl output indicating the
// developer certificate has not been trusted on-device.
var keepAwakeLaunchTrustPattern = regexp.MustCompile(
	`(?i)untrusted.*developer|not.*explicitly trusted|requires.*trust|'Security'|invalid code signature`)

// keepAwakeLaunchNoProviderPattern matches the CoreDeviceError Code=1002
// "No provider was found" failure that devicectl emits when the bundle is
// installed but the host's provisioning profile doesn't match the on-device
// signature (e.g. free Personal Team profile expired after 7 days, or the
// host signed with a different team). Fix: uninstall + reinstall.
var keepAwakeLaunchNoProviderPattern = regexp.MustCompile(
	`(?i)no provider was found|CoreDeviceError.*[Cc]ode=?1002|error 1002`)

// keepAwakeAppRecord is the subset of the `xcrun devicectl device info
// apps` JSON entry that autoawake cares about. The query covers two
// concerns — "is it installed" and "what version is installed" — and
// both share one devicectl invocation via inspectKeepAwakeApp.
type keepAwakeAppRecord struct {
	installed bool
	// version is CFBundleShortVersionString as reported by devicectl
	// (the JSON field is named `version`). Empty when not installed.
	version string
}

// inspectKeepAwakeApp asks installation_proxy for the installed app
// list and pulls out the KeepAwake entry, if present. Single source of
// truth for KeepAwakeInstalled and KeepAwakeInstalledVersion so
// autoawake's per-tick cost stays at one round-trip.
func (a *IOSAdapter) inspectKeepAwakeApp(id string) (keepAwakeAppRecord, error) {
	if id == "" {
		return keepAwakeAppRecord{}, errors.New("device identifier is empty")
	}
	dev, err := a.goios.Session(id)
	if err != nil {
		return keepAwakeAppRecord{}, fmt.Errorf("inspect KeepAwake: %w", err)
	}
	conn, err := installationproxy.New(dev)
	if err != nil {
		a.goios.Invalidate(id)
		return keepAwakeAppRecord{}, fmt.Errorf("installation_proxy on %s: %w", id, err)
	}
	defer conn.Close()
	apps, err := conn.BrowseAllApps()
	if err != nil {
		return keepAwakeAppRecord{}, fmt.Errorf("browse apps on %s: %w", id, err)
	}
	for _, app := range apps {
		if app.CFBundleIdentifier() == KeepAwakeBundleID {
			return keepAwakeAppRecord{
				installed: true,
				version:   app.CFBundleShortVersionString(),
			}, nil
		}
	}
	return keepAwakeAppRecord{installed: false}, nil
}

// KeepAwakeInstalled reports whether the KeepAwake bundle is currently
// installed on the device. Returns (false, nil) on a successful query
// that doesn't list the app; (false, error) on a devicectl failure
// (caller can treat that as "unknown" and skip).
func (a *IOSAdapter) KeepAwakeInstalled(id string) (bool, error) {
	rec, err := a.inspectKeepAwakeApp(id)
	if err != nil {
		return false, err
	}
	return rec.installed, nil
}

// KeepAwakeInstalledVersion returns the CFBundleShortVersionString of
// the KeepAwake bundle currently installed on the device, or "" when
// it isn't installed. Used by autoawake's staleness check (🎯T47): the
// supervisor compares this value against device.ExpectedKeepAwakeVersion()
// (parsed from the bundled pbxproj's MARKETING_VERSION) and triggers an
// uninstall + rebuild + reinstall cycle on mismatch. Versions are
// compared as opaque strings — pre-release suffixes like "-rc1" or
// arbitrary version schemes are honoured verbatim.
func (a *IOSAdapter) KeepAwakeInstalledVersion(id string) (string, error) {
	rec, err := a.inspectKeepAwakeApp(id)
	if err != nil {
		return "", err
	}
	return rec.version, nil
}

// ForegroundApp returns the bundle id (or .app folder name) of the
// foregrounded third-party app on the device, or "" when SpringBoard
// (the home screen) is showing. Routes through the pmd3 bridge's
// /v1/foreground_app endpoint, which scans the same BackBoard
// applicationStateNotification: enumeration that AppState uses and
// returns the entry whose state_description is "Running".
//
// autoawake's convergence loop reads this signal first: a non-empty
// non-KeepAwake result means another app is keeping the screen on
// already and the supervisor stays passive (no relaunch of KA), so a
// spyder-deployed app under test, or anything the user task-switched
// to themselves, is never clobbered. KA is launched only when this
// returns "".
func (a *IOSAdapter) ForegroundApp(id string) (string, error) {
	if id == "" {
		return "", errors.New("device identifier is empty")
	}
	dev, err := a.goios.Session(id)
	if err != nil {
		return "", fmt.Errorf("foreground_app: %w", err)
	}
	recv, closeFn, err := instruments.ListenAppStateNotifications(dev)
	if err != nil {
		// Transport-level failure — drop the cached session so the
		// next call re-handshakes (covers tunnel restart / device
		// replug between calls).
		a.goios.Invalidate(id)
		return "", fmt.Errorf("foreground_app: subscribe on %s: %w", id, err)
	}
	defer closeFn()

	// Drain BackBoard's initial state-enumeration burst into a buffered
	// channel so the deadline below isn't wedged by recv()'s blocking
	// read. The 750ms drain matches pmd3's 0.5s window with a touch of
	// slack for slow tunnels — BackBoard delivers the typical burst
	// (~14-30 entries) in <100ms once the channel is open.
	type recvResult struct {
		data map[string]interface{}
		err  error
	}
	results := make(chan recvResult, 64)
	go func() {
		for {
			data, recvErr := recv()
			results <- recvResult{data: data, err: recvErr}
			if recvErr != nil {
				return
			}
		}
	}()

	deadline := time.After(750 * time.Millisecond)
	for {
		select {
		case <-deadline:
			return "", nil
		case r := <-results:
			if r.err != nil {
				return "", fmt.Errorf("foreground_app: recv on %s: %w", id, r.err)
			}
			stateDesc, _ := r.data["state_description"].(string)
			if stateDesc != "Running" {
				continue
			}
			if bundle, _ := r.data["bundleIdentifier"].(string); bundle != "" {
				return bundle, nil
			}
			if exec, _ := r.data["execName"].(string); exec != "" {
				return appNameFromExec(exec), nil
			}
		}
	}
}

// appNameFromExec extracts the .app folder basename from a BackBoard
// execName like "/private/var/.../KeepAwake.app/KeepAwake". Used as a
// fallback when BackBoard's notification entry omits bundleIdentifier
// (system apps and some older iOS versions).
func appNameFromExec(exec string) string {
	idx := strings.LastIndex(strings.ToLower(exec), ".app")
	if idx <= 0 {
		return ""
	}
	prefix := exec[:idx]
	if slash := strings.LastIndex(prefix, "/"); slash >= 0 {
		return prefix[slash+1:]
	}
	return prefix
}

// IsKeepAwakeForeground returns true when bundle (the value returned by
// ForegroundApp) identifies the KeepAwake companion. The bridge prefers
// BackBoard's bundleIdentifier but falls back to the .app folder name
// when BackBoard doesn't surface one, so this accepts either form.
func IsKeepAwakeForeground(bundle string) bool {
	return bundle == KeepAwakeBundleID || bundle == "KeepAwake"
}

// LaunchKeepAwake foregrounds the KeepAwake companion app via go-ios's
// instruments.ProcessControl (DTX). Assumes the app is already
// installed on the device.
//
// Error classification: returns ErrLocked / ErrTrustNotGranted /
// ErrKeepAwakeNotInstalled when the underlying error matches one of
// the recognised patterns; a wrapped generic error otherwise. The
// CoreDevice-specific ErrNoProviderFound is no longer reachable from
// this path — installation_proxy doesn't do CoreDevice's provider
// lookup, so the Code=1002 failure mode the iPhone has been hitting
// disappears with this migration.
func (a *IOSAdapter) LaunchKeepAwake(id string) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	started := time.Now()
	dev, err := a.goios.Session(id)
	if err != nil {
		return fmt.Errorf("launch KeepAwake: %w", err)
	}
	pc, err := instruments.NewProcessControl(dev)
	if err != nil {
		a.goios.Invalidate(id)
		return classifyKeepAwakeLaunchErr(id, err)
	}
	defer pc.Close()
	pid, err := pc.LaunchApp(KeepAwakeBundleID, map[string]any{})
	elapsedMs := time.Since(started).Milliseconds()
	if err != nil {
		return classifyKeepAwakeLaunchErr(id, err)
	}
	slog.Debug("KeepAwake launched",
		"device", id, "duration_ms", elapsedMs,
		"bundle", KeepAwakeBundleID, "pid", pid)
	_ = pid
	return nil
}

// ErrKeepAwakeNotInstalled is surfaced when LaunchKeepAwake fails
// because KeepAwake isn't installed on the device. Distinguished from
// other launch failures so autoawake can trigger the auto-install flow
// (🎯T32) instead of re-trying the launch.
var ErrKeepAwakeNotInstalled = errors.New("KeepAwake not installed on device")

// keepAwakeLaunchMissingPattern matches output indicating the app
// bundle isn't present on the device. Used for the legacy devicectl
// path AND the new go-ios ProcessControl path — go-ios surfaces
// BackBoard errors as text that overlaps with devicectl's wording.
var keepAwakeLaunchMissingPattern = regexp.MustCompile(
	`(?i)could not find.*app|app.*not installed|bundle.*not found|no such app|application.*does not exist|unknown application`)

// classifyKeepAwakeLaunchErr maps a raw error from go-ios's
// ProcessControl path to one of the typed sentinels autoawake's
// classifier expects (ErrLocked / ErrTrustNotGranted /
// ErrKeepAwakeNotInstalled). Falls through to a wrapped generic error
// when nothing matches. ErrNoProviderFound is no longer reachable
// from this code path — that's a CoreDevice-only failure mode.
func classifyKeepAwakeLaunchErr(udid string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case keepAwakeLaunchMissingPattern.MatchString(msg):
		slog.Debug("go-ios launch KeepAwake: not installed", "device", udid)
		return fmt.Errorf("launch KeepAwake on %s: %w", udid, ErrKeepAwakeNotInstalled)
	case keepAwakeLaunchLockedPattern.MatchString(msg):
		slog.Debug("go-ios launch KeepAwake: device locked", "device", udid)
		return fmt.Errorf("launch KeepAwake on %s: %w", udid, ErrLocked)
	case keepAwakeLaunchTrustPattern.MatchString(msg):
		slog.Debug("go-ios launch KeepAwake: trust not granted", "device", udid)
		return fmt.Errorf("launch KeepAwake on %s: %w", udid, ErrTrustNotGranted)
	}
	slog.Warn("go-ios launch KeepAwake failed",
		"device", udid, "error", truncate(msg, 240))
	return fmt.Errorf("launch KeepAwake on %s: %w", udid, err)
}

// stateTTL bounds how often we re-query a device. Tools called in quick
// succession (e.g. from an agent reasoning loop) share a snapshot so the
// device isn't hammered.
const stateTTL = 2 * time.Second

// devicectlTimeoutSeconds caps every per-device devicectl invocation so
// an unresponsive device can't wedge autoawake's convergence loop. We
// pass it through as devicectl's own --timeout flag (its internal
// machinery aborts the underlying CoreDevice operation cleanly) AND
// wrap with a CommandContext deadline a couple of seconds longer so
// the Go side reaps even if devicectl misbehaves on the timeout.
//
// Observed pre-fix: a freshly-rebooted device returned no
// `device info processes` response for >6 minutes, holding the
// per-device inFlight lock and silently parking convergence forever.
const (
	devicectlTimeoutSeconds = 30
	devicectlTimeout        = (devicectlTimeoutSeconds + 2) * time.Second
)

// errNoBridge is returned by IOSAdapter methods when no bridge was injected.
var errNoBridge = errors.New("iOS adapter requires the pmd3 bridge — ensure the bridge binary is installed")

// IOSAdapter talks to iOS devices. Operations are being migrated from the
// pmd3 Python bridge subprocess to direct in-process go-ios calls (🎯T56);
// during the migration both code paths coexist on the type. The `bridge`
// field is consulted only by methods that haven't been ported yet, and is
// removed at the end of T56.
type IOSAdapter struct {
	bridge *pmd3bridge.Client
	goios  *goios.Resolver
	mu     sync.Mutex
	cache  map[string]cachedState
}

type cachedState struct {
	state State
	at    time.Time
}

// NewIOSAdapter returns a new iOS adapter. bridge may be nil for tests or
// for environments where only the go-ios path is exercised; every method
// that still requires the bridge returns a clear error when it's nil.
func NewIOSAdapter(bridge *pmd3bridge.Client) *IOSAdapter {
	return &IOSAdapter{
		bridge: bridge,
		goios:  goios.New(goios.DefaultTunnelHost, goios.DefaultTunnelPort),
		cache:  map[string]cachedState{},
	}
}

// List returns iOS devices that are currently reachable. The set is the
// intersection of:
//
//   - The pmd3 bridge's /v1/list_devices (tunneld registry + USBMux).
//   - `xcrun devicectl list devices` filtered for tunnelState=connected
//     OR a USB connection (USBMux-only iOS-<17 devices count too).
//
// Devices the bridge knows about but devicectl reports as `unavailable`
// are dropped — they're paired but not currently usable, and including
// them produces useless install/launch attempts (autoawake's
// convergence loop would fire `xcrun devicectl ... launch` per tick
// for each ghost device, all returning "No provider was found"). When
// neither source is available the function returns an empty list
// rather than an error — matching the Android adapter's behaviour
// when adb is absent.
func (a *IOSAdapter) List() ([]Info, error) {
	connected, _ := devicectlConnectedIOSDevices()

	var devices []Info

	// Primary source: go-ios's usbmux enumeration, with per-device
	// lockdown enrichment for the human-friendly fields. Replaces the
	// previous pmd3-bridge /v1/list_devices call (🎯T56).
	if devList, err := goios_ios.ListDevices(); err == nil {
		for _, dev := range devList.DeviceList {
			udid := dev.Properties.SerialNumber
			if connected != nil && !connected[udid] {
				continue
			}
			info := Info{UUID: udid, Platform: "ios"}
			if values, gerr := goios_ios.GetValues(dev); gerr == nil {
				info.Name = values.Value.DeviceName
				info.Model = values.Value.ProductType
				if values.Value.ProductVersion != "" {
					info.OS = "iOS " + values.Value.ProductVersion
				}
			}
			devices = append(devices, info)
		}
	}

	if _, err := exec.LookPath("xcrun"); err == nil {
		tmp, err := os.MkdirTemp("", "spyder-devctl-*")
		if err == nil {
			defer os.RemoveAll(tmp)
			jsonPath := filepath.Join(tmp, "devices.json")
			_, _, _ = runDevicectl("list", "devices", "--quiet", "--json-output", jsonPath)
			if data, err := os.ReadFile(jsonPath); err == nil {
				if parsed, err := parseDevicectlList(data); err == nil {
					// parseDevicectlList already filters by tunnelState
					// internally for the merge step.
					filtered := parsed[:0]
					for _, d := range parsed {
						if connected == nil || connected[d.UUID] {
							filtered = append(filtered, d)
						}
					}
					devices = mergeIOSDevices(devices, filtered)
				}
			}
		}
	}

	return devices, nil
}

// devicectlConnectedIOSDevices returns the set of UDIDs that
// `xcrun devicectl list devices --json-output` reports as
// `tunnelState=connected` AND `transportType=wired` for the iOS
// platform. Used by IOSAdapter.List to filter out paired-but-unavailable
// devices that the tunneld registry would otherwise surface (e.g. a
// phone that was previously trusted but is currently powered off), and
// to exclude devices reachable only over the local network — autoawake's
// KeepAwake exits when batteryState=.unplugged, so a Wi-Fi-only device
// would just spin in a launch/exit loop.
//
// Returns (nil, error) when devicectl can't be queried — caller should
// treat this as "filter unavailable" and pass everything through.
func devicectlConnectedIOSDevices() (map[string]bool, error) {
	if _, err := exec.LookPath("xcrun"); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "spyder-devctl-conn-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	jsonPath := filepath.Join(tmp, "devices.json")
	if _, _, err := runDevicectl("list", "devices", "--quiet", "--json-output", jsonPath); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, err
	}
	return parseDevicectlConnectedIOSDevices(data)
}

// parseDevicectlConnectedIOSDevices applies the wired+connected filter
// to the devicectl JSON document. Extracted from the shell-out wrapper
// so it can be unit-tested without invoking xcrun.
func parseDevicectlConnectedIOSDevices(data []byte) (map[string]bool, error) {
	var doc struct {
		Result struct {
			Devices []struct {
				HardwareProperties struct {
					UDID     string `json:"udid"`
					Platform string `json:"platform"`
				} `json:"hardwareProperties"`
				ConnectionProperties struct {
					TunnelState   string `json:"tunnelState"`
					TransportType string `json:"transportType"`
				} `json:"connectionProperties"`
			} `json:"devices"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(doc.Result.Devices))
	for _, d := range doc.Result.Devices {
		if d.HardwareProperties.Platform != "iOS" {
			continue
		}
		if d.ConnectionProperties.TunnelState != "connected" {
			continue
		}
		if d.ConnectionProperties.TransportType != "wired" {
			continue
		}
		out[d.HardwareProperties.UDID] = true
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

	dev, err := a.goios.Session(id)
	if err != nil {
		return State{}, fmt.Errorf("state: %w", err)
	}

	var state State

	// Battery info comes from lockdown's com.apple.mobile.battery domain
	// — go-ios's GetBatteryDiagnostics wraps the per-key fetch and gives
	// us the capacity (already 0–100) and charging flag in one helper.
	batt, err := goios_ios.GetBatteryDiagnostics(dev)
	if err != nil {
		state.Notes = append(state.Notes, fmt.Sprintf("battery data unavailable: %v", err))
	} else if batt.HasBattery {
		level := int(batt.BatteryCurrentCapacity)
		state.BatteryLevel = &level
		charging := batt.BatteryIsCharging
		state.Charging = &charging
	}

	state.Notes = append(state.Notes,
		"thermal state unavailable on iOS 17.4+ (MobileGestalt deprecated)",
		"foreground app detection unavailable via state today",
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
	return runCaptureCtx(context.Background(), name, args...)
}

// runCaptureCtx is the context-aware variant of runCapture. Per-devicectl
// callers wrap with context.WithTimeout(devicectlTimeout) so a wedged
// device (e.g. one where Xcode's DDI personalization is in flight)
// can't park autoawake's per-device convergence forever.
func runCaptureCtx(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	started := time.Now()
	var outBuf, errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()

	elapsedMs := time.Since(started).Milliseconds()
	if err != nil {
		slog.Warn("exec failed",
			"cmd", name, "args", args,
			"duration_ms", elapsedMs,
			"error", err.Error(),
			"stderr_tail", truncate(errBuf.String(), 200))
	} else {
		slog.Debug("exec ok",
			"cmd", name, "args", args,
			"duration_ms", elapsedMs,
			"stdout_bytes", outBuf.Len(),
			"stderr_bytes", errBuf.Len())
	}
	return outBuf.Bytes(), errBuf.Bytes(), err
}

// runDevicectl invokes a devicectl subcommand with the standard
// devicectlTimeout cap, automatically prepending devicectl's own
// `--timeout <s>` flag so the binary aborts cleanly before our
// CommandContext deadline fires. Always use this for
// `xcrun devicectl ...` calls — never `runCapture` directly.
func runDevicectl(args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), devicectlTimeout)
	defer cancel()
	full := append([]string{"devicectl", "--timeout",
		fmt.Sprintf("%d", devicectlTimeoutSeconds)}, args...)
	return runCaptureCtx(ctx, "xcrun", full...)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Screenshot captures a PNG via go-ios's instruments.ScreenshotService
// (DTX). One round-trip to dtservicehub; raw PNG bytes returned.
func (a *IOSAdapter) Screenshot(id string) ([]byte, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	dev, err := a.goios.Session(id)
	if err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}
	svc, err := instruments.NewScreenshotService(dev)
	if err != nil {
		a.goios.Invalidate(id)
		return nil, fmt.Errorf("screenshot on %s: %w", id, err)
	}
	defer svc.Close()
	data, err := svc.TakeScreenshot()
	if err != nil {
		return nil, fmt.Errorf("screenshot on %s: %w", id, err)
	}
	return data, nil
}

// ListApps returns installed user apps via go-ios's installation_proxy.
// Sorted by bundle id for stable output.
func (a *IOSAdapter) ListApps(id string) ([]AppInfo, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	dev, err := a.goios.Session(id)
	if err != nil {
		return nil, fmt.Errorf("list_apps: %w", err)
	}
	conn, err := installationproxy.New(dev)
	if err != nil {
		a.goios.Invalidate(id)
		return nil, fmt.Errorf("installation_proxy on %s: %w", id, err)
	}
	defer conn.Close()
	raw, err := conn.BrowseUserApps()
	if err != nil {
		return nil, fmt.Errorf("list_apps on %s: %w", id, err)
	}
	apps := make([]AppInfo, 0, len(raw))
	for _, app := range raw {
		apps = append(apps, AppInfo{
			BundleID: app.CFBundleIdentifier(),
			Name:     app.CFBundleName(),
			Version:  app.CFBundleShortVersionString(),
		})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].BundleID < apps[j].BundleID })
	return apps, nil
}

// LaunchApp foregrounds an arbitrary app via go-ios's appservice
// (com.apple.coredevice.feature.launchapplication, the iOS-17+
// CoreDevice launch path). The pid the launch returns is currently
// discarded — callers that need it call AppPID after.
func (a *IOSAdapter) LaunchApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	dev, err := a.goios.Session(id)
	if err != nil {
		return fmt.Errorf("launch: %w", err)
	}
	conn, err := appservice.New(dev)
	if err != nil {
		a.goios.Invalidate(id)
		return fmt.Errorf("appservice on %s: %w", id, err)
	}
	defer conn.Close()
	if _, err := conn.LaunchApp(bundleID, nil, nil, nil, false); err != nil {
		// Map "app not installed"-shaped errors to the spyder convention.
		msg := err.Error()
		if strings.Contains(msg, "BundleIdentifier") || strings.Contains(strings.ToLower(msg), "not installed") {
			return fmt.Errorf("app not installed: %s", bundleID)
		}
		return fmt.Errorf("launch %s on %s: %w", bundleID, id, err)
	}
	return nil
}

// TerminateApp stops an app by bundle id. iOS doesn't expose a
// "kill by bundle id" RPC directly — go-ios's appservice only kills by
// pid — so we resolve the pid first.
func (a *IOSAdapter) TerminateApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	pid, err := a.AppPID(id, bundleID)
	if err != nil {
		// "app not running" is the no-op success: nothing to terminate.
		if strings.HasPrefix(err.Error(), "app not running") {
			return nil
		}
		return err
	}
	dev, err := a.goios.Session(id)
	if err != nil {
		return fmt.Errorf("terminate: %w", err)
	}
	conn, err := appservice.New(dev)
	if err != nil {
		a.goios.Invalidate(id)
		return fmt.Errorf("appservice on %s: %w", id, err)
	}
	defer conn.Close()
	if err := conn.KillProcess(pid); err != nil {
		return fmt.Errorf("terminate %s (pid %d) on %s: %w", bundleID, pid, id, err)
	}
	return nil
}

// AppPID returns the pid of a running app by bundle id. Implemented
// in two RPCs: installation_proxy resolves the bundle id to its
// installed .app folder, and appservice.ListProcesses scans live
// processes for one whose path contains that .app folder. Returns the
// "app not running" error sentinel that the deploy_app handler keys
// off when verify-pid runs immediately after a launch.
func (a *IOSAdapter) AppPID(id, bundleID string) (int, error) {
	if id == "" || bundleID == "" {
		return 0, errors.New("device id and bundle_id are required")
	}
	dev, err := a.goios.Session(id)
	if err != nil {
		return 0, fmt.Errorf("resolve pid: %w", err)
	}
	appBase, err := installedAppFolder(dev, bundleID)
	if err != nil {
		a.goios.Invalidate(id)
		return 0, err
	}
	if appBase == "" {
		return 0, fmt.Errorf("app not installed: %s", bundleID)
	}
	asConn, err := appservice.New(dev)
	if err != nil {
		a.goios.Invalidate(id)
		return 0, fmt.Errorf("appservice on %s: %w", id, err)
	}
	defer asConn.Close()
	procs, err := asConn.ListProcesses()
	if err != nil {
		return 0, fmt.Errorf("list processes on %s: %w", id, err)
	}
	needle := "/" + appBase + "/"
	for _, p := range procs {
		if strings.Contains(p.Path, needle) {
			return p.Pid, nil
		}
	}
	return 0, fmt.Errorf("app not running: %s", bundleID)
}

// installedAppFolder returns the basename of the .app folder
// (e.g. "MultiMaze.app") for the given bundle id, queried via
// installation_proxy. Returns "" when the bundle isn't installed.
func installedAppFolder(dev goios_ios.DeviceEntry, bundleID string) (string, error) {
	conn, err := installationproxy.New(dev)
	if err != nil {
		return "", fmt.Errorf("installation_proxy: %w", err)
	}
	defer conn.Close()
	apps, err := conn.BrowseAllApps()
	if err != nil {
		return "", fmt.Errorf("browse apps: %w", err)
	}
	for _, app := range apps {
		if app.CFBundleIdentifier() == bundleID {
			return filepath.Base(app.Path()), nil
		}
	}
	return "", nil
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

// InstallApp installs a .app or .ipa bundle via go-ios's zipconduit
// (com.apple.streaming_zip_conduit). zipconduit handles both folder
// (.app) and archive (.ipa) inputs natively, no zipping step needed.
func (a *IOSAdapter) InstallApp(id, path string) error {
	if id == "" || path == "" {
		return errors.New("device id and path are required")
	}
	dev, err := a.goios.Session(id)
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
	conn, err := zipconduit.New(dev)
	if err != nil {
		a.goios.Invalidate(id)
		return fmt.Errorf("zipconduit on %s: %w", id, err)
	}
	if err := conn.SendFile(path); err != nil {
		return fmt.Errorf("install %s on %s: %w", filepath.Base(path), id, err)
	}
	return nil
}

// UninstallApp removes an app by bundle identifier via go-ios's
// installation_proxy.
func (a *IOSAdapter) UninstallApp(id, bundleID string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	dev, err := a.goios.Session(id)
	if err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	conn, err := installationproxy.New(dev)
	if err != nil {
		a.goios.Invalidate(id)
		return fmt.Errorf("installation_proxy on %s: %w", id, err)
	}
	defer conn.Close()
	if err := conn.Uninstall(bundleID); err != nil {
		return fmt.Errorf("uninstall %s on %s: %w", bundleID, id, err)
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

// syslogEntryToLogLine converts the bridge's structured SyslogEntry into the
// adapter's LogLine shape. Falls back to time.Now() if the bridge timestamp
// fails to parse — better to surface the message than to drop it.
func syslogEntryToLogLine(e pmd3bridge.SyslogEntry) LogLine {
	ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
	if err != nil {
		ts, _ = time.Parse(time.RFC3339, e.Timestamp)
	}
	return LogLine{
		Timestamp: ts,
		Process:   e.Process,
		Level:     e.Level,
		Message:   e.Message,
	}
}

// LogRange returns syslog lines from the device between since and until,
// streamed through the pmd3 bridge. pmd3 does not expose a stable CLI for
// archived-log timestamp queries, so this drains the live stream and keeps
// entries inside the window. Callers should provide a reasonable upper
// bound; absent one we cap at 5 s to bound the call.
//
// Filter fields: Process (matched against image_name), Subsystem (matched
// against label.subsystem), Regex (applied client-side to Message).
func (a *IOSAdapter) LogRange(id string, filter LogFilter, since, until time.Time) ([]LogLine, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	if a.bridge == nil {
		return nil, errNoBridge
	}

	var regexFilter *regexp.Regexp
	if filter.Regex != "" {
		var err error
		regexFilter, err = regexp.Compile(filter.Regex)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
	}

	// Deadline: respect an explicit `until` up to a reasonable cap so a
	// caller asking for "the next two minutes of logs" gets the next two
	// minutes, not five seconds. When `until` is zero (caller didn't
	// specify) or unreasonably far in the future, fall back to a short
	// default so a bug doesn't leak a long-lived stream. (🎯T49.)
	const (
		defaultLogWait = 5 * time.Second
		maxLogWait     = 5 * time.Minute
	)
	deadline := until
	if deadline.IsZero() {
		deadline = time.Now().Add(defaultLogWait)
	} else if cap := time.Now().Add(maxLogWait); deadline.After(cap) {
		deadline = cap
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	bf := pmd3bridge.SyslogFilter{
		PID:         -1,
		ProcessName: filter.Process,
		Subsystem:   filter.Subsystem,
	}

	var lines []LogLine
	err := a.bridge.Syslog(ctx, id, bf, func(e pmd3bridge.SyslogEntry) bool {
		ll := syslogEntryToLogLine(e)
		if !since.IsZero() && ll.Timestamp.Before(since) {
			return true
		}
		if !until.IsZero() && ll.Timestamp.After(until) {
			return true
		}
		if regexFilter != nil && !regexFilter.MatchString(ll.Message) {
			return true
		}
		lines = append(lines, ll)
		return true
	})
	// Deadline-based cancellation is expected.
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return lines, err
	}
	return lines, nil
}

// LogStream pumps live syslog lines from the device through the pmd3 bridge
// into out until ctx is cancelled. Filter.Process matches image_name,
// Filter.Subsystem matches label.subsystem (both server-side); Filter.Regex
// is applied client-side to Message.
func (a *IOSAdapter) LogStream(ctx context.Context, id string, filter LogFilter, out chan<- LogLine) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	if a.bridge == nil {
		return errNoBridge
	}

	var regexFilter *regexp.Regexp
	if filter.Regex != "" {
		var err error
		regexFilter, err = regexp.Compile(filter.Regex)
		if err != nil {
			return fmt.Errorf("invalid regex: %w", err)
		}
	}

	bf := pmd3bridge.SyslogFilter{
		PID:         -1,
		ProcessName: filter.Process,
		Subsystem:   filter.Subsystem,
	}

	err := a.bridge.Syslog(ctx, id, bf, func(e pmd3bridge.SyslogEntry) bool {
		ll := syslogEntryToLogLine(e)
		if regexFilter != nil && !regexFilter.MatchString(ll.Message) {
			return true
		}
		select {
		case out <- ll:
			return true
		case <-ctx.Done():
			return false
		}
	})
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
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
