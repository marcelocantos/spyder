// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package autoawake supervises iOS devices: whenever a paired iOS device
// appears (via the pmd3 bridge device list), it acquires a power assertion on
// the device to prevent auto-lock, refreshes it periodically, and releases it
// when the device disconnects or the daemon shuts down.
//
// The supervisor is started by daemon.Start. It runs for the lifetime of the
// server and exits on context cancel.
package autoawake

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/marcelocantos/spyder/internal/inventory"
	"github.com/marcelocantos/spyder/internal/pmd3bridge"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// OwnerID is the reservation owner identity used by auto-awake.
// Exported so callers can reference it when checking reservations.
const OwnerID = "autoawake"

const (
	pollInterval     = 2 * time.Second
	assertionTimeout = 300 // seconds (5 minutes); refresh at half this interval
	refreshInterval  = assertionTimeout / 2 * time.Second
)

// Supervisor polls the pmd3 bridge device list and ensures a power assertion
// is held on every iOS device that appears.
type Supervisor struct {
	bridge       *pmd3bridge.Client
	inventory    *inventory.Store
	reservations *reservations.Store // optional: if set, honour other holders' reservations

	mu         sync.Mutex
	assertions map[string]string // udid → handleID
	cancels    map[string]context.CancelFunc
}

// Option configures a Supervisor at construction.
type Option func(*Supervisor)

// WithReservations injects a reservation store so the supervisor
// skips devices held by non-self owners. If omitted, the supervisor
// acts unconditionally.
func WithReservations(s *reservations.Store) Option {
	return func(sv *Supervisor) { sv.reservations = s }
}

// New constructs a Supervisor. bridge is the pmd3 bridge client; it may be
// nil, in which case the supervisor logs a warning and does nothing.
func New(bridge *pmd3bridge.Client, opts ...Option) *Supervisor {
	sv := &Supervisor{
		bridge:     bridge,
		inventory:  inventory.New(),
		assertions: map[string]string{},
		cancels:    map[string]context.CancelFunc{},
	}
	for _, opt := range opts {
		opt(sv)
	}
	return sv
}

// Run blocks polling the device list until ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context) {
	if s.bridge == nil {
		slog.Warn("autoawake: no bridge client — power assertions disabled")
		return
	}
	slog.Info("autoawake: supervisor started (power assertions via pmd3 bridge)")

	seen := map[string]bool{}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// First tick immediately so existing connected devices are handled on startup.
	s.tick(ctx, seen)
	for {
		select {
		case <-ctx.Done():
			s.releaseAll()
			return
		case <-ticker.C:
			s.tick(ctx, seen)
		}
	}
}

func (s *Supervisor) tick(ctx context.Context, seen map[string]bool) {
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	bridgeDevices, err := s.bridge.ListDevices(listCtx)
	if err != nil {
		return // bridge unavailable — quiet retry next tick
	}
	udids := make([]string, 0, len(bridgeDevices))
	for _, d := range bridgeDevices {
		udids = append(udids, d.UDID)
	}

	current := make(map[string]bool, len(udids))
	for _, udid := range udids {
		current[udid] = true
	}

	// Detect disappeared devices.
	for udid := range seen {
		if !current[udid] {
			delete(seen, udid)
			go s.handleDeviceDisconnect(ctx, udid)
		}
	}

	// Detect new devices.
	for _, udid := range newDevices(udids, seen) {
		go s.handleNewDevice(ctx, udid)
	}
}

// newDevices returns the UDIDs in `current` that weren't in `seen`,
// marks them seen, and prunes `seen` entries that are no longer in
// `current` (so unplug+replug retriggers). Pure — no I/O, testable
// without mocks.
func newDevices(current []string, seen map[string]bool) []string {
	present := make(map[string]bool, len(current))
	var fresh []string
	for _, udid := range current {
		present[udid] = true
		if !seen[udid] {
			fresh = append(fresh, udid)
			seen[udid] = true
		}
	}
	for udid := range seen {
		if !present[udid] {
			delete(seen, udid)
		}
	}
	return fresh
}

