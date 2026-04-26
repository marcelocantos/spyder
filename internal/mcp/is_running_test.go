// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/marcelocantos/spyder/internal/device"
)

// TestIsRunning_Running covers the happy path: AppPID returns a PID, so
// state is "running" and pid is populated.
func TestIsRunning_Running(t *testing.T) {
	stub := &stubAdapter{
		appPID: func(id, bundle string) (int, error) {
			if bundle != "com.example.app" {
				t.Fatalf("bundle = %q; want com.example.app", bundle)
			}
			return 4242, nil
		},
	}
	h := newHandlerWithStubs(t, stub, nil)
	res := dispatchJSON(t, h, "is_running", map[string]any{
		"device":    "Pippa",
		"bundle_id": "com.example.app",
	})
	if res.IsError {
		t.Fatalf("IsError = true; result: %s", resultText(t, &res))
	}
	var got isRunningResult
	if err := json.Unmarshal([]byte(resultText(t, &res)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.State != "running" {
		t.Errorf("state = %q; want running", got.State)
	}
	if got.PID != 4242 {
		t.Errorf("pid = %d; want 4242", got.PID)
	}
}

// TestIsRunning_NotRunning_Installed: AppPID errors but ListApps shows
// the bundle is installed → state is "not_running", no pid.
func TestIsRunning_NotRunning_Installed(t *testing.T) {
	stub := &stubAdapter{
		appPID: func(id, bundle string) (int, error) {
			return 0, errors.New("app not running: " + bundle)
		},
		listApps: func(id string) ([]device.AppInfo, error) {
			return []device.AppInfo{{BundleID: "com.example.app"}}, nil
		},
	}
	h := newHandlerWithStubs(t, stub, nil)
	res := dispatchJSON(t, h, "is_running", map[string]any{
		"device":    "Pippa",
		"bundle_id": "com.example.app",
	})
	if res.IsError {
		t.Fatalf("IsError = true; result: %s", resultText(t, &res))
	}
	var got isRunningResult
	if err := json.Unmarshal([]byte(resultText(t, &res)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.State != "not_running" {
		t.Errorf("state = %q; want not_running", got.State)
	}
	if got.PID != 0 {
		t.Errorf("pid = %d; want 0", got.PID)
	}
}

// TestIsRunning_NotInstalled: AppPID errors and the bundle is missing
// from ListApps → state is "not_installed".
func TestIsRunning_NotInstalled(t *testing.T) {
	stub := &stubAdapter{
		appPID: func(id, bundle string) (int, error) {
			return 0, errors.New("app not running: " + bundle)
		},
		listApps: func(id string) ([]device.AppInfo, error) {
			return []device.AppInfo{{BundleID: "com.someone.else"}}, nil
		},
	}
	h := newHandlerWithStubs(t, stub, nil)
	res := dispatchJSON(t, h, "is_running", map[string]any{
		"device":    "Pippa",
		"bundle_id": "com.example.app",
	})
	if res.IsError {
		t.Fatalf("IsError = true; result: %s", resultText(t, &res))
	}
	var got isRunningResult
	if err := json.Unmarshal([]byte(resultText(t, &res)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.State != "not_installed" {
		t.Errorf("state = %q; want not_installed", got.State)
	}
}

// TestIsRunning_ListAppsError: AppPID errors and ListApps fails too →
// surface a tool error so callers know the inventory check is unreliable.
func TestIsRunning_ListAppsError(t *testing.T) {
	stub := &stubAdapter{
		appPID: func(id, bundle string) (int, error) {
			return 0, errors.New("app not running")
		},
		listApps: func(id string) ([]device.AppInfo, error) {
			return nil, errors.New("device not connected")
		},
	}
	h := newHandlerWithStubs(t, stub, nil)
	res := dispatchJSON(t, h, "is_running", map[string]any{
		"device":    "Pippa",
		"bundle_id": "com.example.app",
	})
	if !res.IsError {
		t.Fatalf("IsError = false; want true (list_apps failed)")
	}
}

// TestIsRunning_MissingArgs validates input requirements.
func TestIsRunning_MissingArgs(t *testing.T) {
	h := newHandlerWithStubs(t, &stubAdapter{}, nil)
	if _, err := h.Dispatch("is_running", map[string]any{}); err == nil {
		t.Error("missing device should error")
	}
	if _, err := h.Dispatch("is_running", map[string]any{"device": "Pippa"}); err == nil {
		t.Error("missing bundle_id should error")
	}
}
