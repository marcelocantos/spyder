// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package autoawake keeps attached iOS devices awake by ensuring the
// KeepAwake companion app is running on each one. The supervisor runs
// a convergence loop (🎯T32, redesigned 2026-04-25): rather than
// latching gate states on first sight and only re-checking on re-plug,
// each tick observes the *current* state of every connected device and
// drives toward the desired state ("KeepAwake foregrounded").
//
// Loop:
//
//  1. Every pollInterval, enumerate connected iOS devices via
//     IOSAdapter.List. New devices kick off an immediate convergence
//     step in a goroutine; departed devices have their alerts dismissed
//     and observation state pruned.
//  2. Every convergeInterval, run convergence on every still-present
//     device. This is what catches user-side resolutions of human
//     gates (the user trusted the developer cert, unlocked the device,
//     toggled Developer Mode, etc.) — observations are re-tested, the
//     resolved gate is no longer in effect, and we proceed.
//
// Convergence per device:
//
//  1. Is KeepAwake running? (devicectl process query) → done.
//  2. Is KeepAwake installed? (devicectl apps query) → if no, attempt
//     install (xcodebuild + devicectl install), then proceed.
//  3. Try to launch via devicectl. Errors classify into:
//     - locked: fire idempotent macOS alert; convergence next tick may
//       succeed once the user unlocks.
//     - needs-trust: fire idempotent macOS alert; once the user trusts
//       the cert, the next tick's launch succeeds.
//     - needs-developer-mode / needs-xcode-signin: log on transition,
//       no alert (the user must take action that requires their
//       attention anyway).
//     - other: log + retry next tick.
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
	"github.com/marcelocantos/spyder/internal/pmd3bridge"
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
	classLocked
	classNeedsTrust
	classNeedsDeveloperMode
	classNeedsXcodeSignin
	classNotInstalled
	classOther
)

func (c errorClass) String() string {
	switch c {
	case classConverged:
		return "converged"
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

	// lockAlertGroup non-empty when a macOS lock alert is currently
	// displayed for this device. Cleared when the alert is dismissed.
	lockAlertGroup string

	// trustAlertGroup separate so we can dismiss independently.
	trustAlertGroup string
}

// Supervisor runs the convergence loop. Construct via New; call Run in
// a goroutine for the lifetime of the daemon.
type Supervisor struct {
	// bridge is retained for compatibility with the daemon wiring but
	// is otherwise unused — the convergence loop only depends on the
	// IOSAdapter, which talks to devicectl directly. A nil bridge is
	// fine; the IOSAdapter constructed below tolerates it for the
	// keep-awake-relevant operations.
	bridge       *pmd3bridge.Client
	ios          *device.IOSAdapter
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

// New creates a new Supervisor. bridge may be nil; the convergence loop
// uses devicectl directly via the IOSAdapter and doesn't depend on the
// pmd3 bridge for any keep-awake operation.
func New(bridge *pmd3bridge.Client, opts ...Option) *Supervisor {
	sv := &Supervisor{
		bridge:    bridge,
		ios:       device.NewIOSAdapter(bridge),
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

// converge observes the current state of one device and drives toward
// the desired state (KeepAwake foregrounded). Errors classify into
// errorClass values; transitions between classes emit log/alert
// side-effects via advance().
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

	// 1) Fast path: if KeepAwake is already running, we're converged.
	if running, err := s.ios.KeepAwakeRunning(udid); err == nil && running {
		s.advance(udid, alias, classConverged, nil)
		return
	}

	// 2) If KeepAwake isn't installed, run the transparent install
	// cycle. attemptInstall handles its own classification on failure;
	// on success we fall through to step 3.
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

	// 3) Installed but not running — try to launch. Classify on error.
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
	default:
		s.advance(udid, alias, classOther, launchErr)
	}
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

// summariseErr returns a brief error string with pmd3 / xcodebuild
// traceback decorations stripped for log readability.
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
