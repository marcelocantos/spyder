// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package appchannel implements spyder's bidirectional RPC channel
// to running apps. Wire format is length-prefixed MessagePack frames
// carrying a JSON-RPC-shaped envelope: {id, method, params} requests
// (either direction), {id, result|error} responses, and {method,
// params} async pushes (id omitted).
//
// Apps connect to a per-session TCP listener (see Manager.Start), send
// a Hello as their first message, then service requests dispatched by
// spyder's RPC layer. Apps also push log/perf/event messages without
// waiting for spyder to ask. (🎯T75.)
package appchannel

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/vmihailenco/msgpack/v5/msgpcode"
)

// MaxFrameBytes caps any single frame on the wire to prevent runaway
// allocations from a malformed or hostile peer.
const MaxFrameBytes = 16 * 1024 * 1024

// Standard error codes, JSON-RPC-flavoured. App handlers may return
// their own codes; these are the ones spyder's framework emits.
const (
	ErrCodeParse          = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
	ErrCodeNotConnected   = -32000 // app never sent Hello or has dropped
	ErrCodeUnsupported    = -32001 // method not in app's Hello capabilities
	ErrCodeTimeout        = -32002 // app didn't respond within deadline
	ErrCodeFrameTooLarge  = -32003 // peer sent a frame larger than MaxFrameBytes
)

// Envelope is the on-wire shape. ID 0 means "no id" (push message).
// Exactly one of Method or Result/Error is meaningful on a given
// envelope; the decoder doesn't enforce that — handlers do.
type Envelope struct {
	ID     uint64             `msgpack:"id,omitempty"`
	Method string             `msgpack:"method,omitempty"`
	Params msgpack.RawMessage `msgpack:"params,omitempty"`
	Result msgpack.RawMessage `msgpack:"result,omitempty"`
	Error  *RPCError          `msgpack:"error,omitempty"`
}

// RPCError is the structured error payload.
type RPCError struct {
	Code    int                `msgpack:"code"`
	Message string             `msgpack:"message"`
	Data    msgpack.RawMessage `msgpack:"data,omitempty"`
}

// Error implements error.
func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// IsRequest reports whether the envelope is a request (has Method and ID).
func (e *Envelope) IsRequest() bool { return e.Method != "" && e.ID != 0 }

// IsPush reports whether the envelope is an async push (has Method, no ID).
func (e *Envelope) IsPush() bool { return e.Method != "" && e.ID == 0 }

// IsResponse reports whether the envelope is a response (has ID, no Method).
func (e *Envelope) IsResponse() bool { return e.Method == "" && e.ID != 0 }

// WriteFrame writes a single length-prefixed MessagePack frame to w.
// The 4-byte length is little-endian; the body is the MessagePack
// encoding of env.
func WriteFrame(w io.Writer, env *Envelope) error {
	body, err := msgpack.Marshal(env)
	if err != nil {
		return fmt.Errorf("appchannel: marshal: %w", err)
	}
	if len(body) > MaxFrameBytes {
		return fmt.Errorf("appchannel: outgoing frame %d bytes exceeds max %d", len(body), MaxFrameBytes)
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("appchannel: write length: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("appchannel: write body: %w", err)
	}
	return nil
}

// ReadFrame reads the next length-prefixed MessagePack frame from r
// and decodes it into an Envelope. Returns io.EOF when r is closed
// between frames; an unexpected EOF mid-frame is reported as a wrapped
// io.ErrUnexpectedEOF.
func ReadFrame(r io.Reader) (*Envelope, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("appchannel: short frame header: %w", err)
		}
		return nil, err
	}
	length := binary.LittleEndian.Uint32(hdr[:])
	if length > MaxFrameBytes {
		return nil, fmt.Errorf("appchannel: frame %d bytes exceeds max %d (code %d)",
			length, MaxFrameBytes, ErrCodeFrameTooLarge)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("appchannel: short frame body (want %d): %w", length, err)
	}
	var env Envelope
	if err := msgpack.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("appchannel: decode envelope: %w (code %d)", err, ErrCodeParse)
	}
	return &env, nil
}

// PackParams marshals an arbitrary value as a RawMessage suitable for
// the Params or Result fields. Convenience wrapper around msgpack.Marshal.
func PackParams(v any) (msgpack.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	b, err := msgpack.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("appchannel: pack params: %w", err)
	}
	return b, nil
}

// UnpackParams unmarshals a RawMessage into dst. dst should be a
// pointer. Returns nil if raw is empty (caller can use the dst zero
// value).
func UnpackParams(raw msgpack.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := msgpack.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("appchannel: unpack params: %w", err)
	}
	return nil
}

