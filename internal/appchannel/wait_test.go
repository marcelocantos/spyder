// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package appchannel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

func packSlice(t *testing.T, v any) msgpack.RawMessage {
	t.Helper()
	raw, err := PackParams(v)
	if err != nil {
		t.Fatalf("PackParams: %v", err)
	}
	return raw
}

func TestWaitState_FalseThenTrueReturnsSatisfyingSlice(t *testing.T) {
	payloads := []any{
		map[string]any{"present": false, "active": false, "tick": 1},
		map[string]any{"present": false, "active": false, "tick": 2},
		map[string]any{"present": true, "active": true, "tick": 3, "settled_index": 2},
	}
	var i int
	src := func(context.Context, string) (msgpack.RawMessage, error) {
		if i >= len(payloads) {
			t.Fatal("fetched past the satisfying payload")
		}
		raw := packSlice(t, payloads[i])
		i++
		return raw, nil
	}

	got, err := WaitState(context.Background(), WaitStateArgs{
		Slice:     "carousel",
		Select:    "select(.present == true and .active == true)",
		Timeout:   time.Second,
		PollEvery: time.Millisecond,
	}, src)
	if err != nil {
		t.Fatalf("WaitState: %v", err)
	}
	if i != 3 {
		t.Fatalf("attempts via fetch = %d; want 3", i)
	}
	if got.Attempts != 3 {
		t.Errorf("Attempts = %d; want 3", got.Attempts)
	}
	m, ok := got.Value.(map[string]any)
	if !ok {
		t.Fatalf("Value type %T; want map", got.Value)
	}
	if m["tick"] != int64(3) && m["tick"] != 3 && m["tick"] != float64(3) {
		t.Errorf("tick = %#v; want 3 (the satisfying sample)", m["tick"])
	}
	if m["settled_index"] != int64(2) && m["settled_index"] != 2 && m["settled_index"] != float64(2) {
		t.Errorf("settled_index = %#v; want 2", m["settled_index"])
	}
}

func TestWaitState_TimeoutIncludesLastObserved(t *testing.T) {
	var n int
	src := func(context.Context, string) (msgpack.RawMessage, error) {
		n++
		return packSlice(t, map[string]any{
			"present": false,
			"screen":  "menu",
			"tick":    n,
		}), nil
	}

	_, err := WaitState(context.Background(), WaitStateArgs{
		Slice:     "carousel",
		Select:    "select(.present == true)",
		Timeout:   60 * time.Millisecond,
		PollEvery: 15 * time.Millisecond,
	}, src)
	if err == nil {
		t.Fatal("expected timeout")
	}
	var to *WaitTimeoutError
	if !errors.As(err, &to) {
		t.Fatalf("err type %T (%v); want *WaitTimeoutError", err, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "last observed") {
		t.Errorf("timeout error must include last observed; got %s", msg)
	}
	if strings.TrimSpace(msg) == "timed out" || msg == "timed out" {
		t.Fatal("bare 'timed out' is not self-diagnosing")
	}
	if !strings.Contains(msg, "menu") && !strings.Contains(msg, "present") {
		t.Errorf("last observed slice missing from error: %s", msg)
	}
	if to.LastValue == nil {
		t.Error("LastValue must be the last jq result")
	}
	if to.Attempts < 1 {
		t.Error("expected at least one attempt")
	}
}

func TestWaitState_AlreadyTrueDoesNotSleep(t *testing.T) {
	src := func(context.Context, string) (msgpack.RawMessage, error) {
		return packSlice(t, map[string]any{"ready": true}), nil
	}
	start := time.Now()
	got, err := WaitState(context.Background(), WaitStateArgs{
		Slice:     "session",
		Select:    ".ready",
		Timeout:   time.Second,
		PollEvery: 200 * time.Millisecond,
	}, src)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("already-true wait slept %s", time.Since(start))
	}
	if got.Value != true {
		t.Errorf("Value = %#v; want true", got.Value)
	}
}

func TestJQTruthy(t *testing.T) {
	cases := []struct {
		v    any
		want bool
	}{
		{nil, false},
		{false, false},
		{true, true},
		{[]any{}, false},
		{[]any{1}, true},
		{map[string]any{"a": 1}, true},
		{"", true}, // jq: empty string is truthy
		{0, true},  // jq: 0 is truthy
	}
	for _, c := range cases {
		if got := jqTruthy(c.v); got != c.want {
			t.Errorf("jqTruthy(%#v) = %v; want %v", c.v, got, c.want)
		}
	}
}
