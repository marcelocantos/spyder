// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pmd3bridge

import "time"

// Per-endpoint bug-detection thresholds. These are deliberately generous —
// 2–5× observed worst-case legitimate durations — so that a breach is a strong
// "bridge is unresponsive" signal rather than a "slow device" signal.
//
// Because this is a same-host, same-trust-domain subprocess (not a network
// peer), any breach represents a bug in the bridge, the transport, or us.
// The daemon panics on breach; the external process supervisor (launchd,
// `brew services`) restarts the whole process tree with a clean state.
//
// Do not tune these down to "tight enough to be useful as a SLO" — they are
// assertion thresholds, not SLOs.
const (
	// timeoutHealth bounds the /v1/health probe (🎯T50). The endpoint
	// touches no device state and should return immediately; the budget
	// is generous so that a healthy bridge never trips a false alarm
	// even under heavy concurrent load.
	timeoutHealth         = 5 * time.Second
	timeoutListDevices    = 10 * time.Second
	timeoutPowerAssertion = 10 * time.Second
	timeoutListApps       = 30 * time.Second
	timeoutLaunchKillApp  = 30 * time.Second
	timeoutPidForBundle   = 30 * time.Second
	timeoutAppState       = 30 * time.Second
	timeoutBattery        = 30 * time.Second
	timeoutScreenshot     = 30 * time.Second
	timeoutDeviceState    = 35 * time.Second // screenshot + classification overhead (🎯T29)
	timeoutReadyHandshake = 10 * time.Second
	intervalLivenessProbe = 30 * time.Second

	// Streaming endpoints (🎯T26.3) use a two-tier timeout: a very generous
	// outer end-to-end deadline acts as a safety net; the real bug detector
	// is the inter-packet deadline applied to every chunk read.
	timeoutStreamEndToEnd = 30 * time.Minute
)

// interPacketDeadline applies to streaming endpoints. A gap larger than
// this between successive chunks panics the daemon — the bridge has
// stopped making progress and something is wrong. The primitive-level
// stallReader tests (stream_test.go) pass an explicit deadline rather
// than mutating this value.
const interPacketDeadline = 10 * time.Second
