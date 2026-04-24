// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package autoawake keeps attached iOS devices awake while spyder is
// running. For each paired device it sees:
//
//  1. Launch the KeepAwake companion app via xcrun devicectl (the app
//     sets UIApplication.isIdleTimerDisabled=true while foregrounded,
//     the only iOS mechanism that reliably prevents display auto-lock).
//  2. If the app isn't installed yet, attempt an autonomous install
//     cycle (🎯T32) — detect codesigning identity, build via
//     xcodebuild with the developer's team, install via devicectl,
//     launch. Silent on success; logs a specific actionable message
//     when a human gate is hit (Developer Mode disabled, trust not
//     granted, no Xcode signing identity).
//  3. If the device is locked mid-launch, fire a persistent macOS
//     notification prompting the user to unlock, retry every 10 s
//     while locked, and dismiss the notification once the launch
//     succeeds or the retry budget is exhausted.
//  4. Periodically re-launch (every 15 s) on every known device so
//     user-initiated task-switching / backgrounding self-heals before
//     the next auto-lock fires.
//
// Device enumeration still comes from the pmd3 bridge — that path is
// fast, uses an already-running subprocess, and doesn't require
// separate tunneld.
package autoawake

import (
	"context"
	"errors"
	"fmt"
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

// OwnerID is the reservation owner identity used by auto-awake.
// Exported so callers can reference it when checking reservations.
const OwnerID = "autoawake"

const (
	pollInterval             = 2 * time.Second
	relaunchInterval         = 15 * time.Second
	retryWhileLockedInterval = 10 * time.Second
	retryWhileLockedBudget   = 30 // ≈ 5 minutes of retries
	settleDelay              = 3 * time.Second
)

// gateState names the per-device progress toward a running KeepAwake.
// The state machine is:
//
//	initial → installed (install succeeded or app already present)
//	initial → needs-trust (install blocked on on-device Trust step)
//	initial → needs-developer-mode (device has Developer Mode off)
//	initial → needs-xcode-signin (no codesigning identity in keychain)
//	initial → install-failed (xcodebuild or devicectl install errored)
//	installed → running (launch succeeded)
//	installed → locked (launch blocked by lock screen — alert fired)
//	installed → trust-lost (launch blocked by trust — alert fired)
type gateState string

const (
	gateInitial            gateState = "initial"
	gateInstalled          gateState = "installed"
	gateRunning            gateState = "running"
	gateLocked             gateState = "locked"
	gateTrustLost          gateState = "trust-lost"
	gateNeedsTrust         gateState = "needs-trust"
	gateNeedsDeveloperMode gateState = "needs-developer-mode"
	gateNeedsXcodeSignin   gateState = "needs-xcode-signin"
	gateInstallFailed      gateState = "install-failed"
)

// deviceGate tracks what autoawake knows about a device across ticks.
// Kept per-UDID in Supervisor.gates under s.mu.
type deviceGate struct {
	state        gateState
	lastLaunchAt time.Time

	// lockAlertGroup non-empty when a macOS alert is currently
	// displayed for this device (either lock or trust). Cleared when
	// the alert is dismissed.
	lockAlertGroup string
	// trustAlertGroup separate so we can dismiss independently.
	trustAlertGroup string
}

// Supervisor polls the pmd3 bridge device list and keeps KeepAwake
// foregrounded on every iOS device.
type Supervisor struct {
	bridge       *pmd3bridge.Client
	ios          *device.IOSAdapter
	inventory    *inventory.Store
	reservations *reservations.Store

	mu    sync.Mutex
	gates map[string]*deviceGate
	// inFlight guards per-UDID serial handling so a slow handleNewDevice
	// doesn't overlap with a periodic-relaunch tick for the same device.
	inFlight map[string]bool
}

type Option func(*Supervisor)

func WithReservations(s *reservations.Store) Option {
	return func(sv *Supervisor) { sv.reservations = s }
}

func New(bridge *pmd3bridge.Client, opts ...Option) *Supervisor {
	sv := &Supervisor{
		bridge:    bridge,
		ios:       device.NewIOSAdapter(bridge),
		inventory: inventory.New(),
		gates:     map[string]*deviceGate{},
		inFlight:  map[string]bool{},
	}
	for _, opt := range opts {
		opt(sv)
	}
	return sv
}

// Run blocks polling the device list until ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context) {
	if s.bridge == nil {
		slog.Warn("autoawake: no bridge client — keep-awake disabled")
		return
	}
	slog.Info("autoawake: supervisor started (KeepAwake companion app via devicectl)")

	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()
	relaunchTicker := time.NewTicker(relaunchInterval)
	defer relaunchTicker.Stop()

	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			s.dismissAllAlerts()
			return
		case <-pollTicker.C:
			s.tick(ctx)
		case <-relaunchTicker.C:
			s.relaunchAll(ctx)
		}
	}
}

