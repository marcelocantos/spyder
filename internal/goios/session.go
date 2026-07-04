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
	"strconv"
	"strings"
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

	// resolveTimeout bounds a single tunnel-info → RSD-handshake →
	// enrich sequence. go-ios's RSD HTTP dial (ios.NewWithAddrPortDevice)
	// blocks indefinitely when the device's tunnel is wedged — a real
	// failure mode observed in the field — which would otherwise hang
	// every DTX-backed caller (screenshot, oslog stream, crashes) forever.
	// Bounding it lets those tools surface a structured degraded error
	// "rather than hanging" (🎯T72.4). A healthy handshake is sub-second,
	// so this is generous headroom, not a tight budget.
	resolveTimeout = 15 * time.Second
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

	// resolveFn and reestablishFn are indirections over r.resolve and
	// r.ReestablishTunnel. They exist so the stale-tunnel
	// detect → re-establish → retry-once wiring (🎯T89.1) is unit-testable
	// without a real device or a live tunnel daemon, and they are the
	// first, minimal instance of the injectable probe/event seams
	// 🎯T90.1 generalises. Production code always uses the defaults set
	// in New; only same-package tests override them.
	resolveFn     func(udid string) (ios.DeviceEntry, int, error)
	reestablishFn func(udid string) error
}

type entry struct {
	dev      ios.DeviceEntry
	major    int // iOS major version (e.g. 16, 17). 0 if unknown.
	resolved time.Time
}

// ParseIOSMajor extracts the major version number from a ProductVersion
// string such as "17.4.1" or "16.7". Returns 0 if the string can't be
// parsed; callers should treat 0 as "unknown — assume modern path".
func ParseIOSMajor(productVersion string) int {
	if productVersion == "" {
		return 0
	}
	dot := strings.IndexByte(productVersion, '.')
	head := productVersion
	if dot > 0 {
		head = productVersion[:dot]
	}
	n, err := strconv.Atoi(head)
	if err != nil {
		return 0
	}
	return n
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
	r := &Resolver{
		tunnelHost: host,
		tunnelPort: port,
		cache:      map[string]*entry{},
		inflight:   map[string]chan struct{}{},
	}
	r.resolveFn = r.resolve
	r.reestablishFn = r.ReestablishTunnel
	return r
}

// Session returns an enriched DeviceEntry for udid, ready to pass to
// instruments / installationproxy / screenshotr. Cached per-UDID with a
// short TTL; on transport errors the caller should also Invalidate.
//
// For iOS ≤16 devices the returned DeviceEntry is the bare lockdown
// entry (no RSD enrichment, since RSD doesn't exist on those devices).
// Callers that need to branch on iOS major version should use
// SessionWithVersion instead.
func (r *Resolver) Session(udid string) (ios.DeviceEntry, error) {
	dev, _, err := r.SessionWithVersion(udid)
	return dev, err
}

