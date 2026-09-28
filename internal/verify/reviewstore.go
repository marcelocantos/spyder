// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// reviewsDBFile is the owner-review database under the hub's state dir. The
// dashboard saves as the owner types, so reviews need a store that takes
// frequent small writes safely.
const reviewsDBFile = "reviews.db"

// reviewStore is the source of truth for owner reviews. Runs keep a cache.
type reviewStore struct {
	db *sql.DB
}

// openReviewStore opens (or creates) the database under dir. An empty dir
// gives a private in-memory database (tests).
func openReviewStore(dir string) (*reviewStore, error) {
	dsn := ":memory:"
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		dsn = "file:" + filepath.Join(dir, reviewsDBFile) + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: an in-memory database exists per connection, and
	// review writes are small and serial anyway.
	db.SetMaxOpenConns(1)
	if err := migrateReviews(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", dsn, err)
	}
	return &reviewStore{db: db}, nil
}

func migrateReviews(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 1 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// finding may be empty while the owner has typed notes but not yet
	// chosen a finding; a row with neither is deleted instead.
	if _, err := tx.Exec(`
		CREATE TABLE owner_review (
			run_id     TEXT NOT NULL,
			step_id    TEXT NOT NULL,
			finding    TEXT NOT NULL DEFAULT '',
			notes      TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL,
			PRIMARY KEY (run_id, step_id),
			CHECK (finding <> '' OR notes <> '')
		)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 1`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *reviewStore) put(runID, stepID string, rev *OwnerReview) error {
	if rev == nil {
		_, err := s.db.Exec(`DELETE FROM owner_review WHERE run_id = ? AND step_id = ?`, runID, stepID)
		return err
	}
	_, err := s.db.Exec(`
		INSERT INTO owner_review (run_id, step_id, finding, notes, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (run_id, step_id) DO UPDATE SET
			finding    = excluded.finding,
			notes      = excluded.notes,
			updated_at = excluded.updated_at`,
		runID, stepID, rev.Finding, rev.Notes, rev.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *reviewStore) load(runID string) (map[string]*OwnerReview, error) {
	rows, err := s.db.Query(`SELECT step_id, finding, notes, updated_at FROM owner_review WHERE run_id = ?`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*OwnerReview{}
	for rows.Next() {
		var step, at string
		rev := &OwnerReview{}
		if err := rows.Scan(&step, &rev.Finding, &rev.Notes, &at); err != nil {
			return nil, err
		}
		rev.UpdatedAt, _ = time.Parse(time.RFC3339Nano, at)
		out[step] = rev
	}
	return out, rows.Err()
}
