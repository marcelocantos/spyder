// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"encoding/json"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/danielpaulus/go-ios/ios/ostrace"
	"github.com/marcelocantos/spyder/internal/oslog"
)

// iosLogShowRE matches a `log show` / `log stream` default-style row.
// Spacing between columns varies; the fields are timestamp+zone, thread
// hex, type, activity hex, pid, ttl, process, and the message.
var iosLogShowRE = regexp.MustCompile(
	`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d+)([+-]\d{4})\s+(0x[0-9A-Fa-f]+)\s+(\S+)\s+(0x[0-9A-Fa-f]+)\s+(\d+)\s+(\d+)\s+([^:\s]+):\s*(.*)$`,
)

var iosZoneRE = regexp.MustCompile(`[+-]\d{4}$`)

type iosNDJSON struct {
	Timestamp        string `json:"timestamp"`
	EventMessage     string `json:"eventMessage"`
	ProcessImagePath string `json:"processImagePath"`
	ProcessID        int    `json:"processID"`
	ThreadID         int    `json:"threadID"`
	MessageType      string `json:"messageType"`
	Subsystem        string `json:"subsystem"`
	Category         string `json:"category"`
}

// ParseIOSLogLine parses one physical iOS log line. Recognized formats
// are `log show --style ndjson` and the default `log show` text row.
// Anything else is kept as an unparsed record.
func ParseIOSLogLine(line string, offset int64) LogLine {
	raw := line
	line = strings.TrimRight(line, "\r")
	if strings.HasPrefix(strings.TrimLeft(line, " \t"), "{") {
		var row iosNDJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &row); err == nil && (row.EventMessage != "" || row.Timestamp != "" || row.ProcessID != 0) {
			return iosNDJSONLine(row, raw, offset)
		}
	}
	if m := iosLogShowRE.FindStringSubmatch(strings.TrimLeft(line, " \t")); m != nil {
		return iosShowLine(m, raw, offset)
	}
	diag := iosBase("unknown", "", "ios_log_unparsed")
	diag.Parse = ParseInfo{Status: "unparsed", Error: "unrecognized log record"}
	diag.Raw.ByteOffset = offset
	setRawText(&diag.Raw, raw)
	return LogLine{Diagnostic: diag}
}

func iosNDJSONLine(row iosNDJSON, raw string, offset int64) LogLine {
	sev, platformSev := normalizeIOSSeverity(row.MessageType)
	when, clock, timeOK := parseIOSWall(row.Timestamp)
	status, parseErr := "ok", ""
	if row.Timestamp != "" && !timeOK {
		status, parseErr = "partial", "device timestamp did not parse"
	}
	proc := ""
	if row.ProcessImagePath != "" {
		proc = path.Base(row.ProcessImagePath)
	}
	diag := iosBase(sev, row.EventMessage, "ios_log_ndjson")
	diag.Process = ObservedIdentity(proc)
	diag.Parse = ParseInfo{Status: status, Error: parseErr}
	diag.DeviceTime = clock
	diag.PID, diag.PIDStatus = iosPID(row.ProcessID)
	diag.TID, diag.TIDStatus = iosPID(row.ThreadID)
	diag.ProcessGeneration = iosGeneration(diag.PIDStatus)
	diag.Meta = metaPut(diag.Meta, "platform_severity", platformSev)
	diag.Meta = metaPut(diag.Meta, "subsystem", row.Subsystem)
	diag.Meta = metaPut(diag.Meta, "category", row.Category)
	diag.Meta = metaPut(diag.Meta, "timestamp_basis", iosTimeBasis(clock))
	diag.Meta = metaPut(diag.Meta, "native_packet", "not_retained")
	diag.Raw.ByteOffset = offset
	setRawText(&diag.Raw, raw)
	return LogLine{
		Timestamp:  when,
		Process:    proc,
		Level:      platformSev,
		Message:    row.EventMessage,
		Diagnostic: diag,
	}
}

