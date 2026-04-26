// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package cliexit defines structured exit-code constants and helpers for
// spyder's CLI. Callers import this package and use its constants to
// communicate specific failure modes to Make scripts and shell wrappers.
//
// Exit-code taxonomy:
//
//	 0 – OK
//	 1 – generic failure
//	 2 – usage / argument error
//	10 – daemon unreachable
//	11 – device not found in inventory
//	12 – device not connected (cable, trust, pairing)
//	13 – reservation conflict (device held by another session)
//	14 – device not reserved by the calling session
//	15 – selector grammar not supported (input is neither a known alias
//	     nor a parseable predicate)
//	20 – app not installed on device
//	21 – app install failed
//	22 – app not running on device (also: launch failed). Used by
//	     `is_running` for "installed but not currently running".
//	23 – app terminate failed
//	24 – PID verification failed
//	30 – timeout
//	40 – trust not granted
//	41 – developer mode disabled
//	42 – device locked
package cliexit

import (
	"fmt"
	"os"
	"strings"
)

// Exit-code constants. Values are intentionally stable — scripts may
// hard-code them.
const (
	ExitOK                    = 0
	ExitGeneric               = 1
	ExitUsage                 = 2
	ExitDaemonUnreachable     = 10
	ExitDeviceNotFound        = 11
	ExitDeviceNotConnected    = 12
	ExitReservationConflict   = 13
	ExitNotReservedByYou      = 14
	ExitSelectorNotSupported  = 15
	ExitAppNotInstalled       = 20
	ExitInstallFailed         = 21
	ExitLaunchFailed          = 22 // also surfaced by is_running as ExitAppNotRunning
	ExitAppNotRunning         = 22
	ExitTerminateFailed       = 23
	ExitPIDVerificationFailed = 24
	ExitTimeout               = 30
	ExitTrustNotGranted       = 40
	ExitDeveloperModeDisabled = 41
	ExitDeviceLocked          = 42
)

// MapDaemonError converts a daemon HTTP error response into an exit code.
//
// Precedence (highest to lowest):
//  1. statusCode == 0 → transport-level failure before any HTTP response
//     was received. Subdivided:
//     - "context deadline exceeded" / "timeout" / "timed out" → ExitTimeout.
//     - Everything else (connection refused, no such host, can't assign
//     requested address, no route to host, network unreachable, …) →
//     ExitDaemonUnreachable. A request that never reached the daemon
//     is, by definition, an unreachable-daemon failure regardless of
//     the specific syscall errno.
//  2. statusCode == 503 → ExitDaemonUnreachable (upstream tunneld down).
//  3. Explicit errorCode match — structured codes win over prose.
//  4. Prose match on errorMessage (case-insensitive substring).
//  5. Fallback → ExitGeneric.
func MapDaemonError(statusCode int, errorCode, errorMessage string) int {
	// 1. Network failure before any HTTP response.
	if statusCode == 0 {
		lower := strings.ToLower(errorMessage)
		if strings.Contains(lower, "context deadline exceeded") ||
			strings.Contains(lower, "timeout") ||
			strings.Contains(lower, "timed out") {
			return ExitTimeout
		}
		return ExitDaemonUnreachable
	}

	// 2. HTTP 503 — daemon or its upstream (tunneld) is down.
	if statusCode == 503 {
		return ExitDaemonUnreachable
	}

	// 3. Explicit structured error codes (highest precedence over prose).
	switch errorCode {
	case "tunneld_unavailable":
		return ExitDaemonUnreachable
	case "device_not_paired":
		return ExitDeviceNotConnected
	case "bundle_not_installed":
		return ExitAppNotInstalled
	case "developer_mode_disabled":
		return ExitDeveloperModeDisabled
	case "pmd3_error":
		return ExitGeneric
	}

	// 4. Prose match on errorMessage (case-insensitive).
	lower := strings.ToLower(errorMessage)

	// Reservation checks first to avoid false matches on other substrings.
	if strings.Contains(lower, "reservation") {
		if strings.Contains(lower, "conflict") || strings.Contains(lower, "held by") {
			return ExitReservationConflict
		}
	}
	if strings.Contains(lower, "not reserved by") || strings.Contains(lower, "not the holder") {
		return ExitNotReservedByYou
	}

	// Device connectivity / presence.
	if strings.Contains(lower, "device not connected") {
		return ExitDeviceNotConnected
	}
	if strings.Contains(lower, "device not found") {
		return ExitDeviceNotFound
	}

	// App presence.
	if strings.Contains(lower, "app not installed") || strings.Contains(lower, "bundle not installed") {
		return ExitAppNotInstalled
	}

	// Device state: locked → trust → developer mode.
	// Check "locked" before "trust"/"security" to avoid cross-match.
	if strings.Contains(lower, "locked") || strings.Contains(lower, "device is locked") {
		return ExitDeviceLocked
	}
	if strings.Contains(lower, "security") || strings.Contains(lower, "trust") || strings.Contains(lower, "not trusted") {
		return ExitTrustNotGranted
	}
	if strings.Contains(lower, "developer mode") {
		return ExitDeveloperModeDisabled
	}

	// Timeout.
	if strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "timed out") {
		return ExitTimeout
	}

	// 5. Fallback.
	return ExitGeneric
}

// Errorf writes a formatted message to os.Stderr (appending a newline if
// absent) and exits with the given code.
func Errorf(code int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	fmt.Fprint(os.Stderr, msg)
	os.Exit(code)
}

// Exit is a thin wrapper around os.Exit so callers can use a single import
// for all exit-code needs without importing os directly.
func Exit(code int) {
	os.Exit(code)
}
