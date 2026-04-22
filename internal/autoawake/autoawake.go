// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package autoawake supervises iOS devices: whenever a paired iOS device
// appears (via the pmd3 bridge device list), it acquires a power assertion on
// the device to prevent auto-lock, refreshes it periodically, and releases it
// when the device disconnects or the daemon shuts down.
//
// The supervisor is started by daemon.Start. It runs for the lifetime of the
// server and exits on context cancel.
//
// Error model (🎯T26.2): the bridge client itself panics the daemon on
// transport-level failures. Autoawake only handles structured BridgeError
// responses (device_not_paired, etc.), which it treats as "skip this device"
// rather than "crash". On shutdown release-all, failures are logged and
// swallowed — the process is exiting anyway.
package autoawake

import (
	"context"
	"errors"
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
	bridgeDevices, err := s.bridge.ListDevices(ctx)
	if err != nil {
		// Only caller-initiated cancellation reaches here; bridge bugs
		// panic inside the client. BridgeError from list_devices would
		// be surprising (no device-scoped state to be wrong about) but
		// we don't want to crash on it — log and skip this tick.
		if errors.Is(err, context.Canceled) {
			return
		}
		slog.Warn("autoawake: list_devices returned structured error", "error", err)
		return
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

	handleID, err := s.bridge.AcquirePowerAssertion(ctx, udid,
		"PreventUserIdleSystemSleep", "spyder autoawake",
		assertionTimeout, "")
	if err != nil {
		// Only reachable for structured BridgeError or caller-cancel;
		// transport bugs panic inside the client. Typical cases:
		// device_not_paired (just-plugged, trust not yet granted).
		var be *pmd3bridge.BridgeError
		if errors.As(err, &be) {
			slog.Info("autoawake: skipping device with structured error",
				"udid", udid, "alias", alias, "code", be.Code)
		} else if !errors.Is(err, context.Canceled) {
			slog.Warn("autoawake: acquire assertion returned unexpected error",
				"udid", udid, "alias", alias, "error", summariseErr(err))
		}
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
				// Non-BridgeError failures panic inside the client.
				// Structured errors (e.g. not_found for a handle
				// the bridge forgot) are logged and leave the
				// assertion state as-is; we don't try to re-acquire.
				if rerr := s.bridge.RefreshPowerAssertion(refreshCtx, handleID, assertionTimeout); rerr != nil {
					if errors.Is(rerr, context.Canceled) {
						return
					}
					slog.Warn("autoawake: refresh returned structured error",
						"udid", udid, "alias", alias, "error", summariseErr(rerr))
				}
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

	if err := s.bridge.ReleasePowerAssertion(ctx, handleID); err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("autoawake: release returned structured error on disconnect",
				"udid", udid, "alias", alias, "error", summariseErr(err))
		}
	} else {
		slog.Info("autoawake: power assertion released (device disconnected)",
			"udid", udid, "alias", alias, "handle", handleID)
	}
}

// releaseAll releases all outstanding power assertions. Called on daemon
// shutdown. Releases run in parallel under a shared deadline so shutdown
// doesn't balloon linearly with device count.
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

	for _, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
	}

	drainCtx, drainCancel := context.WithTimeout(context.Background(),
		10*time.Second)
	defer drainCancel()

	var wg sync.WaitGroup
	for udid, handleID := range handles {
		if handleID == "" {
			continue
		}
		wg.Add(1)
		go func(udid, handleID string) {
			defer wg.Done()
			alias := s.aliasOf(udid)
			if err := s.bridge.ReleasePowerAssertion(drainCtx, handleID); err != nil {
				slog.Warn("autoawake: release failed on shutdown",
					"udid", udid, "alias", alias, "error", summariseErr(err))
			} else {
				slog.Info("autoawake: power assertion released (shutdown)",
					"udid", udid, "alias", alias, "handle", handleID)
			}
		}(udid, handleID)
	}
	wg.Wait()
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
