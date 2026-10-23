// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"strings"
	"testing"
)

// ENT-009 / 🎯T132.9: CLAUDE.md Architecture must name doctor/status CLI and app_exec.
func TestCLAUDE_ArchitectureSectionMentionsDoctorStatusAndAppExec(t *testing.T) {
	data, err := os.ReadFile("CLAUDE.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	const heading = "## Architecture"
	start := strings.Index(text, heading)
	if start < 0 {
		t.Fatal("CLAUDE.md missing ## Architecture section")
	}
	rest := text[start+len(heading):]
	nextH2 := strings.Index(rest, "\n## ")
	section := rest
	if nextH2 >= 0 {
		section = rest[:nextH2]
	}
	for _, needle := range []string{"doctor", "status", "app_exec"} {
		if !strings.Contains(section, needle) {
			t.Errorf("Architecture section missing %q", needle)
		}
	}
}
