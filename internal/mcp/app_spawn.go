// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// app_spawn / games: the game launcher surface (🎯T92, 🎯T92.1, 🎯T117).
//
// Design (🎯T117): there is no mobile games registry. A mobile game IS an
// installed bundle on a device spyder already knows — the device adapters
// (go-ios installationproxy on iOS, adb on Android, the desktop process
// adapter) are the source of truth, and deploy_app is how a bundle gets
// there. So app_spawn resolves its target from what spyder already knows:
//
//   - session_id                    → factory spawn (T92.1, unchanged): ask a
//     server-mode session advertising spawn_instance to fork an instance.
//   - device + bundle_id, and the pair's live app-channel session is a
//     factory                       → factory spawn through that session.
//   - device + bundle_id otherwise  → device spawn (T117): launch the
//     installed bundle through the existing adapter launch plumbing
//     (reservation-gated, app-channel env injected) and wait for the app to
//     dial back, returning a ready session. If the pair already has a live
//     session, that session is returned (already_running=true) instead of
//     double-launching.
//   - nothing                       → unique-live-session fallback (factory).
//
// The invariant an agent can rely on: app_spawn always yields a live
// app-channel session for the named game, whatever the medium — it never
// has to choose between app_spawn and launch_app (launch_app remains the
// low-level fire-and-forget launch).
package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// spawnDeviceWait bounds the wait for a device-launched game to dial back
// and complete its app-channel hello. Mobile cold starts can take several
// seconds; 30s matches the factory SpawnInstance timeout and fits under
// DeadlineDeviceOp (60s).
const spawnDeviceWait = 30 * time.Second

// spawnDeviceResult is the JSON payload of a device-path app_spawn (🎯T117).
// The factory path keeps returning a bare sessionInfo for backwards
// compatibility with T92.1 callers.
type spawnDeviceResult struct {
	Device         string      `json:"device"`
	BundleID       string      `json:"bundle_id"`
	AlreadyRunning bool        `json:"already_running,omitempty"`
	Session        sessionInfo `json:"session"`
}

// handleAppSpawn starts a game session on any medium. See the package
// comment for the resolution order (factory session vs device launch).
func (h *Handler) handleAppSpawn(args map[string]any) (*mcpgo.CallToolResult, error) {
	if h.appChannel == nil {
		return toolErr("app channel not configured")
	}

	// 1. Explicit factory session id (T92.1, unchanged).
	if id := optString(args, "session_id"); id != "" {
		s, ok := h.appChannel.GetSession(id)
		if !ok {
			return toolErr("no such session: %s", id)
		}
		if s.Listener() != nil {
			s.Listener().Touch()
		}
		return h.spawnFromFactory(s, args)
	}

	dev := optString(args, "device")
	bundleID := optString(args, "bundle_id")
	if dev != "" && bundleID != "" {
		// 2. The pair already has a live session: a factory spawns through
		// it; anything else is the game itself, already up — return it.
		if s := h.liveSessionFor(dev, bundleID); s != nil {
			if s.Supports(appchannel.MethodSpawnInstance) {
				return h.spawnFromFactory(s, args)
			}
			return toolJSON(spawnDeviceResult{
				Device: dev, BundleID: bundleID,
				AlreadyRunning: true, Session: sessionInfoFrom(s),
			})
		}
		// 3. Device spawn (🎯T117): launch the installed bundle and wait
		// for its session.
		return h.spawnOnDevice(dev, bundleID, args)
	}

	// 4. No target named: unique-live-session fallback (factory).
	factory, errRes := h.requireSession(args)
	if errRes != nil {
		return errRes, nil
	}
	return h.spawnFromFactory(factory, args)
}

