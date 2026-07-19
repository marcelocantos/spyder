// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package mcp implements the spyder MCP tool handler.
// Handler methods return *mcpgo.CallToolResult directly so tools can
// emit image/binary content (e.g. screenshot PNGs) without the daemon
// wrapper needing tool-specific wiring.
package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/baselines"
	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/logcapture"
	"github.com/marcelocantos/spyder/internal/network"
	"github.com/marcelocantos/spyder/internal/recording"
	"github.com/marcelocantos/spyder/internal/reservations"
	"github.com/marcelocantos/spyder/internal/runs"
	"github.com/marcelocantos/spyder/internal/selector"
)

// appliedNetwork tracks a network profile applied to a device by a
// specific owner, so it can be cleared automatically on reservation release.
type appliedNetwork struct {
	profile network.NetworkProfile
	owner   string
}

// PoolManager is the management interface for the sim/emu pool (🎯T24).
// Satisfied by *pool.Pool via the poolManagerAdapter wrapper in daemon.go.
// Using any for Status avoids importing pool from mcp (mcp is already a
// large fan-out package; keeping it pool-agnostic is cleaner).
type PoolManager interface {
	// PoolStatus returns a JSON-serialisable snapshot of all templates.
	PoolStatus() any
	PoolWarm(template string, n int) error
	PoolDrain(template string) error
	// PoolGC deletes orphaned spyder-pool-* sims/AVDs not in the
	// in-memory inventory. Returns a JSON-serialisable result.
	PoolGC() any
}

// Handler implements the spyder tool handler.
type Handler struct {
	mu           sync.Mutex
	inventory    *inventory.Store
	ios          device.Adapter
	android      device.Adapter
	desktop      device.Adapter
	reservations *reservations.Store
	runs         *runs.Store
	bls          *baselines.Store
	recordings   *recording.Registry
	logCapture   *logcapture.Manager
	appChannel   *appchannel.Manager
	instances    *appchannel.InstancePool // 🎯T92.1 factory-spawned instance lifecycle
	runsBaseDir  string                   // base dir for active-run temp files; empty = os.TempDir()
	pool         selector.PoolResolver    // optional hook for 🎯T23 fuzzy selector
	poolMgr      PoolManager              // optional hook for 🎯T24 pool management
	health       *health.Supervisor       // live health model + subprocess supervisor (🎯T90)

	// networkByDevice maps a normalised device reference to the most
	// recently applied network profile for that device. Cleared when
	// the owning reservation is released.
	networkByDevice map[string]appliedNetwork

	// launchTimes records when launch_app last fired for each
	// (resolved device UUID, bundle id) pair. Resolves `since=launch`
	// on the logs / crashes tools to "everything since spyder's most
	// recent launch of that app". In-memory only — lost on daemon
	// restart, and absent for apps the user foregrounded via
	// SpringBoard rather than `launch_app`; both are acceptable
	// trade-offs since the dominant use case is "I just called
	// launch_app and want the lines that scrolled by since then".
	launchTimes map[launchKey]time.Time

	// ops tracks in-flight tool calls for spyder status (🎯T99.5).
	ops *opRegistry

	// onDeviceTimeout is an optional hook tests can set; production
	// uses invalidateDeviceSession when a dispatch times out (🎯T99.1).
	onDeviceTimeout func(device string)

	// testHandlers, when non-nil, overrides named tools for unit tests
	// (synthetic stall / never-return handlers for 🎯T99.1).
	testHandlers map[string]toolFunc

	// streamRelay is the daemon's H.264 stream catalogue (optional; 🎯T100).
	streamRelay StreamServers
	// streamListenPort is the HTTP port spyder serve binds (for STREAM_ADDR).
	streamListenPort int

	// dispatchWatch monitors long tool calls for wedged-but-alive stalls (🎯T99.3).
	dispatchWatch *health.ProgressWatchdog
	// selfRestart rate-limits os.Exit for launchd KeepAlive recovery (🎯T99.3).
	selfRestart *health.SelfRestartLimiter
	// selfRestartGrace is how long after a tool deadline we wait for the
	// handler goroutine to finish before requesting self-restart.
	selfRestartGrace time.Duration
}

