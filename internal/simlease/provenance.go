// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simlease

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Party names who a simulator was made or leased for (🎯T189).
type Party struct {
	Owner   string `json:"owner"`
	Project string `json:"project,omitempty"`
}

// Claim marks a simulator or AVD as someone's, so it is neither reused
// by sim_acquire nor offered for pruning. Used for devices made by hand.
type Claim struct {
	Owner     string    `json:"owner"`
	Project   string    `json:"project,omitempty"`
	Note      string    `json:"note,omitempty"`
	ClaimedAt time.Time `json:"claimed_at"`
}

// Provenance is what spyder remembers about a device after any lease on
// it has ended. Keyed by UDID (simulators) or AVD name.
type Provenance struct {
	ID              string    `json:"id"`
	CreatedBySpyder bool      `json:"created_by_spyder,omitempty"`
	CreatedFor      *Party    `json:"created_for,omitempty"`
	LastLeasedBy    *Party    `json:"last_leased_by,omitempty"`
	LastUsedAt      time.Time `json:"last_used_at,omitzero"`
	Claim           *Claim    `json:"claim,omitempty"`
}

// provKey normalises a device id: UDIDs compare case-insensitively.
func provKey(id string) string { return strings.ToUpper(id) }

func (m *Manager) loadProvenance() error {
	if m.provPath == "" {
		return nil
	}
	data, err := os.ReadFile(m.provPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("simlease: read %s: %w", m.provPath, err)
	}
	var list []Provenance
	if err := json.Unmarshal(data, &list); err != nil {
		// Provenance is a ledger of who made and used each device; a torn
		// write must not take the lease book and its reaper down with it
		// (DH-36). Keep the damaged file for inspection and start empty.
		// Claims recorded in it are lost until someone re-claims.
		aside := fmt.Sprintf("%s.corrupt-%s", m.provPath, m.now().UTC().Format("20060102T150405Z"))
		if rerr := os.Rename(m.provPath, aside); rerr != nil {
			return fmt.Errorf("simlease: parse %s: %w (moving it aside failed: %v)", m.provPath, err, rerr)
		}
		slog.Error("simlease: provenance file damaged; moved aside and started empty",
			"path", m.provPath, "moved_to", aside, "error", err)
		return nil
	}
	for i := range list {
		p := list[i]
		m.prov[provKey(p.ID)] = &p
	}
	return nil
}

// persistProvLocked writes the provenance ledger. Callers hold m.mu.
func (m *Manager) persistProvLocked() {
	m.lastProvPersist = m.now()
	if m.provPath == "" {
		return
	}
	list := make([]Provenance, 0, len(m.prov))
	for _, p := range m.prov {
		list = append(list, *p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	data, err := json.MarshalIndent(list, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(m.provPath), 0o755)
	}
	if err == nil {
		err = writeFileSynced(m.provPath, data)
	}
	if err != nil {
		slog.Error("simlease: persist provenance failed", "path", m.provPath, "error", err)
	}
}

// provLocked returns the record for id, creating it. Callers hold m.mu.
func (m *Manager) provLocked(id string) *Provenance {
	k := provKey(id)
	p, ok := m.prov[k]
	if !ok {
		p = &Provenance{ID: id}
		m.prov[k] = p
	}
	return p
}

// recordAcquireLocked notes a successful acquire. Callers hold m.mu.
func (m *Manager) recordAcquireLocked(l *Lease) {
	p := m.provLocked(l.UDID)
	party := &Party{Owner: l.Owner, Project: l.Project}
	if l.Action == ActionCreated {
		p.CreatedBySpyder = true
		p.CreatedFor = party
	}
	p.LastLeasedBy = party
	p.LastUsedAt = m.now()
	m.persistProvLocked()
}

// recordReleaseLocked notes a release; a deleted device is forgotten.
// Callers hold m.mu.
func (m *Manager) recordReleaseLocked(l Lease, undo string) {
	if undo == "deleted" {
		delete(m.prov, provKey(l.UDID))
	} else if l.UDID != "" {
		m.provLocked(l.UDID).LastUsedAt = m.now()
	}
	m.persistProvLocked()
}

// claimedByOtherLocked reports whether id is claimed by someone other
// than owner. Callers hold m.mu.
func (m *Manager) claimedByOtherLocked(id, owner string) bool {
	p, ok := m.prov[provKey(id)]
	return ok && p.Claim != nil && p.Claim.Owner != owner
}

// ClaimArgs marks a device as someone's.
type ClaimArgs struct {
	ID      string // UDID or AVD name
	Owner   string
	Project string
	Note    string
}

// Claim records that a device belongs to args.Owner. A device claimed
// by someone else must be unclaimed by them first.
func (m *Manager) Claim(args ClaimArgs) (Provenance, error) {
	if args.ID == "" || args.Owner == "" {
		return Provenance{}, errors.New("sim_claim: id and owner are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimedByOtherLocked(args.ID, args.Owner) {
		c := m.prov[provKey(args.ID)].Claim
		return Provenance{}, fmt.Errorf("%s is already claimed by %q", args.ID, c.Owner)
	}
	p := m.provLocked(args.ID)
	p.Claim = &Claim{Owner: args.Owner, Project: args.Project, Note: args.Note, ClaimedAt: m.now()}
	m.persistProvLocked()
	return *p, nil
}

// Unclaim removes owner's claim on id.
func (m *Manager) Unclaim(id, owner string) (Provenance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.prov[provKey(id)]
	if !ok || p.Claim == nil {
		return Provenance{}, fmt.Errorf("%s is not claimed", id)
	}
	if p.Claim.Owner != owner {
		return Provenance{}, fmt.Errorf("%s is claimed by %q, not %q", id, p.Claim.Owner, owner)
	}
	p.Claim = nil
	m.persistProvLocked()
	return *p, nil
}

// forgetMissingLocked drops provenance for devices that no longer exist.
// present holds provKey'd ids of every simulator and AVD; call it only
// when every listing succeeded. Callers hold m.mu.
func (m *Manager) forgetMissingLocked(present map[string]bool) {
	changed := false
	for k := range m.prov {
		if !present[k] {
			delete(m.prov, k)
			changed = true
		}
	}
	if changed {
		m.persistProvLocked()
	}
}

// writeFileSynced replaces path with data through a synced temp file and
// syncs the directory, so a power loss leaves the old file or the new
// one, never an empty one.
func writeFileSynced(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		if err := d.Sync(); err != nil {
			slog.Error("simlease: directory sync", "path", dir, "error", err)
		}
		_ = d.Close()
	}
	return nil
}