// SliceDescriptor names one state slice the app exposes, optionally
// with a representative example payload. Wire-compatible with both
// the bare-string form ("scene") and the struct form ({name: "scene",
// example: {...}}) — apps that don't volunteer an example send a
// plain string and DecodeMsgpack lifts it into a name-only descriptor.
type SliceDescriptor struct {
	Name    string `msgpack:"name" json:"name"`
	Example any    `msgpack:"example,omitempty" json:"example,omitempty"`
}

// DecodeMsgpack accepts either a string (legacy / minimal apps) or a
// map (apps that volunteer an example). Lets Hello carry a mixed list.
func (d *SliceDescriptor) DecodeMsgpack(dec *msgpack.Decoder) error {
	code, err := dec.PeekCode()
	if err != nil {
		return err
	}
	if msgpcode.IsString(code) {
		s, err := dec.DecodeString()
		if err != nil {
			return err
		}
		d.Name = s
		return nil
	}
	// Map form: decode into a map and pull out name / example.
	m, err := dec.DecodeMap()
	if err != nil {
		return err
	}
	if v, ok := m["name"].(string); ok {
		d.Name = v
	}
	if v, ok := m["example"]; ok {
		d.Example = v
	}
	return nil
}

// Hello is the first message an app sends on a new connection.
type Hello struct {
	AppName    string   `msgpack:"app_name"`
	AppVersion string   `msgpack:"app_version"`
	Methods    []string `msgpack:"methods"`          // methods the app handles
	Pushes     []string `msgpack:"pushes,omitempty"` // push categories the app emits
	// Slices enumerates the named state slices the app makes
	// available to `state_query`. Lets agents discover what a game
	// exposes without prior knowledge. Bare strings and {name, example}
	// maps both decode cleanly (see SliceDescriptor).
	Slices []SliceDescriptor `msgpack:"slices,omitempty"`
}

// HelloAck is spyder's response.
type HelloAck struct {
	SpyderVersion   string   `msgpack:"spyder_version"`
	AcceptedMethods []string `msgpack:"accepted_methods"` // intersection of app's methods and spyder's known methods
}

// Standard method names (apps and spyder share this catalogue).
const (
	MethodHello            = "hello"
	MethodPing             = "ping"
	MethodQuit             = "quit"
	MethodFlush            = "flush"
	MethodBackgrounded     = "backgrounded"
	MethodForegrounded     = "foregrounded"
	MethodLowMemoryWarning = "low_memory_warning"
	MethodPause            = "pause"
	MethodResume           = "resume"
	MethodStep             = "step"
	MethodSpeed            = "speed"
	MethodInputInject      = "input_inject"
	// MethodSensorControl: fine-grained per-sensor stream authority
	// (passthrough|override|mute). Not a blanket session mask.
	MethodSensorControl = "sensor_control"
	MethodStateQuery       = "state_query"
	MethodSaveState        = "save_state"
	MethodRestoreState     = "restore_state"
	MethodScreenshotApp    = "screenshot_app"

	// Tweak control (🎯T91.2): tweak plane on the
	// app-channel so a direct-mode app is tunable via spyder only.
	MethodTweakList  = "tweak_list"
	MethodTweakGet   = "tweak_get"
	MethodTweakSet   = "tweak_set"
	MethodTweakReset = "tweak_reset"

	// MethodSpawnInstance (🎯T92.1): a game server that advertises this
	// method is a device FACTORY — spyder calls it to fork a new game
	// instance, which dials the given app-channel address back as its own
	// session (an abstract "device"). This is the spawn backend that lets
	// a game server take part in spyder's launcher/pool model.
	MethodSpawnInstance = "spawn_instance"

	// Push (app → spyder) message methods.
	PushLog          = "log"
	PushPerfCounters = "perf"
)

// KnownMethods is the catalogue spyder advertises in HelloAck (after
// intersecting with what the app sent in Hello). Apps that lack any of
// these don't get the corresponding MCP tool surface for that session.
var KnownMethods = []string{
	MethodPing,
	MethodQuit,
	MethodFlush,
	MethodBackgrounded,
	MethodForegrounded,
	MethodLowMemoryWarning,
	MethodPause,
	MethodResume,
	MethodStep,
	MethodSpeed,
	MethodInputInject,
	MethodSensorControl,
	MethodStateQuery,
	MethodSaveState,
	MethodRestoreState,
	MethodScreenshotApp,
	MethodTweakList,
	MethodTweakGet,
	MethodTweakSet,
	MethodTweakReset,
	MethodSpawnInstance,
}
