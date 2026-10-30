// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
package playerweb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// A real browser executes the shipped platform script, not only the JS mock.
// Physical iOS Safari permission and tilt gameplay remain a separate HIL oracle.
func TestPlatformChrome(t *testing.T) {
	chrome := os.Getenv("SPYDER_TEST_CHROME")
	if chrome == "" {
		chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Skip("Chrome unavailable")
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "player", "web")
	page := `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><canvas id="canvas"></canvas><script src="/platform.js"></script>`
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, page) })
	mux.Handle("/platform.js", http.FileServer(http.Dir(root)))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome), chromedp.Flag("headless", true), chromedp.Flag("disable-gpu", true))...)
	defer cancelAlloc()
	browser, cancelBrowser := chromedp.NewContext(alloc)
	defer cancelBrowser()
	ctx, cancel := context.WithTimeout(browser, 20*time.Second)
	defer cancel()
	if _, err := chromedp.Run(ctx, chromedp.Navigate(srv.URL)); err != nil {
		t.Fatal(err)
	}
	result, err := chromedp.Run(ctx, chromedp.Evaluate[string](`(() => {
 const p = globalThis.spyderPlatform;
 p.configure(1, 1, 3);
 return JSON.stringify({ready:!!p, preMotion:p.takeMotion(0), policy:!document.getElementById('glass-policy').hidden, safe:p.insets().every(Number.isFinite)});
 })()`))
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"ready":true,"preMotion":null,"policy":true,"safe":true}` {
		t.Fatalf("browser platform: %s", result)
	}
}
