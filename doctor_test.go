// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIOSBinaryAlongside_FollowsSymlink reproduces the Homebrew install
// layout (flat bin/ symlinks into a versioned Cellar/) and checks that
// iosBinaryAlongside resolves the symlink before computing the
// libexec sibling. Regression for the bug where `spyder doctor` could
// not find the bundled `ios` binary when invoked via the flat brew
// symlink.
func TestIOSBinaryAlongside_FollowsSymlink(t *testing.T) {
	rawTmp := t.TempDir()
	// Resolve /var → /private/var on macOS so subsequent path
	// comparisons match.
	tmp, err := filepath.EvalSymlinks(rawTmp)
	if err != nil {
		t.Fatalf("EvalSymlinks tmpdir: %v", err)
	}

	cellarBin := filepath.Join(tmp, "Cellar", "spyder", "1.0.0", "bin")
	cellarLibexec := filepath.Join(tmp, "Cellar", "spyder", "1.0.0", "libexec", "spyder")
	flatBin := filepath.Join(tmp, "bin")
	for _, dir := range []string{cellarBin, cellarLibexec, flatBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	realExe := filepath.Join(cellarBin, "spyder")
	if err := os.WriteFile(realExe, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatalf("write real exe: %v", err)
	}
	iosBin := filepath.Join(cellarLibexec, "ios")
	if err := os.WriteFile(iosBin, []byte("ios"), 0o755); err != nil {
		t.Fatalf("write ios bin: %v", err)
	}
	symlink := filepath.Join(flatBin, "spyder")
	if err := os.Symlink(realExe, symlink); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got := iosBinaryAlongside(symlink)
	if got != iosBin {
		t.Errorf("iosBinaryAlongside(symlink) = %q; want %q", got, iosBin)
	}
}

// TestIOSBinaryAlongside_DevTree checks the development fallback where
// bin/ios sits next to bin/spyder (no Cellar libexec).
func TestIOSBinaryAlongside_DevTree(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks tmpdir: %v", err)
	}
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	spyderExe := filepath.Join(bin, "spyder")
	if err := os.WriteFile(spyderExe, []byte("x"), 0o755); err != nil {
		t.Fatalf("write spyder: %v", err)
	}
	iosExe := filepath.Join(bin, "ios")
	if err := os.WriteFile(iosExe, []byte("x"), 0o755); err != nil {
		t.Fatalf("write ios: %v", err)
	}

	got := iosBinaryAlongside(spyderExe)
	if got != iosExe {
		t.Errorf("iosBinaryAlongside(dev) = %q; want %q", got, iosExe)
	}
}

// TestIOSBinaryAlongside_Missing returns empty when no candidate
// resolves under either layout.
func TestIOSBinaryAlongside_Missing(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks tmpdir: %v", err)
	}
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	spyderExe := filepath.Join(bin, "spyder")
	if err := os.WriteFile(spyderExe, []byte("x"), 0o755); err != nil {
		t.Fatalf("write spyder: %v", err)
	}

	if got := iosBinaryAlongside(spyderExe); got != "" {
		t.Errorf("iosBinaryAlongside(no-ios) = %q; want \"\"", got)
	}
}

// TestSudoersContent_HasBypassAndNopasswd asserts the generated
// sudoers body carries both the `!authenticate` Defaults directive
// (so PAM/TouchID is bypassed for the daemon's non-tty sudo) and
// the NOPASSWD grant. Without either, `sudo -n` from the wedge
// monitor fails.
func TestSudoersContent_HasBypassAndNopasswd(t *testing.T) {
	got := sudoersContent("marcelo", "/opt/homebrew/bin/spyder-killusbmuxd")
	for _, want := range []string{
		"Defaults!/opt/homebrew/bin/spyder-killusbmuxd !authenticate",
		"marcelo ALL=(root) NOPASSWD: /opt/homebrew/bin/spyder-killusbmuxd",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("sudoers content missing required line %q\n--- generated ---\n%s", want, got)
		}
	}
}

// TestSudoersContent_VisudoAccepts validates the generated content
// against `visudo -c -f`, which parses sudoers syntax without
// requiring root. Catches any future template tweak that produces
// invalid syntax before it lands on a user's machine.
func TestSudoersContent_VisudoAccepts(t *testing.T) {
	if _, err := exec.LookPath("visudo"); err != nil {
		t.Skip("visudo not available; skipping syntax check")
	}
	content := sudoersContent("marcelo", "/opt/homebrew/bin/spyder-killusbmuxd")
	tmp, err := os.CreateTemp("", "sudoers-test-*.tmp")
	if err != nil {
		t.Fatalf("tempfile: %v", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		t.Fatalf("write: %v", err)
	}
	tmp.Close()
	out, err := exec.Command("visudo", "-c", "-f", tmp.Name()).CombinedOutput()
	if err != nil {
		t.Fatalf("visudo rejected generated sudoers:\n  err: %v\n  output: %s\n  content:\n%s",
			err, out, content)
	}
}
