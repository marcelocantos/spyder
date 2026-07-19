// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"time"

	"github.com/marcelocantos/spyder/internal/health"
)

// Health-model identities for the daemon's own long-lived entities.
var (
	daemonSelfHealthID = health.ID{Kind: health.KindDaemon, Name: "spyder"}
)

// startHealthWiring attaches the attention notifier to the live model and
// registers daemon-self. iOS-tunnel liveness/restart is owned solely by
// health.Supervisor.Supervise on the iostunnel ManagedProcess (🎯T90.5.1) —
// no separate tunnelRegistryProber.
//
// Device-level health is driven by the iOS adapter's usbmux listener
// (🎯T89.2) feeding the classifier.
func startHealthWiring(ctx context.Context, sup *health.Supervisor, tunnelUp bool) {
	m := sup.Model()

	// Push exactly one actionable macOS notification per un-self-healable
	// fault; everything else stays in the pull surface.
	health.NewAttentionNotifier(health.NewMacOSNotifier()).Attach(m)

	// Daemon-self baseline (🎯T99.3 ProgressWatchdog reuses name "spyder").
	m.Register(daemonSelfHealthID, health.KindDaemon, health.Policy{
		MaxAttempts: 3,
		BaseBackoff: 2 * time.Second,
	})
	// Heartbeat while process is responsive. Stall detection overrides via
	// ProgressWatchdog / self-restart path.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.Observe(daemonSelfHealthID, true, "daemon tick")
			}
		}
	}()
	_ = tunnelUp // tunnel entity registered by Supervise when present
}
