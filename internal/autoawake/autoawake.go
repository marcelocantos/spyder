// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package autoawake keeps attached iOS devices awake by filling the
// "no app is foregrounded" gap with a tiny companion app (KeepAwake).
// iOS keeps the screen on while any third-party app is foregrounded;
// the supervisor's only job is to ensure that when the home screen
// would otherwise be the active surface, KeepAwake takes its place.
// When the user (or spyder, on the user's behalf) is running a
// different app, the supervisor stays out of the way.
//
// The convergence rule (🎯T55, redesigned 2026-05-09):
//
//  1. Probe the device's foreground app via the bridge.
//  2. If the foreground app is KeepAwake → classConverged. Proactively
//     clear userOptOut (KeepAwake running is a clear "I want this"
//     signal, regardless of how it got there).
//  3. If the foreground app is anything else (a spyder-deployed app,
//     a user-launched app, …) → classOtherAppForegrounded. Stay
//     passive — that app is already keeping the screen on, and
//     relaunching KeepAwake would clobber it.
//  4. If no third-party app is foregrounded (SpringBoard / home
//     screen):
//     - If the previous tick had KeepAwake foregrounded, this is the
//     user swiping from KeepAwake to home → set userOptOut.
//     - If userOptOut is set → classUserOptOut, stay passive until
//     the user re-foregrounds KeepAwake (which clears the flag) or
//     unplugs/replugs the device (which prunes the obs entry).
//     - Otherwise: run staleness check (uninstall + reinstall on
//     version drift), install KeepAwake if missing, launch.
//
// Loop scaffolding:
//
//  1. Every pollInterval, enumerate connected iOS devices via
//     IOSAdapter.List. New devices kick off an immediate convergence
//     step in a goroutine; departed devices have their alerts dismissed
//     and observation state pruned (which clears any opt-out — replug
//     is a fresh slate).
//  2. Every convergeInterval, run convergence on every still-present
//     device. This catches user-side resolutions of human gates AND
//     user-driven foreground transitions.
//
// Errors during launch classify into the existing error tree:
//   - locked: fire idempotent macOS alert; next tick may succeed once
//     the user unlocks.
//   - needs-trust: fire idempotent macOS alert; next tick's launch
//     succeeds once the user trusts the cert.
//   - needs-developer-mode / needs-xcode-signin: log on transition,
//     no alert (the user must take action that requires their
//     attention anyway).
//   - other: log + retry next tick.
//
// Idempotency: log lines and macOS alerts are emitted only on
// transition between observation classes, not every tick. So the same
// "needs trust" state across many ticks emits one log line and holds
// one alert.
package autoawake

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/notify"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// OwnerID is the reservation owner identity used by autoawake.
// Exported so callers can reference it when checking reservations.
const OwnerID = "autoawake"

const (
	// pollInterval is the cadence for the cheap enumeration tick that
	// detects newly-attached / departed devices. New devices get an
	// immediate convergence run; existing devices wait for the next
	// convergeInterval.
	pollInterval = 2 * time.Second

	// convergeInterval is the cadence for the full per-device
	// convergence sweep. Every tick re-observes every present device,
	// catching user-side resolutions of trust / lock / developer-mode
	// gates. A 15 s ceiling on user-perceived latency is fine: trust
	// the cert, wait < 15 s, KeepAwake foregrounds.
	convergeInterval = 15 * time.Second
)

// errorClass is the post-classification observation we record per
// device. Convergence emits log lines and dispatches alerts only on
// CHANGE between successive observations of the same device, so the
// device sitting in needs-trust for hours produces one alert, not 240.
type errorClass int

const (
	classUnknown errorClass = iota
	classConverged
	classUserOptOut
	classOtherAppForegrounded
	classLocked
	classNeedsTrust
	classNeedsDeveloperMode
	classNeedsXcodeSignin
	classNotInstalled
	classStaleInstall
	classStaleBuild
	classOther
)

