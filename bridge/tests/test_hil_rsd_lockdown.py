# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""HIL (hardware-in-the-loop) integration tests for the RSD-first lockdown
path (🎯T42).

These tests require a real iOS 17+ device connected and registered with
tunneld.  They are skipped unless the environment variable SPYDER_DEVICES
is set (e.g. ``SPYDER_DEVICES=1 pytest tests/test_hil_rsd_lockdown.py``).

What these tests would assert (once enabled):
  1. ``list_apps`` returns a non-empty list via the tunneld RSD path —
     confirming that ``_service_provider_ctx`` selects the RSD and that
     ``InstallationProxyService`` works over RSD on an iOS 17+ device.
  2. ``battery`` returns a response with a numeric level in [0, 1] —
     confirming ``DiagnosticsService`` works over RSD.
  3. ``launch_app`` + ``pid_for_bundle`` + ``kill_app`` round-trip:
     launch a known app (bundle_id taken from the SPYDER_BUNDLE env var
     or defaulting to com.apple.Preferences), verify the PID is non-zero
     via ``pid_for_bundle``, kill it, verify ``pid_for_bundle`` returns
     None within 3 s.
  4. ``crash_reports_list`` returns without raising — confirming
     ``CrashReportsManager`` works over RSD.
  5. With tunneld stopped (or the device UDID removed from the registry),
     the same endpoints fall back gracefully: they either succeed via USBMux
     or raise ``device_not_paired`` — not an empty exception or panic.

Environment variables consumed:
  SPYDER_DEVICES  — set to any non-empty value to enable these tests.
  SPYDER_UDID     — UDID of the target device (required when enabled).
  SPYDER_BUNDLE   — bundle ID to use for the launch round-trip test
                    (default: com.apple.Preferences).
"""
from __future__ import annotations

import os

import pytest

_ENABLED = bool(os.environ.get("SPYDER_DEVICES"))
_UDID = os.environ.get("SPYDER_UDID", "")
_BUNDLE = os.environ.get("SPYDER_BUNDLE", "com.apple.Preferences")

pytestmark = pytest.mark.skipif(
    not _ENABLED,
    reason="Set SPYDER_DEVICES=1 and SPYDER_UDID=<udid> to run HIL tests",
)


async def test_hil_list_apps() -> None:
    """list_apps returns a non-empty list over the RSD path."""
    # TODO(🎯T42-HIL): import and call list_apps; assert len > 0.
    pytest.skip("HIL stub — implement when running against a real device")


async def test_hil_battery() -> None:
    """battery returns a level in [0, 1] over the RSD path."""
    # TODO(🎯T42-HIL): import and call battery; assert 0 <= level <= 1.
    pytest.skip("HIL stub — implement when running against a real device")


async def test_hil_launch_pid_kill_roundtrip() -> None:
    """launch_app / pid_for_bundle / kill_app round-trip."""
    # TODO(🎯T42-HIL):
    #   1. launch_app(UDID, BUNDLE) → pid; assert pid > 0.
    #   2. pid_for_bundle(UDID, BUNDLE) → pid; assert == launched pid.
    #   3. kill_app(UDID, BUNDLE).
    #   4. poll pid_for_bundle up to 3 s; assert returns None.
    pytest.skip("HIL stub — implement when running against a real device")


async def test_hil_crash_reports_list() -> None:
    """crash_reports_list completes without raising over the RSD path."""
    # TODO(🎯T42-HIL): import and call crash_reports_list; consume iterator.
    pytest.skip("HIL stub — implement when running against a real device")
