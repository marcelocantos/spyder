// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package streamrelay

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// kVideoStreamMagic is ge's "GE2V" video-frame magic (Protocol.h).
const kVideoStreamMagic = 0x47453256

// TestStream_Tiltbuggy_Live is the 🎯T92.2 class-1 oracle: a REAL tiltbuggy in
// GE_STREAM mode connects to the relay, a player attaches, and real H.264
// frames captured+encoded from the running game flow server→spyder→player — no
// ged. Proves the whole ge streaming path (capture hook + VideoToolbox encode +
// wire) end-to-end.
//
// Gated on SPYDER_GE_TILTBUGGY (launches a real GUI app); skips headless.
func TestStream_Tiltbuggy_Live(t *testing.T) {
	bin := os.Getenv("SPYDER_GE_TILTBUGGY")
	if bin == "" {
		t.Skip("set SPYDER_GE_TILTBUGGY=<path to tiltbuggy> to run the T92.2 stream e2e")
	}

	relay := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/server", relay.HandleServerSideband)
	mux.HandleFunc("/ws/server/wire/", relay.HandleServerWire)
	mux.HandleFunc("/stream/player/", relay.HandlePlayerConnect)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)

	// Launch tiltbuggy mirroring to the relay.
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "GE_STREAM="+u.Host)
	if err := cmd.Start(); err != nil {
		t.Fatalf("launch tiltbuggy: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	// The game's stream client must register with the relay.
	if !waitFor(20*time.Second, func() bool {
		for _, s := range relay.Servers() {
			if s.Name == "tiltbuggy" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("tiltbuggy did not connect to the stream relay")
	}

	// Attach a player; the relay tells the game to open its wire and stream.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	player, _, err := websocket.Dial(ctx, base+"/stream/player/tiltbuggy", nil)
	if err != nil {
		t.Fatalf("player dial: %v", err)
	}
	player.SetReadLimit(maxFrameBytes)
	t.Cleanup(func() { _ = player.Close(websocket.StatusNormalClosure, "") })

	// Read until a real H.264 video frame arrives (skip SessionConfig etc.).
	var frames, videoBytes int
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		typ, data, rerr := player.Read(ctx)
		if rerr != nil {
			break
		}
		if typ != websocket.MessageBinary || len(data) < 8 {
			continue
		}
		magic := binary.LittleEndian.Uint32(data[:4])
		if magic == kVideoStreamMagic && len(data) > 8+1+4 {
			frames++
			videoBytes += len(data) - (8 + 1 + 4)
			if frames >= 3 { // a few frames = the pipeline is live
				break
			}
		}
	}
	if frames < 3 {
		t.Fatalf("expected H.264 frames from tiltbuggy, got %d (%d payload bytes)", frames, videoBytes)
	}
	t.Logf("received %d H.264 frames (%d payload bytes) server→spyder→player, no ged", frames, videoBytes)
}