func (c errorClass) String() string {
	switch c {
	case classConverged:
		return "converged"
	case classUserOptOut:
		return "user-opt-out"
	case classOtherAppForegrounded:
		return "other-app-foregrounded"
	case classLocked:
		return "locked"
	case classNeedsTrust:
		return "needs-trust"
	case classNeedsDeveloperMode:
		return "needs-developer-mode"
	case classNeedsXcodeSignin:
		return "needs-xcode-signin"
	case classNotInstalled:
		return "not-installed"
	case classStaleInstall:
		return "stale-install"
	case classStaleBuild:
		return "stale-build"
	case classOther:
		return "other"
	default:
		return "unknown"
	}
}

// deviceObs is the per-device observation record carried between
// convergence ticks. The mu-protected fields are read-write; the rest
// are written only on transitions.
type deviceObs struct {
	// lastClass is the most recent error classification for this
	// device. Convergence transitions only emit log/alert side-effects
	// when the class changes.
	lastClass errorClass

	// lastForeground is the bundle id (or .app folder name) reported
	// by the bridge's foreground_app probe on the previous tick. Empty
	// string means SpringBoard / nothing user-facing was foregrounded.
	// Used to detect the user's opt-out gesture: a transition from
	// "KeepAwake foregrounded" to "" (home screen) is the user
	// swiping from KeepAwake to home, which sets userOptOut.
	// Transitions to a different third-party app do NOT set opt-out —
	// the user task-switched, and KeepAwake should fill the gap again
	// when that app is closed.
	lastForeground string

	// userOptOut is set when the user has expressed they don't want
	// autoawake to fight: by swiping from KeepAwake to the home
	// screen. Cleared whenever KeepAwake is observed as the
	// foreground app (any tick, not just edges — the user's "I want
	// KA back" gesture is unambiguous regardless of how KA got there).
	// While set, autoawake observes but does not act — it stays
	// passive even if iOS later kills KeepAwake outright.
	userOptOut bool

	// lockAlertGroup non-empty when a macOS lock alert is currently
	// displayed for this device. Cleared when the alert is dismissed.
	lockAlertGroup string

	// trustAlertGroup separate so we can dismiss independently.
	trustAlertGroup string
}

// iosAdapter is the subset of *device.IOSAdapter used by the convergence
// loop. Extracted as an interface so tests can inject a fake without
// starting real devicectl processes.
type iosAdapter interface {
	List() ([]device.Info, error)
	ForegroundApp(id string) (string, error)
	KeepAwakeInstalled(id string) (bool, error)
	KeepAwakeInstalledVersion(id string) (string, error)
	LaunchKeepAwake(id string) error
	UninstallApp(id, bundleID string) error
}

// Supervisor runs the convergence loop. Construct via New; call Run in
// a goroutine for the lifetime of the daemon.
type Supervisor struct {
	ios          iosAdapter
	inventory    *inventory.Store
	reservations *reservations.Store

	mu sync.Mutex
	// obs is the per-device observation record. Devices in this map are
	// "currently known to be present" (or were on the most recent
	// poll). Removed entries dismiss their alerts.
	obs map[string]*deviceObs
	// inFlight serialises convergence steps per device so a slow tick
	// for a given device doesn't overlap with the next one.
	inFlight map[string]bool
}

type Option func(*Supervisor)

func WithReservations(s *reservations.Store) Option {
	return func(sv *Supervisor) { sv.reservations = s }
}

// withIOSAdapter replaces the default IOSAdapter with the given
// implementation. Intentionally unexported; test packages use it via
// the autoawake_test build tag to inject fakes.
func withIOSAdapter(a iosAdapter) Option {
	return func(sv *Supervisor) { sv.ios = a }
}

// New creates a new Supervisor. The convergence loop uses
// device.IOSAdapter (in-process go-ios) and doesn't depend on any
// other subprocess.
func New(opts ...Option) *Supervisor {
	sv := &Supervisor{
		ios:       device.NewIOSAdapter(),
		inventory: inventory.New(),
		obs:       map[string]*deviceObs{},
		inFlight:  map[string]bool{},
	}
	for _, opt := range opts {
		opt(sv)
	}
	return sv
}

