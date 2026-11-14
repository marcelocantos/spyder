// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simlease

import (
	"context"
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

// InventorySchemaVersion is the inventory's JSON contract version. A
// consumer (loadmon) treats any other version as unknown, so bump it only
// for an incompatible change; adding a field is compatible.
const InventorySchemaVersion = 1

// Device kinds and owner kinds in the inventory.
const (
	KindIOSSimulator = "ios_simulator"
	KindAndroidAVD   = "android_avd"
	KindIOSRuntime   = "ios_runtime"
	KindAndroidImage = "android_system_image"

	OwnerLease = "lease"
	OwnerPool  = "pool"
	OwnerClaim = "claim"
	OwnerNone  = "none"
)

// Inventory is every simulator, AVD, runtime and system image, with who
// owns each and what it costs on disk (🎯T189).
type Inventory struct {
	SchemaVersion int                `json:"schema_version"`
	GeneratedAt   time.Time          `json:"generated_at"`
	Devices       []InventoryDevice  `json:"devices"`
	Runtimes      []InventoryRuntime `json:"runtimes"`
	Errors        []string           `json:"errors,omitempty"`
}

// InventoryOwner says who holds a device now.
type InventoryOwner struct {
	Kind      string     `json:"kind"` // lease | pool | claim | none
	Owner     string     `json:"owner,omitempty"`
	Project   string     `json:"project,omitempty"`
	LeaseID   string     `json:"lease_id,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Note      string     `json:"note,omitempty"`
}

// InventoryDevice is one simulator or AVD.
type InventoryDevice struct {
	Kind            string         `json:"kind"`
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	DeviceType      string         `json:"device_type,omitempty"`
	Runtime         string         `json:"runtime,omitempty"`
	State           string         `json:"state"` // booted | shutdown | booting | ... ; running | stopped (AVD)
	Path            string         `json:"path,omitempty"`
	Bytes           int64          `json:"bytes"`
	Owner           InventoryOwner `json:"owner"`
	CreatedBySpyder bool           `json:"created_by_spyder"`
	CreatedFor      *Party         `json:"created_for"`
	LastLeasedBy    *Party         `json:"last_leased_by,omitempty"`
	LastUsedAt      *time.Time     `json:"last_used_at"`
	LastUsedSource  string         `json:"last_used_source,omitempty"` // spyder | simctl | mtime
}

// UsedBy counts the devices built on a runtime or system image.
type UsedBy struct {
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

// InventoryRuntime is one iOS runtime image or Android system image.
type InventoryRuntime struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"` // runtime identifier, or sdkmanager package id
	ImageID   string `json:"image_id,omitempty"`
	Version   string `json:"version"`
	Path      string `json:"path,omitempty"`
	Bytes     int64  `json:"bytes"`
	Deletable bool   `json:"deletable"`
	UsedBy    UsedBy `json:"used_by"`
	// Keep is set when sim_prune_runtimes would spare this image even
	// though nothing uses it: "newest" marks the newest image of its
	// platform, kept so simulators can still be made.
	Keep string `json:"keep,omitempty"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func lowerState(s string) string { return strings.ToLower(s) }

// Inventory lists every device and runtime. It is read-only apart from
// forgetting provenance for devices that no longer exist.
func (m *Manager) Inventory(ctx context.Context) Inventory {
	inv := Inventory{SchemaVersion: InventorySchemaVersion, GeneratedAt: m.now(),
		Devices: []InventoryDevice{}, Runtimes: []InventoryRuntime{}}

	sims, simErr := m.sim.List(ctx)
	if simErr != nil {
		inv.Errors = append(inv.Errors, fmt.Sprintf("list simulators: %v", simErr))
	}
	images, imgErr := m.sim.RuntimeImages(ctx)
	if imgErr != nil {
		inv.Errors = append(inv.Errors, fmt.Sprintf("list runtimes: %v", imgErr))
	}
	var avds []AVD
	var sysImgs []SystemImage
	var avdErr, sysErr error
	if m.android != nil {
		if avds, avdErr = m.android.AVDs(ctx); avdErr != nil {
			inv.Errors = append(inv.Errors, fmt.Sprintf("list AVDs: %v", avdErr))
		}
		if sysImgs, sysErr = m.android.SystemImages(ctx); sysErr != nil {
			inv.Errors = append(inv.Errors, fmt.Sprintf("list system images: %v", sysErr))
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	leaseBy := map[string]*Lease{}
	for _, l := range m.leases {
		if l.UDID != "" {
			leaseBy[provKey(l.UDID)] = l
		}
	}
	owner := func(id, name string) InventoryOwner {
		if l, ok := leaseBy[provKey(id)]; ok {
			return InventoryOwner{Kind: OwnerLease, Owner: l.Owner, Project: l.Project, LeaseID: l.ID, ExpiresAt: timePtr(l.ExpiresAt)}
		}
		if strings.HasPrefix(name, poolNamePrefix) {
			return InventoryOwner{Kind: OwnerPool, Owner: "spyder-pool"}
		}
		if p, ok := m.prov[provKey(id)]; ok && p.Claim != nil {
			return InventoryOwner{Kind: OwnerClaim, Owner: p.Claim.Owner, Project: p.Claim.Project, Note: p.Claim.Note}
		}
		return InventoryOwner{Kind: OwnerNone}
	}
	present := map[string]bool{}
	runtimeUsers := map[string][]string{}
	for _, s := range sims {
		present[provKey(s.UDID)] = true
		runtimeUsers[s.Runtime] = append(runtimeUsers[s.Runtime], s.UDID)
		d := InventoryDevice{
			Kind: KindIOSSimulator, ID: s.UDID, Name: s.Name, DeviceType: s.DeviceType,
			Runtime: s.Runtime, State: lowerState(s.State), Bytes: s.DataBytes,
			Owner: owner(s.UDID, s.Name),
		}
		if s.DataPath != "" {
			d.Path = filepath.Dir(s.DataPath)
		}
		last, src := s.LastUsedAt, "simctl"
		if p, ok := m.prov[provKey(s.UDID)]; ok {
			d.CreatedBySpyder, d.CreatedFor, d.LastLeasedBy = p.CreatedBySpyder, p.CreatedFor, p.LastLeasedBy
			if p.LastUsedAt.After(last) {
				last, src = p.LastUsedAt, "spyder"
			}
		}
		if last.IsZero() && s.DataPath != "" {
			if fi, err := os.Stat(s.DataPath); err == nil {
				last, src = fi.ModTime(), "mtime"
			}
		}
		if !last.IsZero() {
			d.LastUsedAt, d.LastUsedSource = timePtr(last), src
		}
		inv.Devices = append(inv.Devices, d)
	}
	imageUsers := map[string][]string{}
	for _, a := range avds {
		present[provKey(a.Name)] = true
		imageUsers[a.SystemImage] = append(imageUsers[a.SystemImage], a.Name)
		state := "stopped"
		if a.Running {
			state = "running"
		}
		d := InventoryDevice{
			Kind: KindAndroidAVD, ID: a.Name, Name: a.Name, DeviceType: a.DeviceType,
			Runtime: a.SystemImage, State: state, Path: a.Path, Bytes: a.Bytes,
			Owner: owner(a.Name, a.Name),
		}
		last, src := a.LastUsedAt, "mtime"
		if p, ok := m.prov[provKey(a.Name)]; ok {
			d.CreatedBySpyder, d.CreatedFor, d.LastLeasedBy = p.CreatedBySpyder, p.CreatedFor, p.LastLeasedBy
			if p.LastUsedAt.After(last) {
				last, src = p.LastUsedAt, "spyder"
			}
		}
		if !last.IsZero() {
			d.LastUsedAt, d.LastUsedSource = timePtr(last), src
		}
		inv.Devices = append(inv.Devices, d)
	}
	if simErr == nil && avdErr == nil {
		m.forgetMissingLocked(present)
	}

	newestIOS := newestImages(images)
	for _, img := range images {
		users := runtimeUsers[img.RuntimeID]
		r := InventoryRuntime{
			Kind: KindIOSRuntime, ID: img.RuntimeID, ImageID: img.ImageID, Version: img.Version,
			Path: img.Path, Bytes: img.Bytes, Deletable: img.Deletable,
			UsedBy: UsedBy{Count: len(users), IDs: nonNil(users)},
		}
		if newestIOS[img.ImageID] {
			r.Keep = "newest"
		}
		inv.Runtimes = append(inv.Runtimes, r)
	}
	newestAndroid := ""
	for _, si := range sysImgs {
		if newestAndroid == "" || versionLess(apiLevel(newestAndroid), apiLevel(si.ID)) {
			newestAndroid = si.ID
		}
	}
	for _, si := range sysImgs {
		users := imageUsers[si.ID]
		r := InventoryRuntime{
			Kind: KindAndroidImage, ID: si.ID, Version: si.Version, Path: si.Path, Bytes: si.Bytes,
			Deletable: true, UsedBy: UsedBy{Count: len(users), IDs: nonNil(users)},
		}
		if si.ID == newestAndroid {
			r.Keep = "newest"
		}
		inv.Runtimes = append(inv.Runtimes, r)
	}
	sort.Slice(inv.Devices, func(i, j int) bool { return inv.Devices[i].Bytes > inv.Devices[j].Bytes })
	return inv
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// runtimePlatform is "iOS" for com.apple.CoreSimulator.SimRuntime.iOS-18-6.
func runtimePlatform(id string) string {
	s := strings.TrimPrefix(id, "com.apple.CoreSimulator.SimRuntime.")
	p, _, _ := strings.Cut(s, "-")
	return p
}

// newestImages marks the newest Ready image of each platform.
func newestImages(images []RuntimeImage) map[string]bool {
	best := map[string]RuntimeImage{}
	for _, img := range images {
		if img.State != "" && img.State != "Ready" {
			continue
		}
		p := runtimePlatform(img.RuntimeID)
		if b, ok := best[p]; !ok || versionLess(b.Version, img.Version) {
			best[p] = img
		}
	}
	out := map[string]bool{}
	for _, b := range best {
		out[b.ImageID] = true
	}
	return out
}

// apiLevel turns system-images;android-35;... into "35" for comparison.
func apiLevel(id string) string {
	parts := strings.Split(id, ";")
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimPrefix(parts[1], "android-")
}

// PruneArgs selects what sim_prune removes.
type PruneArgs struct {
	// IdleDays: a device is idle when its last use is older than this.
	IdleDays float64
	// DryRun lists candidates and deletes nothing.
	DryRun bool
	// Owner is who asked; required to delete, and written to the audit log.
	Owner string
	// IncludeNewest (runtimes only) also prunes the newest unused image of
	// each platform.
	IncludeNewest bool
}

// PruneCandidate is one device or image a prune selects.
type PruneCandidate struct {
	Kind       string     `json:"kind"`
	ID         string     `json:"id"`
	Name       string     `json:"name,omitempty"`
	Bytes      int64      `json:"bytes"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	Reason     string     `json:"reason"`
}

// PruneResult reports a prune.
type PruneResult struct {
	DryRun     bool             `json:"dry_run"`
	Candidates []PruneCandidate `json:"candidates"`
	Bytes      int64            `json:"bytes"`
	Deleted    []string         `json:"deleted"`
	Errors     []string         `json:"errors,omitempty"`
}

// PruneDevices removes simulators and AVDs that no one owns (no lease,
// claim or pool), that are shut down, and that have been idle longer than
// IdleDays. It never touches a booted, leased, claimed or pool device.
func (m *Manager) PruneDevices(ctx context.Context, args PruneArgs) (PruneResult, error) {
	if !args.DryRun && args.Owner == "" {
		return PruneResult{}, errors.New("sim_prune: owner is required to delete (or pass dry_run)")
	}
	if args.IdleDays < 0 {
		return PruneResult{}, errors.New("sim_prune: idle_days must be >= 0")
	}
	inv := m.Inventory(ctx)
	if len(inv.Errors) > 0 {
		return PruneResult{}, fmt.Errorf("sim_prune: inventory incomplete, nothing pruned: %s", strings.Join(inv.Errors, "; "))
	}
	cutoff := m.now().Add(-time.Duration(args.IdleDays * float64(24*time.Hour)))
	res := PruneResult{DryRun: args.DryRun, Candidates: []PruneCandidate{}, Deleted: []string{}}
	for _, d := range inv.Devices {
		if d.Owner.Kind != OwnerNone || (d.State != "shutdown" && d.State != "stopped") {
			continue
		}
		if d.LastUsedAt != nil && d.LastUsedAt.After(cutoff) {
			continue
		}
		reason := "unowned, shut down, never used"
		if d.LastUsedAt != nil {
			reason = fmt.Sprintf("unowned, shut down, idle %.0f days", m.now().Sub(*d.LastUsedAt).Hours()/24)
		}
		res.Candidates = append(res.Candidates, PruneCandidate{Kind: d.Kind, ID: d.ID, Name: d.Name, Bytes: d.Bytes, LastUsedAt: d.LastUsedAt, Reason: reason})
		res.Bytes += d.Bytes
	}
	if args.DryRun {
		return res, nil
	}
	for _, c := range res.Candidates {
		if err := m.deleteDevice(ctx, c, args.Owner); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s (%s): %v", c.ID, c.Name, err))
			continue
		}
		res.Deleted = append(res.Deleted, c.ID)
	}
	return res, nil
}

