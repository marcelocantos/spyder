// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// 🎯T117 tests: app_spawn / games for mobile games without a desktop
// games() registry entry. The device seam is the stub adapter (launch /
// installed-check / list-apps); the app side is a raw in-process client
// that dials the keyed app-channel listener like a real game would.
package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/device"
)

const (
	spawnTestDevice = "emulator-5554" // raw ref → android adapter, host 10.0.2.2
	spawnTestBundle = "com.example.game"
)

// newSpawnHandler returns a Handler with an app-channel manager and the
// given android stub (spawnTestDevice resolves to the android adapter).
func newSpawnHandler(t *testing.T, android device.Adapter) *Handler {
	t.Helper()
	h := newTestHandler(t)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)
	if android != nil {
		h.android = android
	}
	return h
}

// dialHelloQuiet connects to a listener and completes the hello handshake
// without t.Fatal, so it is safe from non-test goroutines (a stub
// adapter's LaunchApp playing the part of the app). The connection is
// intentionally kept open — the session must stay live for the test; the
// manager Close in cleanup tears it down.
func dialHelloQuiet(t *testing.T, port int, hello appchannel.Hello) {
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Errorf("app dial: %v", err)
		return
	}
	params, err := appchannel.PackParams(hello)
	if err != nil {
		t.Errorf("app hello pack: %v", err)
		return
	}
	if err := appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: params}); err != nil {
		t.Errorf("app hello write: %v", err)
		return
	}
	if _, err := appchannel.ReadFrame(conn); err != nil {
		t.Errorf("app hello ack: %v", err)
	}
}

// launchingStub returns a stub android adapter whose LaunchApp behaves
// like a real game: it reads SPYDER_APP_CHANNEL from the injected env and
// dials back to complete the app-channel hello.
func launchingStub(t *testing.T, appName string) *stubAdapter {
	t.Helper()
	return &stubAdapter{
		launchApp: func(id, bundle string, env map[string]string) error {
			addr := env["SPYDER_APP_CHANNEL"]
			if addr == "" {
				t.Error("LaunchApp env is missing SPYDER_APP_CHANNEL")
				return fmt.Errorf("no app channel env")
			}
			_, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				return fmt.Errorf("bad SPYDER_APP_CHANNEL %q: %w", addr, err)
			}
			port, err := strconv.Atoi(portStr)
			if err != nil {
				return fmt.Errorf("bad port in %q: %w", addr, err)
			}
			go dialHelloQuiet(t, port, appchannel.Hello{AppName: appName, AppVersion: "1.0"})
			return nil
		},
	}
}

func decodeJSONResult(t *testing.T, r *callToolResultForTest) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(t, r)), &out); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, resultText(t, r))
	}
	return out
}

// app_spawn(device, bundle_id) with no registry entry and no factory
// session launches the installed bundle and returns a ready session —
// the 🎯T117 acceptance path.
func TestAppSpawn_DeviceLaunchesMobileBundle(t *testing.T) {
	h := newSpawnHandler(t, launchingStub(t, "yourworld"))

	r := dispatchJSON(t, h, "app_spawn", map[string]any{
		"device": spawnTestDevice, "bundle_id": spawnTestBundle,
	})
	if r.IsError {
		t.Fatalf("app_spawn: %s", resultText(t, &r))
	}
	out := decodeJSONResult(t, &r)
	if out["device"] != spawnTestDevice || out["bundle_id"] != spawnTestBundle {
		t.Errorf("device/bundle_id echo wrong: %v", out)
	}
	if _, ok := out["already_running"]; ok {
		t.Errorf("fresh launch must not report already_running: %v", out)
	}
	session, ok := out["session"].(map[string]any)
	if !ok {
		t.Fatalf("no session object in result: %v", out)
	}
	if session["session_id"] == "" || session["session_id"] == nil {
		t.Errorf("session_id empty: %v", session)
	}
	if session["app_name"] != "yourworld" {
		t.Errorf("app_name = %v; want yourworld", session["app_name"])
	}
}

