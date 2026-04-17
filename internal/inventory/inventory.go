// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package inventory resolves symbolic device names (e.g. "Pippa") to
// platform-specific UUIDs. Backed by a JSON file at ~/.spyder/inventory.json.
package inventory

import (
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/marcelocantos/spyder/internal/paths"
)

// Entry records a known device with its platform-specific identifiers.
type Entry struct {
	Alias         string `json:"alias"`
	Platform      string `json:"platform"`                 // "ios" or "android"
	IOSUUID       string `json:"ios_uuid,omitempty"`       // pymobiledevice3 / xctrace
	IOSCoreDevice string `json:"ios_coredevice,omitempty"` // devicectl
	AndroidSerial string `json:"android_serial,omitempty"` // adb
	Notes         string `json:"notes,omitempty"`
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