// tick polls the bridge for attached devices, spawns handleNewDevice
// for new arrivals, and prunes departed devices from the gate map.
func (s *Supervisor) tick(ctx context.Context) {
	devices, err := s.bridge.ListDevices(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		slog.Warn("autoawake: list_devices returned structured error", "error", err)
		return
	}

	current := make(map[string]struct{}, len(devices))
	for _, d := range devices {
		current[d.UDID] = struct{}{}
	}

	s.mu.Lock()
	for udid := range s.gates {
		if _, still := current[udid]; !still {
			s.dismissAlertsLocked(udid)
			delete(s.gates, udid)
		}
	}
	var fresh []string
	for udid := range current {
		if _, known := s.gates[udid]; !known {
			s.gates[udid] = &deviceGate{state: gateInitial}
			fresh = append(fresh, udid)
		}
	}
	s.mu.Unlock()

	for _, udid := range fresh {
		go s.handleNewDevice(ctx, udid)
	}
}

// relaunchAll re-foregrounds KeepAwake on every device that's in a
// runnable state. Silent on success / not-running. Devices that are
// already alerting (locked / trust-lost) continue being driven by
// their handleNewDevice goroutines; we don't duplicate alerts here.
func (s *Supervisor) relaunchAll(ctx context.Context) {
	s.mu.Lock()
	udids := make([]string, 0, len(s.gates))
	for udid, g := range s.gates {
		switch g.state {
		case gateRunning, gateInstalled:
			udids = append(udids, udid)
		}
	}
	s.mu.Unlock()

	for _, udid := range udids {
		go s.relaunchOne(ctx, udid)
	}
}

// relaunchOne is the periodic-refresh path. It doesn't fire new
// notifications — that's handleNewDevice's job. If the launch fails
// because the device is now locked, we just upgrade the state and
// let the next handleNewDevice-equivalent (re-plug) handle alerting.
func (s *Supervisor) relaunchOne(ctx context.Context, udid string) {
	if ctx.Err() != nil {
		return
	}
	if !s.beginInFlight(udid) {
		return
	}
	defer s.endInFlight(udid)

	alias := s.aliasOf(udid)
	err := s.ios.LaunchKeepAwake(udid)
	if err == nil {
		s.setGate(udid, gateRunning)
		slog.Debug("autoawake: KeepAwake re-launched",
			"udid", udid, "alias", alias, "reason", "periodic")
		return
	}
	// Non-fatal — if the device got locked between ticks, we know;
	// the next refresh cycle (or a new-device event on re-plug) will
	// re-trigger the lock-alert flow.
	switch {
	case errors.Is(err, device.ErrLocked):
		slog.Debug("autoawake: periodic relaunch: device locked",
			"udid", udid, "alias", alias)
	case errors.Is(err, device.ErrKeepAwakeNotInstalled):
		// KeepAwake was uninstalled under us. Downgrade to initial
		// and let handleNewDevice re-run the install flow on the next
		// new-device edge (triggered by a re-plug or state-walk).
		s.setGate(udid, gateInitial)
		slog.Info("autoawake: KeepAwake disappeared — will reinstall on next attach",
			"udid", udid, "alias", alias)
	default:
		slog.Debug("autoawake: periodic relaunch failed",
			"udid", udid, "alias", alias, "error", summariseErr(err))
	}
}

