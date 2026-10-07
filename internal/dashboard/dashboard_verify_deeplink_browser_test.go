// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package dashboard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/marcelocantos/spyder/internal/dashboard"
	spydermcp "github.com/marcelocantos/spyder/internal/mcp"
	"github.com/marcelocantos/spyder/internal/rest"
	"github.com/marcelocantos/spyder/internal/verify"
)

// findChrome locates a real Chrome/Chromium binary for chromedp to drive.
// The test is skipped (not failed) when none is found, so it stays green on
// hosts without a browser installed while still running as a real-browser
// check wherever one is (🎯T147.2).
func findChrome(t *testing.T) string {
	t.Helper()
	candidates := []string{
		os.Getenv("SPYDER_TEST_CHROME"),
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, p)
		}
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	t.Skip("no Chrome/Chromium binary found (set SPYDER_TEST_CHROME); skipping real-browser deep-link test")
	return ""
}

// TestDashboard_VerifyDeepLinkToDismissedRunRendersInRealBrowser is the
// committed real-browser journey for 🎯T147.2: start a run, let it finish,
// dismiss it so it drops off the live dashboard, then open a real Chrome
// tab on its #verify/<id> link and assert the dismissed run's report
// (workflow name and its step) renders from the backend's ReportAny/
// runs-index lookup (🎯T147) rather than silently falling back to some
// other run or an empty pane.
func TestDashboard_VerifyDeepLinkToDismissedRunRendersInRealBrowser(t *testing.T) {
	chromePath := findChrome(t)

	h := spydermcp.NewHandler()
	mux := http.NewServeMux()
	mux.Handle(rest.Prefix, rest.NewHandler(h))
	dash := dashboard.NewHandler()
	mux.Handle(dashboard.Path, dash)
	mux.Handle(dashboard.Path+"/", dash)
	// The Verify tab needs its live feed and stable-report routes wired,
	// same as internal/daemon.daemon.go does, or the dashboard JS never
	// gets past "offline" (🎯T147.2).
	mux.HandleFunc(verify.WSPath, h.VerifyHub().HandleWS)
	mux.HandleFunc(verify.ReportPathPrefix, h.VerifyHub().HandleReport)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	wf := `
name: deeplink-sample
params:
  device: handset
steps:
  - id: hello
    type: shell
    argv: ["/usr/bin/true"]
`
	cwd := t.TempDir()
	started := postTool(t, srv.URL, "verify", map[string]any{
		"workflow": wf,
		"cwd":      cwd,
		"wait":     true,
	})
	runID, _ := started["run_id"].(string)
	if runID == "" {
		t.Fatalf("verify start: %v", started)
	}

	// Wait for the run to reach a finished status before dismissing it.
	var status string
	for i := 0; i < 100; i++ {
		snap := postTool(t, srv.URL, "verify_status", nil)
		for _, item := range asSlice(snap["runs"]) {
			rm, _ := item.(map[string]any)
			if rm["run_id"] == runID {
				status, _ = rm["status"].(string)
			}
		}
		if status != "" && status != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status == "running" || status == "" {
		t.Fatalf("run %s never finished (status=%q)", runID, status)
	}

	dismiss := postTool(t, srv.URL, "verify_dismiss", map[string]any{"run_id": runID, "cwd": cwd})
	if dismiss["ok"] != true {
		t.Fatalf("verify_dismiss: %v", dismiss)
	}

	// Confirm it is actually gone from the live snapshot: otherwise this
	// test would pass for the wrong reason (the live path, not ReportAny).
	snap := postTool(t, srv.URL, "verify_status", nil)
	for _, item := range asSlice(snap["runs"]) {
		rm, _ := item.(map[string]any)
		if rm["run_id"] == runID {
			t.Fatalf("run %s still live after dismiss; test would not exercise the archived path", runID)
		}
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromePath),
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
		)...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
	defer cancelTimeout()

	url := srv.URL + dashboard.Path + "#verify/" + runID
	if err := chromedp.Do(ctx,
		chromedp.Navigate(url),
		chromedp.Sleep(3*time.Second),
	); err != nil {
		t.Fatalf("chromedp navigate: %v", err)
	}
	var title string
	var err error
	for i := 0; i < 50; i++ {
		title, err = chromedp.Run(ctx, chromedp.Text(`.verify-run-head h3`))
		if err == nil && strings.TrimSpace(title) != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("chromedp text(title): %v", err)
	}
	status2, err := chromedp.Run(ctx, chromedp.Text(`.verify-run-head [data-v="status"]`))
	if err != nil {
		t.Fatalf("chromedp text(status): %v", err)
	}
	stepText, err := chromedp.Run(ctx, chromedp.Text(`[data-v="timeline"]`))
	if err != nil {
		t.Fatalf("chromedp text(timeline): %v", err)
	}

	if !strings.Contains(title, "deeplink-sample") {
		t.Errorf("rendered title = %q, want it to contain %q", title, "deeplink-sample")
	}
	if !strings.Contains(stepText, "hello") {
		t.Errorf("rendered timeline = %q, want it to contain step %q", stepText, "hello")
	}
	if status2 == "" {
		t.Errorf("rendered status was empty for the dismissed run's report")
	}
}
