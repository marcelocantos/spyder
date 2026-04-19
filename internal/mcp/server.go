// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package mcp implements the spyder MCP tool handler.
// Handler methods return *mcpgo.CallToolResult directly so tools can
// emit image/binary content (e.g. screenshot PNGs) without the daemon
// wrapper needing tool-specific wiring.
package mcp

import (
	"fmt"
	"sync"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/baselines"
	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/network"
	"github.com/marcelocantos/spyder/internal/recording"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/runs"
)

// appliedNetwork tracks a network profile applied to a device by a
// specific owner, so it can be cleared automatically on reservation release.
type appliedNetwork struct {
	profile network.NetworkProfile
	owner   string
}

// Handler implements the spyder tool handler.
type Handler struct {
	mu           sync.Mutex
	inventory    *inventory.Store
	ios          device.Adapter
	android      device.Adapter
	tunneld      TunneldGate
	reservations *reservations.Store
	runs         *runs.Store
	bls          *baselines.Store
	recordings   *recording.Registry
	runsBaseDir  string // base dir for active-run temp files; empty = os.TempDir()

	// networkByDevice maps a normalised device reference to the most
	// recently applied network profile for that device. Cleared when
	// the owning reservation is released.
	networkByDevice map[string]appliedNetwork
}

// TunneldGate is satisfied by *tunneld.Client. The small interface lets
// tests inject a fake without a circular package dependency.
type TunneldGate interface {
	Require() error
	Addr() string
}

// HandlerOption configures a Handler at construction.
type HandlerOption func(*Handler)

// WithReservations injects a reservation store so the handler can
// enforce strict holds on mutating tools. If omitted, all mutating
// tools run without any reservation checks (useful for tests).
func WithReservations(s *reservations.Store) HandlerOption {
	return func(h *Handler) { h.reservations = s }
}

// WithRuns injects a run-artefact store. When present, `reserve`
// opens a run, `release` closes it, and artefact-producing tools
// (currently just screenshot) write into the active run dir.
func WithRuns(s *runs.Store) HandlerOption {
	return func(h *Handler) { h.runs = s }
}

// WithInventory injects a shared inventory store. Useful when the
// same inventory view is needed elsewhere (e.g. reservation
// normalization). Defaults to inventory.New().
func WithInventory(inv *inventory.Store) HandlerOption {
	return func(h *Handler) { h.inventory = inv }
}

// WithBaselines injects the visual-regression baseline store. When
// present, `baseline_update`, `diff`, and `baselines_list` are fully
// functional; otherwise they return a clear "not configured" error.
func WithBaselines(s *baselines.Store) HandlerOption {
	return func(h *Handler) { h.bls = s }
}

// WithRunsBaseDir sets the directory where recording temp files are created.
// Defaults to os.TempDir() when empty.
func WithRunsBaseDir(dir string) HandlerOption {
	return func(h *Handler) { h.runsBaseDir = dir }
}