// A bundle that isn't installed fails closed with a deploy_app pointer —
// the installed-bundle catalog is the gate, not a registry.
func TestAppSpawn_DeviceBundleNotInstalled(t *testing.T) {
	stub := &stubAdapter{
		resolveExecutable: func(id, bundle string) (string, bool, error) {
			return "", false, nil
		},
		launchApp: func(id, bundle string, env map[string]string) error {
			t.Error("LaunchApp must not be called for an uninstalled bundle")
			return nil
		},
	}
	h := newSpawnHandler(t, stub)
	r := dispatchJSON(t, h, "app_spawn", map[string]any{
		"device": spawnTestDevice, "bundle_id": spawnTestBundle,
	})
	if !r.IsError {
		t.Fatal("app_spawn should error for an uninstalled bundle")
	}
	if msg := resultText(t, &r); !strings.Contains(msg, "not installed") || !strings.Contains(msg, "deploy_app") {
		t.Errorf("error should name not-installed and point at deploy_app: %s", msg)
	}
}

// game= without a factory session is a clear routing error, not a silent
// device launch — the agent is told the bundle_id names the game.
func TestAppSpawn_GameArgWithoutFactoryErrors(t *testing.T) {
	stub := &stubAdapter{
		launchApp: func(id, bundle string, env map[string]string) error {
			t.Error("LaunchApp must not be called when game= is misdirected")
			return nil
		},
	}
	h := newSpawnHandler(t, stub)
	r := dispatchJSON(t, h, "app_spawn", map[string]any{
		"device": spawnTestDevice, "bundle_id": spawnTestBundle, "game": "yourworld",
	})
	if !r.IsError {
		t.Fatal("app_spawn with game= and no factory should error")
	}
	if msg := resultText(t, &r); !strings.Contains(msg, "factory") {
		t.Errorf("error should explain the factory-only game arg: %s", msg)
	}
}

// A live non-factory session for (device, bundle) short-circuits to that
// session instead of double-launching.
func TestAppSpawn_DeviceAlreadyRunning(t *testing.T) {
	stub := &stubAdapter{
		launchApp: func(id, bundle string, env map[string]string) error {
			t.Error("LaunchApp must not be called when a live session exists")
			return nil
		},
	}
	h := newSpawnHandler(t, stub)

	l, err := h.appChannel.GetOrCreateListener(appchannel.AppKey{DeviceID: spawnTestDevice, BundleID: spawnTestBundle})
	if err != nil {
		t.Fatalf("GetOrCreateListener: %v", err)
	}
	dialHelloQuiet(t, l.Port, appchannel.Hello{AppName: "yourworld", AppVersion: "1.0"})
	sessionID := waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_spawn", map[string]any{
		"device": spawnTestDevice, "bundle_id": spawnTestBundle,
	})
	if r.IsError {
		t.Fatalf("app_spawn: %s", resultText(t, &r))
	}
	out := decodeJSONResult(t, &r)
	if out["already_running"] != true {
		t.Errorf("already_running = %v; want true", out["already_running"])
	}
	session, _ := out["session"].(map[string]any)
	if session == nil || session["session_id"] != sessionID {
		t.Errorf("should return the existing session %s: %v", sessionID, out)
	}
}

// A live FACTORY session for (device, bundle) routes to the factory path,
// which still demands game= — proving factory precedence over the device
// launch.
func TestAppSpawn_FactorySessionTakesPrecedence(t *testing.T) {
	stub := &stubAdapter{
		launchApp: func(id, bundle string, env map[string]string) error {
			t.Error("LaunchApp must not be called when a factory session exists")
			return nil
		},
	}
	h := newSpawnHandler(t, stub)

	l, err := h.appChannel.GetOrCreateListener(appchannel.AppKey{DeviceID: spawnTestDevice, BundleID: spawnTestBundle})
	if err != nil {
		t.Fatalf("GetOrCreateListener: %v", err)
	}
	dialHelloQuiet(t, l.Port, appchannel.Hello{
		AppName: "gameserver", AppVersion: "1.0",
		Methods: appchannel.MethodDescriptors(appchannel.MethodSpawnInstance),
	})
	waitForAppSession(t, h)

	r := dispatchJSON(t, h, "app_spawn", map[string]any{
		"device": spawnTestDevice, "bundle_id": spawnTestBundle,
	})
	if !r.IsError {
		t.Fatal("factory path without game= should error")
	}
	if msg := resultText(t, &r); !strings.Contains(msg, "game is required") {
		t.Errorf("factory path should demand game=: %s", msg)
	}
}

