// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pairinghost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/marcelocantos/pigeon/crypto"
)

// Store persists server-side PairingRecords keyed by peer instance ID.
// Safe for concurrent use.
type Store struct {
	path string
	mu   sync.RWMutex
	recs map[string]*crypto.PairingRecord
}

// OpenStore loads pairings from path (empty map if missing).
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, recs: map[string]*crypto.PairingRecord{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read pairings: %w", err)
	}
	if err := json.Unmarshal(b, &s.recs); err != nil {
		return nil, fmt.Errorf("parse pairings: %w", err)
	}
	if s.recs == nil {
		s.recs = map[string]*crypto.PairingRecord{}
	}
	return s, nil
}

// Get returns the record for peer, or nil if unknown.
func (s *Store) Get(peerInstanceID string) *crypto.PairingRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.recs[peerInstanceID]
}

// List returns a snapshot of peer instance IDs.
func (s *Store) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.recs))
	for id := range s.recs {
		out = append(out, id)
	}
	return out
}

// Put stores rec under its PeerInstanceID and flushes to disk.
func (s *Store) Put(rec *crypto.PairingRecord) error {
	if rec == nil {
		return fmt.Errorf("nil PairingRecord")
	}
	if rec.PeerInstanceID == "" {
		return fmt.Errorf("PairingRecord missing PeerInstanceID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[rec.PeerInstanceID] = rec
	return s.flushLocked()
}

// Resolve implements the pigeon.Register Pairing resolver shape:
// returns the record and true when known.
func (s *Store) Resolve(deviceID string) (*crypto.PairingRecord, bool) {
	rec := s.Get(deviceID)
	if rec == nil {
		return nil, false
	}
	return rec, true
}

func (s *Store) flushLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("mkdir pairings dir: %w", err)
	}
	b, err := json.MarshalIndent(s.recs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
