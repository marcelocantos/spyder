// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package iostunnel

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
)

// lifecycleEnv isolates paths.Base() and gives the supervisor a stub
// registry, so nothing reaches a real tunnel on :60105. The fake `ios`
// binaries live under dir and are killed at cleanup.
func lifecycleEnv(t *testing.T) (dir string, port int) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("SPYDER_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnels", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("[]")) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Cleanup(func() { _ = exec.Command("pkill", "-KILL", "-f", dir).Run() })
	return dir, ln.Addr().(*net.TCPAddr).Port
}

// fakeTunnel writes an `ios` stand-in that records its pid and runs
// until killed. ignoreTERM makes it a tunnel slow to honour SIGTERM.
func fakeTunnel(t *testing.T, dir string, ignoreTERM bool) (bin, pids string) {
	t.Helper()
	pids = filepath.Join(dir, "pids")
	trap := "trap 'exit 0' TERM"
	if ignoreTERM {
		trap = "trap '' TERM"
	}
	bin = filepath.Join(dir, "fake-ios")
	script := "#!/bin/sh\necho $$ >> " + pids + "\n" + trap + "\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, pids
}

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func readPIDs(t *testing.T, path string) []int {
	t.Helper()
	b, _ := os.ReadFile(path)
	var pids []int
	for _, f := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(f); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

func waitFirstPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(readPIDs(t, path)) < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	pids := readPIDs(t, path)
	if len(pids) != 1 {
		t.Fatalf("fake tunnel did not start: %v", pids)
	}
	return pids[0]
}

// The daemon starts the tunnel and then hands the same supervisor to
// health.Supervise, whose first action was Start; Start on a running
// tunnel stops it and spawns another (DH-37).
func TestSuperviseAdoptsAStartedTunnel(t *testing.T) {
	dir, port := lifecycleEnv(t)
	bin, pidsPath := fakeTunnel(t, dir, false)
	ctx, cancel := context.WithCancel(context.Background())
	sup := New(bin, "127.0.0.1", port)
	t.Cleanup(func() { cancel(); _ = sup.Stop(context.Background()) })
	if err := sup.Start(ctx); err != nil {
		t.Fatal(err)
	}
	first := waitFirstPID(t, pidsPath)

	go health.NewSupervisor(health.New()).Supervise(ctx, sup, health.Policy{MaxAttempts: 5, BaseBackoff: 2 * time.Second}, 10*time.Second)
	time.Sleep(1500 * time.Millisecond) // the initial Start, well before the first probe
	if pids := readPIDs(t, pidsPath); len(pids) != 1 || !processAlive(first) {
		t.Fatalf("supervising a running tunnel restarted it: spawned %v, first pid %d alive=%v", pids, first, processAlive(first))
	}
}
