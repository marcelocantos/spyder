// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package inventory

import (
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