// launchKey indexes launchTimes. The device dimension is the
// resolved adapter id (platform-specific UUID), not the user-facing
// alias — so `launch_app device=iPad` followed by
// `logs device=00008130-... since=launch` resolves to the same entry.
type launchKey struct {
	deviceID string
	bundleID string
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

// WithPoolResolver injects the sim/emu pool resolver used by the selector
// when no physical device matches (🎯T23 hook). When nil (the
// default) the pool step is skipped and selector resolution fails if no
// physical candidate matches.
func WithPoolResolver(p selector.PoolResolver) HandlerOption {
	return func(h *Handler) { h.pool = p }
}

// WithPoolManager injects the sim/emu pool manager for pool_list/warm/drain
// tools (🎯T24). When nil, pool management tools return a "pool not
// configured" error.
func WithPoolManager(pm PoolManager) HandlerOption {
	return func(h *Handler) { h.poolMgr = pm }
}

// WithLogCapture injects a managed log-capture session manager (🎯T60).
// When omitted, log_capture_* tools return a "log capture not configured"
// error.
func WithLogCapture(m *logcapture.Manager) HandlerOption {
	return func(h *Handler) { h.logCapture = m }
}

// WithAppChannel injects the bidirectional MessagePack RPC manager
// (🎯T75). When omitted, app_* tools return a "not configured" error.
func WithAppChannel(m *appchannel.Manager) HandlerOption {
	return func(h *Handler) { h.appChannel = m }
}

// WithHealth injects the live health supervisor (🎯T90). When omitted,
// NewHandler creates a default one so the health() builtin and REST
// surface always have a model to read, even before the daemon wires in
// probes and subprocess supervision.
func WithHealth(s *health.Supervisor) HandlerOption {
	return func(h *Handler) {
		if s != nil {
			h.health = s
		}
	}
}

// Health returns the handler's health supervisor. Always non-nil.
// The daemon uses it to run supervise loops, feed device probes, and
// attach the notifier; the health() builtin and /api/v1/health read its
// model.
func (h *Handler) Health() *health.Supervisor { return h.health }

// SetStreamRelay wires the streamrelay catalogue for launch_player (🎯T100.3).
func (h *Handler) SetStreamRelay(r StreamServers, listenPort int) {
	if h == nil {
		return
	}
	h.streamRelay = r
	if listenPort > 0 {
		h.streamListenPort = listenPort
	}
}

// EnableSelfHeal wires ProgressWatchdog + SelfRestartLimiter for 🎯T99.3.
// Call from daemon after health model is live.
//
// watchdogTimeout is the no-progress stall threshold for the "spyder"
// daemon-self entity (must be ≤ typical tool deadlines so Check can fire
// before self-restart). restartGrace is how long after a dispatch deadline
// we wait for the handler goroutine before dumping + exiting for launchd.
func (h *Handler) EnableSelfHeal(watchdogTimeout, restartGrace time.Duration) {
	if h == nil || h.health == nil {
		return
	}
	if watchdogTimeout <= 0 {
		// Match device-op deadline class so stall is visible before restart.
		watchdogTimeout = DeadlineDeviceOp
	}
	if restartGrace <= 0 {
		restartGrace = 5 * time.Second
	}
	// Entity name "spyder" matches daemonSelfHealthID / status surface.
	h.dispatchWatch = health.NewProgressWatchdog(h.health.Model(), "spyder", watchdogTimeout)
	h.selfRestart = health.NewSelfRestartLimiter(3, time.Hour)
	h.selfRestart.SetBeforeExit(h.persistSelfRestartEvidence)
	h.selfRestartGrace = restartGrace
	// Drive Check on a short interval so stalls surface without waiting for
	// the next dispatch.
	go h.dispatchWatch.Run(context.Background(), minDuration(watchdogTimeout/4, 5*time.Second))
}

// persistSelfRestartEvidence writes a goroutine dump + wedge snapshot under
// ~/.spyder/ before process exit (🎯T99.3 acceptance).
func (h *Handler) persistSelfRestartEvidence(reason string) {
	// Local imports kept in this method's callees to avoid cycles.
	persistSelfRestartEvidence(reason)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// NewHandler creates a new spyder tool handler.
func NewHandler(opts ...HandlerOption) *Handler {
	h := &Handler{
		inventory:       inventory.New(),
		ios:             device.NewIOSAdapter(),
		android:         device.NewAndroidAdapter(),
		recordings:      recording.NewRegistry(),
		networkByDevice: map[string]appliedNetwork{},
		launchTimes:     map[launchKey]time.Time{},
		health:          health.NewSupervisor(health.New()),
		ops:             newOpRegistry(),
	}
	for _, opt := range opts {
		opt(h)
	}
	// Constructed after options so the desktop adapter sees the final
	// inventory (WithInventory may have replaced the default).
	if h.desktop == nil {
		h.desktop = device.NewDesktopAdapter(h.inventory)
	}
	// The factory instance pool needs the app-channel manager (🎯T92.1).
	if h.appChannel != nil && h.instances == nil {
		h.instances = appchannel.NewInstancePool(h.appChannel)
	}
	return h
}

// NewHandlerWithAdapters creates a handler with explicit adapter overrides.
// Useful for tests that inject stub adapters without going through HandlerOption
// indirection. Either ios or android may be nil to use the real adapter.
func NewHandlerWithAdapters(ios, android device.Adapter) *Handler {
	h := &Handler{
		inventory:   inventory.New(),
		ios:         device.NewIOSAdapter(),
		android:     device.NewAndroidAdapter(),
		launchTimes: map[launchKey]time.Time{},
		health:      health.NewSupervisor(health.New()),
		ops:         newOpRegistry(),
	}
	if ios != nil {
		h.ios = ios
	}
	if android != nil {
		h.android = android
	}
	h.desktop = device.NewDesktopAdapter(h.inventory)
	return h
}

// tunnelListenerStarter is implemented by device adapters that maintain
// a background usbmux attach/detach listener (currently only the iOS
// adapter, 🎯T89.2). Optional: stub adapters injected in tests don't
// implement it.
type tunnelListenerStarter interface {
	StartTunnelListener(ctx context.Context)
}

// healthModelSetter is implemented by device adapters that report
// per-device health (the iOS adapter, 🎯T90). Optional.
type healthModelSetter interface {
	SetHealthModel(m *health.Model, pinned func(udid string) bool)
}

// StartDeviceListeners starts any device-adapter background listeners —
// currently the iOS usbmux tunnel listener that keeps the RemoteXPC
// tunnel registry fresh across device re-enumeration (🎯T89.2) and feeds
// per-device health into the model (🎯T90). It blocks until ctx is
// cancelled, so callers run it in a goroutine. A no-op for adapters that
// don't maintain a listener.
func (h *Handler) StartDeviceListeners(ctx context.Context) {
	if s, ok := h.ios.(healthModelSetter); ok {
		// 🎯T99.6: pin devices marked expected_present in inventory.
		inv := h.inventory
		s.SetHealthModel(h.health.Model(), func(udid string) bool {
			if inv == nil {
				return false
			}
			e, ok := inv.Lookup(udid)
			return ok && e.ExpectedPresent
		})
	}
	if s, ok := h.ios.(tunnelListenerStarter); ok {
		s.StartTunnelListener(ctx)
	}
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

// Dispatch routes a tool call by name to its handler under ctx (🎯T99.1).
// Every call is logged at INFO on entry and exit, tracked in the
// in-flight op registry (🎯T99.5), and bounded by a tool-class deadline.
// On deadline breach the call returns a structured timeout error and the
// device session is invalidated so the next call can re-resolve.
// A watchdog goroutine still logs `mcp dispatch slow` while in flight.
func (h *Handler) Dispatch(ctx context.Context, name string, args map[string]any) (*mcpgo.CallToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	dev := deviceArg(args)
	slog.Info("mcp dispatch", "tool", name, "device", dev)

	if h.ops == nil {
		h.ops = newOpRegistry()
	}
	opID := h.ops.begin(name, dev)
	defer h.ops.end(opID)

	if h.dispatchWatch != nil {
		h.dispatchWatch.Begin()
		// Done only on the success path below. On timeout, ownership of
		// outstanding transfers to maybeSelfRestartAfterStuck: Done() before
		// ForceStall would clear outstanding and make ForceStall a no-op
		// (🎯T99.3). Done after ForceStall would RecoverySucceeded and clear
		// needs_attention before dump/exit.
	}

	ctx, cancel := withToolDeadline(ctx, name)
	defer cancel()

	done := make(chan struct{})
	go watchSlowDispatch(name, dev, started, done, cancel)

	type outcome struct {
		result *mcpgo.CallToolResult
		err    error
	}
	ch := make(chan outcome, 1)
	// finished is closed when the handler goroutine returns (even after
	// we've already timed out the caller).
	finished := make(chan struct{})
	go func() {
		res, err := h.dispatch(name, args)
		ch <- outcome{res, err}
		close(finished)
	}()

	var result *mcpgo.CallToolResult
	var err error
	select {
	case o := <-ch:
		result, err = o.result, o.err
	case <-ctx.Done():
		// Best-effort: let the handler goroutine finish in the background;
		// we still return a structured timeout so the agent can proceed.
		elapsed := time.Since(started)
		h.invalidateOnTimeout(dev)
		close(done)
		msg := formatTimeoutError(name, dev, elapsed)
		slog.Error("mcp dispatch timeout",
			"tool", name, "device", dev, "duration_ms", elapsed.Milliseconds())
		// 🎯T99.3: if the handler is still stuck after a grace period,
		// force-stall daemon-self and request supervised self-restart.
		// Do not Done the watchdog here — see completeStuckDispatch.
		go h.maybeSelfRestartAfterStuck(name, dev, finished)
		return toolErr("%s", msg)
	}
	close(done)
	if h.dispatchWatch != nil {
		h.dispatchWatch.Done()
	}
	elapsedMs := time.Since(started).Milliseconds()
	if err != nil {
		slog.Error("mcp dispatch failed",
			"tool", name, "duration_ms", elapsedMs, "error", err.Error())
	} else {
		slog.Info("mcp dispatch ok",
			"tool", name, "duration_ms", elapsedMs)
	}
	return result, err
}

// maybeSelfRestartAfterStuck waits selfRestartGrace for the stuck handler
// goroutine. If it finishes late, Done the watchdog (no restart). If it is
// still stuck, completeStuckDispatch force-stalls + dumps + exits (🎯T99.3).
func (h *Handler) maybeSelfRestartAfterStuck(tool, device string, stillRunning <-chan struct{}) {
	if h.dispatchWatch == nil && h.selfRestart == nil {
		return
	}
	grace := h.selfRestartGrace
	if grace <= 0 {
		grace = 5 * time.Second
	}
	select {
	case <-stillRunning:
		// Handler finished after the deadline — clear outstanding; no restart.
		if h.dispatchWatch != nil {
			h.dispatchWatch.Done()
		}
		return
	case <-time.After(grace):
		h.completeStuckDispatch(fmt.Sprintf("dispatch stuck after deadline: %s device=%s", tool, device))
	}
}

// completeStuckDispatch force-stalls daemon-self while the dispatch is still
// outstanding, then requests supervised exit. Intentionally does not call
// Done: Done after ForceStall would RecoverySucceeded and wipe needs_attention
// before the dump/exit path runs.
func (h *Handler) completeStuckDispatch(reason string) {
	if h.dispatchWatch != nil {
		h.dispatchWatch.ForceStall(reason)
	}
	if h.selfRestart != nil {
		h.selfRestart.Request(reason)
	}
}

// InFlightOps returns a snapshot of tool calls still running (🎯T99.5).
func (h *Handler) InFlightOps() []InFlightOp {
	if h == nil || h.ops == nil {
		return nil
	}
	return h.ops.snapshot()
}

// invalidateOnTimeout drops cached device sessions after a dispatch timeout
// so the next call re-resolves rather than reusing a wedged connection.
func (h *Handler) invalidateOnTimeout(dev string) {
	if dev == "" {
		return
	}
	if h.onDeviceTimeout != nil {
		h.onDeviceTimeout(dev)
		return
	}
	// Best-effort: resolve alias → UDID under lock, then invalidate iOS cache.
	h.mu.Lock()
	adapter, _, id, err := h.resolveAdapter(dev)
	h.mu.Unlock()
	if err != nil || id == "" {
		return
	}
	type invalidator interface {
		InvalidateDevice(udid string)
	}
	if inv, ok := adapter.(invalidator); ok {
		inv.InvalidateDevice(id)
	}
}

// slowDispatchThreshold is the in-flight age at which a dispatch
// first qualifies as "slow" and gets a watchdog log line. Tuned to
// the iOS deploy_app pipeline: a fresh tunnel handshake + signed
// install can legitimately take ~10–20 s on a cold device, so 30 s
// is the floor for "this is taking longer than it should."
//
// slowDispatchInterval is the periodic re-log cadence after the
// first slow event. Each fire repeats the in-flight age so an
// operator scanning the log can see a hang's wall-clock duration
// directly from the most recent line.
//
// Declared as vars (not consts) so tests can shorten them.
var (
	slowDispatchThreshold = 30 * time.Second
	slowDispatchInterval  = 60 * time.Second
)

// watchSlowDispatch logs a warning if a dispatch is still in flight
// after slowDispatchThreshold, then re-logs every slowDispatchInterval
// until done is closed. Returns immediately if done fires before
// the threshold — the common (fast) case adds one goroutine
// allocation and one select wakeup, no log noise.
// cancelAtDeadline, when non-nil, is invoked when the slow threshold fires
// so watchSlowDispatch can escalate from logging to cancelling (🎯T99.1).
// The primary cancel path is context.WithTimeout; this is an additional
// log-visible escalation that re-invokes cancel (idempotent).
func watchSlowDispatch(tool, device string, started time.Time, done <-chan struct{}, cancel context.CancelFunc) {
	select {
	case <-done:
		return
	case <-time.After(slowDispatchThreshold):
	}
	slog.Warn("mcp dispatch slow",
		"tool", tool, "device", device,
		"in_flight_ms", time.Since(started).Milliseconds())
	ticker := time.NewTicker(slowDispatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			slog.Warn("mcp dispatch still in flight",
				"tool", tool, "device", device,
				"in_flight_ms", time.Since(started).Milliseconds())
			if cancel != nil {
				cancel()
			}
		}
	}
}

// deviceArg pulls the "device" argument for logging context if present.
// The mcp tools convention is that device-scoped tools take a "device"
// key — logging it lets the trail be correlated per-device.
func deviceArg(args map[string]any) string {
	if v, ok := args["device"].(string); ok {
		return v
	}
	return ""
}

func (h *Handler) dispatch(name string, args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.testHandlers != nil {
		if fn, ok := h.testHandlers[name]; ok {
			return fn(args)
		}
	}
	fn, ok := h.toolHandlers()[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	return fn(args)
}

// toolHandlers is the verb table — the single source of truth for both
// MCP dispatch and the app_exec Starlark builtin bridge (🎯T88), so the
// two surfaces can never drift. Keep entries in the dispatch-group order
// the definitions use.
func (h *Handler) toolHandlers() map[string]toolFunc {
	return map[string]toolFunc{
		"devices":       h.handleDevices,
		"resolve":       h.handleResolve,
		"device_state":  h.handleDeviceState,
		"screenshot":    h.handleScreenshot,
		"list_apps":     h.handleListApps,
		"launch_app":    h.handleLaunchApp,
		"terminate_app": h.handleTerminateApp,
		"install_app":   h.handleInstallApp,
		"uninstall_app": h.handleUninstallApp,
		"deploy_app":    h.handleDeployApp,
		"launch_player": h.handleLaunchPlayer,
		"reserve":       h.handleReserve,
		"release":       h.handleRelease,
		"renew":         h.handleRenew,
		"reservations":  h.handleReservations,
		"runs_list":     h.handleRunsList,
		"runs_show":     h.handleRunsShow,
		"rotate":        h.handleRotate,
		"crashes":       h.handleCrashes,
		// --- simulator tools ---
		"sim_list":     h.handleSimList,
		"sim_create":   h.handleSimCreate,
		"sim_boot":     h.handleSimBoot,
		"sim_shutdown": h.handleSimShutdown,
		"sim_delete":   h.handleSimDelete,
		// --- emulator tools ---
		"emu_list":     h.handleEmuList,
		"emu_create":   h.handleEmuCreate,
		"emu_boot":     h.handleEmuBoot,
		"emu_shutdown": h.handleEmuShutdown,
		"emu_delete":   h.handleEmuDelete,
		// --- visual regression tools ---
		"baseline_update": h.handleBaselineUpdate,
		"diff":            h.handleDiff,
		"baselines_list":  h.handleBaselinesList,
		"record_start":    h.handleRecordStart,
		"record_stop":     h.handleRecordStop,
		"network":         h.handleNetwork,
		"logs":            h.handleLogsRange,
		// --- log-capture sessions ---
		"log_capture_start": h.handleLogCaptureStart,
		"log_capture_get":   h.handleLogCaptureGet,
		"log_capture_stop":  h.handleLogCaptureStop,
		"log_capture_list":  h.handleLogCaptureList,
		// --- bidirectional app channel (🎯T75, 🎯T83) ---
		"app_channel_stop":        h.handleAppChannelStop,
		"app_channel_list":        h.handleAppChannelList,
		"app_ping":                h.handleAppPing,
		"app_quit":                h.handleAppQuit,
		"app_flush":               h.handleAppFlush,
		"app_background":          h.handleAppBackground,
		"app_foreground":          h.handleAppForeground,
		"app_low_memory":          h.handleAppLowMemory,
		"app_pause":               h.handleAppPause,
		"app_resume":              h.handleAppResume,
		"app_step":                h.handleAppStep,
		"app_speed":               h.handleAppSpeed,
		"app_input":               h.handleAppInput,
		"app_state":               h.handleAppState,
		"app_tweak_list":          h.handleAppTweakList,
		"app_tweak_get":           h.handleAppTweakGet,
		"app_tweak_set":           h.handleAppTweakSet,
		"app_tweak_reset":         h.handleAppTweakReset,
		"app_spawn":               h.handleAppSpawn,
		"app_acquire":             h.handleAppAcquire,
		"app_release":             h.handleAppRelease,
		"games":                   h.handleGames,
		"app_save_state":          h.handleAppSaveState,
		"app_restore_state":       h.handleAppRestoreState,
		"app_screenshot":          h.handleAppScreenshot,
		"app_state_slices":        h.handleAppStateSlices,
		"app_state_describe":      h.handleAppStateDescribe,
		"app_state_capture_start": h.handleAppStateCaptureStart,
		"app_state_capture_get":   h.handleAppStateCaptureGet,
		"app_state_capture_stop":  h.handleAppStateCaptureStop,
		"app_state_capture_list":  h.handleAppStateCaptureList,
		"app_log_get":             h.handleAppLogGet,
		"app_perf_get":            h.handleAppPerfGet,
		"is_running":              h.handleIsRunning,
		// --- pool tools (🎯T24) ---
		"pool_list":  h.handlePoolList,
		"pool_warm":  h.handlePoolWarm,
		"pool_drain": h.handlePoolDrain,
		"pool_gc":    h.handlePoolGC,
		// --- scripting entry point (🎯T88) ---
		"app_exec":     h.handleAppExec,
		"list_scripts": h.handleListScripts,
		"run_script":   h.handleRunScript,
	}
}

// Definitions returns the complete MCP tool definition list — core tools
// plus visual-regression tools plus log-capture-session tools.
func Definitions() []mcpgo.Tool {
	// 🎯T88.3: app_exec is spyder's single MCP entry point. Every former
	// one-off tool is reachable as a Starlark builtin inside app_exec (see
	// toolHandlers — the same verb table dispatch uses). The definition
	// builders below are retained as the per-verb arg-schema reference
	// (consumed by the parity test, and available for in-script help) but
	// are no longer advertised on the wire.
	return []mcpgo.Tool{appExecDefinition()}
}

// legacyDefinitions is the union of the former one-off tool schemas, no
// longer advertised (🎯T88.3) but retained as the verb-argument reference
// and parity oracle for the app_exec builtin surface.
func legacyDefinitions() []mcpgo.Tool {
	defs := append(allBaseDefinitions(), visualDefinitions()...)
	defs = append(defs, logCaptureDefinitions()...)
	return append(defs, appChannelDefinitions()...)
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
			mcpgo.WithDescription("Resolve a symbolic device name (e.g. 'iPad') to its platform-specific UUIDs for use with xcodebuild, devicectl, or adb. Supply exactly one of `name` (alias / raw UUID) or `selector` (JSON predicate, same grammar as `reserve`'s selector). With `selector`, returns the inventory entry of the first matching live device. (🎯T38.3)"),
			mcpgo.WithString("name",
				mcpgo.Description("Symbolic name or raw UUID from the device inventory (mutually exclusive with selector)"),
			),
			mcpgo.WithString("selector",
				mcpgo.Description("JSON selector predicate (mutually exclusive with name). Same grammar as the `reserve` selector argument."),
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
			mcpgo.WithDescription("Capture a PNG screenshot of the device. By default returns the image inline for the agent to inspect; pass path to instead save the PNG to that file and return a text confirmation (no inline image). iOS uses the in-process go-ios DTX `ScreenshotService` over lockdown (requires the bundled tunnel for iOS-17+; iOS ≤16 uses lockdown directly and needs the Developer Disk Image mounted — open the device once in Xcode or `ios image auto <udid>`); Android uses adb shell screencap. Read-only; not subject to reservations — any session may screenshot any device. Pass owner to archive the PNG into the active run."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Owner identity; when present and a run is active, the screenshot is archived into the run."),
			),
			mcpgo.WithString("path",
				mcpgo.Description("Optional output file path. When set, the PNG is written here (a leading ~ is expanded; parent directories are created) and the tool returns a text confirmation instead of the inline image. Independent of owner/run archival."),
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
			mcpgo.WithDescription("Foreground an app by bundle id. iOS-17+ uses the in-process go-ios `appservice` launch (CoreDevice/RemoteXPC, requires the bundled tunnel); iOS ≤16 uses go-ios's `instruments.ProcessControl` (DTX-over-lockdown, no tunnel required but needs the Developer Disk Image mounted — open the device once in Xcode or `ios image auto <udid>`). Path selection is automatic per device. Android uses adb monkey with the LAUNCHER intent (or `am start` when env is supplied); iOS simulators use `xcrun simctl launch`. Strictly enforced: rejects if the device is reserved by a different owner.\n\nOptional `env` map sets environment variables for the launched process. On iOS the values become the process environment (readable via `getenv()`); on iOS simulators they're forwarded via `SIMCTL_CHILD_<KEY>=<VALUE>`; on Android they're passed as Intent string-extras and the app's Java/Kotlin shim must extract them and call `setenv()` before native code runs (see agents-guide.md for the shim pattern). The conventional key for dev-time network logging is `SPYDER_APP_CHANNEL=host:port` — apps that opt in install a TCP log sink targeting that address."),
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
			mcpgo.WithObject("env",
				mcpgo.Description("Optional environment variables to inject into the launched app process. Keys and values are strings (non-string values are stringified). Convention: `SPYDER_APP_CHANNEL=host:port` enables the dev-time TCP log sink in apps that support it."),
			),
		),

		mcpgo.NewTool("is_running",
			mcpgo.WithDescription("Report whether an app is currently running on the device, without forcing a launch. Returns JSON {state, pid?} where state ∈ {running, not_running, not_installed}. Distinct from device_state.foreground_app (only sees the foreground app, not backgrounded ones) and from launch_app's PID-verify (which would force a launch). iOS uses the in-process go-ios DTX `processcontrol` service for bundle→pid; Android uses adb shell pidof, with a list_apps cross-check to distinguish not_running from not_installed. Read-only; not subject to reservations. (🎯T38.1)"),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("bundle_id",
				mcpgo.Required(),
				mcpgo.Description("App bundle identifier (e.g. com.example.app)"),
			),
		),

		mcpgo.NewTool("terminate_app",
			mcpgo.WithDescription("Terminate a running app by bundle id. iOS resolves the PID then kills it: iOS-17+ via go-ios's `appservice.KillProcess` (requires the bundled tunnel); iOS ≤16 via `instruments.ProcessControl.KillProcess` (DTX-over-lockdown, no tunnel required, needs the Developer Disk Image mounted). Path selection is automatic per device. Android uses adb am force-stop. Strictly enforced: rejects if the device is reserved by a different owner."),
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

		mcpgo.NewTool("launch_player",
			mcpgo.WithDescription("Launch the spyder stream player on a device with only device + optional server name (🎯T100.3). Injects STREAM_ADDR/stream_addr and SERVER_NAME/server_name automatically (no agent-set env). Picks the sole registered stream server when server is omitted; errors if zero or multiple. Optional path overrides the platform player artifact."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID (or desktop inventory entry)"),
			),
			mcpgo.WithString("server",
				mcpgo.Description("Stream catalogue server name (e.g. tiltbuggy). Optional when exactly one server is registered."),
			),
			mcpgo.WithString("path",
				mcpgo.Description("Optional override path to Player.app / APK / bin/player"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner for strict enforcement"),
			),
		),
		mcpgo.NewTool("deploy_app",
			mcpgo.WithDescription("Atomic deploy helper: terminate → install → launch → verify-new-pid. Returns {bundle_id, pid} on success. Fails fast if install fails. 'Not running' errors from the terminate step are ignored (app may not be running yet). The bundle_id is derived automatically from the .app Info.plist (iOS) or via aapt dump badging (Android); pass bundle_id explicitly to skip derivation. Requires tunneld on iOS (for launch + pid-verify via DVT). Strictly enforced: rejects if the device is reserved by a different owner.\n\nRefuses the spyder stream player (com.spyder.player / Player*.app / player Android APK) — use launch_player so STREAM_ADDR and server name are injected.\n\nOptional `env` map is forwarded to the launch step — see `launch_app` for semantics."),
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
			mcpgo.WithObject("env",
				mcpgo.Description("Optional environment variables to inject into the launched app process. Same semantics as `launch_app`'s `env`. Convention: `SPYDER_APP_CHANNEL=host:port` enables the dev-time TCP log sink."),
			),
		),

		mcpgo.NewTool("reserve",
			mcpgo.WithDescription("Acquire an exclusive reservation on a device so parallel sessions won't interrupt mutating operations (screenshot, launch/terminate). Default TTL is 3600s, max 86400s. Same-owner re-acquires renew in place.\n\nSupply exactly one of device (literal pin) or selector (fuzzy match). The selector is a JSON object with optional fields: platform (required within selector), model_family, os_min, os_max, orientation_capable, tags, attrs. Example: {\"platform\":\"ios\",\"model_family\":\"ipad\"}. The server resolves the selector against live devices and inventory, preferring idle physical devices over sims/emus, and returns a reservation bound to a concrete UUID — the caller never needs to know which device was picked."),
			mcpgo.WithString("device",
				mcpgo.Description("Device alias or UUID (literal pin; mutually exclusive with selector)"),
			),
			mcpgo.WithString("selector",
				mcpgo.Description("JSON selector predicate for fuzzy device matching (mutually exclusive with device). Fields: platform (required), model_family, os_min, os_max, orientation_capable, tags (array), attrs (object). Example: {\"platform\":\"ios\",\"model_family\":\"ipad\"}"),
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
			mcpgo.WithDescription("Fetch crash reports from a device. iOS pulls .ips files via the in-process go-ios `crashreport` service and parses the first-line JSON header for process, reason, and timestamp. Android attempts tombstones via adb pull /data/tombstones/ (requires root) and falls back to `adb logcat -b crash`. `since` accepts either an RFC3339 absolute timestamp or a Go duration relative to now (e.g. `-15m`, `-1h`). To filter by app, pass `bundle_id` (resolved to the iOS `CFBundleExecutable` server-side) or `process` (the raw image-name filter) — not both. Read-only; not reservation-gated. Pass owner to archive reports into the active run."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("since",
				mcpgo.Description("Return only reports newer than this point. RFC3339 absolute (e.g. `2026-04-19T00:00:00Z`); Go duration relative to now (e.g. `-15m`, `-1h`); or the literal `launch`, which resolves to the timestamp of the most recent `launch_app` call for the given `bundle_id` on this device. `since=launch` requires `bundle_id`. Omit to return all available reports."),
			),
			mcpgo.WithString("process",
				mcpgo.Description("Filter by process name (case-insensitive). Mutually exclusive with `bundle_id`. Omit both to return crashes from all processes."),
			),
			mcpgo.WithString("bundle_id",
				mcpgo.Description("Filter by app bundle id. The server resolves to the iOS `CFBundleExecutable` (or Android package name) before filtering. Mutually exclusive with `process`."),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Reservation owner; when present and a run is active, crash report content is archived into the run."),
			),
		),

		mcpgo.NewTool("record_start",
			mcpgo.WithDescription("Start a screen recording on an iOS simulator or Android device/emulator. Returns immediately; the recording runs in the background until record_stop is called. iOS physical devices are not supported — use a simulator (xcrun simctl list devices). Observational; not subject to device reservations — any session may record any device. Only one recording per device at a time; a second record_start on the same device fails until record_stop is called. The owner that starts a recording is the only owner that can stop it."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UDID/serial. For iOS simulators pass the simulator UDID from `xcrun simctl list devices`."),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Owner identity that will own the recording session. Required to later stop it; pass the same value to record_stop."),
			),
		),

		mcpgo.NewTool("record_stop",
			mcpgo.WithDescription("Stop the active screen recording on a device, finalise the mp4, and return the path to the recorded file. Must be called after record_start. Owner must match the owner that started the recording (independent of any device reservation)."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UDID/serial (must match the value passed to record_start)"),
			),
			mcpgo.WithString("owner",
				mcpgo.Description("Owner identity that started the recording (must match the value passed to record_start)"),
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
				"iOS uses the in-process go-ios `syslog` service; Android uses adb logcat. "+
				"`since` and `until` each accept either an RFC3339 absolute timestamp "+
				"(e.g. `2026-05-17T16:43:24Z`) or a Go duration relative to now "+
				"(e.g. `since=-2m` for \"the last two minutes\", `until=+30s`, `until=now`). "+
				"`since=launch` is shorthand for \"everything since spyder last launched the app named by `bundle_id`\". "+
				"To filter by app, pass `bundle_id` (resolved to the iOS `CFBundleExecutable` "+
				"server-side); `process` is the raw image-name filter for callers who already "+
				"know it. Specify one or the other, not both. "+
				"For live streaming (--follow), use the REST SSE endpoint POST /api/v1/log_stream instead — "+
				"MCP transport does not support streaming. Read-only."),
			mcpgo.WithString("device",
				mcpgo.Required(),
				mcpgo.Description("Device alias or UUID"),
			),
			mcpgo.WithString("since",
				mcpgo.Description("Start of the window. RFC3339 absolute (e.g. `2026-04-19T14:00:00Z`); Go duration relative to now (e.g. `-2m`, `now`); or the literal `launch`, which resolves to the timestamp of the most recent `launch_app` call for the given `bundle_id` on this device in this daemon's lifetime. `since=launch` requires `bundle_id`. Defaults to recent output."),
			),
			mcpgo.WithString("until",
				mcpgo.Description("End of the window. RFC3339 absolute or Go duration relative to now (e.g. `now`, `+30s`). Defaults to now."),
			),
			mcpgo.WithString("process",
				mcpgo.Description("Filter by process name (iOS image_name; Android tag/process contains match). Mutually exclusive with `bundle_id`."),
			),
			mcpgo.WithString("bundle_id",
				mcpgo.Description("Filter by app bundle id (e.g. `com.example.app`). The server resolves to the iOS `CFBundleExecutable` (or Android package name) before filtering. Use this when you started the app via `launch_app` and want its logs without having to know the executable name. Mutually exclusive with `process`."),
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

		// ---- sim/emu pool tools ------------------------------------------

		mcpgo.NewTool("pool_list",
			mcpgo.WithDescription("List the current state of all pool templates — how many sim/emu instances are in the available, running, and reserved tiers. Read-only; does not trigger any lifecycle actions."),
		),

		mcpgo.NewTool("pool_warm",
			mcpgo.WithDescription("Force pre-boot N additional instances for a pool template, transitioning them from available to running tier so they are ready for near-instant acquisition."),
			mcpgo.WithString("template",
				mcpgo.Required(),
				mcpgo.Description("Pool template name as declared in ~/.spyder/pool.yaml"),
			),
			mcpgo.WithNumber("count",
				mcpgo.Required(),
				mcpgo.Description("Number of additional instances to pre-boot"),
			),
		),

		mcpgo.NewTool("pool_drain",
			mcpgo.WithDescription("Shut down and delete all idle (available and running) instances for a pool template. Reserved instances are force-terminated first. Use this to reclaim disk/memory, then pool_warm to refill."),
			mcpgo.WithString("template",
				mcpgo.Required(),
				mcpgo.Description("Pool template name as declared in ~/.spyder/pool.yaml"),
			),
		),

		mcpgo.NewTool("pool_gc",
			mcpgo.WithDescription("Delete orphaned spyder-pool-* simulators and AVDs that the daemon no longer tracks (typically left over from prior daemon runs that crashed or restarted before this version). Booted orphans are skipped on the assumption they may be in active use; shut them down and re-run if you want them gone too. Returns the list of deleted and skipped names."),
		),
	}
}
