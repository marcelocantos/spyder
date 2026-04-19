// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package reservations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixedClock returns a now() closure whose value advances only when
// the returned *time.Time is mutated by the caller.
func fixedClock(t0 *time.Time) func() time.Time {
	return func() time.Time { return *t0 }
}

func newTestStore(t *testing.T, path string, now *time.Time) *Store {
	t.Helper()
	s, err := New(path, WithNow(fixedClock(now)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestAcquire_Free(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)
	r, err := s.Acquire("Pippa", "tiltbuggy", 0, "testing")
	if err != nil {
		t.Fatalf("Acquire err = %v", err)
	}
	if r.Owner != "tiltbuggy" || r.Device != "Pippa" {
		t.Errorf("unexpected reservation: %+v", r)
	}
	wantExpiry := now.Add(DefaultTTL)
	if !r.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("ExpiresAt = %v; want %v", r.ExpiresAt, wantExpiry)
	}
}

func TestAcquire_ConflictWithDifferentOwner(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 0, "")
	_, err := s.Acquire("Pippa", "otherproj", 0, "")
	if err == nil {
		t.Fatal("expected Conflict; got nil")
	}
	if !IsConflict(err) {
		t.Errorf("expected Conflict; got %v", err)
	}
}

func TestAcquire_SameOwnerUpgradesExisting(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 10*time.Minute, "initial")
	r, err := s.Acquire("Pippa", "tiltbuggy", 60*time.Minute, "extended run")
	if err != nil {
		t.Fatalf("same-owner Acquire should renew: %v", err)
	}
	wantExpiry := now.Add(60 * time.Minute)
	if !r.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("renewed ExpiresAt = %v; want %v", r.ExpiresAt, wantExpiry)
	}
	if r.Note != "extended run" {
		t.Errorf("note not upgraded: %q", r.Note)
	}
}

func TestAcquire_ExpiredSlotReusable(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 5*time.Minute, "")
	now = now.Add(10 * time.Minute) // past the 5-min TTL
	// A different owner can now acquire, no Conflict.
	_, err := s.Acquire("Pippa", "otherproj", 0, "")
	if err != nil {
		t.Fatalf("expired slot should be reusable; got %v", err)
	}
}

func TestRelease_OnlyOwnerCanRelease(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 0, "")

	if err := s.Release("Pippa", "otherproj"); !IsConflict(err) {
		t.Errorf("Release by non-owner: got %v; want Conflict", err)
	}
	// Hold still active.
	if _, ok := s.Get("Pippa"); !ok {
		t.Error("reservation was cleared by non-owner release")
	}
	if err := s.Release("Pippa", "tiltbuggy"); err != nil {
		t.Errorf("owner Release err = %v", err)
	}
	if _, ok := s.Get("Pippa"); ok {
		t.Error("reservation still present after owner release")
	}
}

func TestRelease_FreeDeviceIsNoOp(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)
	if err := s.Release("NotReserved", "anyone"); err != nil {
		t.Errorf("Release on free device should be no-op; got %v", err)
	}
}

func TestRelease_ExpiredIsNoOp(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 5*time.Minute, "")
	now = now.Add(10 * time.Minute)
	// Non-owner can release an expired reservation cleanly.
	if err := s.Release("Pippa", "otherproj"); err != nil {
		t.Errorf("Release on expired: got %v; want nil", err)
	}
}

func TestRenew(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 10*time.Minute, "")
	now = now.Add(5 * time.Minute)
	r, err := s.Renew("Pippa", "tiltbuggy", 30*time.Minute)
	if err != nil {
		t.Fatalf("Renew err = %v", err)
	}
	wantExpiry := now.Add(30 * time.Minute)
	if !r.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("ExpiresAt = %v; want %v", r.ExpiresAt, wantExpiry)
	}
}

func TestRenew_NonOwnerConflict(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 0, "")
	_, err := s.Renew("Pippa", "otherproj", 0)
	if !IsConflict(err) {
		t.Errorf("Renew by non-owner: got %v; want Conflict", err)
	}
}

func TestRenew_ExpiredReturnsError(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)
	_, _ = s.Acquire("Pippa", "tiltbuggy", 5*time.Minute, "")
	now = now.Add(10 * time.Minute)
	_, err := s.Renew("Pippa", "tiltbuggy", 30*time.Minute)
	if err == nil {
		t.Fatal("Renew on expired reservation should fail")
	}
	if IsConflict(err) {
		t.Error("expired-renew error shouldn't be a Conflict")
	}
}

func TestAuthorize(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)

	// Free device: any owner (including empty) authorized.
	if err := s.Authorize("Pippa", ""); err != nil {
		t.Errorf("free Authorize should be nil; got %v", err)
	}
	if err := s.Authorize("Pippa", "anybody"); err != nil {
		t.Errorf("free Authorize should be nil; got %v", err)
	}

	_, _ = s.Acquire("Pippa", "tiltbuggy", 0, "")

	if err := s.Authorize("Pippa", "tiltbuggy"); err != nil {
		t.Errorf("holder Authorize err = %v", err)
	}
	if err := s.Authorize("Pippa", "otherproj"); !IsConflict(err) {
		t.Errorf("non-holder Authorize: got %v; want Conflict", err)
	}
	if err := s.Authorize("Pippa", ""); !IsConflict(err) {
		t.Errorf("anonymous Authorize on held device: got %v; want Conflict", err)
	}
}

