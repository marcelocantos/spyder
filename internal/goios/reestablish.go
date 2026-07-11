// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielpaulus/go-ios/ios/tunnel"
)

// Reestablish tunables — vars so tests can shrink them.
var (
	// ReestablishPollInterval is the cadence of tunnel-info polls after DELETE.
	ReestablishPollInterval = 200 * time.Millisecond
	// ReestablishTimeout bounds how long we wait for the daemon's
	// UpdateTunnels loop to rebuild a live tunnel after DELETE.
	ReestablishTimeout = 10 * time.Second
)

// httpClient is used for DELETE /tunnel/{udid}. Overridable in tests.
var httpClient = http.DefaultClient

// ReestablishTunnel forces the tunnel daemon to drop any zombie tunnel for
// udid (DELETE /tunnel/{udid}), then polls GET until a live tunnel appears
// (or times out). Always invalidates the local session cache so the next
// Session() re-handshakes RSD against the rebuilt tunnel.
//
// This is the shared primitive for 🎯T89.1 (lazy consumer recovery) and
// 🎯T89.2 (proactive usbmux attach/detach recovery).
func (r *Resolver) ReestablishTunnel(udid string) error {
	if udid == "" {
		return fmt.Errorf("goios: reestablish: empty UDID")
	}
	r.Invalidate(udid)

	if err := r.deleteTunnel(udid); err != nil {
		// DELETE of a missing tunnel often returns 404 with empty body —
		// still treat as success so we can proceed to poll for rebuild.
		slog.Info("goios: tunnel DELETE completed with error (continuing to poll)",
			"udid", udid, "error", err.Error())
	} else {
		slog.Info("goios: tunnel DELETE ok", "udid", udid)
	}

	deadline := time.Now().Add(ReestablishTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		info, err := tunnel.TunnelInfoForDevice(udid, r.tunnelHost, r.tunnelPort)
		if err == nil && info.Address != "" && info.RsdPort != 0 {
			slog.Info("goios: tunnel re-established",
				"udid", udid, "address", info.Address, "rsd_port", info.RsdPort)
			return nil
		}
		lastErr = err
		time.Sleep(ReestablishPollInterval)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("tunnel info empty after rebuild wait")
	}
	return fmt.Errorf("goios: re-establish tunnel for %s timed out after %s: %w",
		udid, ReestablishTimeout, lastErr)
}

// DropTunnel issues DELETE /tunnel/{udid} and invalidates the cache without
// waiting for rebuild. Used on usbmux detach (🎯T89.2) when the device is gone.
func (r *Resolver) DropTunnel(udid string) {
	if udid == "" {
		return
	}
	r.Invalidate(udid)
	if err := r.deleteTunnel(udid); err != nil {
		slog.Info("goios: tunnel DROP on detach", "udid", udid, "error", err.Error())
		return
	}
	slog.Info("goios: tunnel DROP on detach ok", "udid", udid)
}

func (r *Resolver) deleteTunnel(udid string) error {
	url := fmt.Sprintf("http://%s:%d/tunnel/%s", r.tunnelHost, r.tunnelPort, udid)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	// 200 / 204 / 404 are all fine: 404 means "no tunnel to stop".
	if resp.StatusCode >= 500 {
		return fmt.Errorf("DELETE %s: status %d", url, resp.StatusCode)
	}
	return nil
}
