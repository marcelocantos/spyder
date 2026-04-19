// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package baselines manages the visual-regression baseline store under
// ~/.spyder/baselines/<suite>/<variant>/<case>.{png,manifest.json}.
//
// A variant encodes per-device and per-orientation context as a
// URL-safe string (e.g. "pippa-landscape"). Callers construct the
// variant key however makes sense for their test suite; the store is
// opaque to the key's content — it just uses it as a path component.
// For v1 the convention is "<device-alias>-<orientation>"; variants
// whose parts are unknown should pass "default".
//
// All writes are atomic (write-to-temp then rename) so a crash during
// update never leaves a partially-written baseline.
package baselines

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Store is the top-level baseline-store handle rooted at baseDir.
// Safe for concurrent use within one process.
type Store struct {
	baseDir string
}

// New opens a Store rooted at baseDir. Creates the dir on demand;
// doesn't fail if it doesn't exist yet.
func New(baseDir string) (*Store, error) {
	if baseDir == "" {
		return nil, errors.New("baselines: baseDir is required")
	}
	return &Store{baseDir: baseDir}, nil
}

// BaseDir returns the store's root directory.
func (s *Store) BaseDir() string { return s.baseDir }

// Baseline is the in-memory view of a stored baseline entry.
type Baseline struct {
	Suite    string   `json:"suite"`
	Case     string   `json:"case"`
	Variant  string   `json:"variant"`
	PNG      []byte   `json:"-"` // loaded on demand via Get
	Manifest Manifest `json:"manifest,omitempty"`
}

// Manifest is the optional UI-element manifest stored alongside a
// baseline PNG. Its schema is the canonical spyder visual-diff manifest
// format: a flat list of UI elements with bounding boxes and
// platform-agnostic identifiers.
//
// Schema (v1):
//
//	{
//	  "schema_version": 1,
//	  "elements": [
//	    {
//	      "id":    "com.example.app/MainScreen/loginButton",
//	      "kind":  "button",
//	      "bbox":  [x, y, width, height],
//	      "attrs": { "label": "Log In", "enabled": true }
//	    }
//	  ]
//	}
//
// Fields:
//   - id:    Opaque stable identifier. Convention: <bundle>/<screen>/<name>.
//     Must be unique within one manifest; used as the join key for structural
//     diffing between two manifests.
//   - kind:  Semantic element type (button, label, image, textfield, …).
//   - bbox:  [x, y, width, height] in logical pixels, origin top-left.
//   - attrs: Free-form map of observable attributes (text, accessibility
//     label, enabled state, …). Diffed by key equality; unknown keys are
//     preserved verbatim so new platforms can add fields without schema churn.
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	Elements      []Element `json:"elements,omitempty"`
}

// Element is one UI element captured in a Manifest.
type Element struct {
	// ID is a stable unique key for this element within its screen.
	// Convention: "<bundle>/<screen>/<name>".
	ID string `json:"id"`

	// Kind is a semantic type tag: button, label, image, textfield,
	// container, switch, slider, nav-bar, tab-bar, cell, …
	Kind string `json:"kind"`

	// BBox is [x, y, width, height] in logical pixels, top-left origin.
	BBox [4]int `json:"bbox"`

	// Attrs is a platform-agnostic attribute bag (text, enabled, …).
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Put writes a baseline PNG (and optionally a manifest) into the store.
// If manifest is nil, any existing .manifest.json for the same key is
// left intact. If manifest is non-nil it is written atomically.
func (s *Store) Put(suite, caseName, variant string, png []byte, manifest *Manifest) error {
	if err := validateKey(suite, caseName, variant); err != nil {
		return err
	}
	if len(png) == 0 {
		return errors.New("baselines: PNG data is required")
	}
	dir := s.variantDir(suite, variant)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("baselines: mkdir %s: %w", dir, err)
	}
	pngPath := filepath.Join(dir, caseName+".png")
	if err := atomicWrite(pngPath, png, 0o600); err != nil {
		return fmt.Errorf("baselines: write PNG: %w", err)
	}
	if manifest != nil {
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return fmt.Errorf("baselines: marshal manifest: %w", err)
		}
		mpath := filepath.Join(dir, caseName+".manifest.json")
		if err := atomicWrite(mpath, data, 0o600); err != nil {
			return fmt.Errorf("baselines: write manifest: %w", err)
		}
	}
	return nil
}

