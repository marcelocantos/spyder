// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// androidThreadtimeRE matches adb logcat --format=threadtime:
//
//	MM-DD HH:MM:SS.mmm  PID  TID LEVEL TAG  : message
var androidThreadtimeRE = regexp.MustCompile(
	`^(\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}\.\d+)\s+(\d+)\s+(\d+)\s+([A-Z])\s+(\S+)\s*:\s*(.*)$`,
)

// androidEpochRE matches adb logcat -v epoch, including the leading
// horizontal padding real devices use to right-align the timestamp.
//
//	[spaces]SECONDS.FRAC  PID  TID LEVEL TAG: message
var androidEpochRE = regexp.MustCompile(
	`^(\d+(?:\.\d+)?)\s+(\d+)\s+(\d+)\s+([FEWIDV])\s+(\S+)\s*:\s*(.*)$`,
)

// androidLongHeaderRE matches the multiline "long" logcat header. The
// message occupies the following physical lines.
var androidLongHeaderRE = regexp.MustCompile(
	`^\[\s*(\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}\.\d+)\s+(\d+):\s*(\d+)\s+([A-Z])/(\S+)\s*\]$`,
)

// androidProcStartRE matches the events-buffer am_proc_start payload:
// [user, pid, uid, process name, ...].
var androidProcStartRE = regexp.MustCompile(`^\[(\d+),(\d+),(\d+),([^,\]]+)`)

// androidProcDiedRE matches am_proc_died: [user, pid, process name, ...].
var androidProcDiedRE = regexp.MustCompile(`^\[(\d+),(\d+),([^,\]]+)`)

// AndroidLogDecoder turns a stream of logcat text into diagnostic
// records. It keeps malformed lines, links multiline long-format
// crashes, and assigns a process generation when it sees am_proc_start
// so a reused PID is not treated as the same process.
type AndroidLogDecoder struct {
	seq       uint64
	nextGroup int
	open      *androidLongOpen
	procs     androidProcTable
}

type androidLongOpen struct {
	group  string
	parent uint64
	tag    string
	level  string
	pid    int
	pidSt  string
	tid    int
	tidSt  string
	when   time.Time
	clock  Clock
}

type androidProcTable struct {
	gen  map[int]string
	name map[int]string
}

// NewAndroidLogDecoder returns a decoder for one capture.
func NewAndroidLogDecoder() *AndroidLogDecoder {
	return &AndroidLogDecoder{}
}

// Push parses one physical line. offset is the byte offset of line in
// the capture that contains it. The returned LogLine always carries a
// Diagnostic; ok-style callers should inspect Parse.Status.
func (d *AndroidLogDecoder) Push(line string, offset int64) LogLine {
	d.seq++
	rawLine := line
	line = strings.TrimRight(line, "\r")
	body := strings.TrimLeft(line, " \t")

	if m := androidThreadtimeRE.FindStringSubmatch(body); m != nil {
		d.open = nil
		return d.finish(d.fromThreadtime(m, rawLine, offset))
	}
	if m := androidEpochRE.FindStringSubmatch(body); m != nil {
		d.open = nil
		return d.finish(d.fromEpoch(m, rawLine, offset))
	}
	if m := androidLongHeaderRE.FindStringSubmatch(body); m != nil {
		return d.finish(d.fromLongHeader(m, rawLine, offset))
	}
	if d.open != nil {
		return d.finish(d.continuation(rawLine, line, offset))
	}
	return d.finish(d.unparsed(rawLine, offset, "unrecognized log record"))
}

func (d *AndroidLogDecoder) finish(ll LogLine) LogLine {
	if ll.Diagnostic == nil {
		return ll
	}
	ll.Diagnostic.Seq = d.seq
	if d.open != nil && ll.Diagnostic.Raw.Format == "android_logcat_long" {
		d.open.parent = d.seq
	}
	d.noteProc(ll.Diagnostic)
	d.attachGeneration(ll.Diagnostic)
	return ll
}

