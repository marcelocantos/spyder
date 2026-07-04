// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"context"
	"log/slog"

	"github.com/danielpaulus/go-ios/ios"
)

// DeviceEvent is a normalized usbmux attach/detach notification. UDID is
// populated on attach (usbmux carries the SerialNumber there); on detach
// usbmux sends only the DeviceID, so the Listener resolves UDID from the
// DeviceID→UDID map it built on attach.
type DeviceEvent struct {
	DeviceID int
	UDID     string
	Attached bool
}

// EventSource streams usbmux attach/detach events. The production
// implementation wraps go-ios ios.Listen; tests supply a fake. Next
// blocks until the next event and returns a non-nil error when the
// stream ends or fails; the Listener's Run loop exits on that error.
//
// This is the injectable "event seam" 🎯T90.1 generalises: the whole
// attach/detach → drop/re-establish behaviour is table-testable against
// a fake source with no real device.
type EventSource interface {
	Next() (DeviceEvent, error)
	Close() error
}

// TunnelRecovery is the recovery surface the Listener drives on usbmux
// events. *Resolver satisfies it directly (Invalidate / DropTunnel /
// ReestablishTunnel), but it is an interface so higher layers — notably
// the iOS device adapter — can wrap those registry actions with their
// own per-device cache invalidation (pooled service connections that a
// re-enumeration also kills) in the same step, and so the Listener stays
// testable with a fake.
type TunnelRecovery interface {
	// Invalidate drops per-device caches only, without touching the
	// tunnel registry. Used on a startup-snapshot or fresh-hotplug
	// attach, where the daemon builds the tunnel on its own.
	Invalidate(udid string)
	// DropTunnel drops the device's tunnel from the registry and any
	// caches, without waiting for a rebuild. Used on detach.
	DropTunnel(udid string) error
	// ReestablishTunnel force-rebuilds the device's tunnel and drops
	// caches. Used on a genuine re-attach (re-enumeration).
	ReestablishTunnel(udid string) error
}

// Listener subscribes to usbmux attach/detach events and keeps the
// tunnel registry from going stale across device re-enumeration
// (🎯T89.2):
//
//   - On detach it drops the device's tunnel from the registry (and our
//     cache) via the T89.1 DELETE primitive, so no stale/zombie entry
//     lingers.
//   - On a genuine re-attach (a UDID that was previously detached — i.e.
//     a hub power-cycle or unplug/replug) it re-establishes a fresh
//     tunnel via the T89.1 primitive, proactively, so consumers don't
//     have to hit the lazy recovery path.
//
// A plain attach with no preceding detach (usbmux replays an "Attached"
// for every already-connected device the moment we start listening, and
// fresh hot-plugs) only invalidates our cache: the go-ios daemon builds
// the tunnel on its own within ~1s, so force-dropping it would needlessly
// tear down a healthy tunnel at startup.
type Listener struct {
	rec        TunnelRecovery
	byID       map[int]string  // DeviceID → UDID, for detach resolution
	seenDetach map[string]bool // UDIDs that have been detached at least once

	// dispatch runs a recovery action. In production it is `go f()` so a
	// slow re-establish (bounded, but up to ~12s waiting for the daemon
	// to rebuild) never blocks the event loop and starves later events.
	// Tests override it to run synchronously for determinism.
	dispatch func(func())
}

// NewListener builds a Listener that drives rec. Feed it events by
// calling Run with an EventSource; the Listener's bookkeeping (which
// devices are known / have been detached) persists across successive Run
// calls, so a caller can reconnect the source after a stream error
// without losing re-enumeration state.
func NewListener(rec TunnelRecovery) *Listener {
	return &Listener{
		rec:        rec,
		byID:       map[int]string{},
		seenDetach: map[string]bool{},
		dispatch:   func(f func()) { go f() },
	}
}

