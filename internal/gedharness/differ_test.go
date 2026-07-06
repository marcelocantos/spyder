// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package gedharness

import (
	"encoding/json"
	"testing"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestDiffJSONIdentical(t *testing.T) {
	a := raw(`{"connected":false,"servers":[],"sessions":0}`)
	rep, err := DiffJSON(a, a, DefaultGedRules())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed {
		t.Errorf("identical inputs should pass, got %s", rep.String())
	}
}

func TestDiffJSONChangedField(t *testing.T) {
	golden := raw(`{"connected":false,"servers":[],"sessions":0}`)
	cand := raw(`{"connected":true,"servers":[],"sessions":0}`)
	rep, err := DiffJSON(golden, cand, DefaultGedRules())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed {
		t.Fatal("expected a diff on connected")
	}
	if len(rep.Diffs) != 1 {
		t.Fatalf("want exactly 1 diff, got %d: %s", len(rep.Diffs), rep.String())
	}
	d := rep.Diffs[0]
	if d.Path != "connected" || d.Kind != KindChanged {
		t.Errorf("want changed at connected, got %+v", d)
	}
}

func TestDiffJSONAddedAndRemovedKey(t *testing.T) {
	golden := raw(`{"a":1,"only_golden":true}`)
	cand := raw(`{"a":1,"only_candidate":true}`)
	rep, err := DiffJSON(golden, cand, nil)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, d := range rep.Diffs {
		kinds[d.Path] = d.Kind
	}
	if kinds["only_golden"] != KindRemoved {
		t.Errorf("only_golden should be removed, got %q", kinds["only_golden"])
	}
	if kinds["only_candidate"] != KindAdded {
		t.Errorf("only_candidate should be added, got %q", kinds["only_candidate"])
	}
}

// TestDiffJSONNormalizedFieldNoSpuriousDiff is the load-bearing property:
// a field that differs ONLY in a normalized position (pid, session count,
// server ordering, timestamp) produces NO diff.
func TestDiffJSONNormalizedFieldNoSpuriousDiff(t *testing.T) {
	golden := raw(`{"connected":true,"servers":[{"id":"s1","name":"g","pid":100,"sessions":1},{"id":"s2","name":"h","pid":200,"sessions":3}],"sessions":4,"timestamp":"2026-01-01"}`)
	// Same structure: different pids, different session counts, servers in
	// reversed order, different total sessions, different timestamp, and a
	// different server id. All of these are normalized.
	cand := raw(`{"connected":true,"servers":[{"id":"z2","name":"h","pid":999,"sessions":42},{"id":"z1","name":"g","pid":888,"sessions":7}],"sessions":49,"timestamp":"2026-12-31"}`)
	rep, err := DiffJSON(golden, cand, DefaultGedRules())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed {
		t.Errorf("normalization should prevent spurious diffs, got %s", rep.String())
	}
}

// TestDiffJSONRealNonNormalizedFieldStillDiffs proves normalization
// doesn't mask a genuine difference sitting next to normalized ones: same
// setup as above but with a real change to a server name.
func TestDiffJSONRealNonNormalizedFieldStillDiffs(t *testing.T) {
	golden := raw(`{"servers":[{"id":"s1","name":"game","pid":100}]}`)
	cand := raw(`{"servers":[{"id":"z9","name":"OTHER","pid":999}]}`)
	rep, err := DiffJSON(golden, cand, DefaultGedRules())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed {
		t.Fatal("a real name change must diff despite pid/id normalization")
	}
	if len(rep.Diffs) != 1 || rep.Diffs[0].Kind != KindChanged {
		t.Fatalf("want one changed diff on name, got %s", rep.String())
	}
}

func TestDiffCorpusIdentical(t *testing.T) {
	c := &Corpus{Samples: []Sample{
		{Capability: "info", Label: "t0", Response: raw(`{"connected":false,"sessions":0}`)},
		{Capability: "tweaks", Label: "t0", Response: raw(`[]`)},
	}}
	rep, err := DiffCorpus(c, c, DefaultGedRules())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed {
		t.Errorf("identical corpora should pass, got %s", rep.String())
	}
}

func TestDiffCorpusChangedSample(t *testing.T) {
	golden := &Corpus{Samples: []Sample{
		{Capability: "info", Label: "t0", Response: raw(`{"connected":false}`)},
	}}
	cand := &Corpus{Samples: []Sample{
		{Capability: "info", Label: "t0", Response: raw(`{"connected":true}`)},
	}}
	rep, err := DiffCorpus(golden, cand, DefaultGedRules())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed || len(rep.Diffs) != 1 {
		t.Fatalf("want one diff, got %s", rep.String())
	}
	// Path is prefixed with capability:label.
	if got := rep.Diffs[0].Path; got != "info:t0/connected" {
		t.Errorf("want path info:t0/connected, got %q", got)
	}
}

func TestDiffCorpusMissingAndExtraSample(t *testing.T) {
	golden := &Corpus{Samples: []Sample{
		{Capability: "info", Label: "t0", Response: raw(`{}`)},
		{Capability: "tweaks", Label: "t0", Response: raw(`[]`)},
	}}
	cand := &Corpus{Samples: []Sample{
		{Capability: "info", Label: "t0", Response: raw(`{}`)},
		{Capability: "logs", Label: "t0", Response: raw(`[]`)},
	}}
	rep, err := DiffCorpus(golden, cand, DefaultGedRules())
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, d := range rep.Diffs {
		kinds[d.Path] = d.Kind
	}
	if kinds["tweaks:t0"] != KindRemoved {
		t.Errorf("tweaks sample only in golden should be removed, got %q", kinds["tweaks:t0"])
	}
	if kinds["logs:t0"] != KindAdded {
		t.Errorf("logs sample only in candidate should be added, got %q", kinds["logs:t0"])
	}
}
