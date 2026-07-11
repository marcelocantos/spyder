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
	"github.com/danielpaulus/go-ios/ios/crashreport"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"github.com/danielpaulus/go-ios/ios/instruments"
	"github.com/danielpaulus/go-ios/ios/ostrace"
	"github.com/danielpaulus/go-ios/ios/zipconduit"
	"github.com/marcelocantos/spyder/internal/goios"
	"github.com/marcelocantos/spyder/internal/network"
	"github.com/marcelocantos/spyder/internal/oslog"
)

// stateTTL bounds how often we re-query a device. Tools called in quick
// succession (e.g. from an agent reasoning loop) share a snapshot so the
// device isn't hammered.
const stateTTL = 2 * time.Second

// devicectlTimeoutSeconds caps every per-device devicectl invocation so
// an unresponsive device can't wedge a caller. We pass it through as
// devicectl's own --timeout flag (its internal machinery aborts the
// underlying CoreDevice operation cleanly) AND wrap with a
// CommandContext deadline a couple of seconds longer so the Go side
// reaps even if devicectl misbehaves on the timeout.
const (
	devicectlTimeoutSeconds = 30
	devicectlTimeout        = (devicectlTimeoutSeconds + 2) * time.Second
)

// IOSAdapter talks to iOS devices via in-process go-ios calls.
// `xcrun devicectl` is still used for install / uninstall (where
// devicectl's signing and provisioning handling is hard to replace);
// everything else runs in-process.
type IOSAdapter struct {
	goios *goios.Resolver
	// ipPool holds one cached installation_proxy connection per UDID.
	// installation_proxy is the highest-churn service spyder uses
	// (every ListApps + ResolveExecutable + bundle-id resolution
	// opens one), and the per-RPC open/close churn appears to be a
	// trigger for the usbmuxd-wedge symptom (🎯T67). Reusing one
	// connection per device collapses N opens to 1. Pool serialises
	// operations on the cached connection — installation_proxy is
	// not safe for concurrent use anyway.
	ipPool *goios.ServicePool[*installationproxy.Connection]
	// asPool caches one appservice connection per UDID. Each
	// LaunchApp / TerminateApp / AppPID currently opens a fresh
	// appservice DTX channel; pooling collapses launch-then-verify
	// cycles to a single open.
	asPool *goios.ServicePool[*appservice.Connection]
	// ssPool caches one instruments.ScreenshotService per UDID. The
	// DTX handshake costs ~150ms on iOS-17+ tunnels; pooling makes
	// repeated screenshots (visual diff loops, recording fallbacks)
	// effectively free after the first.
	ssPool *goios.ServicePool[*instruments.ScreenshotService]

	mu    sync.Mutex
	cache map[string]cachedState
}

type cachedState struct {
	state State
	at    time.Time
}

// Resolver exposes the adapter's goios.Resolver for tunnel recovery
// (🎯T89) and daemon-level usbmux watching.
func (a *IOSAdapter) Resolver() *goios.Resolver {
	return a.goios
}

// InvalidateDevice drops the session cache, service pools, and state
// snapshot for udid. Implements goios.ServicePoolInvalidator for the
// usbmux watcher (🎯T89.2). Routes through Resolver.Invalidate so the
// onInvalidate hook (pools) fires once.
func (a *IOSAdapter) InvalidateDevice(udid string) {
	if a == nil || udid == "" {
		return
	}
	a.goios.Invalidate(udid)
}

// NewIOSAdapter returns a new iOS adapter wired to a default-tunnel
// goios.Resolver (127.0.0.1:60105 — the `ios tunnel start --userspace`
// registry endpoint).
func NewIOSAdapter() *IOSAdapter {
	resolver := goios.New(goios.DefaultTunnelHost, goios.DefaultTunnelPort)
	a := &IOSAdapter{
		goios: resolver,
		ipPool: goios.NewServicePool(
			resolver,
			func(dev goios_ios.DeviceEntry) (*installationproxy.Connection, error) {
				return installationproxy.New(dev)
			},
			func(c *installationproxy.Connection) error {
				c.Close()
				return nil
			},
			60*time.Second,
		),
		asPool: goios.NewServicePool(
			resolver,
			func(dev goios_ios.DeviceEntry) (*appservice.Connection, error) {
				return appservice.New(dev)
			},
			func(c *appservice.Connection) error {
				return c.Close()
			},
			60*time.Second,
		),
		ssPool: goios.NewServicePool(
			resolver,
			func(dev goios_ios.DeviceEntry) (*instruments.ScreenshotService, error) {
				return instruments.NewScreenshotService(dev)
			},
			func(c *instruments.ScreenshotService) error {
				c.Close()
				return nil
			},
			60*time.Second,
		),
		cache: map[string]cachedState{},
	}
	// When the resolver invalidates a UDID (T89 re-establish / detach),
	// drop pooled DTX connections so the next call re-handshakes.
	resolver.SetOnInvalidate(func(udid string) {
		if a.ipPool != nil {
			a.ipPool.Invalidate(udid)
		}
		if a.asPool != nil {
			a.asPool.Invalidate(udid)
		}
		if a.ssPool != nil {
			a.ssPool.Invalidate(udid)
		}
		a.mu.Lock()
		delete(a.cache, udid)
		a.mu.Unlock()
	})
	return a
}

