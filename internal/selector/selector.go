// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package selector defines the fuzzy-device-selection predicate and
// resolver used by the reserve tool. A Selector describes a class of
// device rather than a concrete alias or UUID; the resolver picks the
// best available candidate from the live device set + inventory.
//
// Resolution preference order (from the accept criteria of 🎯T23):
//  1. Idle physical device that matches all predicate fields.
//  2. Idle pre-warmed sim/emu (hook point via PoolResolver; nil = skip).
//  3. Error with structured near-miss detail.
package selector

import (
	"fmt"
	"strings"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/inventory"
)

// Selector is a structured predicate over device attributes. All
// non-zero/non-empty fields must match; zero/empty fields are wildcards.
type Selector struct {
	// Platform restricts to "ios" or "android". Required.
	Platform string `json:"platform"`

	// ModelFamily is matched case-insensitively against the device Model
	// field (device.Info.Model) and against Tags on the inventory entry.
	// Examples: "ipad", "iphone", "phone", "tablet".
	ModelFamily string `json:"model_family,omitempty"`

	// OSMin and OSMax bound the OS version string (inclusive on both ends).
	// Comparison is lexicographic on the version string after normalisation
	// (leading zeros stripped per-segment). A missing bound means no limit.
	OSMin string `json:"os_min,omitempty"`
	OSMax string `json:"os_max,omitempty"`

	// OrientationCapable requires that the device supports screen rotation
	// (i.e. is a simulator or emulator). Physical devices never satisfy this
	// predicate because rotation on physical hardware is a sensor, not a
	// software-controllable feature.
	OrientationCapable bool `json:"orientation_capable,omitempty"`

	// Tags lists labels that must all be present on the inventory entry's
	// Tags slice (set-subset match).
	Tags []string `json:"tags,omitempty"`

	// Attrs lists key/value pairs that must all be present in the inventory
	// entry's Attrs map (exact-match per key).
	Attrs map[string]string `json:"attrs,omitempty"`
}

