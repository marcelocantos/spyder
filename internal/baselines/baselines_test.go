// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package baselines_test

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/marcelocantos/spyder/internal/baselines"
)

func newStore(t *testing.T) *baselines.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := baselines.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestPutGet_PNGOnly(t *testing.T) {
	s := newStore(t)
	png := []byte{0x89, 0x50, 0x4e, 0x47} // PNG magic
	if err := s.Put("login", "main-screen", "default", png, nil); err != nil {
		t.Fatalf("Put: %v", err)
	}
	b, err := s.Get("login", "main-screen", "default")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(b.PNG) != string(png) {
		t.Fatalf("PNG mismatch")
	}
	if len(b.Manifest.Elements) != 0 {
		t.Fatalf("expected empty manifest, got %d elements", len(b.Manifest.Elements))
	}
}

func TestPutGet_WithManifest(t *testing.T) {
	s := newStore(t)
	png := []byte("fakepng")
	m := &baselines.Manifest{
		SchemaVersion: 1,
		Elements: []baselines.Element{
			{ID: "app/screen/btn", Kind: "button", BBox: [4]int{10, 20, 100, 40}},
		},
	}
	if err := s.Put("suite", "case1", "pippa-portrait", png, m); err != nil {
		t.Fatalf("Put: %v", err)
	}
	b, err := s.Get("suite", "case1", "pippa-portrait")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if b.Manifest.SchemaVersion != 1 {
		t.Fatalf("schema_version: got %d, want 1", b.Manifest.SchemaVersion)
	}
	if len(b.Manifest.Elements) != 1 {
		t.Fatalf("elements: got %d, want 1", len(b.Manifest.Elements))
	}
	el := b.Manifest.Elements[0]
	if el.ID != "app/screen/btn" {
		t.Fatalf("element id: got %q", el.ID)
	}
}

func TestGet_NotExist(t *testing.T) {
	s := newStore(t)
	_, err := s.Get("suite", "case", "default")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestPut_AtomicOverwrite(t *testing.T) {
	s := newStore(t)
	png1 := []byte("version1")
	png2 := []byte("version2")
	if err := s.Put("s", "c", "v", png1, nil); err != nil {
		t.Fatalf("Put1: %v", err)
	}
	if err := s.Put("s", "c", "v", png2, nil); err != nil {
		t.Fatalf("Put2: %v", err)
	}
	b, _ := s.Get("s", "c", "v")
	if string(b.PNG) != "version2" {
		t.Fatalf("expected version2, got %q", b.PNG)
	}
}

func TestList(t *testing.T) {
	s := newStore(t)
	pngs := map[string]string{
		"c1": "pippa-portrait",
		"c2": "pippa-portrait",
		"c3": "pippa-landscape",
	}
	for c, v := range pngs {
		if err := s.Put("mysuite", c, v, []byte("png"), nil); err != nil {
			t.Fatalf("Put %s/%s: %v", c, v, err)
		}
	}
	entries, err := s.List("mysuite")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if !e.HasPNG {
			t.Errorf("entry %+v missing PNG", e)
		}
	}
}

func TestList_EmptySuite(t *testing.T) {
	s := newStore(t)
	entries, err := s.List("nonexistent")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestValidation_EmptyFields(t *testing.T) {
	s := newStore(t)
	if err := s.Put("", "case", "variant", []byte("p"), nil); err == nil {
		t.Fatal("expected error for empty suite")
	}
	if err := s.Put("suite", "", "variant", []byte("p"), nil); err == nil {
		t.Fatal("expected error for empty case")
	}
	if err := s.Put("suite", "case", "", []byte("p"), nil); err == nil {
		t.Fatal("expected error for empty variant")
	}
	if err := s.Put("suite", "case", "variant", nil, nil); err == nil {
		t.Fatal("expected error for empty PNG")
	}
}

func TestValidation_PathTraversal(t *testing.T) {
	s := newStore(t)
	if err := s.Put("../evil", "case", "variant", []byte("p"), nil); err == nil {
		t.Fatal("expected error for path traversal in suite")
	}
}

func TestNew_EmptyBaseDir(t *testing.T) {
	_, err := baselines.New("")
	if err == nil {
		t.Fatal("expected error for empty baseDir")
	}
}

func TestBaseDir(t *testing.T) {
	dir := t.TempDir()
	s, _ := baselines.New(dir)
	if s.BaseDir() != dir {
		t.Fatalf("BaseDir: got %q, want %q", s.BaseDir(), dir)
	}
}

// TestPut_CreatesDirs verifies the store creates missing directories on Put.
func TestPut_CreatesDirs(t *testing.T) {
	dir := t.TempDir()
	s, _ := baselines.New(filepath.Join(dir, "subdir"))
	if err := s.Put("s", "c", "v", []byte("png"), nil); err != nil {
		t.Fatalf("Put into new dir: %v", err)
	}
}
