// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/marcelocantos/spyder/internal/oslog"
)

func TestDTXDrainPreservesTerminalCaptureFailure(t *testing.T) {
	for _, cause := range []string{"DTX connection EOF", "batch decode underflow"} {
		t.Run(cause, func(t *testing.T) {
			rows := make(chan oslog.Record, 1)
			rows <- oslog.Record{PID: 42, ImageName: "Stock Cars", MessageType: "Info", Message: "before loss"}
			close(rows)
			failure := fmt.Errorf("%w: %s", oslog.ErrCaptureIntegrity, cause)
			var received []LogLine
			err := drainOSLogDTX(context.Background(), rows, func() error { return failure }, LogFilter{}, nil,
				func(row LogLine) bool { received = append(received, row); return true })
			if !errors.Is(err, oslog.ErrCaptureIntegrity) || err != failure || len(received) != 1 {
				t.Fatalf("opened stream loss erased: err=%v rows=%d", err, len(received))
			}
		})
	}
}

func TestDTXDrainCancellationCannotErasePriorFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	failure := fmt.Errorf("%w: already lost batch", oslog.ErrCaptureIntegrity)
	err := drainOSLogDTX(ctx, make(chan oslog.Record), func() error { return failure }, LogFilter{}, nil, func(LogLine) bool { return true })
	if err != failure {
		t.Fatalf("cancellation erased earlier integrity failure: %v", err)
	}
}

func TestDTXDrainStoppedConsumerCannotErasePriorFailure(t *testing.T) {
	rows := make(chan oslog.Record, 1)
	rows <- oslog.Record{Message: "queued before loss"}
	failure := fmt.Errorf("%w: already lost batch", oslog.ErrCaptureIntegrity)
	err := drainOSLogDTX(context.Background(), rows, func() error { return failure }, LogFilter{}, nil, func(LogLine) bool { return false })
	if err != failure {
		t.Fatalf("consumer stop erased earlier integrity failure: %v", err)
	}
}

func TestDTXDrainNormalCancellationAndFiltering(t *testing.T) {
	rows := make(chan oslog.Record, 4)
	for _, row := range []oslog.Record{
		{ImageName: "Another App", Subsystem: "wanted", Message: "match"},
		{ImageName: "Stock Cars", Subsystem: "other", Message: "match"},
		{ImageName: "Stock Cars", Subsystem: "wanted", Message: "ignore"},
		{PID: 42, ImageName: "Stock Cars", Subsystem: "wanted", Message: "match", MessageType: "Info"},
	} {
		rows <- row
	}
	close(rows)
	count := 0
	err := drainOSLogDTX(context.Background(), rows, func() error { return nil }, LogFilter{Process: "Stock Cars", Subsystem: "wanted"}, regexp.MustCompile("^match$"),
		func(row LogLine) bool { count++; return true })
	if err != nil || count != 1 {
		t.Fatalf("normal row filtering changed: err=%v count=%d", err, count)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = drainOSLogDTX(ctx, make(chan oslog.Record), func() error { return nil }, LogFilter{}, nil, func(LogLine) bool { return true })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("normal cancellation failed: %v", err)
	}
}
