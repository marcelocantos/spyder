// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAndroidEpochPaddingRetainsRawAndSeparatesClocks(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "android_epoch_padded.log"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ParseDiagnosticStream("android", bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	var records []Diagnostic
	dec := json.NewDecoder(&out)
	for dec.More() {
		var rec Diagnostic
		if err := dec.Decode(&rec); err != nil {
			t.Fatal(err)
		}
		records = append(records, rec)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3 (parsed, unparsed, crash)", len(records))
	}
	first := records[0]
	if first.SchemaVersion != DiagnosticSchemaVersion || first.Platform != "android" {
		t.Fatalf("schema/platform = %d %s", first.SchemaVersion, first.Platform)
	}
	if first.Parse.Status != "ok" || first.Raw.Format != "android_logcat_epoch" {
		t.Fatalf("parse = %+v format %s", first.Parse, first.Raw.Format)
	}
	if !strings.HasPrefix(first.Raw.Text, "         1792200000.001") {
		t.Fatalf("raw padding dropped: %q", first.Raw.Text)
	}
	if first.DeviceTime.Source != "device_logcat_epoch" || first.DeviceTime.Timezone != "UTC" || !first.DeviceTime.Verified {
		t.Fatalf("device clock = %+v", first.DeviceTime)
	}
	if first.HostReceipt.Source != "host_receipt" || !first.HostReceipt.Verified {
		t.Fatalf("host receipt = %+v", first.HostReceipt)
	}
	if first.DeviceTime.Time == first.HostReceipt.Time {
		t.Fatalf("device time and host receipt collapsed: %s", first.DeviceTime.Time)
	}
	if first.Meta["tag"] != "StockCarsRegression" || first.Severity != "info" || first.Message != "fixture-current-nonce" {
		t.Fatalf("normalized fields = %s %s %q", first.Meta["tag"], first.Severity, first.Message)
	}
	if first.PIDStatus != IdentityObserved || first.PID != 789 || first.TID != 789 {
		t.Fatalf("pid/tid = %d/%s %d/%s", first.PID, first.PIDStatus, first.TID, first.TIDStatus)
	}
	if first.ProcessGeneration.Status != IdentityUnverified {
		t.Fatalf("pid without a start event was %s", first.ProcessGeneration.Status)
	}
	if first.Process.Status != IdentityUnverified {
		t.Fatalf("process without a start event was %s", first.Process.Status)
	}
	if first.App.Status != IdentityUnavailable {
		t.Fatalf("app without a package was %s", first.App.Status)
	}
	if records[1].Parse.Status != "unparsed" || records[1].Raw.Text != "not a log line" || records[1].Parse.Error == "" {
		t.Fatalf("unparsed record dropped or unmarked: %+v", records[1])
	}
	if records[2].Severity != "error" || records[2].Message != "Process: com.example.racing, PID: 123" {
		t.Fatalf("crash line = %+v", records[2])
	}
	if records[0].Seq >= records[2].Seq {
		t.Fatalf("seq did not follow arrival: %d then %d", records[0].Seq, records[2].Seq)
	}
}

func TestAndroidLongCrashAndMalformedStayAddressable(t *testing.T) {
	longRaw, err := os.ReadFile(filepath.Join("testdata", "android_long_crash.log"))
	if err != nil {
		t.Fatal(err)
	}
	dec := NewAndroidLogDecoder()
	var lines []LogLine
	offset := int64(0)
	for _, line := range strings.Split(strings.TrimRight(string(longRaw), "\n"), "\n") {
		lines = append(lines, dec.Push(line, offset))
		offset += int64(len(line)) + 1
	}
	if len(lines) != 4 {
		t.Fatalf("long crash lines = %d, want 4", len(lines))
	}
	if lines[0].Diagnostic.Parse.Status != "partial" || lines[0].Diagnostic.Raw.Format != "android_logcat_long" {
		t.Fatalf("header = %+v", lines[0].Diagnostic.Parse)
	}
	for i, ll := range lines[1:] {
		if ll.Diagnostic.Parse.Status != "multiline" {
			t.Fatalf("line %d status = %s", i+1, ll.Diagnostic.Parse.Status)
		}
		if ll.Diagnostic.Raw.ParentSeq != lines[0].Diagnostic.Seq || ll.Diagnostic.Raw.Group == "" {
			t.Fatalf("line %d not tied to header: %+v", i+1, ll.Diagnostic.Raw)
		}
		if ll.Tag != "AndroidRuntime" || ll.Diagnostic.PID != 321 {
			t.Fatalf("continuation lost header identity: tag %s pid %d", ll.Tag, ll.Diagnostic.PID)
		}
	}
	if !strings.Contains(lines[2].Message, "Process: com.example.racing, PID: 321") {
		t.Fatalf("crash process line missing: %q", lines[2].Message)
	}
	if lines[0].Diagnostic.DeviceTime.Source == lines[0].Diagnostic.HostReceipt.Source {
		t.Fatal("long-format header did not keep device and host clocks apart")
	}

	bad, err := os.ReadFile(filepath.Join("testdata", "android_malformed.log"))
	if err != nil {
		t.Fatal(err)
	}
	dec = NewAndroidLogDecoder()
	var kept int
	for _, line := range strings.Split(strings.TrimRight(string(bad), "\n"), "\n") {
		ll := dec.Push(line, 0)
		if ll.Diagnostic == nil || ll.Diagnostic.Raw.Text != line || ll.Diagnostic.Parse.Status != "unparsed" {
			t.Fatalf("malformed line not retained: %q -> %+v", line, ll.Diagnostic)
		}
		kept++
	}
	if kept != 2 {
		t.Fatalf("kept %d malformed lines", kept)
	}
}

