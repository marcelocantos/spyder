// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package oslog

import (
	"bytes"
	"encoding/binary"
	"math/big"
	"strings"
	"testing"
)

// encodeImmediate is the inverse of readImmediate: it produces the
// wire bytes for a variable-length push of `data`. Used by the round-
// trip tests below. The encoder packs `data` MSB-first into chunks of
// 14 bits; the last chunk is padded with zero bits at the LSB end to
// fill 14 bits, and its word carries the 0b11 terminator prefix.
func encodeImmediate(data []byte) []byte {
	bits := len(data) * 8
	if bits == 0 {
		return nil
	}
	nchunks := (bits + 13) / 14 // ceil(bits/14)
	totalBits := nchunks * 14
	padBits := totalBits - bits

	// Treat data as a big-endian arbitrary-precision integer, then
	// left-shift by padBits to align the data to the chunk boundary.
	acc := new(big.Int).SetBytes(data)
	acc.Lsh(acc, uint(padBits))

	// Emit chunks from MSB to LSB. The terminator is the last chunk.
	out := make([]byte, 0, nchunks*2)
	mask := new(big.Int).SetUint64(0x3FFF)
	chunkVal := new(big.Int)
	for i := 0; i < nchunks; i++ {
		shift := uint(14 * (nchunks - 1 - i))
		chunkVal.Rsh(acc, shift)
		chunkVal.And(chunkVal, mask)
		word := uint16(chunkVal.Uint64()) & 0x3FFF
		if i == nchunks-1 {
			word |= 0xC000 // terminator: top 2 bits = 0b11
		} else {
			word |= 0x8000 // continuation: top 2 bits = 0b10
		}
		var be [2]byte
		binary.LittleEndian.PutUint16(be[:], word)
		out = append(out, be[:]...)
	}
	return out
}

// TestReadImmediate_RoundTrip is the regression guard for the
// variable-length push decoder. A uint64 accumulator silently
// overflowed for any push spanning >= 5 chunks (>= 70 bits accumulated)
// and truncated the leading bytes of the value. math/big.Int has no
// fixed width — these cases must round-trip cleanly now.
func TestReadImmediate_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"single byte", []byte{0x4D}},
		{"two bytes", []byte("Mu")},
		{"three bytes - exactly two chunks", []byte("Mul")},
		{"five bytes - within uint64", []byte("hello")},
		{"seven bytes - 56 bits exactly four chunks", []byte("MultiMa")},
		{"eight bytes - 64 bits at the uint64 boundary", []byte("MultiMaz")},
		{"nine bytes - first overflow case", []byte("MultiMaze")},
		{"ten bytes - 'MultiMaze' + NUL", append([]byte("MultiMaze"), 0)},
		{"sixteen bytes - the bug case", []byte("MultiMaze marble")},
		{"long path-like value", []byte("/private/var/containers/Bundle/Application/MultiMaze.app/MultiMaze")},
		{"100 bytes", bytes.Repeat([]byte{0xAB}, 100)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := encodeImmediate(tc.data)
			if len(wire) == 0 {
				t.Fatalf("encoder produced no output for %q", tc.data)
			}
			r := &frameReader{buf: wire}
			first, err := r.readWord()
			if err != nil {
				t.Fatalf("readWord: %v", err)
			}
			got, err := readImmediate(r, first)
			if err != nil {
				t.Fatalf("readImmediate: %v", err)
			}
			// The decoder pads to byte alignment; trailing zero bytes
			// beyond the data are allowed. Verify the data is the
			// PREFIX of the decoded bytes.
			if !bytes.HasPrefix(got, tc.data) {
				t.Errorf("decoded %x does not start with %x (len got=%d want=%d)",
					got, tc.data, len(got), len(tc.data))
			}
			// Any extra bytes must be zero padding.
			for i := len(tc.data); i < len(got); i++ {
				if got[i] != 0 {
					t.Errorf("non-zero pad byte at index %d: 0x%02x", i, got[i])
				}
			}
		})
	}
}

// TestReadImmediate_LongStringPreservesLeading is the explicit
// regression test for the truncation bug: a sufficiently long push
// must not lose its leading bytes. Pre-fix, "MultiMaze\0" came back
// as "ltiMaze\0" (top 20 bits dropped on uint64 overflow).
func TestReadImmediate_LongStringPreservesLeading(t *testing.T) {
	data := append([]byte("MultiMaze"), 0)
	wire := encodeImmediate(data)
	r := &frameReader{buf: wire}
	first, _ := r.readWord()
	got, err := readImmediate(r, first)
	if err != nil {
		t.Fatalf("readImmediate: %v", err)
	}
	if !bytes.HasPrefix(got, []byte("MultiMaze")) {
		t.Fatalf("leading bytes lost: got %q, want prefix %q", got, "MultiMaze")
	}
	// Also confirm `nullTerminated` returns the right string.
	if s := nullTerminated(got); s != "MultiMaze" {
		t.Errorf("nullTerminated(%q) = %q; want \"MultiMaze\"", got, s)
	}
}

// TestNullTerminated_LeadingNul guards against a different
// regression: when the column data has many leading NUL bytes
// followed by the actual string, nullTerminated returns the empty
// string (matching pmd3's decode_str semantics). This is by design —
// such data isn't a regular C-string and the caller is expected to
// look at the un-truncated bytes if they want the trailing content.
func TestNullTerminated_LeadingNul(t *testing.T) {
	in := append(bytes.Repeat([]byte{0}, 5), []byte("World")...)
	if got := nullTerminated(in); got != "" {
		t.Errorf("nullTerminated of leading-NUL input = %q; want \"\"", got)
	}
}

// TestDecodeMessageFormat handles the common (type, data) pairs that
// arrive in the `message` column. Strings concatenate; uint64 values
// stringify in the requested base; private fields render as "<private>".
func TestDecodeMessageFormat(t *testing.T) {
	pairs := []stackItem{
		[]stackItem{[]byte("string\x00"), []byte("hello ")},
		[]stackItem{[]byte("private\x00"), []byte(nil)},
		[]stackItem{[]byte("string\x00"), []byte(" world")},
	}
	got := decodeMessageFormat(pairs)
	if !strings.Contains(got, "hello") || !strings.Contains(got, "<private>") || !strings.Contains(got, "world") {
		t.Errorf("decodeMessageFormat = %q; want concatenation including hello, <private>, world", got)
	}
}
