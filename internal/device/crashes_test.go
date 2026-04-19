// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// iOS .ips filename parser
// ---------------------------------------------------------------------------

func TestParseIPSFilename(t *testing.T) {
	cases := []struct {
		name        string
		wantProcess string
		wantYear    int // 0 = don't check
	}{
		{
			name:        "MyApp-2026-04-19-143022.ips",
			wantProcess: "MyApp",
			wantYear:    2026,
		},
		{
			name:        "MyApp-2026-04-19-143022-0700.ips",
			wantProcess: "MyApp",
			wantYear:    2026,
		},
		{
			// Subdirectory prefix is stripped.
			name:        "/var/mobile/Library/Logs/CrashReporter/MyApp-2026-01-02-030405.ips",
			wantProcess: "MyApp",
			wantYear:    2026,
		},
		{
			// Process names with underscores: first underscore-token is used.
			// The full base is split at first '-'; all underscore-delimited tokens
			// before that are part of process. Since the basename starts with a
			// token containing underscores, we get the first underscore-delimited
			// portion as the process name. Timestamp extraction may fail if the
			// suffix isn't a recognisable date; this is acceptable.
			name:        "com_example_app_UUID_device_os_2026-04-19-120000.ips",
			wantProcess: "com",
			wantYear:    0, // timestamp in suffix "04-19-120000" doesn't match known layouts
		},
		{
			// No recognisable timestamp → zero time.
			name:        "WeirdReport.ips",
			wantProcess: "WeirdReport",
			wantYear:    0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := parseIPSFilename(c.name)
			if m.process != c.wantProcess {
				t.Errorf("process = %q; want %q", m.process, c.wantProcess)
			}
			if c.wantYear != 0 && m.ts.Year() != c.wantYear {
				t.Errorf("ts.Year() = %d; want %d (ts=%v)", m.ts.Year(), c.wantYear, m.ts)
			}
			if c.wantYear == 0 && !m.ts.IsZero() {
				t.Errorf("ts = %v; want zero", m.ts)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// iOS crash report list parser
// ---------------------------------------------------------------------------

func TestParseCrashReportList_JSON(t *testing.T) {
	data := []byte(`["MyApp-2026-04-19-143022.ips", "OtherApp-2026-01-01-000000.ips"]`)
	metas, err := parseCrashReportList(data)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("got %d entries; want 2", len(metas))
	}
	if metas[0].filename != "MyApp-2026-04-19-143022.ips" {
		t.Errorf("filename[0] = %q", metas[0].filename)
	}
	if metas[0].process != "MyApp" {
		t.Errorf("process[0] = %q; want MyApp", metas[0].process)
	}
}

func TestParseCrashReportList_PlainText(t *testing.T) {
	data := []byte("MyApp-2026-04-19-143022.ips\nOtherApp-2026-01-01-000000.ips\n")
	metas, err := parseCrashReportList(data)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("got %d entries; want 2", len(metas))
	}
}

func TestParseCrashReportList_Empty(t *testing.T) {
	metas, err := parseCrashReportList([]byte(""))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("got %d; want 0", len(metas))
	}
}

func TestParseCrashReportList_EmptyJSON(t *testing.T) {
	metas, err := parseCrashReportList([]byte("[]"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("got %d; want 0", len(metas))
	}
}

// ---------------------------------------------------------------------------
// .ips file header parser
// ---------------------------------------------------------------------------

func TestParseIPSReport_WithHeader(t *testing.T) {
	// Realistic .ips first line: a single JSON object.
	raw := `{"procName":"MyApp","captured_time":"2026-04-19T14:30:22Z","exception_type":"EXC_CRASH","exception_info":"SIGABRT"}
Termination Reason: Namespace SIGNAL, Code 6 Abort trap: 6
...rest of the crash report...`
	meta := ipsFileMeta{filename: "MyApp-2026-04-19-143022.ips", process: "MyApp"}
	cr := parseIPSReport([]byte(raw), meta)
	if cr.Process != "MyApp" {
		t.Errorf("Process = %q; want MyApp", cr.Process)
	}
	if cr.Reason != "EXC_CRASH: SIGABRT" {
		t.Errorf("Reason = %q; want EXC_CRASH: SIGABRT", cr.Reason)
	}
	if cr.Timestamp.IsZero() {
		t.Error("Timestamp is zero; want 2026-04-19T14:30:22Z")
	}
	if !strings.Contains(cr.Raw, "Termination Reason") {
		t.Error("Raw does not contain full file content")
	}
}

func TestParseIPSReport_MalformedHeader(t *testing.T) {
	// Non-JSON first line — should fall back to meta values.
	raw := "not json at all\nsome crash body"
	ts := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)
	meta := ipsFileMeta{filename: "MyApp-2026-04-19-000000.ips", process: "MyApp", ts: ts}
	cr := parseIPSReport([]byte(raw), meta)
	if cr.Process != "MyApp" {
		t.Errorf("Process fallback = %q; want MyApp", cr.Process)
	}
	if cr.Timestamp != ts {
		t.Errorf("Timestamp fallback = %v; want %v", cr.Timestamp, ts)
	}
}

func TestParseIPSReport_AlternativeProcessKey(t *testing.T) {
	raw := `{"process_name":"AltApp","captured_time":"2026-04-19T10:00:00Z"}
body`
	meta := ipsFileMeta{filename: "AltApp.ips", process: "AltApp"}
	cr := parseIPSReport([]byte(raw), meta)
	if cr.Process != "AltApp" {
		t.Errorf("Process from process_name key = %q; want AltApp", cr.Process)
	}
}

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
