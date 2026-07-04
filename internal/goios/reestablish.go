// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielpaulus/go-ios/ios/tunnel"
)

// ErrStaleTunnel classifies the recoverable, spyder-side fault that
// 🎯T89 self-heals: the device is attached at the usbmux/lockdown layer
// but the `ios tunnel start --userspace` daemon has no live RemoteXPC
// tunnel for it. resolve() wraps a tunnel-info failure with this
// sentinel — and only after ios.GetDevice has already succeeded, so
// reaching it structurally means "attached but tunnel stale/missing",
// never "device gone". Callers use errors.Is(err, ErrStaleTunnel) to
// decide whether to re-establish and retry.
var ErrStaleTunnel = errors.New("goios: stale or missing device tunnel")

// Re-establish tunables. Declared as vars so tests can shrink them.
var (
	// reestablishTimeout bounds how long ReestablishTunnel waits for the
	// daemon to rebuild a tunnel after we force-drop it. The go-ios
	// daemon reconciles every 1s (UpdateTunnels) and a rebuild handshake
	// can take up to its ~10s startTunnelTimeout, so this is generous
	// headroom, not a tight budget.
	reestablishTimeout = 12 * time.Second

	// reestablishPollInterval is the gap between GET /tunnel/{udid} polls
	// while waiting for the daemon to rebuild.
	reestablishPollInterval = 250 * time.Millisecond
)

// deleteTunnelTimeout bounds the single DELETE call to the daemon.
const deleteTunnelTimeout = 5 * time.Second

// ReestablishTunnel forces a fresh RemoteXPC tunnel for udid and returns
// once the daemon has rebuilt one (or reestablishTimeout elapses).
//
// spyder does not build tunnels itself — the bundled `ios tunnel start
// --userspace` daemon owns them and reconciles every second. But a
// tunnel whose device lifeline silently wedged (a USB hub power-cycle
// where the stale connection hangs rather than errors) can linger as a
// zombie the daemon still considers alive, or be absent entirely after
// re-enumeration. Both surface to consumers as empty/malformed
// tunnel-info: the daemon's `FindTunnel` returns empty, its handler
// writes a 404 with an empty body, and go-ios's TunnelInfoForDevice —
// which never checks the status code — fails json.Unmarshal with
// "unexpected end of JSON input".
//
// The one mutating lever go-ios exposes over HTTP is DELETE
// /tunnel/{udid} (tm.stopTunnel). We use it to force-drop whatever is
// there, then poll until the daemon's reconcile loop rebuilds a live
// tunnel — the device is still attached, so it will — or the timeout
// fires. Our own cached DeviceEntry is dropped first so the subsequent
// resolve re-handshakes against the fresh tunnel.
//
// This is the shared primitive 🎯T89.1 factors out; 🎯T89.2 reuses it
// on usbmux attach/detach and 🎯T90.1's device-tunnel recovery policy
// reuses it rather than duplicating the DELETE/poll dance.
func (r *Resolver) ReestablishTunnel(udid string) error {
	r.Invalidate(udid)
	if err := deleteTunnel(r.tunnelHost, r.tunnelPort, udid); err != nil {
		// A transport failure reaching the daemon is worth logging, but
		// not fatal on its own: the daemon may still rebuild on its next
		// tick, so fall through to the poll and let the timeout decide.
		slog.Warn("goios: force-drop tunnel failed; still waiting for daemon rebuild",
			"udid", udid, "error", err.Error())
	}

	deadline := time.Now().Add(reestablishTimeout)
	for {
		info, err := tunnel.TunnelInfoForDevice(udid, r.tunnelHost, r.tunnelPort)
		if err == nil && info.Address != "" {
			slog.Info("goios: tunnel re-established", "udid", udid, "address", info.Address)
			return nil
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("tunnel-info still empty (no address)")
			}
			return fmt.Errorf(
				"goios: re-establish tunnel for %s: daemon did not rebuild within %s: %w",
				udid, reestablishTimeout, err)
		}
		time.Sleep(reestablishPollInterval)
	}
}

// deleteTunnel issues DELETE /tunnel/{udid} to the tunnel daemon,
// forcing it to stop (and thus, on its next 1s reconcile, rebuild) the
// device's tunnel. A 404 means there was no tunnel to drop — not an
// error here, since the daemon will still build one for an attached
// device on its next tick.
func deleteTunnel(host string, port int, udid string) error {
	url := fmt.Sprintf("http://%s:%d/tunnel/%s", host, port, udid)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("goios: build DELETE tunnel request for %s: %w", udid, err)
	}
	client := &http.Client{Timeout: deleteTunnelTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("goios: DELETE tunnel %s: %w", udid, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("goios: DELETE tunnel %s: unexpected status %d", udid, resp.StatusCode)
	}
	return nil
}
