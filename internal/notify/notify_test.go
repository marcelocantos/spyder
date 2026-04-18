// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package notify

import (
	"strings"
	"testing"
)

func TestEscAppleScript(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{``, ``},
		{`hello world`, `hello world`},
		{`say "hi"`, `say \"hi\"`},
		{`path\to\file`, `path\\to\\file`},
		{"line1\nline2", "line1 line2"},
		{`"quoted" \and\ newline` + "\n" + `here`, `\"quoted\" \\and\\ newline here`},
	}
	for _, c := range cases {
		got := escAppleScript(c.in)
		if got != c.want {
			t.Errorf("escAppleScript(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestOsascriptScript_Shape(t *testing.T) {
	got := osascriptScript("spyder", "unlock Pippa")
	want := `display notification "unlock Pippa" with title "spyder"`
	if got != want {
		t.Errorf("osascriptScript basic = %q; want %q", got, want)
	}
}

func TestOsascriptScript_EscapesBothFields(t *testing.T) {
	got := osascriptScript(`ti"tle`, `bo"dy`)
	if !strings.Contains(got, `\"`) {
		t.Errorf("expected escaped double-quote in %q", got)
	}
	// Structural: two \" pairs (title + body).
	if strings.Count(got, `\"`) < 2 {
		t.Errorf("expected >=2 escaped quotes; got %q", got)
	}
}

func TestOsascriptScript_CollapsesNewlines(t *testing.T) {
	got := osascriptScript("spyder", "line1\nline2")
	if strings.Contains(got, "\n") {
		t.Errorf("osascriptScript should collapse newlines; got %q", got)
	}
}