// Run drives the convergence loop until ctx is cancelled. Two tickers:
// pollInterval for cheap enumeration, convergeInterval for the full
// per-device sweep.
func (s *Supervisor) Run(ctx context.Context) {
	if ctx.Err() != nil {
		// Pre-cancelled context: don't even start the initial poll
		// (which can take seconds on a fresh CI runner where devicectl
		// hasn't warmed up yet). Tests rely on this: a cancelled-from-
		// the-start ctx means the supervisor should be a no-op.
		return
	}
	slog.Info("autoawake: convergence supervisor started",
		"poll", pollInterval, "converge", convergeInterval)

	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()
	convTicker := time.NewTicker(convergeInterval)
	defer convTicker.Stop()

	s.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			s.dismissAllAlerts()
			return
		case <-pollTicker.C:
			s.poll(ctx)
		case <-convTicker.C:
			s.convergeAll(ctx)
		}
	}
}

// poll enumerates connected devices, kicks off a convergence step for
// each newly-detected device, and prunes departed devices from the
// observation map.
func (s *Supervisor) poll(ctx context.Context) {
	devices, err := s.ios.List()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		slog.Warn("autoawake: enumerate devices failed", "error", summariseErr(err))
		return
	}

	seen := make(map[string]struct{}, len(devices))
	for _, d := range devices {
		seen[d.UUID] = struct{}{}
	}

	s.mu.Lock()
	var fresh []string
	for udid := range seen {
		if _, known := s.obs[udid]; !known {
			s.obs[udid] = &deviceObs{lastClass: classUnknown}
			fresh = append(fresh, udid)
		}
	}
	var gone []string
	for udid := range s.obs {
		if _, still := seen[udid]; !still {
			gone = append(gone, udid)
		}
	}
	s.mu.Unlock()

	for _, udid := range gone {
		s.removeDevice(udid)
	}
	for _, udid := range fresh {
		alias := s.aliasOf(udid)
		slog.Info("autoawake: device attached", "udid", udid, "alias", alias)
		go s.converge(ctx, udid)
	}
}

// convergeAll runs a convergence step for every currently-known device.
// Called on the convergeInterval tick to re-observe state and detect
// user-side resolution of human gates.
func (s *Supervisor) convergeAll(ctx context.Context) {
	s.mu.Lock()
	udids := make([]string, 0, len(s.obs))
	for udid := range s.obs {
		udids = append(udids, udid)
	}
	s.mu.Unlock()

	for _, udid := range udids {
		go s.converge(ctx, udid)
	}
}

