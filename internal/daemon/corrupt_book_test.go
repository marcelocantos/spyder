// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/reservations"
)

func TestBuild_CorruptReservationBookStaysEnforcing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(paths.DataDirEnv, dir)
	path := filepath.Join(dir, "reservations.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, store, handler, _, _ := Build(Config{Addr: "127.0.0.1:0", Version: "test"})
	if store == nil {
		t.Fatal("corrupt book disabled reservations")
	}
	if _, err := store.Acquire("dev", "job-A", time.Minute, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acquire("dev", "job-B", time.Minute, ""); !reservations.IsConflict(err) {
		t.Fatalf("enforcement off after a corrupt book: %v", err)
	}
	snap, ok := handler.Health().Model().Get(health.ID{Kind: health.KindDaemon, Name: "reservations"})
	if !ok || snap.State != health.NeedsAttention {
		t.Fatalf("health = %+v present=%v", snap.State, ok)
	}
	matches, err := filepath.Glob(path + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("quarantine files = %v", matches)
	}
}