// Device spawns are reservation-gated like every other device-state
// mutation: another owner's reservation blocks the launch.
func TestAppSpawn_DeviceReservationGated(t *testing.T) {
	stub := &stubAdapter{
		launchApp: func(id, bundle string, env map[string]string) error {
			t.Error("LaunchApp must not run against another owner's reservation")
			return nil
		},
	}
	h, store := newHandlerWithReservations(t, nil, stub)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)

	if _, err := store.Acquire(spawnTestDevice, "someone-else", 0, ""); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	r := dispatchJSON(t, h, "app_spawn", map[string]any{
		"device": spawnTestDevice, "bundle_id": spawnTestBundle, "owner": "me",
	})
	if !r.IsError {
		t.Fatal("app_spawn should be blocked by another owner's reservation")
	}
	if msg := resultText(t, &r); !strings.Contains(msg, "someone-else") {
		t.Errorf("conflict should name the holder: %s", msg)
	}
}

// games(device=...) lists the device's installed bundles — the mobile
// game catalog is the device itself, not a registry (🎯T117).
func TestGames_DeviceListsInstalledBundles(t *testing.T) {
	stub := &stubAdapter{
		listApps: func(id string) ([]device.AppInfo, error) {
			return []device.AppInfo{
				{BundleID: "com.example.game", Name: "YourWorld", Version: "2.1"},
				{BundleID: "com.example.other"},
			}, nil
		},
	}
	h := newSpawnHandler(t, stub)
	r := dispatchJSON(t, h, "games", map[string]any{"device": spawnTestDevice})
	if r.IsError {
		t.Fatalf("games: %s", resultText(t, &r))
	}
	out := decodeJSONResult(t, &r)
	installed, ok := out["installed"].(map[string]any)
	if !ok {
		t.Fatalf("games(device=...) should include installed: %v", out)
	}
	if installed["device"] != spawnTestDevice {
		t.Errorf("installed.device = %v; want %s", installed["device"], spawnTestDevice)
	}
	apps, _ := installed["apps"].([]any)
	if len(apps) != 2 {
		t.Fatalf("installed.apps len = %d; want 2: %v", len(apps), installed)
	}
	first, _ := apps[0].(map[string]any)
	if first["bundle_id"] != "com.example.game" || first["name"] != "YourWorld" {
		t.Errorf("first app wrong: %v", first)
	}
}

// games() with no args stays cheap (no device round-trips) but points the
// agent at the mobile catalog: inventory ios/android entries are listed
// as mobile_devices alongside the unchanged desktop/factories sections.
func TestGames_NoArgsListsMobileDevices(t *testing.T) {
	listAppsCalled := false
	stub := &stubAdapter{
		listApps: func(id string) ([]device.AppInfo, error) {
			listAppsCalled = true
			return nil, nil
		},
	}
	h := newSpawnHandler(t, stub)
	r := dispatchJSON(t, h, "games", nil)
	if r.IsError {
		t.Fatalf("games: %s", resultText(t, &r))
	}
	if listAppsCalled {
		t.Error("games() without device= must not round-trip to devices")
	}
	out := decodeJSONResult(t, &r)
	for _, key := range []string{"desktop", "factories", "mobile_devices"} {
		if _, ok := out[key]; !ok {
			t.Errorf("games() missing %q section: %v", key, out)
		}
	}
	if _, ok := out["installed"]; ok {
		t.Errorf("games() without device= must not include installed: %v", out)
	}
	// The newTestHandler inventory has iPad (ios) and Raspberry (android).
	mobile, _ := out["mobile_devices"].([]any)
	aliases := map[string]bool{}
	for _, m := range mobile {
		e, _ := m.(map[string]any)
		aliases[fmt.Sprint(e["alias"])] = true
	}
	if !aliases["iPad"] || !aliases["Raspberry"] {
		t.Errorf("mobile_devices should list inventory ios/android aliases, got %v", mobile)
	}
}
