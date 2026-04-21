# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""Thin async wrappers around pymobiledevice3 services.

Every function is async because pmd3's public API is async throughout
(`create_using_usbmux`, `get_apps`, `proclist`, `take_screenshot`,
`CrashReportsManager.ls/pull`, and so on). Callers — FastAPI route
handlers — `await` these directly.

All functions raise BridgeError on classifiable failure conditions;
app.py translates BridgeError into the corresponding HTTP status.
"""
from __future__ import annotations

import base64
import os
import re
import tempfile
from datetime import datetime, timezone
from typing import Optional

from .schemas import (
    AppInfo,
    BatteryResponse,
    CrashReportEntry,
    DeviceInfo,
)


class BridgeError(Exception):
    """Raised when a pmd3 operation fails in a classifiable way."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code
        self.message = message


async def _lockdown(udid: str):  # type: ignore[return]
    """Open a lockdown client for the given UDID.

    Raises BridgeError if the device is not paired or not reachable.
    """
    try:
        from pymobiledevice3.lockdown import create_using_usbmux
        return await create_using_usbmux(serial=udid)
    except Exception as exc:
        msg = str(exc).lower()
        if "pair" in msg or "trust" in msg:
            raise BridgeError(
                "device_not_paired",
                f"Device {udid} is not paired: {exc}",
            ) from exc
        raise BridgeError(
            "pmd3_error",
            f"Failed to connect to device {udid}: {exc}",
        ) from exc


async def list_devices() -> list[DeviceInfo]:
    """Return every USB-connected device visible to usbmux."""
    try:
        from pymobiledevice3.usbmux import select_devices_by_connection_type
        muxes = await select_devices_by_connection_type(connection_type="USB")
    except Exception as exc:
        raise BridgeError(
            "pmd3_error",
            f"Failed to list devices: {exc}",
        ) from exc

    result: list[DeviceInfo] = []
    for dev in muxes:
        try:
            lc = await _lockdown(dev.serial)
            result.append(
                DeviceInfo(
                    udid=dev.serial,
                    name=lc.display_name,
                    product_type=lc.product_type,
                    os_version=lc.product_version,
                )
            )
        except BridgeError:
            # Include unpaired devices with placeholder fields so callers
            # can still see them.
            result.append(
                DeviceInfo(
                    udid=dev.serial,
                    name="unknown",
                    product_type="unknown",
                    os_version="unknown",
                )
            )
    return result


async def list_apps(udid: str) -> list[AppInfo]:
    """Return installed apps for the device."""
    lc = await _lockdown(udid)
    try:
        from pymobiledevice3.services.installation_proxy import (
            InstallationProxyService,
        )
        # "Any" covers User + System in pmd3's installation-proxy API.
        svc = InstallationProxyService(lockdown=lc)
        apps_raw = await svc.get_apps(application_type="Any")
    except BridgeError:
        raise
    except Exception as exc:
        raise BridgeError(
            "pmd3_error",
            f"Failed to list apps on {udid}: {exc}",
        ) from exc

    result: list[AppInfo] = []
    for bundle_id, info in apps_raw.items():
        result.append(
            AppInfo(
                bundle_id=bundle_id,
                name=info.get("CFBundleDisplayName") or info.get("CFBundleName"),
                version=info.get("CFBundleShortVersionString"),
            )
        )
    return result


async def launch_app(udid: str, bundle_id: str) -> int:
    """Launch an app and return its PID."""
    lc = await _lockdown(udid)
    try:
        from pymobiledevice3.services.dvt.dvt_secure_socket_proxy import (
            DvtSecureSocketProxyService,
        )
        from pymobiledevice3.services.dvt.instruments.process_control import (
            ProcessControl,
        )
        async with DvtSecureSocketProxyService(lockdown=lc) as dvt:
            pc = ProcessControl(dvt)
            pid = await pc.launch(bundle_id=bundle_id)
            return pid
    except BridgeError:
        raise
    except Exception as exc:
        msg = str(exc).lower()
        if "not installed" in msg or "could not find" in msg:
            raise BridgeError(
                "bundle_not_installed",
                f"Bundle {bundle_id} not installed on {udid}",
            ) from exc
        raise BridgeError(
            "pmd3_error",
            f"Failed to launch {bundle_id} on {udid}: {exc}",
        ) from exc


