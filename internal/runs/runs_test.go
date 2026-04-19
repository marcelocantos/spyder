// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T, now *time.Time, opts ...Option) *Store {
	t.Helper()
	dir := t.TempDir()
	all := append([]Option{WithNow(func() time.Time { return *now })}, opts...)
	s, err := New(dir, all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestOpen_CreatesDirAndManifest(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 30, 22, 0, time.UTC)
	s := newTestStore(t, &now)

	r, err := s.Open("Pippa", "tiltbuggy", "ui regression")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !strings.HasPrefix(r.ID, "20260419-143022-") {
		t.Errorf("run id %q does not carry timestamp prefix", r.ID)
	}
	if r.Device != "Pippa" || r.Owner != "tiltbuggy" || r.Note != "ui regression" {
		t.Errorf("unexpected run: %+v", r)
	}
	if !r.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v; want %v", r.CreatedAt, now)
	}
	if r.ClosedAt != nil {
		t.Errorf("ClosedAt should be nil on Open: %v", r.ClosedAt)
	}

	// Manifest on disk matches.
	manifest := filepath.Join(s.BaseDir(), r.ID, "manifest.json")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
	var disk Run
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatalf("manifest json: %v", err)
	}
	if disk.ID != r.ID {
		t.Errorf("manifest ID = %q; want %q", disk.ID, r.ID)
	}
}

func TestOpen_RequiresDeviceAndOwner(t *testing.T) {
	now := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, &now)
	if _, err := s.Open("", "owner", ""); err == nil {
		t.Error("Open with empty device should fail")
	}
	if _, err := s.Open("dev", "", ""); err == nil {
		t.Error("Open with empty owner should fail")
	}
}

