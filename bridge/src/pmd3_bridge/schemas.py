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


class BatteryRequest(BaseModel):
    udid: str


class ScreenshotRequest(BaseModel):
    udid: str


class CrashReportsListRequest(BaseModel):
    udid: str
    since_iso8601: Optional[str] = None
    process: Optional[str] = None


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


class BatteryResponse(BaseModel):
    level: Optional[float] = None
    charging: Optional[bool] = None


class ScreenshotResponse(BaseModel):
    png_b64: str


class CrashReportEntry(BaseModel):
    name: str
    process: str
    timestamp: str


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


class ErrorResponse(BaseModel):
    error: str
    message: str
