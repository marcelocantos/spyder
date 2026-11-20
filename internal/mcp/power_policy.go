// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/spyder/internal/battery"
	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/notify"
)

// Lab power policy (🎯T194).
//
// Heavy loads are allowed to outrun a hub port. spyder steps in only
// when a device is heading for empty: at battery.DrainFloor and still
// falling, it stops the device's apps and holds it out of new leases and
// launches until it is back to battery.DrainResume. Separately, apps
// spyder launched are stopped when their lease ends or after
// idleStopAfter without a lease or a verb naming the device. spyder never
// sends sleep, power or lock input.

// idleStopAfter is how long a device with apps spyder launched may go
// without a lease or a verb naming it before those apps are stopped.
const idleStopAfter = 10 * time.Minute

// stopAppsTimeout bounds one stop pass over a device.
const stopAppsTimeout = 45 * time.Second

// heldLaunchVerbs start or restart an app. They are refused on a device
// held for recharge unless the caller passes force=true.
var heldLaunchVerbs = map[string]bool{
	"launch_app":     true,
	"ensure_session": true,
	"app_spawn":      true,
	"deploy_app":     true,
	"launch_player":  true,
}

// SetLaunchRegistryPath makes the launch registry persistent. The daemon
// calls it once before serving.
func (h *Handler) SetLaunchRegistryPath(path string) {
	h.launched = newLaunchRegistry(path)
}

// recordLaunch notes a launch for since=launch log queries and for the
// idle policy.
func (h *Handler) recordLaunch(id, bundleID string) {
	now := time.Now()
	h.mu.Lock()
	h.launchTimes[launchKey{deviceID: id, bundleID: bundleID}] = now
	h.mu.Unlock()
	h.launched.Note(id, bundleID, now)
}

// deviceIDForRef maps an alias or id to the platform id the adapters
// and the battery sampler use.
func (h *Handler) deviceIDForRef(ref string) string {
	if h.inventory == nil {
		return ref
	}
	entry, ok := h.inventory.Lookup(ref)
	if !ok {
		return ref
	}
	switch {
	case entry.AndroidSerial != "":
		return entry.AndroidSerial
	case entry.IOSUUID != "":
		return entry.IOSUUID
	}
	return ref
}

// beforeVerb runs ahead of every verb, from MCP/REST and from scripts.
// It marks the named device as in use and refuses app launches on a
// device held for recharge.
func (h *Handler) beforeVerb(name string, args map[string]any) *mcpgo.CallToolResult {
	dev := deviceArg(args)
	if dev == "" {
		return nil
	}
	id := h.deviceIDForRef(dev)
	h.launched.Touch(id, time.Now())
	if !heldLaunchVerbs[name] || optBoolArg(args, "force", false) {
		return nil
	}
	if tr := h.trend(id); tr.Held() {
		res, _ := toolErr("needs_setup: %s is held for recharge: battery %s. Pass force=true to launch anyway.", dev, tr.Reason)
		return res
	}
	return nil
}

func (h *Handler) trend(id string) *device.PowerTrend {
	h.powerMu.Lock()
	defer h.powerMu.Unlock()
	return h.trends[id]
}

// leaseRefusal says why a device should not be leased now, or "".
func (h *Handler) leaseRefusal(id string) string {
	h.powerMu.Lock()
	defer h.powerMu.Unlock()
	tr := h.trends[id]
	if tr.Held() {
		return "battery " + tr.Reason
	}
	r, ok := h.lastPower[id]
	if !ok || time.Since(r.at) > powerFresh {
		return ""
	}
	var why []string
	for _, issue := range r.power.Issues {
		switch {
		case strings.HasPrefix(issue, device.PowerLockedSecure+":"),
			strings.HasPrefix(issue, device.PowerOffline+":"):
			why = append(why, issue)
		case strings.HasPrefix(issue, device.PowerNotPowered+":") && tr != nil && tr.Level < battery.DrainResume:
			why = append(why, fmt.Sprintf("%s (battery at %d%%)", issue, tr.Level))
		}
	}
	return strings.Join(why, "; ")
}