func TestClose_StampsManifest(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	s := newTestStore(t, &now)
	r, _ := s.Open("Pippa", "tiltbuggy", "")

	now = now.Add(5 * time.Minute)
	if err := s.Close(r.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := s.Get(r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ClosedAt == nil || !got.ClosedAt.Equal(now) {
		t.Errorf("ClosedAt = %v; want %v", got.ClosedAt, now)
	}

	// Idempotent close — no error, ClosedAt stays as first-close value.
	now = now.Add(1 * time.Minute)
	if err := s.Close(r.ID); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	got2, _ := s.Get(r.ID)
	if !got2.ClosedAt.Equal(*got.ClosedAt) {
		t.Errorf("second Close should be idempotent; ClosedAt shifted %v -> %v",
			got.ClosedAt, got2.ClosedAt)
	}
}

func TestActive_MatchesDeviceAndOwner(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	s := newTestStore(t, &now)

	r1, _ := s.Open("Pippa", "tiltbuggy", "")
	now = now.Add(10 * time.Second)
	r2, _ := s.Open("Pippa", "otherproj", "")

	got, err := s.Active("Pippa", "tiltbuggy")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if got == nil || got.ID != r1.ID {
		t.Errorf("Active(Pippa, tiltbuggy) = %+v; want %q", got, r1.ID)
	}

	// After closing r1, only r2 remains active for Pippa.
	_ = s.Close(r1.ID)
	got, _ = s.Active("Pippa", "tiltbuggy")
	if got != nil {
		t.Errorf("Active after Close should be nil; got %+v", got)
	}
	got, _ = s.Active("Pippa", "otherproj")
	if got == nil || got.ID != r2.ID {
		t.Errorf("Active(Pippa, otherproj) = %+v; want %q", got, r2.ID)
	}

	// Unknown (device, owner) → nil with nil error.
	got, err = s.Active("Phantom", "nobody")
	if err != nil {
		t.Errorf("Active err = %v; want nil", err)
	}
	if got != nil {
		t.Errorf("unknown active should be nil; got %+v", got)
	}
}

func TestAddArtefact_PersistsAndUpdatesManifest(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	s := newTestStore(t, &now)
	r, _ := s.Open("Pippa", "tiltbuggy", "")

	png := []byte("\x89PNG\r\n\x1a\nfake")
	a, err := s.AddArtefact(r.ID, "screenshot", "shot-1.png", "image/png", png)
	if err != nil {
		t.Fatalf("AddArtefact: %v", err)
	}
	if a.Name != "shot-1.png" || a.Size != int64(len(png)) || a.Source != "screenshot" {
		t.Errorf("unexpected artefact: %+v", a)
	}

	// File exists on disk.
	data, err := os.ReadFile(filepath.Join(s.BaseDir(), r.ID, "shot-1.png"))
	if err != nil {
		t.Fatalf("artefact file: %v", err)
	}
	if string(data) != string(png) {
		t.Errorf("artefact bytes mismatch")
	}

	// Manifest now carries the record.
	got, _ := s.Get(r.ID)
	if len(got.Artefacts) != 1 || got.Artefacts[0].Name != "shot-1.png" {
		t.Errorf("manifest artefacts = %+v", got.Artefacts)
	}
}

func TestAddArtefact_RejectsReservedManifestName(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	s := newTestStore(t, &now)
	r, _ := s.Open("Pippa", "tiltbuggy", "")

	if _, err := s.AddArtefact(r.ID, "screenshot", "manifest.json", "", []byte("x")); err == nil {
		t.Error("AddArtefact(name=manifest.json) should be rejected")
	}
}

func TestAddArtefact_RejectsPathTraversal(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	s := newTestStore(t, &now)
	r, _ := s.Open("Pippa", "tiltbuggy", "")

	// filepath.Base strips traversal segments; the bytes must land in
	// the run dir.
	a, err := s.AddArtefact(r.ID, "screenshot", "../escaped.png", "image/png", []byte("x"))
	if err != nil {
		t.Fatalf("AddArtefact with traversal input: %v", err)
	}
	if strings.Contains(a.Name, "..") {
		t.Errorf("artefact name %q still contains traversal segment", a.Name)
	}
	if _, err := os.Stat(filepath.Join(s.BaseDir(), r.ID, a.Name)); err != nil {
		t.Errorf("artefact file missing after traversal sanitisation: %v", err)
	}
}

func TestAddArtefact_ClosedRunRejected(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	s := newTestStore(t, &now)
	r, _ := s.Open("Pippa", "tiltbuggy", "")
	_ = s.Close(r.ID)
	if _, err := s.AddArtefact(r.ID, "screenshot", "shot.png", "", []byte("x")); err == nil {
		t.Error("AddArtefact on closed run should fail")
	}
}

func TestListGet_NewestFirst(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	s := newTestStore(t, &now)

	r1, _ := s.Open("A", "o", "")
	now = now.Add(1 * time.Minute)
	r2, _ := s.Open("B", "o", "")
	now = now.Add(1 * time.Minute)
	r3, _ := s.Open("C", "o", "")

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d; want 3", len(got))
	}
	// Newest first.
	if got[0].ID != r3.ID || got[1].ID != r2.ID || got[2].ID != r1.ID {
		t.Errorf("List order = %q/%q/%q; want %q/%q/%q",
			got[0].ID, got[1].ID, got[2].ID, r3.ID, r2.ID, r1.ID)
	}

	one, err := s.Get(r2.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if one.ID != r2.ID {
		t.Errorf("Get ID = %q; want %q", one.ID, r2.ID)
	}
}

func TestGet_InvalidIDRejected(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	s := newTestStore(t, &now)
	for _, bad := range []string{"", "..", ".", "foo/bar", `win\path`} {
		if _, err := s.Get(bad); err == nil {
			t.Errorf("Get(%q) should fail", bad)
		}
	}
}

func TestList_EmptyBaseDir(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "not-yet-created"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := s.List()
	if err != nil {
		t.Fatalf("List on missing dir: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List on missing dir = %v; want empty", got)
	}
}

func TestPrune_MaxAgeRemovesOldClosedRuns(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	s := newTestStore(t, &now, WithPolicy(Policy{MaxAge: 24 * time.Hour}))

	// Old run: created 3 days ago, closed same time.
	now = now.Add(-72 * time.Hour)
	old, _ := s.Open("A", "o", "")
	_ = s.Close(old.ID)

	// Fresh run: just now.
	now = time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	fresh, _ := s.Open("B", "o", "")
	_ = s.Close(fresh.ID)

	res, err := s.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != old.ID {
		t.Errorf("removed = %v; want [%s]", res.Removed, old.ID)
	}
	if res.Retained != 1 {
		t.Errorf("retained = %d; want 1", res.Retained)
	}

	// Old run dir deleted; fresh dir intact.
	if _, err := os.Stat(filepath.Join(s.BaseDir(), old.ID)); !os.IsNotExist(err) {
		t.Errorf("old run dir still present: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(s.BaseDir(), fresh.ID)); err != nil {
		t.Errorf("fresh run dir missing: %v", err)
	}
}

func TestPrune_MaxAgePreservesOpenRuns(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	s := newTestStore(t, &now, WithPolicy(Policy{MaxAge: 24 * time.Hour}))

	// Open but ancient run — still in progress; must not be pruned.
	now = now.Add(-72 * time.Hour)
	r, _ := s.Open("A", "o", "")

	now = time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	res, _ := s.Prune()
	if len(res.Removed) != 0 {
		t.Errorf("open run was pruned; removed=%v", res.Removed)
	}
	if _, err := os.Stat(filepath.Join(s.BaseDir(), r.ID)); err != nil {
		t.Errorf("open run dir was deleted: %v", err)
	}
}

func TestPrune_MaxSizeDropsOldestUntilUnderCap(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	// 100-byte cap; each run holds ~40 bytes → max 2 survive.
	s := newTestStore(t, &now, WithPolicy(Policy{MaxSize: 100}))

	payload := make([]byte, 40)

	var ids []string
	for i := range 4 {
		r, err := s.Open("D", "o", "")
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if _, err := s.AddArtefact(r.ID, "screenshot", "shot.bin", "", payload); err != nil {
			t.Fatalf("AddArtefact %d: %v", i, err)
		}
		if err := s.Close(r.ID); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
		ids = append(ids, r.ID)
		now = now.Add(1 * time.Minute)
	}

	res, err := s.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// The two oldest should be gone.
	if len(res.Removed) != 2 {
		t.Errorf("removed count = %d; want 2 (got %v)", len(res.Removed), res.Removed)
	}
	remaining, _ := s.List()
	if len(remaining) != 2 {
		t.Errorf("remaining = %d; want 2", len(remaining))
	}
	// Two newest should survive.
	wantKeep := map[string]bool{ids[2]: true, ids[3]: true}
	for _, r := range remaining {
		if !wantKeep[r.ID] {
			t.Errorf("survivor %q isn't one of the two newest", r.ID)
		}
	}
}

func TestPrune_NoPolicyIsNoOp(t *testing.T) {
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	s := newTestStore(t, &now) // no policy
	r, _ := s.Open("A", "o", "")
	_ = s.Close(r.ID)

	res, err := s.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("zero-policy prune removed %v", res.Removed)
	}
}

func TestCrossStore_ReadsForeignRuns(t *testing.T) {
	// Simulates two processes sharing the same base dir: one writes,
	// another reads.
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	writer, err := New(dir, WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("writer New: %v", err)
	}
	r, err := writer.Open("Pippa", "tiltbuggy", "")
	if err != nil {
		t.Fatalf("writer Open: %v", err)
	}

	reader, err := New(dir, WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("reader New: %v", err)
	}
	got, err := reader.Active("Pippa", "tiltbuggy")
	if err != nil {
		t.Fatalf("reader Active: %v", err)
	}
	if got == nil || got.ID != r.ID {
		t.Errorf("reader.Active = %+v; want id=%q", got, r.ID)
	}
}