// String returns a compact human-readable summary of the selector for
// error messages.
func (s Selector) String() string {
	parts := []string{"platform=" + s.Platform}
	if s.ModelFamily != "" {
		parts = append(parts, "model_family="+s.ModelFamily)
	}
	if s.OSMin != "" {
		parts = append(parts, "os_min="+s.OSMin)
	}
	if s.OSMax != "" {
		parts = append(parts, "os_max="+s.OSMax)
	}
	if s.OrientationCapable {
		parts = append(parts, "orientation_capable=true")
	}
	for _, tag := range s.Tags {
		parts = append(parts, "tag="+tag)
	}
	for k, v := range s.Attrs {
		parts = append(parts, k+"="+v)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// Candidate pairs a live device.Info with its inventory.Entry (if any).
// Physical devices found via the adapters are combined with their
// inventory entry; sims/emus that are not in the inventory have a zero Entry.
type Candidate struct {
	Info       device.Info
	Entry      inventory.Entry
	IsSimOrEmu bool // true when UUID looks like a sim UDID or serial starts with "emulator-"
	IsReserved bool // true when a live reservation holds this device
}

// PoolResolver is an optional extension point for 🎯T24 (sim/emu pool).
// When non-nil and no physical candidate matches, the resolver asks the
// pool to find or mint a suitable sim/emu. Returning ("", nil) means the
// pool has nothing; returning ("", err) means a hard pool error.
type PoolResolver interface {
	Resolve(sel Selector) (uuid string, err error)
}

// NearMiss describes a candidate that matched all but one predicate field.
type NearMiss struct {
	UUID            string // device UUID or serial
	Alias           string // inventory alias (empty if unknown)
	FailedPredicate string // human-readable description of the failed predicate
}

// NoMatchError is returned when the selector cannot be satisfied.
type NoMatchError struct {
	Selector   Selector
	NearMisses []NearMiss // up to 3, one failed predicate each
}

func (e *NoMatchError) Error() string {
	msg := fmt.Sprintf("no device matches selector %s", e.Selector)
	if len(e.NearMisses) == 0 {
		msg += " (no candidates evaluated)"
		return msg
	}
	msg += "; closest near-misses:"
	for _, nm := range e.NearMisses {
		id := nm.UUID
		if nm.Alias != "" {
			id = nm.Alias + " (" + nm.UUID + ")"
		}
		msg += fmt.Sprintf("\n  %s: failed predicate %q", id, nm.FailedPredicate)
	}
	return msg
}

// Resolve selects the best candidate from candidates that matches sel.
// The returned device.Info is bound to a concrete UUID; the caller should
// pass that UUID directly to the reservation store.
//
// If pool is non-nil and no physical candidate matches, it is consulted
// for a sim/emu candidate. If pool is nil, that step is skipped and
// resolution fails with a NoMatchError.
//
// Prefer: idle physical → idle sim/emu from pool → error.
func Resolve(sel Selector, candidates []Candidate, pool PoolResolver) (device.Info, error) {
	if sel.Platform == "" {
		return device.Info{}, fmt.Errorf("selector: platform is required")
	}

	type scored struct {
		c     Candidate
		score int // number of predicates matched (lower first in near-miss)
	}
	total := countPredicates(sel)

	var matches []Candidate
	var nearMisses []scored

	for _, c := range candidates {
		matched, failedPred := matchCandidate(sel, c)
		if matched {
			if !c.IsReserved {
				matches = append(matches, c)
			}
		} else {
			// Count how many predicates this candidate passed.
			s := total - 1 // near-miss = passed N-1
			if failedPred != "" {
				nearMisses = append(nearMisses, scored{c: c, score: s})
			}
		}
	}

	// Prefer physical over sim/emu.
	var physicalMatches, simMatches []Candidate
	for _, c := range matches {
		if c.IsSimOrEmu {
			simMatches = append(simMatches, c)
		} else {
			physicalMatches = append(physicalMatches, c)
		}
	}

	if len(physicalMatches) > 0 {
		return physicalMatches[0].Info, nil
	}
	if len(simMatches) > 0 {
		return simMatches[0].Info, nil
	}

	// Ask the pool (hook for 🎯T24).
	if pool != nil {
		uuid, err := pool.Resolve(sel)
		if err != nil {
			return device.Info{}, fmt.Errorf("pool resolver: %w", err)
		}
		if uuid != "" {
			return device.Info{
				UUID:     uuid,
				Platform: sel.Platform,
			}, nil
		}
	}

	// Build near-miss report (top 3).
	var nms []NearMiss
	for i, ns := range nearMisses {
		if i >= 3 {
			break
		}
		_, failedPred := matchCandidate(sel, ns.c)
		nms = append(nms, NearMiss{
			UUID:            ns.c.Info.UUID,
			Alias:           ns.c.Entry.Alias,
			FailedPredicate: failedPred,
		})
	}
	return device.Info{}, &NoMatchError{Selector: sel, NearMisses: nms}
}

// matchCandidate returns (true, "") if the candidate satisfies all
// selector predicates, or (false, failedPredicate) for the first
// predicate that fails. failedPredicate is a human-readable description.
func matchCandidate(sel Selector, c Candidate) (bool, string) {
	// Platform.
	if !strings.EqualFold(c.Info.Platform, sel.Platform) {
		return false, "platform=" + sel.Platform
	}

	// ModelFamily: match against device.Info.Model and inventory tags.
	if sel.ModelFamily != "" {
		if !modelFamilyMatches(sel.ModelFamily, c.Info.Model, c.Entry.Tags) {
			return false, "model_family=" + sel.ModelFamily
		}
	}

	// OSMin / OSMax.
	if sel.OSMin != "" && c.Info.OS != "" {
		if compareVersion(c.Info.OS, sel.OSMin) < 0 {
			return false, "os_min=" + sel.OSMin
		}
	}
	if sel.OSMax != "" && c.Info.OS != "" {
		if compareVersion(c.Info.OS, sel.OSMax) > 0 {
			return false, "os_max=" + sel.OSMax
		}
	}

	// OrientationCapable.
	if sel.OrientationCapable && !c.IsSimOrEmu {
		return false, "orientation_capable=true"
	}

	// Tags (all must be present in inventory entry tags).
	for _, tag := range sel.Tags {
		if !containsTagCI(c.Entry.Tags, tag) {
			return false, "tag=" + tag
		}
	}

	// Attrs (exact key/value match in inventory entry attrs).
	for k, v := range sel.Attrs {
		got, ok := c.Entry.Attrs[k]
		if !ok || got != v {
			return false, "attr " + k + "=" + v
		}
	}

	return true, ""
}

// modelFamilyMatches returns true when family appears (case-insensitive)
// in model or in any of the tags.
func modelFamilyMatches(family, model string, tags []string) bool {
	fLower := strings.ToLower(family)
	if strings.Contains(strings.ToLower(model), fLower) {
		return true
	}
	return containsTagCI(tags, family)
}

// containsTagCI reports whether tags contains target (case-insensitive).
func containsTagCI(tags []string, target string) bool {
	tLower := strings.ToLower(target)
	for _, t := range tags {
		if strings.ToLower(t) == tLower {
			return true
		}
	}
	return false
}

// countPredicates counts the number of active (non-zero) predicate
// fields in sel. Used to compute near-miss scores.
func countPredicates(sel Selector) int {
	n := 1 // platform is always active
	if sel.ModelFamily != "" {
		n++
	}
	if sel.OSMin != "" {
		n++
	}
	if sel.OSMax != "" {
		n++
	}
	if sel.OrientationCapable {
		n++
	}
	n += len(sel.Tags)
	n += len(sel.Attrs)
	return n
}

// compareVersion compares two version strings by segment (split on ".").
// Each segment is compared numerically after stripping leading zeros.
// Trailing ".0" segments are treated as absent (so "1.0" == "1" and
// "34.0" == "34"). Returns -1, 0, or 1.
func compareVersion(a, b string) int {
	aParts := trimTrailingZeros(strings.Split(a, "."))
	bParts := trimTrailingZeros(strings.Split(b, "."))
	maxLen := len(aParts)
	if len(bParts) > maxLen {
		maxLen = len(bParts)
	}
	for i := range maxLen {
		var av, bv string
		if i < len(aParts) {
			av = strings.TrimLeft(aParts[i], "0")
			if av == "" {
				av = "0"
			}
		} else {
			av = "0"
		}
		if i < len(bParts) {
			bv = strings.TrimLeft(bParts[i], "0")
			if bv == "" {
				bv = "0"
			}
		} else {
			bv = "0"
		}
		if len(av) != len(bv) {
			if len(av) < len(bv) {
				return -1
			}
			return 1
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

// trimTrailingZeros removes trailing "0" segments from a version parts slice.
func trimTrailingZeros(parts []string) []string {
	i := len(parts)
	for i > 1 {
		seg := strings.TrimLeft(parts[i-1], "0")
		if seg != "" {
			break
		}
		i--
	}
	return parts[:i]
}