func (d *AndroidLogDecoder) noteProc(diag *Diagnostic) {
	tag := ""
	if diag.Meta != nil {
		tag = diag.Meta["tag"]
	}
	switch tag {
	case "am_proc_start":
		m := androidProcStartRE.FindStringSubmatch(strings.TrimSpace(diag.Message))
		if m == nil {
			return
		}
		pid, err := strconv.Atoi(m[2])
		if err != nil {
			return
		}
		name := strings.TrimSpace(m[4])
		d.procs.noteStart(pid, name, fmt.Sprintf("start-seq:%d", diag.Seq))
		diag.Meta = metaPut(diag.Meta, "started_pid", strconv.Itoa(pid))
		diag.Meta = metaPut(diag.Meta, "started_process", name)
	case "am_proc_died":
		m := androidProcDiedRE.FindStringSubmatch(strings.TrimSpace(diag.Message))
		if m == nil {
			return
		}
		pid, err := strconv.Atoi(m[2])
		if err != nil {
			return
		}
		d.procs.noteDied(pid)
		diag.Meta = metaPut(diag.Meta, "died_pid", strconv.Itoa(pid))
	}
}

func (d *AndroidLogDecoder) attachGeneration(diag *Diagnostic) {
	if diag.Parse.Status == "unparsed" {
		diag.ProcessGeneration = UnavailableIdentity()
		return
	}
	diag.Process = d.procs.processName(diag.PID, diag.PIDStatus)
	diag.App = d.procs.appName(diag.PID, diag.PIDStatus)
	diag.ProcessGeneration = d.procs.generation(diag.PID, diag.PIDStatus)
}

func (d *AndroidLogDecoder) fromThreadtime(m []string, raw string, offset int64) LogLine {
	when, clock := parseAndroidLocalTimestamp(m[1])
	return d.line(raw, offset, "android_logcat_threadtime", "ok", "", when, clock, m[4], m[5], m[6], m[2], m[3])
}

func (d *AndroidLogDecoder) fromEpoch(m []string, raw string, offset int64) LogLine {
	when, clock, ok := parseAndroidEpochTimestamp(m[1])
	status := "ok"
	parseErr := ""
	if !ok {
		status = "partial"
		parseErr = "epoch timestamp did not parse"
		clock = UnavailableClock()
	}
	return d.line(raw, offset, "android_logcat_epoch", status, parseErr, when, clock, m[4], m[5], m[6], m[2], m[3])
}

func (d *AndroidLogDecoder) fromLongHeader(m []string, raw string, offset int64) LogLine {
	when, clock := parseAndroidLocalTimestamp(m[1])
	ll := d.line(raw, offset, "android_logcat_long", "partial", "long-format header; message follows", when, clock, m[4], m[5], "", m[2], m[3])
	d.nextGroup++
	group := fmt.Sprintf("g%d", d.nextGroup)
	ll.Diagnostic.Raw.Group = group
	d.open = &androidLongOpen{
		group:  group,
		parent: ll.Diagnostic.Seq,
		tag:    m[5],
		level:  ll.Level,
		pid:    ll.Diagnostic.PID,
		pidSt:  ll.Diagnostic.PIDStatus,
		tid:    ll.Diagnostic.TID,
		tidSt:  ll.Diagnostic.TIDStatus,
		when:   when,
		clock:  clock,
	}
	return ll
}

func (d *AndroidLogDecoder) continuation(raw, trimmed string, offset int64) LogLine {
	open := d.open
	ll := d.line(raw, offset, "android_logcat_continuation", "multiline", "", open.when, open.clock, levelLetter(open.level), open.tag, strings.TrimRight(trimmed, "\r"), strconv.Itoa(open.pid), strconv.Itoa(open.tid))
	ll.Diagnostic.Raw.Group = open.group
	ll.Diagnostic.Raw.ParentSeq = open.parent
	ll.Diagnostic.PIDStatus = open.pidSt
	ll.Diagnostic.TIDStatus = open.tidSt
	if open.pidSt != IdentityObserved {
		ll.Diagnostic.PID = 0
	}
	if open.tidSt != IdentityObserved {
		ll.Diagnostic.TID = 0
	}
	return ll
}

