// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package inventory resolves symbolic device names (e.g. "Pippa") to
// platform-specific UUIDs. Backed by a JSON file at ~/.spyder/inventory.json.
package inventory

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/marcelocantos/spyder/internal/paths"
)

// iOS hardware UDID: 8 hex, dash, 16 hex. Emitted by pymobiledevice3 and
// xcrun xctrace list devices.
var iosHardwareUDID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{16}$`)

// Standard UUID (8-4-4-4-12). iOS 17+ CoreDevice UUIDs from devicectl
// follow this form.
var standardUUID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// Entry records a known device with its platform-specific identifiers.
type Entry struct {
	Alias         string            `json:"alias"`
	Platform      string            `json:"platform"`                 // "ios" or "android"
	IOSUUID       string            `json:"ios_uuid,omitempty"`       // pymobiledevice3 / xctrace
	IOSCoreDevice string            `json:"ios_coredevice,omitempty"` // devicectl
	AndroidSerial string            `json:"android_serial,omitempty"` // adb
	Notes         string            `json:"notes,omitempty"`
	Tags          []string          `json:"tags,omitempty"`  // free-form labels for selector matching
	Attrs         map[string]string `json:"attrs,omitempty"` // key/value pairs for exact-match selector predicates
}

// Store holds the inventory, loaded lazily from disk.
type Store struct {
	mu      sync.Mutex
	entries []Entry
	loaded  bool
}

// New creates an empty inventory store.
func New() *Store { return &Store{} }

// Path returns the on-disk location of the inventory file.
func (s *Store) Path() string { return paths.InventoryPath() }

// Lookup finds an entry by alias (case-insensitive) or by any known UUID.
func (s *Store) Lookup(ref string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()

	for _, e := range s.entries {
		if strings.EqualFold(e.Alias, ref) ||
			e.IOSUUID == ref ||
			e.IOSCoreDevice == ref ||
			e.AndroidSerial == ref {
			return e, true
		}
	}
	return Entry{}, false
}

// ClassifyRaw echoes a raw identifier back as an Entry with the input
// placed in the field that best matches its format. Use when Lookup
// misses — callers (e.g. the resolve tool) can treat any input as a
// pass-through identifier rather than erroring.
func ClassifyRaw(raw string) Entry {
	switch {
	case iosHardwareUDID.MatchString(raw):
		return Entry{Platform: "ios", IOSUUID: raw}
	case standardUUID.MatchString(raw):
		return Entry{Platform: "ios", IOSCoreDevice: raw}
	default:
		return Entry{Platform: "android", AndroidSerial: raw}
	}
}

// AliasFor returns the alias registered for a UUID, or "" if unknown.
func (s *Store) AliasFor(uuid string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()

	for _, e := range s.entries {
		if e.IOSUUID == uuid || e.IOSCoreDevice == uuid || e.AndroidSerial == uuid {
			return e.Alias
		}
	}
	return ""
}

// Entries returns a snapshot of all inventory entries. Used by the
// selector resolver to build the candidate list.
func (s *Store) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

func (s *Store) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true

	data, err := os.ReadFile(paths.InventoryPath())
	if err != nil {
		return // missing file is fine — empty inventory
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return
	}
	s.entries = entries
}
