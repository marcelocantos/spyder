// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package inventory resolves symbolic device names (e.g. "iPad") to
// platform-specific UUIDs. Backed by a JSON file at ~/.spyder/inventory.json.
//
// The store re-reads the file when its size or mtime changes, so edits to
// inventory.json take effect without restarting the daemon.
package inventory

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/marcelocantos/spyder/internal/paths"
)

// iOS hardware UDID: 8 hex, dash, 16 hex. Emitted by `ios list` (go-ios)
// and `xcrun xctrace list devices`.
var iosHardwareUDID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{16}$`)

// Standard UUID (8-4-4-4-12). iOS 17+ CoreDevice UUIDs from devicectl
// follow this form.
var standardUUID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// Entry records a known device with its platform-specific identifiers.
type Entry struct {
	Alias         string `json:"alias"`
	Platform      string `json:"platform"`                 // "ios" or "android"
	IOSUUID       string `json:"ios_uuid,omitempty"`       // go-ios / xctrace
	IOSCoreDevice string `json:"ios_coredevice,omitempty"` // devicectl
	AndroidSerial string `json:"android_serial,omitempty"` // adb
	// ExecutablePath is the binary spyder launches for a platform="desktop"
	// entry; it doubles as the desktop "device id" (analogous to ios_uuid /
	// android_serial). WorkingDir optionally overrides the launched process's
	// cwd (default: the binary's own directory).
	ExecutablePath string            `json:"executable_path,omitempty"`
	WorkingDir     string            `json:"working_dir,omitempty"`
	Notes            string            `json:"notes,omitempty"`
	// ExpectedPresent marks a fleet device that should trigger needs_attention when absent (🎯T99.6).
	ExpectedPresent bool              `json:"expected_present,omitempty"`
	Tags           []string          `json:"tags,omitempty"`  // free-form labels for selector matching
	Attrs          map[string]string `json:"attrs,omitempty"` // key/value pairs for exact-match selector predicates
}

// Store holds the inventory, reloaded from disk when inventory.json changes.
type Store struct {
	mu      sync.Mutex
	entries []Entry
	// Last successfully applied inventory.json identity (size + mtime).
	// fileSize == -1 means the file was missing at last check.
	fileMod  time.Time
	fileSize int64
}

// New creates an empty inventory store.
func New() *Store { return &Store{fileSize: -1} }

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

// loadLocked reloads ~/.spyder/inventory.json when its size or mtime
// differs from the last successful load. Callers must hold s.mu.
//
// Missing file → empty inventory. Unreadable or invalid JSON keeps the
// previous snapshot (so a half-written editor save does not blank aliases).
func (s *Store) loadLocked() {
	path := paths.InventoryPath()
	fi, err := os.Stat(path)
	if err != nil {
		// Gone or never present.
		s.entries = nil
		s.fileMod = time.Time{}
		s.fileSize = -1
		return
	}
	mod, size := fi.ModTime(), fi.Size()
	if s.fileSize == size && s.fileMod.Equal(mod) {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return // keep previous snapshot
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return // keep previous snapshot (mid-write / syntax error)
	}
	s.entries = entries
	s.fileMod = mod
	s.fileSize = size
}
