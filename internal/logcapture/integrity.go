// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package logcapture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/marcelocantos/spyder/internal/device"
	"github.com/marcelocantos/spyder/internal/paths"
)

// FreshAnchor is the first record whose message matched the capture's
// fresh-run marker. Correlation uses Seq, not a wall-clock comparison.
type FreshAnchor struct {
	Seq        uint64 `json:"seq"`
	ByteOffset int64  `json:"byte_offset"`
	Message    string `json:"message"`
	Tag        string `json:"tag,omitempty"`
}

// CaptureIntegrity is the explicit outcome of a capture. Zero counters
// are present so a limit or a parse failure is not an omitted field.
type CaptureIntegrity struct {
	SchemaVersion            int                   `json:"schema_version"`
	DroppedLines             int                   `json:"dropped_lines"`
	DurableBytes             int64                 `json:"durable_bytes"`
	DurablePath              string                `json:"durable_path,omitempty"`
	DurableTruncated         bool                  `json:"durable_truncated"`
	DurableDropped           int                   `json:"durable_dropped"`
	DurableError             string                `json:"durable_error,omitempty"`
	TransportError           string                `json:"transport_error,omitempty"`
	Incomplete               bool                  `json:"incomplete"`
	Cancelled                bool                  `json:"cancelled"`
	ReaderReaped             bool                  `json:"reader_reaped"`
	ParseErrors              int                   `json:"parse_errors"`
	UnparsedRecords          int                   `json:"unparsed_records"`
	Ordering                 string                `json:"ordering"`
	FreshAnchor              *FreshAnchor          `json:"fresh_anchor,omitempty"`
	UnsupportedCrashChannels []device.CrashChannel `json:"unsupported_crash_channels"`
}

func (s *Session) openDurable() {
	dir := paths.CaptureDir(s.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.durableErr = err.Error()
		s.incomplete = true
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "raw.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		s.durableErr = err.Error()
		s.incomplete = true
		return
	}
	s.raw = f
	s.rawPath = f.Name()
}

func (s *Session) closeDurable() {
	if s.raw != nil {
		_ = s.raw.Close()
		s.raw = nil
	}
}

func (s *Session) annotateLocked(ll device.LogLine) device.LogLine {
	if ll.Diagnostic == nil {
		ll.Diagnostic = legacyDiagnostic(ll)
	}
	s.seq++
	ll.Diagnostic.SchemaVersion = device.DiagnosticSchemaVersion
	ll.Diagnostic.Seq = s.seq
	ll.Diagnostic.CaptureID = s.ID
	ll.Diagnostic.DeviceID = s.DeviceID
	if s.platform != "" {
		ll.Diagnostic.Platform = s.platform
	}
	if ll.Diagnostic.HostReceipt.Source == "" || ll.Diagnostic.HostReceipt.Source == "unavailable" {
		ll.Diagnostic.HostReceipt = device.HostClock(time.Now(), "host_receipt")
	}
	switch ll.Diagnostic.Parse.Status {
	case "unparsed":
		s.unparsed++
		s.parseErrors++
	case "partial":
		s.parseErrors++
	}
	s.writeDurableLocked(&ll)
	s.noteMarkerLocked(ll)
	return ll
}

func (s *Session) writeDurableLocked(ll *device.LogLine) {
	text := ll.Message
	if ll.Diagnostic != nil && ll.Diagnostic.Raw.Text != "" {
		text = ll.Diagnostic.Raw.Text
	}
	payload := text + "\n"
	if s.raw == nil {
		return
	}
	if s.durableTruncated || s.rawBytes+int64(len(payload)) > int64(s.MaxBytes) {
		s.durableTruncated = true
		s.incomplete = true
		s.durableDropped++
		if ll.Diagnostic != nil {
			ll.Diagnostic.Raw.Truncated = true
		}
		return
	}
	if _, err := s.raw.WriteString(payload); err != nil {
		s.durableErr = err.Error()
		s.incomplete = true
		return
	}
	if ll.Diagnostic != nil {
		ll.Diagnostic.Raw.ByteOffset = s.rawBytes
	}
	s.rawBytes += int64(len(payload))
}

