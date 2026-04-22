// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// interPacketStallError is the error returned by stallReader.Read when the
// inter-packet deadline fires. Streaming clients identify stalls via
// errors.As on this type, which keeps the precedence unambiguous even
// though the underlying cause is often context.Canceled.
type interPacketStallError struct {
	endpoint string
	deadline time.Duration
	cause    error
}

func (e *interPacketStallError) Error() string {
	return fmt.Sprintf("pmd3bridge: %s: inter-packet deadline %s exceeded (no chunk for that long) — bridge unresponsive: %v",
		e.endpoint, e.deadline, e.cause)
}

func (e *interPacketStallError) Unwrap() error { return e.cause }

// stallReader wraps an io.ReadCloser with an inter-packet deadline (🎯T26.3).
// Every successful Read refreshes the last-progress timestamp; a watchdog
// goroutine cancels the underlying HTTP context if no progress is observed
// within the deadline. When that fires, subsequent Reads return a stall
// error that streaming clients convert into a fatal (panic).
//
// The end-to-end safety-net deadline is applied by the enclosing context at
// the top of postStream, so the outer HTTP request is still bounded by
// timeoutStreamEndToEnd. The stall detector is what actually catches bridge
// bugs; the outer deadline is there to keep a pathologically slow-but-never-
// silent server from hogging a goroutine forever.
type stallReader struct {
	endpoint string
	inner    io.ReadCloser
	cancel   context.CancelFunc
	deadline time.Duration

	lastRead atomic.Int64 // unix nano
	stalled  atomic.Bool
	stopCh   chan struct{}
	stopOnce atomic.Bool
}

func newStallReader(endpoint string, body io.ReadCloser,
	cancel context.CancelFunc, deadline time.Duration,
) *stallReader {
	sr := &stallReader{
		endpoint: endpoint,
		inner:    body,
		cancel:   cancel,
		deadline: deadline,
		stopCh:   make(chan struct{}),
	}
	sr.lastRead.Store(time.Now().UnixNano())
	go sr.watchdog()
	return sr
}

func (s *stallReader) watchdog() {
	tick := s.deadline / 4
	if tick <= 0 {
		tick = time.Second
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			last := time.Unix(0, s.lastRead.Load())
			if time.Since(last) > s.deadline {
				s.stalled.Store(true)
				s.cancel()
				slog.Error("bridge stream inter-packet deadline exceeded",
					"endpoint", s.endpoint,
					"deadline", s.deadline,
					"since_last_read", time.Since(last))
				return
			}
		}
	}
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.inner.Read(p)
	if n > 0 {
		s.lastRead.Store(time.Now().UnixNano())
	}
	if err != nil && s.stalled.Load() {
		return n, &interPacketStallError{
			endpoint: s.endpoint,
			deadline: s.deadline,
			cause:    err,
		}
	}
	return n, err
}

func (s *stallReader) Close() error {
	if !s.stopOnce.Swap(true) {
		close(s.stopCh)
	}
	return s.inner.Close()
}

// postStream is the streaming counterpart to post(). It POSTs a JSON body
// to path, returns the response unbuffered, and wraps the body with a
// stallReader so streaming callers get inter-packet deadline detection for
// free. On structured BridgeError, returns the error without committing to
// streaming. On transport error, fires the fatal hook.
func (c *Client) postStream(ctx context.Context, path string, reqBody any,
) (*http.Response, io.ReadCloser, error) {
	started := time.Now()
	slog.Debug("bridge stream call", "endpoint", path,
		"end_to_end_timeout_ms", timeoutStreamEndToEnd.Milliseconds(),
		"inter_packet_deadline_ms", interPacketDeadline.Milliseconds())

	callCtx, cancel := context.WithTimeout(ctx, timeoutStreamEndToEnd)

	body, err := json.Marshal(reqBody)
	if err != nil {
		cancel()
		c.fire(fmt.Errorf("pmd3bridge: %s: marshal request: %w", path, err))
		return nil, nil, err
	}

	baseURL, token := c.endpoint()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		baseURL+path, bytes.NewReader(body))
	if err != nil {
		cancel()
		c.fire(fmt.Errorf("pmd3bridge: %s: build request: %w", path, err))
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
			return nil, nil, err
		}
		c.fire(fmt.Errorf("pmd3bridge: %s: transport error: %w", path, err))
		return nil, nil, err
	}

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		var errBody bridgeErrorBody
		if jerr := json.Unmarshal(raw, &errBody); jerr != nil {
			c.fire(fmt.Errorf("pmd3bridge: %s: unstructured error response (%d): %s",
				path, resp.StatusCode, raw))
			return nil, nil, &BridgeError{Code: "unknown", Message: string(raw), Status: resp.StatusCode}
		}
		slog.Debug("bridge stream bridge_error",
			"endpoint", path, "code", errBody.Error, "status", resp.StatusCode,
			"duration_ms", time.Since(started).Milliseconds())
		return nil, nil, &BridgeError{
			Code:    errBody.Error,
			Message: errBody.Message,
			Status:  resp.StatusCode,
		}
	}

	slog.Debug("bridge stream opened",
		"endpoint", path, "status", resp.StatusCode,
		"duration_ms", time.Since(started).Milliseconds())

	// Wrap with stall detection. The stallReader owns `cancel`; when Close
	// is called (by the caller, or by defer), the watchdog exits and the
	// outer context is released.
	stall := newStallReader(path, resp.Body, cancel, interPacketDeadline)
	return resp, stall, nil
}

// drainErr inspects the error after reading from a stall-wrapped body.
// Precedence: inter-packet stall → fire (bug); context.Canceled without
// stall → legitimate shutdown; any other mid-stream failure → fire (bug).
func (c *Client) drainErr(endpoint string, err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	var stall *interPacketStallError
	if errors.As(err, &stall) {
		c.fire(err)
		return err
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	// Any other mid-stream error (truncated body, connection reset) is
	// also a bug — we were reading 2xx content and lost it.
	c.fire(fmt.Errorf("pmd3bridge: %s: mid-stream read error — bridge unresponsive: %w",
		endpoint, err))
	return err
}

// scanNDJSON reads newline-delimited JSON objects from r, decoding each
// into a fresh T, and invokes yield for each decoded value. It stops on
// EOF (clean stream end), an error from r, or yield returning false.
// The return value is the error (if any) that terminated the stream.
func scanNDJSON[T any](r io.Reader, yield func(T) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			return fmt.Errorf("decode ndjson line: %w", err)
		}
		if !yield(v) {
			return nil
		}
	}
	return scanner.Err()
}
