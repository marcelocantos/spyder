// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package autoawake supervises iOS devices: whenever a paired iOS device
// appears, it foregrounds the KeepAwake companion app on that device via
// xcrun devicectl. The app holds UIApplication.isIdleTimerDisabled=true
// while foregrounded — the sole iOS mechanism that reliably prevents
// display auto-lock.
//
// 🎯T31 restored this approach after T25 (v0.6.0) attempted to replace it
// with pmd3's PowerAssertionService. That turned out to be a no-op for
// display sleep on iOS: v0.6.0 through v0.8.0 all shipped with autoawake
// claiming to keep devices awake while not actually doing so.
//
// The supervisor is started by daemon.Start. It runs for the lifetime of
// the server and exits on context cancel. Device enumeration still comes
// from the pmd3 bridge (faster than shelling out to usbmux per tick and
// already paid for as a startup cost).
package autoawake

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/pmd3bridge"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// OwnerID is the reservation owner identity used by auto-awake.
// Exported so callers can reference it when checking reservations.
const OwnerID = "autoawake"

const (
	// pollInterval is how often we enumerate devices to detect
	// newly-attached ones.
	pollInterval = 2 * time.Second
	// relaunchInterval is how often we re-foreground the KeepAwake app
	// on every known device. This catches the case where the user
	// backgrounded or switched away from the app — without a periodic
	// re-launch the device would auto-lock next time its idle timer
	// expires. Set shorter than typical auto-lock times so a
	// backgrounding event gets corrected before the device sleeps.
	relaunchInterval = 15 * time.Second
)

// Supervisor polls the pmd3 bridge device list and keeps the KeepAwake
// companion app foregrounded on every iOS device that appears.
type Supervisor struct {
	bridge       *pmd3bridge.Client
	ios          *device.IOSAdapter
	inventory    *inventory.Store
	reservations *reservations.Store // optional: honour other holders' reservations

	mu   sync.Mutex
	seen map[string]time.Time // udid → last-successful-launch time
}

// Option configures a Supervisor at construction.
type Option func(*Supervisor)

// WithReservations injects a reservation store so the supervisor
// skips devices held by non-self owners.
func WithReservations(s *reservations.Store) Option {
	return func(sv *Supervisor) { sv.reservations = s }
}

// New constructs a Supervisor. bridge is used for device enumeration; it
// may be nil, in which case the supervisor logs a warning and does
// nothing (device enumeration requires the bridge).
func New(bridge *pmd3bridge.Client, opts ...Option) *Supervisor {
	sv := &Supervisor{
		bridge:    bridge,
		ios:       device.NewIOSAdapter(bridge),
		inventory: inventory.New(),
		seen:      map[string]time.Time{},
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

	// Prime seen set and launch on startup.
	s.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
			s.tick(ctx)
		case <-relaunchTicker.C:
			s.relaunchAll(ctx)
		}
	}
}

// tick polls the bridge for currently-attached devices, launches
// KeepAwake on new arrivals, and prunes departed devices from the seen
// set.
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
	// Drop departed devices.
	for udid := range s.seen {
		if _, still := current[udid]; !still {
			delete(s.seen, udid)
		}
	}
	// Identify new arrivals.
	var fresh []string
	for udid := range current {
		if _, known := s.seen[udid]; !known {
			fresh = append(fresh, udid)
		}
	}
	s.mu.Unlock()

	for _, udid := range fresh {
		go s.launch(ctx, udid, "new device")
	}
}

// relaunchAll re-foregrounds KeepAwake on every known device.
// Idempotent-ish: launching an already-foregrounded app is a no-op.
// Catches the case where the user backgrounded the app or switched to
// another app, which would otherwise let the device auto-lock.
func (s *Supervisor) relaunchAll(ctx context.Context) {
	s.mu.Lock()
	udids := make([]string, 0, len(s.seen))
	for udid := range s.seen {
		udids = append(udids, udid)
	}
	s.mu.Unlock()

	for _, udid := range udids {
		go s.launch(ctx, udid, "periodic relaunch")
	}
}

// launch foregrounds KeepAwake on the given device and records
// success/failure in the seen map. Failures are logged but non-fatal;
// the next tick will retry.
func (s *Supervisor) launch(ctx context.Context, udid, reason string) {
	if s.reservations != nil {
		if err := s.reservations.Authorize(udid, OwnerID); err != nil {
			slog.Debug("autoawake: skipping device held by another owner",
				"udid", udid, "reason", reason, "error", err)
			return
		}
	}
	alias := s.aliasOf(udid)
	if err := s.ios.LaunchKeepAwake(udid); err != nil {
		slog.Warn("autoawake: LaunchKeepAwake failed",
			"udid", udid, "alias", alias, "reason", reason,
			"error", err.Error())
		return
	}
	s.mu.Lock()
	s.seen[udid] = time.Now()
	s.mu.Unlock()
	slog.Info("autoawake: KeepAwake foregrounded",
		"udid", udid, "alias", alias, "reason", reason)
}

// aliasOf resolves a UDID to an inventory alias, falling back to a
// short UDID form for readability.
func (s *Supervisor) aliasOf(udid string) string {
	if a := s.inventory.AliasFor(udid); a != "" {
		return a
	}
	if len(udid) > 12 {
		return udid[:8] + "…"
	}
	return udid
}

// Status returns a snapshot of devices the supervisor has launched
// KeepAwake on, with the timestamp of the most recent launch.
func (s *Supervisor) Status() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]time.Time, len(s.seen))
	for udid, t := range s.seen {
		out[udid] = t
	}
	return out
}
