// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package autoawake

import (
	"context"
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
	s := New(nil) // bridge nil; aliasOf doesn't use it
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

// --- summariseErr -------------------------------------------

func TestSummariseErr_FirstLine(t *testing.T) {
	input := "first line\nsecond line\nthird"
	got := summariseErr(errFromString(input))
	if got != "first line" {
		t.Errorf("summariseErr = %q; want first line", got)
	}
}

func TestSummariseErr_SingleLine(t *testing.T) {
	got := summariseErr(errFromString("just one line"))
	if got != "just one line" {
		t.Errorf("summariseErr = %q; want 'just one line'", got)
	}
}

// --- nil bridge guard ------------------------------------------------

func TestSupervisorNilBridge_RunExitsImmediately(t *testing.T) {
	// Supervisor with nil bridge should log and return without panicking.
	// We can't block on Run() in a unit test, so test the guard via the
	// exported Run signature (it returns when ctx is done or bridge is nil).
	s := New(nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(cancelledContext(t))
	}()
	select {
	case <-done:
	case <-timeoutCh(2000):
		t.Error("Run with nil bridge did not return within 2s")
	}
}

// --- helpers -----------------------------------------------------------

// errFromString returns an error whose Error() returns s.
type stringError string

func (s stringError) Error() string { return string(s) }

func errFromString(s string) error { return stringError(s) }

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

// cancelledContext returns a context that is already cancelled.
func cancelledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// timeoutCh returns a channel that receives after ms milliseconds.
func timeoutCh(ms int) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		time.Sleep(time.Duration(ms) * time.Millisecond)
		close(ch)
	}()
	return ch
}
