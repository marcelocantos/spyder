// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package clitimeout provides uniform per-command timeout support for spyder
// CLI subcommands: a flag registrar and a context factory.
package clitimeout

import (
	"context"
	"flag"
	"fmt"
	"time"
)

// Default timeout durations for each command family.
const (
	// DefaultRead covers devices, list-apps, reservations, resolve,
	// device-state, runs commands, sim/emu/pool list, crashes,
	// log (non-follow), diff, and baselines list.
	DefaultRead = 10 * time.Second

	// DefaultLaunch covers launch-app and terminate-app.
	DefaultLaunch = 60 * time.Second

	// DefaultInstall covers install and uninstall.
	DefaultInstall = 5 * time.Minute

	// DefaultDeploy covers deploy (terminate + install + launch + verify pid).
	DefaultDeploy = 10 * time.Minute

	// DefaultScreenshot covers screenshot commands.
	DefaultScreenshot = 30 * time.Second

	// DefaultRecord covers record start/stop control calls (not the recording
	// duration itself).
	DefaultRecord = 60 * time.Second

	// DefaultReserve covers reserve, release, renew, rotate, and baseline update.
	DefaultReserve = 30 * time.Second

	// DefaultLogStream means no timeout — used for log --follow.
	DefaultLogStream time.Duration = 0

	// DefaultRun means no timeout — spyder run wraps a user command whose own
	// runtime governs.
	DefaultRun time.Duration = 0
)

// durationFlag is a flag.Value that parses a Go duration string and stores
// the result in a *time.Duration.
type durationFlag struct {
	val *time.Duration
}

func (f *durationFlag) String() string {
	if f.val == nil {
		return "0s"
	}
	return f.val.String()
}

func (f *durationFlag) Set(s string) error {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*f.val = d
	return nil
}

// RegisterFlag registers a --timeout flag on fs with the supplied default.
// The returned *time.Duration is valid after fs.Parse has been called.
//
// When defaultDur == 0, the help text advertises "0 (no timeout)".
func RegisterFlag(fs *flag.FlagSet, defaultDur time.Duration) *time.Duration {
	val := defaultDur
	df := &durationFlag{val: &val}

	defaultStr := "0 (no timeout)"
	if defaultDur != 0 {
		defaultStr = defaultDur.String()
	}
	fs.Var(df, "timeout",
		fmt.Sprintf("request timeout (e.g. 30s, 5m); 0 disables (default %s)", defaultStr))

	return &val
}

// Context returns a context derived from context.Background() with the given
// timeout applied. When d == 0, no deadline is set and the cancel function is
// a no-op. Callers must always defer the returned cancel.
func Context(d time.Duration) (context.Context, context.CancelFunc) {
	if d == 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), d)
}
