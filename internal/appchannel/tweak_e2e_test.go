// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// TestTweakEndToEnd_Tiltbuggy is the 🎯T91.2 live oracle: a real direct-mode
// ge app (tiltbuggy, built with the app-channel tweak handlers) connects to a
// spyder app-channel listener, and tweak_list/get/set round-trip at parity
// with ged's tweak plane — all without ged.
//
// Gated on SPYDER_GE_TILTBUGGY (path to the tiltbuggy binary) because it
// launches a real GUI app; skips cleanly so `go test ./...` stays headless.
// The fixture must declare the demo tweaks physics.grip_scale + camera.zoom
// (sample/tiltbuggy/src/Scene.cpp).
func TestTweakEndToEnd_Tiltbuggy(t *testing.T) {
	bin := os.Getenv("SPYDER_GE_TILTBUGGY")
	if bin == "" {
		t.Skip("set SPYDER_GE_TILTBUGGY=<path to tiltbuggy> to run the T91.2 e2e")
	}

	m := NewManager()
	t.Cleanup(m.Close)
	l, err := m.Start(StartParams{Owner: "t91.2-e2e"})
	if err != nil {
		t.Fatalf("Start listener: %v", err)
	}
	t.Cleanup(l.Stop)

	addr := fmt.Sprintf("127.0.0.1:%d", l.Port)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "SPYDER_APP_CHANNEL="+addr)
	if err := cmd.Start(); err != nil {
		t.Fatalf("launch tiltbuggy: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// Wait for the app to dial in.
	var s *Session
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ss := l.Sessions(); len(ss) > 0 {
			s = ss[0]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if s == nil {
		t.Fatal("tiltbuggy did not connect to the app-channel within 15s")
	}
	ctx := context.Background()

	// tweak_list — the fixture's declared tweaks must be present.
	raw, err := s.Call(ctx, MethodTweakList, nil, 10*time.Second)
	if err != nil {
		t.Fatalf("tweak_list: %v", err)
	}
	var list []map[string]any
	if err := msgpack.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode tweak_list: %v", err)
	}
	names := map[string]bool{}
	for _, tw := range list {
		if n, ok := tw["name"].(string); ok {
			names[n] = true
		}
	}
	if !names["camera.zoom"] || !names["physics.grip_scale"] {
		t.Fatalf("tweak_list missing demo tweaks; got %v", names)
	}

	// tweak_set → tweak_get round-trip.
	if _, err := s.Call(ctx, MethodTweakSet,
		map[string]any{"name": "camera.zoom", "value": 2.5}, 10*time.Second); err != nil {
		t.Fatalf("tweak_set: %v", err)
	}
	graw, err := s.Call(ctx, MethodTweakGet,
		map[string]string{"name": "camera.zoom"}, 10*time.Second)
	if err != nil {
		t.Fatalf("tweak_get: %v", err)
	}
	var got map[string]any
	if err := msgpack.Unmarshal(graw, &got); err != nil {
		t.Fatalf("decode tweak_get: %v", err)
	}
	if v, ok := toFloat(got["value"]); !ok || v != 2.5 {
		t.Fatalf("tweak_get camera.zoom = %v (%T), want 2.5", got["value"], got["value"])
	}
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}
