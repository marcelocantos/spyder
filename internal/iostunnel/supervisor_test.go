// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package iostunnel

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSupervisorRestartsUnexpectedExit(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	attemptsPath := filepath.Join(tmp, "attempts")
	scriptPath := filepath.Join(tmp, "fake-ios")
	script := `#!/bin/sh
attempts_file="$FAKE_IOS_ATTEMPTS"
attempts=0
if [ -f "$attempts_file" ]; then
  attempts=$(cat "$attempts_file")
fi
attempts=$((attempts + 1))
echo "$attempts" > "$attempts_file"
if [ "$attempts" -eq 1 ]; then
  exit 42
fi
trap 'exit 0' TERM
while true; do
  sleep 1
done
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ios: %v", err)
	}
	t.Setenv("FAKE_IOS_ATTEMPTS", attemptsPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := New(scriptPath, "", 0)
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		cancel()
		_ = s.Stop(context.Background())
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		attemptsBytes, err := os.ReadFile(attemptsPath)
		if err == nil {
			attempts, err := strconv.Atoi(strings.TrimSpace(string(attemptsBytes)))
			if err != nil {
				t.Fatalf("parse attempts: %v", err)
			}
			if attempts >= 2 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("supervisor did not restart child; attempts file: %q", attemptsBytes)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// Health probe SIGTERMs the daemon after HealthProbeFailureThreshold
// consecutive probe failures, even when the subprocess itself stays
// alive — the field-observed "process up, registry wedged" case
// (🎯T84).
func TestSupervisor_HealthProbeKillsWedgedDaemon(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Stub registry — initially serves `/tunnels` 200 OK, then flips
	// to 500 to simulate a wedge. We listen on a free loopback port
	// instead of httptest's auto-assigned port so the supervisor's
	// healthProbe can hit it via host+port.
	var wedged atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnels", func(w http.ResponseWriter, r *http.Request) {
		if wedged.Load() {
			http.Error(w, "wedged", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("[]"))
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// Fake `ios` binary that counts how many times it's been started
	// (so the test can confirm SIGTERM landed + the restartLoop
	// respawned a fresh process). Traps SIGTERM to exit cleanly so
	// the spawn cost stays low.
	attemptsPath := filepath.Join(tmp, "attempts")
	scriptPath := filepath.Join(tmp, "fake-ios")
	script := fmt.Sprintf(`#!/bin/sh
attempts=0
if [ -f %q ]; then attempts=$(cat %q); fi
attempts=$((attempts + 1))
echo "$attempts" > %q
trap 'exit 0' TERM
while true; do sleep 1; done
`, attemptsPath, attemptsPath, attemptsPath)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ios: %v", err)
	}

	// Shrink the probe cadence so the test runs in milliseconds.
	prevInterval, prevThreshold := HealthProbeInterval, HealthProbeFailureThreshold
	HealthProbeInterval = 20 * time.Millisecond
	HealthProbeFailureThreshold = 2
	t.Cleanup(func() {
		HealthProbeInterval = prevInterval
		HealthProbeFailureThreshold = prevThreshold
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := New(scriptPath, "127.0.0.1", port)
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop(context.Background())

	// Let a few healthy probes pass, then wedge the registry.
	time.Sleep(80 * time.Millisecond)
	wedged.Store(true)

	// 4s deadline: ~80ms probe + 1s restartLoop backoff + spawn ~ <1.5s
	// in the green case; doubling provides slack on a loaded runner.
	deadline := time.Now().Add(4 * time.Second)
	for {
		if data, err := os.ReadFile(attemptsPath); err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			if n >= 2 {
				return // fresh process spawned → kill-then-restart fired
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("health probe did not kill+restart the wedged daemon within 4s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// silenceUnusedHttptest is a defensive guard so the httptest import
// stays referenced if the test above is later refactored to use
// httptest.NewServer instead of a raw http.Server.
var _ = httptest.NewServer
