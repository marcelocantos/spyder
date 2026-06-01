// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

func TestRoundTripRequest(t *testing.T) {
	params, err := PackParams(map[string]any{"slice": "scene"})
	if err != nil {
		t.Fatalf("PackParams: %v", err)
	}
	in := &Envelope{ID: 42, Method: "state_query", Params: params}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, in); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	out, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !out.IsRequest() {
		t.Errorf("IsRequest false; want true (%+v)", out)
	}
	if out.ID != 42 || out.Method != "state_query" {
		t.Errorf("envelope = %+v", out)
	}
	var got map[string]string
	if err := UnpackParams(out.Params, &got); err != nil {
		t.Fatalf("UnpackParams: %v", err)
	}
	if got["slice"] != "scene" {
		t.Errorf("params.slice = %q; want scene", got["slice"])
	}
}

func TestRoundTripPush(t *testing.T) {
	in := &Envelope{Method: "log"} // ID == 0 → push
	var buf bytes.Buffer
	if err := WriteFrame(&buf, in); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	out, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !out.IsPush() {
		t.Errorf("IsPush false; want true (%+v)", out)
	}
}

func TestRoundTripResponse(t *testing.T) {
	res, _ := PackParams(map[string]int{"pid": 1234})
	in := &Envelope{ID: 7, Result: res}
	var buf bytes.Buffer
	_ = WriteFrame(&buf, in)
	out, _ := ReadFrame(&buf)
	if !out.IsResponse() {
		t.Errorf("IsResponse false; want true (%+v)", out)
	}
}

func TestErrorEnvelope(t *testing.T) {
	in := &Envelope{ID: 9, Error: &RPCError{Code: ErrCodeMethodNotFound, Message: "unknown method foo"}}
	var buf bytes.Buffer
	_ = WriteFrame(&buf, in)
	out, _ := ReadFrame(&buf)
	if out.Error == nil {
		t.Fatalf("Error nil; want set")
	}
	if out.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("code = %d; want %d", out.Error.Code, ErrCodeMethodNotFound)
	}
}

func TestRejectsOversizedFrame(t *testing.T) {
	// Synthesize a frame header claiming the body is larger than MaxFrameBytes.
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], MaxFrameBytes+1)
	_, err := ReadFrame(bytes.NewReader(hdr[:]))
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
}

func TestShortFrameEOF(t *testing.T) {
	// Just a length prefix, no body.
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], 16)
	_, err := ReadFrame(bytes.NewReader(hdr[:]))
	if err == nil {
		t.Fatal("expected error for short body")
	}
}

func TestEOFBeforeFrame(t *testing.T) {
	_, err := ReadFrame(bytes.NewReader(nil))
	if !errors.Is(err, io.EOF) {
		t.Errorf("err = %v; want io.EOF", err)
	}
}

func TestMalformedMessagePack(t *testing.T) {
	var buf bytes.Buffer
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], 5)
	buf.Write(hdr[:])
	buf.Write([]byte{0xc1, 0xc1, 0xc1, 0xc1, 0xc1}) // invalid msgpack
	_, err := ReadFrame(&buf)
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestHelloRoundTrip(t *testing.T) {
	helloParams, _ := PackParams(Hello{
		AppName:    "tiltbuggy",
		AppVersion: "0.13.0",
		Methods:    []string{"ping", "quit", "state_query"},
	})
	env := &Envelope{ID: 1, Method: MethodHello, Params: helloParams}
	var buf bytes.Buffer
	_ = WriteFrame(&buf, env)
	out, _ := ReadFrame(&buf)
	var hello Hello
	if err := UnpackParams(out.Params, &hello); err != nil {
		t.Fatalf("UnpackParams: %v", err)
	}
	if hello.AppName != "tiltbuggy" || hello.AppVersion != "0.13.0" {
		t.Errorf("hello = %+v", hello)
	}
	if len(hello.Methods) != 3 {
		t.Errorf("methods = %v; want 3 entries", hello.Methods)
	}
}

func TestKnownMethodsCovered(t *testing.T) {
	// Sanity: KnownMethods should include each of the standard method
	// constants except hello (which is implicit).
	want := []string{
		MethodPing, MethodQuit, MethodFlush, MethodBackgrounded,
		MethodForegrounded, MethodLowMemoryWarning, MethodPause,
		MethodResume, MethodStep, MethodSpeed, MethodInputInject,
		MethodStateQuery, MethodSaveState, MethodRestoreState,
		MethodScreenshotApp,
	}
	have := map[string]bool{}
	for _, m := range KnownMethods {
		have[m] = true
	}
	for _, m := range want {
		if !have[m] {
			t.Errorf("KnownMethods missing %q", m)
		}
	}
}

// Defensive: the msgpack import should remain a direct dep (catch
// accidental removal). If this breaks, msgpack is gone from imports.
func TestMsgpackImportLive(t *testing.T) {
	var raw msgpack.RawMessage = []byte{0xc0} // nil
	if len(raw) != 1 {
		t.Fail()
	}
}