// afterBatteryTick updates each sampled device's drain trend, publishes
// changes, steps in on devices that turned critical, and runs the idle
// policy. It runs on the battery sampler's goroutine once a minute.
func (h *Handler) afterBatteryTick(now time.Time, samples []battery.Sample) {
	h.seedLevels(now)

	type change struct {
		id, from, to string
		trend        *device.PowerTrend
		healthFrom   string
		health       *device.Power
	}
	var changes []change
	h.powerMu.Lock()
	for _, sm := range samples {
		p, ok := battery.PointFromSample(sm)
		if !ok {
			continue
		}
		pts := append(h.levels[sm.DeviceID], p)
		cut := 0
		for cut < len(pts) && now.Sub(pts[cut].TS) > battery.DrainLookback {
			cut++
		}
		pts = pts[cut:]
		h.levels[sm.DeviceID] = pts
		tr := battery.Assess(pts, now)
		prev := h.trends[sm.DeviceID]
		h.trends[sm.DeviceID] = tr
		if r, ok := h.lastPower[sm.DeviceID]; ok && tr != nil {
			r.power.Trend = tr
			h.lastPower[sm.DeviceID] = r
		}
		c := change{id: sm.DeviceID, trend: tr}
		if tr != nil && (prev == nil || prev.State != tr.State) {
			c.from, c.to = device.DrainOK, tr.State
			if prev != nil {
				c.from = prev.State
			}
		}
		if sm.Power != nil && h.lastHealth[sm.DeviceID] != sm.Power.Health {
			c.healthFrom = h.lastHealth[sm.DeviceID]
			c.health = sm.Power
			h.lastHealth[sm.DeviceID] = sm.Power.Health
		}
		if c.to != "" || c.health != nil {
			changes = append(changes, c)
		}
	}
	h.powerMu.Unlock()

	for _, c := range changes {
		// A first sighting in the ok state is not news.
		if c.to != "" && !(c.from == device.DrainOK && c.to == device.DrainOK) {
			slog.Info("power: drain state changed", "device", c.id, "from", c.from, "state", c.to, "level", c.trend.Level, "reason", c.trend.Reason)
			h.publishNotification(notify.Event{Event: "power.changed", DeviceID: c.id, Data: map[string]string{
				"from": c.from, "state": c.to, "level": strconv.Itoa(c.trend.Level), "reason": c.trend.Reason,
			}})
		}
		if c.to == device.DrainCritical {
			go h.stopApps(c.id, h.launched.OnDevice(c.id), "battery "+c.trend.Reason, true)
		}
		if c.health != nil && !(c.healthFrom == "" && c.health.Health == device.PowerOK) {
			slog.Info("power: health changed", "device", c.id, "from", c.healthFrom, "state", c.health.Health, "issues", c.health.Issues)
			h.publishNotification(notify.Event{Event: "power.health", DeviceID: c.id, Data: map[string]string{
				"from": c.healthFrom, "state": c.health.Health, "issues": strings.Join(c.health.Issues, "; "),
			}})
		}
	}
	h.sweepIdle(now)
}

// seedLevels loads recent history once, so a restarted daemon remembers
// holds and trends.
func (h *Handler) seedLevels(now time.Time) {
	h.powerMu.Lock()
	if h.levels != nil {
		h.powerMu.Unlock()
		return
	}
	h.levels = map[string][]battery.Point{}
	if h.trends == nil {
		h.trends = map[string]*device.PowerTrend{}
	}
	if h.lastHealth == nil {
		h.lastHealth = map[string]string{}
	}
	h.powerMu.Unlock()

	h.mu.Lock()
	store := h.batteryStore
	h.mu.Unlock()
	if store == nil {
		return
	}
	samples, err := store.Query(battery.Query{Since: now.Add(-battery.DrainLookback), Until: now})
	if err != nil {
		slog.Warn("power: history seed failed", "error", err)
		return
	}
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].TS.Before(samples[j].TS) })
	h.powerMu.Lock()
	defer h.powerMu.Unlock()
	for _, sm := range samples {
		if p, ok := battery.PointFromSample(sm); ok {
			h.levels[sm.DeviceID] = append(h.levels[sm.DeviceID], p)
		}
	}
}

// sweepIdle stops apps spyder launched on devices nobody has leased or
// named for idleStopAfter (🎯T194.2).
func (h *Handler) sweepIdle(now time.Time) {
	if h.reservations != nil {
		active, err := h.reservations.List()
		if err != nil {
			slog.Warn("power: idle check skipped; reservations unavailable", "error", err)
			return
		}
		for _, r := range active {
			h.launched.Touch(h.deviceIDForRef(r.Device), now)
		}
	}
	byDevice := map[string][]string{}
	for _, a := range h.launched.Idle(now, idleStopAfter) {
		byDevice[a.DeviceID] = append(byDevice[a.DeviceID], a.BundleID)
	}
	for id, bundles := range byDevice {
		// Restart the idle clock so a slow stop is not queued twice.
		h.launched.Touch(id, now)
		go h.stopApps(id, bundles, fmt.Sprintf("idle: no lease or spyder use for %s", idleStopAfter), false)
	}
}

// stopApps stops the given apps on a device and, when foreground is set
// on Android, a third-party app in the foreground. It publishes
// power.stepped_in with what it stopped.
func (h *Handler) stopApps(id string, bundles []string, reason string, foreground bool) {
	h.mu.Lock()
	adapter, platform, resolved, err := h.resolveAdapter(id)
	h.mu.Unlock()
	if err != nil {
		slog.Warn("power: stop apps: device not resolved", "device", id, "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopAppsTimeout)
	defer cancel()

	var stopped, failures []string
	stop := func(bundle string) {
		err := device.TerminateAppContext(ctx, adapter, resolved, bundle)
		if err != nil && !strings.Contains(err.Error(), "not running") {
			failures = append(failures, bundle+": "+err.Error())
			return
		}
		h.launched.Forget(id, bundle)
		stopped = append(stopped, bundle)
	}
	for _, b := range bundles {
		stop(b)
	}
	if foreground && platform == "android" {
		if a, ok := adapter.(interface {
			ThirdPartyForeground(string) (string, error)
		}); ok {
			pkg, err := a.ThirdPartyForeground(resolved)
			switch {
			case err != nil:
				failures = append(failures, "foreground: "+err.Error())
			case pkg != "" && !containsString(stopped, pkg):
				stop(pkg)
			}
		}
	}
	if len(stopped) == 0 && len(failures) == 0 {
		return
	}
	slog.Info("power: stopped apps", "device", id, "reason", reason, "stopped", stopped, "errors", failures)
	h.publishNotification(notify.Event{Event: "power.stepped_in", DeviceID: id, Data: map[string]string{
		"reason": reason, "stopped": strings.Join(stopped, ","), "errors": strings.Join(failures, "; "),
	}})
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
