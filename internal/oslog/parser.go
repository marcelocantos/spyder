// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package oslog decodes the binary stream emitted by Apple's
// `com.apple.instruments.server.services.activitytracetap` DTX channel
// — the same path Xcode Console.app uses to surface live OSLog entries
// from an iOS device, including third-party app emissions that the
// lockdown-level os_trace_relay service filters out.
//
// The wire format is a stack-based virtual machine. Each message
// frame contains a sequence of 16-bit little-endian words; the high
// byte of a word is an opcode (table reset, define-table, end-row,
// struct, copy, sentinel, debug, placeholder-count, no-op) and the
// low byte carries an opcode-specific operand. Words whose top two
// bits are not 0b11 are pushed onto the stack as the leading 14-bit
// chunk of a variable-length immediate value that continues until a
// word with the 0b11 prefix terminates it. Tables are defined by
// consuming four stack items (the last is a list of column
// descriptors); rows are flushed by popping len(columns) items off
// the stack and presenting them as a record according to the most
// recently defined table.
//
// Protocol reverse-engineering reference: pymobiledevice3's
// activity_trace_tap.py. No pmd3 code or behaviour ships in spyder;
// this package is a clean-room port using go-ios primitives.
package oslog

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Opcode constants, in the high byte of each 16-bit word.
const (
	cmdDefineTable           = 1
	cmdEndRow                = 2
	cmdConvertMachContinuous = 5
	cmdTableReset            = 0x64
	cmdCopy                  = 0x65
	cmdSentinel              = 0x68
	cmdStruct                = 0x69
	cmdPlaceholderCount      = 0x6A
	cmdDebug                 = 0x6B
)

// stackItem represents one entry on the parser's value stack. The
// stream pushes raw byte sequences (variable-length immediates) plus
// occasional `nil` sentinels and structured tuples (slices of items).
type stackItem any

// table is a column-descriptor set defined by CMD_DEFINE_TABLE. Rows
// emitted under a given generation reference the columns of the table
// at that generation index.
type table struct {
	name    string
	columns []string
}

// Record is one decoded log row. Only the most common, useful fields
// are surfaced; the raw column map is available via Columns for less
// common cases. Fields that aren't present in a given row stay at
// their zero value.
type Record struct {
	PID         uint32
	ThreadID    uint32
	Timestamp   uint64 // mach absolute time; convert with MachTimebase.WallTime
	MessageType string // "default", "info", "debug", "error", "fault", "signpost" (rare)
	Subsystem   string
	Category    string
	ImageName   string // sender_image_path basename — the executable name
	Message     string // decoded message-format expansion

	// Columns is the raw decoded column map, useful when a row carries
	// fields not listed above.
	Columns map[string]any
}

// Decoder consumes one message frame (an opaque blob delivered by the
// DTX activitytracetap channel) and yields zero or more Records via
// its Decode method. The decoder maintains stack, table, and
// PID-to-image-name state across multiple frames in the same stream
// — feed every frame through the same Decoder instance, not a fresh
// one per frame.
//
// Activitytracetap on iOS 17+ ships a sparse log schema:
// `[time, process, thread, message]` — the process column is a
// 4-byte little-endian PID, NOT the image path. The image-name
// resolution happens via a separate side-channel table whose rows
// carry pid → name bindings; the decoder remembers those bindings
// and stamps subsequent log rows with the resolved name. Bindings
// stay valid for the lifetime of the stream (and the corresponding
// process on the device).
type Decoder struct {
	stack      []stackItem
	tables     []table
	generation int
	pidToName  map[uint32]string
}

// NewDecoder returns an initialised Decoder. The zero-value Decoder
// is also usable.
func NewDecoder() *Decoder { return &Decoder{} }

