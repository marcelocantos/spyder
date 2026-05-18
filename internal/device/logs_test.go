// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"testing"
	"time"
)

// iOS log parsing is now handled inside go-ios's `ostrace` package
// (structured os_log_relay records, not BSD syslog text). Coverage for
// the parser lives there. The mapping ostrace.LogEntry → device.LogLine
// in ostraceEntryToLogLine is a trivial field copy.

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
