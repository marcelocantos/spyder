// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Agent-UX session flows: deploy_app/launch_app structured results
// (🎯T121/🎯T119), stale session_id diagnosis (🎯T119), ensure_session
// (🎯T118), and the read-only state_query probe (🎯T122). These tests
// exercise the user-facing flows — several drive the verbs the way an
// agent would, through app_exec Starlark scripts.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/device"
)

// shortPostLaunchWait shrinks the launch→handshake wait for tests whose
// fake apps never dial back.
func shortPostLaunchWait(t *testing.T) {
	t.Helper()
	prev := postLaunchSessionWait
	postLaunchSessionWait = 50 * time.Millisecond
	t.Cleanup(func() { postLaunchSessionWait = prev })
}

// dialSmokeFromEnv connects a fake app to the injected SPYDER_APP_CHANNEL
// address the way a real app would. Goroutine-safe (no t helpers); a
// failed connect simply produces no session, which the test then reports.
func dialSmokeFromEnv(env map[string]string, methods []string) {
	addr, ok := env["SPYDER_APP_CHANNEL"]
	if !ok {
		return
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	go func() {
		conn, err := net.Dial("tcp", "127.0.0.1:"+port)
		if err != nil {
			return
		}
		raw, _ := appchannel.PackParams(appchannel.Hello{
			AppName:    "smoke",
			AppVersion: "1.0",
			Methods:    appchannel.MethodDescriptors(methods...),
		})
		if err := appchannel.WriteFrame(conn, &appchannel.Envelope{ID: 1, Method: appchannel.MethodHello, Params: raw}); err != nil {
			_ = conn.Close()
			return
		}
		if _, err := appchannel.ReadFrame(conn); err != nil {
			_ = conn.Close()
			return
		}
		c := &smokeClient{conn: conn, stop: make(chan struct{})}
		c.serve()
	}()
}

func mkAppFixture(t *testing.T) string {
	t.Helper()
	appPath := filepath.Join(t.TempDir(), "MyApp.app")
	if err := os.MkdirAll(appPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return appPath
}

func waitSessionGone(t *testing.T, h *Handler, sid string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := h.appChannel.GetSession(sid); !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s still live after close", sid)
}

func waitForOtherSession(t *testing.T, h *Handler, exclude string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range h.appChannel.Sessions() {
			if s.ID != exclude && s.HelloInfo() != nil {
				return s.ID
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no replacement session within 2s")
	return ""
}

// --- 🎯T121: deploy_app structured result ------------------------------

func TestHandleDeployApp_StructuredResultWithSession(t *testing.T) {
	appPath := mkAppFixture(t)
	ios := &stubAdapter{
		listApps: func(id string) ([]device.AppInfo, error) {
			return []device.AppInfo{{BundleID: "com.example.app"}}, nil // prior build present
		},
		terminateApp: func(id, bundle string) error { return nil },
		installApp:   func(id, path string) error { return nil },
		launchApp: func(id, bundle string, env map[string]string) error {
			dialSmokeFromEnv(env, []string{appchannel.MethodPing})
			return nil
		},
		appPID: func(id, bundle string) (int, error) { return 9999, nil },
	}
	h := newHandlerWithStubs(t, ios, nil)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)

	r := dispatchJSON(t, h, "deploy_app", map[string]any{
		"device":    "iPad",
		"path":      appPath,
		"bundle_id": "com.example.app",
	})
	if r.IsError {
		t.Fatalf("deploy_app should succeed; body=%s", resultText(t, &r))
	}
	var out deployResult
	if err := json.Unmarshal([]byte(resultText(t, &r)), &out); err != nil {
		t.Fatalf("unmarshal deploy result: %v", err)
	}
	if out.BundleID != "com.example.app" || out.PID != 9999 {
		t.Errorf("bundle/pid = %q/%d; want com.example.app/9999", out.BundleID, out.PID)
	}
	if !out.Replaced {
		t.Error("replaced = false; the bundle was already installed")
	}
	if out.SessionID == "" || out.ChannelPort == 0 {
		t.Errorf("session_id/channel_port missing from deploy result: %+v", out)
	}
	if _, ok := h.appChannel.GetSession(out.SessionID); !ok {
		t.Errorf("deploy result names session %s but it is not live", out.SessionID)
	}
}

func TestHandleDeployApp_FreshInstallNoChannelDial(t *testing.T) {
	shortPostLaunchWait(t)
	appPath := mkAppFixture(t)
	ios := &stubAdapter{
		listApps:     func(id string) ([]device.AppInfo, error) { return nil, nil }, // not installed
		terminateApp: func(id, bundle string) error { return nil },
		installApp:   func(id, path string) error { return nil },
		launchApp:    func(id, bundle string, env map[string]string) error { return nil }, // never dials
		appPID:       func(id, bundle string) (int, error) { return 7, nil },
	}
	h := newHandlerWithStubs(t, ios, nil)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)

	r := dispatchJSON(t, h, "deploy_app", map[string]any{
		"device":    "iPad",
		"path":      appPath,
		"bundle_id": "com.example.app",
	})
	if r.IsError {
		t.Fatalf("deploy_app should succeed; body=%s", resultText(t, &r))
	}
	body := resultText(t, &r)
	if !strings.Contains(body, `"replaced": false`) && !strings.Contains(body, `"replaced":false`) {
		t.Errorf("fresh install should report replaced=false; body=%s", body)
	}
	if strings.Contains(body, "session_id") {
		t.Errorf("no app dialed — session_id must be omitted; body=%s", body)
	}
}

// --- 🎯T119: launch_app result includes the live session ----------------

func TestHandleLaunchApp_ResultIncludesSession(t *testing.T) {
	ios := &stubAdapter{
		launchApp: func(id, bundle string, env map[string]string) error {
			dialSmokeFromEnv(env, []string{appchannel.MethodPing})
			return nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)

	r := dispatchJSON(t, h, "launch_app", map[string]any{
		"device":    "iPad",
		"bundle_id": "com.example.app",
	})
	if r.IsError {
		t.Fatalf("launch_app should succeed; body=%s", resultText(t, &r))
	}
	var out launchAppResult
	if err := json.Unmarshal([]byte(resultText(t, &r)), &out); err != nil {
		t.Fatalf("unmarshal launch result: %v", err)
	}
	if out.BundleID != "com.example.app" || out.Device != "iPad" {
		t.Errorf("device/bundle = %q/%q", out.Device, out.BundleID)
	}
	if out.SessionID == "" || out.ChannelPort == 0 {
		t.Errorf("session_id/channel_port missing from launch result: %+v", out)
	}
}

// --- 🎯T119: stale session_id diagnosis --------------------------------

func TestRequireSession_StaleSidNamesLiveReplacement(t *testing.T) {
	h := startAppChannelHandler(t)
	const iPadUUID = "00008103-001122334455667A"
	l, err := h.appChannel.GetOrCreateListener(appchannel.AppKey{
		DeviceID: iPadUUID, BundleID: "com.smoke.app",
	})
	if err != nil {
		t.Fatalf("GetOrCreateListener: %v", err)
	}

	a := dialSmoke(t, l.Port, []string{appchannel.MethodPing})
	sid1 := waitForAppSession(t, h)
	a.close()
	waitSessionGone(t, h, sid1)

	b := dialSmoke(t, l.Port, []string{appchannel.MethodPing})
	defer b.close()
	sid2 := waitForOtherSession(t, h, sid1)

	r := dispatchJSON(t, h, "app_ping", map[string]any{"session_id": sid1})
	if !r.IsError {
		t.Fatal("app_ping with a stale sid should error")
	}
	body := resultText(t, &r)
	for _, want := range []string{"stale", sid2, "com.smoke.app", iPadUUID} {
		if !strings.Contains(body, want) {
			t.Errorf("stale-sid error missing %q: %s", want, body)
		}
	}
}

func TestRequireSession_StaleSidNoLiveSessionSaysRelaunch(t *testing.T) {
	h := startAppChannelHandler(t)
	l, err := h.appChannel.GetOrCreateListener(appchannel.AppKey{
		DeviceID: "00008103-001122334455667A", BundleID: "com.smoke.app",
	})
	if err != nil {
		t.Fatalf("GetOrCreateListener: %v", err)
	}
	a := dialSmoke(t, l.Port, []string{appchannel.MethodPing})
	sid := waitForAppSession(t, h)
	a.close()
	waitSessionGone(t, h, sid)

	r := dispatchJSON(t, h, "app_ping", map[string]any{"session_id": sid})
	if !r.IsError {
		t.Fatal("app_ping with a stale sid should error")
	}
	body := resultText(t, &r)
	for _, want := range []string{"stale", "com.smoke.app", "ensure_session"} {
		if !strings.Contains(body, want) {
			t.Errorf("stale-sid error missing %q: %s", want, body)
		}
	}
}

func TestRequireSession_UnknownSidStillExplicit(t *testing.T) {
	h := startAppChannelHandler(t)
	r := dispatchJSON(t, h, "app_ping", map[string]any{"session_id": "never-existed"})
	if !r.IsError {
		t.Fatal("app_ping with an unknown sid should error")
	}
	body := resultText(t, &r)
	if !strings.Contains(body, "never-existed") || !strings.Contains(body, "app_channel_list") {
		t.Errorf("unknown-sid error should name the sid and the recovery path: %s", body)
	}
}

// --- 🎯T118: ensure_session --------------------------------------------

func TestEnsureSession_HappyPathViaAppExec(t *testing.T) {
	appPath := mkAppFixture(t)
	installs, launches := 0, 0
	ios := &stubAdapter{
		listApps:     func(id string) ([]device.AppInfo, error) { return nil, nil }, // not installed
		installApp:   func(id, path string) error { installs++; return nil },
		terminateApp: func(id, bundle string) error { return nil },
		launchApp: func(id, bundle string, env map[string]string) error {
			launches++
			dialSmokeFromEnv(env, []string{appchannel.MethodPing, appchannel.MethodStateQuery})
			return nil
		},
		appPID: func(id, bundle string) (int, error) {
			if launches == 0 {
				return 0, errors.New("app not running")
			}
			return 4321, nil
		},
	}
	h := newHandlerWithStubs(t, ios, nil)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)

	script := fmt.Sprintf(`ensure_session(device="iPad", bundle_id="com.example.app", path=%q)`, appPath)
	r := dispatchJSON(t, h, "app_exec", map[string]any{"script": script})
	if r.IsError {
		t.Fatalf("ensure_session via app_exec failed: %s", resultText(t, &r))
	}
	var out ensureSessionResult
	if err := json.Unmarshal([]byte(resultText(t, &r)), &out); err != nil {
		t.Fatalf("unmarshal ensure_session result: %v; body=%s", err, resultText(t, &r))
	}
	if out.SessionID == "" || out.ChannelPort == 0 {
		t.Fatalf("no session in ensure_session result: %+v", out)
	}
	if !out.Deployed || !out.Launched || out.PID != 4321 {
		t.Errorf("first call should deploy+launch with pid 4321: %+v", out)
	}
	if installs != 1 || launches != 1 {
		t.Errorf("installs/launches = %d/%d; want 1/1", installs, launches)
	}

	// Second call: healthy session already up — idempotent, no device I/O.
	r2 := dispatchJSON(t, h, "app_exec", map[string]any{"script": script})
	if r2.IsError {
		t.Fatalf("idempotent ensure_session failed: %s", resultText(t, &r2))
	}
	var out2 ensureSessionResult
	if err := json.Unmarshal([]byte(resultText(t, &r2)), &out2); err != nil {
		t.Fatalf("unmarshal second result: %v", err)
	}
	if out2.SessionID != out.SessionID {
		t.Errorf("idempotent call returned a different session: %s vs %s", out2.SessionID, out.SessionID)
	}
	if out2.Deployed || out2.Launched {
		t.Errorf("idempotent call must not deploy or launch: %+v", out2)
	}
	if installs != 1 || launches != 1 {
		t.Errorf("device was touched on the idempotent path: installs/launches = %d/%d", installs, launches)
	}
}

func TestEnsureSession_SkipsInstallWhenAlreadyInstalled(t *testing.T) {
	appPath := mkAppFixture(t)
	installs := 0
	ios := &stubAdapter{
		listApps: func(id string) ([]device.AppInfo, error) {
			return []device.AppInfo{{BundleID: "com.example.app"}}, nil
		},
		installApp:   func(id, path string) error { installs++; return nil },
		terminateApp: func(id, bundle string) error { return nil },
		launchApp: func(id, bundle string, env map[string]string) error {
			dialSmokeFromEnv(env, []string{appchannel.MethodPing})
			return nil
		},
		appPID: func(id, bundle string) (int, error) { return 77, nil },
	}
	h := newHandlerWithStubs(t, ios, nil)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)

	r := dispatchJSON(t, h, "ensure_session", map[string]any{
		"device":    "iPad",
		"bundle_id": "com.example.app",
		"path":      appPath,
	})
	if r.IsError {
		t.Fatalf("ensure_session failed: %s", resultText(t, &r))
	}
	var out ensureSessionResult
	if err := json.Unmarshal([]byte(resultText(t, &r)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if installs != 0 || out.Deployed {
		t.Errorf("install must be skipped when the bundle is present: installs=%d deployed=%v", installs, out.Deployed)
	}
	if !out.Launched || out.SessionID == "" {
		t.Errorf("expected launch + session: %+v", out)
	}
}

func TestEnsureSession_NoHandshakeIsExplicit(t *testing.T) {
	ios := &stubAdapter{
		terminateApp: func(id, bundle string) error { return nil },
		launchApp:    func(id, bundle string, env map[string]string) error { return nil }, // never dials
		appPID:       func(id, bundle string) (int, error) { return 55, nil },
	}
	h := newHandlerWithStubs(t, ios, nil)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)

	r := dispatchJSON(t, h, "ensure_session", map[string]any{
		"device":     "iPad",
		"bundle_id":  "com.example.app",
		"timeout_ms": 100.0,
	})
	if !r.IsError {
		t.Fatalf("ensure_session should fail when no handshake forms; body=%s", resultText(t, &r))
	}
	body := resultText(t, &r)
	if !strings.Contains(body, "no app-channel session") || !strings.Contains(body, "SPYDER_APP_CHANNEL") {
		t.Errorf("timeout error should explain the missing handshake: %s", body)
	}
}

// --- 🎯T122: read-only state_query -------------------------------------

func TestStateQuery_ReadOnlyAndReservationFree(t *testing.T) {
	h, store := newHandlerWithReservations(t, &stubAdapter{}, nil)
	h.appChannel = appchannel.NewManager()
	t.Cleanup(h.appChannel.Close)
	if _, err := store.Acquire("iPad", "someone-else", 0, "hold"); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	l, err := h.appChannel.GetOrCreateListener(appchannel.AppKey{
		DeviceID: "00008103-001122334455667A", BundleID: "com.smoke.app",
	})
	if err != nil {
		t.Fatalf("GetOrCreateListener: %v", err)
	}
	client := dialSmoke(t, l.Port, []string{appchannel.MethodPing, appchannel.MethodStateQuery})
	defer client.close()
	_ = waitForAppSession(t, h)

	// Read-only probe succeeds despite the foreign reservation, driven
	// the way an agent would: through app_exec.
	r := dispatchJSON(t, h, "app_exec", map[string]any{
		"script": `state_query(device="iPad", bundle_id="com.smoke.app")`,
	})
	if r.IsError {
		t.Fatalf("state_query must succeed regardless of reservation holder: %s", resultText(t, &r))
	}
	body := resultText(t, &r)
	for _, want := range []string{"landmarks", "placed_count", "paused"} {
		if !strings.Contains(body, want) {
			t.Errorf("state_query body missing %q: %s", want, body)
		}
	}

	// The probe sent nothing mutating to the app.
	if client.pauseCalled || client.resumeCalled || client.stepFrames != 0 || client.lastInput != nil {
		t.Error("state_query must not touch pause/resume/step/input")
	}
	if client.lastAppCallMethod != "" {
		t.Errorf("state_query must not invoke app-registered commands; saw %q", client.lastAppCallMethod)
	}

	// Named slice still works through the same verb.
	r2 := dispatchJSON(t, h, "state_query", map[string]any{
		"device": "iPad", "bundle_id": "com.smoke.app", "slice": "scene",
	})
	if r2.IsError || !strings.Contains(resultText(t, &r2), "data for scene") {
		t.Errorf("state_query with slice: %s", resultText(t, &r2))
	}

	// Contrast: a mutating verb on the reserved device is refused.
	r3 := dispatchJSON(t, h, "launch_app", map[string]any{
		"device": "iPad", "bundle_id": "com.smoke.app", "owner": "me",
	})
	if !r3.IsError {
		t.Fatal("launch_app must be refused while another owner holds the device")
	}
}
