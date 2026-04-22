// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unixTestServer spins up an httptest.Server bound to a Unix socket in a temp
// directory and returns the server, Client, and a cleanup function.
// On macOS, Unix socket paths are limited to 104 bytes; we create the socket
// file in os.TempDir() with a short name to stay safely under that limit.
func unixTestServer(t *testing.T, mux http.Handler) (*httptest.Server, *Client) {
	t.Helper()

	// Use a file in os.TempDir() with a short, unique name. We can't use
	// t.TempDir() directly because macOS has a 104-byte socket path limit and
	// the test-scoped temp directories have long path names.
	f, err := os.CreateTemp("", "spyder-test-*.sock")
	if err != nil {
		t.Fatalf("create temp socket: %v", err)
	}
	sock := f.Name()
	f.Close()
	_ = os.Remove(sock) // remove so net.Listen can bind it fresh

	t.Cleanup(func() { _ = os.Remove(sock) })

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sock, err)
	}

	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	return srv, NewClient(sock)
}

// respond writes a JSON-encoded body with the given HTTP status code.
func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// --- ListDevices ---

func TestClient_ListDevices(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/list_devices", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, listDevicesResponse{
			Devices: []DeviceInfo{
				{UDID: "abc", Name: "Pippa", ProductType: "iPad14,1", OSVersion: "18.3"},
			},
		})
	})

	_, c := unixTestServer(t, mux)
	devs, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) != 1 || devs[0].UDID != "abc" {
		t.Errorf("got %+v; want [{UDID:abc ...}]", devs)
	}
}

// --- ListApps ---

func TestClient_ListApps(t *testing.T) {
	name := "My App"
	ver := "1.0"
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/list_apps", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, listAppsResponse{
			Apps: []AppInfo{
				{BundleID: "com.example.app", Name: &name, Version: &ver},
			},
		})
	})

	_, c := unixTestServer(t, mux)
	apps, err := c.ListApps(context.Background(), "abc")
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	if len(apps) != 1 || apps[0].BundleID != "com.example.app" {
		t.Errorf("got %+v", apps)
	}
}

// --- LaunchApp ---

func TestClient_LaunchApp(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/launch_app", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, launchAppResponse{PID: 1234})
	})

	_, c := unixTestServer(t, mux)
	pid, err := c.LaunchApp(context.Background(), "abc", "com.example.app")
	if err != nil {
		t.Fatalf("LaunchApp: %v", err)
	}
	if pid != 1234 {
		t.Errorf("pid = %d; want 1234", pid)
	}
}

// --- KillApp ---

func TestClient_KillApp(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/kill_app", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]any{})
	})

	_, c := unixTestServer(t, mux)
	if err := c.KillApp(context.Background(), "abc", "com.example.app"); err != nil {
		t.Fatalf("KillApp: %v", err)
	}
}

// --- PIDForBundle ---

func TestClient_PIDForBundle_Running(t *testing.T) {
	pid := 99
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pid_for_bundle", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, pidForBundleResponse{PID: &pid})
	})

	_, c := unixTestServer(t, mux)
	got, err := c.PIDForBundle(context.Background(), "abc", "com.example.app")
	if err != nil {
		t.Fatalf("PIDForBundle: %v", err)
	}
	if got == nil || *got != 99 {
		t.Errorf("pid = %v; want 99", got)
	}
}

func TestClient_PIDForBundle_NotRunning(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pid_for_bundle", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, pidForBundleResponse{PID: nil})
	})

	_, c := unixTestServer(t, mux)
	got, err := c.PIDForBundle(context.Background(), "abc", "com.example.app")
	if err != nil {
		t.Fatalf("PIDForBundle: %v", err)
	}
	if got != nil {
		t.Errorf("pid = %v; want nil", *got)
	}
}

// --- Battery ---