func iosShowLine(m []string, raw string, offset int64) LogLine {
	sev, platformSev := normalizeIOSSeverity(m[4])
	when, clock, timeOK := parseIOSWall(m[1] + m[2])
	status, parseErr := "ok", ""
	if !timeOK {
		status, parseErr = "partial", "device timestamp did not parse"
	}
	pid, pidSt := parseID(m[6])
	diag := iosBase(sev, m[9], "ios_log_show")
	diag.Process = ObservedIdentity(m[8])
	diag.Thread = ObservedIdentity(m[3])
	diag.Parse = ParseInfo{Status: status, Error: parseErr}
	diag.DeviceTime = clock
	diag.PID, diag.PIDStatus = pid, pidSt
	diag.TIDStatus = IdentityUnavailable
	diag.ProcessGeneration = iosGeneration(pidSt)
	diag.Meta = metaPut(diag.Meta, "platform_severity", platformSev)
	diag.Meta = metaPut(diag.Meta, "thread_id", m[3])
	diag.Meta = metaPut(diag.Meta, "activity", m[5])
	diag.Meta = metaPut(diag.Meta, "timestamp_basis", iosTimeBasis(clock))
	diag.Raw.ByteOffset = offset
	setRawText(&diag.Raw, raw)
	return LogLine{
		Timestamp:  when,
		Process:    m[8],
		Level:      platformSev,
		Message:    m[9],
		Diagnostic: diag,
	}
}

func iosBase(severity, message, format string) *Diagnostic {
	diag := &Diagnostic{
		SchemaVersion:     DiagnosticSchemaVersion,
		Platform:          "ios",
		Severity:          severity,
		Message:           message,
		App:               UnavailableIdentity(),
		Process:           UnavailableIdentity(),
		Thread:            UnavailableIdentity(),
		PIDStatus:         IdentityUnavailable,
		TIDStatus:         IdentityUnavailable,
		ProcessGeneration: UnavailableIdentity(),
		DeviceTime:        UnavailableClock(),
		HostReceipt:       UnavailableClock(),
		Meta:              map[string]string{},
	}
	diag.Raw.Format = format
	diag.Raw.TextEncoding = "utf-8"
	return diag
}

func iosPID(n int) (int, string) {
	if n <= 0 {
		return 0, IdentityUnavailable
	}
	return n, IdentityObserved
}

func iosGeneration(pidStatus string) Identity {
	if pidStatus != IdentityObserved {
		return UnavailableIdentity()
	}
	// A PID alone does not survive process restart. Nothing in the text
	// formats proves a generation.
	return Identity{Status: IdentityUnverified}
}

func iosTimeBasis(c Clock) string {
	if c.Source == "" || c.Source == "unavailable" {
		return "none"
	}
	return "device_time"
}

func normalizeIOSSeverity(level string) (severity, platform string) {
	platform = level
	switch strings.ToLower(level) {
	case "debug":
		return "debug", platform
	case "info":
		return "info", platform
	case "default", "useraction":
		return "info", platform
	case "error":
		return "error", platform
	case "fault":
		return "fatal", platform
	default:
		if level == "" {
			return "unknown", ""
		}
		return "unknown", platform
	}
}

func parseIOSWall(s string) (time.Time, Clock, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, UnavailableClock(), false
	}
	prec := "second"
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		rest := s[dot+1:]
		digits := 0
		for _, r := range rest {
			if r < '0' || r > '9' {
				break
			}
			digits++
		}
		prec = precisionName(digits)
	}
	layouts := []string{
		"2006-01-02 15:04:05.000000000-0700",
		"2006-01-02 15:04:05.000000-0700",
		"2006-01-02 15:04:05.000-0700",
		"2006-01-02 15:04:05-0700",
	}
	var parsed time.Time
	var err error
	for _, layout := range layouts {
		parsed, err = time.Parse(layout, s)
		if err == nil {
			break
		}
	}
	if err != nil {
		return time.Time{}, UnavailableClock(), false
	}
	zone := "unavailable"
	verified := false
	if iosZoneRE.MatchString(s) {
		zone = parsed.Format("-07:00")
		verified = true
	}
	return parsed, Clock{
		Time:      parsed.Format(time.RFC3339Nano),
		Source:    "device_log_text",
		Timezone:  zone,
		Precision: prec,
		Verified:  verified,
	}, true
}

