// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package oslog

import (
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"
)

// activityIdentityMaxWireBytes follows the upstream activitytracetap integer
// representation: zero-bit elision and whole-byte padding produce 2–9 bytes.
// https://github.com/doronz88/pymobiledevice3/blob/master/pymobiledevice3/services/dvt/instruments/activity_trace_tap.py
// Record.PID and Record.ThreadID are uint32, so only four bytes may be nonzero.
const (
	activityIdentityMaxWireBytes = 9
	recordIdentityBytes          = 4
)

// activityIdentity reads only the first direct scalar of a process/thread
// tuple. The remaining tuple fields are opaque, not fallback identities.
// Legacy direct-byte scalars remain supported. Missing, malformed, over-wide,
// or overflowing values stay unavailable (zero), rather than being truncated.
func activityIdentity(value stackItem) uint32 {
	if tuple, ok := value.([]stackItem); ok {
		if len(tuple) == 0 {
			return 0
		}
		value = tuple[0]
	}
	b, ok := value.([]byte)
	if !ok || len(b) == 0 || len(b) > activityIdentityMaxWireBytes {
		return 0
	}
	for _, high := range b[min(len(b), recordIdentityBytes):] {
		if high != 0 {
			return 0
		}
	}
	return uint32(leUint64(b))
}

// buildRecord folds a row's column items into a Record.
//
// Column semantics (derived from the activitytracetap stream's
// table definitions): process/thread tuples carry their integer identity in
// the first direct byte scalar; legacy flat scalars are accepted too. Time
// carries scalar bytes, with NUL-terminated strings for fields like
// `message_type`, `subsystem`, `category`, `sender_image_path`, and a
// structured tuple-of-tuples for `message` describing a format-string
// expansion. Anything not classified gets stashed in Columns under
// its raw column name.
//
// Returns ok=false for rows that don't represent log messages (e.g.
// pid-to-exec-name mapping rows defined by other tables in the same
// stream). The caller should drop those.
func buildRecord(columns []string, items []stackItem) (Record, bool) {
	rec := Record{Columns: make(map[string]any, len(columns))}
	hasMessageField := false
	for i, col := range columns {
		if i >= len(items) {
			break
		}
		v := items[i]
		rec.Columns[col] = v
		switch col {
		case "process":
			rec.PID = activityIdentity(v)
		case "thread":
			rec.ThreadID = activityIdentity(v)
		case "time", "timestamp":
			rec.Timestamp = leUint64(asBytes(v))
		case "message_type", "message-type":
			rec.MessageType = nullTerminated(asBytes(v))
		case "subsystem":
			rec.Subsystem = nullTerminated(asBytes(v))
		case "category":
			rec.Category = nullTerminated(asBytes(v))
		case "sender_image_path", "sender-image-path",
			"process_image_path", "process-image-path":
			// The wire field is a full path like
			// /private/var/containers/Bundle/Application/<UUID>/Foo.app/Foo.
			// Spyder's log filter compares against the executable name
			// (matches Apple's `image_name` semantics), so reduce to
			// the basename. `process-image-path` (the path of the
			// emitting process) wins over `sender-image-path` (the
			// dylib/framework path where the call originated) since
			// the former matches what `list_apps` returns as
			// CFBundleExecutable.
			img := filepath.Base(nullTerminated(asBytes(v)))
			if rec.ImageName == "" || col == "process-image-path" || col == "process_image_path" {
				rec.ImageName = img
			}
		case "message":
			hasMessageField = true
			rec.Message = decodeMessageFormat(asTuple(v))
		}
	}
	return rec, hasMessageField
}

// decodeMessageFormat expands a list of (type, data) tuples produced
// by activitytracetap for the `message` column into a plain string.
// Each pair carries a type tag identifying how to interpret the data
// bytes — narrative text, a redacted private value, a uint64 in
// decimal or hex, a UUID, raw data, etc.
func decodeMessageFormat(pairs []stackItem) string {
	var b strings.Builder
	for _, pair := range pairs {
		fields := asTuple(pair)
		if len(fields) < 2 {
			continue
		}
		typ := nullTerminated(asBytes(fields[0]))
		data := fields[1]

		// Strip a single trailing NUL byte from data if present.
		dataBytes := asBytes(data)
		if len(dataBytes) > 0 && dataBytes[len(dataBytes)-1] == 0 {
			dataBytes = dataBytes[:len(dataBytes)-1]
		}

		// `address` collapses to a hex-formatted uint64.
		if typ == "address" {
			typ = "uint64-hex"
		}

		switch {
		case typ == "narrative-text" || typ == "string":
			if data == nil {
				b.WriteString("<None>")
			} else {
				b.Write(dataBytes)
			}
		case typ == "private":
			b.WriteString("<private>")
		case strings.HasPrefix(typ, "uint64"):
			v := leUint64(dataBytes)
			if strings.Contains(typ, "hex") {
				s := strconv.FormatUint(v, 16)
				if !strings.Contains(typ, "lowercase") {
					s = strings.ToUpper(s)
				} else {
					s = strings.ToLower(s)
				}
				b.WriteString(s)
			} else {
				b.WriteString(strconv.FormatUint(v, 10))
			}
		case strings.Contains(typ, "decimal"):
			b.WriteString(strconv.FormatUint(leUint64(dataBytes), 10))
		case typ == "data" || typ == "uuid":
			if data != nil {
				b.WriteString(hex.EncodeToString(dataBytes))
			}
		default:
			// Unknown type: emit the raw bytes if any, otherwise the
			// type-name as a placeholder.
			if len(dataBytes) > 0 {
				b.Write(dataBytes)
			}
		}
	}
	return b.String()
}