// deleteDevice deletes one prune candidate, re-checking under the lock
// that nobody leased or claimed it since the inventory was taken.
func (m *Manager) deleteDevice(ctx context.Context, c PruneCandidate, by string) error {
	k := provKey(c.ID)
	m.mu.Lock()
	if m.findLocked(c.ID) != nil {
		m.mu.Unlock()
		return errors.New("leased since the inventory was taken")
	}
	if p, ok := m.prov[k]; ok && p.Claim != nil {
		m.mu.Unlock()
		return errors.New("claimed since the inventory was taken")
	}
	m.pruning[k] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pruning, k)
		m.mu.Unlock()
	}()

	var err error
	switch c.Kind {
	case KindIOSSimulator:
		// simctl refuses to delete a booted simulator, so a boot since the
		// inventory makes this fail rather than destroy a live device.
		err = m.sim.Delete(ctx, c.ID)
	case KindAndroidAVD:
		if runningAVDs(ctx)[c.ID] {
			return errors.New("emulator started since the inventory was taken")
		}
		err = m.android.DeleteAVD(ctx, c.ID)
	default:
		err = fmt.Errorf("unknown kind %q", c.Kind)
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.prov, k)
	m.persistProvLocked()
	m.mu.Unlock()
	m.audit(by, c)
	slog.Info("simlease: pruned", "kind", c.Kind, "id", c.ID, "name", c.Name, "bytes", c.Bytes, "by", by)
	return nil
}