func TestAndroidClockDifferenceDoesNotDefineOrder(t *testing.T) {
	dec := NewAndroidLogDecoder()
	later := dec.Push("04-15 14:23:01.123  1  1 I Late: by local clock", 0)
	earlier := dec.Push("1000.000  1  1 I Early: epoch 1970", 40)
	if !earlier.Timestamp.Before(later.Timestamp) {
		t.Fatalf("fixture clocks are not inverted: later %s earlier %s", later.Timestamp, earlier.Timestamp)
	}
	if later.Diagnostic.Seq >= earlier.Diagnostic.Seq {
		t.Fatalf("arrival seq followed the wall clock: %d then %d", later.Diagnostic.Seq, earlier.Diagnostic.Seq)
	}
	if later.Diagnostic.DeviceTime.Source == earlier.Diagnostic.DeviceTime.Source {
		t.Fatal("local threadtime and epoch were given the same clock source")
	}
	if later.Diagnostic.DeviceTime.Verified || !earlier.Diagnostic.DeviceTime.Verified {
		t.Fatalf("verification = local %v epoch %v", later.Diagnostic.DeviceTime.Verified, earlier.Diagnostic.DeviceTime.Verified)
	}
	if later.Diagnostic.DeviceTime.YearAssumed == false {
		t.Fatal("threadtime year was treated as present in the text")
	}
}

func TestAndroidPIDReuseChangesProcessGeneration(t *testing.T) {
	dec := NewAndroidLogDecoder()
	before := dec.Push("1792200000.000 50 50 I Unity: before", 0)
	start1 := dec.Push("1792200000.001 10 10 I am_proc_start: [0,50,1000,com.example.racing,activity,com.example.racing.Main]", 0)
	first := dec.Push("1792200000.002 50 50 I Unity: first generation", 0)
	dec.Push("1792200000.003 10 10 I am_proc_died: [0,50,com.example.racing]", 0)
	between := dec.Push("1792200000.004 50 50 I Unity: after death", 0)
	start2 := dec.Push("1792200000.005 10 10 I am_proc_start: [0,50,1000,com.example.racing,activity,com.example.racing.Main]", 0)
	second := dec.Push("1792200000.006 50 50 I Unity: second generation", 0)

	if before.Diagnostic.ProcessGeneration.Status != IdentityUnverified {
		t.Fatalf("pid before any start = %s", before.Diagnostic.ProcessGeneration.Status)
	}
	if start1.Diagnostic.Meta["started_pid"] != "50" || start2.Diagnostic.Meta["started_pid"] != "50" {
		t.Fatalf("start events did not record the child pid")
	}
	if first.Diagnostic.ProcessGeneration.Status != IdentityObserved || second.Diagnostic.ProcessGeneration.Status != IdentityObserved {
		t.Fatalf("live generations = %s %s", first.Diagnostic.ProcessGeneration.Status, second.Diagnostic.ProcessGeneration.Status)
	}
	if first.Diagnostic.ProcessGeneration.Value == second.Diagnostic.ProcessGeneration.Value {
		t.Fatalf("reused pid kept generation %s", first.Diagnostic.ProcessGeneration.Value)
	}
	if between.Diagnostic.ProcessGeneration.Status != IdentityUnverified {
		t.Fatalf("pid after death still verified: %+v", between.Diagnostic.ProcessGeneration)
	}
	if first.Diagnostic.App.Status != IdentityObserved || first.Diagnostic.App.Value != "com.example.racing" {
		t.Fatalf("app identity = %+v", first.Diagnostic.App)
	}
}

