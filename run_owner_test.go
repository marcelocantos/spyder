// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// Two spyder run processes started in the same directory must not share
// a lease. Without --as the owner used to be the directory base name,
// and Acquire by that owner renews instead of conflicting (DH-10).
func TestTwoRunsFromSameDirDoNotShareTheLease(t *testing.T) {
	data := t.TempDir()
	work := filepath.Join(t.TempDir(), "game")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	start := func(job string) *exec.Cmd {
		t.Helper()
		args := []string{"--device", "fake-dev", "--", "/bin/sh", "-c", job}
		cmd := exec.Command(os.Args[0])
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"SPYDER_RESV_HELPER=run",
			"SPYDER_RESV_ARGS="+strings.Join(args, "\x1f"),
			paths.DataDirEnv+"="+data,
			"HOME="+t.TempDir(),
		)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	first := start("sleep 30")
	t.Cleanup(func() {
		_ = first.Process.Kill()
		_, _ = first.Process.Wait()
	})
	book := filepath.Join(data, "reservations.json")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(book); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first run did not write a reservation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	second := start("true")
	err := second.Wait()
	if err == nil {
		t.Fatal("a second spyder run from the same directory got the device the first was still using")
	}
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
		t.Fatalf("second run exit = %v, want 3 (reservation conflict)", err)
	}
}

// A lease whose owner is the directory base name, which is what the
// installed spyder run writes, still loads and still conflicts with a
// different owner.
func TestOldDirectoryOwnerLeaseStillConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reservations.json")
	body := `[
  {
    "device": "fake-dev",
    "owner": "game",
    "expires_at": "2027-01-15T09:00:00Z",
    "note": "spyder run: make test",
    "created_at": "2027-01-15T08:00:00Z"
  }
]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := reservations.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acquire("fake-dev", "game:1@host", time.Hour, ""); !reservations.IsConflict(err) {
		t.Fatalf("new owner acquired over the installed lease: %v", err)
	}
	if _, err := store.Acquire("fake-dev", "game", time.Hour, "renewed"); err != nil {
		t.Fatalf("the owner named in the installed lease could not renew: %v", err)
	}
}
