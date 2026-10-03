// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package scriptlib

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// TestRecipesMatchRepoMirror protects both the shipped embed and the
// documented scripts/lib copy: neither can be edited without the other.
func TestRecipesMatchRepoMirror(t *testing.T) {
	mirror := os.DirFS(filepath.Join("..", "..", "scripts", "lib"))
	if err := compareRecipeMirrors(bundledFS, "recipes", mirror, "."); err != nil {
		t.Fatal(err)
	}
}

func compareRecipeMirrors(bundled fs.FS, bundledDir string, mirror fs.FS, mirrorDir string) error {
	bundledNames, err := recipeNames(bundled, bundledDir)
	if err != nil {
		return fmt.Errorf("bundled recipes: %w", err)
	}
	mirrorNames, err := recipeNames(mirror, mirrorDir)
	if err != nil {
		return fmt.Errorf("scripts/lib mirror: %w", err)
	}
	for name := range bundledNames {
		if !mirrorNames[name] {
			return fmt.Errorf("scripts/lib missing %s", name)
		}
		shipped, err := fs.ReadFile(bundled, path.Join(bundledDir, name))
		if err != nil {
			return fmt.Errorf("read bundled %s: %w", name, err)
		}
		repo, err := fs.ReadFile(mirror, path.Join(mirrorDir, name))
		if err != nil {
			return fmt.Errorf("read scripts/lib %s: %w", name, err)
		}
		if !bytes.Equal(shipped, repo) {
			return fmt.Errorf("recipe %s differs between bundled recipes and scripts/lib", name)
		}
	}
	for name := range mirrorNames {
		if !bundledNames[name] {
			return fmt.Errorf("bundled recipes missing %s from scripts/lib", name)
		}
	}
	return nil
}

func recipeNames(tree fs.FS, dir string) (map[string]bool, error) {
	entries, err := fs.ReadDir(tree, dir)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".star" {
			return nil, fmt.Errorf("unexpected entry %s in %s", entry.Name(), dir)
		}
		names[entry.Name()] = true
	}
	return names, nil
}

func TestCompareRecipeMirrorsDetectsOneSidedChanges(t *testing.T) {
	base := fstest.MapFS{"recipes/a.star": &fstest.MapFile{Data: []byte("a")}}
	cases := []struct {
		name   string
		mirror fstest.MapFS
		want   string
	}{
		{"equal", fstest.MapFS{"a.star": &fstest.MapFile{Data: []byte("a")}}, ""},
		{"missing mirror", fstest.MapFS{"b.star": &fstest.MapFile{Data: []byte("b")}}, "scripts/lib missing a.star"},
		{"extra mirror", fstest.MapFS{"a.star": &fstest.MapFile{Data: []byte("a")}, "b.star": &fstest.MapFile{Data: []byte("b")}}, "bundled recipes missing b.star"},
		{"edited mirror", fstest.MapFS{"a.star": &fstest.MapFile{Data: []byte("b")}}, "recipe a.star differs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := compareRecipeMirrors(base, "recipes", tc.mirror, ".")
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !bytes.Contains([]byte(err.Error()), []byte(tc.want)) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
		})
	}
	// Reverse the two one-sided edits: the shipped tree can gain a file or
	// change its bytes while the documented mirror stays unchanged.
	for _, tc := range []struct {
		name    string
		bundled fstest.MapFS
		want    string
	}{
		{"extra bundled", fstest.MapFS{"recipes/a.star": &fstest.MapFile{Data: []byte("a")}, "recipes/b.star": &fstest.MapFile{Data: []byte("b")}}, "scripts/lib missing b.star"},
		{"edited bundled", fstest.MapFS{"recipes/a.star": &fstest.MapFile{Data: []byte("b")}}, "recipe a.star differs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := compareRecipeMirrors(tc.bundled, "recipes", cases[0].mirror, ".")
			if err == nil || !bytes.Contains([]byte(err.Error()), []byte(tc.want)) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
		})
	}
}
