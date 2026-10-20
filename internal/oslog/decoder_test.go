// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package oslog

import (
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// This synthetic corpus includes table definitions, a PID binding, stack
// copies, and two rows. Its expected values are independent of frame splits.
func incrementalCorpus() []byte {
	return buildFrame(
		defineTableFrame("Processes", []string{"process", "name"}),
		encodeImmediate([]byte{42, 0, 0, 0}), pushString("MyApp"), word16(cmdEndRow, 0),
		defineTableFrame("Logs", []string{"process", "message"}),
		encodeImmediate([]byte{42, 0, 0, 0}), word16(cmdCopy, 0), word16(cmdPlaceholderCount, 1),
		pushString("narrative-text"), encodeImmediate([]byte("got it")), word16(cmdStruct, 2), word16(cmdStruct, 1), word16(cmdEndRow, 1),
		encodeImmediate([]byte{42, 0, 0, 0}),
		pushString("narrative-text"), encodeImmediate([]byte("second")), word16(cmdStruct, 2), word16(cmdStruct, 1), word16(cmdEndRow, 1),
	)
}

// Synthetic wire schema: process=(pid, opaque), thread=(tid, process).
// Only the first direct scalar carries each identity. No real device data.
func tupleIdentityCorpus() []byte {
	return buildFrame(
		defineTableFrame("Logs", []string{"process", "thread", "process-image-path", "message"}),
		encodeImmediate([]byte{42}), encodeImmediate([]byte{1, 2, 3, 4, 5}), word16(cmdStruct, 2),
		encodeImmediate([]byte{84}), word16(cmdCopy, 1), word16(cmdStruct, 2),
		pushString("/private/Test/StockCars"),
		pushString("narrative-text"), encodeImmediate([]byte("ready")), word16(cmdStruct, 2), word16(cmdStruct, 1), word16(cmdEndRow, 0),
	)
}

func TestDecoderTupleProcessAndThreadIdentity(t *testing.T) {
	corpus := tupleIdentityCorpus()
	process := []stackItem{[]byte{42, 0}, []byte{1, 2, 3, 4, 5, 0}}
	thread := []stackItem{[]byte{84, 0}, process}
	for split := 0; split <= len(corpus); split++ {
		d := NewDecoder()
		var rows []Record
		for _, part := range [][]byte{corpus[:split], corpus[split:]} {
			var err error
			rows, err = d.Decode(part, rows)
			if err != nil {
				t.Fatalf("split %d: %v", split, err)
			}
		}
		if d.Finish() != nil || len(rows) != 1 {
			t.Fatalf("split %d incomplete corpus", split)
		}
		rec := rows[0]
		if rec.PID != 42 || rec.ThreadID != 84 || rec.ImageName != "StockCars" || rec.Message != "ready" {
			t.Fatalf("split %d lost tuple identity: PID=%d TID=%d image=%q message=%q", split, rec.PID, rec.ThreadID, rec.ImageName, rec.Message)
		}
		if !reflect.DeepEqual(rec.Columns["process"], process) || !reflect.DeepEqual(rec.Columns["thread"], thread) {
			t.Fatalf("split %d changed raw identity Columns", split)
		}
	}
}

func TestDecoderIncrementalEveryFrameSplit(t *testing.T) {
	corpus := incrementalCorpus()
	baseline := NewDecoder()
	want, err := baseline.Decode(corpus, nil)
	if err != nil || len(want) != 2 || want[0].PID != 42 || want[0].ImageName != "MyApp" || want[0].Message != "got it" || want[1].Message != "second" {
		t.Fatalf("synthetic oracle mismatch: rows=%v err=%v", want, err)
	}
	for split := 0; split <= len(corpus); split++ {
		d := NewDecoder()
		var got []Record
		for _, part := range [][]byte{corpus[:split], nil, corpus[split:]} {
			got, err = d.Decode(part, got)
			if err != nil {
				t.Fatalf("split %d: %v", split, err)
			}
		}
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(d, baseline) {
			t.Fatalf("split %d changed rows or VM state", split)
		}
		if err := d.Finish(); err != nil {
			t.Fatalf("split %d failed actual EOF: %v", split, err)
		}
	}
	for _, chunk := range []int{1, 2, 3, 7, 31} {
		d := NewDecoder()
		var got []Record
		for start := 0; start < len(corpus); start += chunk {
			got, err = d.Decode(corpus[start:min(start+chunk, len(corpus))], got)
			if err != nil {
				t.Fatalf("chunk %d: %v", chunk, err)
			}
		}
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(d, baseline) || d.Finish() != nil {
			t.Fatalf("chunk %d changed rows or VM state", chunk)
		}
	}
}

func TestDecoderCompleteRowsBeforePendingInstruction(t *testing.T) {
	corpus := incrementalCorpus()
	d := NewDecoder()
	// A complete corpus followed by an unfinished push emits each completed
	// row now. Replaying that one pending push must not repeat prior rows.
	rows, err := d.Decode(append(corpus, 0x00, 0x80), nil)
	if err != nil || len(rows) != 2 {
		t.Fatalf("completed prefix: %v %v", rows, err)
	}
	rows, err = d.Decode([]byte{0x00, 0xc0}, nil)
	if err != nil || len(rows) != 0 || len(d.stack) != 1 || d.Finish() != nil {
		t.Fatalf("continuation replayed or lost VM work: rows=%v stack=%v err=%v", rows, d.stack, err)
	}
}

func TestDecoderTerminalPendingInputIsSticky(t *testing.T) {
	for _, partial := range [][]byte{{0x00}, {0x00, 0x80}, {0x00, 0x80, 0x00}} {
		d := NewDecoder()
		if _, err := d.Decode(partial, nil); err != nil {
			t.Fatal(err)
		}
		if len(d.stack) != 0 {
			t.Fatal("partial instruction mutated stack")
		}
		err := d.Finish()
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("unfinished stream passed: %v", err)
		}
		if _, next := d.Decode([]byte{0x00, 0xc0}, nil); next != err || d.Finish() != err {
			t.Fatal("continuation or cleanup erased terminal failure")
		}
	}
}

