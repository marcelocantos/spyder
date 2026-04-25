// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package clitimeout_test

import (
	"context"
	"flag"
	"testing"
	"time"

	"github.com/marcelocantos/spyder/internal/clitimeout"
)

// TestRegisterFlagDefault verifies that the registered duration pointer holds
// the default value when --timeout is not supplied.
func TestRegisterFlagDefault(t *testing.T) {
	tests := []struct {
		name string
		def  time.Duration
	}{
		{"non-zero default", clitimeout.DefaultRead},
		{"zero default (no timeout)", clitimeout.DefaultLogStream},
		{"install default", clitimeout.DefaultInstall},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			dur := clitimeout.RegisterFlag(fs, tt.def)
			if err := fs.Parse([]string{}); err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			if *dur != tt.def {
				t.Errorf("got %v, want %v", *dur, tt.def)
			}
		})
	}
}

// TestRegisterFlagOverride verifies that --timeout=X is respected.
func TestRegisterFlagOverride(t *testing.T) {
	tests := []struct {
		flagVal string
		want    time.Duration
	}{
		{"2s", 2 * time.Second},
		{"5m", 5 * time.Minute},
		{"0", 0},
		{"1h30m", 90 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.flagVal, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			dur := clitimeout.RegisterFlag(fs, clitimeout.DefaultRead)
			if err := fs.Parse([]string{"--timeout=" + tt.flagVal}); err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			if *dur != tt.want {
				t.Errorf("got %v, want %v", *dur, tt.want)
			}
		})
	}
}

// TestRegisterFlagInvalidDuration verifies that an invalid duration string
// causes fs.Parse to return a non-nil error.
func TestRegisterFlagInvalidDuration(t *testing.T) {
	invalid := []string{"abc", "10", "2days", "-", ""}

	for _, s := range invalid {
		t.Run(s, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			clitimeout.RegisterFlag(fs, clitimeout.DefaultRead)
			err := fs.Parse([]string{"--timeout=" + s})
			if err == nil {
				t.Errorf("expected error for invalid duration %q, got nil", s)
			}
		})
	}
}

// TestContextZero verifies that Context(0) returns context.Background() (no
// deadline) and a callable cancel.
func TestContextZero(t *testing.T) {
	ctx, cancel := clitimeout.Context(0)
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Error("expected no deadline for Context(0), but one was set")
	}

	// Cancel must be a no-op and safe to call.
	cancel()
	cancel() // calling twice must not panic
}

// TestContextNonZero verifies that Context(d>0) sets a deadline and the
// context fires after d elapses.
func TestContextNonZero(t *testing.T) {
	const d = 5 * time.Millisecond

	ctx, cancel := clitimeout.Context(d)
	defer cancel()

	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("expected a deadline for Context(d>0), but none was set")
	}

	// Wait for the context to expire.
	select {
	case <-ctx.Done():
		if ctx.Err() != context.DeadlineExceeded {
			t.Errorf("expected DeadlineExceeded, got %v", ctx.Err())
		}
	case <-time.After(d + 200*time.Millisecond):
		t.Error("context did not fire within expected window")
	}
}

// TestContextCancelFunction verifies that the cancel returned by Context(d>0)
// is callable (and stops the deadline from firing early).
func TestContextCancelFunction(t *testing.T) {
	ctx, cancel := clitimeout.Context(10 * time.Second)

	// Cancel immediately — context should be done with Canceled, not timeout.
	cancel()
	cancel() // second call must not panic

	select {
	case <-ctx.Done():
		// Success — context was cancelled.
	default:
		t.Error("context not done after explicit cancel")
	}
}
