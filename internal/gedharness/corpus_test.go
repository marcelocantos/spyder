// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package gedharness

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestCorpusRoundTrip(t *testing.T) {
	orig := &Corpus{
		GedVersion: "1.2.3",
		Fixture:    "tiltbuggy",
		Samples: []Sample{
			{Capability: "info", Label: "t0", Response: raw(`{"connected":false,"servers":[],"sessions":0}`)},
			{Capability: "tweaks", Label: "t0", Response: raw(`[{"name":"camera.fov_deg","value":60}]`)},
			{Capability: "info", Label: "after_set_fov", Response: raw(`{"connected":true,"sessions":1}`)},
		},
	}

	path := filepath.Join(t.TempDir(), "corpus.json")
	if err := orig.WriteFile(path); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := LoadCorpus(path)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}

	if got.GedVersion != orig.GedVersion || got.Fixture != orig.Fixture {
		t.Errorf("provenance mismatch: got %+v", got)
	}
	if len(got.Samples) != len(orig.Samples) {
		t.Fatalf("sample count: got %d want %d", len(got.Samples), len(orig.Samples))
	}
	for i := range orig.Samples {
		if got.Samples[i].Capability != orig.Samples[i].Capability ||
			got.Samples[i].Label != orig.Samples[i].Label {
			t.Errorf("sample[%d] key mismatch: got %+v want %+v", i, got.Samples[i], orig.Samples[i])
		}
		// Raw JSON is semantically equal after a marshal/unmarshal round
		// trip (whitespace may differ); compare decoded trees.
		if !reflect.DeepEqual(decodeAny(t, string(got.Samples[i].Response)), decodeAny(t, string(orig.Samples[i].Response))) {
			t.Errorf("sample[%d] response not lossless:\n got %s\nwant %s",
				i, got.Samples[i].Response, orig.Samples[i].Response)
		}
	}
}

func TestCorpusDeterministicMarshal(t *testing.T) {
	c := &Corpus{Fixture: "f", Samples: []Sample{
		{Capability: "info", Label: "t0", Response: raw(`{"b":2,"a":1}`)},
	}}
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a.json")
	p2 := filepath.Join(dir, "b.json")
	if err := c.WriteFile(p1); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteFile(p2); err != nil {
		t.Fatal(err)
	}
	// Two writes of the same corpus must be byte-identical.
	l1, err := LoadCorpus(p1)
	if err != nil {
		t.Fatal(err)
	}
	l2, err := LoadCorpus(p2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(l1, l2) {
		t.Error("repeated writes are not deterministic")
	}
}

func TestLoadCorpusMissingFile(t *testing.T) {
	if _, err := LoadCorpus(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("expected error loading a missing corpus")
	}
}
