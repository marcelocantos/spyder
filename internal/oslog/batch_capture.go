// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package oslog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	batchCapturePathEnv            = "SPYDER_OSLOG_BATCH_PATH"
	batchCaptureLimitEnv           = "SPYDER_OSLOG_BATCH_MAX_BYTES"
	defaultBatchCaptureBytes int64 = 16 << 20
	maximumBatchCaptureBytes int64 = 64 << 20
	batchTerminalReserve     int64 = 4096
	maximumBatchErrorBytes         = int(batchTerminalReserve / 8)
)

// traceBatch is the exact post-DTX Payload[0] supplied to the VM, with its
// transport framing metadata. It is not reconstructed from decoded log text.
type traceBatch struct {
	Payload       []byte `json:"payload_base64"`
	MessageType   uint32 `json:"message_type"`
	ChannelCode   int    `json:"channel_code"`
	Identifier    int    `json:"identifier"`
	FragmentIndex uint16 `json:"fragment_index"`
	Fragments     uint16 `json:"fragments"`
}

type batchCapture struct {
	file                *os.File
	limit, written      int64
	sequence, attempted uint64
}

func batchCaptureFromEnvironment(deviceID string) (*batchCapture, error) {
	path, enabled := os.LookupEnv(batchCapturePathEnv)
	limitValue, hasLimit := os.LookupEnv(batchCaptureLimitEnv)
	if !enabled && !hasLimit {
		return nil, nil
	}
	if !enabled || path == "" {
		return nil, errors.New("diagnostic batch capture needs an explicit output path")
	}
	limit := defaultBatchCaptureBytes
	if hasLimit {
		value, err := strconv.ParseInt(limitValue, 10, 64)
		if err != nil {
			return nil, errors.New("diagnostic batch byte bound is invalid")
		}
		limit = value
	}
	return openBatchCapture(path, deviceID, limit)
}

func openBatchCapture(path, deviceID string, limit int64) (*batchCapture, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.TrimSpace(deviceID) == "" {
		return nil, errors.New("diagnostic batch capture needs a clean absolute path and device identity")
	}
	if limit < 2*batchTerminalReserve || limit > maximumBatchCaptureBytes {
		return nil, errors.New("diagnostic batch byte bound is outside the supported range")
	}
	if !strings.HasPrefix(path, "/private/tmp/") && !strings.HasPrefix(path, filepath.Clean(os.TempDir())+string(filepath.Separator)) {
		return nil, errors.New("diagnostic output must be in private temporary storage")
	}
	parent := filepath.Dir(path)
	for ancestor := parent; ; ancestor = filepath.Dir(ancestor) {
		if _, err := os.Lstat(filepath.Join(ancestor, ".git")); err == nil || !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("diagnostic output must not be inside a Git worktree")
		}
		if ancestor == filepath.Dir(ancestor) {
			break
		}
	}
	before, err := os.Lstat(parent)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode().Perm() != 0700 || !batchPathOwned(before) {
		return nil, errors.New("diagnostic output parent must be a real owned mode0700 directory")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("diagnostic output parent changed while opening")
	}
	file, err := root.OpenFile(filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	capture := &batchCapture{file: file, limit: limit}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !batchPathOwned(info) {
		file.Close()
		return nil, errors.New("diagnostic output is not an owned mode0600 regular file")
	}
	header := map[string]any{"kind": "header", "schema_version": 1, "device_id": deviceID,
		"max_file_bytes": limit, "scope": "exact post-DTX payload before VM decoding; not original USB wire; binary data is base64",
		"from_stream_open": true}
	encoded, err := json.Marshal(header)
	if err == nil {
		err = capture.writeLine(encoded, limit-batchTerminalReserve)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return capture, nil
}

func (c *batchCapture) writeLine(encoded []byte, bound int64) error {
	if int64(len(encoded))+1 > bound-c.written {
		return errors.New("diagnostic batch capture byte bound exhausted")
	}
	encoded = append(encoded, '\n')
	n, err := c.file.Write(encoded)
	c.written += int64(n)
	if err != nil {
		return err
	}
	if n != len(encoded) {
		return errors.New("diagnostic batch capture short write")
	}
	return nil
}

func (c *batchCapture) writeBatch(batch traceBatch) error {
	c.attempted++
	// Bound the allocation before encoding the exact payload as base64.
	if int64(len(batch.Payload)) > c.limit-c.written-batchTerminalReserve {
		return errors.New("diagnostic batch capture payload exceeds remaining byte bound")
	}
	sum := sha256.Sum256(batch.Payload)
	entry := struct {
		Kind     string `json:"kind"`
		Sequence uint64 `json:"sequence"`
		Length   int    `json:"payload_length"`
		SHA256   string `json:"sha256"`
		traceBatch
	}{"batch", c.attempted, len(batch.Payload), hex.EncodeToString(sum[:]), batch}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if err := c.writeLine(encoded, c.limit-batchTerminalReserve); err != nil {
		return err
	}
	c.sequence = c.attempted
	return nil
}

func (c *batchCapture) finish(cause error) error {
	status, message := "owned_stop", ""
	truncated := false
	if cause != nil {
		status, message = "failed", cause.Error()
		if len(message) > maximumBatchErrorBytes {
			message = message[:maximumBatchErrorBytes]
			truncated = true
		}
	}
	entry := map[string]any{"kind": "terminal", "status": status, "incomplete": cause != nil,
		"recorded_batches": c.sequence, "last_attempted_batch": c.attempted, "error": message, "error_truncated": truncated}
	encoded, err := json.Marshal(entry)
	if err == nil {
		err = c.writeLine(encoded, c.limit)
	}
	syncErr := c.file.Sync()
	closeErr := c.file.Close()
	return errors.Join(err, syncErr, closeErr)
}

func (s *Stream) finishBatchCapture() {
	if s.batchCapture == nil {
		return
	}
	if err := s.batchCapture.finish(s.Err()); err != nil {
		s.errMu.Lock()
		s.err = errors.Join(s.err, fmt.Errorf("%w: diagnostic batch finalization: %w", ErrCaptureIntegrity, err))
		s.errMu.Unlock()
	}
}