// Run consumes events from src until it errors (e.g. the daemon shuts
// down and closes it) or ctx is cancelled. It closes src on ctx
// cancellation so the blocking Next unblocks. Run is single-goroutine:
// its bookkeeping maps need no locking; only the dispatched recovery
// actions touch the (internally-locked) Resolver.
func (l *Listener) Run(ctx context.Context, src EventSource) {
	if ctx != nil {
		stop := context.AfterFunc(ctx, func() { _ = src.Close() })
		defer stop()
	}
	for {
		ev, err := src.Next()
		if err != nil {
			if ctx != nil && ctx.Err() != nil {
				slog.Info("goios listener: stopped", "reason", "context cancelled")
				return
			}
			slog.Info("goios listener: event stream ended", "error", err.Error())
			return
		}
		if ev.Attached {
			l.onAttach(ev)
		} else {
			l.onDetach(ev)
		}
	}
}

func (l *Listener) onAttach(ev DeviceEvent) {
	if ev.UDID == "" {
		return
	}
	l.byID[ev.DeviceID] = ev.UDID
	if l.seenDetach[ev.UDID] {
		// Genuine re-enumeration (this UDID detached earlier). Force a
		// fresh tunnel via the shared primitive — this is the hub
		// power-cycle case T89 exists for.
		delete(l.seenDetach, ev.UDID)
		udid := ev.UDID
		slog.Info("goios listener: device re-attached; re-establishing tunnel", "udid", udid)
		l.dispatch(func() {
			if err := l.rec.ReestablishTunnel(udid); err != nil {
				slog.Warn("goios listener: re-establish on re-attach failed",
					"udid", udid, "error", err.Error())
			}
		})
		return
	}
	// Startup snapshot or fresh hot-plug: the daemon builds the tunnel on
	// its own. Just drop any stale cache so the next resolve re-handshakes.
	l.rec.Invalidate(ev.UDID)
}

func (l *Listener) onDetach(ev DeviceEvent) {
	udid := ev.UDID
	if udid == "" {
		udid = l.byID[ev.DeviceID]
	}
	delete(l.byID, ev.DeviceID)
	if udid == "" {
		return // never saw this device attach; nothing to drop
	}
	l.seenDetach[udid] = true
	slog.Info("goios listener: device detached; dropping tunnel", "udid", udid)
	l.dispatch(func() {
		if err := l.rec.DropTunnel(udid); err != nil {
			slog.Warn("goios listener: drop tunnel on detach failed",
				"udid", udid, "error", err.Error())
		}
	})
}

// usbmuxEventSource is the production EventSource, wrapping go-ios
// ios.Listen. It filters the raw usbmux stream down to attach/detach
// events carrying a resolvable device, skipping the other message types
// (e.g. Paired) usbmux interleaves.
type usbmuxEventSource struct {
	recv  func() (ios.AttachedMessage, error)
	close func() error
}

// NewUsbmuxEventSource opens a usbmux listen connection. The caller runs
// the returned source under a Listener and is responsible for the
// Listener's lifecycle (Run + ctx cancellation, which closes this).
func NewUsbmuxEventSource() (EventSource, error) {
	recv, closeFn, err := ios.Listen()
	if err != nil {
		return nil, err
	}
	return &usbmuxEventSource{recv: recv, close: closeFn}, nil
}

func (s *usbmuxEventSource) Next() (DeviceEvent, error) {
	for {
		msg, err := s.recv()
		if err != nil {
			return DeviceEvent{}, err
		}
		switch {
		case msg.DeviceAttached():
			udid := msg.Properties.SerialNumber
			if udid == "" {
				continue // malformed attach; nothing to key on
			}
			return DeviceEvent{DeviceID: msg.DeviceID, UDID: udid, Attached: true}, nil
		case msg.DeviceDetached():
			// Detached carries only DeviceID; UDID is resolved by the
			// Listener from its DeviceID→UDID map.
			return DeviceEvent{DeviceID: msg.DeviceID, Attached: false}, nil
		default:
			continue // other message types (e.g. Paired) — ignore
		}
	}
}

func (s *usbmuxEventSource) Close() error {
	if s.close == nil {
		return nil
	}
	return s.close()
}
