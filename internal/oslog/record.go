// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package oslog

import (
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"
)

// buildRecord folds a row's column items into a Record.
//
// Column semantics (derived from the activitytracetap stream's
// table definitions): the row carries scalar bytes for `process` /
// `thread` / `timestamp`, NUL-terminated strings for fields like
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
			// The activitytracetap `process` column is a 4-byte
			// little-endian PID, not the image path. The image-name
			// resolution happens in the Decoder via the side-channel
			// pid-to-name table.
			rec.PID = uint32(leUint64(asBytes(v)))
		case "thread":
			rec.ThreadID = uint32(leUint64(asBytes(v)))
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
