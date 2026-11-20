// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simlease

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Provenance is informational. A torn provenance write made New fail,
// and the daemon then ran with no lease book and no reaper, so leases
// from before the restart were never undone (DH-36).
func TestDamagedProvenanceKeepsLeasing(t *testing.T) {
	for _, body := range []string{"", "[{\"id\":"} {
		dir := t.TempDir()
		leasePath := filepath.Join(dir, "sim-leases.json")
		provPath := filepath.Join(dir, "sim-provenance.json")
		// A lease the previous daemon left, in the installed lease-file shape.
		lease := `[{"id":"x","udid":"AAAAAAAA-0000-4000-8000-000000000001","name":"n","device_type":"` + iphone16 +
			`","runtime":"` + runtime18 + `","owner":"a","action":"booted","ttl_s":60,"acquired_at":"2027-01-15T08:00:00Z","renewed_at":"2027-01-15T08:00:00Z","expires_at":"2027-01-15T08:01:00Z"}]`
		if err := os.WriteFile(leasePath, []byte(lease), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(provPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		m, err := New(&Args{Simctl: &fakeSimctl{}, Path: leasePath, ProvenancePath: provPath, Now: fixedClock()})
		if err != nil {
			t.Fatalf("provenance %q disabled simulator leases: %v", body, err)
		}
		if n := len(m.Status(ctx).Leases); n != 1 {
			t.Fatalf("provenance %q: lease book holds %d leases, want the 1 on disk", body, n)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		kept := false
		for _, e := range entries {
			kept = kept || strings.HasPrefix(e.Name(), "sim-provenance.json.corrupt-")
		}
		if !kept {
			t.Fatalf("provenance %q: damaged file was not kept for inspection: %v", body, entries)
		}
	}
}
