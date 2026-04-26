// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// unixTestServer is the legacy name retained for call-site compatibility;
// it now stands up an httptest.Server on loopback TCP (🎯T26.1 flipped the
// transport away from Unix sockets) and returns a Client pointing at it
// with a fixed test token.
func unixTestServer(t *testing.T, mux http.Handler) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, NewClient(srv.URL, "test-token")
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

// --- CrashReportsList (NDJSON streaming) ---

// writeNDJSONLine writes one NDJSON line and flushes so the client sees it
// before the next write. Flushing is essential for the inter-packet
// deadline tests — without it httptest.Server batches the response.
func writeNDJSONLine(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
	_, _ = w.Write([]byte{'\n'})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func TestClient_CrashReportsList_StreamsNDJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/crash_reports_list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		writeNDJSONLine(w, CrashReport{
			Name: "MyApp_2026-01-01.ips", Process: "MyApp",
			Timestamp: "2026-01-01T00:00:00Z",
		})
		writeNDJSONLine(w, CrashReport{
			Name: "MyApp_2026-01-02.ips", Process: "MyApp",
			Timestamp: "2026-01-02T00:00:00Z",
		})
	})

	_, c := unixTestServer(t, mux)
	reports, err := c.CrashReportsList(context.Background(), "abc", time.Time{}, "")
	if err != nil {
		t.Fatalf("CrashReportsList: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("got %d reports; want 2", len(reports))
	}
	if reports[0].Name != "MyApp_2026-01-01.ips" || reports[1].Name != "MyApp_2026-01-02.ips" {
		t.Errorf("unexpected reports: %+v", reports)
	}
}

// --- CrashReportsPull (octet-stream streaming) ---

func TestClient_CrashReportsPull_StreamsBytes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/crash_reports_pull", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chunk-a"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("chunk-b"))
	})
	_, c := unixTestServer(t, mux)

	text, err := c.CrashReportsPull(context.Background(), "abc", "name.ips")
	if err != nil {
		t.Fatalf("CrashReportsPull: %v", err)
	}
	if text != "chunk-achunk-b" {
		t.Errorf("content = %q; want 'chunk-achunk-b'", text)
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

// TestClient_SendsAuthHeader verifies the Authorization: Bearer <token>
// header is set on every outgoing request (🎯T26.1).
func TestClient_SendsAuthHeader(t *testing.T) {
	var seenAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/list_devices", func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		respond(w, http.StatusOK, listDevicesResponse{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "abc123")
	if _, err := c.ListDevices(context.Background()); err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if seenAuth != "Bearer abc123" {
		t.Errorf("Authorization = %q; want Bearer abc123", seenAuth)
	}
}

// TestClient_TransportErrorReturnsWithoutPanic verifies that a dial
// failure (bridge not listening on the advertised port) returns the
// error to the caller WITHOUT invoking the fatal hook. Under 🎯T41,
// transport faults are recoverable: the supervisor restarts the bridge
// subprocess transparently and the next call succeeds. Panicking the
// daemon on every transient was the v0.18/v0.19 bug that kicked
// connected MCP clients every ~30 minutes.
func TestClient_TransportErrorReturnsWithoutPanic(t *testing.T) {
	// A bound-then-closed TCP port gives us a guaranteed dial-refused URL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := srv.URL
	srv.Close()

	c := NewClient(deadURL, "test-token")
	var captured error
	c.fatal = func(err error) { captured = err }

	_, err := c.ListDevices(context.Background())
	if err == nil {
		t.Fatal("ListDevices returned nil error against a dead URL")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected dial-refused error, got: %v", err)
	}
	if captured != nil {
		t.Errorf("fatal hook should NOT have fired for transport error; got %v", captured)
	}
}

// TestClient_DeadlineExceededReturnsWithoutPanic verifies that a
// per-endpoint deadline expiry returns the error to the caller WITHOUT
// invoking the fatal hook. Same rationale as
// TestClient_TransportErrorReturnsWithoutPanic — a wedged bridge should
// not take the daemon down.
func TestClient_DeadlineExceededReturnsWithoutPanic(t *testing.T) {
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
	err := c.post(context.Background(), "/v1/ping", 100*time.Millisecond,
		map[string]any{}, nil)
	if err == nil {
		t.Fatal("post returned nil error after deadline expiry")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
	if captured != nil {
		t.Errorf("fatal hook should NOT have fired for deadline expiry; got %v", captured)
	}
}

// TestClient_UnstructuredErrorResponseStillCallsFatal preserves the
// other half of 🎯T41's split: a 5xx with a non-JSON body is a bridge
// protocol bug (the bridge contract says all errors are JSON), and
// those still surface immediately via the fatal hook so they don't get
// silently swallowed.
func TestClient_UnstructuredErrorResponseStillCallsFatal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("not-json garbage from a misbehaving bridge"))
	})
	_, c := unixTestServer(t, mux)

	var captured error
	c.fatal = func(err error) { captured = err }

	_ = c.post(context.Background(), "/v1/ping", 1*time.Second,
		map[string]any{}, nil)
	if captured == nil {
		t.Fatal("fatal hook should have fired for unstructured error response")
	}
	if !strings.Contains(captured.Error(), "unstructured error response") {
		t.Errorf("unexpected fatal message: %v", captured)
	}
}
