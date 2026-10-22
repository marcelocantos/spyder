// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
)

// 🎯T170: concurrent `devices` requests share one listing, and a completed
// listing is reused briefly instead of spawning adb/devicectl/ioreg again.
func TestDevices_ConcurrentRequestsShareOneListing(t *testing.T) {
	var iosCalls, androidCalls, censuses atomic.Int32
	release := make(chan struct{})
	ios := &stubAdapter{list: func() ([]device.Info, error) {
		iosCalls.Add(1)
		<-release
		return []device.Info{{UUID: "00008103-001122334455667A", Platform: "ios"}}, nil
	}}
	android := &stubAdapter{list: func() ([]device.Info, error) {
		androidCalls.Add(1)
		return []device.Info{{UUID: "R5CR112X76K", Platform: "android"}}, nil
	}}
	h := newHandlerWithStubs(t, ios, android)
	h.readUSBCensus = func() ([]byte, error) {
		censuses.Add(1)
		return []byte{}, nil
	}

	const callers = 8
	var wg sync.WaitGroup
	texts := make([]string, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := dispatchJSON(t, h, "devices", map[string]any{"platform": "all"})
			texts[i] = resultText(t, &r)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	// A request inside the reuse window is answered from the same listing.
	r := dispatchJSON(t, h, "devices", map[string]any{"platform": "all"})
	texts = append(texts, resultText(t, &r))

	if iosCalls.Load() != 1 || androidCalls.Load() != 1 || censuses.Load() != 1 {
		t.Fatalf("ios=%d android=%d ioreg=%d listings for %d requests, want 1 each",
			iosCalls.Load(), androidCalls.Load(), censuses.Load(), callers+1)
	}
	for i, text := range texts {
		if !strings.Contains(text, "00008103-001122334455667A") || !strings.Contains(text, "R5CR112X76K") {
			t.Fatalf("request %d: %s", i, text)
		}
	}
}

// After the reuse window a request lists afresh.
func TestDevices_ListsAgainAfterReuseWindow(t *testing.T) {
	var androidCalls atomic.Int32
	android := &stubAdapter{list: func() ([]device.Info, error) {
		androidCalls.Add(1)
		return nil, nil
	}}
	h := newHandlerWithStubs(t, &stubAdapter{}, android)
	h.readUSBCensus = func() ([]byte, error) { return []byte{}, nil }
	dispatchJSON(t, h, "devices", map[string]any{"platform": "android"})
	h.devicesMu.Lock()
	snap := h.devicesCache["android"]
	snap.at = time.Now().Add(-2 * devicesTTL)
	h.devicesCache["android"] = snap
	h.devicesMu.Unlock()
	dispatchJSON(t, h, "devices", map[string]any{"platform": "android"})
	if got := androidCalls.Load(); got != 2 {
		t.Fatalf("android listings = %d, want 2", got)
	}
}

// 🎯T169: an iOS census that overruns its budget does not hold up Android
// rows; the response carries the last good iOS rows and says so, and the
// census refreshes them when it finishes.
func TestDevices_SlowIOSAnswersWithLastGoodRows(t *testing.T) {
	var slow atomic.Bool
	release := make(chan struct{})
	ios := &stubAdapter{list: func() ([]device.Info, error) {
		if slow.Load() {
			<-release
			return []device.Info{{UUID: "00008110-0014182E0AC2801E", Platform: "ios"}}, nil
		}
		return []device.Info{{UUID: "00008103-001122334455667A", Platform: "ios"}}, nil
	}}
	android := &stubAdapter{list: func() ([]device.Info, error) {
		return []device.Info{{UUID: "R5CR112X76K", Platform: "android"}}, nil
	}}
	h := newHandlerWithStubs(t, ios, android)
	h.readUSBCensus = func() ([]byte, error) { return []byte{}, nil }
	h.iosListBudget = 100 * time.Millisecond

	dispatchJSON(t, h, "devices", map[string]any{"platform": "all"}) // last good iOS rows
	h.devicesMu.Lock()
	delete(h.devicesCache, "all")
	h.devicesMu.Unlock()

	slow.Store(true)
	started := time.Now()
	r := dispatchJSON(t, h, "devices", map[string]any{"platform": "all"})
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("listing waited %s for a slow iOS census", elapsed)
	}
	text := resultText(t, &r)
	for _, want := range []string{"R5CR112X76K", "00008103-001122334455667A", "listing still running"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.devicesMu.Lock()
		fresh := len(h.lastIOS) == 1 && h.lastIOS[0].UUID == "00008110-0014182E0AC2801E"
		h.devicesMu.Unlock()
		if fresh {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the overrunning census never refreshed the last good iOS rows")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// With no good iOS rows yet, an overrun is reported as an iOS error and
// Android rows still arrive.
func TestDevices_SlowIOSWithNoHistoryReportsError(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ios := &stubAdapter{list: func() ([]device.Info, error) {
		<-release
		return nil, nil
	}}
	android := &stubAdapter{list: func() ([]device.Info, error) {
		return []device.Info{{UUID: "R5CR112X76K", Platform: "android"}}, nil
	}}
	h := newHandlerWithStubs(t, ios, android)
	h.readUSBCensus = func() ([]byte, error) { return []byte{}, nil }
	h.iosListBudget = 100 * time.Millisecond
	r := dispatchJSON(t, h, "devices", map[string]any{"platform": "all"})
	text := resultText(t, &r)
	if r.IsError || !strings.Contains(text, "R5CR112X76K") || !strings.Contains(text, "ios: listing still running") {
		t.Fatalf("isError=%v body=%s", r.IsError, text)
	}
}
