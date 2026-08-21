// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package usbspeed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Store persists the highest USB link speed observed per physical-device
// serial. It ratchets up only: a later 480 Mb/s plug does not lower a
// 5 Gb/s ceiling. Path is injectable so tests never touch ~/.spyder/.
type Store struct {
	mu     sync.Mutex
	path   string
	speeds map[string]int // CanonicalID → kUSBDeviceSpeed
}

// Open loads ceilings from path. A missing or unreadable file is an
// empty store — first observations seed it.
func Open(path string) *Store {
	s := &Store{path: path, speeds: map[string]int{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var raw map[string]int
	if err := json.Unmarshal(data, &raw); err != nil {
		return s
	}
	for k, v := range raw {
		s.speeds[CanonicalID(k)] = v
	}
	return s
}

// Has reports whether a ceiling is already recorded for id.
func (s *Store) Has(id string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.speeds[CanonicalID(id)]
	return ok
}

// SetCeiling records id's ceiling without comparing to a live speed.
// Used to apply an optional inventory usb_max seed before Observe.
func (s *Store) SetCeiling(id string, speed int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := CanonicalID(id)
	if prev, ok := s.speeds[key]; ok && prev >= speed {
		return
	}
	s.speeds[key] = speed
}

// Observe records a live speed. Returns the (possibly ratcheted) ceiling
// and whether live is below that ceiling. First observation seeds the
// ceiling and is never an anomaly.
func (s *Store) Observe(id string, live int) (ceiling int, anomaly bool, changed bool) {
	if s == nil {
		return live, false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := CanonicalID(id)
	cur, ok := s.speeds[key]
	if !ok {
		s.speeds[key] = live
		return live, false, true
	}
	if live > cur {
		s.speeds[key] = live
		return live, false, true
	}
	return cur, live < cur, false
}

// Save writes the ceiling map to path. No-op when path is empty.
func (s *Store) Save() error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.speeds, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(s.path, data, 0o600)
}
