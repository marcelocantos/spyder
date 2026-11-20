// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/paths"
	"github.com/marcelocantos/spyder/internal/reservations"
)

func TestBuild_UnreadableReservationBookFailsClosed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv(paths.DataDirEnv, dir)
	if err := os.Mkdir(filepath.Join(dir, "reservations.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	mux, store, handler, _, _ := Build(Config{Addr: "127.0.0.1:0", Version: "test"})
	if store == nil || store.Authorize("dev", "") == nil {
		t.Error("unreadable reservation book disabled enforcement")
	}
	for _, verb := range []string{"reservations", "reservation_status"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/"+verb, strings.NewReader(`{"device":"dev"}`)))
		if !strings.Contains(w.Body.String(), `"isError":true`) || !strings.Contains(w.Body.String(), "reservations") {
			t.Errorf("%s reported a readable book: %s", verb, w.Body.String())
		}
	}
	snap, ok := handler.Health().Model().Get(health.ID{Kind: health.KindDaemon, Name: "reservations"})
	if !ok || snap.State != health.NeedsAttention {
		t.Fatalf("health = %+v present=%v", snap.State, ok)
	}
}

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
