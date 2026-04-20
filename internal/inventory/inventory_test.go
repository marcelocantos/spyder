// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package inventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const sampleInventory = `[
  {
    "alias": "Pippa",
    "platform": "ios",
    "ios_uuid": "00008103-000D39301A6A201E",
    "ios_coredevice": "E1A01EA6-8D77-556C-B18D-D470B2909E87",
    "notes": "Preferred iPad test device"
  },
  {
    "alias": "Raspberry",
    "platform": "android",
    "android_serial": "R5CR112X76K"
  }
]`

// withInventory sets HOME to a temp dir containing the given inventory
// JSON. Returns a fresh Store that will read from it.
func withInventory(t *testing.T, contents string) *Store {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if contents != "" {
		dir := filepath.Join(tmp, ".spyder")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "inventory.json"), []byte(contents), 0o600); err != nil {
			t.Fatalf("write inventory: %v", err)
		}
	}
	return New()
}

func TestLookup_AliasCaseInsensitive(t *testing.T) {
	s := withInventory(t, sampleInventory)
	for _, name := range []string{"Pippa", "pippa", "PIPPA", "PiPpA"} {
		entry, ok := s.Lookup(name)
		if !ok {
			t.Errorf("Lookup(%q) = !ok; want ok", name)
			continue
		}
		if entry.Alias != "Pippa" {
			t.Errorf("Lookup(%q).Alias = %q; want Pippa", name, entry.Alias)
		}
	}
}

func TestLookup_ByUUID(t *testing.T) {
	s := withInventory(t, sampleInventory)
	cases := map[string]string{
		"00008103-000D39301A6A201E":            "Pippa",     // iOS hardware UDID
		"E1A01EA6-8D77-556C-B18D-D470B2909E87": "Pippa",     // iOS CoreDevice UUID
		"R5CR112X76K":                          "Raspberry", // Android serial
	}
	for id, wantAlias := range cases {
		entry, ok := s.Lookup(id)
		if !ok {
			t.Errorf("Lookup(%q) = !ok; want ok", id)
			continue
		}
		if entry.Alias != wantAlias {
			t.Errorf("Lookup(%q).Alias = %q; want %q", id, entry.Alias, wantAlias)
		}
	}
}

func TestLookup_Miss(t *testing.T) {
	s := withInventory(t, sampleInventory)
	if _, ok := s.Lookup("NobodyHere"); ok {
		t.Errorf("Lookup(unknown) = ok; want !ok")
	}
}

func TestAliasFor(t *testing.T) {
	s := withInventory(t, sampleInventory)
	if got := s.AliasFor("00008103-000D39301A6A201E"); got != "Pippa" {
		t.Errorf("AliasFor(iOS UDID) = %q; want Pippa", got)
	}
	if got := s.AliasFor("R5CR112X76K"); got != "Raspberry" {
		t.Errorf("AliasFor(Android serial) = %q; want Raspberry", got)
	}
	if got := s.AliasFor("deadbeef"); got != "" {
		t.Errorf("AliasFor(unknown) = %q; want empty", got)
	}
}

func TestMissingInventory_IsEmpty(t *testing.T) {
	// Don't write any inventory file — Lookup must return no error.
	s := withInventory(t, "")
	if _, ok := s.Lookup("Pippa"); ok {
		t.Errorf("Lookup on missing inventory returned ok; want !ok")
	}
	if got := s.AliasFor("00008103-000D39301A6A201E"); got != "" {
		t.Errorf("AliasFor on missing inventory = %q; want empty", got)
	}
}

// TestEntryTagsAttrs_JSONRoundTrip verifies that Tags and Attrs
// survive a JSON encode/decode cycle and that inventory entries without
// these fields (i.e. old-format files) load cleanly with nil/empty values.
func TestEntryTagsAttrs_JSONRoundTrip(t *testing.T) {
	const withTagsAttrs = `[
	  {
	    "alias": "Pippa",
	    "platform": "ios",
	    "ios_uuid": "00008103-000D39301A6A201E",
	    "tags": ["ipad", "arm64"],
	    "attrs": {"env": "ci", "zone": "lab-a"}
	  }
	]`
	var entries []Entry
	if err := json.Unmarshal([]byte(withTagsAttrs), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if len(e.Tags) != 2 {
		t.Errorf("Tags: got %v; want [ipad arm64]", e.Tags)
	}
	if e.Attrs["env"] != "ci" || e.Attrs["zone"] != "lab-a" {
		t.Errorf("Attrs: got %v", e.Attrs)
	}

	// Round-trip: marshal back and compare key fields.
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var entries2 []Entry
	if err := json.Unmarshal(data, &entries2); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	e2 := entries2[0]
	if e2.Tags[0] != "ipad" || e2.Attrs["env"] != "ci" {
		t.Errorf("round-trip mismatch: %+v", e2)
	}
}

// TestEntryTagsAttrs_BackwardsCompat verifies that old inventory entries
// (without tags/attrs) load without errors and have nil/empty values.
func TestEntryTagsAttrs_BackwardsCompat(t *testing.T) {
	s := withInventory(t, sampleInventory)
	entry, ok := s.Lookup("Pippa")
	if !ok {
		t.Fatal("Pippa not found")
	}
	if entry.Tags != nil {
		t.Errorf("old entry should have nil Tags; got %v", entry.Tags)
	}
	if entry.Attrs != nil {
		t.Errorf("old entry should have nil Attrs; got %v", entry.Attrs)
	}
}

// TestStore_Entries returns all entries from the store.
func TestStore_Entries(t *testing.T) {
	s := withInventory(t, sampleInventory)
	entries := s.Entries()
	if len(entries) != 2 {
		t.Errorf("Entries() returned %d entries; want 2", len(entries))
	}
}

func TestClassifyRaw(t *testing.T) {
	cases := []struct {
		input        string
		wantPlatform string
		wantField    string // which field the input should end up in
	}{
		{"00008103-000D39301A6A201E", "ios", "ios_uuid"},                  // hardware UDID
		{"E1A01EA6-8D77-556C-B18D-D470B2909E87", "ios", "ios_coredevice"}, // standard UUID
		{"12345678-1234-1234-1234-1234567890AB", "ios", "ios_coredevice"},
		{"R5CR112X76K", "android", "android_serial"},
		{"some-random-string", "android", "android_serial"},
	}
	for _, c := range cases {
		e := ClassifyRaw(c.input)
		if e.Platform != c.wantPlatform {
			t.Errorf("ClassifyRaw(%q).Platform = %q; want %q", c.input, e.Platform, c.wantPlatform)
		}
		var got string
		switch c.wantField {
		case "ios_uuid":
			got = e.IOSUUID
		case "ios_coredevice":
			got = e.IOSCoreDevice
		case "android_serial":
			got = e.AndroidSerial
		}
		if got != c.input {
			t.Errorf("ClassifyRaw(%q) didn't echo input into %s (got %q)", c.input, c.wantField, got)
		}
	}
}