func (d *AndroidLogDecoder) unparsed(raw string, offset int64, reason string) LogLine {
	diag := &Diagnostic{
		SchemaVersion:     DiagnosticSchemaVersion,
		Platform:          "android",
		Severity:          "unknown",
		App:               UnavailableIdentity(),
		Process:           UnavailableIdentity(),
		Thread:            UnavailableIdentity(),
		PIDStatus:         IdentityUnavailable,
		TIDStatus:         IdentityUnavailable,
		ProcessGeneration: UnavailableIdentity(),
		DeviceTime:        UnavailableClock(),
		HostReceipt:       UnavailableClock(),
		Parse:             ParseInfo{Status: "unparsed", Error: reason},
		Meta:              map[string]string{"timestamp_basis": "none"},
	}
	diag.Raw.Format = "android_logcat_unparsed"
	diag.Raw.ByteOffset = offset
	setRawText(&diag.Raw, raw)
	return LogLine{Message: "", Diagnostic: diag}
}

func (d *AndroidLogDecoder) line(raw string, offset int64, format, status, parseErr string, when time.Time, clock Clock, letter, tag, message, pidText, tidText string) LogLine {
	level := expandAndroidLevel(letter)
	pid, pidSt := parseID(pidText)
	tid, tidSt := parseID(tidText)
	basis := "none"
	if clock.Source != "" && clock.Source != "unavailable" {
		basis = "device_time"
	}
	diag := &Diagnostic{
		SchemaVersion: DiagnosticSchemaVersion,
		Platform:      "android",
		Severity:      level,
		Message:       message,
		App:           UnavailableIdentity(),
		Process:       UnavailableIdentity(),
		Thread:        UnavailableIdentity(),
		PID:           pid,
		PIDStatus:     pidSt,
		TID:           tid,
		TIDStatus:     tidSt,
		DeviceTime:    clock,
		HostReceipt:   UnavailableClock(),
		Parse:         ParseInfo{Status: status, Error: parseErr},
		Meta: map[string]string{
			"tag":             tag,
			"timestamp_basis": basis,
		},
	}
	if clock.YearAssumed {
		diag.Meta["assumed_location"] = time.Local.String()
	}
	diag.Raw.Format = format
	diag.Raw.ByteOffset = offset
	setRawText(&diag.Raw, raw)
	return LogLine{
		Timestamp:  when,
		Level:      level,
		Tag:        tag,
		Message:    message,
		Diagnostic: diag,
	}
}

func parseID(s string) (int, string) {
	if s == "" {
		return 0, IdentityUnavailable
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, IdentityUnverified
	}
	return n, IdentityObserved
}

func levelLetter(word string) string {
	switch word {
	case "verbose":
		return "V"
	case "debug":
		return "D"
	case "info":
		return "I"
	case "warning":
		return "W"
	case "error":
		return "E"
	case "fatal":
		return "F"
	case "silent":
		return "S"
	default:
		return "I"
	}
}

func (t *androidProcTable) noteStart(pid int, name, generation string) {
	if t.gen == nil {
		t.gen = map[int]string{}
		t.name = map[int]string{}
	}
	t.gen[pid] = generation
	if name != "" {
		t.name[pid] = name
	}
}

func (t *androidProcTable) noteDied(pid int) {
	delete(t.gen, pid)
}

func (t *androidProcTable) generation(pid int, pidStatus string) Identity {
	if pidStatus != IdentityObserved {
		return UnavailableIdentity()
	}
	if g, ok := t.gen[pid]; ok {
		return ObservedIdentity(g)
	}
	return Identity{Status: IdentityUnverified}
}

func (t *androidProcTable) processName(pid int, pidStatus string) Identity {
	if pidStatus != IdentityObserved {
		return UnavailableIdentity()
	}
	if name, ok := t.name[pid]; ok {
		if _, live := t.gen[pid]; live {
			return ObservedIdentity(name)
		}
	}
	return Identity{Status: IdentityUnverified}
}