// spawnFromFactory asks a game-server factory session (one advertising
// spawn_instance) to fork a game instance and returns the new instance's
// session once it dials back (🎯T92.1). The instance dials the same
// app-channel listener the factory is on (127.0.0.1 for the local/
// server-mode case this path targets — LAN/dev only, per T91.4), so it
// connects as its own session with the full monitor surface.
func (h *Handler) spawnFromFactory(factory *appchannel.Session, args map[string]any) (*mcpgo.CallToolResult, error) {
	game, err := requireString(args, "game")
	if err != nil {
		return toolErr("%v", err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", factory.Port)
	inst, err := h.appChannel.SpawnInstance(context.Background(), factory,
		appchannel.SpawnRequest{Game: game, AppChannel: addr, InstanceID: optString(args, "instance_id")},
		30*time.Second)
	if err != nil {
		return toolErr("spawn: %v", err)
	}
	return toolJSON(sessionInfoFrom(inst))
}

// spawnOnDevice is the 🎯T117 device path: reservation-gated launch of an
// installed bundle through the existing adapter plumbing (the exact steps
// launch_app performs), then a wait for the app-channel session so the
// caller gets a ready session, not a fire-and-forget launch.
func (h *Handler) spawnOnDevice(dev, bundleID string, args map[string]any) (*mcpgo.CallToolResult, error) {
	if g := optString(args, "game"); g != "" {
		return toolErr("app_spawn: game=%q names a factory instance, but there is no live factory session for device=%s bundle_id=%s — on a device the bundle_id names the game; omit game", g, dev, bundleID)
	}
	owner := optString(args, "owner")
	env := optStringMap(args, "env")

	// Launch under h.mu like launch_app; released before the session wait
	// so a slow app start doesn't wedge unrelated tool calls.
	h.mu.Lock()
	if res := h.authorize(dev, owner); res != nil {
		h.mu.Unlock()
		return res, nil
	}
	adapter, platform, id, err := h.resolveAdapter(dev)
	if err != nil {
		h.mu.Unlock()
		return toolErr("%v", err)
	}
	// The bundle must already be installed — the device's installed
	// bundles ARE the mobile game catalog (🎯T117); deploy_app is the
	// install path.
	if _, installed, rerr := adapter.ResolveExecutable(id, bundleID); rerr != nil {
		h.mu.Unlock()
		return toolErr("app_spawn: check %s on %s: %v", bundleID, dev, rerr)
	} else if !installed {
		h.mu.Unlock()
		return toolErr("app_spawn: %s is not installed on %s — deploy_app it first (games(device=%q) lists installed bundles)", bundleID, dev, dev)
	}
	env, err = h.ensureAppChannelEnv(env, platform, id, bundleID)
	if err != nil {
		h.mu.Unlock()
		return toolErr("app_spawn %s on %s: %v", bundleID, dev, err)
	}
	if err := adapter.LaunchApp(id, bundleID, env); err != nil {
		h.mu.Unlock()
		return toolErr("app_spawn %s on %s: %v", bundleID, dev, err)
	}
	h.launchTimes[launchKey{deviceID: id, bundleID: bundleID}] = time.Now()
	h.mu.Unlock()

	s, err := h.waitForKeyedSession(id, bundleID, spawnDeviceWait)
	if err != nil {
		return toolErr("app_spawn %s on %s: %v", bundleID, dev, err)
	}
	return toolJSON(spawnDeviceResult{Device: dev, BundleID: bundleID, Session: sessionInfoFrom(s)})
}

// liveSessionFor returns the (device, bundle_id) pair's hello-complete
// app-channel session, or nil when the pair has no listener or no usable
// session. Unlike requireSession it never errors — app_spawn uses absence
// as the signal to take the device-launch path.
func (h *Handler) liveSessionFor(dev, bundleID string) *appchannel.Session {
	_, _, deviceID, err := h.resolveAdapter(dev)
	if err != nil {
		return nil
	}
	l, ok := h.appChannel.LookupKeyed(appchannel.AppKey{DeviceID: deviceID, BundleID: bundleID})
	if !ok {
		return nil
	}
	l.Touch()
	for _, s := range l.Sessions() {
		if s.HelloInfo() != nil {
			return s
		}
	}
	return nil
}

// waitForKeyedSession polls the (device, bundle) keyed listener until a
// session completes its hello, or timeout.
func (h *Handler) waitForKeyedSession(deviceID, bundleID string, timeout time.Duration) (*appchannel.Session, error) {
	key := appchannel.AppKey{DeviceID: deviceID, BundleID: bundleID}
	deadline := time.Now().Add(timeout)
	for {
		if l, ok := h.appChannel.LookupKeyed(key); ok {
			for _, s := range l.Sessions() {
				if s.HelloInfo() != nil {
					return s, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("app launched but no app-channel session appeared within %s (does the app honour SPYDER_APP_CHANNEL?)", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// handleGames returns the game catalog (🎯T92 clause 3, 🎯T117): launchable
// games across media, alongside the device inventory.
//
//   - desktop:        platform=desktop inventory entries (launch via
//     app_spawn(device=alias, bundle_id=...) or launch_app).
//   - factories:      connected server-mode sessions advertising
//     spawn_instance (instances via app_spawn(session_id=..., game=...)).
//   - mobile_devices: ios/android inventory entries. Mobile games have no
//     registry — the device's installed bundles are the catalog (🎯T117);
//     pass device=<alias> to list them.
//   - installed:      with device=, that device's installed third-party
//     bundles, each spawnable via app_spawn(device=..., bundle_id=...).
func (h *Handler) handleGames(args map[string]any) (*mcpgo.CallToolResult, error) {
	desktop := []map[string]string{}
	mobileDevices := []map[string]string{}
	for _, e := range h.inventory.Entries() {
		switch e.Platform {
		case "desktop":
			desktop = append(desktop, map[string]string{
				"alias":           e.Alias,
				"executable_path": e.ExecutablePath,
			})
		case "ios", "android":
			mobileDevices = append(mobileDevices, map[string]string{
				"alias":    e.Alias,
				"platform": e.Platform,
			})
		}
	}
	factories := []map[string]string{}
	if h.appChannel != nil {
		for _, s := range h.appChannel.Sessions() {
			if s.Supports(appchannel.MethodSpawnInstance) {
				factories = append(factories, map[string]string{
					"session_id": s.ID,
					"app_name":   s.HelloInfo().AppName,
				})
			}
		}
	}
	out := map[string]any{
		"desktop":        desktop,
		"factories":      factories,
		"mobile_devices": mobileDevices,
		"hint":           "mobile games are a device's installed bundles, not registry entries: games(device=<alias>) lists them; app_spawn(device=..., bundle_id=...) starts one and returns a ready session (🎯T117)",
	}
	if dev := optString(args, "device"); dev != "" {
		h.mu.Lock()
		adapter, _, id, err := h.resolveAdapter(dev)
		if err != nil {
			h.mu.Unlock()
			return toolErr("games: %v", err)
		}
		apps, err := adapter.ListApps(id)
		h.mu.Unlock()
		if err != nil {
			return toolErr("games: list apps on %s: %v", dev, err)
		}
		installed := make([]map[string]string, 0, len(apps))
		for _, a := range apps {
			entry := map[string]string{"bundle_id": a.BundleID}
			if a.Name != "" {
				entry["name"] = a.Name
			}
			if a.Version != "" {
				entry["version"] = a.Version
			}
			installed = append(installed, entry)
		}
		out["installed"] = map[string]any{"device": dev, "apps": installed}
	}
	return toolJSON(out)
}
