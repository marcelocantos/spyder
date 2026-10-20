// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/oslog"
)

func TestDTXDecodedTupleIdentitySurvivesDiagnosticNormalization(t *testing.T) {
	// Fixed synthetic VM input, not captured provider data: Logs has process,
	// thread, process-image-path, message columns; process=(42,opaque),
	// thread=(84,process), image=/private/Test/StockCars, message=ready.
	// This crosses the actual decoder→diagnostic boundary instead of creating
	// an already-normalized Record with an invented PID in the test.
	wire, err := hex.DecodeString("00c000c01b9377b600cc1c9cf6a6958d73b300c01a9d2687859500e41c9cf6a6958d73b35a8bd6969d852da5189c469700e0599b3797858d65a700c00469000180ca408030a014d0026900d501650269dc8b2687d9a574a14b9946b5cd952fb4dd94f686ad8d61839c9c00f0989b2797858969b4999dd792959174b800c0999c1696e4d1026901690002")
	if err != nil {
		t.Fatal(err)
	}
	d := oslog.NewDecoder()
	rows, err := d.Decode(wire, nil)
	if err != nil || d.Finish() != nil || len(rows) != 1 {
		t.Fatalf("synthetic wire decode: rows=%d err=%v", len(rows), err)
	}
	diag := DiagnosticFromActivityTrace(rows[0], time.Unix(1, 0))
	if diag.PID != 42 || diag.TID != 84 || diag.PIDStatus != IdentityObserved || diag.TIDStatus != IdentityObserved {
		t.Fatalf("actual decoded identity was lost at diagnostic boundary: PID=%d/%s TID=%d/%s", diag.PID, diag.PIDStatus, diag.TID, diag.TIDStatus)
	}
	if diag.Process.Value != "StockCars" || diag.Process.Status != IdentityObserved {
		t.Fatalf("decoded executable identity changed: %+v", diag.Process)
	}
	if diag.Message != "ready" {
		t.Fatalf("decoded message changed: %q", diag.Message)
	}
}

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
