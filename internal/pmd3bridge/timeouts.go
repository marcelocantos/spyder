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
	timeoutListDevices      = 10 * time.Second
	timeoutPowerAssertion   = 10 * time.Second
	timeoutListApps         = 30 * time.Second
	timeoutLaunchKillApp    = 30 * time.Second
	timeoutPidForBundle     = 30 * time.Second
	timeoutBattery          = 30 * time.Second
	timeoutScreenshot       = 30 * time.Second
	timeoutCrashReportsList = 120 * time.Second
	timeoutCrashReportsPull = 120 * time.Second
	timeoutReadyHandshake   = 10 * time.Second
	intervalLivenessProbe   = 30 * time.Second
)
