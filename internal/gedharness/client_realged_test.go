// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package gedharness

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// gedBinary is the runnable reference daemon. The live test spawns it,
// hits the real HTTP surface, then reaps it.
const gedBinary = "/Users/marcelo/work/github.com/squz/bin/ged"

// liveEnvVar gates the live test: unset → t.Skip, so `go test ./...`
// stays green without a ged binary present.
const liveEnvVar = "GEDHARNESS_LIVE"

// TestInfoAgainstRealGed_RealGed spawns real ged on a free port, waits
// for readiness, and asserts /api/info decodes to the empty state.
// Skipped unless GEDHARNESS_LIVE=1 and the binary exists.
func TestInfoAgainstRealGed_RealGed(t *testing.T) {
	if os.Getenv(liveEnvVar) != "1" {
		t.Skipf("set %s=1 to run the live ged test", liveEnvVar)
	}
	if _, err := os.Stat(gedBinary); err != nil {
		t.Skipf("ged binary not found at %s: %v", gedBinary, err)
	}

	port := freePort(t)
	// --no-open: headless; --port: the port we probed as free.
	cmd := exec.Command(gedBinary, "--no-open", "--port", strconv.Itoa(port))
	// Own process group so we can signal the whole tree on teardown.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ged: %v", err)
	}
	t.Cleanup(func() {
		// SIGINT the process group; ged handles it for a clean exit.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
		}
		_ = cmd.Wait()
	})

	base := "http://localhost:" + strconv.Itoa(port)
	client := NewClient(base)
	waitForReady(t, client)

	raw, err := client.Info(context.Background())
	if err != nil {
		t.Fatalf("Info against real ged: %v", err)
	}
	var info struct {
		Connected bool  `json:"connected"`
		Servers   []any `json:"servers"`
		Sessions  int   `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode real ged info: %v (raw=%s)", err, raw)
	}
	// No app is connected, so ged reports the empty state.
	if info.Connected {
		t.Errorf("expected connected=false, got true")
	}
	if len(info.Servers) != 0 {
		t.Errorf("expected no servers, got %v", info.Servers)
	}
	if info.Sessions != 0 {
		t.Errorf("expected 0 sessions, got %d", info.Sessions)
	}
	t.Logf("real ged /api/info: %s", raw)
}

// freePort asks the OS for an unused TCP port and returns it. ged binds
// this port next; the tiny race between close and re-bind is acceptable
// for a local test.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitForReady polls /api/info until ged answers or a deadline passes.
func waitForReady(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		_, err := c.Info(ctx)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("ged did not become ready within deadline")
}
