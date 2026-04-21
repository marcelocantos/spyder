// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Android tombstone parser
// ---------------------------------------------------------------------------

func TestParseTombstone_Basic(t *testing.T) {
	data := `*** *** *** *** *** *** *** *** *** *** *** *** *** *** *** ***
Build fingerprint: 'google/raven/raven:12/SQ3A.220605.009.B1/8650216:user/release-keys'
pid: 1234, tid: 1234, name: my_process  >>> com.example.app <<<
signal 6 (SIGABRT), code -1 (SI_QUEUE), fault addr --------
`
	ts := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	cr := parseTombstone([]byte(data), ts, "")
	if cr == nil {
		t.Fatal("parseTombstone returned nil; want a record")
	}
	if cr.Process != "my_process" {
		t.Errorf("Process = %q; want my_process", cr.Process)
	}
	if cr.Reason != "signal 6 (SIGABRT), code -1 (SI_QUEUE), fault addr --------" {
		t.Errorf("Reason = %q", cr.Reason)
	}
	if cr.Timestamp != ts {
		t.Errorf("Timestamp = %v; want %v", cr.Timestamp, ts)
	}
}

func TestParseTombstone_CmdLine(t *testing.T) {
	data := `Cmd line: com.example.myapp
signal 11 (SIGSEGV), code 1 (SEGV_MAPERR)`
	ts := time.Now().UTC()
	cr := parseTombstone([]byte(data), ts, "")
	if cr == nil {
		t.Fatal("parseTombstone returned nil")
	}
	if cr.Process != "com.example.myapp" {
		t.Errorf("Process from Cmd line = %q; want com.example.myapp", cr.Process)
	}
}

func TestParseTombstone_ProcessFilter(t *testing.T) {
	data := `pid: 99, tid: 99, name: target_proc  >>> com.target <<<
signal 11 (SIGSEGV)`
	ts := time.Now().UTC()

	// Filter matches → returns record.
	cr := parseTombstone([]byte(data), ts, "target_proc")
	if cr == nil {
		t.Fatal("parseTombstone returned nil for matching filter")
	}

	// Filter doesn't match → returns nil.
	cr2 := parseTombstone([]byte(data), ts, "other_proc")
	if cr2 != nil {
		t.Error("parseTombstone returned record for non-matching filter; want nil")
	}
}

// ---------------------------------------------------------------------------
// Android logcat crash buffer parser
// ---------------------------------------------------------------------------

func TestParseLogcatCrashBuffer(t *testing.T) {
	// Typical AndroidRuntime crash block in threadtime format.
	logcat := `04-19 14:30:22.000  1234  1234 E AndroidRuntime: FATAL EXCEPTION: main
04-19 14:30:22.001  1234  1234 E AndroidRuntime: Process: com.example.app, PID: 1234
04-19 14:30:22.002  1234  1234 E AndroidRuntime: java.lang.NullPointerException: foo
04-19 14:30:22.003  1234  1234 E AndroidRuntime: 	at com.example.app.Main.onCreate(Main.java:42)
`
	reports := parseLogcatCrashBuffer(logcat, time.Time{}, "")
	if len(reports) != 1 {
		t.Fatalf("got %d reports; want 1", len(reports))
	}
	cr := reports[0]
	if cr.Process != "AndroidRuntime" {
		t.Errorf("Process = %q; want AndroidRuntime", cr.Process)
	}
	if !strings.Contains(cr.Reason, "FATAL EXCEPTION") {
		t.Errorf("Reason = %q; want FATAL EXCEPTION mention", cr.Reason)
	}
	if cr.Raw == "" {
		t.Error("Raw is empty; want logcat lines")
	}
}

func TestParseLogcatCrashBuffer_ProcessFilter(t *testing.T) {
	logcat := `04-19 14:30:22.000  1234  1234 E AndroidRuntime: FATAL EXCEPTION: main
04-19 14:30:22.001  1234  1234 E AndroidRuntime: stack line
`
	// Filter by different process → no results.
	reports := parseLogcatCrashBuffer(logcat, time.Time{}, "SomeOtherTag")
	if len(reports) != 0 {
		t.Errorf("got %d reports with non-matching filter; want 0", len(reports))
	}

	// Filter matching → one result.
	reports2 := parseLogcatCrashBuffer(logcat, time.Time{}, "AndroidRuntime")
	if len(reports2) != 1 {
		t.Errorf("got %d reports with matching filter; want 1", len(reports2))
	}
}

func TestParseLogcatCrashBuffer_Empty(t *testing.T) {
	reports := parseLogcatCrashBuffer("", time.Time{}, "")
	if len(reports) != 0 {
		t.Errorf("got %d reports on empty input; want 0", len(reports))
	}
}
