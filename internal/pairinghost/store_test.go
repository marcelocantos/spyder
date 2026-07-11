// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pairinghost

import (
	"path/filepath"
	"testing"

	"github.com/marcelocantos/pigeon/crypto"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pairings.json")

	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 {
		t.Fatalf("expected empty store, got %v", s.List())
	}

	local, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	rec := crypto.NewPairingRecord("peer-device-1", "https://relay.example.com", local, peer.Public)
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}

	got, ok := s.Resolve("peer-device-1")
	if !ok || got == nil {
		t.Fatal("Resolve missed peer")
	}
	if got.PeerInstanceID != "peer-device-1" {
		t.Fatalf("peer id: %s", got.PeerInstanceID)
	}

	// Reload from disk.
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.List()) != 1 {
		t.Fatalf("reload list: %v", s2.List())
	}
	got2 := s2.Get("peer-device-1")
	if got2 == nil || got2.RelayURL != "https://relay.example.com" {
		t.Fatalf("reload record: %+v", got2)
	}
}

func TestStoreRejectsNil(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "p.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(nil); err == nil {
		t.Fatal("expected error for nil record")
	}
}