func TestClient_Battery(t *testing.T) {
	level := 0.85
	charging := true
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/battery", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, Battery{Level: &level, Charging: &charging})
	})

	_, c := unixTestServer(t, mux)
	batt, err := c.Battery(context.Background(), "abc")
	if err != nil {
		t.Fatalf("Battery: %v", err)
	}
	if batt.Level == nil || *batt.Level != 0.85 {
		t.Errorf("level = %v; want 0.85", batt.Level)
	}
	if batt.Charging == nil || !*batt.Charging {
		t.Errorf("charging = %v; want true", batt.Charging)
	}
}

// --- Screenshot ---

func TestClient_Screenshot(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4E, 0x47} // PNG magic bytes
	b64 := base64.StdEncoding.EncodeToString(pngBytes)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/screenshot", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, screenshotResponse{PNGBase64: b64})
	})

	_, c := unixTestServer(t, mux)
	data, err := c.Screenshot(context.Background(), "abc")
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if string(data) != string(pngBytes) {
		t.Errorf("got %v; want %v", data, pngBytes)
	}
}

// --- CrashReportsList ---

func TestClient_CrashReportsList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/crash_reports_list", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, crashReportsListResponse{
			Reports: []CrashReport{
				{Name: "MyApp_2026-01-01.ips", Process: "MyApp", Timestamp: "2026-01-01T00:00:00Z"},
			},
		})
	})

	_, c := unixTestServer(t, mux)
	reports, err := c.CrashReportsList(context.Background(), "abc", time.Time{}, "")
	if err != nil {
		t.Fatalf("CrashReportsList: %v", err)
	}
	if len(reports) != 1 || reports[0].Process != "MyApp" {
		t.Errorf("got %+v", reports)
	}
}

// --- CrashReportsPull ---

func TestClient_CrashReportsPull(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/crash_reports_pull", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, crashReportsPullResponse{Content: "crash text here"})
	})

	_, c := unixTestServer(t, mux)
	text, err := c.CrashReportsPull(context.Background(), "abc", "MyApp_2026-01-01.ips")
	if err != nil {
		t.Fatalf("CrashReportsPull: %v", err)
	}
	if text != "crash text here" {
		t.Errorf("content = %q; want 'crash text here'", text)
	}
}

// --- Power assertion lifecycle ---

func TestClient_PowerAssertionLifecycle(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/acquire_power_assertion", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, acquirePowerAssertionResponse{HandleID: "handle-1"})
	})
	mux.HandleFunc("/v1/refresh_power_assertion", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]any{})
	})
	mux.HandleFunc("/v1/release_power_assertion", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]any{})
	})

	_, c := unixTestServer(t, mux)
	ctx := context.Background()

	handle, err := c.AcquirePowerAssertion(ctx, "abc", "NoIdleSleep", "test", 60, "")
	if err != nil {
		t.Fatalf("AcquirePowerAssertion: %v", err)
	}
	if handle != "handle-1" {
		t.Errorf("handle = %q; want 'handle-1'", handle)
	}

	if err := c.RefreshPowerAssertion(ctx, handle, 60); err != nil {
		t.Fatalf("RefreshPowerAssertion: %v", err)
	}

	if err := c.ReleasePowerAssertion(ctx, handle); err != nil {
		t.Fatalf("ReleasePowerAssertion: %v", err)
	}
}

// --- Error classification ---

func errorServer(code string, status int) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		respond(w, status, bridgeErrorBody{Error: code, Message: "test: " + code})
	})
	return mux
}

func TestClient_ErrorClassification_DeviceNotPaired(t *testing.T) {
	_, c := unixTestServer(t, errorServer("device_not_paired", 409))
	_, err := c.ListDevices(context.Background())
	if !IsDeviceNotPaired(err) {
		t.Errorf("IsDeviceNotPaired = false; want true; err = %v", err)
	}
	var be *BridgeError
	if !asErr(err, &be) || be.Status != 409 {
		t.Errorf("want BridgeError with status 409; got %v", err)
	}
}