// PruneRuntimes removes iOS runtime images and Android system images that
// no simulator or AVD uses. The newest image of each platform is kept
// unless IncludeNewest, so simulators can still be created.
func (m *Manager) PruneRuntimes(ctx context.Context, args PruneArgs) (PruneResult, error) {
	if !args.DryRun && args.Owner == "" {
		return PruneResult{}, errors.New("sim_prune_runtimes: owner is required to delete (or pass dry_run)")
	}
	inv := m.Inventory(ctx)
	if len(inv.Errors) > 0 {
		return PruneResult{}, fmt.Errorf("sim_prune_runtimes: inventory incomplete, nothing pruned: %s", strings.Join(inv.Errors, "; "))
	}
	res := PruneResult{DryRun: args.DryRun, Candidates: []PruneCandidate{}, Deleted: []string{}}
	for _, r := range inv.Runtimes {
		if !r.Deletable || r.UsedBy.Count > 0 || (r.Keep != "" && !args.IncludeNewest) {
			continue
		}
		id := r.ID
		if r.Kind == KindIOSRuntime {
			id = r.ImageID
		}
		res.Candidates = append(res.Candidates, PruneCandidate{Kind: r.Kind, ID: id, Name: r.ID + " " + r.Version, Bytes: r.Bytes, Reason: "no simulator or AVD uses it"})
		res.Bytes += r.Bytes
	}
	if args.DryRun {
		return res, nil
	}
	for _, c := range res.Candidates {
		var err error
		switch c.Kind {
		case KindIOSRuntime:
			err = m.sim.DeleteRuntime(ctx, c.ID)
		case KindAndroidImage:
			err = m.android.DeleteSystemImage(ctx, c.ID)
		}
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", c.Name, err))
			continue
		}
		res.Deleted = append(res.Deleted, c.ID)
		m.audit(args.Owner, c)
		slog.Info("simlease: pruned runtime", "kind", c.Kind, "id", c.ID, "name", c.Name, "bytes", c.Bytes, "by", args.Owner)
	}
	return res, nil
}

// audit appends one JSON line per deletion.
func (m *Manager) audit(by string, c PruneCandidate) {
	if m.auditPath == "" {
		return
	}
	line, _ := json.Marshal(struct {
		At time.Time `json:"at"`
		By string    `json:"by"`
		PruneCandidate
	}{m.now(), by, c})
	f, err := os.OpenFile(m.auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Error("simlease: audit log", "path", m.auditPath, "error", err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}