// Get returns the stored PNG and optional manifest for (suite, case,
// variant). Returns os.ErrNotExist if no baseline is found.
func (s *Store) Get(suite, caseName, variant string) (*Baseline, error) {
	if err := validateKey(suite, caseName, variant); err != nil {
		return nil, err
	}
	dir := s.variantDir(suite, variant)
	pngPath := filepath.Join(dir, caseName+".png")
	png, err := os.ReadFile(pngPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("baselines %s/%s/%s: %w", suite, variant, caseName, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("baselines: read PNG: %w", err)
	}
	b := &Baseline{
		Suite:   suite,
		Case:    caseName,
		Variant: variant,
		PNG:     png,
	}
	mpath := filepath.Join(dir, caseName+".manifest.json")
	mdata, err := os.ReadFile(mpath)
	if err == nil {
		if jerr := json.Unmarshal(mdata, &b.Manifest); jerr != nil {
			return nil, fmt.Errorf("baselines: parse manifest: %w", jerr)
		}
	}
	// Missing manifest is OK — not every baseline has one.
	return b, nil
}

// ListEntry is one item returned by List.
type ListEntry struct {
	Case    string `json:"case"`
	Variant string `json:"variant"`
	HasPNG  bool   `json:"has_png"`
	HasMani bool   `json:"has_manifest"`
}

// List enumerates all baseline entries for suite. The store may contain
// entries for multiple variants; List returns all of them. Entries are
// in filesystem order (not sorted). Missing suite dir is treated as
// empty, not an error.
func (s *Store) List(suite string) ([]ListEntry, error) {
	if suite == "" {
		return nil, errors.New("baselines: suite is required")
	}
	suiteDir := filepath.Join(s.baseDir, sanitize(suite))
	variants, err := os.ReadDir(suiteDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("baselines: list %s: %w", suite, err)
	}
	var out []ListEntry
	for _, v := range variants {
		if !v.IsDir() {
			continue
		}
		variant := v.Name()
		files, err := os.ReadDir(filepath.Join(suiteDir, variant))
		if err != nil {
			continue
		}
		// Build a map of case → {hasPNG, hasManifest}.
		type flags struct{ png, mani bool }
		caseMap := map[string]*flags{}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			switch {
			case strings.HasSuffix(name, ".manifest.json"):
				c := strings.TrimSuffix(name, ".manifest.json")
				if _, ok := caseMap[c]; !ok {
					caseMap[c] = &flags{}
				}
				caseMap[c].mani = true
			case strings.HasSuffix(name, ".png"):
				c := strings.TrimSuffix(name, ".png")
				if _, ok := caseMap[c]; !ok {
					caseMap[c] = &flags{}
				}
				caseMap[c].png = true
			}
		}
		for c, f := range caseMap {
			out = append(out, ListEntry{
				Case:    c,
				Variant: variant,
				HasPNG:  f.png,
				HasMani: f.mani,
			})
		}
	}
	return out, nil
}

// --- internals -------------------------------------------------------

func (s *Store) variantDir(suite, variant string) string {
	return filepath.Join(s.baseDir, sanitize(suite), sanitize(variant))
}

// sanitize converts a suite/case/variant key component into a safe
// filename segment. Slashes, dots-at-start, and whitespace are replaced.
func sanitize(s string) string {
	s = strings.TrimSpace(s)
	r := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		"..", "__",
	)
	return r.Replace(s)
}

// validateKey checks that suite, caseName, and variant are all non-empty
// and don't contain path-traversal sequences.
func validateKey(suite, caseName, variant string) error {
	for label, v := range map[string]string{
		"suite":   suite,
		"case":    caseName,
		"variant": variant,
	} {
		if v == "" {
			return fmt.Errorf("baselines: %s is required", label)
		}
		if strings.Contains(v, "..") {
			return fmt.Errorf("baselines: %s must not contain '..': %q", label, v)
		}
	}
	return nil
}

// atomicWrite stages via a sibling .tmp file then renames.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
