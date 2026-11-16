// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// launchRegistry remembers which apps spyder launched on which device,
// so the idle policy can stop them when nobody is using the device
// (🎯T194.2). It is saved to disk so a daemon restart does not forget a
// game left running. Apps spyder did not launch are never in it.
type launchRegistry struct {
	mu   sync.Mutex
	path string // "" keeps it in memory (tests)
	// apps maps device id → bundle id → entry.
	apps map[string]map[string]launchEntry
}

type launchEntry struct {
	LaunchedAt time.Time `json:"launched_at"`
	// LastActive is the last time the device was leased or named by a
	// verb. The idle policy measures idleness from it.
	LastActive time.Time `json:"last_active"`
}

type launchedApp struct {
	DeviceID string
	BundleID string
}

func newLaunchRegistry(path string) *launchRegistry {
	r := &launchRegistry{path: path, apps: map[string]map[string]launchEntry{}}
	if path == "" {
		return r
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		slog.Warn("launch registry: read failed", "path", path, "error", err)
	default:
		if err := json.Unmarshal(data, &r.apps); err != nil {
			slog.Warn("launch registry: parse failed; starting empty", "path", path, "error", err)
			r.apps = map[string]map[string]launchEntry{}
		}
	}
	return r
}

// Note records a launch.
func (r *launchRegistry) Note(deviceID, bundleID string, now time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.apps[deviceID] == nil {
		r.apps[deviceID] = map[string]launchEntry{}
	}
	r.apps[deviceID][bundleID] = launchEntry{LaunchedAt: now, LastActive: now}
	r.saveLocked()
}

// Forget drops an app that has been stopped.
func (r *launchRegistry) Forget(deviceID, bundleID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.apps[deviceID][bundleID]; !ok {
		return
	}
	delete(r.apps[deviceID], bundleID)
	if len(r.apps[deviceID]) == 0 {
		delete(r.apps, deviceID)
	}
	r.saveLocked()
}

// Touch marks a device as in use. It is not saved: losing a few
// minutes of activity on restart only delays the idle stop.
func (r *launchRegistry) Touch(deviceID string, now time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for b, e := range r.apps[deviceID] {
		if now.After(e.LastActive) {
			e.LastActive = now
			r.apps[deviceID][b] = e
		}
	}
}

// OnDevice lists the bundles spyder launched on a device.
func (r *launchRegistry) OnDevice(deviceID string) []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for b := range r.apps[deviceID] {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// Idle lists apps whose device has been inactive for at least idle.
func (r *launchRegistry) Idle(now time.Time, idle time.Duration) []launchedApp {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []launchedApp
	for d, bundles := range r.apps {
		for b, e := range bundles {
			if now.Sub(e.LastActive) >= idle {
				out = append(out, launchedApp{DeviceID: d, BundleID: b})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeviceID != out[j].DeviceID {
			return out[i].DeviceID < out[j].DeviceID
		}
		return out[i].BundleID < out[j].BundleID
	})
	return out
}

// Devices lists device ids with launched apps.
func (r *launchRegistry) Devices() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.apps))
	for d := range r.apps {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func (r *launchRegistry) saveLocked() {
	if r.path == "" {
		return
	}
	data, err := json.MarshalIndent(r.apps, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		slog.Warn("launch registry: mkdir failed", "error", err)
		return
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		slog.Warn("launch registry: write failed", "error", err)
		return
	}
	if err := os.Rename(tmp, r.path); err != nil {
		slog.Warn("launch registry: rename failed", "error", err)
	}
}