// converge observes the current foreground app and drives the device
// toward "any third-party app foregrounded". KeepAwake fills the gap
// only when nothing else is foregrounded and the user hasn't opted
// out. Errors classify into errorClass values; transitions between
// classes emit log/alert side-effects via advance().
func (s *Supervisor) converge(ctx context.Context, udid string) {
	if ctx.Err() != nil {
		return
	}
	if !s.beginInFlight(udid) {
		return
	}
	defer s.endInFlight(udid)

	alias := s.aliasOf(udid)

	if s.reservations != nil {
		if err := s.reservations.Authorize(udid, OwnerID); err != nil {
			slog.Debug("autoawake: skipping device held by another owner",
				"udid", udid, "alias", alias, "owner", err.Error())
			return
		}
	}

	// 1) Probe foreground app + update obs. recordForeground applies
	// the opt-out transition rules and returns the in-effect
	// userOptOut flag.
	fg, err := s.ios.ForegroundApp(udid)
	if err != nil {
		slog.Debug("autoawake: ForegroundApp probe failed; skipping tick",
			"udid", udid, "alias", alias, "error", summariseErr(err))
		return
	}
	optedOut := s.recordForeground(udid, fg)

	// 2) Some other third-party app is foregrounded — a spyder-deployed
	// app under test, or anything the user task-switched to themselves.
	// That app is keeping the screen on; relaunching KeepAwake would
	// clobber it. Stay passive — staleness redeploy and KA launch are
	// both skipped here on purpose.
	if fg != "" && !device.IsKeepAwakeForeground(fg) {
		s.advance(udid, alias, classOtherAppForegrounded, nil)
		return
	}

	// 3) SpringBoard / home screen with userOptOut → stay passive. A
	// fresh observation with no captured transition has no opt-out
	// signal and falls through to staleness/install/launch — unplug →
	// replug is the reset gesture for that case. (When KA is
	// foregrounded, recordForeground has already cleared opt-out, so
	// this check only fires when fg=="".)
	if optedOut {
		s.advance(udid, alias, classUserOptOut, nil)
		return
	}

	// 4) Staleness check (🎯T47): when the installed bundle's
	// CFBundleShortVersionString differs from the source-of-truth
	// MARKETING_VERSION baked into the bundled pbxproj, uninstall,
	// rebuild, reinstall, and relaunch. Runs in both the KA-foreground
	// and empty-foreground cases — never the foreign-app case (gated
	// above), so a redeploy can never disturb an app under test.
	if expected, verr := device.ExpectedKeepAwakeVersion(); verr == nil && expected != "" {
		if installed, ierr := s.ios.KeepAwakeInstalledVersion(udid); ierr == nil &&
			installed != "" && installed != expected {
			slog.Info("autoawake: KeepAwake version drift; uninstalling to redeploy",
				"udid", udid, "alias", alias,
				"installed", installed, "expected", expected)
			s.attemptReinstall(ctx, udid, alias, classStaleBuild)
			return
		}
	}

	// 5) KeepAwake is the foreground app and version is current →
	// converged. No launch needed.
	if device.IsKeepAwakeForeground(fg) {
		s.advance(udid, alias, classConverged, nil)
		return
	}

	// 6) Foreground is empty and version is current. Install if missing,
	// then launch. LaunchKeepAwake foregrounds a backgrounded KA and
	// starts a terminated one — both converge to the foreground.
	// attemptInstall handles its own classification on failure; on
	// success we fall through to launch.
	installed, err := s.ios.KeepAwakeInstalled(udid)
	if err != nil {
		slog.Debug("autoawake: KeepAwakeInstalled probe failed; assuming installed",
			"udid", udid, "alias", alias, "error", summariseErr(err))
		installed = true
	}
	if !installed {
		if !s.attemptInstall(ctx, udid, alias) {
			return
		}
	}

	launchErr := s.ios.LaunchKeepAwake(udid)
	if launchErr == nil {
		s.advance(udid, alias, classConverged, nil)
		return
	}
	switch {
	case errors.Is(launchErr, device.ErrLocked):
		s.advance(udid, alias, classLocked, launchErr)
	case errors.Is(launchErr, device.ErrTrustNotGranted):
		s.advance(udid, alias, classNeedsTrust, launchErr)
	case errors.Is(launchErr, device.ErrKeepAwakeNotInstalled):
		// Race: app got uninstalled between observe and launch.
		// Next tick's installed-probe will trigger reinstall.
		s.advance(udid, alias, classNotInstalled, launchErr)
	case errors.Is(launchErr, device.ErrNoProviderFound):
		// Stale provisioning profile: uninstall the corrupt copy so the
		// next tick's install-probe triggers a fresh install + launch.
		s.attemptReinstall(ctx, udid, alias, classStaleInstall)
	default:
		s.advance(udid, alias, classOther, launchErr)
	}
}

// recordForeground updates the per-device foreground record and
// applies the opt-out transition rules. Returns the in-effect
// userOptOut flag for use by the caller.
//
// Rules:
//
//	any           → KA          clear userOptOut (proactive: KA running
//	                             is an unambiguous "I want this" signal,
//	                             not just an edge — every observation
//	                             where KA is foreground clears it).
//	KA            → ""          set userOptOut (user swiped from
//	                             KeepAwake back to the home screen).
//	KA            → other       no change (user task-switched to a
//	                             different app; KeepAwake should fill
//	                             the gap again when they close it).
//	other         → ""          no change (the other app's user closed
//	                             it; KeepAwake fills the gap normally).
//	other         → other'      no change (passive while another app
//	                             holds the foreground).
//
// "" means SpringBoard / home screen / no foreground app.
func (s *Supervisor) recordForeground(udid, curr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	obs, ok := s.obs[udid]
	if !ok {
		return false
	}
	prev := obs.lastForeground
	obs.lastForeground = curr
	switch {
	case device.IsKeepAwakeForeground(curr):
		obs.userOptOut = false
	case curr == "" && device.IsKeepAwakeForeground(prev):
		obs.userOptOut = true
	}
	return obs.userOptOut
}