// handleNewDevice runs the full install + launch sequence for a newly-
// seen UDID, including the persistent-alert retry loop for locked
// devices. Safe to run concurrently with other handleNewDevice
// goroutines (per-UDID inFlight guard).
func (s *Supervisor) handleNewDevice(ctx context.Context, udid string) {
	if !s.beginInFlight(udid) {
		return
	}
	defer s.endInFlight(udid)

	alias := s.aliasOf(udid)
	slog.Info("autoawake: new device", "udid", udid, "alias", alias)

	if s.reservations != nil {
		if err := s.reservations.Authorize(udid, OwnerID); err != nil {
			slog.Info("autoawake: skipping device held by another owner",
				"udid", udid, "alias", alias, "error", err)
			return
		}
	}

	// Brief settle so usbmux + any startup state is ready.
	select {
	case <-ctx.Done():
		return
	case <-time.After(settleDelay):
	}

	// First launch attempt. If KeepAwake isn't installed, fall into
	// the auto-install path (🎯T32).
	err := s.ios.LaunchKeepAwake(udid)
	if errors.Is(err, device.ErrKeepAwakeNotInstalled) {
		if !s.autoInstall(ctx, udid, alias) {
			return
		}
		err = s.ios.LaunchKeepAwake(udid)
	}

	// Retry-while-locked loop. A locked device produces ErrLocked;
	// we fire one persistent alert and retry silently every 10 s
	// until the user unlocks or the retry budget expires.
	for range retryWhileLockedBudget {
		if err == nil {
			s.setGate(udid, gateRunning)
			s.dismissAlerts(udid)
			slog.Info("autoawake: KeepAwake foregrounded",
				"udid", udid, "alias", alias, "reason", "new device")
			return
		}
		switch {
		case errors.Is(err, device.ErrLocked):
			s.ensureLockAlert(udid, alias)
			s.setGate(udid, gateLocked)
			select {
			case <-ctx.Done():
				s.dismissAlerts(udid)
				return
			case <-time.After(retryWhileLockedInterval):
			}
			err = s.ios.LaunchKeepAwake(udid)
			continue
		case errors.Is(err, device.ErrTrustNotGranted):
			s.ensureTrustAlert(udid, alias)
			s.setGate(udid, gateTrustLost)
			return
		default:
			slog.Warn("autoawake: LaunchKeepAwake failed (non-recoverable)",
				"udid", udid, "alias", alias, "error", summariseErr(err))
			s.setGate(udid, gateInstallFailed)
			return
		}
	}
	slog.Info("autoawake: giving up on locked device after retry budget",
		"udid", udid, "alias", alias)
	s.dismissAlerts(udid)
}

