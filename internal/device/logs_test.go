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

// TestParseAndroidLogcatTimestampUsesLocalTZ guards the regression
// where logcat entries were parsed as UTC, causing devices in a
// non-UTC timezone to appear hours ahead of "now" and get filtered
// out by `until = now+3s` windows. The fix uses time.ParseInLocation
// with time.Local. Asserting the parse picks up the host's local
// timezone (whatever it happens to be in the test environment) is
// enough — we don't need to fake a specific TZ for the regression
// check, only that the offset is non-UTC when the host's is non-UTC.
func TestParseAndroidLogcatTimestampUsesLocalTZ(t *testing.T) {
	ts := parseAndroidLogcatTimestamp("04-15 14:23:01.123")
	if ts.Location() != time.Local {
		t.Errorf("Location() = %v; want time.Local — UTC parsing would shift the timestamp incorrectly relative to time.Now() comparisons", ts.Location())
	}
}

// TestParseAndroidLogcatTimestampLineUpsWithNow is the exact-shape
// regression for the Samsung filter-everything bug. Synthesise a
// logcat line whose local-time HH:MM:SS matches the current host
// local time, then assert the parsed timestamp falls within a few
// seconds of time.Now() — i.e., the `Timestamp.After(now+3s)` filter
// in LogRange will NOT drop it. The pre-fix code would parse the
// same string as UTC, producing a timestamp offset by the host's
// TZ which falsely tested After(now) on every non-UTC host.
func TestParseAndroidLogcatTimestampLineUpsWithNow(t *testing.T) {
	now := time.Now()
	// Build "MM-DD HH:MM:SS.000" from now in local time.
	s := now.Format("01-02 15:04:05.000")
	ts := parseAndroidLogcatTimestamp(s)
	delta := now.Sub(ts)
	if delta < -3*time.Second || delta > 3*time.Second {
		t.Errorf("parsed %q → %s; now=%s; delta=%v (want |delta| ≤ 3s — anything else means TZ handling is broken and LogRange will filter out all entries)",
			s, ts.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), delta)
	}
}
