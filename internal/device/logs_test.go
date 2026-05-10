// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"testing"
	"time"
)

// --- iOS syslog parser -----------------------------------------------

func TestParseIOSSyslogLine_HappyPath(t *testing.T) {
	// Typical line from `pymobiledevice3 syslog live` text output.
	line := "Mar 15 14:23:01.123 Pippa MyApp[1234] <Error>: crash happened"
	ll, ok := ParseIOSSyslogLine(line)
	if !ok {
		t.Fatalf("ParseIOSSyslogLine returned ok=false for valid line")
	}
	if ll.Process != "MyApp" {
		t.Errorf("Process = %q; want MyApp", ll.Process)
	}
	if ll.Level != "Error" {
		t.Errorf("Level = %q; want Error", ll.Level)
	}
	if ll.Message != "crash happened" {
		t.Errorf("Message = %q; want 'crash happened'", ll.Message)
	}
	// Timestamp must be non-zero and in the current year.
	if ll.Timestamp.IsZero() {
		t.Error("Timestamp is zero")
	}
	if ll.Timestamp.Year() < 2026 {
		t.Errorf("Timestamp year = %d; want >= 2026", ll.Timestamp.Year())
	}
}

func TestParseIOSSyslogLine_WithSubsystem(t *testing.T) {
	// Process field can contain a subsystem in parentheses before the pid bracket.
	line := "Apr  5 09:00:00.000 iPad com.apple.network[42] <Debug>: interface up"
	ll, ok := ParseIOSSyslogLine(line)
	if !ok {
		t.Fatalf("ParseIOSSyslogLine returned ok=false for subsystem line")
	}
	if ll.Level != "Debug" {
		t.Errorf("Level = %q; want Debug", ll.Level)
	}
	if ll.Message != "interface up" {
		t.Errorf("Message = %q; want 'interface up'", ll.Message)
	}
}

func TestParseIOSSyslogLine_MessageWithColon(t *testing.T) {
	// Message body may contain colons — only the first one after <Level> is the separator.
	line := "Jan  1 00:00:00.000 Dev Foo[99] <Info>: key: value: extra"
	ll, ok := ParseIOSSyslogLine(line)
	if !ok {
		t.Fatalf("ParseIOSSyslogLine returned ok=false")
	}
	if ll.Message != "key: value: extra" {
		t.Errorf("Message = %q; want 'key: value: extra'", ll.Message)
	}
}

func TestParseIOSSyslogLine_Junk(t *testing.T) {
	junk := []string{
		"",
		"not a log line",
		"--- some separator ---",
		"Timestamp without enough fields",
	}
	for _, s := range junk {
		if _, ok := ParseIOSSyslogLine(s); ok {
			t.Errorf("ParseIOSSyslogLine(%q) = ok=true; want false", s)
		}
	}
}

// --- Android logcat parser -------------------------------------------

func TestParseAndroidLogcatLine_HappyPath(t *testing.T) {
	// Canonical threadtime format line.
	line := "04-15 14:23:01.123  1234  5678 E MyTag  : something went wrong"
	ll, ok := ParseAndroidLogcatLine(line)
	if !ok {
		t.Fatalf("ParseAndroidLogcatLine returned ok=false for valid line")
	}
	if ll.Level != "error" {
		t.Errorf("Level = %q; want error", ll.Level)
	}
	if ll.Tag != "MyTag" {
		t.Errorf("Tag = %q; want MyTag", ll.Tag)
	}
	if ll.Message != "something went wrong" {
		t.Errorf("Message = %q; want 'something went wrong'", ll.Message)
	}
	if ll.Timestamp.IsZero() {
		t.Error("Timestamp is zero")
	}
}

func TestParseAndroidLogcatLine_AllLevels(t *testing.T) {
	cases := []struct {
		letter string
		want   string
	}{
		{"V", "verbose"},
		{"D", "debug"},
		{"I", "info"},
		{"W", "warning"},
		{"E", "error"},
		{"F", "fatal"},
		{"S", "silent"},
	}
	for _, c := range cases {
		line := "01-01 00:00:00.000  100  200 " + c.letter + " TAG  : msg"
		ll, ok := ParseAndroidLogcatLine(line)
		if !ok {
			t.Errorf("ParseAndroidLogcatLine(%q) ok=false", line)
			continue
		}
		if ll.Level != c.want {
			t.Errorf("level %q → %q; want %q", c.letter, ll.Level, c.want)
		}
	}
}

func TestParseAndroidLogcatLine_Junk(t *testing.T) {
	junk := []string{
		"",
		"--------- beginning of main",
		"not a log line",
	}
	for _, s := range junk {
		if _, ok := ParseAndroidLogcatLine(s); ok {
			t.Errorf("ParseAndroidLogcatLine(%q) = ok=true; want false", s)
		}
	}
}

func TestParseAndroidLogcatTimestamp(t *testing.T) {
	ts := parseAndroidLogcatTimestamp("04-15 14:23:01.123")
	if ts.IsZero() {
		t.Error("timestamp is zero")
	}
	if ts.Month() != time.April {
		t.Errorf("month = %v; want April", ts.Month())
	}
	if ts.Day() != 15 {
		t.Errorf("day = %d; want 15", ts.Day())
	}
	if ts.Hour() != 14 || ts.Minute() != 23 || ts.Second() != 1 {
		t.Errorf("time = %v; want 14:23:01", ts)
	}
}
