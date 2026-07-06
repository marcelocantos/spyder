// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package dashboard_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/dashboard"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/rest"
)

// TestDashboard_LiveTiltbuggy is the 🎯T91.5 data-path oracle: it stands up
// the exact stack the daemon serves — the REST tool surface + the dashboard
// SPA over an app-channel Manager — connects a real direct-mode tiltbuggy on
// a keyed listener (as launch_app would in production), and asserts the
// endpoints the dashboard's JS consumes return the app's data. This gates the
// dashboard's correctness decidably; only the visual UX is left for a human.
//
// Gated on SPYDER_GE_TILTBUGGY (launches a real GUI app); skips headless.
func TestDashboard_LiveTiltbuggy(t *testing.T) {
	bin := os.Getenv("SPYDER_GE_TILTBUGGY")
	if bin == "" {
		t.Skip("set SPYDER_GE_TILTBUGGY=<path to tiltbuggy> to run the T91.5 dashboard smoke test")
	}

	mgr := appchannel.NewManager()
	t.Cleanup(mgr.Close)
	// A keyed listener mirrors production, where launch_app keys by device+
	// bundle and app_channel_list (the dashboard's app source) surfaces it.
	l, err := mgr.GetOrCreateListener(appchannel.AppKey{DeviceID: "dev-smoke", BundleID: "com.squz.tiltbuggy"})
	if err != nil {
		t.Fatalf("GetOrCreateListener: %v", err)
	}

	h := spydermcp.NewHandler(spydermcp.WithAppChannel(mgr))
	mux := http.NewServeMux()
	mux.Handle(rest.Prefix, rest.NewHandler(h))
	mux.Handle(dashboard.Path, dashboard.NewHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "SPYDER_APP_CHANNEL="+fmt.Sprintf("127.0.0.1:%d", l.Port))
	if err := cmd.Start(); err != nil {
		t.Fatalf("launch tiltbuggy: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	// The dashboard page itself must serve.
	if body := httpGet(t, srv.URL+dashboard.Path); !bytes.Contains(body, []byte("spyder dashboard")) {
		t.Fatalf("GET %s did not return the dashboard page", dashboard.Path)
	}

	// app_channel_list is the dashboard's app source — wait for tiltbuggy.
	var sessionID string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res := postTool(t, srv.URL, "app_channel_list", nil)
		for _, ln := range res["listeners"].([]any) {
			lm := ln.(map[string]any)
			for _, s := range asSlice(lm["sessions"]) {
				sm := s.(map[string]any)
				if sm["app_name"] == "tiltbuggy" {
					sessionID, _ = sm["session_id"].(string)
				}
			}
		}
		if sessionID != "" {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if sessionID == "" {
		t.Fatal("tiltbuggy never appeared in app_channel_list (the dashboard app list)")
	}

	// app_tweak_list — the Tweaks panel's data.
	tweaks := postTool(t, srv.URL, "app_tweak_list", map[string]any{"session_id": sessionID})
	names := map[string]bool{}
	for _, tw := range tweaks["_list"].([]any) {
		names[tw.(map[string]any)["name"].(string)] = true
	}
	if !names["camera.zoom"] || !names["physics.grip_scale"] {
		t.Fatalf("Tweaks panel would be empty; app_tweak_list names = %v", names)
	}

	// app_state_slices — the State panel's slice catalogue (shape check; ok if empty).
	_ = postTool(t, srv.URL, "app_state_slices", map[string]any{"session_id": sessionID})
}

// postTool POSTs to /api/v1/<tool> and returns the tool's JSON result parsed
// from the text content block. A top-level JSON array is returned under the
// "_list" key so callers can index it uniformly.
func postTool(t *testing.T, base, tool string, args map[string]any) map[string]any {
	t.Helper()
	var bodyReader io.Reader
	if args != nil {
		b, _ := json.Marshal(args)
		bodyReader = bytes.NewReader(b)
	}
	resp, err := http.Post(base+rest.Prefix+tool, "application/json", bodyReader)
	if err != nil {
		t.Fatalf("POST %s: %v", tool, err)
	}
	defer resp.Body.Close()
	var call struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&call); err != nil {
		t.Fatalf("decode %s result: %v", tool, err)
	}
	if len(call.Content) == 0 {
		return map[string]any{}
	}
	text := call.Content[0].Text
	if call.IsError {
		t.Fatalf("%s returned error: %s", tool, text)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err == nil {
		return obj
	}
	var arr []any
	if err := json.Unmarshal([]byte(text), &arr); err == nil {
		return map[string]any{"_list": arr}
	}
	t.Fatalf("%s result not JSON: %s", tool, text)
	return nil
}

func httpGet(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