// attemptInstall runs the transparent install cycle (🎯T32) for a
// device whose KeepAwake is missing. Returns true on success (caller
// should attempt the launch). Returns false when a precondition fails;
// classification + alert dispatch happens before returning.
func (s *Supervisor) attemptInstall(ctx context.Context, udid, alias string) bool {
	if ctx.Err() != nil {
		return false
	}

	team, err := device.DetectCodesigningTeam()
	if err != nil {
		s.advance(udid, alias, classNeedsXcodeSignin, err)
		return false
	}

	if enabled, probeErr := device.DetectDeveloperMode(udid); probeErr == nil && !enabled {
		s.advance(udid, alias, classNeedsDeveloperMode, nil)
		return false
	}

	slog.Info("autoawake: building KeepAwake (cached per daemon lifetime)",
		"team", team, "udid", udid, "alias", alias)
	appPath, err := device.BuildKeepAwake(team)
	if err != nil {
		slog.Warn("autoawake: xcodebuild KeepAwake failed",
			"udid", udid, "alias", alias, "error", summariseErr(err))
		s.advance(udid, alias, classOther, err)
		return false
	}

	slog.Info("autoawake: installing KeepAwake", "udid", udid, "alias", alias)
	if err := device.InstallKeepAwake(udid, appPath); err != nil {
		if errors.Is(err, device.ErrTrustNotGranted) {
			s.advance(udid, alias, classNeedsTrust, err)
			return false
		}
		slog.Warn("autoawake: devicectl install KeepAwake failed",
			"udid", udid, "alias", alias, "error", summariseErr(err))
		s.advance(udid, alias, classOther, err)
		return false
	}
	return true
}

// attemptReinstall drives the uninstall → rebuild → reinstall → launch
// cycle for two recovery paths:
//
//   - **classStaleInstall** (🎯T34): the installed KeepAwake copy has a
//     stale provisioning profile (e.g. a free Personal Team profile
//     expired after 7 days). LaunchKeepAwake returned ErrNoProviderFound.
//   - **classStaleBuild** (🎯T47): the installed bundle's CFBundleShort-
//     VersionString differs from the source pbxproj's MARKETING_VERSION,
//     so a manual version bump should propagate to existing devices.
//
// Both paths share the same recovery sequence; only the entry log line
// and observation class differ. The reason argument names which case
// fired so advance() emits exactly one log line per error session
// regardless of how many ticks we spend here. On repeated failure
// (e.g. uninstall or rebuild also fail) we fall through to classOther,
// which is also idempotent.
func (s *Supervisor) attemptReinstall(ctx context.Context, udid, alias string, reason errorClass) {
	s.advance(udid, alias, reason, nil)

	slog.Info("autoawake: uninstalling KeepAwake to redeploy",
		"udid", udid, "alias", alias, "reason", reason.String())
	if err := s.ios.UninstallApp(udid, device.KeepAwakeBundleID); err != nil {
		slog.Warn("autoawake: uninstall stale KeepAwake failed",
			"udid", udid, "alias", alias, "error", summariseErr(err))
		s.advance(udid, alias, classOther, err)
		return
	}

	// Reset the cached build so the next build fetches a fresh profile.
	device.ResetKeepAwakeBuild()

	// Re-run the full install + launch cycle.
	if !s.attemptInstall(ctx, udid, alias) {
		return
	}
	launchErr := s.ios.LaunchKeepAwake(udid)
	if launchErr == nil {
		s.advance(udid, alias, classConverged, nil)
		return
	}
	// If launch fails again, classify normally — advance() keeps it
	// idempotent so we don't spam on repeated failure.
	switch {
	case errors.Is(launchErr, device.ErrLocked):
		s.advance(udid, alias, classLocked, launchErr)
	case errors.Is(launchErr, device.ErrTrustNotGranted):
		s.advance(udid, alias, classNeedsTrust, launchErr)
	case errors.Is(launchErr, device.ErrKeepAwakeNotInstalled):
		s.advance(udid, alias, classNotInstalled, launchErr)
	default:
		s.advance(udid, alias, classOther, launchErr)
	}
}

