// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package gedharness

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Diff kinds.
const (
	KindAdded   = "added"   // present in candidate, absent in golden
	KindRemoved = "removed" // present in golden, absent in candidate
	KindChanged = "changed" // present in both, values differ
)

// Diff is one residual difference after normalization. Path is a
// slash-delimited location ("" is the root). For added/removed, the
// absent side is nil.
type Diff struct {
	Path      string
	Golden    any
	Candidate any
	Kind      string
}

// Report is the outcome of a diff. Passed is true iff there are no diffs.
type Report struct {
	Diffs  []Diff
	Passed bool
}

// DiffJSON normalizes both sides with rules, then structurally diffs.
// Any residual difference becomes a Diff entry, ordered deterministically
// by path.
func DiffJSON(golden, candidate json.RawMessage, rules []Rule) (*Report, error) {
	g, err := decode(golden)
	if err != nil {
		return nil, fmt.Errorf("gedharness: decode golden: %w", err)
	}
	c, err := decode(candidate)
	if err != nil {
		return nil, fmt.Errorf("gedharness: decode candidate: %w", err)
	}
	gn := Normalize(g, rules)
	cn := Normalize(c, rules)

	var diffs []Diff
	diffValues("", gn, cn, &diffs)
	return finish(diffs), nil
}

// DiffCorpus pairs samples by (capability,label) and diffs each. A sample
// present in only one corpus is itself a diff (added/removed), keyed by
// the pair. Residual per-sample paths are prefixed with the pair so the
// report says which sample the diff belongs to.
func DiffCorpus(golden, candidate *Corpus, rules []Rule) (*Report, error) {
	gs := indexSamples(golden)
	cs := indexSamples(candidate)

	// Union of keys, deterministically ordered.
	keys := make([]sampleKey, 0, len(gs)+len(cs))
	seen := map[sampleKey]bool{}
	for k := range gs {
		if !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	for k := range cs {
		if !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].capability != keys[j].capability {
			return keys[i].capability < keys[j].capability
		}
		return keys[i].label < keys[j].label
	})

	var diffs []Diff
	for _, k := range keys {
		g, gok := gs[k]
		c, cok := cs[k]
		prefix := k.capability + ":" + k.label
		switch {
		case gok && !cok:
			diffs = append(diffs, Diff{Path: prefix, Golden: samplePreview(g), Kind: KindRemoved})
		case !gok && cok:
			diffs = append(diffs, Diff{Path: prefix, Candidate: samplePreview(c), Kind: KindAdded})
		default:
			rep, err := DiffJSON(g.Response, c.Response, rules)
			if err != nil {
				return nil, fmt.Errorf("gedharness: diff sample %s: %w", prefix, err)
			}
			for _, d := range rep.Diffs {
				d.Path = joinPath(prefix, d.Path)
				diffs = append(diffs, d)
			}
		}
	}
	return finish(diffs), nil
}

type sampleKey struct{ capability, label string }

func indexSamples(c *Corpus) map[sampleKey]Sample {
	m := make(map[sampleKey]Sample, len(c.Samples))
	for _, s := range c.Samples {
		m[sampleKey{s.Capability, s.Label}] = s
	}
	return m
}

// samplePreview decodes a sample's raw response for reporting; on failure
// it falls back to the raw string so the report is never empty.
func samplePreview(s Sample) any {
	if v, err := decode(s.Response); err == nil {
		return v
	}
	return string(s.Response)
}

func decode(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// diffValues walks two normalized values in lockstep, appending a Diff
// for every structural difference. It assumes both sides are already
// normalized (rules applied, object-arrays sorted).
func diffValues(path string, g, c any, out *[]Diff) {
	gm, gIsMap := g.(map[string]any)
	cm, cIsMap := c.(map[string]any)
	if gIsMap && cIsMap {
		diffMaps(path, gm, cm, out)
		return
	}

	gs, gIsSlice := g.([]any)
	cs, cIsSlice := c.([]any)
	if gIsSlice && cIsSlice {
		diffSlices(path, gs, cs, out)
		return
	}

	// Type mismatch or scalar comparison: DeepEqual settles it.
	if !reflect.DeepEqual(g, c) {
		*out = append(*out, Diff{Path: path, Golden: g, Candidate: c, Kind: KindChanged})
	}
}

func diffMaps(path string, g, c map[string]any, out *[]Diff) {
	keys := unionKeys(g, c)
	for _, k := range keys {
		gv, gok := g[k]
		cv, cok := c[k]
		child := joinPath(path, k)
		switch {
		case gok && !cok:
			*out = append(*out, Diff{Path: child, Golden: gv, Kind: KindRemoved})
		case !gok && cok:
			*out = append(*out, Diff{Path: child, Candidate: cv, Kind: KindAdded})
		default:
			diffValues(child, gv, cv, out)
		}
	}
}

func diffSlices(path string, g, c []any, out *[]Diff) {
	n := min(len(g), len(c))
	for i := range n {
		diffValues(indexPath(path, i), g[i], c[i], out)
	}
	// Extra golden elements are removed; extra candidate elements added.
	for i := n; i < len(g); i++ {
		*out = append(*out, Diff{Path: indexPath(path, i), Golden: g[i], Kind: KindRemoved})
	}
	for i := n; i < len(c); i++ {
		*out = append(*out, Diff{Path: indexPath(path, i), Candidate: c[i], Kind: KindAdded})
	}
}

func unionKeys(g, c map[string]any) []string {
	seen := make(map[string]bool, len(g)+len(c))
	keys := make([]string, 0, len(g)+len(c))
	for k := range g {
		seen[k] = true
		keys = append(keys, k)
	}
	for k := range c {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	if child == "" {
		return parent
	}
	return parent + "/" + child
}

func indexPath(parent string, i int) string {
	return joinPath(parent, fmt.Sprintf("[%d]", i))
}

// finish sorts diffs by path for a deterministic report and sets Passed.
func finish(diffs []Diff) *Report {
	sort.SliceStable(diffs, func(i, j int) bool {
		if diffs[i].Path != diffs[j].Path {
			return diffs[i].Path < diffs[j].Path
		}
		return diffs[i].Kind < diffs[j].Kind
	})
	return &Report{Diffs: diffs, Passed: len(diffs) == 0}
}

// String renders a report as a compact, human-readable list for CLI use.
func (r *Report) String() string {
	if r.Passed {
		return "PASS (no diffs)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FAIL (%d diff(s)):\n", len(r.Diffs))
	for _, d := range r.Diffs {
		switch d.Kind {
		case KindAdded:
			fmt.Fprintf(&b, "  + %s: %s\n", d.Path, compact(d.Candidate))
		case KindRemoved:
			fmt.Fprintf(&b, "  - %s: %s\n", d.Path, compact(d.Golden))
		default:
			fmt.Fprintf(&b, "  ~ %s: %s -> %s\n", d.Path, compact(d.Golden), compact(d.Candidate))
		}
	}
	return b.String()
}

// compact renders a value as one-line JSON for a diff line.
func compact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
