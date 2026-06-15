// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/itchyny/gojq"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	// DefaultStateCaptureInterval is used when the caller asks for a
	// capture with no interval. ~10 Hz is a reasonable default for
	// watching a game state evolve under inputs.
	DefaultStateCaptureInterval = 100 * time.Millisecond

	// MinStateCaptureInterval is a guard against runaway captures that
	// would saturate the RPC channel.
	MinStateCaptureInterval = 10 * time.Millisecond

	// MaxStateCaptureSamples bounds a single capture's in-memory
	// buffer. FIFO eviction when exceeded; the dropped count is
	// reported in Get/Stop responses.
	MaxStateCaptureSamples = 100_000

	// stateCaptureCallTimeout bounds each individual state_query the
	// poller makes. Generous — the app's slice handler is expected
	// to be sub-second, but the call shouldn't block forever if the
	// app stalls.
	stateCaptureCallTimeout = 2 * time.Second
)

// StateCaptureSample is one polled slice response.
type StateCaptureSample struct {
	Timestamp time.Time          `json:"timestamp"`
	Data      msgpack.RawMessage `json:"-"`
}

// MarshalJSON decodes Data into a generic Go value and re-emits as
// JSON so the agent sees a structured object rather than opaque
// MessagePack bytes.
func (s StateCaptureSample) MarshalJSON() ([]byte, error) {
	type out struct {
		Timestamp time.Time `json:"timestamp"`
		Data      any       `json:"data"`
	}
	var v any
	if len(s.Data) > 0 {
		if err := msgpack.Unmarshal(s.Data, &v); err != nil {
			v = nil
		}
	}
	return jsonMarshal(out{Timestamp: s.Timestamp, Data: v})
}

// StateCapture is a background poller that calls state_query{slice}
// on a session at a fixed interval, accumulating samples until Stop.
type StateCapture struct {
	ID       string
	Slice    string
	Interval time.Duration
	Started  time.Time
	// SelectExpr is the optional jq expression applied to each
	// sample's payload before insertion. When set, samples whose
	// filter result is empty are skipped (don't enter the ring),
	// trading the agent-context-budget win at start-time rather
	// than drain-time. Per-sample eval errors collapse to nil.
	SelectExpr string
	jqCode     *gojq.Code

	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	samples []StateCaptureSample
	dropped int    // FIFO-evicted samples
	errors  int    // failed state_query calls
	lastErr string // last non-nil error message
}

// StateCaptureGetResult is the payload returned by GetStateCapture.
type StateCaptureGetResult struct {
	CaptureID string               `json:"capture_id"`
	Slice     string               `json:"slice"`
	Samples   []StateCaptureSample `json:"samples"`
	Dropped   int                  `json:"dropped_samples,omitempty"`
	Errors    int                  `json:"errors,omitempty"`
	LastError string               `json:"last_error,omitempty"`
}

// StateCaptureStopResult is the payload returned by StopStateCapture.
type StateCaptureStopResult struct {
	CaptureID string               `json:"capture_id"`
	Slice     string               `json:"slice"`
	StoppedAt time.Time            `json:"stopped_at"`
	Samples   []StateCaptureSample `json:"samples"`
	Dropped   int                  `json:"dropped_samples,omitempty"`
	Errors    int                  `json:"errors,omitempty"`
	LastError string               `json:"last_error,omitempty"`
}

// StateCaptureInfo is the per-capture record returned by
// ListStateCaptures and embedded in session listings.
type StateCaptureInfo struct {
	CaptureID  string    `json:"capture_id"`
	Slice      string    `json:"slice"`
	IntervalMs int       `json:"interval_ms"`
	StartedAt  time.Time `json:"started_at"`
	Samples    int       `json:"samples"`
	Dropped    int       `json:"dropped_samples,omitempty"`
	Errors     int       `json:"errors,omitempty"`
}