// advance transitions the device's observation to a new class. Side-
// effects (log lines, macOS alert dispatch / dismissal) fire only when
// the class actually changes — the same class across ticks produces
// no log noise and no alert duplication.
func (s *Supervisor) advance(udid, alias string, class errorClass, err error) {
	s.mu.Lock()
	obs, ok := s.obs[udid]
	if !ok {
		// Device was pruned out (poll dropped it) between converge
		// scheduling and now. Drop the result silently.
		s.mu.Unlock()
		return
	}
	prev := obs.lastClass
	obs.lastClass = class
	s.mu.Unlock()

	if prev == class {
		// Idempotent: silent re-observation of the same state.
		return
	}

	switch class {
	case classConverged:
		slog.Info("autoawake: KeepAwake foregrounded", "udid", udid, "alias", alias)
		s.dismissAlerts(udid)
	case classUserOptOut:
		slog.Info("autoawake: user dismissed KeepAwake — staying passive until they re-foreground it (or the device is unplugged)",
			"udid", udid, "alias", alias)
		s.dismissAlerts(udid)
	case classLocked:
		s.ensureLockAlert(udid, alias)
		s.dismissTrustAlert(udid)
	case classNeedsTrust:
		s.ensureTrustAlert(udid, alias)
		s.dismissLockAlert(udid)
	case classNeedsDeveloperMode:
		slog.Warn("autoawake: Developer Mode disabled — enable at Settings → Privacy & Security → Developer Mode (device will reboot)",
			"udid", udid, "alias", alias)
		s.dismissAlerts(udid)
	case classNeedsXcodeSignin:
		errMsg := ""
		if err != nil {
			errMsg = summariseErr(err)
		}
		slog.Warn("autoawake: no codesigning identity in keychain — sign in to Xcode → Settings → Accounts with your Apple ID",
			"udid", udid, "alias", alias, "error", errMsg)
		s.dismissAlerts(udid)
	case classNotInstalled:
		slog.Debug("autoawake: KeepAwake not installed; will reinstall next tick",
			"udid", udid, "alias", alias)
		s.dismissAlerts(udid)
	case classStaleInstall:
		slog.Warn("autoawake: KeepAwake has stale provisioning profile; uninstalling and reinstalling",
			"udid", udid, "alias", alias)
		s.dismissAlerts(udid)
	case classStaleBuild:
		slog.Info("autoawake: KeepAwake source-version drift; uninstalling and reinstalling to redeploy",
			"udid", udid, "alias", alias)
		s.dismissAlerts(udid)
	case classOther:
		errMsg := ""
		if err != nil {
			errMsg = summariseErr(err)
		}
		slog.Warn("autoawake: install/launch failed (will retry on next convergence tick)",
			"udid", udid, "alias", alias, "error", errMsg)
		s.dismissAlerts(udid)
	}
}

// removeDevice prunes a departed device from the observation map and
// dismisses any alerts that were active for it.
func (s *Supervisor) removeDevice(udid string) {
	alias := s.aliasOf(udid)
	s.mu.Lock()
	if obs := s.obs[udid]; obs != nil {
		s.dismissAlertsLocked(udid)
	}
	delete(s.obs, udid)
	s.mu.Unlock()
	slog.Info("autoawake: device departed", "udid", udid, "alias", alias)
}

// ── alert dispatch ──────────────────────────────────────────────────────────

func (s *Supervisor) ensureLockAlert(udid, alias string) {
	s.mu.Lock()
	obs := s.obs[udid]
	if obs == nil || obs.lockAlertGroup != "" {
		s.mu.Unlock()
		return
	}
	group := "spyder-lock-" + udid
	obs.lockAlertGroup = group
	s.mu.Unlock()

	slog.Info("autoawake: device locked — alerting user",
		"udid", udid, "alias", alias)
	go func() {
		if err := notify.MacOSAlert("spyder",
			"Unlock "+alias+" to enable keep-awake",
			group); err != nil {
			slog.Warn("autoawake: lock alert failed", "error", err)
		}
	}()
}

