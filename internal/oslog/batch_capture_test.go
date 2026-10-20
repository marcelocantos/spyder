//go:build unix

// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package oslog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	dtx "github.com/danielpaulus/go-ios/ios/dtx_codec"
)

type capturedBatchEntry struct {
	Kind           string `json:"kind"`
	Sequence       uint64 `json:"sequence"`
	Length         int    `json:"payload_length"`
	SHA256         string `json:"sha256"`
	Incomplete     bool   `json:"incomplete"`
	Recorded       uint64 `json:"recorded_batches"`
	Attempted      uint64 `json:"last_attempted_batch"`
	Error          string `json:"error"`
	ErrorTruncated bool   `json:"error_truncated"`
	traceBatch
}

func privateBatchDirectory(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	return parent
}

func readBatchEntries(t *testing.T, path string) []capturedBatchEntry {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var entries []capturedBatchEntry
	for {
		var entry capturedBatchEntry
		err := decoder.Decode(&entry)
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
}

func batchTestStream(t *testing.T, capture *batchCapture) (*Stream, context.CancelFunc) {
	t.Helper()
	connection := &controlledConnection{closed: make(chan struct{}), cause: errors.New("injected DTX EOF")}
	stream := &Stream{conn: connection, frames: make(chan traceBatch), closeCh: make(chan struct{}), Records: make(chan Record, 8), batchCapture: capture}
	ctx, cancel := context.WithCancel(context.Background())
	go stream.pump(ctx)
	t.Cleanup(func() {
		cancel()
		stream.Close()
		for range stream.Records {
		}
	})
	return stream, cancel
}

func TestBatchCaptureReplaysExactValidPrefixAndFailingVMInput(t *testing.T) {
	path := filepath.Join(privateBatchDirectory(t), "batches.jsonl")
	capture, err := openBatchCapture(path, "test-device", defaultBatchCaptureBytes)
	if err != nil {
		t.Fatal(err)
	}
	stream, cancel := batchTestStream(t, capture)
	inputs := [][]byte{heartbeatPrefix, defineTableFrame("Logs", []string{"message"}), buildFrame(word16(cmdStruct, 0), word16(cmdEndRow, 0)), {0x00, 0x80}}
	dispatcher := frameDispatcher{frames: stream.frames, closeCh: stream.closeCh}
	for i, payload := range inputs {
		dispatcher.Dispatch(dtx.Message{Payload: []any{payload}, PayloadHeader: dtx.PayloadHeader{MessageType: 1},
			ChannelCode: -1, Identifier: i + 100, FragmentIndex: 2, Fragments: 3})
	}
	// The unbuffered final dispatch has been received by pump; it must finish
	// decoding that case before observing cancellation on its next select.
	cancel()
	var rows int
	for range stream.Records {
		rows++
	}
	if rows != 1 || !errors.Is(stream.Err(), ErrCaptureIntegrity) || !strings.Contains(stream.Err().Error(), "truncated push") {
		t.Fatalf("wrong replay control outcome: rows=%d err=%v", rows, stream.Err())
	}
	cancel()
	stream.Close()
	entries := readBatchEntries(t, path)
	if len(entries) != len(inputs)+2 || entries[0].Kind != "header" {
		t.Fatalf("missing from-open batch prefix: %d entries", len(entries))
	}
	decoder := NewDecoder()
	for i, entry := range entries[1 : len(entries)-1] {
		sum := sha256.Sum256(inputs[i])
		if entry.Kind != "batch" || entry.Sequence != uint64(i+1) || entry.Length != len(inputs[i]) || entry.SHA256 != hex.EncodeToString(sum[:]) || !bytes.Equal(entry.Payload, inputs[i]) {
			t.Fatalf("batch%d did not preserve exact input", i+1)
		}
		if entry.MessageType != 1 || entry.ChannelCode != -1 || entry.Identifier != i+100 || entry.FragmentIndex != 2 || entry.Fragments != 3 {
			t.Fatal("lost DTX framing metadata")
		}
		if bytes.HasPrefix(entry.Payload, heartbeatPrefix) {
			continue
		}
		_, err = decoder.Decode(entry.Payload, nil)
		if i < len(inputs)-1 && err != nil {
			t.Fatalf("recorded valid prefix failed at%d: %v", i, err)
		}
	}
	if err != nil {
		t.Fatalf("message boundary was mistaken for stream EOF: %v", err)
	}
	if err := decoder.Finish(); err == nil || !strings.Contains(err.Error(), "truncated push") {
		t.Fatal("recorded terminal prefix does not reproduce truncation")
	}
	terminal := entries[len(entries)-1]
	if terminal.Kind != "terminal" || !terminal.Incomplete || terminal.Recorded != 4 || terminal.Attempted != 4 || !strings.Contains(terminal.Error, "truncated push") {
		t.Fatal("decoder failure lost its bounded terminal evidence")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 || info.Size() > defaultBatchCaptureBytes {
		t.Fatal("diagnostic evidence is not private and bounded")
	}
}

func TestBatchCaptureExhaustionFailsBeforeDecodingAndRetainsTerminal(t *testing.T) {
	const limit = 2 * batchTerminalReserve
	path := filepath.Join(privateBatchDirectory(t), "bounded.jsonl")
	capture, err := openBatchCapture(path, "test-device", limit)
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := batchTestStream(t, capture)
	stream.frames <- traceBatch{Payload: heartbeatPrefix}
	stream.frames <- traceBatch{Payload: make([]byte, limit)}
	for range stream.Records {
	}
	if !errors.Is(stream.Err(), ErrCaptureIntegrity) || !strings.Contains(stream.Err().Error(), "byte bound") || stream.LastDecodeError() != nil {
		t.Fatalf("bound must fail before VM decode: %v", stream.Err())
	}
	entries := readBatchEntries(t, path)
	terminal := entries[len(entries)-1]
	if len(entries) != 3 || !terminal.Incomplete || terminal.Recorded != 1 || terminal.Attempted != 2 {
		t.Fatal("bound exhaustion hid the missing diagnostic batch")
	}
	info, _ := os.Stat(path)
	if info.Size() > limit {
		t.Fatal("diagnostic file exceeded its declared bound")
	}
}

func TestBatchCaptureWriteAndFinalizationFailuresCannotPass(t *testing.T) {
	path := filepath.Join(privateBatchDirectory(t), "closed.jsonl")
	capture, err := openBatchCapture(path, "test-device", defaultBatchCaptureBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := capture.file.Close(); err != nil {
		t.Fatal(err)
	}
	stream, _ := batchTestStream(t, capture)
	stream.frames <- traceBatch{Payload: heartbeatPrefix}
	for range stream.Records {
	}
	if !errors.Is(stream.Err(), ErrCaptureIntegrity) || !strings.Contains(stream.Err().Error(), "diagnostic batch") {
		t.Fatalf("write/close failure looked clean: %v", stream.Err())
	}
}

func TestBatchCaptureNormalCancellationClosesPrivateEvidence(t *testing.T) {
	path := filepath.Join(privateBatchDirectory(t), "cancelled.jsonl")
	capture, err := openBatchCapture(path, "test-device", defaultBatchCaptureBytes)
	if err != nil {
		t.Fatal(err)
	}
	stream, cancel := batchTestStream(t, capture)
	cancel()
	for range stream.Records {
	}
	if stream.Err() != nil {
		t.Fatalf("owned cancellation changed transport semantics: %v", stream.Err())
	}
	entries := readBatchEntries(t, path)
	if entries[len(entries)-1].Incomplete {
		t.Fatal("normal cancellation poisoned diagnostic integrity")
	}
	if _, err := capture.file.Write([]byte("forbidden")); err == nil {
		t.Fatal("diagnostic descriptor remained open")
	}
}

type foreignBatchInfo struct{ fs.FileInfo }

func (f foreignBatchInfo) Sys() any { return &syscall.Stat_t{Uid: uint32(os.Getuid() + 1)} }

func TestBatchCaptureRejectsUnsafeOwnershipPathsAndCollisions(t *testing.T) {
	parent := privateBatchDirectory(t)
	info, err := os.Stat(parent)
	if err != nil || !batchPathOwned(info) || batchPathOwned(foreignBatchInfo{info}) {
		t.Fatal("ownership check did not distinguish foreign owner")
	}
	public := filepath.Join(parent, "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "linked-parent")
	if err := os.Symlink(parent, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative.jsonl", filepath.Join(public, "capture.jsonl"), filepath.Join(link, "capture.jsonl")} {
		if capture, err := openBatchCapture(path, "test-device", defaultBatchCaptureBytes); err == nil {
			capture.finish(nil)
			t.Fatalf("accepted unsafe diagnostic path %s", path)
		}
	}
	gitParent := privateBatchDirectory(t)
	if err := os.WriteFile(filepath.Join(gitParent, ".git"), []byte("gitdir: /do-not-write"), 0600); err != nil {
		t.Fatal(err)
	}
	if capture, err := openBatchCapture(filepath.Join(gitParent, "raw.jsonl"), "test-device", defaultBatchCaptureBytes); err == nil {
		capture.finish(nil)
		t.Fatal("raw provider output was admitted inside a Git worktree")
	}
	existing := filepath.Join(parent, "existing")
	if err := os.WriteFile(existing, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(parent, "symlink")
	if err := os.Symlink(existing, symlink); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{existing, symlink} {
		if capture, err := openBatchCapture(path, "test-device", defaultBatchCaptureBytes); err == nil {
			capture.finish(nil)
			t.Fatal("diagnostic output overwrote an existing path")
		}
	}
	contents, err := os.ReadFile(existing)
	if err != nil || string(contents) != "preserve" {
		t.Fatal("collision altered existing evidence")
	}
}

func TestBatchCaptureDefaultOffAndConfigurationBounds(t *testing.T) {
	t.Setenv(batchCapturePathEnv, "")
	t.Setenv(batchCaptureLimitEnv, "")
	os.Unsetenv(batchCapturePathEnv)
	os.Unsetenv(batchCaptureLimitEnv)
	if capture, err := batchCaptureFromEnvironment("device"); capture != nil || err != nil {
		t.Fatal("diagnostic hook is not off by default")
	}
	t.Setenv(batchCaptureLimitEnv, "8192")
	if _, err := batchCaptureFromEnvironment("device"); err == nil {
		t.Fatal("bound without explicit path was accepted")
	}
	t.Setenv(batchCapturePathEnv, filepath.Join(privateBatchDirectory(t), "capture.jsonl"))
	for _, limit := range []string{"", "NaN", "0", "-1", "67108865"} {
		t.Setenv(batchCaptureLimitEnv, limit)
		if capture, err := batchCaptureFromEnvironment("device"); err == nil {
			capture.finish(nil)
			t.Fatalf("invalid byte bound accepted: %q", limit)
		}
	}
}

func TestBatchCaptureTerminalCauseIsBoundedAndExplicitlyTruncated(t *testing.T) {
	const limit = 2 * batchTerminalReserve
	path := filepath.Join(privateBatchDirectory(t), "terminal.jsonl")
	capture, err := openBatchCapture(path, "test-device", limit)
	if err != nil {
		t.Fatal(err)
	}
	if err := capture.finish(errors.New(strings.Repeat("\x00", int(limit)))); err != nil {
		t.Fatalf("reserved terminal space was insufficient: %v", err)
	}
	entries := readBatchEntries(t, path)
	terminal := entries[len(entries)-1]
	if !terminal.Incomplete || !terminal.ErrorTruncated {
		t.Fatal("oversized terminal cause was silently shortened")
	}
	info, _ := os.Stat(path)
	if info.Size() > limit {
		t.Fatal("terminal cause exceeded file byte bound")
	}
}
