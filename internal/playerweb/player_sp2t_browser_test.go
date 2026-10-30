// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package playerweb

import (
	"bytes"
	"context"
	"database/sql"
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
	received := make(chan []byte, 8)
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
					received <- nil
				} else {
					received <- bytes.Clone(msg[8:])
				}
			}
		}
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
	var persisted bool
	for time.Now().Before(deadline) {
		result, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`new Promise(resolve => { const q = indexedDB.open('/spyder-state'); q.onerror = () => resolve(false); q.onsuccess = () => { const d=q.result; if (!d.objectStoreNames.contains('FILE_DATA')) { d.close(); resolve(false); return; } const tx=d.transaction('FILE_DATA'); const get=tx.objectStore('FILE_DATA').get('/spyder-state/games/alpha.db'); get.onsuccess=()=>{const ok=!!get.result; d.close(); resolve(ok)}; get.onerror=()=>{d.close(); resolve(false)}; }; })`, chromedp.EvalAwaitPromise))
		if err == nil && result {
			persisted = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !persisted {
		t.Fatal("SP2T snapshot did not persist in real IndexedDB before tab close")
	}
	closeTab()
	ctx, closeTab = newTab()
	defer func() { closeTab() }()
	if _, err := chromedp.Run(ctx, chromedp.Navigate(url("alpha"))); err != nil {
		t.Fatalf("reopen alpha: %v", err)
	}
	select {
	case got := <-received:
		if !bytes.Equal(got, seed) {
			t.Fatalf("reopened alpha seed differs: got %d bytes, want %d", len(got), len(seed))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("reopened alpha did not send SP2T seed")
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
		t.Fatalf("beta leaked alpha SP2T seed (%d bytes)", len(got))
	case <-time.After(1500 * time.Millisecond):
	}
}