func TestIOSRetainedFormatsKeepIdentityExplicit(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "ios_log_ndjson.log"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ParseDiagnosticStream("ios", bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	var records []Diagnostic
	dec := json.NewDecoder(&out)
	for dec.More() {
		var rec Diagnostic
		if err := dec.Decode(&rec); err != nil {
			t.Fatal(err)
		}
		records = append(records, rec)
	}
	if len(records) != 2 {
		t.Fatalf("ios ndjson records = %d", len(records))
	}
	ok := records[0]
	if ok.Raw.Format != "ios_log_ndjson" || ok.Process.Value != "StockCars" || ok.Process.Status != IdentityObserved {
		t.Fatalf("process = %+v format %s", ok.Process, ok.Raw.Format)
	}
	if ok.App.Status != IdentityUnavailable {
		t.Fatalf("subsystem was treated as the app: %+v", ok.App)
	}
	if ok.PID != 4321 || ok.TID != 4322 || ok.ProcessGeneration.Status != IdentityUnverified {
		t.Fatalf("pid generation = %d %d %+v", ok.PID, ok.TID, ok.ProcessGeneration)
	}
	if ok.DeviceTime.Timezone != "+11:00" || !ok.DeviceTime.Verified || ok.DeviceTime.Source != "device_log_text" {
		t.Fatalf("device clock = %+v", ok.DeviceTime)
	}
	if ok.HostReceipt.Source != "host_receipt" {
		t.Fatalf("host receipt = %+v", ok.HostReceipt)
	}
	if ok.Severity != "info" || ok.Meta["subsystem"] != "com.minicades.stockcars" {
		t.Fatalf("severity/subsystem = %s %+v", ok.Severity, ok.Meta)
	}
	if records[1].Parse.Status != "unparsed" || records[1].Parse.Error == "" {
		t.Fatalf("bad ndjson row was parsed: %+v", records[1])
	}

	show, err := os.ReadFile(filepath.Join("testdata", "ios_log_show.txt"))
	if err != nil {
		t.Fatal(err)
	}
	parsed := NewLineDecoderIOS(string(show))
	if parsed[0].Raw.Format != "ios_log_show" || parsed[0].Message != "fresh marker" || parsed[0].Thread.Value != "0x1a2b" {
		t.Fatalf("log show row = %+v", parsed[0])
	}
	if parsed[0].TIDStatus != IdentityUnavailable {
		t.Fatalf("hex thread id was stored as a decimal tid")
	}
	if parsed[1].Parse.Status != "unparsed" || !strings.Contains(parsed[1].Raw.Text, "torn record") {
		t.Fatalf("torn ios record dropped: %+v", parsed[1])
	}
}

func TestCrashChannelsDeclareUnsupportedSources(t *testing.T) {
	var tombstone, ips bool
	for _, ch := range CrashChannels() {
		if ch.Status != "supported" && ch.Status != "unsupported" {
			t.Fatalf("channel %s has status %q", ch.ID, ch.Status)
		}
		if ch.ID == "android_tombstone" && ch.Status == "unsupported" {
			tombstone = true
		}
		if ch.ID == "ios_ips" && ch.Status == "unsupported" {
			ips = true
		}
	}
	if !tombstone || !ips {
		t.Fatal("unsupported tombstone and ips channels must stay declared")
	}
}

func TestParseDiagnosticStreamRejectsUnknownPlatform(t *testing.T) {
	err := ParseDiagnosticStream("desktop", strings.NewReader("x\n"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected unknown platform error")
	}
}

// NewLineDecoderIOS parses a whole ios text blob. Tests use it so a
// trailing newline does not have to be special-cased at each call.
func NewLineDecoderIOS(raw string) []Diagnostic {
	var out []Diagnostic
	offset := int64(0)
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		out = append(out, *ParseIOSLogLine(line, offset).Diagnostic)
		offset += int64(len(line)) + 1
	}
	return out
}

func TestEpochInstantIsUTC(t *testing.T) {
	when, clock, ok := parseAndroidEpochTimestamp("1792200000.001")
	if !ok {
		t.Fatal("epoch did not parse")
	}
	want := time.Unix(1792200000, int64(0.001*1e9)).UTC()
	if !when.Equal(want) {
		t.Fatalf("epoch = %s, want %s", when, want)
	}
	if clock.Precision != "millisecond" || clock.Timezone != "UTC" {
		t.Fatalf("clock = %+v", clock)
	}
}
