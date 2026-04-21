# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""Thin wrappers around pymobiledevice3 services.

All functions raise BridgeError on known failure conditions.
Callers translate BridgeError into the appropriate HTTP response.
"""
from __future__ import annotations

import base64
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


def _lockdown(udid: str):  # type: ignore[return]
    """Open a lockdown client for the given UDID.

    Raises BridgeError if the device is not paired or not connected.
    """
    try:
        from pymobiledevice3.lockdown import create_using_usbmux
        return create_using_usbmux(serial=udid)
    except Exception as exc:
        msg = str(exc).lower()
        if "pairing" in msg or "pair" in msg or "trust" in msg:
            raise BridgeError("device_not_paired", f"Device {udid} is not paired: {exc}") from exc
        raise BridgeError("pmd3_error", f"Failed to connect to device {udid}: {exc}") from exc


def list_devices() -> list[DeviceInfo]:
    """Return a list of all connected devices visible to usbmux."""
    try:
        from pymobiledevice3.usbmux import select_devices_by_connection_type
        devices = select_devices_by_connection_type(connection_type="USB")
    except Exception as exc:
        raise BridgeError("pmd3_error", f"Failed to list devices: {exc}") from exc

    result: list[DeviceInfo] = []
    for dev in devices:
        try:
            lc = _lockdown(dev.serial)
            result.append(DeviceInfo(
                udid=dev.serial,
                name=lc.name,
                product_type=lc.product_type,
                os_version=lc.product_version,
            ))
        except BridgeError:
            # Include the device with minimal info if lockdown fails.
            result.append(DeviceInfo(
                udid=dev.serial,
                name="unknown",
                product_type="unknown",
                os_version="unknown",
            ))
    return result


def list_apps(udid: str) -> list[AppInfo]:
    """Return installed apps for the device."""
    lc = _lockdown(udid)
    try:
        from pymobiledevice3.services.installation_proxy import InstallationProxyService
        svc = InstallationProxyService(lockdown=lc)
        apps_raw = svc.get_apps(app_types=["User", "System"])
    except BridgeError:
        raise
    except Exception as exc:
        raise BridgeError("pmd3_error", f"Failed to list apps on {udid}: {exc}") from exc

    result: list[AppInfo] = []
    for bundle_id, info in apps_raw.items():
        result.append(AppInfo(
            bundle_id=bundle_id,
            name=info.get("CFBundleDisplayName") or info.get("CFBundleName"),
            version=info.get("CFBundleShortVersionString"),
        ))
    return result


def launch_app(udid: str, bundle_id: str) -> int:
    """Launch app and return its PID."""
    lc = _lockdown(udid)
    try:
        from pymobiledevice3.services.dvt.instruments.process_control import ProcessControl
        from pymobiledevice3.services.dvt.dvt_secure_socket_proxy import DvtSecureSocketProxyService
        dvt = DvtSecureSocketProxyService(lockdown=lc)
        pc = ProcessControl(dvt)
        pid = pc.launch(bundle_id=bundle_id)
        return pid
    except BridgeError:
        raise
    except Exception as exc:
        msg = str(exc).lower()
        if "not installed" in msg or "bundle" in msg:
            raise BridgeError("bundle_not_installed", f"Bundle {bundle_id} not installed on {udid}") from exc
        raise BridgeError("pmd3_error", f"Failed to launch {bundle_id} on {udid}: {exc}") from exc


def kill_app(udid: str, bundle_id: str) -> None:
    """Kill the running app with the given bundle ID."""
    lc = _lockdown(udid)
    try:
        from pymobiledevice3.services.dvt.instruments.process_control import ProcessControl
        from pymobiledevice3.services.dvt.dvt_secure_socket_proxy import DvtSecureSocketProxyService
        dvt = DvtSecureSocketProxyService(lockdown=lc)
        pc = ProcessControl(dvt)
        pid = _find_pid(lc, bundle_id)
        if pid is not None:
            pc.kill(pid)
    except BridgeError:
        raise
    except Exception as exc:
        raise BridgeError("pmd3_error", f"Failed to kill {bundle_id} on {udid}: {exc}") from exc


def pid_for_bundle(udid: str, bundle_id: str) -> Optional[int]:
    """Return PID for a running bundle, or None if not running."""
    lc = _lockdown(udid)
    return _find_pid(lc, bundle_id)


def _find_pid(lc, bundle_id: str) -> Optional[int]:  # type: ignore[no-untyped-def]
    try:
        from pymobiledevice3.services.dvt.instruments.device_info import DeviceInfo as DvtDeviceInfo
        from pymobiledevice3.services.dvt.dvt_secure_socket_proxy import DvtSecureSocketProxyService
        dvt = DvtSecureSocketProxyService(lockdown=lc)
        di = DvtDeviceInfo(dvt)
        processes = di.proclist()
        for proc in processes:
            if proc.get("bundleIdentifier") == bundle_id:
                return proc.get("pid")
        return None
    except Exception as exc:
        raise BridgeError("pmd3_error", f"Failed to query process list: {exc}") from exc


def battery(udid: str) -> BatteryResponse:
    """Return battery level and charging state."""
    lc = _lockdown(udid)
    try:
        from pymobiledevice3.services.diagnostics import DiagnosticsService
        svc = DiagnosticsService(lockdown=lc)
        info = svc.get_battery()
        level_raw = info.get("BatteryCurrentCapacity")
        charging_raw = info.get("ExternalConnected")
        return BatteryResponse(
            level=float(level_raw) if level_raw is not None else None,
            charging=bool(charging_raw) if charging_raw is not None else None,
        )
    except BridgeError:
        raise
    except Exception as exc:
        raise BridgeError("pmd3_error", f"Failed to read battery on {udid}: {exc}") from exc


def screenshot(udid: str) -> str:
    """Take a screenshot and return it as base64-encoded PNG."""
    lc = _lockdown(udid)
    try:
        from pymobiledevice3.services.screenshot import ScreenshotService
        svc = ScreenshotService(lockdown=lc)
        png_bytes = svc.take_screenshot()
        return base64.b64encode(png_bytes).decode()
    except BridgeError:
        raise
    except Exception as exc:
        raise BridgeError("pmd3_error", f"Failed to take screenshot on {udid}: {exc}") from exc


def crash_reports_list(
    udid: str,
    since_iso8601: Optional[str] = None,
    process: Optional[str] = None,
) -> list[CrashReportEntry]:
    """List crash reports, optionally filtered by time and process name."""
    lc = _lockdown(udid)
    try:
        from pymobiledevice3.services.crash_reports import CrashReportsManager
        mgr = CrashReportsManager(lockdown=lc)
        since_dt: Optional[datetime] = None
        if since_iso8601:
            since_dt = datetime.fromisoformat(since_iso8601)
            if since_dt.tzinfo is None:
                since_dt = since_dt.replace(tzinfo=timezone.utc)

        reports = mgr.ls(since=since_dt)
        result: list[CrashReportEntry] = []
        for r in reports:
            # r is typically a dict with name/process/timestamp keys.
            proc_name = r.get("process", "")
            if process and proc_name != process:
                continue
            ts = r.get("timestamp", "")
            if isinstance(ts, datetime):
                ts = ts.isoformat()
            result.append(CrashReportEntry(
                name=r.get("name", ""),
                process=proc_name,
                timestamp=str(ts),
            ))
        return result
    except BridgeError:
        raise
    except Exception as exc:
        raise BridgeError("pmd3_error", f"Failed to list crash reports on {udid}: {exc}") from exc


def crash_reports_pull(udid: str, name: str) -> str:
    """Pull the content of a named crash report."""
    lc = _lockdown(udid)
    try:
        from pymobiledevice3.services.crash_reports import CrashReportsManager
        mgr = CrashReportsManager(lockdown=lc)
        content = mgr.get(name)
        if isinstance(content, bytes):
            content = content.decode("utf-8", errors="replace")
        return content
    except BridgeError:
        raise
    except Exception as exc:
        raise BridgeError("pmd3_error", f"Failed to pull crash report {name} from {udid}: {exc}") from exc
