// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package inventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleInventory = `[
  {
    "alias": "iPad",
    "platform": "ios",
    "ios_uuid": "00008103-001122334455667A",
    "ios_coredevice": "00000000-0000-0000-0000-000000000001",
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
	for _, name := range []string{"iPad", "ipad", "IPAD", "iPaD"} {
		entry, ok := s.Lookup(name)
		if !ok {
			t.Errorf("Lookup(%q) = !ok; want ok", name)
			continue
		}
		if entry.Alias != "iPad" {
			t.Errorf("Lookup(%q).Alias = %q; want iPad", name, entry.Alias)
		}
	}
}

func TestLookup_ByUUID(t *testing.T) {
	s := withInventory(t, sampleInventory)
	cases := map[string]string{
		"00008103-001122334455667A":            "iPad",      // iOS hardware UDID
		"00000000-0000-0000-0000-000000000001": "iPad",      // iOS CoreDevice UUID
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
	if got := s.AliasFor("00008103-001122334455667A"); got != "iPad" {
		t.Errorf("AliasFor(iOS UDID) = %q; want iPad", got)
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
	if _, ok := s.Lookup("iPad"); ok {
		t.Errorf("Lookup on missing inventory returned ok; want !ok")
	}
	if got := s.AliasFor("00008103-001122334455667A"); got != "" {
		t.Errorf("AliasFor on missing inventory = %q; want empty", got)
	}
}

// TestEntryTagsAttrs_JSONRoundTrip verifies that Tags and Attrs
// survive a JSON encode/decode cycle and that inventory entries without
// these fields (i.e. old-format files) load cleanly with nil/empty values.
func TestEntryTagsAttrs_JSONRoundTrip(t *testing.T) {
	const withTagsAttrs = `[
	  {
	    "alias": "iPad",
	    "platform": "ios",
	    "ios_uuid": "00008103-001122334455667A",
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
	entry, ok := s.Lookup("iPad")
	if !ok {
		t.Fatal("iPad not found")
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

func TestStore_ReloadsOnFileChange(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".spyder")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "inventory.json")
	if err := os.WriteFile(path, []byte(sampleInventory), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := New()
	if _, ok := s.Lookup("Raspberry"); !ok {
		t.Fatal("expected Raspberry after first load")
	}

	// Sleep so mtime advances on filesystems with 1s resolution.
	time.Sleep(1100 * time.Millisecond)
	const updated = `[
  {
    "alias": "S24",
    "platform": "android",
    "android_serial": "RFCX20VKMAR"
  }
]`
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	if _, ok := s.Lookup("Raspberry"); ok {
		t.Error("old alias should be gone after reload")
	}
	entry, ok := s.Lookup("S24")
	if !ok {
		t.Fatal("expected S24 after reload")
	}
	if entry.AndroidSerial != "RFCX20VKMAR" {
		t.Errorf("AndroidSerial = %q; want RFCX20VKMAR", entry.AndroidSerial)
	}
	if got := s.AliasFor("RFCX20VKMAR"); got != "S24" {
		t.Errorf("AliasFor = %q; want S24", got)
	}
}

func TestStore_InvalidJSONKeepsPrevious(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".spyder")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "inventory.json")
	if err := os.WriteFile(path, []byte(sampleInventory), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := New()
	if _, ok := s.Lookup("iPad"); !ok {
		t.Fatal("expected iPad")
	}

	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(path, []byte("{not valid"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	if _, ok := s.Lookup("iPad"); !ok {
		t.Error("invalid JSON should keep previous snapshot")
	}
}

func TestClassifyRaw(t *testing.T) {
	cases := []struct {
		input        string
		wantPlatform string
		wantField    string // which field the input should end up in
	}{
		{"00008103-001122334455667A", "ios", "ios_uuid"},                  // hardware UDID
		{"00000000-0000-0000-0000-000000000001", "ios", "ios_coredevice"}, // standard UUID
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