func TestDecoderMalformedInstructionsFailAcrossPartitions(t *testing.T) {
	for _, malformed := range [][]byte{word16(0x70, 0), word16(cmdStruct, 1), word16(cmdStruct, 0xff), word16(cmdCopy, 0), word16(cmdCopy, 0xff), word16(cmdDefineTable, 0), word16(cmdDebug, 0), word16(cmdEndRow, 0), word16(cmdPlaceholderCount, 1)} {
		for split := 0; split <= len(malformed); split++ {
			d := NewDecoder()
			_, err := d.Decode(malformed[:split], nil)
			if err == nil {
				_, err = d.Decode(malformed[split:], nil)
			}
			if err == nil || errors.Is(err, io.EOF) || d.Finish() != err {
				t.Fatalf("malformed instruction %x split %d did not fail sticky: %v", malformed, split, err)
			}
		}
	}
}

func TestDecoderImmediateResourceBudgetIsPartitionIndependent(t *testing.T) {
	// All-zero data chunks have continuation prefix 10. The final chunk
	// terminates exactly at the declared host budget; one extra word fails.
	valid := make([]byte, maxImmediateWireBytes)
	for i := 1; i < len(valid); i += 2 {
		valid[i] = 0x80
	}
	valid[len(valid)-1] = 0xc0
	oversize := append(append([]byte(nil), valid[:len(valid)-2]...), 0, 0x80, 0, 0xc0)
	for _, input := range [][]byte{valid, oversize} {
		for _, split := range []int{0, 1, len(input) / 2, len(input) - 1, len(input)} {
			d := NewDecoder()
			_, err := d.Decode(input[:split], nil)
			if err == nil {
				_, err = d.Decode(input[split:], nil)
			}
			if len(input) == maxImmediateWireBytes {
				if err != nil || d.Finish() != nil || len(d.stack) != 1 {
					t.Fatalf("budget boundary split %d: %v", split, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "resource budget") || d.Finish() != err || len(d.stack) != 0 {
				t.Fatalf("oversize split %d passed or mutated stack: %v", split, err)
			}
		}
	}
}

// word16 encodes a 16-bit little-endian word with the given high-byte
// opcode and low-byte operand.
func word16(opcode, operand byte) []byte {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], uint16(opcode)<<8|uint16(operand))
	return b[:]
}

