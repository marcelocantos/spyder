// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package gedharness

import (
	"encoding/json"
	"sort"
	"strings"
)

// RuleAction selects what Normalize does to a value at a matched path.
type RuleAction int

const (
	// Drop removes the matched key from its parent object (arrays keep
	// their length; only object members can be dropped).
	Drop RuleAction = iota
	// Zero replaces the matched value with a stable zero for its JSON
	// type: 0 for numbers, "" for strings, false for booleans, [] for
	// arrays, {} for objects, nil for null. Zeroing (not dropping) keeps
	// the field present so a *missing* field still diffs.
	Zero
)

// Rule matches a JSON location by a slash-delimited path and applies an
// Action there. An interior "*" matches any single array index or object
// key, so "servers/*/pid" hits the pid of every element of servers. A
// LEADING "*" matches at any depth, so "*/timestamp" hits a "timestamp"
// key wherever it appears — at the root or nested under any parent.
type Rule struct {
	Path   string
	Action RuleAction
}

// segments splits a rule path into its components, tolerating a leading
// slash. An empty path yields no segments (matches nothing useful).
func (r Rule) segments() []string {
	p := strings.Trim(r.Path, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// DefaultGedRules returns the known non-deterministic fields of ged's
// HTTP surface. /api/info is {connected, servers[{id,name,pid,sessions}],
// sessions}: pids, per-server session counts, server IDs, and the total
// session count all vary run-to-run, so Zero them (keep the shape).
// timestamp/ts fields (present on some sideband-derived payloads) are
// dropped outright since their value carries no comparable structure.
func DefaultGedRules() []Rule {
	return []Rule{
		{Path: "servers/*/pid", Action: Zero},
		{Path: "servers/*/id", Action: Zero},
		{Path: "servers/*/sessions", Action: Zero},
		{Path: "sessions", Action: Zero},
		{Path: "*/timestamp", Action: Drop},
		{Path: "*/ts", Action: Drop},
	}
}

// sortKeys are the object fields, in preference order, used to order an
// array of objects deterministically. ged identifies servers by "id" and
// tweaks by "name", so those give a stable order even when the source
// array order is not stable.
var sortKeys = []string{"id", "name", "key", "label"}

// Normalize walks the decoded JSON value v, applies rules, and sorts
// arrays of objects by a stable key so ordering differences don't diff.
// It is pure: v is not mutated; a normalized copy is returned. v should
// be the result of json.Unmarshal into `any` (maps, slices, scalars).
func Normalize(v any, rules []Rule) any {
	ruleSegs := make([][]string, 0, len(rules))
	ruleActs := make([]RuleAction, 0, len(rules))
	for _, r := range rules {
		if segs := r.segments(); len(segs) > 0 {
			ruleSegs = append(ruleSegs, segs)
			ruleActs = append(ruleActs, r.Action)
		}
	}
	return normalize(v, nil, ruleSegs, ruleActs)
}

// normalize recursively rebuilds v. path is the current location as a
// list of keys/indices used to test rules. Zero actions are applied to
// the value at a matched path; Drop actions are applied by the *parent*
// object (it skips a child whose path matches a Drop rule).
func normalize(v any, path []string, ruleSegs [][]string, ruleActs []RuleAction) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			// extend, not append: a fresh backing array per child so
			// sibling paths can't clobber each other's segments.
			childPath := extend(path, k)
			if act, ok := matchAction(childPath, ruleSegs, ruleActs); ok && act == Drop {
				continue // parent drops this member
			}
			out[k] = normalize(child, childPath, ruleSegs, ruleActs)
		}
		return out
	case []any:
		// Sort BEFORE normalizing: identity fields used for ordering
		// (e.g. "id") may themselves be zeroed by a rule, which would
		// destroy the ordering. Compute the order from the raw children,
		// then normalize each in that stable order.
		ordered := sortedCopy(t)
		out := make([]any, len(ordered))
		for i, child := range ordered {
			// "*" matches any index; use a literal "*" as the path
			// segment so index-specific rules aren't accidentally hit.
			out[i] = normalize(child, extend(path, "*"), ruleSegs, ruleActs)
		}
		return out
	default:
		// Scalars (and null): Zero if this exact path matches a Zero rule.
		if act, ok := matchAction(path, ruleSegs, ruleActs); ok && act == Zero {
			return zeroFor(v)
		}
		return v
	}
}

// extend returns path with seg appended onto a fresh backing array, so
// concurrent sibling recursions never share (and corrupt) segments.
func extend(path []string, seg string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = seg
	return out
}

// matchAction reports whether path matches any rule and returns that
// rule's action. When several rules match, the first in ruleSegs wins;
// callers order rules so the intended one comes first.
func matchAction(path []string, ruleSegs [][]string, ruleActs []RuleAction) (RuleAction, bool) {
	for i, segs := range ruleSegs {
		if pathMatches(path, segs) {
			return ruleActs[i], true
		}
	}
	return 0, false
}

// pathMatches reports whether the concrete path matches the rule
// segments. An interior "*" matches exactly one path element (an array
// index — recorded as "*" — or an object key). A LEADING "*" matches any
// depth (zero or more leading elements), so "*/timestamp" hits a
// "timestamp" key whether it sits at the root or nested under any parent.
// This makes the "match a field anywhere" idiom work without enumerating
// every depth.
func pathMatches(path, segs []string) bool {
	if len(segs) == 0 {
		return false
	}
	if segs[0] == "*" {
		// Leading wildcard: the remaining segments must match a suffix of
		// path (any depth of leading elements is absorbed by the "*").
		tail := segs[1:]
		if len(tail) == 0 {
			return true // "*" alone matches anything
		}
		if len(path) < len(tail) {
			return false
		}
		return matchExact(path[len(path)-len(tail):], tail)
	}
	if len(path) != len(segs) {
		return false
	}
	return matchExact(path, segs)
}

// matchExact reports whether path equals segs positionally, with "*"
// segments matching any single element.
func matchExact(path, segs []string) bool {
	for i := range segs {
		if segs[i] == "*" {
			continue
		}
		if segs[i] != path[i] {
			return false
		}
	}
	return true
}

// zeroFor returns the stable zero value for the JSON type of v, keeping
// the field present but valueless. json numbers decode to float64.
func zeroFor(v any) any {
	switch v.(type) {
	case float64:
		return float64(0)
	case json.Number:
		return json.Number("0")
	case string:
		return ""
	case bool:
		return false
	case []any:
		return []any{}
	case map[string]any:
		return map[string]any{}
	default:
		// null and unknown scalar types normalize to null.
		return nil
	}
}

// sortedCopy returns a new slice ordered by a stable object key (see
// sortKeys) when every element is an object sharing that key. Mixed or
// key-less slices are returned in source order — reordering scalars would
// change meaning, and arrays without a stable identity field can't be
// reordered safely. The input slice is not mutated (purity).
func sortedCopy(s []any) []any {
	out := make([]any, len(s))
	copy(out, s)
	if len(out) < 2 {
		return out
	}
	key := ""
	for _, cand := range sortKeys {
		if allObjectsHaveStringKey(out, cand) {
			key = cand
			break
		}
	}
	if key == "" {
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].(map[string]any)[key].(string) < out[j].(map[string]any)[key].(string)
	})
	return out
}

// allObjectsHaveStringKey reports whether every element of s is an object
// carrying key with a string value.
func allObjectsHaveStringKey(s []any, key string) bool {
	for _, e := range s {
		m, ok := e.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := m[key].(string); !ok {
			return false
		}
	}
	return true
}
