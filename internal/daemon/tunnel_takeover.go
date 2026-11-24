// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/iostunnel"
)

// tunnelHealthPolicy and tunnelHealthInterval govern health.Supervise for
// a tunnel this daemon owns.
var tunnelHealthPolicy = health.Policy{MaxAttempts: 5, BaseBackoff: 2 * time.Second}

const tunnelHealthInterval = 10 * time.Second

// tunnelTakeoverPoll is how often a daemon that found someone else's
// tunnel registry checks whether it is still there.
const tunnelTakeoverPoll = 2 * time.Second

// tunnelTakeoverMisses is how many consecutive failed probes count as
// "gone", so one slow answer does not start a second tunnel.
const tunnelTakeoverMisses = 2

// takeOverTunnel waits for a tunnel registry this daemon did not start to
// stop answering, then calls start (🎯T193). Without it, a daemon that
// restarts while the previous daemon's tunnel is still exiting is left
// with no tunnel at all. A failed start is retried on the next miss.
func takeOverTunnel(ctx context.Context, listening func() bool, start func() error, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if listening() {
			misses = 0
			continue
		}
		misses++
		if misses < tunnelTakeoverMisses {
			continue
		}
		if err := start(); err != nil {
			slog.Error("iostunnel: takeover start failed; will retry", "error", err)
			misses = 0
			continue
		}
		return
	}
}

// finishTunnel ends the daemon's hold on the iOS tunnel at shutdown.
// Resident runners left for the next daemon reach their devices
// through it, so with any left it stays up; otherwise it is stopped
// (🎯T236). Call before cancelling the tunnel's supervision context,
// whose cancellation stops a tunnel that was not released.
func finishTunnel(ctx context.Context, sup *iostunnel.Supervisor, leftRunners bool) {
	if leftRunners {
		sup.Release()
		return
	}
	if err := sup.Stop(ctx); err != nil {
		slog.Error("daemon: iostunnel stop error", "error", err)
	}
}