// StartStateCapture launches a poller that calls state_query{slice}
// every interval. Returns immediately with the capture handle.
func (s *Session) StartStateCapture(slice string, interval time.Duration) (*StateCapture, error) {
	return s.StartStateCaptureWithSelect(slice, interval, "")
}

// StartStateCaptureWithSelect is StartStateCapture plus an optional
// jq expression applied at insert time. Samples whose filter result
// is empty are skipped — saves buffer memory for the agent that
// only cares about a small subset of a large slice.
func (s *Session) StartStateCaptureWithSelect(slice string, interval time.Duration, selectExpr string) (*StateCapture, error) {
	if slice == "" {
		return nil, errors.New("appchannel: state capture requires a slice name")
	}
	if interval <= 0 {
		interval = DefaultStateCaptureInterval
	}
	if interval < MinStateCaptureInterval {
		interval = MinStateCaptureInterval
	}
	if !s.Supports(MethodStateQuery) {
		return nil, fmt.Errorf("appchannel: app does not support state_query")
	}

	// Pre-compile any select expression so a bad filter fails fast at
	// start time rather than per-sample at insert time.
	var jqCode *gojq.Code
	if selectExpr != "" {
		query, err := gojq.Parse(selectExpr)
		if err != nil {
			return nil, &JQError{Expression: selectExpr, Stage: "parse", Detail: err.Error()}
		}
		jqCode, err = gojq.Compile(query)
		if err != nil {
			return nil, &JQError{Expression: selectExpr, Stage: "compile", Detail: err.Error()}
		}
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &StateCapture{
		ID:         id,
		Slice:      slice,
		Interval:   interval,
		Started:    time.Now(),
		SelectExpr: selectExpr,
		jqCode:     jqCode,
		cancel:     cancel,
		done:       make(chan struct{}),
	}

	s.mu.Lock()
	s.stateCaptures[id] = c
	s.mu.Unlock()

	go c.poll(ctx, s)

	slog.Info("appchannel: state capture started",
		"session_id", s.ID, "capture_id", id,
		"slice", slice, "interval", interval)

	return c, nil
}

// poll runs the sampling loop until ctx fires. Each iteration: wait
// for the next tick, call state_query, append to buffer (FIFO-evict
// past the cap), record errors. Exits cleanly on ctx done.
func (c *StateCapture) poll(ctx context.Context, s *Session) {
	defer close(c.done)
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			res, err := s.Call(ctx, MethodStateQuery, map[string]string{"slice": c.Slice}, stateCaptureCallTimeout)
			now := time.Now()
			c.mu.Lock()
			if err != nil {
				c.errors++
				c.lastErr = err.Error()
				c.mu.Unlock()
				continue
			}
			// Apply insert-time filter if one was registered.
			sample := StateCaptureSample{Timestamp: now, Data: res}
			if c.jqCode != nil {
				filtered, ok := c.applyJQ(ctx, res)
				if !ok {
					c.mu.Unlock()
					continue
				}
				// Re-encode the filtered result as msgpack so the
				// sample retains the same RawMessage shape callers
				// already handle.
				b, err := msgpack.Marshal(filtered)
				if err != nil {
					c.errors++
					c.lastErr = err.Error()
					c.mu.Unlock()
					continue
				}
				sample.Data = b
			}
			c.samples = append(c.samples, sample)
			for len(c.samples) > MaxStateCaptureSamples {
				c.samples = c.samples[1:]
				c.dropped++
			}
			c.mu.Unlock()
		}
	}
}