async def kill_app(udid: str, bundle_id: str) -> None:
    """Kill any running instance of the app with the given bundle id."""
    lc = await _lockdown(udid)
    try:
        from pymobiledevice3.services.dvt.dvt_secure_socket_proxy import (
            DvtSecureSocketProxyService,
        )
        from pymobiledevice3.services.dvt.instruments.device_info import (
            DeviceInfo as DvtDeviceInfo,
        )
        from pymobiledevice3.services.dvt.instruments.process_control import (
            ProcessControl,
        )
        async with DvtSecureSocketProxyService(lockdown=lc) as dvt:
            di = DvtDeviceInfo(dvt)
            processes = await di.proclist()
            target_pid: Optional[int] = None
            for proc in processes:
                if proc.get("bundleIdentifier") == bundle_id:
                    target_pid = proc.get("pid")
                    break
            if target_pid is not None:
                pc = ProcessControl(dvt)
                await pc.kill(target_pid)
    except BridgeError:
        raise
    except Exception as exc:
        raise BridgeError(
            "pmd3_error",
            f"Failed to kill {bundle_id} on {udid}: {exc}",
        ) from exc


async def pid_for_bundle(udid: str, bundle_id: str) -> Optional[int]:
    """Return PID for a running bundle, or None if not running."""
    lc = await _lockdown(udid)
    try:
        from pymobiledevice3.services.dvt.dvt_secure_socket_proxy import (
            DvtSecureSocketProxyService,
        )
        from pymobiledevice3.services.dvt.instruments.device_info import (
            DeviceInfo as DvtDeviceInfo,
        )
        async with DvtSecureSocketProxyService(lockdown=lc) as dvt:
            di = DvtDeviceInfo(dvt)
            processes = await di.proclist()
            for proc in processes:
                if proc.get("bundleIdentifier") == bundle_id:
                    pid = proc.get("pid")
                    return int(pid) if pid is not None else None
            return None
    except Exception as exc:
        raise BridgeError(
            "pmd3_error",
            f"Failed to query process list on {udid}: {exc}",
        ) from exc


async def battery(udid: str) -> BatteryResponse:
    """Return battery level and charging state."""
    lc = await _lockdown(udid)
    try:
        from pymobiledevice3.services.diagnostics import DiagnosticsService
        svc = DiagnosticsService(lockdown=lc)
        info = await svc.get_battery()
    except BridgeError:
        raise
    except Exception as exc:
        raise BridgeError(
            "pmd3_error",
            f"Failed to read battery on {udid}: {exc}",
        ) from exc

    # pmd3 returns CurrentCapacity/MaxCapacity as 0..100 integers, and
    # IsCharging/ExternalConnected as bools. Level is normalised to 0..1.
    current = info.get("CurrentCapacity")
    maximum = info.get("MaxCapacity") or 100
    level: Optional[float] = None
    if current is not None and maximum:
        level = float(current) / float(maximum)
    charging_raw = info.get("IsCharging")
    if charging_raw is None:
        charging_raw = info.get("ExternalConnected")
    return BatteryResponse(
        level=level,
        charging=bool(charging_raw) if charging_raw is not None else None,
    )


async def screenshot(udid: str) -> str:
    """Take a screenshot; return base64-encoded PNG bytes."""
    lc = await _lockdown(udid)
    try:
        from pymobiledevice3.services.screenshot import ScreenshotService
        svc = ScreenshotService(lockdown=lc)
        png_bytes = await svc.take_screenshot()
    except BridgeError:
        raise
    except Exception as exc:
        raise BridgeError(
            "pmd3_error",
            f"Failed to take screenshot on {udid}: {exc}",
        ) from exc
    return base64.b64encode(png_bytes).decode()


