// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package clidaemon holds CLI-side daemon reachability and auto-start
// policy (🎯T173). Kept out of main so restart-wait and spawn
// preflight are unit-testable without execing spyder serve.
package clidaemon

import (
	"context"
	"fmt"
	"time"

	"github.com/marcelocantos/spyder/internal/goios"
	"github.com/marcelocantos/spyder/internal/listenaddr"
	"github.com/marcelocantos/spyder/internal/listenpreflight"
)

// RestartWait is how long the CLI polls the default loopback daemon when
// state shows a prior installation before trying auto-start.
const RestartWait = 45 * time.Second

// RestartPoll is the interval between reachability probes during RestartWait.
const RestartPoll = 200 * time.Millisecond

// ExistingInstall reports whether spyder has persisted a prior serve bind
// (~/.spyder/listen-addr). When true, ECONNREFUSED on the default URL is
// more likely a supervised restart than a machine with no daemon yet.
func ExistingInstall(listenAddrPath string) bool {
	return listenaddr.Load(listenAddrPath) != ""
}

// AutoStartPreflight returns an error when spawning a detached serve would
// risk a second long-lived daemon (🎯T173 / ENT-008).
func AutoStartPreflight() error {
	if inUse, err := listenpreflight.AnyTCPListener(listenpreflight.DaemonPort); err != nil {
		return fmt.Errorf("listen preflight on :%d: %w", listenpreflight.DaemonPort, err)
	} else if inUse {
		return fmt.Errorf(
			"port %d already has a TCP listener (see: lsof -nP -iTCP:%d -sTCP:LISTEN); "+
				"auto-start would risk a second daemon — start or restart the supervised service instead",
			listenpreflight.DaemonPort, listenpreflight.DaemonPort,
		)
	}
	tunnelPort := goios.DefaultTunnelPort
	if inUse, err := listenpreflight.AnyTCPListener(tunnelPort); err != nil {
		return fmt.Errorf("listen preflight on iOS tunnel :%d: %w", tunnelPort, err)
	} else if inUse {
		return fmt.Errorf(
			"iOS tunnel port %d already has a TCP listener; "+
				"a supervised daemon may be restarting — wait for brew services or stop the stray tunnel owner before auto-start",
			tunnelPort,
		)
	}
	return nil
}

// PollWhileConnRefused keeps calling fn while it reports connRefused, until
// timeout or fn reports a different outcome. Returns the last fn error when
// connRefused persists through the deadline (may be nil if fn never ran).
func PollWhileConnRefused(ctx context.Context, timeout, poll time.Duration, fn func() (connRefused bool, err error)) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		refused, err := fn()
		last = err
		if !refused {
			return err
		}
		if time.Now().After(deadline) {
			return last
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
