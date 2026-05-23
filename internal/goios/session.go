// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package goios is a thin per-UDID session helper around
// github.com/danielpaulus/go-ios. Its job is to take a UDID, walk the
// tunnel-info → RSD-handshake → enriched-DeviceEntry sequence once, and
// hand callers the populated DeviceEntry that go-ios's
// instruments / installationproxy / screenshotr packages expect on the
// iOS-17+ RSD path.
//
// Without this layer, every call site would have to repeat the same ~10
// lines of tunnel-lookup boilerplate that go-ios's CLI does in main.go,
// and the per-call RSD handshake would dominate the cost of frequent
// operations like the autoawake convergence loop's foreground-app probe.
//
// The session result is cached per-UDID. Callers should call
// Invalidate(udid) after a transport-level error, on device unplug, or
// after the tunnel daemon restarts. The cache also expires after a TTL
// to avoid relying on lazy refresh in steady state.
package goios

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/tunnel"
	"github.com/marcelocantos/spyder/internal/wedge"
)

// Defaults match `ios tunnel start --userspace`'s registry endpoint.
const (
	DefaultTunnelHost = "127.0.0.1"
	DefaultTunnelPort = 60105

	// cacheTTL bounds how long a resolved DeviceEntry is reused before
	// being re-fetched. Long enough to amortise the ~150–300ms RSD
	// handshake across many probes; short enough that an unplug-replug
	// or tunnel restart self-heals within a minute or two without
	// needing explicit Invalidate calls everywhere.
	cacheTTL = 60 * time.Second
)

// Resolver hands out enriched ios.DeviceEntry values keyed by UDID,
// walking the tunnel-info / RSD handshake once per cache miss. Safe for
// concurrent use; per-UDID resolution is serialised so two goroutines
// asking for the same device at the same time do one handshake, not two.
type Resolver struct {
	tunnelHost string
	tunnelPort int

	mu    sync.Mutex
	cache map[string]*entry
	// inflight gates concurrent first-time resolution of the same UDID.
	inflight map[string]chan struct{}
}

type entry struct {
	dev      ios.DeviceEntry
	resolved time.Time
}

// New constructs a Resolver against the tunnel-info endpoint at
// host:port. Pass DefaultTunnelHost / DefaultTunnelPort for the standard
// `ios tunnel start --userspace` registry.
func New(host string, port int) *Resolver {
	if host == "" {
		host = DefaultTunnelHost
	}
	if port == 0 {
		port = DefaultTunnelPort
	}
	return &Resolver{
		tunnelHost: host,
		tunnelPort: port,
		cache:      map[string]*entry{},
		inflight:   map[string]chan struct{}{},
	}
}

// Session returns an enriched DeviceEntry for udid, ready to pass to
// instruments / installationproxy / screenshotr. Cached per-UDID with a
// short TTL; on transport errors the caller should also Invalidate.
func (r *Resolver) Session(udid string) (ios.DeviceEntry, error) {
	if udid == "" {
		return ios.DeviceEntry{}, errors.New("goios: empty UDID")
	}

	for {
		r.mu.Lock()
		if e, ok := r.cache[udid]; ok && time.Since(e.resolved) < cacheTTL {
			dev := e.dev
			r.mu.Unlock()
			return dev, nil
		}
		// If another goroutine is already resolving this UDID, wait for
		// it to finish, then re-check the cache.
		if wait, busy := r.inflight[udid]; busy {
			r.mu.Unlock()
			<-wait
			continue
		}
		// We're the resolver. Mark inflight and drop the lock for the
		// (potentially slow) resolution.
		done := make(chan struct{})
		r.inflight[udid] = done
		r.mu.Unlock()

		dev, err := r.resolve(udid)

		r.mu.Lock()
		delete(r.inflight, udid)
		close(done)
		if err == nil {
			r.cache[udid] = &entry{dev: dev, resolved: time.Now()}
		}
		r.mu.Unlock()
		return dev, err
	}
}

// Invalidate drops any cached DeviceEntry for udid. Call after a
// transport-level failure or when the tunnel daemon restarts.
func (r *Resolver) Invalidate(udid string) {
	r.mu.Lock()
	delete(r.cache, udid)
	r.mu.Unlock()
}

// resolve performs the tunnel-info → RSD-handshake → enriched-DeviceEntry
// dance. Mirrors what go-ios's CLI does in main.go's
// deviceWithRsdProvider, with all errors wrapped for context.
//
// On every error path, fires wedge.Capture so the next observed
// wedge has a discrete trigger event with usbmux/CoreDevice
// snapshots correlated to the failing call (🎯T68.1). Capture is
// internally throttled, so high-frequency churn won't flood the log.
//
// Start/end events are logged at Info — bounded by the 60s session
// cache, so this is event-shaped (one fresh handshake per device
// per minute under steady load), not per-RPC noise.
func (r *Resolver) resolve(udid string) (ios.DeviceEntry, error) {
	started := time.Now()
	slog.Info("goios resolve: start", "udid", udid)

	fail := func(stage string, err error) error {
		wedge.Capture(udid, "goios.resolve."+stage)
		slog.Error("goios resolve: failed", "udid", udid, "stage", stage,
			"duration_ms", time.Since(started).Milliseconds(), "error", err.Error())
		return err
	}

	dev, err := ios.GetDevice(udid)
	if err != nil {
		return ios.DeviceEntry{}, fail("GetDevice", fmt.Errorf("goios: get device %s: %w", udid, err))
	}
	info, err := tunnel.TunnelInfoForDevice(udid, r.tunnelHost, r.tunnelPort)
	if err != nil {
		return ios.DeviceEntry{}, fail("TunnelInfo", fmt.Errorf(
			"goios: tunnel info for %s from %s:%d: %w (is `ios tunnel start` running?)",
			udid, r.tunnelHost, r.tunnelPort, err))
	}
	dev.UserspaceTUN = info.UserspaceTUN
	dev.UserspaceTUNHost = r.tunnelHost
	dev.UserspaceTUNPort = info.UserspaceTUNPort

	rsdService, err := ios.NewWithAddrPortDevice(info.Address, info.RsdPort, dev)
	if err != nil {
		return ios.DeviceEntry{}, fail("NewRSD",
			fmt.Errorf("goios: connect RSD %s:%d: %w", info.Address, info.RsdPort, err))
	}
	defer rsdService.Close()
	rsdProvider, err := rsdService.Handshake()
	if err != nil {
		return ios.DeviceEntry{}, fail("Handshake",
			fmt.Errorf("goios: RSD handshake for %s: %w", udid, err))
	}
	enriched, err := ios.GetDeviceWithAddress(udid, info.Address, rsdProvider)
	if err != nil {
		return ios.DeviceEntry{}, fail("Enrich",
			fmt.Errorf("goios: enrich device %s: %w", udid, err))
	}
	enriched.UserspaceTUN = dev.UserspaceTUN
	enriched.UserspaceTUNHost = dev.UserspaceTUNHost
	enriched.UserspaceTUNPort = dev.UserspaceTUNPort
	slog.Info("goios resolve: ok", "udid", udid,
		"duration_ms", time.Since(started).Milliseconds())
	return enriched, nil
}