// applyJQ runs the capture's compiled filter against the raw sample
// and returns (filteredValue, true) when at least one match was
// produced, or (nil, false) when the filter matched nothing (so the
// sample should be skipped). Eval errors bump the errors counter.
// Caller must hold c.mu (so the errors counter mutation is safe).
func (c *StateCapture) applyJQ(ctx context.Context, raw msgpack.RawMessage) (any, bool) {
	var input any
	if err := msgpack.Unmarshal(raw, &input); err != nil {
		c.errors++
		c.lastErr = err.Error()
		return nil, false
	}
	input = normaliseForJQ(input)
	iter := c.jqCode.RunWithContext(ctx, input)
	var results []any
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			c.errors++
			c.lastErr = err.Error()
			return nil, false
		}
		results = append(results, v)
	}
	switch len(results) {
	case 0:
		return nil, false
	case 1:
		return results[0], true
	default:
		return results, true
	}
}

// GetStateCapture drains the buffered samples without stopping the
// capture. Mirrors app_log_get.
func (s *Session) GetStateCapture(captureID string) (*StateCaptureGetResult, error) {
	s.mu.Lock()
	c, ok := s.stateCaptures[captureID]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("appchannel: no such state capture: %s", captureID)
	}
	c.mu.Lock()
	samples := c.samples
	if samples == nil {
		samples = []StateCaptureSample{}
	}
	dropped := c.dropped
	errs := c.errors
	lastErr := c.lastErr
	c.samples = nil
	c.dropped = 0
	c.errors = 0
	c.lastErr = ""
	c.mu.Unlock()
	return &StateCaptureGetResult{
		CaptureID: captureID,
		Slice:     c.Slice,
		Samples:   samples,
		Dropped:   dropped,
		Errors:    errs,
		LastError: lastErr,
	}, nil
}

// StopStateCapture stops the poller and returns the remaining samples.
func (s *Session) StopStateCapture(captureID string) (*StateCaptureStopResult, error) {
	s.mu.Lock()
	c, ok := s.stateCaptures[captureID]
	if ok {
		delete(s.stateCaptures, captureID)
	}
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("appchannel: no such state capture: %s", captureID)
	}
	c.cancel()
	<-c.done
	c.mu.Lock()
	samples := c.samples
	if samples == nil {
		samples = []StateCaptureSample{}
	}
	dropped := c.dropped
	errs := c.errors
	lastErr := c.lastErr
	c.samples = nil
	c.dropped = 0
	c.errors = 0
	c.lastErr = ""
	c.mu.Unlock()
	slog.Info("appchannel: state capture stopped",
		"session_id", s.ID, "capture_id", captureID,
		"samples", len(samples), "dropped", dropped, "errors", errs)
	return &StateCaptureStopResult{
		CaptureID: captureID,
		Slice:     c.Slice,
		StoppedAt: time.Now(),
		Samples:   samples,
		Dropped:   dropped,
		Errors:    errs,
		LastError: lastErr,
	}, nil
}

// ListStateCaptures returns metadata for every live capture.
func (s *Session) ListStateCaptures() []StateCaptureInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StateCaptureInfo, 0, len(s.stateCaptures))
	for _, c := range s.stateCaptures {
		c.mu.Lock()
		out = append(out, StateCaptureInfo{
			CaptureID:  c.ID,
			Slice:      c.Slice,
			IntervalMs: int(c.Interval / time.Millisecond),
			StartedAt:  c.Started,
			Samples:    len(c.samples),
			Dropped:    c.dropped,
			Errors:     c.errors,
		})
		c.mu.Unlock()
	}
	return out
}

// closeStateCaptures tears down every active capture for this session.
// Called when the session ends so background goroutines don't leak.
func (s *Session) closeStateCaptures() {
	s.mu.Lock()
	captures := make([]*StateCapture, 0, len(s.stateCaptures))
	for _, c := range s.stateCaptures {
		captures = append(captures, c)
	}
	s.stateCaptures = nil
	s.mu.Unlock()
	for _, c := range captures {
		c.cancel()
		<-c.done
	}
}

// jsonMarshal is a thin indirection so StateCaptureSample.MarshalJSON
// doesn't import encoding/json into the package's public surface.
// Forwards to encoding/json; declared in state_capture_json.go.