// NewHandler creates a new spyder tool handler. tun may be nil for
// handler instances that never call DVT-dependent tools; tools that
// need it will return a clear error when tun is missing.
func NewHandler(tun TunneldGate, opts ...HandlerOption) *Handler {
	h := &Handler{
		inventory:       inventory.New(),
		ios:             device.NewIOSAdapter(),
		android:         device.NewAndroidAdapter(),
		tunneld:         tun,
		recordings:      recording.NewRegistry(),
		networkByDevice: map[string]appliedNetwork{},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// NewHandlerWithAdapters creates a handler with explicit adapter overrides.
// Useful for tests that inject stub adapters without going through HandlerOption
// indirection. Either ios or android may be nil to use the real adapter.
func NewHandlerWithAdapters(tun TunneldGate, ios, android device.Adapter) *Handler {
	h := &Handler{
		inventory: inventory.New(),
		ios:       device.NewIOSAdapter(),
		android:   device.NewAndroidAdapter(),
		tunneld:   tun,
	}
	if ios != nil {
		h.ios = ios
	}
	if android != nil {
		h.android = android
	}
	return h
}

// ResolveAdapterForStream exposes adapter resolution for the REST SSE
// streaming endpoint. Returns the adapter and the platform-specific device
// id. The caller must not hold h.mu when calling this; it acquires the lock
// internally.
func (h *Handler) ResolveAdapterForStream(dev string) (device.Adapter, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	adapter, _, id, err := h.resolveAdapter(dev)
	return adapter, id, err
}

// Dispatch routes a tool call by name to its handler.
func (h *Handler) Dispatch(name string, args map[string]any) (*mcpgo.CallToolResult, error) {
	switch name {
	case "devices":
		return h.handleDevices(args)
	case "resolve":
		return h.handleResolve(args)
	case "keepawake":
		return h.handleKeepAwake(args)
	case "device_state":
		return h.handleDeviceState(args)
	case "screenshot":
		return h.handleScreenshot(args)
	case "list_apps":
		return h.handleListApps(args)
	case "launch_app":
		return h.handleLaunchApp(args)
	case "terminate_app":
		return h.handleTerminateApp(args)
	case "install_app":
		return h.handleInstallApp(args)
	case "uninstall_app":
		return h.handleUninstallApp(args)
	case "deploy_app":
		return h.handleDeployApp(args)
	case "reserve":
		return h.handleReserve(args)
	case "release":
		return h.handleRelease(args)
	case "renew":
		return h.handleRenew(args)
	case "reservations":
		return h.handleReservations(args)
	case "runs_list":
		return h.handleRunsList(args)
	case "runs_show":
		return h.handleRunsShow(args)
	case "rotate":
		return h.handleRotate(args)
	case "crashes":
		return h.handleCrashes(args)
	// --- simulator tools --------------------------------------------------
	case "sim_list":
		return h.handleSimList(args)
	case "sim_create":
		return h.handleSimCreate(args)
	case "sim_boot":
		return h.handleSimBoot(args)
	case "sim_shutdown":
		return h.handleSimShutdown(args)
	case "sim_delete":
		return h.handleSimDelete(args)
	// --- emulator tools ---------------------------------------------------
	case "emu_list":
		return h.handleEmuList(args)
	case "emu_create":
		return h.handleEmuCreate(args)
	case "emu_boot":
		return h.handleEmuBoot(args)
	case "emu_shutdown":
		return h.handleEmuShutdown(args)
	case "emu_delete":
		return h.handleEmuDelete(args)
	// --- visual regression tools ------------------------------------------
	case "baseline_update":
		return h.handleBaselineUpdate(args)
	case "diff":
		return h.handleDiff(args)
	case "baselines_list":
		return h.handleBaselinesList(args)
	case "record_start":
		return h.handleRecordStart(args)
	case "record_stop":
		return h.handleRecordStop(args)
	case "network":
		return h.handleNetwork(args)
	case "logs":
		return h.handleLogsRange(args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// Definitions returns the complete MCP tool definition list — core tools
// plus visual-regression tools.
func Definitions() []mcpgo.Tool {
	return append(allBaseDefinitions(), visualDefinitions()...)
}

// allBaseDefinitions returns the core (non-visual) tool definitions.
func allBaseDefinitions() []mcpgo.Tool {
	return []mcpgo.Tool{
		mcpgo.NewTool("devices",
			mcpgo.WithDescription("List connected mobile devices across platforms, with alias, platform, model, and OS version."),
			mcpgo.WithString("platform",
				mcpgo.Description("Filter by platform: ios, android, or all (default)"),
			),
		),

		mcpgo.NewTool("resolve",
			mcpgo.WithDescription("Resolve a symbolic device name (e.g. 'Pippa') to its platform-specific UUIDs for use with xcodebuild, devicectl, pymobiledevice3, or adb."),
			mcpgo.WithString("name",
				mcpgo.Required(),
				mcpgo.Description("Symbolic name or raw UUID from the device inventory"),
			),
		),

		mcpgo.NewTool("keepawake",
			mcpgo.WithDescription("Foreground the KeepAwake companion app on a device so it holds the screen awake while plugged in. Typically called by test-run wrappers after tests finish. Strictly enforced: rejects if the device is reserved by a different owner."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("device_state",
			mcpgo.WithDescription("Report current device state: battery level, thermal state, charging status, foreground app. Read-only; not subject to reservations."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
		),

		mcpgo.NewTool("screenshot",
			mcpgo.WithDescription("Capture a PNG screenshot of the device. Returns the image inline for the agent to inspect. iOS uses pymobiledevice3 developer dvt (requires tunneld); Android uses adb shell screencap. Strictly enforced: rejects if the device is reserved by a different owner."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("list_apps",
			mcpgo.WithDescription("List installed third-party apps on the device with bundle id, and (iOS only) display name and version. Read-only; not subject to reservations."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
		),

		mcpgo.NewTool("launch_app",
			mcpgo.WithDescription("Foreground an app by bundle id. iOS uses pymobiledevice3 dvt launch (requires tunneld); Android uses adb monkey with the LAUNCHER intent. Strictly enforced: rejects if the device is reserved by a different owner."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("bundle_id",
				mcpgo.Required(),
				mcpgo.Description("App bundle identifier (e.g. com.example.app)"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("terminate_app",
			mcpgo.WithDescription("Terminate a running app by bundle id. iOS resolves the PID via dvt then kills (requires tunneld); Android uses adb am force-stop. Strictly enforced: rejects if the device is reserved by a different owner."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("bundle_id",
				mcpgo.Required(),
				mcpgo.Description("App bundle identifier (e.g. com.example.app)"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("install_app",
			mcpgo.WithDescription("Install an app on the device. Accepts a .app or .ipa path (iOS) or .apk path (Android). The path must not contain '..' and must exist. Strictly enforced: rejects if the device is reserved by a different owner."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("path",
				mcpgo.Required(),
				mcpgo.Description("Absolute or relative path to the .app/.ipa (iOS) or .apk (Android) to install"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("uninstall_app",
			mcpgo.WithDescription("Remove an app from the device by bundle id / package name. iOS uses xcrun devicectl; Android uses adb uninstall. Strictly enforced: rejects if the device is reserved by a different owner."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("bundle_id",
				mcpgo.Required(),
				mcpgo.Description("App bundle identifier (e.g. com.example.app)"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("deploy_app",
			mcpgo.WithDescription("Atomic deploy helper: terminate → install → launch → verify-new-pid. Returns {bundle_id, pid} on success. Fails fast if install fails. 'Not running' errors from the terminate step are ignored (app may not be running yet). The bundle_id is derived automatically from the .app Info.plist (iOS) or via aapt dump badging (Android); pass bundle_id explicitly to skip derivation. Requires tunneld on iOS (for launch + pid-verify via DVT). Strictly enforced: rejects if the device is reserved by a different owner."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("path",
				mcpgo.Required(),
				mcpgo.Description("Absolute or relative path to the .app/.ipa (iOS) or .apk (Android) to install"),
			),
			mcpgo.WithString("bundle_id",
				mcpgo.Description("App bundle identifier — derived automatically from Info.plist or aapt if omitted"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("reserve",
			mcpgo.WithDescription("Acquire an exclusive reservation on a device so parallel sessions won't interrupt mutating operations (keepawake, screenshot, launch/terminate). Default TTL is 3600s, max 86400s. Same-owner re-acquires renew in place."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Required(),
				mcpgo.Description("Free-form owner identity; convention is the project basename (e.g. 'tiltbuggy')"),
			),
			mcpgo.WithNumber("ttl_seconds",
				mcpgo.Description("Reservation lifetime in seconds (default 3600, max 86400)"),
			),
			mcpgo.WithString("note",
				mcpgo.Description("Human-readable note surfaced in conflict errors (e.g. 'UI regression run')"),
			),
		),

		mcpgo.NewTool("release",
			mcpgo.WithDescription("Release a reservation held by the given owner. Freeing a device you don't own returns a Conflict; freeing an unreserved device is a no-op."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Required(),
				mcpgo.Description("Owner identity under which the reservation was taken"),
			),
		),

		mcpgo.NewTool("renew",
			mcpgo.WithDescription("Extend the TTL on an existing reservation. Only the owner can renew. Useful for long-running workflows that outlive the default TTL."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Required(),
				mcpgo.Description("Owner identity under which the reservation was taken"),
			),
			mcpgo.WithNumber("ttl_seconds",
				mcpgo.Description("New reservation lifetime in seconds from now (default 3600, max 86400)"),
			),
		),

		mcpgo.NewTool("reservations",
			mcpgo.WithDescription("List all active reservations across all devices. Read-only."),
		),

		mcpgo.NewTool("runs_list",
			mcpgo.WithDescription("List run-artefact bundles under ~/.spyder/runs, newest first. Each reservation opens a run; artefact-producing tools (screenshot, future: record/log/crashes) deposit files there."),
		),

		mcpgo.NewTool("runs_show",
			mcpgo.WithDescription("Return a single run's full manifest — device, owner, note, timestamps, and the list of artefacts (name, source tool, mime, size, timestamp)."),
			mcpgo.WithString("run_id",
				mcpgo.Required(),
				mcpgo.Description("Run id as returned by runs_list (e.g. 20260419-143022-a3f1b2)"),
			),
		),

		mcpgo.NewTool("rotate",
			mcpgo.WithDescription("Rotate an iOS simulator or Android emulator to the specified screen orientation. Physical iOS and Android devices return an error — only simulators (iOS) and emulators (Android serials matching 'emulator-*') are supported. Strictly enforced: rejects if the device is reserved by a different owner."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Simulator UDID or emulator serial (e.g. emulator-5554)"),
			),
			mcpgo.WithString("orientation",
				mcpgo.Required(),
				mcpgo.Description("Target orientation: portrait, landscape-left, landscape-right, or portrait-upside-down"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		// ---- iOS simulator tools ----------------------------------------

		mcpgo.NewTool("sim_list",
			mcpgo.WithDescription("List all iOS simulators known to simctl, with UDID, name, state (Booted/Shutdown), and runtime. Booted simulators automatically appear in `spyder devices` iOS output. Read-only."),
			mcpgo.WithString("state",
				mcpgo.Description("Optional filter: 'Booted', 'Shutdown', etc. Omit for all."),
			),
		),

		mcpgo.NewTool("sim_create",
			mcpgo.WithDescription("Create a new iOS simulator. Returns the UDID of the new simulator. Use sim_list to find existing simulators; use `xcrun simctl list devicetypes --json` and `xcrun simctl list runtimes --json` to discover available device types and runtimes."),
			mcpgo.WithString("name",
				mcpgo.Required(),
				mcpgo.Description("Human-readable name for the simulator (e.g. 'MyTestPhone')"),
			),
			mcpgo.WithString("device_type_id",
				mcpgo.Required(),
				mcpgo.Description("Device type identifier, e.g. 'com.apple.CoreSimulator.SimDeviceType.iPhone-15'"),
			),
			mcpgo.WithString("runtime_id",
				mcpgo.Required(),
				mcpgo.Description("Runtime identifier, e.g. 'com.apple.CoreSimulator.SimRuntime.iOS-17-5'"),
			),
		),

		mcpgo.NewTool("sim_boot",
			mcpgo.WithDescription("Boot a shutdown iOS simulator by UDID. The simulator will appear in `spyder devices` iOS output once booted. Use sim_list to find available simulators."),
			mcpgo.WithString("udid",
				mcpgo.Required(),
				mcpgo.Description("Simulator UDID as returned by sim_list"),
			),
		),

		mcpgo.NewTool("sim_shutdown",
			mcpgo.WithDescription("Shut down a booted iOS simulator by UDID. The simulator will no longer appear as connected in `spyder devices`."),
			mcpgo.WithString("udid",
				mcpgo.Required(),
				mcpgo.Description("Simulator UDID as returned by sim_list"),
			),
		),

		mcpgo.NewTool("sim_delete",
			mcpgo.WithDescription("Delete an iOS simulator by UDID. The simulator must be shut down first. This is irreversible."),
			mcpgo.WithString("udid",
				mcpgo.Required(),
				mcpgo.Description("Simulator UDID as returned by sim_list"),
			),
		),

		// ---- Android emulator tools -------------------------------------

		mcpgo.NewTool("emu_list",
			mcpgo.WithDescription("List all configured Android Virtual Devices (AVDs) with name, path, target, and ABI. Booted emulators appear in `spyder devices` Android output with a serial like 'emulator-5554'. Read-only."),
		),

		mcpgo.NewTool("emu_create",
			mcpgo.WithDescription("Create a new Android Virtual Device (AVD). The system image package must already be installed via Android SDK Manager. Use `avdmanager list target` and `avdmanager list device` to discover available targets and device profiles."),
			mcpgo.WithString("name",
				mcpgo.Required(),
				mcpgo.Description("Name for the AVD (e.g. 'Pixel6_API34')"),
			),
			mcpgo.WithString("system_image",
				mcpgo.Required(),
				mcpgo.Description("System image package path, e.g. 'system-images;android-34;google_apis;arm64-v8a'"),
			),
			mcpgo.WithString("device_profile",
				mcpgo.Required(),
				mcpgo.Description("Device profile ID, e.g. 'pixel_6'. List options with `avdmanager list device`."),
			),
		),

		mcpgo.NewTool("emu_boot",
			mcpgo.WithDescription("Start an Android emulator (AVD) in headless mode. The emulator process is detached and will appear in `adb devices` and `spyder devices` once fully booted (typically 30–90 seconds). Use emu_shutdown with the emulator serial to stop it."),
			mcpgo.WithString("name",
				mcpgo.Required(),
				mcpgo.Description("AVD name as returned by emu_list"),
			),
		),

		mcpgo.NewTool("emu_shutdown",
			mcpgo.WithDescription("Shut down a running Android emulator by its adb serial (e.g. 'emulator-5554'). Sends `adb emu kill` to the specific emulator."),
			mcpgo.WithString("serial",
				mcpgo.Required(),
				mcpgo.Description("Emulator serial as shown in `adb devices`, e.g. 'emulator-5554'"),
			),
		),

		mcpgo.NewTool("emu_delete",
			mcpgo.WithDescription("Delete an Android Virtual Device (AVD) by name. The emulator should be shut down first. This removes the AVD configuration and data; the action is irreversible."),
			mcpgo.WithString("name",
				mcpgo.Required(),
				mcpgo.Description("AVD name as returned by emu_list"),
			),
		),

		mcpgo.NewTool("crashes",
			mcpgo.WithDescription("Fetch crash reports from a device. iOS pulls .ips files via pymobiledevice3 crash-reports and parses the first-line JSON header for process, reason, and timestamp. Android attempts tombstones via adb pull /data/tombstones/ (requires root) and falls back to `adb logcat -b crash`. Read-only; not reservation-gated. Pass owner to archive reports into the active run."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("since",
				mcpgo.Description("Return only reports newer than this RFC3339 timestamp (e.g. 2026-04-19T00:00:00Z). Omit to return all available reports."),
			),
			mcpgo.WithString("process",
				mcpgo.Description("Filter by process name (case-insensitive). Omit to return crashes from all processes."),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner; when present and a run is active, crash report content is archived into the run."),
			),
		),

		mcpgo.NewTool("record_start",
			mcpgo.WithDescription("Start a screen recording on an iOS simulator or Android device/emulator. Returns immediately; the recording runs in the background until record_stop is called. iOS physical devices are not supported — use a simulator (xcrun simctl list devices). Strictly enforced: rejects if the device is reserved by a different owner, or if a recording is already in progress on the same device."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UDID/serial. For iOS simulators pass the simulator UDID from `xcrun simctl list devices`."),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("record_stop",
			mcpgo.WithDescription("Stop the active screen recording on a device, finalise the mp4, and return the path to the recorded file. Must be called after record_start."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UDID/serial (must match the value passed to record_start)"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner to authenticate as (optional; required if the device is reserved)"),
			),
		),

		mcpgo.NewTool("network",
			mcpgo.WithDescription(
				"Apply or clear network condition shaping on a device. "+
					"Supported on Android emulators via the adb console. "+
					"iOS (simulator and physical) and physical Android devices are not supported — "+
					"a clear error is returned for those targets.\n\n"+
					"Named profiles: wifi (full-speed), 4g, 3g, edge, gsm, offline.\n"+
					"Dynamic profiles: lossy-<pct> (0–100% packet loss), delay-<ms> (extra one-way latency).\n\n"+
					"NOTE — packet loss (lossy-<pct>) is not implemented by the adb console protocol. "+
					"The profile is partially applied (speed/delay) and an error is returned describing the gap.\n\n"+
					"Applied profiles are cleared automatically when the reservation for the device is released. "+
					"If the daemon exits abnormally before a release, the emulator retains the last applied profile "+
					"until the next ApplyNetwork or ClearNetwork call, or the emulator is restarted.",
			),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Required(),
				mcpgo.Description("Reservation owner to authenticate as"),
			),
			mcpgo.WithString("profile",
				mcpgo.Description("Network profile to apply: wifi, 4g, 3g, edge, gsm, offline, lossy-<pct>, delay-<ms>. Mutually exclusive with clear."),
			),
			mcpgo.WithBoolean("clear",
				mcpgo.Description("If true, clear the applied profile and restore full-speed connectivity. Mutually exclusive with profile."),
			),
		),

		mcpgo.NewTool("logs",
			mcpgo.WithDescription("Fetch log lines from a device between two timestamps. "+
				"iOS uses pymobiledevice3 syslog live; Android uses adb logcat. "+
				"For live streaming (--follow), use the REST SSE endpoint POST /api/v1/log_stream instead — "+
				"MCP transport does not support streaming. Read-only."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("since",
				mcpgo.Description("Start timestamp (RFC3339, e.g. 2026-04-19T14:00:00Z). Defaults to recent output."),
			),
			mcpgo.WithString("until",
				mcpgo.Description("End timestamp (RFC3339). Defaults to now."),
			),
			mcpgo.WithString("process",
				mcpgo.Description("Filter by process name (iOS: --procname; Android: tag/process contains match)"),
			),
			mcpgo.WithString("subsystem",
				mcpgo.Description("Filter by iOS subsystem (e.g. com.apple.networking). Ignored on Android."),
			),
			mcpgo.WithString("tag",
				mcpgo.Description("Filter by Android logcat tag. Ignored on iOS."),
			),
			mcpgo.WithString("regex",
				mcpgo.Description("Regular expression applied to the message body on both platforms."),
			),
		),
	}
}
