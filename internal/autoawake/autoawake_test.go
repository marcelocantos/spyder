// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package autoawake

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
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

// --- isTrustError -----------------------------------------------------

func TestIsTrustError(t *testing.T) {
	yes := []string{
		"DvtException: {'BSErrorCodeDescription': 'Security', 'NSLocalizedFailureReason': '...'}",
		"Unable to launch com.foo because it has an invalid code signature",
		"... or its profile has not been explicitly trusted by the user",
	}
	for _, s := range yes {
		if !isTrustError(errFromString(s)) {
			t.Errorf("isTrustError(%q) = false; want true", s[:min(60, len(s))])
		}
	}
	no := []string{
		"DvtException: {'BSErrorCodeDescription': 'Locked', ...}",
		"device not connected: 00008103-…",
		"",
	}
	for _, s := range no {
		if isTrustError(errFromString(s)) {
			t.Errorf("isTrustError(%q) = true; want false", s)
		}
	}
}

// --- summariseErr ------------------------------------------------------

func TestSummariseErr_PrefersDvtException(t *testing.T) {
	input := `dvt launch: exit status 1
2026-04-18 21:52:56 colossus.lan pymobiledevice3.__main__[...] WARNING blah
╭───── Traceback ─────╮
│ some decorative box │
╰─────────────────────╯
DvtException: {'BSErrorCodeDescription': 'Locked', 'NSLocalizedFailureReason': 'foo'}`
	err := errFromString(input)
	got := summariseErr(err)
	if got == "" || got == "dvt launch: exit status 1" {
		t.Errorf("summariseErr should pick the DvtException line; got %q", got)
	}
	if !containsStr(got, "DvtException") {
		t.Errorf("summariseErr output missing DvtException marker: %q", got)
	}
}

func TestSummariseErr_FallsBackToFirstNonDecorative(t *testing.T) {
	input := `dvt launch: exit status 1
╭────╮
│ ok │
╰────╯`
	got := summariseErr(errFromString(input))
	// Shouldn't return empty; first non-decorative line is the first.
	if got != "dvt launch: exit status 1" {
		t.Errorf("summariseErr = %q; want first non-decorative line", got)
	}
}

// --- aliasOf -----------------------------------------------------------

func TestAliasOf_FromInventory(t *testing.T) {
	// Set up a temp HOME with Pippa registered so inventory.AliasFor
	// matches. Use the public New to exercise the production path.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := os.MkdirAll(filepath.Join(tmp, ".spyder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".spyder/inventory.json"),
		[]byte(`[{"alias":"Pippa","platform":"ios","ios_uuid":"00008103-000D39301A6A201E"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(nil) // tunneld nil; aliasOf doesn't use it
	if got := s.aliasOf("00008103-000D39301A6A201E"); got != "Pippa" {
		t.Errorf("aliasOf(Pippa UDID) = %q; want Pippa", got)
	}
}

func TestAliasOf_UnknownShortens(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp) // no inventory file
	s := New(nil)

	if got := s.aliasOf("00008103-000D39301A6A201E"); got != "00008103…" {
		t.Errorf("aliasOf(unknown long) = %q; want 00008103…", got)
	}
	// Shorter than the cutoff: passes through unchanged.
	if got := s.aliasOf("short"); got != "short" {
		t.Errorf("aliasOf(short) = %q; want short", got)
	}
}

// --- findBuiltApp -----------------------------------------------------
//
// findBuiltApp globs DerivedData under $HOME. We can't easily feign a
// full DerivedData layout, but we can override HOME and create the
// expected subtree so the glob matches.

func TestFindBuiltApp_PicksNewestMatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Create two candidate app dirs with different mtimes.
	base := filepath.Join(tmp, "Library/Developer/Xcode/DerivedData")
	old := filepath.Join(base, "KeepAwake-aaaa/Build/Products/Debug-iphoneos/KeepAwake.app")
	fresh := filepath.Join(base, "KeepAwake-bbbb/Build/Products/Debug-iphoneos/KeepAwake.app")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	got, err := findBuiltApp()
	if err != nil {
		t.Fatalf("findBuiltApp err = %v", err)
	}
	// Resolve symlinks because macOS /var is symlinked.
	gotReal, _ := filepath.EvalSymlinks(got)
	freshReal, _ := filepath.EvalSymlinks(fresh)
	if gotReal != freshReal {
		t.Errorf("findBuiltApp = %q; want newest %q", got, fresh)
	}
}

func TestFindBuiltApp_NoMatches(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp) // empty DerivedData

	_, err := findBuiltApp()
	if err == nil {
		t.Error("findBuiltApp with no matches returned nil err; want error")
	}
}

// --- helpers -----------------------------------------------------------

// errFromString returns an error whose Error() returns s. Used to
// feed summariseErr the exact multi-line input we want to test.
type stringError string

func (s stringError) Error() string { return string(s) }

func errFromString(s string) error { return stringError(s) }

// containsStr is a tiny local helper (avoids another import).
func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

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