func (s *Session) noteMarkerLocked(ll device.LogLine) {
	if s.anchor != nil || s.marker == "" {
		return
	}
	msg := ll.Message
	tag := ll.Tag
	if ll.Diagnostic != nil {
		if ll.Diagnostic.Message != "" {
			msg = ll.Diagnostic.Message
		}
		if ll.Diagnostic.Meta != nil && ll.Diagnostic.Meta["tag"] != "" {
			tag = ll.Diagnostic.Meta["tag"]
		}
	}
	if msg != s.marker || (s.markerTag != "" && tag != s.markerTag) {
		return
	}
	anchor := &FreshAnchor{Message: msg, Tag: tag}
	if ll.Diagnostic != nil {
		anchor.Seq = ll.Diagnostic.Seq
		anchor.ByteOffset = ll.Diagnostic.Raw.ByteOffset
	}
	s.anchor = anchor
}

func (s *Session) noteStreamEnd(ctx context.Context, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readerReaped = true
	if ctx.Err() != nil {
		s.cancelled = true
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		s.transportErr = err.Error()
		s.incomplete = true
	}
}

func (s *Session) integrityLocked(dropped int) CaptureIntegrity {
	var anchor *FreshAnchor
	if s.anchor != nil {
		copy := *s.anchor
		anchor = &copy
	}
	return CaptureIntegrity{
		SchemaVersion:            device.DiagnosticSchemaVersion,
		DroppedLines:             dropped,
		DurableBytes:             s.rawBytes,
		DurablePath:              s.rawPath,
		DurableTruncated:         s.durableTruncated,
		DurableDropped:           s.durableDropped,
		DurableError:             s.durableErr,
		TransportError:           s.transportErr,
		Incomplete:               s.incomplete,
		Cancelled:                s.cancelled,
		ReaderReaped:             s.readerReaped,
		ParseErrors:              s.parseErrors,
		UnparsedRecords:          s.unparsed,
		Ordering:                 "seq",
		FreshAnchor:              anchor,
		UnsupportedCrashChannels: device.UnsupportedCrashChannels(),
	}
}

func legacyDiagnostic(ll device.LogLine) *device.Diagnostic {
	diag := &device.Diagnostic{
		SchemaVersion:     device.DiagnosticSchemaVersion,
		Severity:          ll.Level,
		Message:           ll.Message,
		App:               device.UnavailableIdentity(),
		Process:           device.ObservedIdentity(ll.Process),
		Thread:            device.UnavailableIdentity(),
		PIDStatus:         device.IdentityUnavailable,
		TIDStatus:         device.IdentityUnavailable,
		ProcessGeneration: device.UnavailableIdentity(),
		DeviceTime:        device.UnavailableClock(),
		HostReceipt:       device.UnavailableClock(),
		Parse:             device.ParseInfo{Status: "ok"},
		Meta:              map[string]string{"timestamp_basis": "legacy"},
	}
	if ll.Process == "" {
		diag.Process = device.UnavailableIdentity()
	}
	if !ll.Timestamp.IsZero() {
		diag.DeviceTime = device.Clock{
			Time:      ll.Timestamp.Format(time.RFC3339Nano),
			Source:    "legacy_timestamp",
			Timezone:  "unspecified",
			Precision: "unknown",
		}
		diag.Meta["timestamp_basis"] = "legacy_timestamp"
	}
	if ll.Tag != "" {
		diag.Meta["tag"] = ll.Tag
	}
	diag.Raw.Format = "legacy"
	diag.Raw.Text = ll.Message
	diag.Raw.TextEncoding = "utf-8"
	return diag
}
