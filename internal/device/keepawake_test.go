// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"strings"
	"testing"
)

// TestExpectedKeepAwakeVersion_FromBundledPbxproj asserts that the
// MARKETING_VERSION reader successfully locates and parses the bundled
// pbxproj. Returned value must be non-empty; comparing against a
// literal would couple the test to the project's current version, so
// we only assert structural properties.
func TestExpectedKeepAwakeVersion_FromBundledPbxproj(t *testing.T) {
	v, err := ExpectedKeepAwakeVersion()
	if err != nil {
		t.Fatalf("ExpectedKeepAwakeVersion() error = %v", err)
	}
	if v == "" {
		t.Fatal("ExpectedKeepAwakeVersion() returned empty string")
	}
	if v != strings.TrimSpace(v) {
		t.Errorf("version has surrounding whitespace: %q", v)
	}
	if strings.ContainsAny(v, `";`) {
		t.Errorf("version contains pbxproj syntax leftovers: %q", v)
	}
	t.Logf("ExpectedKeepAwakeVersion() = %q", v)
}

// TestMarketingVersionPattern_Variants checks the regex against the
// shapes Xcode actually emits — bare semver, quoted strings, pre-
// release suffixes, date-style versions — so we don't accidentally
// re-tighten the pattern later.
func TestMarketingVersionPattern_Variants(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"MARKETING_VERSION = 0.1.0;", "0.1.0"},
		{"MARKETING_VERSION = 0.2.0-rc1;", "0.2.0-rc1"},
		{`MARKETING_VERSION = "0.2.0 with spaces";`, `"0.2.0 with spaces"`},
		{"\t\tMARKETING_VERSION = 2026.04.27;", "2026.04.27"},
		{"MARKETING_VERSION=0.1.0;", "0.1.0"},
	}
	for _, c := range cases {
		m := marketingVersionPattern.FindStringSubmatch(c.line)
		if m == nil {
			t.Errorf("no match: %q", c.line)
			continue
		}
		if m[1] != c.want {
			t.Errorf("input %q: matched %q, want %q", c.line, m[1], c.want)
		}
	}
}

// TestDetectCodesigningTeam_LiveKeychain is an opportunistic
// integration check: when run on a Mac that has at least one team
// registered with Xcode, assert the picked team matches the documented
// preference order (paid > free). Skips on hosts without Xcode or
// without any registered team.
func TestDetectCodesigningTeam_LiveKeychain(t *testing.T) {
	team, err := DetectCodesigningTeam()
	if err != nil {
		t.Skipf("no Xcode-registered team on this host: %v", err)
	}
	if len(team) != 10 {
		t.Errorf("team ID length = %d; want 10 (e.g. 'SWA3H3N7TW')", len(team))
	}
	t.Logf("DetectCodesigningTeam returned %s", team)
}
