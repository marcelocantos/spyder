// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// DiagnosticSchemaVersion is the shared Android/iOS capture contract
// (T162). Additive LogLine.Diagnostic fields use this version. Existing
// timestamp/process/level/tag/message fields stay populated for clients
// that predate the contract.
const DiagnosticSchemaVersion = 1

// Identity statuses. A missing name is never an empty observed value.
const (
	IdentityObserved    = "observed"
	IdentityUnavailable = "unavailable"
	IdentityUnverified  = "unverified"
)

// Identity is a name or id together with whether the capture actually
// observed it. Unverified means a value is present but the stream cannot
// prove it (for example a PID that may have been reused).
type Identity struct {
	Value  string `json:"value,omitempty"`
	Status string `json:"status"`
}

// Clock is one time reading. Device event time and host receipt are
// separate values; callers must not treat them as one clock.
type Clock struct {
	// Time is RFC3339Nano when a wall reading exists.
	Time        string `json:"time,omitempty"`
	Source      string `json:"source"`
	Timezone    string `json:"timezone"`
	Precision   string `json:"precision"`
	YearAssumed bool   `json:"year_assumed,omitempty"`
	Verified    bool   `json:"verified"`
}

// RawEvidence is the original record text, addressable by byte offset
// within the capture that produced it. Base64 is set only when Text had
// to replace bytes that were not valid UTF-8.
type RawEvidence struct {
	Text         string `json:"text"`
	TextEncoding string `json:"text_encoding"`
	Base64       string `json:"base64,omitempty"`
	Format       string `json:"format"`
	ByteOffset   int64  `json:"byte_offset,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	Group        string `json:"group,omitempty"`
	ParentSeq    uint64 `json:"parent_seq,omitempty"`
}

// ParseInfo says whether normalization succeeded. Unparsed and partial
// records stay in the capture; they are not dropped.
type ParseInfo struct {
	Status string `json:"status"` // ok, partial, unparsed, multiline
	Error  string `json:"error,omitempty"`
}

// Diagnostic is schema DiagnosticSchemaVersion. Ordering inside a capture
// is Seq, assigned by the capture, not a comparison of DeviceTime.
type Diagnostic struct {
	SchemaVersion int    `json:"schema_version"`
	CaptureID     string `json:"capture_id,omitempty"`
	DeviceID      string `json:"device_id,omitempty"`
	Platform      string `json:"platform"`
	Seq           uint64 `json:"seq,omitempty"`

	Severity string `json:"severity"`
	Message  string `json:"message,omitempty"`

	App     Identity `json:"app"`
	Process Identity `json:"process"`
	Thread  Identity `json:"thread"`

	PID       int    `json:"pid,omitempty"`
	PIDStatus string `json:"pid_status"`
	TID       int    `json:"tid,omitempty"`
	TIDStatus string `json:"tid_status"`

	// ProcessGeneration distinguishes a PID across process restarts.
	// Observed only when this capture saw the start event. A bare PID is
	// unverified: Android and iOS reuse PIDs.
	ProcessGeneration Identity `json:"process_generation"`

	DeviceTime  Clock `json:"device_time"`
	HostReceipt Clock `json:"host_receipt"`

	// Meta holds platform fields that are not identities: Android tag,
	// iOS subsystem/category/channel, timestamp_basis.
	Meta map[string]string `json:"meta,omitempty"`

	Raw   RawEvidence `json:"raw"`
	Parse ParseInfo   `json:"parse"`
}

// CrashChannel states whether a native crash source is part of this
// capture. Unsupported channels stay listed so callers do not treat a
// quiet log stream as proof that no crash exists.
type CrashChannel struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Status   string `json:"status"` // supported or unsupported
	Reason   string `json:"reason,omitempty"`
}

// CrashChannels is the capture contract's crash-source list.
func CrashChannels() []CrashChannel {
	return []CrashChannel{
		{
			ID: "android_logcat_crash", Platform: "android", Status: "supported",
			Reason: "Lines from the logcat crash buffer are retained when the reader includes that buffer.",
		},
		{
			ID: "android_tombstone", Platform: "android", Status: "unsupported",
			Reason: "Tombstones under /data/tombstones require root. This capture does not read them.",
		},
		{
			ID: "ios_oslog", Platform: "ios", Status: "supported",
			Reason: "os_log messages that surface on activitytracetap or os_trace_relay are retained.",
		},
		{
			ID: "ios_ips", Platform: "ios", Status: "unsupported",
			Reason: ".ips crash reports are pulled by Crashes, not by the log capture stream.",
		},
	}
}

// UnsupportedCrashChannels returns the channels this capture does not read.
func UnsupportedCrashChannels() []CrashChannel {
	var out []CrashChannel
	for _, ch := range CrashChannels() {
		if ch.Status == "unsupported" {
			out = append(out, ch)
		}
	}
	return out
}

// ObservedIdentity marks a non-empty value as seen in the record.
func ObservedIdentity(value string) Identity {
	if value == "" {
		return UnavailableIdentity()
	}
	return Identity{Value: value, Status: IdentityObserved}
}

// UnavailableIdentity means the record did not carry this field.
func UnavailableIdentity() Identity {
	return Identity{Status: IdentityUnavailable}
}

// UnavailableClock means this side of the timeline has no reading.
func UnavailableClock() Clock {
	return Clock{Source: "unavailable", Timezone: "unavailable", Precision: "unavailable"}
}

// HostClock records a host-side observation. source is host_receipt for a
// line read from a live stream, or host_buffer_read when an entire dump
// was read at one instant.
func HostClock(at time.Time, source string) Clock {
	if at.IsZero() {
		return UnavailableClock()
	}
	return Clock{
		Time:      at.Format(time.RFC3339Nano),
		Source:    source,
		Timezone:  at.Location().String(),
		Precision: "nanosecond",
		Verified:  true,
	}
}

func setRawText(raw *RawEvidence, text string) {
	if utf8.ValidString(text) {
		raw.Text = text
		raw.TextEncoding = "utf-8"
		return
	}
	raw.Text = strings.ToValidUTF8(text, "\uFFFD")
	raw.Base64 = base64.StdEncoding.EncodeToString([]byte(text))
	raw.TextEncoding = "utf-8-lossy"
}

func metaPut(meta map[string]string, key, value string) map[string]string {
	if value == "" {
		return meta
	}
	if meta == nil {
		meta = map[string]string{}
	}
	meta[key] = value
	return meta
}

// ParseDiagnosticStream reads platform log text and writes one schema
// record per line as JSON. platform is "android" or "ios". Host receipt
// is the time this process read the line, which is not the device clock.
func ParseDiagnosticStream(platform string, r io.Reader, w io.Writer) error {
	switch platform {
	case "android", "ios":
	default:
		return errBadPlatform(platform)
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := bufio.NewWriter(w)
	enc := json.NewEncoder(out)
	var dec *AndroidLogDecoder
	if platform == "android" {
		dec = NewAndroidLogDecoder()
	}
	var offset int64
	for sc.Scan() {
		line := sc.Text()
		var diag *Diagnostic
		if platform == "android" {
			diag = dec.Push(line, offset).Diagnostic
		} else {
			diag = ParseIOSLogLine(line, offset).Diagnostic
		}
		if diag.HostReceipt.Source == "" || diag.HostReceipt.Source == "unavailable" {
			diag.HostReceipt = HostClock(time.Now(), "host_receipt")
		}
		if err := enc.Encode(diag); err != nil {
			return err
		}
		if err := out.Flush(); err != nil {
			return err
		}
		offset += int64(len(line)) + 1
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return nil
}

type platformError string

func (e platformError) Error() string { return string(e) }

func errBadPlatform(platform string) error {
	return platformError("diagnostic platform must be android or ios, got " + platform)
}
