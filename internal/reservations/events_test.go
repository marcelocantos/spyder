// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package reservations

import (
	"reflect"
	"testing"
	"time"
)

func TestReservationLifecycleObservers(t *testing.T) {
	now := time.Now()
	s := newTestStore(t, "", &now)
	var events []string
	s.SetOnChange(func(kind string, r Reservation) { events = append(events, kind+":"+r.Owner) })
	if _, err := s.Acquire("phone", "one", time.Second, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Renew("phone", "one", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Release("phone", "other"); !IsConflict(err) {
		t.Fatal("expected conflict")
	}
	if err := s.Release("phone", "one"); err != nil {
		t.Fatal(err)
	}
	if err := s.Release("phone", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire("phone", "two", time.Second, ""); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	s.List()
	s.List()
	want := []string{"acquired:one", "renewed:one", "released:one", "acquired:two", "expired:two"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events %v, wanted %v", events, want)
	}
}
