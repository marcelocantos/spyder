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

// kVideoStreamMagic is stream wire "SP2V" video-frame magic (Protocol.h).
const kVideoStreamMagic = 0x53503256

// TestStream_Tiltbuggy_Live is the 🎯T92.2 class-1 oracle: a REAL tiltbuggy in
// server stream mode connects to the relay, a player attaches, and real H.264
// frames captured+encoded from the running game flow server→spyder→player — no
// Proves the whole ge streaming path (capture hook + VideoToolbox encode +
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
	cmd.Env = append(os.Environ(), "GE_SERVER="+u.Host)
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

	// Read video frames; also verify a keyframe is self-decodable (carries
	// SPS+PPS+IDR inline) so a WebCodecs player can initialize from it.
	var frames, videoBytes int
	keyframeOK := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		typ, data, rerr := player.Read(ctx)
		if rerr != nil {
			break
		}
		if typ != websocket.MessageBinary || len(data) <= 13 { // 8 hdr + 1 flags + 4 seq
			continue
		}
		if binary.LittleEndian.Uint32(data[:4]) != kVideoStreamMagic {
			continue
		}
		frames++
		videoBytes += len(data) - 13
		if data[8]&1 == 1 && !keyframeOK { // keyframe flag
			keyframeOK = selfDecodableKeyframe(data[13:])
		}
		if frames >= 3 && keyframeOK {
			break
		}
	}
	if frames < 3 {
		t.Fatalf("expected H.264 frames from tiltbuggy, got %d (%d payload bytes)", frames, videoBytes)
	}
	if !keyframeOK {
		t.Fatal("no self-decodable keyframe (SPS+PPS+IDR inline) seen — a browser decoder could not initialize")
	}
	t.Logf("received %d valid H.264 frames (%d payload bytes) server→spyder→player", frames, videoBytes)
}

// selfDecodableKeyframe reports whether an AVCC access unit carries an SPS
// (NAL type 7), PPS (8), and IDR slice (5) — a keyframe a decoder can start
// from with no out-of-band parameter sets.
func selfDecodableKeyframe(avcc []byte) bool {
	seen := map[byte]bool{}
	for i := 0; i+4 <= len(avcc); {
		l := int(binary.BigEndian.Uint32(avcc[i : i+4]))
		i += 4
		if l <= 0 || i+l > len(avcc) {
			break
		}
		seen[avcc[i]&0x1f] = true
		i += l
	}
	return seen[7] && seen[8] && seen[5]
}