// Decode processes one frame, appending any complete rows to out and
// returning the result. Frames that produce no rows (e.g. table
// definitions only) return out unchanged. Heartbeat frames whose
// payload starts with `bplist` should be filtered by the caller before
// invoking Decode — they are out-of-band keepalives, not opcode
// streams, and would otherwise produce nonsense.
func (d *Decoder) Decode(frame []byte, out []Record) ([]Record, error) {
	r := &frameReader{buf: frame}
	for {
		word, err := r.readWord()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		opcode := word >> 8
		switch opcode {
		case cmdTableReset:
			d.stack = d.stack[:0]
			d.generation++
		case cmdSentinel:
			d.stack = append(d.stack, nil)
		case cmdStruct:
			distance := int(word & 0xFF)
			if distance == 0xFF {
				return out, fmt.Errorf("oslog: long struct opcode not implemented")
			}
			if distance > len(d.stack) {
				return out, fmt.Errorf("oslog: struct underflow distance=%d stack=%d", distance, len(d.stack))
			}
			tuple := make([]stackItem, distance)
			copy(tuple, d.stack[len(d.stack)-distance:])
			d.stack = append(d.stack[:len(d.stack)-distance], tuple)
		case cmdDefineTable:
			const distance = 4
			if distance > len(d.stack) {
				return out, fmt.Errorf("oslog: define-table underflow stack=%d", len(d.stack))
			}
			args := d.stack[len(d.stack)-distance:]
			t := table{
				name:    nullTerminated(asBytes(args[2])),
				columns: decodeColumnNames(asTuple(args[3])),
			}
			d.stack = d.stack[:len(d.stack)-distance]
			d.tables = append(d.tables, t)
		case cmdDebug:
			// Self-check: top of stack is a back-reference to its own
			// stack index. Pop without acting (we'd otherwise need the
			// reference for diagnostics).
			if len(d.stack) == 0 {
				return out, fmt.Errorf("oslog: debug underflow")
			}
			d.stack = d.stack[:len(d.stack)-1]
		case cmdCopy:
			distance := int(word & 0xFF)
			if distance == 0xFF {
				// long form: top-of-stack carries the back-reference
				if len(d.stack) == 0 {
					return out, fmt.Errorf("oslog: copy long-form underflow")
				}
				ref := leUint64(asBytes(d.stack[len(d.stack)-1])) - 1
				d.stack = d.stack[:len(d.stack)-1]
				if int(ref) >= len(d.stack) {
					return out, fmt.Errorf("oslog: copy long-form ref %d >= stack %d", ref, len(d.stack))
				}
				d.stack = append(d.stack, d.stack[ref])
			} else {
				idx := len(d.stack) - distance - 1
				if idx < 0 || idx >= len(d.stack) {
					return out, fmt.Errorf("oslog: copy underflow distance=%d stack=%d", distance, len(d.stack))
				}
				d.stack = append(d.stack, d.stack[idx])
			}
		case cmdEndRow:
			gen := int(word & 0xFF)
			if gen >= len(d.tables) {
				return out, fmt.Errorf("oslog: end-row references unknown table generation=%d (have %d)", gen, len(d.tables))
			}
			t := d.tables[gen]
			ncol := len(t.columns)
			if ncol > len(d.stack) {
				return out, fmt.Errorf("oslog: end-row underflow ncol=%d stack=%d", ncol, len(d.stack))
			}
			rowItems := make([]stackItem, ncol)
			copy(rowItems, d.stack[len(d.stack)-ncol:])
			d.stack = d.stack[:len(d.stack)-ncol]
			// Some rows are pid → name bindings, not log entries.
			// Update the lookup table and skip emit. Bindings travel
			// in tables with column lists containing both `process`
			// (the pid) and `name` but no `message` column.
			if !hasColumn(t.columns, "message") && hasColumn(t.columns, "name") {
				d.recordPidBinding(t.columns, rowItems)
				continue
			}
			rec, ok := buildRecord(t.columns, rowItems)
			if ok {
				if rec.ImageName == "" && rec.PID != 0 {
					if name, found := d.pidToName[rec.PID]; found {
						rec.ImageName = name
					}
				}
				out = append(out, rec)
			}
		case cmdPlaceholderCount:
			count := int(word & 0xFF)
			if count > len(d.stack) {
				return out, fmt.Errorf("oslog: placeholder-count underflow count=%d stack=%d", count, len(d.stack))
			}
			d.stack = d.stack[:len(d.stack)-count]
		case cmdConvertMachContinuous:
			// No-op (push-then-pop in the original).
		default:
			// Not an opcode — this word starts a variable-length push.
			imm, err := readImmediate(r, word)
			if err != nil {
				return out, err
			}
			d.stack = append(d.stack, imm)
		}
	}
}

// frameReader walks a frame as a stream of little-endian 16-bit words.
type frameReader struct {
	buf []byte
	pos int
}

