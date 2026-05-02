// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package poolstore

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPutListDelete(t *testing.T) {
	s := newTestStore(t)

	now := time.Now()
	h := Hold{
		InstanceID: "inst-1",
		DeviceID:   "UDID-1",
		Template:   "iphone-16",
		Platform:   "ios",
		Holder:     "alice",
		AcquiredAt: now,
	}
	if err := s.Put(h); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 hold, got %d", len(got))
	}
	if got[0].InstanceID != h.InstanceID || got[0].DeviceID != h.DeviceID ||
		got[0].Template != h.Template || got[0].Platform != h.Platform ||
		got[0].Holder != h.Holder {
		t.Errorf("hold mismatch: got %+v want %+v", got[0], h)
	}
	if !got[0].AcquiredAt.Equal(time.Unix(now.Unix(), 0)) {
		t.Errorf("acquired_at mismatch: got %v want %v", got[0].AcquiredAt, now)
	}

	if err := s.Delete("inst-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err = s.List()
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 holds after delete, got %d", len(got))
	}
}

func TestPutIdempotent(t *testing.T) {
	s := newTestStore(t)

	h := Hold{
		InstanceID: "inst-1",
		DeviceID:   "UDID-1",
		Template:   "iphone-16",
		Platform:   "ios",
		Holder:     "alice",
		AcquiredAt: time.Now(),
	}
	if err := s.Put(h); err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	h.Holder = "bob"
	if err := s.Put(h); err != nil {
		t.Fatalf("Put 2: %v", err)
	}

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 hold (upsert), got %d", len(got))
	}
	if got[0].Holder != "bob" {
		t.Errorf("holder = %q, want %q", got[0].Holder, "bob")
	}
}

func TestDeleteByDevice(t *testing.T) {
	s := newTestStore(t)

	h := Hold{
		InstanceID: "inst-1",
		DeviceID:   "UDID-1",
		Template:   "t",
		Platform:   "ios",
		Holder:     "a",
		AcquiredAt: time.Now(),
	}
	if err := s.Put(h); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.DeleteByDevice("UDID-1"); err != nil {
		t.Fatalf("DeleteByDevice: %v", err)
	}
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 holds, got %d", len(got))
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	if err := s1.Put(Hold{
		InstanceID: "inst-1",
		DeviceID:   "UDID-1",
		Template:   "t",
		Platform:   "ios",
		Holder:     "alice",
		AcquiredAt: time.Now(),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer s2.Close()
	got, err := s2.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].InstanceID != "inst-1" {
		t.Fatalf("want 1 hold inst-1, got %+v", got)
	}
}

func TestDeleteMissingIsNoop(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete("nope"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestRequiredFields(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put(Hold{InstanceID: "x"}); err == nil {
		t.Errorf("want error for missing device_id")
	}
	if err := s.Put(Hold{DeviceID: "x"}); err == nil {
		t.Errorf("want error for missing instance_id")
	}
}
