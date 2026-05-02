// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package poolstore is a SQLite-backed ledger of pool reservations
// (TierReserved holds) so the pool can re-attribute booted sims to
// the right caller after a daemon restart. The pool's inventory of
// available/running instances is rebuilt from live simctl/avdmanager
// state at startup; only the holds need to survive restarts.
package poolstore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Hold is one pool reservation persisted across restarts.
type Hold struct {
	InstanceID string    // pool's UUID for the instance
	DeviceID   string    // UDID (iOS) or AVD name (Android)
	Template   string    // pool template name
	Platform   string    // "ios" or "android"
	Holder     string    // caller identity (free-form; e.g. session id)
	AcquiredAt time.Time // wall-clock acquire time
}

// Store is a thread-safe handle to the pool-hold ledger.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite ledger at path. Pass an empty
// path for an in-memory store (tests). The schema is created on first
// use and migrated forward via PRAGMA user_version.
func Open(path string) (*Store, error) {
	dsn := "file::memory:?cache=shared"
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("poolstore: ensure dir: %w", err)
		}
		dsn = "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("poolstore: open %s: %w", path, err)
	}
	// Single writer is enough; SQLite serialises writes anyway. Keep a
	// generous pool for concurrent reads.
	db.SetMaxOpenConns(4)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("poolstore: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Put inserts or replaces a hold row. Idempotent — calling it twice
// for the same instance updates the holder/acquired_at.
func (s *Store) Put(h Hold) error {
	if h.InstanceID == "" || h.DeviceID == "" {
		return errors.New("poolstore: instance_id and device_id are required")
	}
	const q = `
		INSERT INTO pool_holds (instance_id, device_id, template, platform, holder, acquired_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET
			device_id   = excluded.device_id,
			template    = excluded.template,
			platform    = excluded.platform,
			holder      = excluded.holder,
			acquired_at = excluded.acquired_at`
	_, err := s.db.Exec(q,
		h.InstanceID, h.DeviceID, h.Template, h.Platform, h.Holder,
		h.AcquiredAt.Unix())
	if err != nil {
		return fmt.Errorf("poolstore: put %s: %w", h.InstanceID, err)
	}
	return nil
}

// Delete removes a hold row by instance ID. Returns nil if the row
// was absent.
func (s *Store) Delete(instanceID string) error {
	_, err := s.db.Exec(`DELETE FROM pool_holds WHERE instance_id = ?`, instanceID)
	if err != nil {
		return fmt.Errorf("poolstore: delete %s: %w", instanceID, err)
	}
	return nil
}

// DeleteByDevice removes a hold row by device ID. Used during
// adoption when a row's device has vanished.
func (s *Store) DeleteByDevice(deviceID string) error {
	_, err := s.db.Exec(`DELETE FROM pool_holds WHERE device_id = ?`, deviceID)
	if err != nil {
		return fmt.Errorf("poolstore: delete-by-device %s: %w", deviceID, err)
	}
	return nil
}

// List returns every persisted hold. Order is unspecified.
func (s *Store) List() ([]Hold, error) {
	const q = `SELECT instance_id, device_id, template, platform, holder, acquired_at FROM pool_holds`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("poolstore: list: %w", err)
	}
	defer rows.Close()

	var out []Hold
	for rows.Next() {
		var h Hold
		var acquired int64
		if err := rows.Scan(&h.InstanceID, &h.DeviceID, &h.Template, &h.Platform, &h.Holder, &acquired); err != nil {
			return nil, fmt.Errorf("poolstore: scan: %w", err)
		}
		h.AcquiredAt = time.Unix(acquired, 0)
		out = append(out, h)
	}
	return out, rows.Err()
}

// migrate creates or upgrades the schema. Versioned via PRAGMA
// user_version; new versions append new branches.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version < 1 {
		const ddl = `
			CREATE TABLE IF NOT EXISTS pool_holds (
				instance_id TEXT PRIMARY KEY,
				device_id   TEXT NOT NULL UNIQUE,
				template    TEXT NOT NULL,
				platform    TEXT NOT NULL,
				holder      TEXT NOT NULL,
				acquired_at INTEGER NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_pool_holds_device ON pool_holds(device_id);
			PRAGMA user_version = 1;`
		if _, err := db.Exec(ddl); err != nil {
			return err
		}
	}
	return nil
}
