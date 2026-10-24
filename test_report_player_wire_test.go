// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// ENT-010 / 🎯T132.10: test-report must record player-wire (ENT-006 wire oracle).
func TestTestReportScriptRecordsPlayerWire(t *testing.T) {
	script, err := os.ReadFile("scripts/test-report.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	if !strings.Contains(text, "run_suite player-wire") {
		t.Fatal("scripts/test-report.sh missing run_suite player-wire")
	}
	if !strings.Contains(text, "make check-wire") {
		t.Error("scripts/test-report.sh player-wire suite should invoke make check-wire")
	}

	report, err := os.ReadFile("TEST-REPORT.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Suites []struct {
			Name string `json:"name"`
		} `json:"suites"`
	}
	if err := json.Unmarshal(report, &doc); err != nil {
		t.Fatal(err)
	}
	for _, s := range doc.Suites {
		if s.Name == "player-wire" {
			return
		}
	}
	t.Fatal("TEST-REPORT.json missing player-wire suite")
}