// List returns iOS devices that are currently reachable. The set is the
// union of:
//
//   - go-ios's USBMux enumeration (with per-device lockdown enrichment
//     for the human-friendly fields). iOS ≤16 entries need a
//     successful lockdown probe to be included; iOS-17+ entries are
//     always included but marked TunnelPending when devicectl hasn't
//     reported `tunnelState=connected` yet (🎯T84).
//   - `xcrun devicectl list devices` for any USBMux-invisible devices
//     in the `connected` state (rare — usually devicectl is a subset
//     of usbmux).
//
// When neither source is available the function returns an empty list
// rather than an error — matching the Android adapter's behaviour when
// adb is absent.
func (a *IOSAdapter) List() ([]Info, error) {
	connected, _ := devicectlConnectedIOSDevices()

	var devices []Info

	// Primary source: go-ios's usbmux enumeration, with per-device
	// lockdown enrichment for the human-friendly fields.
	//
	// For iOS-17+, devicectl's connected set is advisory (it gates
	// the TunnelPending flag), not a filter — surfacing tunnel-
	// pending devices instead of dropping them means USB-connected
	// devices on a settling RSD tunnel still show up in `spyder
	// devices` and the user can tell something's there even when
	// install/launch would fail with a "tunnel not ready" error.
	// CoreDevice doesn't talk to iOS ≤16 devices at all, so those
	// gate purely on a successful lockdown probe (the legacy oracle).
	if devList, err := goios_ios.ListDevices(); err == nil {
		for _, dev := range devList.DeviceList {
			udid := dev.Properties.SerialNumber
			info := Info{UUID: udid, Platform: "ios"}
			values, gerr := goios_ios.GetValues(dev)
			if gerr == nil {
				info.Name = values.Value.DeviceName
				info.Model = values.Value.ProductType
				if values.Value.ProductVersion != "" {
					info.OS = "iOS " + values.Value.ProductVersion
				}
			}
			major := 0
			if gerr == nil {
				major = goios.ParseIOSMajor(values.Value.ProductVersion)
			}
			include, pending := classifyUSBMuxEntry(major, gerr == nil, udid, connected)
			if !include {
				continue
			}
			info.TunnelPending = pending
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
// to exclude devices reachable only over the local network.
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

// classifyUSBMuxEntry decides whether a usbmux-visible iOS device
// should be included in List() output, and if so whether its tunnel
// is still pending the RSD handshake's settling (🎯T84).
//
// Rules:
//
//   - iOS ≤16 (no RSD): include iff lockdown enrichment succeeded
//     (that's the oracle for "reachable" on the legacy path). Never
//     marked pending — these devices have no tunnel.
//   - iOS-17+ or unknown-major: always include. Marked TunnelPending
//     when devicectl reports a `connected` set AND this UDID isn't
//     in it — the user can see the device exists while the tunnel
//     is settling rather than watching it silently disappear.
//   - When devicectl isn't queryable (no `connected` map), nothing
//     can be classified as pending — surface the device cleanly.
func classifyUSBMuxEntry(major int, lockdownOK bool, udid string, connected map[string]bool) (include, pending bool) {
	if major != 0 && major < 17 {
		return lockdownOK, false
	}
	if connected == nil {
		return true, false
	}
	return true, !connected[udid]
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
// human-readable diagnostics from stderr (some CLIs log errors to
// stderr while exiting 0).
func runCapture(name string, args ...string) (stdout, stderr []byte, err error) {
	return runCaptureCtx(context.Background(), name, args...)
}

// runCaptureCtx is the context-aware variant of runCapture. Per-devicectl
// callers wrap with context.WithTimeout(devicectlTimeout) so a wedged
// device (e.g. one where Xcode's DDI personalization is in flight)
// can't park a caller forever.
func runCaptureCtx(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	started := time.Now()
	var outBuf, errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()

	elapsedMs := time.Since(started).Milliseconds()
	if err != nil {
		slog.Error("exec failed",
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
	svc, release, err := a.ssPool.Acquire(id)
	if err != nil {
		a.goios.Invalidate(id)
		return nil, fmt.Errorf("screenshot on %s: %w", id, err)
	}
	defer release()
	data, err := svc.TakeScreenshot()
	if err != nil {
		a.ssPool.Invalidate(id)
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
	conn, release, err := a.ipPool.Acquire(id)
	if err != nil {
		a.goios.Invalidate(id)
		return nil, fmt.Errorf("installation_proxy on %s: %w", id, err)
	}
	defer release()
	raw, err := conn.BrowseUserApps()
	if err != nil {
		a.ipPool.Invalidate(id)
		return nil, fmt.Errorf("list_apps on %s: %w", id, err)
	}
	apps := make([]AppInfo, 0, len(raw))
	for _, app := range raw {
		apps = append(apps, AppInfo{
			BundleID:   app.CFBundleIdentifier(),
			Name:       app.CFBundleName(),
			Executable: app.CFBundleExecutable(),
			Version:    app.CFBundleShortVersionString(),
		})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].BundleID < apps[j].BundleID })
	return apps, nil
}

// ResolveExecutable maps an iOS bundle id to its CFBundleExecutable —
// the string the device's syslog stream uses to identify the app in
// the `process` column. Returns ("", false, nil) when the bundle isn't
// installed.
func (a *IOSAdapter) ResolveExecutable(id, bundleID string) (string, bool, error) {
	if id == "" || bundleID == "" {
		return "", false, errors.New("device id and bundle_id are required")
	}
	conn, release, err := a.ipPool.Acquire(id)
	if err != nil {
		a.goios.Invalidate(id)
		return "", false, fmt.Errorf("installation_proxy on %s: %w", id, err)
	}
	defer release()
	apps, err := conn.BrowseAllApps()
	if err != nil {
		a.ipPool.Invalidate(id)
		return "", false, fmt.Errorf("browse apps on %s: %w", id, err)
	}
	for _, app := range apps {
		if app.CFBundleIdentifier() == bundleID {
			return app.CFBundleExecutable(), true, nil
		}
	}
	return "", false, nil
}

// LaunchApp foregrounds an arbitrary app. Path selection is automatic
// per device by iOS major version: iOS-17+ devices go through go-ios's
// appservice (com.apple.coredevice.feature.launchapplication, the
// CoreDevice/RemoteXPC path that requires `ios tunnel start`); iOS ≤16
// devices go through go-ios's instruments.ProcessControl (DTX-over-
// lockdown, no tunnel required). The pid the launch returns is
// currently discarded — callers that need it call AppPID after.
func (a *IOSAdapter) LaunchApp(id, bundleID string, env map[string]string) error {
	if id == "" || bundleID == "" {
		return errors.New("device id and bundle_id are required")
	}
	_, major, sErr := a.goios.SessionWithVersion(id)
	if sErr != nil {
		return fmt.Errorf("launch %s on %s: %w", bundleID, id, sErr)
	}
	if major != 0 && major < 17 {
		return a.launchAppLockdown(id, bundleID, env)
	}
	return a.launchAppAppservice(id, bundleID, env)
}

// launchAppAppservice is the iOS-17+ path (CoreDevice/RemoteXPC).
func (a *IOSAdapter) launchAppAppservice(id, bundleID string, env map[string]string) error {
	conn, release, err := a.asPool.Acquire(id)
	if err != nil {
		a.goios.Invalidate(id)
		return fmt.Errorf("appservice on %s: %w", id, err)
	}
	defer release()
	var envArg map[string]interface{}
	if len(env) > 0 {
		envArg = make(map[string]interface{}, len(env))
		for k, v := range env {
			envArg[k] = v
		}
	}
	if _, err := conn.LaunchApp(bundleID, nil, envArg, nil, false); err != nil {
		// Map "app not installed"-shaped errors to the spyder convention.
		msg := err.Error()
		if strings.Contains(msg, "BundleIdentifier") || strings.Contains(strings.ToLower(msg), "not installed") {
			return fmt.Errorf("app not installed: %s", bundleID)
		}
		a.asPool.Invalidate(id)
		return fmt.Errorf("launch %s on %s: %w", bundleID, id, err)
	}
	return nil
}

// launchAppLockdown is the iOS ≤16 path (DTX-over-lockdown via
// instruments.ProcessControl). Requires a mounted Developer Disk Image
// — if the device doesn't have one (Xcode hasn't opened it recently),
// the underlying ProcessControl handshake fails with a clear error that
// we wrap with a hint.
func (a *IOSAdapter) launchAppLockdown(id, bundleID string, env map[string]string) error {
	dev, _, err := a.goios.SessionWithVersion(id)
	if err != nil {
		return fmt.Errorf("launch %s on %s: %w", bundleID, id, err)
	}
	pc, err := instruments.NewProcessControl(dev)
	if err != nil {
		a.goios.Invalidate(id)
		return wrapMissingDDI(fmt.Errorf("launch %s on %s: instruments.ProcessControl: %w", bundleID, id, err))
	}
	defer pc.Close()
	var envArg map[string]any
	if len(env) > 0 {
		envArg = make(map[string]any, len(env))
		for k, v := range env {
			envArg[k] = v
		}
	}
	if _, err := pc.LaunchAppWithArgs(bundleID, nil, envArg, nil); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "BundleIdentifier") || strings.Contains(strings.ToLower(msg), "not installed") {
			return fmt.Errorf("app not installed: %s", bundleID)
		}
		return wrapMissingDDI(fmt.Errorf("launch %s on %s: %w", bundleID, id, err))
	}
	return nil
}

// wrapMissingDDI adds a helpful hint to errors that look like the
// device's Developer Disk Image isn't mounted. The bundled `ios image
// auto <udid>` (or opening the device once in Xcode) installs it.
func wrapMissingDDI(err error) error {
	if err == nil {
		return nil
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "developer disk") || strings.Contains(s, "image not mounted") || strings.Contains(s, "could not start service") {
		return fmt.Errorf("%w\n\nHint: this is the iOS ≤16 lockdown path which requires the Developer Disk Image to be mounted. Mount it by opening the device once in Xcode → Devices and Simulators, or run `ios image auto <udid>` (the bundled binary is in $(brew --prefix)/opt/spyder/libexec/spyder/ios).", err)
	}
	return err
}

// TerminateApp stops an app by bundle id. iOS doesn't expose a
// "kill by bundle id" RPC directly — go-ios kills by pid — so we
// resolve the pid first. Path selection mirrors LaunchApp: iOS-17+
// uses appservice.KillProcess; iOS ≤16 uses
// instruments.ProcessControl.KillProcess.
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
	_, major, sErr := a.goios.SessionWithVersion(id)
	if sErr != nil {
		return fmt.Errorf("terminate %s on %s: %w", bundleID, id, sErr)
	}
	if major != 0 && major < 17 {
		dev, _, err := a.goios.SessionWithVersion(id)
		if err != nil {
			return fmt.Errorf("terminate %s on %s: %w", bundleID, id, err)
		}
		pc, err := instruments.NewProcessControl(dev)
		if err != nil {
			a.goios.Invalidate(id)
			return wrapMissingDDI(fmt.Errorf("terminate %s on %s: instruments.ProcessControl: %w", bundleID, id, err))
		}
		defer pc.Close()
		if err := pc.KillProcess(uint64(pid)); err != nil {
			return fmt.Errorf("terminate %s (pid %d) on %s: %w", bundleID, pid, id, err)
		}
		return nil
	}
	conn, release, err := a.asPool.Acquire(id)
	if err != nil {
		a.goios.Invalidate(id)
		return fmt.Errorf("appservice on %s: %w", id, err)
	}
	defer release()
	if err := conn.KillProcess(pid); err != nil {
		a.asPool.Invalidate(id)
		return fmt.Errorf("terminate %s (pid %d) on %s: %w", bundleID, pid, id, err)
	}
	return nil
}

// AppPID returns the pid of a running app by bundle id. Implemented
// in two RPCs: installation_proxy resolves the bundle id to its
// installed .app folder, and a process-list RPC scans live processes
// for one whose path contains that .app folder. The process-list path
// branches per iOS major version: appservice.ListProcesses on iOS-17+
// (RemoteXPC), instruments.NewDeviceInfoService.ProcessList on iOS ≤16
// (DTX-over-lockdown). Returns the "app not running" error sentinel
// that the deploy_app handler keys off when verify-pid runs
// immediately after a launch.
func (a *IOSAdapter) AppPID(id, bundleID string) (int, error) {
	if id == "" || bundleID == "" {
		return 0, errors.New("device id and bundle_id are required")
	}
	appBase, err := a.installedAppFolder(id, bundleID)
	if err != nil {
		a.goios.Invalidate(id)
		return 0, err
	}
	if appBase == "" {
		return 0, fmt.Errorf("app not installed: %s", bundleID)
	}
	_, major, sErr := a.goios.SessionWithVersion(id)
	if sErr != nil {
		return 0, fmt.Errorf("AppPID for %s on %s: %w", bundleID, id, sErr)
	}
	needle := "/" + appBase + "/"
	if major != 0 && major < 17 {
		dev, _, err := a.goios.SessionWithVersion(id)
		if err != nil {
			return 0, fmt.Errorf("AppPID for %s on %s: %w", bundleID, id, err)
		}
		di, err := instruments.NewDeviceInfoService(dev)
		if err != nil {
			a.goios.Invalidate(id)
			return 0, wrapMissingDDI(fmt.Errorf("AppPID for %s on %s: instruments.DeviceInfoService: %w", bundleID, id, err))
		}
		defer di.Close()
		procs, err := di.ProcessList()
		if err != nil {
			return 0, fmt.Errorf("list processes on %s: %w", id, err)
		}
		for _, p := range procs {
			// ProcessInfo on the lockdown path doesn't always carry a
			// full executable path — match on .Name when Path is empty,
			// falling back to Path when present.
			if p.RealAppName != "" && strings.Contains(p.RealAppName, needle) {
				return int(p.Pid), nil
			}
			if p.Name != "" && (p.Name == appBase || strings.HasSuffix(p.Name, "/"+appBase)) {
				return int(p.Pid), nil
			}
		}
		return 0, fmt.Errorf("app not running: %s", bundleID)
	}
	asConn, release, err := a.asPool.Acquire(id)
	if err != nil {
		a.goios.Invalidate(id)
		return 0, fmt.Errorf("appservice on %s: %w", id, err)
	}
	defer release()
	procs, err := asConn.ListProcesses()
	if err != nil {
		a.asPool.Invalidate(id)
		return 0, fmt.Errorf("list processes on %s: %w", id, err)
	}
	for _, p := range procs {
		if strings.Contains(p.Path, needle) {
			return p.Pid, nil
		}
	}
	return 0, fmt.Errorf("app not running: %s", bundleID)
}

// installedAppFolder returns the basename of the .app folder
// (e.g. "MultiMaze.app") for the given bundle id, queried via
// installation_proxy through the pooled connection. Returns "" when
// the bundle isn't installed.
func (a *IOSAdapter) installedAppFolder(id, bundleID string) (string, error) {
	conn, release, err := a.ipPool.Acquire(id)
	if err != nil {
		return "", fmt.Errorf("installation_proxy: %w", err)
	}
	defer release()
	apps, err := conn.BrowseAllApps()
	if err != nil {
		a.ipPool.Invalidate(id)
		return "", fmt.Errorf("browse apps: %w", err)
	}
	for _, app := range apps {
		if app.CFBundleIdentifier() == bundleID {
			return filepath.Base(app.Path()), nil
		}
	}
	return "", nil
}

// Crashes fetches crash reports from the device via go-ios's
// crashreport package (afc over com.apple.crashreportcopymobile).
// Bulk-downloads all .ips files into a temp directory, parses the
// first-line JSON header from each, and filters by since/process.
// Returns reports newest-first.
func (a *IOSAdapter) Crashes(id string, since time.Time, process string) ([]CrashReport, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	dev, err := a.goios.Session(id)
	if err != nil {
		return nil, fmt.Errorf("crashes: %w", err)
	}

	tmp, err := os.MkdirTemp("", "spyder-crashes-*")
	if err != nil {
		return nil, fmt.Errorf("mkdir temp for crashes: %w", err)
	}
	defer os.RemoveAll(tmp)

	if err := crashreport.DownloadReports(dev, "*", tmp); err != nil {
		a.goios.Invalidate(id)
		return nil, fmt.Errorf("crashreport download on %s: %w", id, err)
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		return nil, fmt.Errorf("read crash temp dir: %w", err)
	}

	reports := make([]CrashReport, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(tmp, e.Name()))
		if rerr != nil {
			continue
		}
		cr := CrashReport{Raw: string(raw)}

		// Parse the first-line JSON header for structured fields. .ips
		// files start with a one-line JSON envelope, then a multi-line
		// body. The legacy bridge path used the same pattern.
		firstLine := cr.Raw
		if i := strings.IndexByte(cr.Raw, '\n'); i >= 0 {
			firstLine = cr.Raw[:i]
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

		// Apply since/process filters that the bridge previously did
		// server-side.
		if !since.IsZero() && cr.Timestamp.Before(since) {
			continue
		}
		if process != "" && cr.Process != process {
			continue
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

// IsSimulatorID is the exported wrapper for isSimulatorID, for callers
// outside the device package (e.g. mcp's appchannel host picker).
func IsSimulatorID(id string) bool { return isSimulatorID(id) }

// isSimulatorID returns true when id looks like an iOS simulator UUID
// rather than a hardware device UDID.
//
// Hardware UDID format: exactly 8 hex + "-" + 16 hex, e.g.
//
//	00008103-001122334455667A
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
	conn, release, err := a.ipPool.Acquire(id)
	if err != nil {
		a.goios.Invalidate(id)
		return fmt.Errorf("installation_proxy on %s: %w", id, err)
	}
	defer release()
	if err := conn.Uninstall(bundleID); err != nil {
		a.ipPool.Invalidate(id)
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

// LogRange returns log entries from the device between since and
// until, streamed through go-ios's `os_trace_relay` (RSD-shimmed on
// iOS-17+) — the same Apple service Xcode's Console.app uses. The
// device exposes only a live tail (no stable API to query archived
// entries by timestamp), so this drains the live stream and keeps
// entries whose Timestamp lies inside the window. Callers should
// provide a reasonable upper bound; absent one we cap at 5s.
//
// Filter fields:
//
//   - Process: matched client-side against the parsed image_name
//     (CFBundleExecutable for third-party apps; daemon binary name
//     for system processes).
//   - Subsystem: matched server-side against `entry.Label.Subsystem`
//     (the OSLog subsystem registered by the emitter, e.g.
//     `com.apple.network`).
//   - Regex: matched client-side against Message.
func (a *IOSAdapter) LogRange(id string, filter LogFilter, since, until time.Time) ([]LogLine, error) {
	if id == "" {
		return nil, errors.New("device identifier is empty")
	}
	regexFilter, err := compileLogRegex(filter.Regex)
	if err != nil {
		return nil, err
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

	var lines []LogLine
	err = a.streamOSTrace(ctx, id, filter, regexFilter, func(ll LogLine) bool {
		if !since.IsZero() && ll.Timestamp.Before(since) {
			return true
		}
		if !until.IsZero() && ll.Timestamp.After(until) {
			return true
		}
		lines = append(lines, ll)
		return true
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return lines, err
	}
	return lines, nil
}

// LogStream pumps live log entries from the device through go-ios's
// `os_trace_relay` service into out until ctx is cancelled. Filter
// semantics match LogRange.
func (a *IOSAdapter) LogStream(ctx context.Context, id string, filter LogFilter, out chan<- LogLine) error {
	if id == "" {
		return errors.New("device identifier is empty")
	}
	regexFilter, err := compileLogRegex(filter.Regex)
	if err != nil {
		return err
	}
	err = a.streamOSTrace(ctx, id, filter, regexFilter, func(ll LogLine) bool {
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

func compileLogRegex(s string) (*regexp.Regexp, error) {
	if s == "" {
		return nil, nil
	}
	r, err := regexp.Compile(s)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}
	return r, nil
}

// streamOSTrace surfaces iOS log entries to spyder. It prefers the
// DTX `activitytracetap` channel (the same path Xcode's Console.app
// uses, surfaces third-party app emissions) and falls back to the
// lockdown-level `os_trace_relay` service when DTX isn't available
// (developer disk image not mounted, iOS <17, etc.). os_trace_relay
// is hardened against third-party app output on iOS 17+ — fallback
// produces system-process coverage only.
//
// emit returns false to stop the stream early (e.g. ctx cancellation
// in LogStream's send branch). All resources are released when emit
// returns false or ctx fires.
func (a *IOSAdapter) streamOSTrace(ctx context.Context, id string, filter LogFilter,
	regexFilter *regexp.Regexp, emit func(LogLine) bool) error {
	dev, err := a.goios.Session(id)
	if err != nil {
		return fmt.Errorf("ostrace: %w", err)
	}

	// Try DTX first.
	if err := a.streamOSLogDTX(ctx, id, dev, filter, regexFilter, emit); err == nil {
		return nil
	} else if ctx.Err() != nil {
		return ctx.Err()
	} else {
		slog.Warn("oslog DTX path unavailable; falling back to lockdown os_trace_relay (no third-party app coverage)",
			"device", id, "error", err.Error())
	}

	// Fallback: lockdown os_trace_relay.
	// pid=-1: all processes. MessageFilterLogMessage: only os_log
	// entries (skip ActivityCreate/Transition/Signpost record types
	// the caller didn't ask for). StreamFlagsAll: emit every severity
	// the device exposes (Default, Info, Debug, Error, Fault).
	conn, err := ostrace.New(dev, -1,
		ostrace.MessageFilterLogMessage,
		ostrace.StreamFlagsAll)
	if err != nil {
		a.goios.Invalidate(id)
		return fmt.Errorf("ostrace on %s: %w", id, err)
	}
	defer conn.Close()

	// Cancel the blocking read by closing the connection when ctx fires
	// — ReadEntry doesn't honour context directly.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	var read, emitted int
	for {
		entry, err := conn.ReadEntry()
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("ostrace stream end (context canceled)",
					"device", id, "read", read, "emitted", emitted)
				return ctx.Err()
			}
			slog.Error("ostrace stream end (transport error)",
				"device", id, "read", read, "emitted", emitted, "err", err.Error())
			return fmt.Errorf("ostrace read on %s: %w", id, err)
		}
		read++
		if filter.Process != "" && entry.ImageName != filter.Process {
			continue
		}
		if filter.Subsystem != "" {
			if entry.Label == nil ||
				!strings.Contains(entry.Label.Subsystem, filter.Subsystem) {
				continue
			}
		}
		if regexFilter != nil && !regexFilter.MatchString(entry.Message) {
			continue
		}
		ll := ostraceEntryToLogLine(entry)
		emitted++
		if !emit(ll) {
			return ctx.Err()
		}
	}
}

func ostraceEntryToLogLine(e ostrace.LogEntry) LogLine {
	return LogLine{
		Timestamp: e.Timestamp,
		Process:   e.ImageName,
		Level:     e.Level.String(),
		Message:   e.Message,
	}
}

// streamOSLogDTX drains records from spyder's oslog package (a DTX
// activitytracetap client) into emit. Returns nil when the stream
// terminates cleanly; non-nil error means the channel couldn't be
// opened or hit a fatal protocol error — callers should treat that
// as a signal to fall back to the lockdown os_trace_relay path.
//
// Record.Timestamp comes from the device as mach absolute time, which
// requires a per-device anchor to map onto wall-clock. Rather than
// chase that anchor, we stamp each LogLine with host-side time.Now()
// at receive — sufficient for the dominant "since launch" / `-2m`
// filtering use cases, with skew bounded by the channel's flush rate
// (the setConfig `ur` parameter, default 500ms).
func (a *IOSAdapter) streamOSLogDTX(ctx context.Context, id string,
	dev goios_ios.DeviceEntry, filter LogFilter, regexFilter *regexp.Regexp,
	emit func(LogLine) bool) error {

	stream, err := oslog.Open(ctx, dev)
	if err != nil {
		return err
	}
	defer stream.Close()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case rec, ok := <-stream.Records:
			if !ok {
				return nil
			}
			if filter.Process != "" && rec.ImageName != filter.Process {
				continue
			}
			if filter.Subsystem != "" &&
				!strings.Contains(rec.Subsystem, filter.Subsystem) {
				continue
			}
			if regexFilter != nil && !regexFilter.MatchString(rec.Message) {
				continue
			}
			ll := LogLine{
				Timestamp: time.Now(),
				Process:   rec.ImageName,
				Level:     rec.MessageType,
				Message:   rec.Message,
			}
			if !emit(ll) {
				return ctx.Err()
			}
		}
	}
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
