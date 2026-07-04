// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios/tunnel"
)

// errEventStreamClosed signals a clean end of the fake event stream.
var errEventStreamClosed = errors.New("goios: event stream closed")

// fakeEventSource replays a scripted slice of usbmux events, then ends
// the stream — the class-1 oracle's event seam.
type fakeEventSource struct {
	events []DeviceEvent
	i      int
	closed bool
}

func (s *fakeEventSource) Next() (DeviceEvent, error) {
	if s.i >= len(s.events) {
		return DeviceEvent{}, errEventStreamClosed
	}
	ev := s.events[s.i]
	s.i++
	return ev, nil
}

func (s *fakeEventSource) Close() error { s.closed = true; return nil }

// setTunnel / hasTunnel let a test model the daemon's registry contents
// directly (used by the listener oracle).
func (d *fakeTunnelDaemon) setTunnel(udid string, tun tunnel.Tunnel) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tunnels[udid] = tun
}

func (d *fakeTunnelDaemon) hasTunnel(udid string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.tunnels[udid]
	return ok
}

// syncListener builds a Listener whose recovery actions run
// synchronously, so a single Run call fully processes its events before
// returning — the oracle can then assert deterministic state.
func syncListener(r *Resolver) *Listener {
	l := NewListener(r)
	l.dispatch = func(f func()) { f() }
	return l
}

// The core 🎯T89.2 behaviour: on detach the tunnel is dropped from the
// registry via DELETE; on the subsequent genuine re-attach it is
// re-established via the T89.1 primitive. Driven entirely through the
// fake event source + fake tunnel daemon — no real device.
func TestListener_DetachDropsTunnel_ReattachReestablishes(t *testing.T) {
	setFastReestablish(t, 2*time.Second, 5*time.Millisecond)
	d := newFakeTunnelDaemon(t)
	const udid = "FAKE-REENUM"
	// Device is connected with a healthy tunnel.
	d.setTunnel(udid, tunnel.Tunnel{Address: "::live", RsdPort: 1, Udid: udid})

	l := syncListener(New(d.host, d.port))

	// Pass 1: startup snapshot attach, then a detach (unplug).
	l.Run(context.Background(), &fakeEventSource{events: []DeviceEvent{
		{DeviceID: 1, UDID: udid, Attached: true},
		{DeviceID: 1, Attached: false},
	}})
	if got := d.deleteCount(); got != 1 {
		t.Fatalf("detach should issue exactly one DELETE to drop the tunnel; got %d", got)
	}
	if d.hasTunnel(udid) {
		t.Fatalf("registry must hold no entry for the detached device")
	}

	// Pass 2: re-attach (new DeviceID, same UDID) — the daemon rebuilds a
	// fresh tunnel two polls into the re-establish wait.
	d.onGet = func(dm *fakeTunnelDaemon, u string, n int) {
		if u == udid && n == 2 {
			dm.tunnels[udid] = tunnel.Tunnel{Address: "::rebuilt", RsdPort: 2, Udid: udid}
		}
	}
	l.Run(context.Background(), &fakeEventSource{events: []DeviceEvent{
		{DeviceID: 2, UDID: udid, Attached: true},
	}})
	if !d.hasTunnel(udid) {
		t.Errorf("re-attach should have re-established a live tunnel")
	}
	if got := d.deleteCount(); got != 2 {
		t.Errorf("re-attach's re-establish should have issued its own DELETE (total 2); got %d", got)
	}
}

// A plain startup attach with no prior detach must NOT tear down a
// healthy tunnel — usbmux replays an "Attached" for every already-
// connected device the instant we start listening, and force-dropping
// those would churn every healthy tunnel at boot.
func TestListener_StartupAttachDoesNotDropHealthyTunnel(t *testing.T) {
	d := newFakeTunnelDaemon(t)
	const udid = "FAKE-HEALTHY"
	d.setTunnel(udid, tunnel.Tunnel{Address: "::live", RsdPort: 1, Udid: udid})

	l := syncListener(New(d.host, d.port))
	l.Run(context.Background(), &fakeEventSource{events: []DeviceEvent{
		{DeviceID: 1, UDID: udid, Attached: true},
	}})

	if got := d.deleteCount(); got != 0 {
		t.Errorf("startup attach must not DELETE a healthy tunnel; got %d DELETEs", got)
	}
	if !d.hasTunnel(udid) {
		t.Errorf("healthy tunnel must survive a startup attach")
	}
}

// A detach for a device we never saw attach (no DeviceID→UDID mapping)
// is a no-op — nothing to drop, and we must not issue a DELETE for an
// unknown/empty UDID.
func TestListener_DetachOfUnknownDeviceIsNoop(t *testing.T) {
	d := newFakeTunnelDaemon(t)
	l := syncListener(New(d.host, d.port))
	l.Run(context.Background(), &fakeEventSource{events: []DeviceEvent{
		{DeviceID: 99, Attached: false},
	}})
	if got := d.deleteCount(); got != 0 {
		t.Errorf("detach of an unknown device must not DELETE anything; got %d", got)
	}
}
