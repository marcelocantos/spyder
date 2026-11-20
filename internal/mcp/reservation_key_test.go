// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/reservations"
)

// A reservation on the inventory alias must gate every ref resolveAdapter
// accepts for that device: the connected name, and the UDID with its
// dash removed (DH-09).
func TestReservationGatesConnectedNameAndDashlessUDID(t *testing.T) {
	const udid = "00008103-001122334455667A"
	var launchedOn []string
	ios := &stubAdapter{
		list: func() ([]device.Info, error) {
			return []device.Info{{UUID: udid, Name: "LabPad", Platform: "ios"}}, nil
		},
		launchApp: func(id, _ string, _ map[string]string) error {
			launchedOn = append(launchedOn, id)
			return nil
		},
	}
	h := newHandlerWithStubs(t, ios, &stubAdapter{})
	// The daemon's normaliser: inventory alias, otherwise the raw string.
	resv, err := reservations.New("", reservations.WithNormalizer(func(ref string) string {
		if e, ok := h.inventory.Lookup(ref); ok && e.Alias != "" {
			return e.Alias
		}
		return ref
	}))
	if err != nil {
		t.Fatal(err)
	}
	h.reservations = resv
	if _, err := resv.Acquire("iPad", "job-A", time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"LabPad", "00008103001122334455667A"} {
		res, err := h.Dispatch(context.Background(), "launch_app", map[string]any{
			"device": ref, "bundle_id": "com.other.app", "owner": "job-B",
		})
		if err != nil {
			t.Fatal(err)
		}
		if res == nil || !res.IsError {
			t.Errorf("job-B launched on job-A's reserved iPad via %q", ref)
		}
	}
	if len(launchedOn) != 0 {
		t.Fatalf("launches reached device ids: %v", launchedOn)
	}
}

// A lease file written by the installed daemon (device key is the alias)
// still blocks the other names of that device.
func TestOldAliasReservationGatesOtherNames(t *testing.T) {
	const udid = "00008103-001122334455667A"
	ios := &stubAdapter{
		list: func() ([]device.Info, error) {
			return []device.Info{{UUID: udid, Name: "Lab iPad", Platform: "ios"}}, nil
		},
		launchApp: func(id, _ string, _ map[string]string) error { return nil },
	}
	h := newHandlerWithStubs(t, ios, &stubAdapter{})
	path := filepath.Join(t.TempDir(), "reservations.json")
	body := `[
  {
    "device": "iPad",
    "owner": "job-A",
    "expires_at": "2027-01-15T09:00:00Z",
    "note": "spyder run",
    "created_at": "2027-01-15T08:00:00Z"
  }
]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	resv, err := reservations.New(path, reservations.WithNormalizer(func(ref string) string {
		if e, ok := h.inventory.Lookup(ref); ok && e.Alias != "" {
			return e.Alias
		}
		return ref
	}))
	if err != nil {
		t.Fatal(err)
	}
	h.reservations = resv
	for _, ref := range []string{"iPad", "Lab iPad", "00008103001122334455667A"} {
		res, err := h.Dispatch(context.Background(), "launch_app", map[string]any{
			"device": ref, "bundle_id": "com.other.app", "owner": "job-B",
		})
		if err != nil {
			t.Fatal(err)
		}
		if res == nil || !res.IsError {
			t.Errorf("old alias lease did not gate %q", ref)
		}
	}
}

// An old CLI could write the display name verbatim. Calls using that same
// spelling must still see the hold even though they now resolve to the alias.
func TestLegacyRawReservationStillGatesItsOriginalReference(t *testing.T) {
	const name = "Lab iPad"
	ios := &stubAdapter{
		list: func() ([]device.Info, error) {
			return []device.Info{{UUID: "00008103-001122334455667A", Name: name, Platform: "ios"}}, nil
		},
	}
	h := newHandlerWithStubs(t, ios, &stubAdapter{})
	path := filepath.Join(t.TempDir(), "reservations.json")
	body := `[{"device":"Lab iPad","owner":"old-checkout","expires_at":"2027-01-15T09:00:00Z","created_at":"2027-01-15T08:00:00Z"}]`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	h.reservations, err = reservations.New(path, reservations.WithNormalizer(h.ReservationKey),
		reservations.WithNow(func() time.Time { return time.Date(2027, 1, 15, 8, 30, 0, 0, time.UTC) }))
	if err != nil {
		t.Fatal(err)
	}
	if res := h.authorize(name, "another-checkout"); res == nil || !res.IsError {
		t.Fatal("legacy reservation lost enforcement for its original reference")
	}
}