func (s *Supervisor) ensureTrustAlert(udid, alias string) {
	s.mu.Lock()
	obs := s.obs[udid]
	if obs == nil || obs.trustAlertGroup != "" {
		s.mu.Unlock()
		return
	}
	group := "spyder-trust-" + udid
	obs.trustAlertGroup = group
	s.mu.Unlock()

	slog.Info("autoawake: developer certificate not trusted — alerting user",
		"udid", udid, "alias", alias)
	go func() {
		if err := notify.MacOSAlert("spyder",
			"Trust the developer profile on "+alias+" to enable keep-awake (Settings → General → VPN & Device Management → tap developer entry → Trust)",
			group); err != nil {
			slog.Warn("autoawake: trust alert failed", "error", err)
		}
	}()
}

func (s *Supervisor) dismissAlerts(udid string) {
	s.mu.Lock()
	s.dismissAlertsLocked(udid)
	s.mu.Unlock()
}

func (s *Supervisor) dismissLockAlert(udid string) {
	s.mu.Lock()
	obs := s.obs[udid]
	if obs == nil || obs.lockAlertGroup == "" {
		s.mu.Unlock()
		return
	}
	group := obs.lockAlertGroup
	obs.lockAlertGroup = ""
	s.mu.Unlock()
	go func() { _ = notify.MacOSAlertRemove(group) }()
}

func (s *Supervisor) dismissTrustAlert(udid string) {
	s.mu.Lock()
	obs := s.obs[udid]
	if obs == nil || obs.trustAlertGroup == "" {
		s.mu.Unlock()
		return
	}
	group := obs.trustAlertGroup
	obs.trustAlertGroup = ""
	s.mu.Unlock()
	go func() { _ = notify.MacOSAlertRemove(group) }()
}

func (s *Supervisor) dismissAlertsLocked(udid string) {
	obs := s.obs[udid]
	if obs == nil {
		return
	}
	if obs.lockAlertGroup != "" {
		group := obs.lockAlertGroup
		obs.lockAlertGroup = ""
		go func() { _ = notify.MacOSAlertRemove(group) }()
	}
	if obs.trustAlertGroup != "" {
		group := obs.trustAlertGroup
		obs.trustAlertGroup = ""
		go func() { _ = notify.MacOSAlertRemove(group) }()
	}
}

func (s *Supervisor) dismissAllAlerts() {
	s.mu.Lock()
	for udid := range s.obs {
		s.dismissAlertsLocked(udid)
	}
	s.mu.Unlock()
}

// ── per-device convergence serialisation ────────────────────────────────────

func (s *Supervisor) beginInFlight(udid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight[udid] {
		return false
	}
	s.inFlight[udid] = true
	return true
}

func (s *Supervisor) endInFlight(udid string) {
	s.mu.Lock()
	delete(s.inFlight, udid)
	s.mu.Unlock()
}

// ── helpers ─────────────────────────────────────────────────────────────────

func (s *Supervisor) aliasOf(udid string) string {
	if a := s.inventory.AliasFor(udid); a != "" {
		return a
	}
	if len(udid) > 12 {
		return udid[:8] + "…"
	}
	return udid
}

// Status returns a snapshot of per-device observation classes for
// tests / debugging. Convergence-state (vs the old gate states) is the
// public surface now.
func (s *Supervisor) Status() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.obs))
	for udid, obs := range s.obs {
		out[udid] = obs.lastClass.String()
	}
	return out
}

// summariseErr returns a brief error string with xcodebuild traceback
// decorations stripped for log readability.
func summariseErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "│") || strings.HasPrefix(line, "╰") ||
			strings.HasPrefix(line, "╭") || strings.HasPrefix(line, "→") {
			continue
		}
		if len(line) > 300 {
			return line[:300] + "…"
		}
		return line
	}
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
