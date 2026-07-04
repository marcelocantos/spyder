// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"time"

	"github.com/danielpaulus/go-ios/ios/tunnel"

	"github.com/marcelocantos/spyder/internal/goios"
	"github.com/marcelocantos/spyder/internal/health"
)

// tunnelProbeInterval is how often the daemon pings the bundled ios tunnel
// daemon's registry to reflect its liveness in the health model. Matches
// the tunnel supervisor's own probe cadence — this is the surfacing view,
// not the recovery driver (the iostunnel supervisor still owns restart).
const tunnelProbeInterval = 10 * time.Second

// Health-model identities for the daemon's own long-lived entities.
var (
	daemonSelfHealthID = health.ID{Kind: health.KindDaemon, Name: "spyder"}
	iosTunnelHealthID  = health.ID{Kind: health.KindSubprocess, Name: "ios-tunnel"}
)

// startHealthWiring attaches the attention notifier to the live model and
// starts the background probes that populate it (🎯T90). It runs until ctx
// is cancelled. Device-level health is driven separately by the iOS
// adapter's usbmux listener (🎯T89.2) feeding the classifier.
//
// This is the surfacing/notification spine: the model is the single source
// of truth (T90.1), the notifier pushes only un-self-healable faults
// (T90.4), and `spyder status` / /api/v1/health / the health() builtin
// read it (T90.3).
func startHealthWiring(ctx context.Context, sup *health.Supervisor, tunnelUp bool) {
	m := sup.Model()

	// Push exactly one actionable macOS notification per un-self-healable
	// fault; everything else stays in the pull surface.
	health.NewAttentionNotifier(health.NewMacOSNotifier()).Attach(m)

	// Daemon-self baseline. A progress watchdog around wedge-prone handlers
	// (🎯T83) attaches to this entity; until those call sites are
	// instrumented it simply reads healthy.
	m.Register(daemonSelfHealthID, health.KindDaemon, health.Policy{})

	// Surface the bundled ios tunnel daemon's registry liveness. Probe-only
	// (the iostunnel supervisor owns restart), so a wedged registry shows as
	// degraded in `spyder status` rather than escalating to a notification —
	// folding the restart action into health.Supervisor is the remaining
	// T90.5 step.
	if tunnelUp {
		m.Register(iosTunnelHealthID, health.KindSubprocess, health.Policy{})
		go m.RunPoll(ctx, tunnelProbeInterval, tunnelRegistryProber{
			host: goios.DefaultTunnelHost,
			port: goios.DefaultTunnelPort,
		})
	}
}

// tunnelRegistryProber reports whether the ios tunnel daemon's registry
// HTTP endpoint is responsive — the "process up but registry wedged"
// signal, observed into the health model.
type tunnelRegistryProber struct {
	host string
	port int
}

func (p tunnelRegistryProber) Probe() []health.ProbeResult {
	_, err := tunnel.ListRunningTunnels(p.host, p.port)
	detail := "registry responsive"
	if err != nil {
		detail = "registry unresponsive: " + err.Error()
	}
	return []health.ProbeResult{{
		ID:     iosTunnelHealthID,
		OK:     err == nil,
		Detail: detail,
	}}
}