// buildFrame concatenates the provided byte slices into a single frame.
func buildFrame(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// pushString encodes a NUL-terminated string as a variable-length
// immediate push. The zero byte is appended so the decoder's
// nullTerminated helper strips it correctly.
func pushString(s string) []byte {
	return encodeImmediate(append([]byte(s), 0))
}

// defineTableFrame builds a frame that defines a table with the given
// name and column names.
//
// cmdDefineTable consumes 4 stack items: args[0], args[1], name, columns.
// args[0] and args[1] are opaque unknowns; we use single-byte zero values.
// columns is a tuple produced by CMD_STRUCT(len(cols)) wrapping per-column
// name immediates.
func defineTableFrame(tableName string, cols []string) []byte {
	var parts [][]byte
	// unknown0 and unknown2 (args[0] and args[1])
	parts = append(parts, encodeImmediate([]byte{0}))
	parts = append(parts, encodeImmediate([]byte{0}))
	// table name (args[2])
	parts = append(parts, pushString(tableName))
	// column names as individual immediates, then CMD_STRUCT(n)
	for _, c := range cols {
		parts = append(parts, pushString(c))
	}
	parts = append(parts, word16(cmdStruct, byte(len(cols))))
	// CMD_DEFINE_TABLE (opcode 1, operand 0)
	parts = append(parts, word16(cmdDefineTable, 0))
	return buildFrame(parts...)
}

// TestDecoder_TableResetClearsStack verifies that CMD_TABLE_RESET
// (0x64) empties the stack regardless of how many items were pushed.
func TestDecoder_TableResetClearsStack(t *testing.T) {
	d := NewDecoder()
	frame := buildFrame(
		encodeImmediate([]byte{0x01}),
		encodeImmediate([]byte{0x02}),
		encodeImmediate([]byte{0x03}),
		word16(cmdTableReset, 0),
	)
	_, err := d.Decode(frame, nil)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(d.stack) != 0 {
		t.Errorf("stack length = %d after TABLE_RESET; want 0", len(d.stack))
	}
}

// TestDecoder_SentinelPushesNil verifies that CMD_SENTINEL pushes a
// nil item. We confirm this by observing that a define-table consuming
// 4 items (two unknowns, a name, and a zero-column struct) succeeds
// when preceded by a sentinel-filled unknown.
func TestDecoder_SentinelPushesNil(t *testing.T) {
	d := NewDecoder()
	// Push a nil sentinel; then push 3 normal items; the sentinel
	// will be args[0] (unknown0).
	frame := buildFrame(
		word16(cmdSentinel, 0),     // nil for args[0]
		encodeImmediate([]byte{0}), // args[1]
		pushString("TestTable"),    // name (args[2])
		word16(cmdStruct, 0),       // empty column tuple (args[3])
		word16(cmdDefineTable, 0),
	)
	_, err := d.Decode(frame, nil)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(d.tables) != 1 {
		t.Fatalf("expected 1 table defined, got %d", len(d.tables))
	}
	if d.tables[0].name != "TestTable" {
		t.Errorf("table name = %q; want %q", d.tables[0].name, "TestTable")
	}
}

// TestDecoder_StructGroupsItems verifies CMD_STRUCT(3) folds 3 top-of-
// stack items into a single tuple, so the stack shrinks from 3 to 1.
// We use 7-byte values: ceil(56/14)=4 chunks, so decode produces exactly
// 7 bytes with no alignment padding, making exact comparison straightforward.
func TestDecoder_StructGroupsItems(t *testing.T) {
	d := NewDecoder()
	// 7 bytes each → 4 × 14-bit chunks → 56 bits → no padding bytes.
	items := [][]byte{
		[]byte("AAAAAAA"),
		[]byte("BBBBBBB"),
		[]byte("CCCCCCC"),
	}
	frame := buildFrame(
		encodeImmediate(items[0]),
		encodeImmediate(items[1]),
		encodeImmediate(items[2]),
		word16(cmdStruct, 3),
	)
	_, err := d.Decode(frame, nil)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(d.stack) != 1 {
		t.Fatalf("stack length = %d; want 1", len(d.stack))
	}
	tuple := asTuple(d.stack[0])
	if len(tuple) != 3 {
		t.Fatalf("tuple length = %d; want 3", len(tuple))
	}
	for i, want := range items {
		got := asBytes(tuple[i])
		if string(got) != string(want) {
			t.Errorf("tuple[%d] = %q; want %q", i, got, want)
		}
	}
}

// TestDecoder_CopyDuplicatesAtDistance verifies CMD_COPY with distance=2
// duplicates the item 2 positions below the top-of-stack (i.e. index
// len(stack)-3 = item A when stack is [A, B, C]).
// We use 7-byte values so the encoded form decodes with no alignment padding.
func TestDecoder_CopyDuplicatesAtDistance(t *testing.T) {
	d := NewDecoder()
	itemA := []byte("AAAAAAA")
	frame := buildFrame(
		encodeImmediate(itemA),
		encodeImmediate([]byte("BBBBBBB")),
		encodeImmediate([]byte("CCCCCCC")),
		word16(cmdCopy, 2), // idx = len(stack)-2-1 = 3-3 = 0 → "AAAAAAA"
	)
	_, err := d.Decode(frame, nil)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(d.stack) != 4 {
		t.Fatalf("stack length = %d; want 4", len(d.stack))
	}
	top := asBytes(d.stack[len(d.stack)-1])
	if string(top) != string(itemA) {
		t.Errorf("top of stack = %q; want %q", top, itemA)
	}
}

// TestDecoder_PlaceholderCountPops verifies CMD_PLACEHOLDER_COUNT(2)
// removes the top 2 items from the stack.
func TestDecoder_PlaceholderCountPops(t *testing.T) {
	d := NewDecoder()
	frame := buildFrame(
		encodeImmediate([]byte{1}),
		encodeImmediate([]byte{2}),
		encodeImmediate([]byte{3}),
		word16(cmdPlaceholderCount, 2),
	)
	_, err := d.Decode(frame, nil)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(d.stack) != 1 {
		t.Errorf("stack length = %d; want 1", len(d.stack))
	}
}

// TestDecoder_TableAndRowRoundTrip defines a two-column table
// ("process-image-path", "message"), emits a row with a known image
// path and a simple text message, and checks the resulting Record.
func TestDecoder_TableAndRowRoundTrip(t *testing.T) {
	d := NewDecoder()

	// Frame 1: define the table (generation index 0).
	f1 := defineTableFrame("log", []string{"process-image-path", "message"})

	// Frame 2: push row values and emit CMD_END_ROW(0).
	//
	// Column 0: process-image-path — a NUL-terminated path string.
	// Column 1: message — a tuple of (type, data) pairs.
	//   We push one pair: ("narrative-text\0", "hello world")
	//   then CMD_STRUCT(2) to make the pair, then CMD_STRUCT(1) to
	//   wrap it in the outer message tuple.
	f2 := buildFrame(
		// process-image-path value
		pushString("/private/var/containers/MyApp.app/MyApp"),
		// message: inner pair — type uses pushString (NUL-terminated),
		// data uses raw encodeImmediate. "got it" is 6 bytes: ceil(48/14)=4
		// chunks → 56 bits → 7 decoded bytes → 1 trailing NUL → strip
		// produces exactly "got it".
		pushString("narrative-text"),
		encodeImmediate([]byte("got it")),
		word16(cmdStruct, 2), // one (type, data) pair
		word16(cmdStruct, 1), // outer message tuple of 1 pair
		// CMD_END_ROW referencing generation 0
		word16(cmdEndRow, 0),
	)

	var records []Record
	var err error
	records, err = d.Decode(f1, records)
	if err != nil {
		t.Fatalf("Decode(f1): %v", err)
	}
	records, err = d.Decode(f2, records)
	if err != nil {
		t.Fatalf("Decode(f2): %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("got %d records; want 1", len(records))
	}
	rec := records[0]
	if rec.ImageName != "MyApp" {
		t.Errorf("ImageName = %q; want %q", rec.ImageName, "MyApp")
	}
	if rec.Message != "got it" {
		t.Errorf("Message = %q; want %q", rec.Message, "got it")
	}
}

// TestDecoder_PidNameBinding verifies the pid→name side-channel.
// A binding-table row (has "name", no "message") is not emitted as a
// Record but is remembered; subsequent log rows with the same pid get
// their ImageName populated from the binding.
func TestDecoder_PidNameBinding(t *testing.T) {
	d := NewDecoder()

	// Table 0: binding table (has "process" and "name", no "message").
	f1 := defineTableFrame("binding", []string{"process", "name"})

	// Table 1: log table (has "process" and "message").
	f2 := defineTableFrame("log", []string{"process", "message"})

	// Frame 3: emit a binding row — pid=42 → name="MyApp".
	pid42 := make([]byte, 4)
	binary.LittleEndian.PutUint32(pid42, 42)
	f3 := buildFrame(
		encodeImmediate(pid42), // process = 42
		pushString("MyApp"),    // name
		word16(cmdEndRow, 0),   // gen 0 = binding table
	)

	// Frame 4: emit a log row — pid=42, message="got it".
	f4 := buildFrame(
		encodeImmediate(pid42),
		// message tuple: one narrative-text pair
		pushString("narrative-text"),
		pushString("got it"),
		word16(cmdStruct, 2),
		word16(cmdStruct, 1),
		word16(cmdEndRow, 1), // gen 1 = log table
	)

	var records []Record
	for i, frame := range [][]byte{f1, f2, f3, f4} {
		var err error
		records, err = d.Decode(frame, records)
		if err != nil {
			t.Fatalf("Decode(frame %d): %v", i, err)
		}
	}

	// The binding row should not appear in records; only the log row.
	if len(records) != 1 {
		t.Fatalf("got %d records; want 1", len(records))
	}
	rec := records[0]
	if rec.PID != 42 {
		t.Errorf("PID = %d; want 42", rec.PID)
	}
	if rec.ImageName != "MyApp" {
		t.Errorf("ImageName = %q; want \"MyApp\"", rec.ImageName)
	}
	if rec.Message != "got it" {
		t.Errorf("Message = %q; want \"got it\"", rec.Message)
	}
}

// TestDecoder_DefineTableHandlesEmptyColumnNames verifies that a table
// definition with empty-string column names (common in iOS 17+ schemas)
// is accepted and that a row against it decodes without panic.
//
// Empty-named columns are preserved in the column list as ""; they
// simply don't match any named field in buildRecord and end up in the
// raw Columns map under the key "". The table here has 4 columns:
// ["time", "", "", "message"], so a complete row must push 4 values.
func TestDecoder_DefineTableHandlesEmptyColumnNames(t *testing.T) {
	d := NewDecoder()

	// Table with columns ["time", "", "", "message"].
	// A NUL byte encodes as a one-byte immediate; nullTerminated returns ""
	// for it, so the column name is the empty string.
	cols := []string{"time", "", "", "message"}
	var parts [][]byte
	parts = append(parts, encodeImmediate([]byte{0})) // unknown0
	parts = append(parts, encodeImmediate([]byte{0})) // unknown2
	parts = append(parts, pushString("sparse"))       // table name
	for _, c := range cols {
		if c == "" {
			parts = append(parts, encodeImmediate([]byte{0})) // single NUL → ""
		} else {
			parts = append(parts, pushString(c))
		}
	}
	parts = append(parts, word16(cmdStruct, byte(len(cols))))
	parts = append(parts, word16(cmdDefineTable, 0))
	f1 := buildFrame(parts...)

	if _, err := d.Decode(f1, nil); err != nil {
		t.Fatalf("Decode(f1): %v", err)
	}
	if len(d.tables) != 1 {
		t.Fatalf("expected 1 table; got %d", len(d.tables))
	}
	tbl := d.tables[0]
	if len(tbl.columns) != 4 {
		t.Fatalf("expected 4 columns; got %d: %v", len(tbl.columns), tbl.columns)
	}
	if tbl.columns[0] != "time" || tbl.columns[3] != "message" {
		t.Errorf("unexpected column layout: %v", tbl.columns)
	}

	// Emit a 4-value row. The two unnamed columns get placeholder bytes.
	// "sparse log" is 10 bytes → 11 decoded bytes → 1 trailing NUL → strip
	// produces exactly "sparse log".
	tsBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(tsBytes, 12345)
	f2 := buildFrame(
		encodeImmediate(tsBytes),   // col 0: time
		encodeImmediate([]byte{0}), // col 1: "" (placeholder)
		encodeImmediate([]byte{0}), // col 2: "" (placeholder)
		// col 3: message tuple
		pushString("narrative-text"),          // type
		encodeImmediate([]byte("sparse log")), // data (10 bytes → 1 NUL pad)
		word16(cmdStruct, 2),                  // one (type, data) pair
		word16(cmdStruct, 1),                  // outer message tuple
		word16(cmdEndRow, 0),
	)
	records, err := d.Decode(f2, nil)
	if err != nil {
		t.Fatalf("Decode(f2): %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records; want 1", len(records))
	}
	if records[0].Message != "sparse log" {
		t.Errorf("Message = %q; want \"sparse log\"", records[0].Message)
	}
}
