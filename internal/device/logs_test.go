// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/pmd3bridge"
)

// 🎯T49: pmd3 produces naive datetimes via datetime.fromtimestamp(). The
// bridge must promote them to timezone-aware before isoformat() so the
// Go side can parse them with RFC3339Nano. Without that, Timestamp on
// every entry is the zero value and the since/until filter in LogRange
// drops them all — the v0.24.0 "logs returns []" regression. This test
// pins the parser path.

func TestSyslogEntryToLogLine_RFC3339NanoWithOffset(t *testing.T) {
	// Shape produced by `entry.timestamp.astimezone().isoformat()` on
	// a host whose local tz is +10:00 (e.g. Sydney during AEST).
	e := pmd3bridge.SyslogEntry{
		PID:       1234,
		Timestamp: "2026-04-28T17:30:00.123456+10:00",
		Level:     "INFO",
		Process:   "MyApp",
		Subsystem: "com.example",
		Category:  "ui",
		Message:   "hello",
	}
	ll := syslogEntryToLogLine(e)
	if ll.Timestamp.IsZero() {
		t.Fatalf("Timestamp parsed as zero — RFC3339Nano with offset must be accepted (got input %q)", e.Timestamp)
	}
	want := time.Date(2026, 4, 28, 17, 30, 0, 123456000, time.FixedZone("+1000", 10*60*60))
	if !ll.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v; want %v", ll.Timestamp, want)
	}
}

func TestSyslogEntryToLogLine_RFC3339NanoUTC(t *testing.T) {
	e := pmd3bridge.SyslogEntry{
		Timestamp: "2026-04-28T07:30:00.500000Z",
	}
	ll := syslogEntryToLogLine(e)
	if ll.Timestamp.IsZero() {
		t.Fatalf("Timestamp parsed as zero for valid UTC RFC3339Nano %q", e.Timestamp)
	}
	if ll.Timestamp.UTC().Hour() != 7 {
		t.Errorf("hour = %d; want 7", ll.Timestamp.UTC().Hour())
	}
}

func TestSyslogEntryToLogLine_TimezonelessIsZero(t *testing.T) {
	// Documents the failure mode that 🎯T49 fixes on the bridge side:
	// a naive isoformat() string parses as zero, so the test pins the
	// contract — bridge must NOT emit this shape.
	e := pmd3bridge.SyslogEntry{
		Timestamp: "2026-04-28T17:30:00.123456",
	}
	ll := syslogEntryToLogLine(e)
	if !ll.Timestamp.IsZero() {
		t.Errorf("expected zero timestamp for timezone-less input %q (so the bridge knows it must emit tz-aware ISO strings); got %v",
			e.Timestamp, ll.Timestamp)
	}
}

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
