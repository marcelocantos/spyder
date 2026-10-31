// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package playerweb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/coder/websocket"
	_ "modernc.org/sqlite"
)

const sp2tMagic = 0x53503254

// TestPlayer_SP2T_IDBFS_TabReopenSeedsServer exercises the compiled player in
// Chrome, not the Node mock of FS.syncfs. A server pushes a SQLite snapshot;
// closing the tab destroys the wasm FS; the next tab must send the same
// user-table database to the server before any new push. A different game
// must not seed from the first game's database.
func TestPlayer_SP2T_IDBFS_TabReopenSeedsServer(t *testing.T) {
	chrome := findChrome(t)
	dist := os.Getenv("SPYDER_TEST_PLAYER_WEB_DIST")
	if dist == "" {
		dist = playerWebBundleDir(t)
	}
	if _, err := os.Stat(filepath.Join(dist, "player.wasm")); err != nil {
		t.Fatalf("wasm bundle: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "seed.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE progress (level INTEGER NOT NULL); INSERT INTO progress VALUES (42)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(seed, []byte("SQLite format 3")) {
		t.Fatal("fixture is not SQLite")
	}

	var mu sync.Mutex
	var connections int
	type wireSeed struct {
		connection int
		data       []byte
	}
	received := make(chan wireSeed, 16)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/wire", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		mu.Lock()
		connections++
		n := connections
		mu.Unlock()
		if err := c.Write(ctx, websocket.MessageBinary, buildSessionConfigMessage(kTransportH264)); err != nil {
			return
		}
		// Wait for DeviceInfo (connect handshake), then inspect any player seed.
		for {
			_, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			if len(msg) < 8 {
				continue
			}
			switch binary.LittleEndian.Uint32(msg) {
			case kDeviceInfoMagic:
				if n == 1 {
					packet := make([]byte, 8+len(seed))
					binary.LittleEndian.PutUint32(packet, sp2tMagic)
					binary.LittleEndian.PutUint32(packet[4:], uint32(len(seed)))
					copy(packet[8:], seed)
					if err := c.Write(ctx, websocket.MessageBinary, packet); err != nil {
						return
					}
				}
			case sp2tMagic:
				// A player seed must be a complete, correctly framed SQLite image.
				if int(binary.LittleEndian.Uint32(msg[4:])) != len(msg)-8 {
					received <- wireSeed{connection: n}
				} else {
					received <- wireSeed{connection: n, data: bytes.Clone(msg[8:])}
				}
			}
		}
	})
	// Capture the compiled player's own logs in the tab for a useful failure
	// diagnosis without changing production code or interpreting an empty wire.
	index, err := os.ReadFile(filepath.Join(dist, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	capture := `<script>globalThis.sp2tLogs=[]; for(const k of ['log','error']) { const prior=console[k].bind(console); console[k]=function(...xs){globalThis.sp2tLogs.push(xs.join(' '));prior(...xs)} }</script>`
	if !bytes.Contains(index, []byte(`<script src="player.js"></script>`)) {
		t.Fatal("player index missing script anchor")
	}
	index = bytes.Replace(index, []byte(`<script src="player.js"></script>`), []byte(capture+`<script src="player.js"></script>`), 1)
	mux.HandleFunc("/player/index.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
	// Blank same-origin page checks browser-profile continuity without booting wasm.
	mux.HandleFunc("/player/profile-probe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>profile probe</title>"))
	})
	mux.Handle("/player/", http.StripPrefix("/player/", http.FileServer(http.Dir(dist))))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host, port, err := netSplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	url := func(name string) string {
		return fmt.Sprintf("%s/player/index.html?name=%s&host=%s&port=%s", srv.URL, name, host, port)
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome), chromedp.Flag("headless", true), chromedp.Flag("disable-gpu", true))...)
	defer cancelAlloc()
	browser, cancelBrowser := chromedp.NewContext(alloc)
	defer cancelBrowser()
	// Allocate the browser on the parent BEFORE making children. Otherwise each
	// child's first Run allocates its own Chrome/profile and loses IndexedDB.
	if _, err := chromedp.Run(browser, chromedp.Navigate("about:blank")); err != nil {
		t.Fatalf("allocate shared Chrome browser: %v", err)
	}
	newTab := func() (context.Context, context.CancelFunc) {
		tab, closeTab := chromedp.NewContext(browser)
		ctx, cancelTimeout := context.WithTimeout(tab, 70*time.Second)
		return ctx, func() { cancelTimeout(); closeTab() }
	}
	// Separate tabs share Chrome's IndexedDB, but not the wasm memory FS.
	ctx, closeTab := newTab()
	if _, err := chromedp.Run(ctx, chromedp.Navigate(url("alpha"))); err != nil {
		t.Fatalf("first tab: %v", err)
	}
	deadline := time.Now().Add(25 * time.Second)
	for {
		mu.Lock()
		n := connections
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first player never connected")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Wait for IDBFS's asynchronous syncfs(false), not just the wire write.
	// IndexedDB is the actual persistence oracle; read it from the browser.
	const readAlphaIDB = `new Promise(resolve => { const q = indexedDB.open('/spyder-state'); q.onerror = () => resolve(''); q.onsuccess = () => { const d=q.result; if (!d.objectStoreNames.contains('FILE_DATA')) { d.close(); resolve(''); return; } const tx=d.transaction('FILE_DATA'); const get=tx.objectStore('FILE_DATA').get('/spyder-state/games/alpha.db'); get.onsuccess=()=>{const bytes=get.result?.contents; d.close(); resolve(bytes ? btoa(String.fromCharCode(...bytes)) : '')}; get.onerror=()=>{d.close(); resolve('')}; }; })`
	var persisted bool
	for time.Now().Before(deadline) {
		encoded, err := chromedp.Run(ctx, chromedp.Evaluate[string](readAlphaIDB, chromedp.EvalAwaitPromise))
		if err == nil && encoded != "" {
			stored, decodeErr := base64.StdEncoding.DecodeString(encoded)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if !bytes.Equal(stored, seed) {
				t.Fatalf("IndexedDB alpha.db differs from pushed user-table fixture: got %d bytes, want %d", len(stored), len(seed))
			}
			persisted = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !persisted {
		t.Fatal("SP2T snapshot did not persist in real IndexedDB before tab close")
	}
	// Do not allow a first-connection seed to satisfy the reopen assertion.
	select {
	case got := <-received:
		t.Fatalf("first attach unexpectedly sent SP2T seed on connection %d (%d bytes)", got.connection, len(got.data))
	default:
	}
	closeTab()
	ctx, closeTab = newTab()
	defer func() { closeTab() }()
	// A same-origin blank tab must see the first tab's exact IndexedDB bytes
	// before the player runs: this guards the test's shared-profile premise.
	if _, err := chromedp.Run(ctx, chromedp.Navigate(srv.URL+"/player/profile-probe")); err != nil {
		t.Fatalf("navigate reopened browser profile: %v", err)
	}
	reopenedIDB, err := chromedp.Run(ctx, chromedp.Evaluate[string](readAlphaIDB, chromedp.EvalAwaitPromise))
	if err != nil {
		t.Fatalf("probe reopened browser profile: %v", err)
	}
	reopenedBytes, err := base64.StdEncoding.DecodeString(reopenedIDB)
	if err != nil || !bytes.Equal(reopenedBytes, seed) {
		t.Fatalf("reopened tab lacks first tab's IndexedDB bytes before player boot: got %d bytes, decode error %v; want %d", len(reopenedBytes), err, len(seed))
	}
	if _, err := chromedp.Run(ctx, chromedp.Navigate(url("alpha"))); err != nil {
		t.Fatalf("reopen alpha: %v", err)
	}
	select {
	case got := <-received:
		if got.connection != 2 {
			t.Fatalf("SP2T seed belongs to connection %d, not reopened alpha connection 2", got.connection)
		}
		if !bytes.Equal(got.data, seed) {
			t.Fatalf("reopened alpha seed differs: got %d bytes, want %d", len(got.data), len(seed))
		}
	case <-time.After(20 * time.Second):
		logs, _ := chromedp.Run(ctx, chromedp.Evaluate[string](`JSON.stringify({status:document.getElementById('status')?.textContent,logs:globalThis.sp2tLogs?.slice(-30)})`))
		mu.Lock()
		count := connections
		mu.Unlock()
		t.Fatalf("reopened alpha did not send SP2T seed; connections=%d browser=%s", count, logs)
	}
	closeTab()
	ctx, closeTab = newTab()
	if _, err := chromedp.Run(ctx, chromedp.Navigate(url("beta"))); err != nil {
		t.Fatalf("open beta: %v", err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := connections
		mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	n := connections
	mu.Unlock()
	if n < 3 {
		t.Fatal("beta never connected")
	}
	select {
	case got := <-received:
		t.Fatalf("beta or prior connection sent unexpected SP2T seed: connection=%d bytes=%d", got.connection, len(got.data))
	case <-time.After(1500 * time.Millisecond):
	}
}