func (r *frameReader) readWord() (uint16, error) {
	if r.pos+2 > len(r.buf) {
		return 0, io.EOF
	}
	w := binary.LittleEndian.Uint16(r.buf[r.pos:])
	r.pos += 2
	return w, nil
}

// readImmediate consumes a variable-length push that started at `first`.
// Each contributing word carries 14 payload bits in its low 14; the
// terminator is signalled by the high two bits being 0b11. The
// accumulated bit-stream is emitted MSB-first as a big-endian byte
// slice, padded with zero bits at the LSB end to byte-align.
//
// Implementation: a sliding-window bit buffer drains full bytes off
// the top as soon as enough bits accumulate. The window never holds
// more than 14 (incoming) + 7 (carry-over) = 21 bits, so a uint64
// accumulator has comfortable headroom. The pre-v0.40 path used
// math/big.Int to "be safe" against long pushes, but the encoding is
// concatenation, not arithmetic — there's no value in arbitrary
// precision once you flush bytes out the top of the window as they
// complete.
func readImmediate(r *frameReader, first uint16) ([]byte, error) {
	const tailMask = 0xC000

	if first>>14 != 0b10 && first>>14 != 0b11 {
		return nil, fmt.Errorf("oslog: unexpected push word prefix 0x%04x", first)
	}

	var out []byte
	var bits uint64 // sliding window, low-aligned
	var n int       // bit count currently in window

	word := first
	for {
		bits = (bits << 14) | uint64(word&0x3FFF)
		n += 14
		for n >= 8 {
			n -= 8
			out = append(out, byte(bits>>n))
			bits &= (1 << n) - 1
		}
		if word&tailMask == 0xC000 {
			break
		}
		next, err := r.readWord()
		if err != nil {
			return nil, fmt.Errorf("oslog: truncated push: %w", err)
		}
		word = next
	}
	// Pad remaining bits to a byte boundary (zeros at the LSB end).
	if n > 0 {
		out = append(out, byte(bits<<(8-n)))
	}
	return out, nil
}

// nullTerminated truncates a NUL-terminated byte slice and returns
// the resulting string. Used for column names and table names which
// arrive with trailing NUL padding.
func nullTerminated(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// asBytes returns the underlying byte slice for a stack item that is
// a leaf immediate; returns nil for sentinels or tuples.
func asBytes(item stackItem) []byte {
	if item == nil {
		return nil
	}
	if b, ok := item.([]byte); ok {
		return b
	}
	return nil
}

// asTuple returns the slice of stack items for a structured item
// pushed by cmdStruct; returns nil for non-tuples.
func asTuple(item stackItem) []stackItem {
	if item == nil {
		return nil
	}
	if t, ok := item.([]stackItem); ok {
		return t
	}
	return nil
}

// decodeColumnNames pulls a list of NUL-terminated column names out
// of a tuple-of-bytes pushed by cmdStruct.
func decodeColumnNames(items []stackItem) []string {
	cols := make([]string, 0, len(items))
	for _, it := range items {
		if b := asBytes(it); b != nil {
			cols = append(cols, nullTerminated(b))
		}
	}
	return cols
}

// hasColumn returns whether a column with the given name is present.
func hasColumn(cols []string, name string) bool {
	for _, c := range cols {
		if c == name {
			return true
		}
	}
	return false
}

// recordPidBinding extracts a (pid → name) binding from a row whose
// schema is `[time, process, thread, name, value]` (or similar) and
// stores it in the decoder's lookup table. The `value` column is
// ignored — the relevant data is pid (from `process`) and name.
func (d *Decoder) recordPidBinding(cols []string, items []stackItem) {
	if d.pidToName == nil {
		d.pidToName = make(map[uint32]string)
	}
	var pid uint32
	var name string
	for i, c := range cols {
		if i >= len(items) {
			break
		}
		switch c {
		case "process":
			pid = uint32(leUint64(asBytes(items[i])))
		case "name":
			name = nullTerminated(asBytes(items[i]))
		}
	}
	if pid != 0 && name != "" {
		d.pidToName[pid] = name
	}
}

// leUint64 reads a little-endian uint64 from a byte slice, zero-
// padding on the right if the slice is shorter than 8 bytes.
func leUint64(b []byte) uint64 {
	var v uint64
	for i := 0; i < 8 && i < len(b); i++ {
		v |= uint64(b[i]) << (8 * i)
	}
	return v
}