func TestTTLBounds(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)

	// Zero TTL → default.
	r, _ := s.Acquire("A", "o", 0, "")
	if got := r.ExpiresAt.Sub(now); got != DefaultTTL {
		t.Errorf("zero TTL = %v; want DefaultTTL %v", got, DefaultTTL)
	}

	// Over-cap TTL → MaxTTL.
	r, _ = s.Acquire("B", "o", 100*time.Hour, "")
	if got := r.ExpiresAt.Sub(now); got != MaxTTL {
		t.Errorf("over-cap TTL = %v; want MaxTTL %v", got, MaxTTL)
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reservations.json")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	s1 := newTestStore(t, path, &now)
	if _, err := s1.Acquire("Pippa", "tiltbuggy", time.Hour, "live"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Raw file sanity: should have valid JSON with 1 entry.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var raw []Reservation
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(raw) != 1 || raw[0].Owner != "tiltbuggy" {
		t.Fatalf("unexpected persisted state: %v", raw)
	}

	// Fresh store at same path reads the existing entry.
	s2 := newTestStore(t, path, &now)
	r, ok := s2.Get("Pippa")
	if !ok {
		t.Fatal("reservation did not persist")
	}
	if r.Owner != "tiltbuggy" {
		t.Errorf("persisted owner = %q; want tiltbuggy", r.Owner)
	}
}

func TestPersistence_ExpiredPrunedOnLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reservations.json")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Create and acquire in one clock era.
	s1 := newTestStore(t, path, &now)
	_, _ = s1.Acquire("Pippa", "tiltbuggy", 5*time.Minute, "")

	// Time travel past expiry, load fresh.
	later := now.Add(1 * time.Hour)
	s2, err := New(path, WithNow(func() time.Time { return later }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := s2.Get("Pippa"); ok {
		t.Error("expired reservation should have been pruned on load")
	}
}

func TestNormalizer(t *testing.T) {
	// Simulates inventory.AliasFor: maps UDIDs to "Pippa" and leaves
	// unknown inputs untouched.
	norm := func(ref string) string {
		if ref == "00008103-000D39301A6A201E" || ref == "Pippa" {
			return "Pippa"
		}
		return ref
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, err := New("", WithNow(func() time.Time { return now }), WithNormalizer(norm))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Reserve by alias, conflict-check by raw UDID.
	_, _ = s.Acquire("Pippa", "tiltbuggy", 0, "")
	if err := s.Authorize("00008103-000D39301A6A201E", "otherproj"); !IsConflict(err) {
		t.Errorf("normalizer should have caught raw UDID; got %v", err)
	}

	// Reserve by raw UDID, release by alias.
	_, _ = s.Acquire("00008103-000D39301A6A201E", "tiltbuggy2", 0, "")
	// Already held by tiltbuggy under key Pippa — the above should
	// renew rather than conflict. Owner is different though → conflict.
	// But we just acquired Pippa for tiltbuggy; a second Acquire with
	// owner="tiltbuggy2" conflicts.
	// That's fine — in that case we need to release first.
	_ = s.Release("Pippa", "tiltbuggy")
	if _, err := s.Acquire("00008103-000D39301A6A201E", "tiltbuggy2", 0, ""); err != nil {
		t.Errorf("Acquire by raw UDID after Release by alias: %v", err)
	}
	if _, ok := s.Get("Pippa"); !ok {
		t.Error("reservation should be visible under alias key")
	}
}

func TestList_PrunesExpired(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)
	_, _ = s.Acquire("A", "o1", 5*time.Minute, "")
	_, _ = s.Acquire("B", "o2", 30*time.Minute, "")

	now = now.Add(10 * time.Minute) // A expired, B still live
	got := s.List()
	if len(got) != 1 {
		t.Fatalf("List returned %d entries; want 1", len(got))
	}
	if got[0].Device != "B" {
		t.Errorf("remaining entry = %+v; want device B", got[0])
	}
}

func TestAcquire_OwnerRequired(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStore(t, "", &now)
	if _, err := s.Acquire("Pippa", "", 0, ""); err == nil {
		t.Error("Acquire with empty owner should error")
	}
}

func TestConflictError(t *testing.T) {
	r := Reservation{Device: "Pippa", Owner: "X", ExpiresAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Note: "n"}
	c := &Conflict{Reservation: r}
	msg := c.Error()
	for _, want := range []string{"Pippa", "X", "2026-01-01", "(note:"} {
		if !stringsContains(msg, want) {
			t.Errorf("Conflict.Error() missing %q: %s", want, msg)
		}
	}
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