func (t *androidProcTable) appName(pid int, pidStatus string) Identity {
	name := t.processName(pid, pidStatus)
	if name.Status == IdentityObserved && strings.Contains(name.Value, ".") {
		return name
	}
	// A PID is not an application identity. Leave the app unavailable
	// until a process-start event names a package.
	return UnavailableIdentity()
}

// parseAndroidLocalTimestamp parses MM-DD HH:MM:SS.mmm. logcat prints the
// device's local clock and omits the year and zone. The host zone and the
// current year are assumptions, so the clock is not verified.
func parseAndroidLocalTimestamp(s string) (time.Time, Clock) {
	prec := "millisecond"
	if i := strings.LastIndex(s, "."); i >= 0 {
		prec = precisionName(len(s) - i - 1)
	}
	year := time.Now().Year()
	t, err := time.ParseInLocation("01-02 15:04:05.000", s, time.Local)
	if err != nil {
		t, err = time.ParseInLocation("01-02 15:04:05.99", s, time.Local)
	}
	if err != nil {
		t, err = time.ParseInLocation("01-02 15:04:05.9", s, time.Local)
	}
	if err != nil {
		return time.Time{}, UnavailableClock()
	}
	t = t.AddDate(year, 0, 0)
	return t, Clock{
		Time:        t.Format(time.RFC3339Nano),
		Source:      "device_logcat_local",
		Timezone:    "local_unspecified",
		Precision:   prec,
		YearAssumed: true,
		Verified:    false,
	}
}

func parseAndroidEpochTimestamp(s string) (time.Time, Clock, bool) {
	whole, frac, hasFrac := strings.Cut(s, ".")
	sec, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || sec < 0 {
		return time.Time{}, UnavailableClock(), false
	}
	prec := "second"
	var nsec int64
	if hasFrac {
		if frac == "" || strings.IndexFunc(frac, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return time.Time{}, UnavailableClock(), false
		}
		prec = precisionName(len(frac))
		digits := frac
		if len(digits) > 9 {
			digits = digits[:9]
		}
		padded := digits + strings.Repeat("0", 9-len(digits))
		n, err := strconv.ParseInt(padded, 10, 64)
		if err != nil {
			return time.Time{}, UnavailableClock(), false
		}
		nsec = n
	}
	t := time.Unix(sec, nsec).UTC()
	return t, Clock{
		Time:      t.Format(time.RFC3339Nano),
		Source:    "device_logcat_epoch",
		Timezone:  "UTC",
		Precision: prec,
		Verified:  true,
	}, true
}

func precisionName(digits int) string {
	switch {
	case digits >= 9:
		return "nanosecond"
	case digits >= 6:
		return "microsecond"
	case digits >= 3:
		return "millisecond"
	case digits > 0:
		return "fractional_second"
	default:
		return "second"
	}
}

// acceptAndroidLine keeps unparsed records even when a filter is set, so
// a parse failure is not a silent gap. Parsed lines obey since/until,
// process, tag, and regex as before.
func acceptAndroidLine(dec *AndroidLogDecoder, line string, offset int64, filter LogFilter, re *regexp.Regexp, since, until time.Time) (LogLine, bool) {
	ll := dec.Push(line, offset)
	diag := ll.Diagnostic
	if diag != nil && (diag.Parse.Status == "unparsed") {
		return ll, true
	}
	if !since.IsZero() && !ll.Timestamp.IsZero() && ll.Timestamp.Before(since) {
		return ll, false
	}
	if !until.IsZero() && !ll.Timestamp.IsZero() && ll.Timestamp.After(until) {
		return ll, false
	}
	if filter.Process != "" && !strings.EqualFold(ll.Tag, filter.Process) &&
		!strings.Contains(strings.ToLower(ll.Tag), strings.ToLower(filter.Process)) {
		return ll, false
	}
	if filter.Tag != "" && ll.Tag != filter.Tag && !strings.Contains(ll.Tag, filter.Tag) {
		return ll, false
	}
	if re != nil && !re.MatchString(ll.Message) {
		return ll, false
	}
	return ll, true
}
