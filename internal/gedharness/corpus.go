// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package gedharness

import (
	"encoding/json"
	"fmt"
	"os"
)

// Sample is one recorded ged response at one waypoint. Capability names
// the endpoint ("info" | "tweaks" | "logs"); Label is a waypoint marker
// (e.g. "t0", "after_set_fov") so a corpus can hold a sequence.
type Sample struct {
	Capability string          `json:"capability"`
	Label      string          `json:"label"`
	Response   json.RawMessage `json:"response"`
}

// Corpus is a golden recording: a flat, single-file list of samples plus
// optional provenance. It is deliberately not a directory framework — one
// JSON file is the whole artifact.
type Corpus struct {
	GedVersion string   `json:"ged_version,omitempty"`
	Fixture    string   `json:"fixture,omitempty"`
	Samples    []Sample `json:"samples"`
}

// corpusFileMode is 0644: a corpus is a checked-in-style artifact, not a
// secret, so it stays world-readable.
const corpusFileMode = 0o644

// WriteFile marshals the corpus with stable, indented formatting and
// writes it to path. Indentation keeps the artifact diff-friendly; the
// sample order is preserved as-is (the recorder controls it).
func (c *Corpus) WriteFile(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("gedharness: marshal corpus: %w", err)
	}
	// Trailing newline: keeps the file POSIX-clean and diff-tool-friendly.
	data = append(data, '\n')
	if err := os.WriteFile(path, data, corpusFileMode); err != nil {
		return fmt.Errorf("gedharness: write corpus %s: %w", path, err)
	}
	return nil
}

// LoadCorpus reads and decodes a corpus file written by WriteFile.
func LoadCorpus(path string) (*Corpus, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("gedharness: read corpus %s: %w", path, err)
	}
	var c Corpus
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("gedharness: parse corpus %s: %w", path, err)
	}
	return &c, nil
}
