# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
from __future__ import annotations

from typing import Optional

from pydantic import BaseModel


# ── Request models ─────────────────────────────────────────────────────────────

class UdidRequest(BaseModel):
    udid: str


class ListAppsRequest(BaseModel):
    udid: str


class LaunchAppRequest(BaseModel):
    udid: str
    bundle_id: str


class KillAppRequest(BaseModel):
    udid: str
    bundle_id: str


class PidForBundleRequest(BaseModel):
    udid: str
    bundle_id: str


class AppStateRequest(BaseModel):
    udid: str
    bundle_id: str


class BatteryRequest(BaseModel):
    udid: str


class ScreenshotRequest(BaseModel):
    udid: str


class CrashReportsListRequest(BaseModel):
    udid: str
    since_iso8601: Optional[str] = None
    process: Optional[str] = None


class SyslogRequest(BaseModel):
    """Streaming syslog watch request.

    pid:           filter to a single process id (-1 = all).
    process_name:  filter by image_name (case-sensitive exact match).
                   Empty = no filter.
    subsystem:     filter by SyslogLabel.subsystem (case-sensitive exact
                   match). Empty = no filter.
    """

    udid: str
    pid: int = -1
    process_name: Optional[str] = None
    subsystem: Optional[str] = None


class CrashReportsPullRequest(BaseModel):
    udid: str
    name: str


class AcquirePowerAssertionRequest(BaseModel):
    udid: str
    type: str
    name: str
    timeout_sec: int
    details: Optional[str] = None


class RefreshPowerAssertionRequest(BaseModel):
    handle_id: str
    timeout_sec: int


class ReleasePowerAssertionRequest(BaseModel):
    handle_id: str


# ── Response models ────────────────────────────────────────────────────────────

class DeviceInfo(BaseModel):
    udid: str
    name: str
    product_type: str
    os_version: str


class ListDevicesResponse(BaseModel):
    devices: list[DeviceInfo]


class AppInfo(BaseModel):
    bundle_id: str
    name: Optional[str] = None
    version: Optional[str] = None


class ListAppsResponse(BaseModel):
    apps: list[AppInfo]


class LaunchAppResponse(BaseModel):
    pid: int


class KillAppResponse(BaseModel):
    pass


class PidForBundleResponse(BaseModel):
    pid: Optional[int] = None


class AppStateResponse(BaseModel):
    """Lifecycle state of one app on the device.

    state is one of:
      - "running":      the app is foregrounded (BKS state_description = Running).
      - "backgrounded": the app exists but is suspended or in background mode
                        (state_description ∈ {Suspended, Background, ...}).
      - "terminated":   the app is not in the BackBoard state list — either
                        never launched in this boot, or iOS has fully reaped it.

    Used by autoawake to detect user-initiated opt-out: a Running →
    backgrounded transition for KeepAwake means the user swiped away
    (or launched another app), and autoawake should stay passive.
    """

    state: str
    description: str = ""  # raw state_description from BKS, for debugging


class BatteryResponse(BaseModel):
    level: Optional[float] = None
    charging: Optional[bool] = None


class ScreenshotResponse(BaseModel):
    png_b64: str


class CrashReportEntry(BaseModel):
    name: str
    process: str
    timestamp: str


class SyslogEntry(BaseModel):
    """One emitted syslog line, structured.

    timestamp is RFC3339 with the device-local timezone preserved (pmd3
    surfaces a tz-aware datetime; we keep that).
    process is the image_name (executable basename) reported by pmd3.
    level is the SyslogLogLevel enum name (NOTICE, INFO, DEBUG,
    USER_ACTION, ERROR, FAULT) — not lower-cased; clients normalise.
    subsystem and category are flattened from SyslogLabel for
    convenience; both empty when label is None.
    """

    pid: int
    timestamp: str
    level: str
    process: str
    subsystem: str = ""
    category: str = ""
    message: str


class CrashReportsListResponse(BaseModel):
    reports: list[CrashReportEntry]


class CrashReportsPullResponse(BaseModel):
    content: str
    mime: str = "application/x-apple-crashreport"


class AcquirePowerAssertionResponse(BaseModel):
    handle_id: str


class RefreshPowerAssertionResponse(BaseModel):
    pass


class ReleasePowerAssertionResponse(BaseModel):
    pass


class DevicePowerStateRequest(BaseModel):
    udid: str


class DevicePowerStateResponse(BaseModel):
    """Power/display state of a device.

    state values:
      "awake"       — display is on and the framebuffer is readable (DVT screenshot succeeded).
      "display_off" — screenshot succeeded but pixels are entirely dark (framebuffer
                      blank, display off or screen saver).
      "asleep"      — screenshot failed with an error that indicates the display/device
                      is off or asleep (e.g. DVT instrument closed by device).
      "unknown"     — could not determine state (tunneld unavailable, developer mode
                      off, or unrecognised error).
    """
    state: str
    detail: Optional[str] = None


class ErrorResponse(BaseModel):
    error: str
    message: str