// DiagnosticFromOSTrace maps a go-ios os_trace_relay entry. The packet's
// unix seconds are the device event time. The original binary packet is
// not retained; native_packet says so.
func DiagnosticFromOSTrace(e ostrace.LogEntry) *Diagnostic {
	sev, platformSev := normalizeIOSSeverity(e.Level.String())
	diag := iosBase(sev, e.Message, "ios_os_trace_relay")
	diag.Process = ObservedIdentity(e.ImageName)
	diag.Parse = ParseInfo{Status: "ok"}
	if e.Timestamp.IsZero() {
		diag.DeviceTime = UnavailableClock()
	} else {
		diag.DeviceTime = Clock{
			Time:      e.Timestamp.UTC().Format(time.RFC3339Nano),
			Source:    "device_unix",
			Timezone:  "UTC",
			Precision: "microsecond",
			Verified:  true,
		}
	}
	diag.PID, diag.PIDStatus = iosPID(int(e.PID))
	diag.TID, diag.TIDStatus = iosPID(int(e.ThreadID))
	diag.ProcessGeneration = iosGeneration(diag.PIDStatus)
	diag.Meta = metaPut(diag.Meta, "channel", "os_trace_relay")
	diag.Meta = metaPut(diag.Meta, "platform_severity", platformSev)
	diag.Meta = metaPut(diag.Meta, "timestamp_basis", iosTimeBasis(diag.DeviceTime))
	diag.Meta = metaPut(diag.Meta, "native_packet", "not_retained")
	if e.Label != nil {
		diag.Meta = metaPut(diag.Meta, "subsystem", e.Label.Subsystem)
		diag.Meta = metaPut(diag.Meta, "category", e.Label.Category)
	}
	setRawText(&diag.Raw, e.Message)
	return diag
}

// DiagnosticFromActivityTrace maps one DTX activitytracetap row. Mach
// absolute time is not converted: device_time stays unverified and the
// legacy Timestamp is the host receipt, which timestamp_basis names.
func DiagnosticFromActivityTrace(rec oslog.Record, received time.Time) *Diagnostic {
	sev, platformSev := normalizeIOSSeverity(rec.MessageType)
	diag := iosBase(sev, rec.Message, "ios_activitytracetap")
	diag.Process = ObservedIdentity(rec.ImageName)
	diag.Parse = ParseInfo{Status: "ok"}
	diag.DeviceTime = Clock{
		Source:    "device_mach_absolute_unconverted",
		Timezone:  "unavailable",
		Precision: "unknown",
	}
	diag.HostReceipt = HostClock(received, "host_receipt")
	diag.PID, diag.PIDStatus = iosPID(int(rec.PID))
	diag.TID, diag.TIDStatus = iosPID(int(rec.ThreadID))
	diag.ProcessGeneration = iosGeneration(diag.PIDStatus)
	diag.Meta = metaPut(diag.Meta, "channel", "activitytracetap")
	diag.Meta = metaPut(diag.Meta, "platform_severity", platformSev)
	diag.Meta = metaPut(diag.Meta, "subsystem", rec.Subsystem)
	diag.Meta = metaPut(diag.Meta, "category", rec.Category)
	diag.Meta = metaPut(diag.Meta, "timestamp_basis", "host_receipt")
	diag.Meta = metaPut(diag.Meta, "native_packet", "not_retained")
	if rec.Timestamp != 0 {
		diag.Meta = metaPut(diag.Meta, "mach_absolute", strconv.FormatUint(rec.Timestamp, 10))
	}
	setRawText(&diag.Raw, rec.Message)
	return diag
}