// autoInstall runs the transparent install cycle for 🎯T32 against a
// device whose KeepAwake is missing. Returns true on success (caller
// should attempt the launch again). Returns false when a human gate
// is hit or install errors; the gate state is recorded so we don't
// re-attempt on every tick.
func (s *Supervisor) autoInstall(ctx context.Context, udid, alias string) bool {
	if ctx.Err() != nil {
		return false
	}
	// Codesigning identity probe — the one hard prerequisite.
	team, err := device.DetectCodesigningTeam()
	if err != nil {
		slog.Warn("autoawake: no codesigning identity — KeepAwake install blocked. Sign in to Xcode → Settings → Accounts with your Apple ID.",
			"udid", udid, "alias", alias, "error", err.Error())
		s.setGate(udid, gateNeedsXcodeSignin)
		return false
	}

	// Developer Mode probe is best-effort — if the probe itself
	// fails (pmd3 missing, transient), we fall through and let the
	// install attempt surface the real error.
	if enabled, probeErr := device.DetectDeveloperMode(udid); probeErr == nil && !enabled {
		slog.Warn("autoawake: Developer Mode disabled on device — KeepAwake install blocked. Enable at Settings → Privacy & Security → Developer Mode (device will reboot).",
			"udid", udid, "alias", alias)
		s.setGate(udid, gateNeedsDeveloperMode)
		return false
	}

	slog.Info("autoawake: building KeepAwake (cached per daemon lifetime)",
		"team", team, "udid", udid, "alias", alias)
	appPath, err := device.BuildKeepAwake(team)
	if err != nil {
		slog.Warn("autoawake: xcodebuild KeepAwake failed",
			"udid", udid, "alias", alias, "error", summariseErr(err))
		s.setGate(udid, gateInstallFailed)
		return false
	}

	slog.Info("autoawake: installing KeepAwake", "udid", udid, "alias", alias)
	if err := device.InstallKeepAwake(udid, appPath); err != nil {
		if errors.Is(err, device.ErrTrustNotGranted) {
			s.ensureTrustAlert(udid, alias)
			s.setGate(udid, gateNeedsTrust)
			return false
		}
		slog.Warn("autoawake: devicectl install KeepAwake failed",
			"udid", udid, "alias", alias, "error", summariseErr(err))
		s.setGate(udid, gateInstallFailed)
		return false
	}
	s.setGate(udid, gateInstalled)
	return true
}

// ── alert / gate management ─────────────────────────────────────────────────

func (s *Supervisor) ensureLockAlert(udid, alias string) {
	s.mu.Lock()
	g := s.gates[udid]
	if g == nil || g.lockAlertGroup != "" {
		s.mu.Unlock()
		return
	}
	group := "spyder-lock-" + udid
	g.lockAlertGroup = group
	s.mu.Unlock()

	slog.Info("autoawake: device locked — alerting user",
		"udid", udid, "alias", alias)
	go func() {
		if err := notify.MacOSAlert("spyder",
			fmt.Sprintf("Unlock %s to enable keep-awake", alias),
			group); err != nil {
			slog.Warn("autoawake: lock alert failed", "error", err)
		}
	}()
}

func (s *Supervisor) ensureTrustAlert(udid, alias string) {
	s.mu.Lock()
	g := s.gates[udid]
	if g == nil || g.trustAlertGroup != "" {
		s.mu.Unlock()
		return
	}
	group := "spyder-trust-" + udid
	g.trustAlertGroup = group
	s.mu.Unlock()

	slog.Info("autoawake: developer certificate not trusted — alerting user",
		"udid", udid, "alias", alias)
	go func() {
		if err := notify.MacOSAlert("spyder",
			fmt.Sprintf("Trust the developer profile on %s to enable keep-awake (Settings → General → VPN & Device Management → tap developer entry → Trust)", alias),
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

func (s *Supervisor) dismissAlertsLocked(udid string) {
	g := s.gates[udid]
	if g == nil {
		return
	}
	if g.lockAlertGroup != "" {
		group := g.lockAlertGroup
		g.lockAlertGroup = ""
		go func() { _ = notify.MacOSAlertRemove(group) }()
	}
	if g.trustAlertGroup != "" {
		group := g.trustAlertGroup
		g.trustAlertGroup = ""
		go func() { _ = notify.MacOSAlertRemove(group) }()
	}
}

func (s *Supervisor) dismissAllAlerts() {
	s.mu.Lock()
	for udid := range s.gates {
		s.dismissAlertsLocked(udid)
	}
	s.mu.Unlock()
}

func (s *Supervisor) setGate(udid string, state gateState) {
	s.mu.Lock()
	if g := s.gates[udid]; g != nil {
		g.state = state
		if state == gateRunning {
			g.lastLaunchAt = time.Now()
		}
	}
	s.mu.Unlock()
}

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

// Status returns a snapshot of per-device gate states for tests /
// debugging.
func (s *Supervisor) Status() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.gates))
	for udid, g := range s.gates {
		out[udid] = string(g.state)
	}
	return out
}

// summariseErr returns a brief error string with pmd3 / xcodebuild
// traceback decorations stripped for log readability.
func summariseErr(err error) string {
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