# ── Crash reports ─────────────────────────────────────────────────────────────

# pmd3's crash-report filenames are of the form
# `<process>-<timestamp>-<pid>.ips` for `.ips` files, and similar shapes for
# the few other extensions. The timestamp is formatted as
# `YYYY-MM-DD-HHMMSS` (local device time, no timezone offset). This regex
# captures process + timestamp for the listing shape spyder expects.
_CRASH_NAME_RE = re.compile(
    r"^(?P<process>[^-]+)-(?P<ts>\d{4}-\d{2}-\d{2}-\d{6})(?:-\d+)?\.(?:ips|crash|synced|beta)$",
    re.IGNORECASE,
)


def _parse_crash_timestamp(ts: str) -> Optional[datetime]:
    try:
        return datetime.strptime(ts, "%Y-%m-%d-%H%M%S").replace(tzinfo=timezone.utc)
    except ValueError:
        return None


async def crash_reports_list(
    udid: str,
    since_iso8601: Optional[str] = None,
    process: Optional[str] = None,
) -> list[CrashReportEntry]:
    """List crash reports, optionally filtered by time and process."""
    lc = await _lockdown(udid)
    since_dt: Optional[datetime] = None
    if since_iso8601:
        try:
            since_dt = datetime.fromisoformat(since_iso8601)
            if since_dt.tzinfo is None:
                since_dt = since_dt.replace(tzinfo=timezone.utc)
        except ValueError as exc:
            raise BridgeError(
                "pmd3_error",
                f"Invalid since_iso8601: {since_iso8601!r}: {exc}",
            ) from exc

    try:
        from pymobiledevice3.services.crash_reports import CrashReportsManager
        mgr = CrashReportsManager(lockdown=lc)
        names = await mgr.ls(path="/", depth=1)
    except BridgeError:
        raise
    except Exception as exc:
        raise BridgeError(
            "pmd3_error",
            f"Failed to list crash reports on {udid}: {exc}",
        ) from exc

    result: list[CrashReportEntry] = []
    for name in names:
        match = _CRASH_NAME_RE.match(name)
        if not match:
            # Skip directories and anything that isn't a recognisable report
            # file — no metadata to surface for these.
            continue
        proc_name = match.group("process")
        if process and proc_name != process:
            continue
        ts_raw = match.group("ts")
        ts_dt = _parse_crash_timestamp(ts_raw)
        if since_dt is not None and ts_dt is not None and ts_dt < since_dt:
            continue
        result.append(
            CrashReportEntry(
                name=name,
                process=proc_name,
                timestamp=ts_dt.isoformat() if ts_dt else ts_raw,
            )
        )
    return result


async def crash_reports_pull(udid: str, name: str) -> str:
    """Pull a single crash report's content by filename."""
    if "/" in name or name.startswith("."):
        raise BridgeError(
            "pmd3_error",
            f"Invalid crash report name: {name!r}",
        )
    lc = await _lockdown(udid)
    try:
        from pymobiledevice3.services.crash_reports import CrashReportsManager
        mgr = CrashReportsManager(lockdown=lc)
        with tempfile.TemporaryDirectory(prefix="pmd3-crash-") as tmp:
            await mgr.pull(out=tmp, entry=name, progress_bar=False)
            path = os.path.join(tmp, name)
            if not os.path.exists(path):
                # pmd3 sometimes strips the filename down; fall back to the
                # first file in the tempdir.
                files = os.listdir(tmp)
                if not files:
                    raise BridgeError(
                        "pmd3_error",
                        f"Crash report {name} not found on {udid}",
                    )
                path = os.path.join(tmp, files[0])
            with open(path, "rb") as fh:
                raw = fh.read()
    except BridgeError:
        raise
    except Exception as exc:
        raise BridgeError(
            "pmd3_error",
            f"Failed to pull crash report {name} from {udid}: {exc}",
        ) from exc
    try:
        return raw.decode("utf-8")
    except UnicodeDecodeError:
        return raw.decode("utf-8", errors="replace")