// SessionWithVersion is like Session but also returns the device's iOS
// major version (e.g. 16, 17). Major == 0 means "unknown" — callers
// should treat that as "assume the modern path", since not knowing the
// version is more likely a fresh-device timing issue than an iOS ≤16
// device that's been online long enough to enumerate.
func (r *Resolver) SessionWithVersion(udid string) (ios.DeviceEntry, int, error) {
	if udid == "" {
		return ios.DeviceEntry{}, 0, errors.New("goios: empty UDID")
	}

	for {
		r.mu.Lock()
		if e, ok := r.cache[udid]; ok && time.Since(e.resolved) < cacheTTL {
			dev, major := e.dev, e.major
			r.mu.Unlock()
			return dev, major, nil
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

		dev, major, err := r.resolveWithRecovery(udid)

		r.mu.Lock()
		delete(r.inflight, udid)
		close(done)
		if err == nil {
			r.cache[udid] = &entry{dev: dev, major: major, resolved: time.Now()}
		}
		r.mu.Unlock()
		return dev, major, err
	}
}

// Invalidate drops any cached DeviceEntry for udid. Call after a
// transport-level failure or when the tunnel daemon restarts.
func (r *Resolver) Invalidate(udid string) {
	r.mu.Lock()
	delete(r.cache, udid)
	r.mu.Unlock()
}

// TunnelInfoRetryAttempts is how many times r.tunnelInfoWithRetry will
// call tunnel.TunnelInfoForDevice before giving up. Declared as a var
// so tests can lower it. Three attempts at 200ms/500ms/1s backoff
// covers the typical "RSD tunnel still settling after device connect"
// window without inflating cold-call latency on a healthy daemon
// (🎯T84).
var TunnelInfoRetryAttempts = 3

// tunnelInfoBackoffs is the per-attempt delay applied BEFORE the
// retry. The slice length should be >= TunnelInfoRetryAttempts-1; the
// last entry is reused if needed.
var tunnelInfoBackoffs = []time.Duration{
	200 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
}

// tunnelInfoWithRetry wraps tunnel.TunnelInfoForDevice with a bounded
// retry. The registry's per-device handler racing with go-ios's
// internal tunnel state can briefly emit "unexpected end of JSON" /
// EOF responses during settling (🎯T84); a retry with backoff
// recovers without the caller having to know. Non-transient failures
// (e.g. daemon unreachable) still surface — the call returns the
// last error and the supervisor's health probe handles the wedge
// case independently.
func (r *Resolver) tunnelInfoWithRetry(udid string) (tunnel.Tunnel, error) {
	var lastErr error
	for attempt := 0; attempt < TunnelInfoRetryAttempts; attempt++ {
		if attempt > 0 {
			delay := tunnelInfoBackoffs[len(tunnelInfoBackoffs)-1]
			if attempt-1 < len(tunnelInfoBackoffs) {
				delay = tunnelInfoBackoffs[attempt-1]
			}
			time.Sleep(delay)
		}
		info, err := tunnel.TunnelInfoForDevice(udid, r.tunnelHost, r.tunnelPort)
		if err == nil {
			if attempt > 0 {
				slog.Info("goios: tunnel-info recovered after retry",
					"udid", udid, "attempts", attempt+1)
			}
			return info, nil
		}
		lastErr = err
	}
	return tunnel.Tunnel{}, lastErr
}

// resolveWithRecovery runs one resolve and, if it fails specifically
// with a stale-tunnel fault (the device is attached but the daemon has
// no live tunnel — see ErrStaleTunnel), re-establishes the tunnel and
// retries the resolve exactly once before surfacing any error (🎯T89.1).
//
// The re-establish + retry happen while SessionWithVersion still holds
// the per-UDID inflight gate, so concurrent callers wait for the
// recovered result rather than each triggering their own recovery.
//
// Non-stale failures (daemon unreachable, RSD handshake error, timeout)
// are returned as-is: those are not the "force-drop and let the daemon
// rebuild" failure mode, and retrying them here would only add latency.
func (r *Resolver) resolveWithRecovery(udid string) (ios.DeviceEntry, int, error) {
	dev, major, err := r.resolveWithTimeout(udid)
	if err == nil || !errors.Is(err, ErrStaleTunnel) {
		return dev, major, err
	}
	slog.Warn("goios: stale tunnel detected; re-establishing and retrying once", "udid", udid)
	if reErr := r.reestablishFn(udid); reErr != nil {
		slog.Error("goios: tunnel re-establish failed; surfacing original error",
			"udid", udid, "error", reErr.Error())
		return dev, major, err
	}
	return r.resolveWithTimeout(udid)
}

// resolveWithTimeout runs resolve under resolveTimeout. go-ios's RSD dial
// has no internal deadline, so a wedged tunnel blocks resolve forever;
// this bounds it. On timeout the in-flight resolve goroutine is abandoned
// — it sends to a buffered channel that's discarded, and resolve's own
// deferred Close cleans up the RSD service when (if) it finally unblocks.
func (r *Resolver) resolveWithTimeout(udid string) (ios.DeviceEntry, int, error) {
	type result struct {
		dev   ios.DeviceEntry
		major int
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		dev, major, err := r.resolveFn(udid)
		ch <- result{dev, major, err}
	}()
	select {
	case res := <-ch:
		return res.dev, res.major, res.err
	case <-time.After(resolveTimeout):
		wedge.Capture(udid, "goios.resolve.timeout")
		slog.Error("goios resolve: timed out", "udid", udid, "timeout", resolveTimeout.String())
		return ios.DeviceEntry{}, 0, fmt.Errorf(
			"goios: RSD session for %s timed out after %s (tunnel/usbmuxd may be wedged)",
			udid, resolveTimeout)
	}
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
func (r *Resolver) resolve(udid string) (ios.DeviceEntry, int, error) {
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
		return ios.DeviceEntry{}, 0, fail("GetDevice", fmt.Errorf("goios: get device %s: %w", udid, err))
	}

	// Detect iOS major version up front. Lockdown's GetValues works on
	// both eras (USBMux path, sub-100ms). If the device is iOS ≤16 we
	// skip the tunnel/RSD dance entirely — those services don't exist on
	// that path and the bare lockdown DeviceEntry is what the legacy
	// instruments / installationproxy / screenshotr services need.
	major := 0
	if values, gvErr := ios.GetValues(dev); gvErr == nil {
		major = ParseIOSMajor(values.Value.ProductVersion)
	}
	if major != 0 && major < 17 {
		slog.Info("goios resolve: ok (lockdown-only, no tunnel)",
			"udid", udid, "ios_major", major,
			"duration_ms", time.Since(started).Milliseconds())
		return dev, major, nil
	}

	info, err := r.tunnelInfoWithRetry(udid)
	if err != nil {
		// GetDevice above already succeeded, so the device is attached at
		// the usbmux/lockdown layer; a tunnel-info failure here therefore
		// means "attached but tunnel stale/missing". Tag it ErrStaleTunnel
		// so resolveWithRecovery can force-drop-and-retry once (🎯T89.1).
		return ios.DeviceEntry{}, major, fail("TunnelInfo", fmt.Errorf(
			"%w: tunnel info for %s from %s:%d: %w (is `ios tunnel start` running?)",
			ErrStaleTunnel, udid, r.tunnelHost, r.tunnelPort, err))
	}
	dev.UserspaceTUN = info.UserspaceTUN
	dev.UserspaceTUNHost = r.tunnelHost
	dev.UserspaceTUNPort = info.UserspaceTUNPort

	rsdService, err := ios.NewWithAddrPortDevice(info.Address, info.RsdPort, dev)
	if err != nil {
		return ios.DeviceEntry{}, major, fail("NewRSD",
			fmt.Errorf("goios: connect RSD %s:%d: %w", info.Address, info.RsdPort, err))
	}
	defer rsdService.Close()
	rsdProvider, err := rsdService.Handshake()
	if err != nil {
		return ios.DeviceEntry{}, major, fail("Handshake",
			fmt.Errorf("goios: RSD handshake for %s: %w", udid, err))
	}
	enriched, err := ios.GetDeviceWithAddress(udid, info.Address, rsdProvider)
	if err != nil {
		return ios.DeviceEntry{}, major, fail("Enrich",
			fmt.Errorf("goios: enrich device %s: %w", udid, err))
	}
	enriched.UserspaceTUN = dev.UserspaceTUN
	enriched.UserspaceTUNHost = dev.UserspaceTUNHost
	enriched.UserspaceTUNPort = dev.UserspaceTUNPort
	slog.Info("goios resolve: ok", "udid", udid, "ios_major", major,
		"duration_ms", time.Since(started).Milliseconds())
	return enriched, major, nil
}
