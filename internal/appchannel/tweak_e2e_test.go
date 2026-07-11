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

// These are the 🎯T91.2 live oracles: a real direct-mode ge app (tiltbuggy,
// built with the app-channel tweak handlers) connects to a spyder app-channel
// listener and its tweak plane round-trips — no legacy broker, no streaming.
//
// Gated on SPYDER_GE_TILTBUGGY (path to the tiltbuggy binary) because they
// launch a real GUI app; they skip cleanly so `go test ./...` stays headless.
// The fixture must declare the demo tweaks physics.grip_scale + camera.zoom
// (sample/tiltbuggy/src/Scene.cpp).

// launchTiltbuggy starts a listener, launches tiltbuggy pointed at it, and
// returns the connected session plus a cleanup that kills the app and stops
// the listener.
func launchTiltbuggy(t *testing.T, bin string) (*Session, func()) {
	t.Helper()
	m := NewManager()
	l, err := m.Start(StartParams{Owner: "t91.2-e2e"})
	if err != nil {
		m.Close()
		t.Fatalf("Start listener: %v", err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "SPYDER_APP_CHANNEL="+fmt.Sprintf("127.0.0.1:%d", l.Port))
	if err := cmd.Start(); err != nil {
		l.Stop()
		m.Close()
		t.Fatalf("launch tiltbuggy: %v", err)
	}
	cleanup := func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		l.Stop()
		m.Close()
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ss := l.Sessions(); len(ss) > 0 {
			return ss[0], cleanup
		}
		time.Sleep(100 * time.Millisecond)
	}
	cleanup()
	t.Fatal("tiltbuggy did not connect to the app-channel within 15s")
	return nil, nil
}

func requireTiltbuggy(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("SPYDER_GE_TILTBUGGY")
	if bin == "" {
		t.Skip("set SPYDER_GE_TILTBUGGY=<path to tiltbuggy> to run the T91.2 e2e")
	}
	return bin
}

// TestTweakEndToEnd_Tiltbuggy: list returns the fixture's tweaks and
// set→get round-trips within one session.
func TestTweakEndToEnd_Tiltbuggy(t *testing.T) {
	bin := requireTiltbuggy(t)
	s, cleanup := launchTiltbuggy(t, bin)
	defer cleanup()
	ctx := context.Background()

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
		// Contract parity with the engine allToJson contract: each entry must carry the same fields
		// allToJson emits (both proxy tweak::allToJson) — name, value,
		// default, and the scale/speed metadata. This is the migration's
		// shape-parity gate: a live-broker differential is impossible (it needs
		// the retiring streaming push), so we assert conformance to the shared
		// allToJson contract that tweak_list must produce.
		for _, field := range []string{"name", "value", "default", "scale", "speed"} {
			if _, ok := tw[field]; !ok {
				t.Errorf("tweak_list entry %v missing required field %q", tw["name"], field)
			}
		}
	}
	if !names["camera.zoom"] || !names["physics.grip_scale"] {
		t.Fatalf("tweak_list missing demo tweaks; got %v", names)
	}

	if _, err := s.Call(ctx, MethodTweakSet,
		map[string]any{"name": "camera.zoom", "value": 2.5}, 10*time.Second); err != nil {
		t.Fatalf("tweak_set: %v", err)
	}
	if v := getTweakValue(t, s, "camera.zoom"); v != 2.5 {
		t.Fatalf("tweak_get camera.zoom = %v, want 2.5", v)
	}
}

// TestTweakPersistsAcrossRestart_Tiltbuggy: a tweak set in one run is
// re-read after the app restarts, proving the value persisted to the app's
// tweak DB (🎯T91.2 acceptance: "persists via the app's tweak DB").
func TestTweakPersistsAcrossRestart_Tiltbuggy(t *testing.T) {
	bin := requireTiltbuggy(t)
	ctx := context.Background()
	const want = 3.75

	// Run 1: set a distinctive value, then kill the app.
	s1, cleanup1 := launchTiltbuggy(t, bin)
	// The tweak DB opens during render-host init (after the app-channel goes
	// live), so give the app a moment to finish starting before setting —
	// otherwise save() runs before the DB exists and the value never persists.
	// (This models the real case: an agent tweaks an already-running app.)
	time.Sleep(3 * time.Second)
	if _, err := s1.Call(ctx, MethodTweakSet,
		map[string]any{"name": "camera.zoom", "value": want}, 10*time.Second); err != nil {
		cleanup1()
		t.Fatalf("tweak_set: %v", err)
	}
	// Small grace for the async save to hit sqlite before we kill the process.
	time.Sleep(500 * time.Millisecond)
	cleanup1()

	// Run 2: a fresh process must load the persisted value. The tweak DB is
	// applied during render-host init (after the app-channel goes live), so
	// poll until the loaded value appears rather than reading once on the
	// handshake (which would observe the pre-load default).
	s2, cleanup2 := launchTiltbuggy(t, bin)
	defer cleanup2()
	deadline := time.Now().Add(6 * time.Second)
	var last float64
	for time.Now().Before(deadline) {
		last = getTweakValue(t, s2, "camera.zoom")
		if last == want {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if last != want {
		t.Fatalf("after restart camera.zoom = %v, want %v (not persisted)", last, want)
	}

	// Reset so the persisted DB doesn't leak the test value into other runs.
	_, _ = s2.Call(ctx, MethodTweakReset,
		map[string]any{"name": "camera.zoom"}, 10*time.Second)
}

func getTweakValue(t *testing.T, s *Session, name string) float64 {
	t.Helper()
	raw, err := s.Call(context.Background(), MethodTweakGet,
		map[string]string{"name": name}, 10*time.Second)
	if err != nil {
		t.Fatalf("tweak_get %s: %v", name, err)
	}
	var got map[string]any
	if err := msgpack.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode tweak_get: %v", err)
	}
	v, ok := toFloat(got["value"])
	if !ok {
		t.Fatalf("tweak_get %s: value %v is not numeric (%T)", name, got["value"], got["value"])
	}
	return v
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