// handleNewDevice acquires a power assertion on the device and starts a
// refresh goroutine.
func (s *Supervisor) handleNewDevice(ctx context.Context, udid string) {
	// Check whether a goroutine is already handling this UDID.
	s.mu.Lock()
	if _, exists := s.assertions[udid]; exists {
		s.mu.Unlock()
		return
	}
	// Placeholder so concurrent ticks don't double-handle.
	s.assertions[udid] = ""
	s.mu.Unlock()

	alias := s.aliasOf(udid)
	slog.Info("autoawake: new device", "udid", udid, "alias", alias)

	// Respect reservations owned by anyone else.
	if s.reservations != nil {
		if err := s.reservations.Authorize(udid, OwnerID); err != nil {
			slog.Info("autoawake: skipping device held by another owner",
				"udid", udid, "alias", alias, "error", err)
			s.mu.Lock()
			delete(s.assertions, udid)
			s.mu.Unlock()
			return
		}
	}

	acquireCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	handleID, err := s.bridge.AcquirePowerAssertion(acquireCtx, udid,
		"PreventUserIdleSystemSleep", "spyder autoawake",
		assertionTimeout, "")
	if err != nil {
		slog.Warn("autoawake: acquire assertion failed",
			"udid", udid, "alias", alias, "error", summariseErr(err))
		s.mu.Lock()
		delete(s.assertions, udid)
		s.mu.Unlock()
		return
	}

	refreshCtx, refreshCancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.assertions[udid] = handleID
	s.cancels[udid] = refreshCancel
	s.mu.Unlock()

	slog.Info("autoawake: power assertion acquired",
		"udid", udid, "alias", alias, "handle", handleID)

	// Refresh goroutine: ticks at half the assertion timeout.
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-ticker.C:
				rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
				if rerr := s.bridge.RefreshPowerAssertion(rctx, handleID, assertionTimeout); rerr != nil {
					slog.Warn("autoawake: refresh assertion failed",
						"udid", udid, "alias", alias, "error", rerr)
				}
				rcancel()
			}
		}
	}()
}

// handleDeviceDisconnect releases the power assertion for a device that
// disappeared from the bridge device list.
func (s *Supervisor) handleDeviceDisconnect(ctx context.Context, udid string) {
	s.mu.Lock()
	handleID, ok := s.assertions[udid]
	cancel := s.cancels[udid]
	delete(s.assertions, udid)
	delete(s.cancels, udid)
	s.mu.Unlock()

	if !ok || handleID == "" {
		return
	}

	alias := s.aliasOf(udid)
	if cancel != nil {
		cancel()
	}

	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	if err := s.bridge.ReleasePowerAssertion(rctx, handleID); err != nil {
		slog.Warn("autoawake: release assertion failed on disconnect",
			"udid", udid, "alias", alias, "error", err)
	} else {
		slog.Info("autoawake: power assertion released (device disconnected)",
			"udid", udid, "alias", alias, "handle", handleID)
	}
}

// releaseAll releases all outstanding power assertions. Called on daemon shutdown.
func (s *Supervisor) releaseAll() {
	s.mu.Lock()
	handles := make(map[string]string, len(s.assertions))
	cancels := make(map[string]context.CancelFunc, len(s.cancels))
	for udid, h := range s.assertions {
		handles[udid] = h
	}
	for udid, c := range s.cancels {
		cancels[udid] = c
	}
	s.assertions = map[string]string{}
	s.cancels = map[string]context.CancelFunc{}
	s.mu.Unlock()

	for udid, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
		_ = udid
	}

	for udid, handleID := range handles {
		if handleID == "" {
			continue
		}
		alias := s.aliasOf(udid)
		rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := s.bridge.ReleasePowerAssertion(rctx, handleID); err != nil {
			slog.Warn("autoawake: release assertion failed on shutdown",
				"udid", udid, "alias", alias, "error", err)
		} else {
			slog.Info("autoawake: power assertion released (shutdown)",
				"udid", udid, "alias", alias, "handle", handleID)
		}
		rcancel()
	}
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

// summariseErr returns a brief error string, stripping pmd3 tracebacks.
func summariseErr(err error) string {
	s := err.Error()
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return fmt.Sprintf("%.200s", line)
		}
	}
	return fmt.Sprintf("%.200s", s)
}