func TestClient_ErrorClassification_BundleNotInstalled(t *testing.T) {
	_, c := unixTestServer(t, errorServer("bundle_not_installed", 422))
	_, err := c.LaunchApp(context.Background(), "abc", "com.example.app")
	if !IsBundleNotInstalled(err) {
		t.Errorf("IsBundleNotInstalled = false; want true; err = %v", err)
	}
}

func TestClient_ErrorClassification_TunneldUnavailable(t *testing.T) {
	_, c := unixTestServer(t, errorServer("tunneld_unavailable", 503))
	_, err := c.ListApps(context.Background(), "abc")
	if !IsTunneldUnavailable(err) {
		t.Errorf("IsTunneldUnavailable = false; want true; err = %v", err)
	}
}

func TestClient_ErrorClassification_PMD3Error(t *testing.T) {
	_, c := unixTestServer(t, errorServer("pmd3_error", 500))
	_, err := c.Battery(context.Background(), "abc")
	if !IsPMD3Error(err) {
		t.Errorf("IsPMD3Error = false; want true; err = %v", err)
	}
}

func TestClient_ErrorClassification_None(t *testing.T) {
	if IsDeviceNotPaired(nil) {
		t.Error("IsDeviceNotPaired(nil) = true; want false")
	}
	if IsBundleNotInstalled(nil) {
		t.Error("IsBundleNotInstalled(nil) = true; want false")
	}
}

// --- Context cancellation ---

// TestClient_ContextCancellation verifies that caller-initiated ctx cancel
// returns context.Canceled instead of panicking. Cancellation is the sole
// legitimate escape from the fail-fast error model — it represents daemon
// shutdown, not a bug in the bridge.
func TestClient_ContextCancellation(t *testing.T) {
	mux := http.NewServeMux()
	// Handler exists only so a Listener is available; it should never run.
	mux.HandleFunc("/v1/list_devices", func(w http.ResponseWriter, r *http.Request) {})
	_, c := unixTestServer(t, mux)

	// Pre-cancel the context so the dial fails with context.Canceled rather
	// than making a real round-trip.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.ListDevices(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context; got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled; got %v", err)
	}
}

// TestClient_TransportErrorCalledFatal verifies that a dial failure (bridge
// socket missing) invokes the fatal hook rather than returning an error.
// Under the 🎯T26.2 model, transport failures are bugs.
func TestClient_TransportErrorCallsFatal(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "no.sock") // intentionally non-existent socket
	_ = os.Remove(sock)

	c := NewClient(sock)
	var captured error
	c.fatal = func(err error) { captured = err }

	_, _ = c.ListDevices(context.Background())
	if captured == nil {
		t.Fatal("fatal hook was not called on dial failure")
	}
	if !strings.Contains(captured.Error(), "transport error") {
		t.Errorf("unexpected fatal message: %v", captured)
	}
}

// TestClient_DeadlineExceededCallsFatal verifies that the per-endpoint
// deadline expiring treats the call as a bug (unresponsive bridge).
func TestClient_DeadlineExceededCallsFatal(t *testing.T) {
	// Handler that blocks past the client's endpoint timeout. We use a
	// short custom client to avoid waiting 10s for the real timeout.
	mux := http.NewServeMux()
	release := make(chan struct{})
	mux.HandleFunc("/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	_, c := unixTestServer(t, mux)
	defer close(release)

	var captured error
	c.fatal = func(err error) { captured = err }

	// Use post() directly with a 100ms timeout to exercise deadline path.
	_ = c.post(context.Background(), "/v1/ping", 100*time.Millisecond,
		map[string]any{}, nil)
	if captured == nil {
		t.Fatal("fatal hook was not called on deadline expiry")
	}
	if !strings.Contains(captured.Error(), "transport error") {
		t.Errorf("unexpected fatal message: %v", captured)
	}
}
