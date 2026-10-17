// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package goios

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios"
)

type traceBuffer struct {
	mu      sync.Mutex
	data    bytes.Buffer
	changed chan struct{}
}

func (b *traceBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	n, err := b.data.Write(data)
	b.mu.Unlock()
	select {
	case b.changed <- struct{}{}:
	default:
	}
	return n, err
}
func (b *traceBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.data.String() }
func captureTrace(t *testing.T) *traceBuffer {
	t.Helper()
	buffer := &traceBuffer{changed: make(chan struct{}, 1)}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buffer
}
func traceEvents(t *testing.T, buffer *traceBuffer, phase, event string) []map[string]any {
	t.Helper()
	var result []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["phase"] == phase && record["event"] == event {
			result = append(result, record)
		}
	}
	return result
}
func waitTraceEvent(t *testing.T, buffer *traceBuffer, phase string) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for len(traceEvents(t, buffer, phase, "start")) == 0 {
		select {
		case <-buffer.changed:
		case <-timer.C:
			t.Fatalf("phase never entered: %s", buffer.String())
		}
	}
}

func TestLifecycleTrace_ExactCorrelationNestedParentAndSanitizedError(t *testing.T) {
	output := captureTrace(t)
	root := LifecycleOperation(context.Background(), 17, "ensure_session", "fixture-a")
	child := LifecycleOperation(root, 18, "terminate_app", "fixture-a")
	ended := LifecyclePhase(child, "pid.list_processes", "connection_generation", 3)
	ended(errors.New("PRIVATE_PAYLOAD_MUST_NOT_LOG"))
	ended(nil)
	log := output.String()
	if strings.Contains(log, "PRIVATE_PAYLOAD") || strings.Count(log, `"event":"start"`) != 1 || strings.Count(log, `"event":"end"`) != 1 || !strings.Contains(log, `"connection_generation":3`) || !strings.Contains(log, `"phase":"pid.list_processes"`) || !strings.Contains(log, `"parent_operation_id":"`) {
		t.Fatal(log)
	}
	LifecyclePhase(context.Background(), "unrelated")(nil)
	if output.String() != log {
		t.Fatal("unrelated background probe emitted a correlated phase")
	}
}
func TestLifecycleTrace_ActualFactoryPendingAndLateGenerationCleanup(t *testing.T) {
	output := captureTrace(t)
	r := New(DefaultTunnelHost, DefaultTunnelPort)
	r.cache["fixture"] = &entry{dev: ios.DeviceEntry{}, major: 27, resolved: time.Now()}
	entered, drain := make(chan struct{}), make(chan struct{})
	var closed atomic.Int32
	pool := NewServicePool(r, func(ios.DeviceEntry) (int, error) { close(entered); <-drain; return 1, nil }, func(int) error { closed.Add(1); return nil }, time.Minute)
	defer pool.Close()
	ctx, cancel := context.WithCancel(LifecycleOperation(context.Background(), 19, "terminate_app", "fixture"))
	done := make(chan error, 1)
	go func() { _, err := pool.AcquireLease(ctx, "fixture"); done <- err }()
	<-entered
	log := output.String()
	if len(traceEvents(t, output, "service.factory", "start")) != 1 || len(traceEvents(t, output, "service.factory", "end")) != 0 {
		t.Fatal(log)
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("opaque factory returned before actual drain: %v", err)
	default:
	}
	close(drain)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("factory did not drain")
	}
	if closed.Load() != 1 || pool.Opens() != 0 {
		t.Fatalf("closed=%d opens=%d", closed.Load(), pool.Opens())
	}
	log = output.String()
	if len(traceEvents(t, output, "service.factory", "end")) != 1 || len(traceEvents(t, output, "service.late_cancel_close", "end")) != 1 || !strings.Contains(log, `"outcome":"cancelled"`) {
		t.Fatal(log)
	}
}
