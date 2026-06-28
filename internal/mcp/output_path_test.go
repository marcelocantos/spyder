// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveOutputPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"tilde root", "~", home},
		{"tilde child", "~/shots/a.png", filepath.Join(home, "shots", "a.png")},
		{"absolute", "/tmp/a.png", "/tmp/a.png"},
		{"traversal permitted", "/tmp/x/../a.png", "/tmp/a.png"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveOutputPath(c.in)
			if err != nil {
				t.Fatalf("resolveOutputPath(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("resolveOutputPath(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	// Relative paths resolve against the working directory.
	t.Run("relative becomes absolute", func(t *testing.T) {
		got, err := resolveOutputPath("a.png")
		if err != nil {
			t.Fatalf("resolveOutputPath: %v", err)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("got %q, want an absolute path", got)
		}
	})
}

func TestWriteOutputFile(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "nested", "deeper", "shot.png")
	data := []byte{0x89, 'P', 'N', 'G'}

	if err := writeOutputFile(dst, data); err != nil {
		t.Fatalf("writeOutputFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("file contents = %v, want %v", got, data)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("mode = %o, want 644", perm)
	}
}
