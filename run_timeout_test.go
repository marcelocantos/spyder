// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// spyder run --timeout must not release the lease while a grandchild of
// the wrapped command is still running (DH-12).
func TestRunTimeoutDoesNotOrphanWrappedJob(t *testing.T) {
	data := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "worker.pid")
	job := "sleep 30 & echo $! > " + pidFile + "; wait"
	args := []string{"--device", "fake-dev", "--as", "job-A", "--timeout", "1s", "--", "/bin/sh", "-c", job}
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"SPYDER_RESV_HELPER=run",
		"SPYDER_RESV_ARGS="+strings.Join(args, "\x1f"),
		paths.DataDirEnv+"="+data,
		"HOME="+t.TempDir(),
	)
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 30 {
		t.Fatalf("want exit 30 (timeout), got %v", err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	time.Sleep(200 * time.Millisecond)

	store, err := reservations.New(filepath.Join(data, "reservations.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, held, err := store.Get("fake-dev")
	if err != nil {
		t.Fatal(err)
	}
	alive := syscall.Kill(pid, 0) == nil
	if alive && !held {
		t.Errorf("reservation released and spyder exited, but the job's worker (pid %d) is still running", pid)
	}
}
