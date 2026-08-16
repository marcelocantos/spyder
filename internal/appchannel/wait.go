// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

const (
	// DefaultWaitTimeout is used when WaitStateArgs.Timeout is zero.
	DefaultWaitTimeout = 10 * time.Second
	// DefaultWaitPoll is used when WaitStateArgs.PollEvery is zero.
	DefaultWaitPoll = 200 * time.Millisecond
	// MaxWaitTimeout caps a single wait_state call so the dispatch
	// deadline (DeadlineWaitState) can cover it with margin.
	MaxWaitTimeout = 120 * time.Second
	// MinWaitPoll is the fastest poll cadence. Matches capture's floor.
	MinWaitPoll = 10 * time.Millisecond
)

// WaitStateArgs is the wait_state poll configuration.
type WaitStateArgs struct {
	Slice     string
	Select    string
	Timeout   time.Duration
	PollEvery time.Duration
}

// WaitStateResult is a successful wait: the jq value that became truthy.
type WaitStateResult struct {
	Value    any
	Attempts int
	Elapsed  time.Duration
}

// SliceSource fetches one raw state_query payload for slice.
type SliceSource func(ctx context.Context, slice string) (msgpack.RawMessage, error)

// WaitTimeoutError is returned when the predicate never became truthy.
// LastValue is the last ApplyJQ result (not merely "timed out").
type WaitTimeoutError struct {
	Slice     string
	Select    string
	LastValue any
	Attempts  int
	Timeout   time.Duration
}

func (e *WaitTimeoutError) Error() string {
	last, err := json.Marshal(e.LastValue)
	if err != nil {
		last = []byte(fmt.Sprintf("%v", e.LastValue))
	}
	sel := e.Select
	if sel == "" {
		sel = "."
	}
	return fmt.Sprintf("wait_state timed out after %s (%d attempts) on slice %q select %q; last observed: %s",
		e.Timeout.Round(time.Millisecond), e.Attempts, e.Slice, sel, last)
}

// jqTruthy reports whether an ApplyJQ result satisfies wait_state.
// jq itself treats only false and null as falsy; we also treat an empty
// []any as unsatisfied because ApplyJQ returns that for "no matches"
// (e.g. select(.present) on a non-matching object).
func jqTruthy(v any) bool {
	if v == nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case []any:
		return len(x) > 0
	default:
		return true
	}
}

// WaitState polls src until ApplyJQ(select, payload) is truthy, then
// returns that value. On timeout it returns a WaitTimeoutError that
// carries the last observed value.
func WaitState(ctx context.Context, args WaitStateArgs, src SliceSource) (WaitStateResult, error) {
	if args.Slice == "" {
		return WaitStateResult{}, fmt.Errorf("slice is required")
	}
	if src == nil {
		return WaitStateResult{}, fmt.Errorf("slice source is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	timeout := args.Timeout
	if timeout <= 0 {
		timeout = DefaultWaitTimeout
	}
	if timeout > MaxWaitTimeout {
		timeout = MaxWaitTimeout
	}
	poll := args.PollEvery
	if poll <= 0 {
		poll = DefaultWaitPoll
	}
	if poll < MinWaitPoll {
		poll = MinWaitPoll
	}

	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}

	var last any
	attempts := 0
	start := time.Now()

	for {
		if err := ctx.Err(); err != nil {
			return WaitStateResult{Attempts: attempts, Elapsed: time.Since(start)}, err
		}

		attempts++
		raw, err := src(ctx, args.Slice)
		if err != nil {
			return WaitStateResult{Attempts: attempts, Elapsed: time.Since(start)}, fmt.Errorf("wait_state fetch: %w", err)
		}
		out, err := ApplyJQ(args.Select, raw)
		if err != nil {
			return WaitStateResult{Attempts: attempts, Elapsed: time.Since(start)}, err
		}
		last = out
		if jqTruthy(out) {
			return WaitStateResult{Value: out, Attempts: attempts, Elapsed: time.Since(start)}, nil
		}

		if !time.Now().Before(deadline) {
			break
		}
		remaining := time.Until(deadline)
		sleep := poll
		if sleep > remaining {
			sleep = remaining
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return WaitStateResult{Attempts: attempts, Elapsed: time.Since(start)}, ctx.Err()
		case <-timer.C:
		}
	}

	return WaitStateResult{Attempts: attempts, Elapsed: time.Since(start)}, &WaitTimeoutError{
		Slice:     args.Slice,
		Select:    args.Select,
		LastValue: last,
		Attempts:  attempts,
		Timeout:   timeout,
	}
}
