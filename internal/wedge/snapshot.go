// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package wedge captures diagnostic snapshots when spyder detects
// the usbmuxd third-party-visibility wedge — the macOS-side desync
// where `xcrun devicectl` sees an attached iOS device but go-ios's
// usbmux view does not. Recovery requires restarting usbmuxd; this
// package does NOT recover, it observes.
//
// Hook Capture(udid, trigger) at error-return sites that match the
// wedge signature (resolve failure, "Device not found" RPC error,
// transport timeout against a previously-resolved device). Each
// call probes both usbmuxd (in-process via go-ios's ListDevices)
// and CoreDevice (subprocess via `xcrun devicectl`), computes the
// discrepancy, slogs a structured event, and writes a JSON snapshot
// to ~/.spyder/wedge-snapshots/.
//
// Calls are throttled per process to one snapshot per minInterval —
// a sustained wedge produces many errors, but one snapshot per
// minute is enough to establish a trigger pattern without flooding.
//
// (🎯T68.1.)
package wedge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	goios_ios "github.com/danielpaulus/go-ios/ios"
	"github.com/marcelocantos/spyder/internal/paths"
)

const (
	minInterval             = 60 * time.Second
	devicectlTimeoutSeconds = 30
)

var (
	mu          sync.Mutex
	lastCapture time.Time
)

// Capture probes usbmuxd and CoreDevice for their current iOS-device
// lists, computes the discrepancy, and writes a diagnostic snapshot
// to ~/.spyder/wedge-snapshots/. Throttled to one snapshot per
// minInterval per process — safe to call from any error path.
//
// trigger names the call site that observed the wedge symptom
// (e.g. "goios.resolve", "ios.install", "ios.launch"). udid is the
// device the failing operation was targeting; pass "" if unknown.
func Capture(udid, trigger string) {
	mu.Lock()
	if time.Since(lastCapture) < minInterval {
		mu.Unlock()
		return
	}
	lastCapture = time.Now()
	mu.Unlock()

	usbmux, usbmuxErr := usbmuxUDIDs()
	devicectl, devicectlErr := devicectlUDIDs()
	snap := buildSnapshot(udid, trigger, usbmux, devicectl, usbmuxErr, devicectlErr)
	snap.log()
	snap.write()
}

// Snapshot is the diagnostic record written to disk and emitted via
// slog when Capture fires. Exported for tests and for any future
// in-process reader (e.g. an MCP tool that surfaces the latest
// snapshot for an LLM agent to reason about).
type Snapshot struct {
	Timestamp      time.Time `json:"timestamp"`
	Trigger        string    `json:"trigger"`
	TriggerUDID    string    `json:"trigger_udid,omitempty"`
	UsbmuxUDIDs    []string  `json:"usbmux_udids"`
	UsbmuxError    string    `json:"usbmux_error,omitempty"`
	DevicectlUDIDs []string  `json:"devicectl_udids"`
	DevicectlError string    `json:"devicectl_error,omitempty"`
	MissingFromMux []string  `json:"missing_from_usbmux,omitempty"`
	ExtraInMux     []string  `json:"extra_in_usbmux,omitempty"`
	Wedged         bool      `json:"wedged"`
}

// buildSnapshot composes a Snapshot from raw inputs. Pure function —
// the I/O calls live in Capture; this is the testable core.
func buildSnapshot(udid, trigger string, usbmux, devicectl []string, usbmuxErr, devicectlErr string) Snapshot {
	s := Snapshot{
		Timestamp:      time.Now().UTC(),
		Trigger:        trigger,
		TriggerUDID:    udid,
		UsbmuxUDIDs:    usbmux,
		DevicectlUDIDs: devicectl,
		UsbmuxError:    usbmuxErr,
		DevicectlError: devicectlErr,
	}
	s.MissingFromMux, s.ExtraInMux = diffUDIDs(usbmux, devicectl)
	// Wedge = devicectl saw a connected device that usbmux doesn't,
	// AND devicectl gave us a non-empty list at all (an empty
	// devicectl result is "nothing attached", not a wedge).
	s.Wedged = len(devicectl) > 0 && len(s.MissingFromMux) > 0
	return s
}

