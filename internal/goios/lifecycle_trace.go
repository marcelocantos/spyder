// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

type lifecycleTraceKey struct{}
type lifecycleTrace struct{ id, parent, tool, device string }

var traceProcessStart = time.Now().UnixNano()

// LifecycleOperation correlates one real dispatch with its provider phases.
// It observes only; deadlines, retries, ownership and outcomes are unchanged.
func LifecycleOperation(ctx context.Context, operation uint64, tool, device string) context.Context {
	value := lifecycleTrace{id: fmt.Sprintf("%d-%d-%d", os.Getpid(), traceProcessStart, operation), tool: tool, device: device}
	if parent, ok := ctx.Value(lifecycleTraceKey{}).(lifecycleTrace); ok {
		value.parent = parent.id
	}
	return context.WithValue(ctx, lifecycleTraceKey{}, value)
}

// LifecyclePhase logs bounded identifiers and error classes, never payloads,
// environment values, pair records, account identifiers or error messages.
// Direct background probes without a dispatch context emit no extra events.
func LifecyclePhase(ctx context.Context, phase string, attributes ...any) func(error) {
	operation, ok := ctx.Value(lifecycleTraceKey{}).(lifecycleTrace)
	if !ok {
		return func(error) {}
	}
	started := time.Now()
	common := []any{"operation_id", operation.id, "parent_operation_id", operation.parent, "tool", operation.tool, "device", operation.device, "phase", phase}
	common = append(common, attributes...)
	slog.Info("iOS lifecycle phase", append(append([]any{}, common...), "event", "start")...)
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			outcome := "ok"
			if err != nil {
				outcome = "error"
			}
			if ctx.Err() != nil {
				outcome = "cancelled"
			}
			slog.Info("iOS lifecycle phase", append(append([]any{}, common...), "event", "end", "elapsed_ms", time.Since(started).Milliseconds(), "outcome", outcome, "error_type", fmt.Sprintf("%T", err), "cancelled", errors.Is(err, context.Canceled), "deadline", errors.Is(err, context.DeadlineExceeded))...)
		})
	}
}
