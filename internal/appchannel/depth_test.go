// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"
)

// DH-01: ReadFrame decodes peer bytes with msgpack, and the library's
// Skip walks nested containers by recursion. A frame inside MaxFrameBytes
// made of one-element arrays must be rejected, not overflow the stack.
func TestNestedMsgpackFrameDoesNotCrash(t *testing.T) {
	if os.Getenv("SPYDER_DH01_CHILD") == "1" {
		debug.SetMaxStack(4 << 20)
		// {"params": [[[[...nil...]]]]}. 0x91 is a one-element fixarray.
		// Far below MaxFrameBytes; the production 1 GiB stack only changes
		// how many levels fit before the same fatal overflow.
		body := []byte{0x81, 0xa6, 'p', 'a', 'r', 'a', 'm', 's'}
		body = append(body, bytes.Repeat([]byte{0x91}, 100_000)...)
		body = append(body, 0xc0)
		var hdr [4]byte
		binary.LittleEndian.PutUint32(hdr[:], uint32(len(body)))
		_, err := ReadFrame(bytes.NewReader(append(hdr[:], body...)))
		if err == nil {
			t.Fatal("decoded a 100000-deep array")
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestNestedMsgpackFrameDoesNotCrash$", "-test.count=1")
	cmd.Env = append(os.Environ(), "SPYDER_DH01_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(out), "stack exceeds") {
		t.Fatalf("listener died on a nested-array frame (%v):\n%s", err, out)
	}
}

// A frame an older app writes (WriteFrame of a plain envelope) still
// decodes. The depth limit does not change the wire format.
func TestReadFrameAcceptsOrdinaryEnvelope(t *testing.T) {
	params, err := PackParams(map[string]any{"slice": "scene", "n": 1})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, &Envelope{ID: 3, Method: "state_query", Params: params}); err != nil {
		t.Fatal(err)
	}
	out, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ordinary envelope rejected: %v", err)
	}
	if out.Method != "state_query" || out.ID != 3 {
		t.Fatalf("envelope = %+v", out)
	}
	var got map[string]any
	if err := UnpackParams(out.Params, &got); err != nil {
		t.Fatal(err)
	}
	if got["slice"] != "scene" {
		t.Fatalf("params = %#v", got)
	}
}

func TestMsgpackDepthBoundary(t *testing.T) {
	// 256 nested one-element arrays are inside the limit. Unmarshal into
	// an envelope fails because the body is an array, and that failure
	// is a decode error, not a crash.
	ok := nestedArrays(maxMsgpackDepth, []byte{0xc0})
	if err := checkMsgpackDepth(ok, maxMsgpackDepth); err != nil {
		t.Fatalf("depth %d rejected: %v", maxMsgpackDepth, err)
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(ok)))
	if _, err := ReadFrame(bytes.NewReader(append(hdr[:], ok...))); err == nil {
		t.Fatal("array body decoded as an envelope")
	}

	tooDeep := nestedArrays(maxMsgpackDepth+1, []byte{0xc0})
	if err := checkMsgpackDepth(tooDeep, maxMsgpackDepth); err == nil {
		t.Fatal("depth 257 accepted")
	}
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(tooDeep)))
	if _, err := ReadFrame(bytes.NewReader(append(hdr[:], tooDeep...))); err == nil {
		t.Fatal("depth 257 decoded")
	}

	// array32 / map16 heads take the same limit.
	var array32 []byte
	array32 = append(array32, 0xdd, 0, 0, 0, 1)
	array32 = append(array32, nestedArrays(maxMsgpackDepth, []byte{0xc0})...)
	if err := checkMsgpackDepth(array32, maxMsgpackDepth); err == nil {
		t.Fatal("array32 past the limit accepted")
	}

	// A declared length with no body ends as a short read, not a hang.
	huge := []byte{0xdd, 0xff, 0xff, 0xff, 0xff}
	if err := checkMsgpackDepth(huge, maxMsgpackDepth); err == nil {
		t.Fatal("array32 of 2^32-1 with no elements accepted")
	}

	// Nested params inside a real envelope still unpack.
	params := nestedArrays(8, []byte{0x01})
	raw := msgpackRawParams(t, params)
	var env Envelope
	if err := unmarshalBounded(raw, &env); err != nil {
		t.Fatal(err)
	}
	var got any
	if err := UnpackParams(env.Params, &got); err != nil {
		t.Fatal(err)
	}
}

func nestedArrays(depth int, leaf []byte) []byte {
	body := make([]byte, 0, depth+len(leaf))
	for i := 0; i < depth; i++ {
		body = append(body, 0x91)
	}
	return append(body, leaf...)
}

func msgpackRawParams(t *testing.T, params []byte) []byte {
	t.Helper()
	// fixmap of 1, key "params", then the raw params value.
	body := []byte{0x81, 0xa6, 'p', 'a', 'r', 'a', 'm', 's'}
	return append(body, params...)
}