// diffUDIDs returns the device IDs in devicectl that usbmux is
// missing (the wedge signature), and the IDs in usbmux that
// devicectl doesn't see (the inverse — rarer, usually a stale
// usbmux cache).
func diffUDIDs(usbmux, devicectl []string) (missing, extra []string) {
	muxSet := map[string]bool{}
	for _, u := range usbmux {
		muxSet[u] = true
	}
	dcSet := map[string]bool{}
	for _, u := range devicectl {
		dcSet[u] = true
	}
	for _, u := range devicectl {
		if !muxSet[u] {
			missing = append(missing, u)
		}
	}
	for _, u := range usbmux {
		if !dcSet[u] {
			extra = append(extra, u)
		}
	}
	return missing, extra
}

func usbmuxUDIDs() ([]string, string) {
	list, err := goios_ios.ListDevices()
	if err != nil {
		return nil, err.Error()
	}
	udids := make([]string, 0, len(list.DeviceList))
	for _, dev := range list.DeviceList {
		if dev.Properties.SerialNumber != "" {
			udids = append(udids, dev.Properties.SerialNumber)
		}
	}
	return udids, ""
}

func devicectlUDIDs() ([]string, string) {
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(devicectlTimeoutSeconds+2)*time.Second)
	defer cancel()
	tmp, err := os.CreateTemp("", "spyder-wedge-devicectl-*.json")
	if err != nil {
		return nil, fmt.Sprintf("tempfile: %v", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)

	cmd := exec.CommandContext(ctx, "xcrun", "devicectl",
		"--timeout", fmt.Sprintf("%d", devicectlTimeoutSeconds),
		"list", "devices", "--quiet", "--json-output", path)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Sprintf("devicectl exec: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Sprintf("devicectl read: %v", err)
	}
	var parsed struct {
		Result struct {
			Devices []struct {
				ConnectionProperties struct {
					TunnelState string `json:"tunnelState"`
				} `json:"connectionProperties"`
				HardwareProperties struct {
					UDID string `json:"udid"`
				} `json:"hardwareProperties"`
			} `json:"devices"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Sprintf("devicectl parse: %v", err)
	}
	var udids []string
	for _, d := range parsed.Result.Devices {
		if d.ConnectionProperties.TunnelState == "connected" && d.HardwareProperties.UDID != "" {
			udids = append(udids, d.HardwareProperties.UDID)
		}
	}
	return udids, ""
}

func (s Snapshot) log() {
	level := slog.LevelInfo
	if s.Wedged {
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, "wedge: diagnostic snapshot",
		"trigger", s.Trigger,
		"udid", s.TriggerUDID,
		"wedged", s.Wedged,
		"usbmux", s.UsbmuxUDIDs,
		"devicectl", s.DevicectlUDIDs,
		"missing_from_usbmux", s.MissingFromMux,
		"usbmux_error", s.UsbmuxError,
		"devicectl_error", s.DevicectlError,
	)
}

func (s Snapshot) write() {
	dir := filepath.Join(paths.Base(), "wedge-snapshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("wedge: snapshot dir mkdir failed", "error", err)
		return
	}
	ts := s.Timestamp.Format("20060102T150405Z")
	tail := s.TriggerUDID
	if tail == "" {
		tail = "unknown"
	}
	file := filepath.Join(dir, fmt.Sprintf("%s-%s.json", ts, tail))
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		slog.Warn("wedge: snapshot marshal failed", "error", err)
		return
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		slog.Warn("wedge: snapshot write failed", "error", err, "path", file)
		return
	}
	slog.Info("wedge: snapshot written", "path", file, "wedged", s.Wedged)
}
