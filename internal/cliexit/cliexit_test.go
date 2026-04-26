// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package cliexit_test

import (
	"testing"

	"github.com/marcelocantos/spyder/internal/cliexit"
)

func TestMapDaemonError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		statusCode   int
		errorCode    string
		errorMessage string
		want         int
	}{
		// ── Network failure (statusCode == 0) ─────────────────────────────
		{
			name:         "network: connection refused",
			statusCode:   0,
			errorMessage: "dial tcp: connection refused",
			want:         cliexit.ExitDaemonUnreachable,
		},
		{
			name:         "network: no such host",
			statusCode:   0,
			errorMessage: "dial tcp: no such host",
			want:         cliexit.ExitDaemonUnreachable,
		},
		{
			name:         "network: context deadline exceeded (no body) → timeout, not unreachable",
			statusCode:   0,
			errorMessage: "context deadline exceeded",
			want:         cliexit.ExitTimeout,
		},
		{
			name:         "network: can't assign requested address (e.g. port 0)",
			statusCode:   0,
			errorMessage: "dial tcp 127.0.0.1:0: connect: can't assign requested address",
			want:         cliexit.ExitDaemonUnreachable,
		},
		{
			name:         "network: no route to host",
			statusCode:   0,
			errorMessage: "dial tcp: no route to host",
			want:         cliexit.ExitDaemonUnreachable,
		},
		{
			name:         "network: network is unreachable",
			statusCode:   0,
			errorMessage: "dial tcp: network is unreachable",
			want:         cliexit.ExitDaemonUnreachable,
		},
		{
			name:         "network: empty errorMessage with statusCode 0",
			statusCode:   0,
			errorMessage: "",
			want:         cliexit.ExitDaemonUnreachable,
		},

		// ── HTTP 503 ──────────────────────────────────────────────────────
		{
			name:       "503 → daemon unreachable",
			statusCode: 503,
			want:       cliexit.ExitDaemonUnreachable,
		},

		// ── Explicit errorCode wins over prose ────────────────────────────
		{
			name:         "errorCode tunneld_unavailable beats prose",
			statusCode:   500,
			errorCode:    "tunneld_unavailable",
			errorMessage: "device not connected", // would be ExitDeviceNotConnected by prose
			want:         cliexit.ExitDaemonUnreachable,
		},
		{
			name:         "errorCode device_not_paired beats prose",
			statusCode:   422,
			errorCode:    "device_not_paired",
			errorMessage: "device not found", // would be ExitDeviceNotFound by prose
			want:         cliexit.ExitDeviceNotConnected,
		},
		{
			name:         "errorCode bundle_not_installed beats prose",
			statusCode:   422,
			errorCode:    "bundle_not_installed",
			errorMessage: "device is locked", // would be ExitDeviceLocked by prose
			want:         cliexit.ExitAppNotInstalled,
		},
		{
			name:         "errorCode developer_mode_disabled beats prose",
			statusCode:   422,
			errorCode:    "developer_mode_disabled",
			errorMessage: "trust not granted", // would be ExitTrustNotGranted by prose
			want:         cliexit.ExitDeveloperModeDisabled,
		},
		{
			name:         "errorCode pmd3_error is generic regardless of prose",
			statusCode:   500,
			errorCode:    "pmd3_error",
			errorMessage: "device not found", // would be ExitDeviceNotFound by prose
			want:         cliexit.ExitGeneric,
		},

		// ── Prose matches (no structured errorCode) ───────────────────────
		{
			name:         "prose: device not connected",
			statusCode:   422,
			errorMessage: "Device not connected to host",
			want:         cliexit.ExitDeviceNotConnected,
		},
		{
			name:         "prose: device not found",
			statusCode:   404,
			errorMessage: "Device not found in inventory",
			want:         cliexit.ExitDeviceNotFound,
		},
		{
			name:         "prose: app not installed",
			statusCode:   422,
			errorMessage: "App not installed on device",
			want:         cliexit.ExitAppNotInstalled,
		},
		{
			name:         "prose: bundle not installed",
			statusCode:   422,
			errorMessage: "Bundle not installed",
			want:         cliexit.ExitAppNotInstalled,
		},
		{
			name:         "prose: Locked",
			statusCode:   422,
			errorMessage: "Locked",
			want:         cliexit.ExitDeviceLocked,
		},
		{
			name:         "prose: device is locked",
			statusCode:   422,
			errorMessage: "The device is locked",
			want:         cliexit.ExitDeviceLocked,
		},
		{
			name:         "prose: Security",
			statusCode:   422,
			errorMessage: "Security error communicating with device",
			want:         cliexit.ExitTrustNotGranted,
		},
		{
			name:         "prose: trust",
			statusCode:   422,
			errorMessage: "trust dialog not accepted",
			want:         cliexit.ExitTrustNotGranted,
		},
		{
			name:         "prose: not trusted",
			statusCode:   422,
			errorMessage: "host is not trusted",
			want:         cliexit.ExitTrustNotGranted,
		},
		{
			name:         "prose: developer mode",
			statusCode:   422,
			errorMessage: "developer mode is not enabled on this device",
			want:         cliexit.ExitDeveloperModeDisabled,
		},
		{
			name:         "prose: context deadline exceeded (with body)",
			statusCode:   504,
			errorMessage: "context deadline exceeded while waiting for device",
			want:         cliexit.ExitTimeout,
		},
		{
			name:         "prose: timeout",
			statusCode:   504,
			errorMessage: "operation timed out",
			want:         cliexit.ExitTimeout,
		},
		{
			name:         "prose: reservation conflict",
			statusCode:   409,
			errorMessage: "reservation conflict: device held by session abc",
			want:         cliexit.ExitReservationConflict,
		},
		{
			name:         "prose: reservation held by",
			statusCode:   409,
			errorMessage: "reservation is held by another session",
			want:         cliexit.ExitReservationConflict,
		},
		{
			name:         "prose: not reserved by",
			statusCode:   403,
			errorMessage: "not reserved by this session",
			want:         cliexit.ExitNotReservedByYou,
		},
		{
			name:         "prose: not the holder",
			statusCode:   403,
			errorMessage: "caller is not the holder of the reservation",
			want:         cliexit.ExitNotReservedByYou,
		},

		// ── Fallback ──────────────────────────────────────────────────────
		{
			name:         "unknown error → generic",
			statusCode:   500,
			errorCode:    "some_unknown_code",
			errorMessage: "something went wrong",
			want:         cliexit.ExitGeneric,
		},
		{
			name:         "zero status with arbitrary message → daemon unreachable (any pre-response failure)",
			statusCode:   0,
			errorMessage: "something completely unrelated",
			want:         cliexit.ExitDaemonUnreachable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cliexit.MapDaemonError(tc.statusCode, tc.errorCode, tc.errorMessage)
			if got != tc.want {
				t.Errorf("MapDaemonError(%d, %q, %q) = %d, want %d",
					tc.statusCode, tc.errorCode, tc.errorMessage, got, tc.want)
			}
		})
	}
}
