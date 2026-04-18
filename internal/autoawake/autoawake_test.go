// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package autoawake

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// --- newDevices seen-set logic ----------------------------------------

func TestNewDevices_FirstSeenAllFresh(t *testing.T) {
	seen := map[string]bool{}
	fresh := newDevices([]string{"a", "b", "c"}, seen)
	sort.Strings(fresh)
	want := []string{"a", "b", "c"}
	if !equalStrings(fresh, want) {
		t.Errorf("fresh = %v; want %v", fresh, want)
	}
	for _, u := range want {
		if !seen[u] {
			t.Errorf("%q not marked seen", u)
		}
	}
}

func TestNewDevices_SameSetNoneFresh(t *testing.T) {
	seen := map[string]bool{"a": true, "b": true}
	fresh := newDevices([]string{"a", "b"}, seen)
	if len(fresh) != 0 {
		t.Errorf("expected no fresh; got %v", fresh)
	}
}

func TestNewDevices_DisappearedDevicesPruned(t *testing.T) {
	seen := map[string]bool{"a": true, "b": true}
	_ = newDevices([]string{"a"}, seen) // b disappeared
	if seen["b"] {
		t.Errorf("expected b pruned from seen; got %v", seen)
	}
	if !seen["a"] {
		t.Errorf("expected a kept; got %v", seen)
	}
}

func TestNewDevices_ReplugRetriggers(t *testing.T) {
	seen := map[string]bool{}
	_ = newDevices([]string{"a"}, seen)      // first seen
	_ = newDevices([]string{}, seen)         // unplugged, pruned
	fresh := newDevices([]string{"a"}, seen) // replugged
	if len(fresh) != 1 || fresh[0] != "a" {
		t.Errorf("expected a refreshed; got %v", fresh)
	}
}

func TestNewDevices_MixedAddAndRemove(t *testing.T) {
	seen := map[string]bool{"a": true}
	fresh := newDevices([]string{"b", "c"}, seen) // a gone; b, c new
	sort.Strings(fresh)
	if !equalStrings(fresh, []string{"b", "c"}) {
		t.Errorf("fresh = %v; want [b c]", fresh)
	}
	if seen["a"] {
		t.Errorf("a should be pruned")
	}
}

// --- findKeepAwakeProject ---------------------------------------------

func TestFindKeepAwakeProject_FromEnvVar(t *testing.T) {
	tmp := t.TempDir()
	// Create a project.yml in the temp dir.
	if err := os.WriteFile(filepath.Join(tmp, "project.yml"), []byte("name: KeepAwake\n"), 0o600); err != nil {
		t.Fatalf("write project.yml: %v", err)
	}
	t.Setenv("SPYDER_KEEPAWAKE_PROJECT", tmp)
	got := findKeepAwakeProject()
	if got != tmp {
		t.Errorf("findKeepAwakeProject via env = %q; want %q", got, tmp)
	}
}

func TestFindKeepAwakeProject_EnvVarMissingFile(t *testing.T) {
	tmp := t.TempDir() // empty
	t.Setenv("SPYDER_KEEPAWAKE_PROJECT", tmp)
	// The env-var points at a dir without project.yml — skip and try
	// cwd-walk. We'll set cwd to a dir that also doesn't have one, so
	// the walk returns "".
	cwdDir := t.TempDir()
	chdir(t, cwdDir)
	got := findKeepAwakeProject()
	if got != "" {
		t.Errorf("findKeepAwakeProject should return empty; got %q", got)
	}
}

func TestFindKeepAwakeProject_WalkUp(t *testing.T) {
	t.Setenv("SPYDER_KEEPAWAKE_PROJECT", "")
	// Simulate a repo layout: <tmp>/ios/KeepAwake/project.yml. cwd
	// deeper so the walk-up has to traverse multiple levels.
	tmp := t.TempDir()
	projectDir := filepath.Join(tmp, "ios", "KeepAwake")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "project.yml"), []byte{}, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	deep := filepath.Join(tmp, "internal", "pkg", "sub")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir deep: %v", err)
	}
	chdir(t, deep)
	got := findKeepAwakeProject()
	wantResolved, _ := filepath.EvalSymlinks(projectDir)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("findKeepAwakeProject walkup = %q; want %q", got, projectDir)
	}
}

// --- helpers -----------------------------------------------------------

// chdir changes into dir for the duration of the test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
