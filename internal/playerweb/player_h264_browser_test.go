// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package playerweb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	goruntime "runtime"

	"github.com/chromedp/chromedp"
)

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
	t.Skip("no Chrome/Chromium binary (set SPYDER_TEST_CHROME); skipping wasm WebCodecs browser oracle")
	return ""
}

func playerWebBundleDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	dist := filepath.Join(root, "player", "web", "dist")
	if st, err := os.Stat(filepath.Join(dist, "player.wasm")); err != nil || st.IsDir() {
		t.Skip("player wasm bundle missing; run `make -C player web`")
	}
	return dist
}

// TestPlayer_WasmWebCodecsH264_InChrome is the 🎯T101.5 class-1 oracle in
// headless Chrome: wasm player connects to a mock H.264 wire, WebCodecs
// decodes SP2V, and PlayerFPS reports ≥55 fps at 320×240 stream resolution.
func TestPlayer_WasmWebCodecsH264_InChrome(t *testing.T) {
	chromePath := findChrome(t)
	bundleDir := playerWebBundleDir(t)

	fixture := filepath.Join("testdata", "baseline_320x240_60fps.h264")
	raw, err := os.ReadFile(fixture)
	if err != nil {
		_, file, _, _ := goruntime.Caller(0)
		fixture = filepath.Join(filepath.Dir(file), "testdata", "baseline_320x240_60fps.h264")
		raw, err = os.ReadFile(fixture)
	}
	if err != nil {
		t.Fatal(err)
	}
	aus, err := annexBAccessUnits(raw)
	if err != nil {
		t.Fatal(err)
	}

	st := &h264WireState{}
	mux := http.NewServeMux()
	registerMockH264Wire(mux, aus, st)
	// Test index hooks console.log before player.js loads (wasm spdlog → console).
	hookIndex := filepath.Join("testdata", "player_index_hook.html")
	if _, err := os.Stat(hookIndex); err != nil {
		_, file, _, _ := goruntime.Caller(0)
		hookIndex = filepath.Join(filepath.Dir(file), "testdata", "player_index_hook.html")
	}
	mux.HandleFunc(Path+"/index.html", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, hookIndex)
	})
	mux.Handle(Path+"/", http.StripPrefix(Path+"/", http.FileServer(http.Dir(bundleDir))))
	mux.Handle(Path, http.RedirectHandler(Path+"/", http.StatusMovedPermanently))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	hostPort := strings.TrimPrefix(srv.URL, "http://")
	h, p, _ := netSplitHostPort(hostPort)
	playerURL := fmt.Sprintf("%s/player/index.html?name=test&host=%s&port=%s", srv.URL, h, p)

	var logsMu sync.Mutex
	var logs []string
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromePath),
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
		)...)
	defer cancelAlloc()
	browser, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()
	testCtx, cancelTest := context.WithTimeout(browser, 85*time.Second)
	defer cancelTest()

	if err := chromedp.Do(testCtx, chromedp.Navigate(playerURL)); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	deadline := time.Now().Add(75 * time.Second)
	var ok bool
	for time.Now().Before(deadline) {
		rawJSON, err := chromedp.Run(testCtx, chromedp.Evaluate[string](
			`JSON.stringify(globalThis.spyderTest || {})`))
		if err == nil && rawJSON != "" && rawJSON != "{}" {
			var probe struct {
				WCConfigured bool    `json:"wcConfigured"`
				WCFrames     float64 `json:"wcFrames"`
				LastFPS      float64 `json:"lastFPS"`
				Cmdstream    float64 `json:"cmdstream"`
			}
			if json.Unmarshal([]byte(rawJSON), &probe) == nil {
				logsMu.Lock()
				logs = []string{rawJSON}
				logsMu.Unlock()
				if probe.WCConfigured && probe.Cmdstream == 0 && probe.WCFrames >= 30 &&
					probe.LastFPS >= 55 {
					ok = true
					break
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ok {
		err := fmt.Errorf("timeout waiting for WebCodecs configure + PlayerFPS ≥55 (video rung)")
		logsMu.Lock()
		tail := logs
		if len(tail) > 50 {
			tail = tail[len(tail)-50:]
		}
		logsMu.Unlock()
		diag, _ := chromedp.Run(testCtx, chromedp.Evaluate[string](
			`JSON.stringify(globalThis.spyderTest || {})`))
		t.Fatalf("%v\nlast probe:\n%s\ndiag: %s", err, strings.Join(tail, "\n"), diag)
	}

	caps, transport, frames := st.snapshot()
	if transport != kTransportH264 {
		t.Fatalf("expected H.264 transport, got %d", transport)
	}
	if caps&kCapH264 == 0 {
		t.Fatalf("DeviceInfo missing kCapH264 (caps=%d)", caps)
	}
	if caps&kCapCommandStream == 0 {
		t.Fatalf("DeviceInfo missing kCapCommandStream (caps=%d)", caps)
	}
	if frames < 55 {
		t.Fatalf("mock wire sent too few SP2V frames: %d", frames)
	}
}

var playerFPSRe = regexp.MustCompile(`PlayerFPS: ([0-9.]+) fps .+ cmdstream=([01])`)

func scanPlayerLogs(logs []string) (webCodecsOK bool, bestFPS float64) {
	for _, ln := range logs {
		if strings.Contains(ln, "WebCodecs configured") {
			webCodecsOK = true
		}
		if m := playerFPSRe.FindStringSubmatch(ln); len(m) == 3 {
			if m[2] == "0" {
				v, _ := strconv.ParseFloat(m[1], 64)
				if v > bestFPS {
					bestFPS = v
				}
			}
		}
	}
	return webCodecsOK, bestFPS
}

func netSplitHostPort(host string) (string, string, error) {
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i], host[i+1:], nil
	}
	return host, "80", nil
}
