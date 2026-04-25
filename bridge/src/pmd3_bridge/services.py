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

import asyncio
import base64
import logging
import os
import re
import tempfile
import time
from contextlib import asynccontextmanager
from datetime import datetime, timezone
from typing import AsyncIterator, Optional

from .schemas import (
    AppInfo,
    BatteryResponse,
    CrashReportEntry,
    DeviceInfo,
)

log = logging.getLogger("pmd3_bridge.services")


class BridgeError(Exception):
    """Raised when a pmd3 operation fails in a classifiable way."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code
        self.message = message


@asynccontextmanager
async def _lockdown_ctx(udid: str):
    """Open a lockdown client scoped to an ``async with`` block.

    The underlying pymobiledevice3 lockdown client holds a usbmux socket
    that must be explicitly closed, or it leaks. Without the close,
    EMFILE (`Too many open files`) hits within minutes under autoawake's
    2 s device-poll cadence. Use this helper in every service function
    that needs a lockdown client.
    """
    lc = await _lockdown(udid)
    try:
        yield lc
    finally:
        try:
            await lc.close()
        except Exception:
            log.warning("lockdown close failed udid=%s", udid)


async def _lockdown(udid: str):  # type: ignore[return]
    """Open a lockdown client for the given UDID.

    Raises BridgeError if the device is not paired or not reachable.
    Prefer ``_lockdown_ctx`` in callers so the socket is closed on the
    error path too.
    """
    started = time.monotonic()
    try:
        from pymobiledevice3.lockdown import create_using_usbmux
        lc = await create_using_usbmux(serial=udid)
        log.debug("lockdown opened udid=%s elapsed_ms=%d",
                  udid, int((time.monotonic() - started) * 1000))
        return lc
    except Exception as exc:
        elapsed_ms = int((time.monotonic() - started) * 1000)
        msg = str(exc).lower()
        if "pair" in msg or "trust" in msg:
            log.warning("lockdown unpaired udid=%s elapsed_ms=%d err=%s",
                        udid, elapsed_ms, exc)
            raise BridgeError(
                "device_not_paired",
                f"Device {udid} is not paired: {exc}",
            ) from exc
        log.warning("lockdown failed udid=%s elapsed_ms=%d err=%s",
                    udid, elapsed_ms, exc)
        raise BridgeError(
            "pmd3_error",
            f"Failed to connect to device {udid}: {exc}",
        ) from exc


async def list_devices() -> list[DeviceInfo]:
    """Return every USB-connected device visible to usbmux.

    Every call opens (and explicitly closes) a lockdown client per device to
    read metadata. Without the close, pymobiledevice3 leaks the underlying
    usbmux sockets — observed in v0.7.0 as EMFILE ("Too many open files")
    after a few minutes of 2 s autoawake polling.
    """
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
            async with _lockdown_ctx(dev.serial) as lc:
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
    async with _lockdown_ctx(udid) as lc:
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
    async with _lockdown_ctx(udid) as lc:
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
    async with _lockdown_ctx(udid) as lc:
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
    async with _lockdown_ctx(udid) as lc:
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
    async with _lockdown_ctx(udid) as lc:
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
    """Take a screenshot; return base64-encoded PNG bytes.

    Routes through pmd3's DVT-based ``Screenshot`` instrument over a
    tunneld-mediated RemoteServiceDiscovery (RSD) connection — the iOS
    17+ path. The legacy ``com.apple.mobile.screenshotr`` lockdown
    service was deprecated by Apple in iOS 17 and returns
    InvalidServiceError on every modern device; this implementation
    replaces it (🎯T30).

    Requires an externally-managed ``pymobiledevice3 remote tunneld``
    process — when absent, raises ``tunneld_unavailable``. When tunneld
    is running but the device isn't in its registry, raises
    ``device_not_paired``.
    """
    started = time.monotonic()
    rsd = await _tunneld_rsd_for(udid)
    try:
        from pymobiledevice3.services.dvt.instruments.dvt_provider import DvtProvider
        from pymobiledevice3.services.dvt.instruments.screenshot import Screenshot
        try:
            async with DvtProvider(rsd) as dvt, Screenshot(dvt) as shot:
                png_bytes = await shot.get_screenshot()
        except BridgeError:
            raise
        except Exception as exc:
            raise BridgeError(
                "pmd3_error",
                f"Failed to take screenshot on {udid}: {exc}",
            ) from exc
    finally:
        try:
            await rsd.close()
        except Exception:
            log.warning("rsd close failed udid=%s", udid)
    log.info("screenshot captured udid=%s bytes=%d elapsed_ms=%d",
             udid, len(png_bytes), int((time.monotonic() - started) * 1000))
    return base64.b64encode(png_bytes).decode()


async def _tunneld_rsd_for(udid: str):  # type: ignore[no-untyped-def]
    """Return the tunneld-registered ``RemoteServiceDiscoveryService`` for
    the given UDID. Closes every other RSD tunneld registered before
    returning so the caller only owns one connection.

    Raises BridgeError("tunneld_unavailable") when tunneld is unreachable,
    BridgeError("device_not_paired") when the udid isn't in the registry.
    """
    try:
        from pymobiledevice3.tunneld.api import (
            TUNNELD_DEFAULT_ADDRESS,
            get_tunneld_devices,
        )
        rsds = await get_tunneld_devices(TUNNELD_DEFAULT_ADDRESS)
    except Exception as exc:
        raise BridgeError(
            "tunneld_unavailable",
            f"tunneld unreachable at {TUNNELD_DEFAULT_ADDRESS}: {exc}; "
            "start it with `sudo pymobiledevice3 remote tunneld`",
        ) from exc
    if not rsds:
        raise BridgeError(
            "tunneld_unavailable",
            "tunneld is running but has no devices registered; "
            "ensure paired iOS 17+ devices are connected and trusted",
        )
    target = next((r for r in rsds if r.udid == udid), None)
    if target is None:
        for r in rsds:
            try:
                await r.close()
            except Exception:
                pass
        raise BridgeError(
            "device_not_paired",
            f"device {udid} not in tunneld registry; "
            f"available udids: {[r.udid for r in rsds]}",
        )
    for r in rsds:
        if r is target:
            continue
        try:
            await r.close()
        except Exception:
            pass
    return target


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
) -> AsyncIterator[CrashReportEntry]:
    """List crash reports, optionally filtered by time and process.

    Returns an async iterator (🎯T26.3) so the bridge can hand entries to
    the HTTP response as they become available. Synchronous setup failures
    (invalid since_iso8601, unpaired device, pmd3 error fetching the index)
    raise BridgeError immediately — before any iterator is returned — so
    the HTTP layer can render them as 4xx without committing to a 200
    stream. Today pmd3's ``CrashReportsManager.ls()`` returns the directory
    listing atomically; a later refactor can drop to AFC directly for true
    per-entry device-level streaming.
    """
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

    started = time.monotonic()
    async with _lockdown_ctx(udid) as lc:
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
    log.info("crash_reports listed udid=%s names=%d elapsed_ms=%d",
             udid, len(names), int((time.monotonic() - started) * 1000))

    return _crash_reports_list_stream(names, since_dt, process)


async def _crash_reports_list_stream(
    names: list[str],
    since_dt: Optional[datetime],
    process: Optional[str],
) -> AsyncIterator[CrashReportEntry]:
    emitted = 0
    for name in names:
        match = _CRASH_NAME_RE.match(name)
        if not match:
            continue
        proc_name = match.group("process")
        if process and proc_name != process:
            continue
        ts_raw = match.group("ts")
        ts_dt = _parse_crash_timestamp(ts_raw)
        if since_dt is not None and ts_dt is not None and ts_dt < since_dt:
            continue
        yield CrashReportEntry(
            name=name,
            process=proc_name,
            timestamp=ts_dt.isoformat() if ts_dt else ts_raw,
        )
        emitted += 1
        # Yield to the event loop so the StreamingResponse can flush each
        # entry to the wire before we compute the next one.
        await asyncio.sleep(0)
    log.debug("crash_reports list emitted=%d filtered_from=%d", emitted, len(names))


# Chunk size for crash-report pulls. Matches AFC's native ~64KB block size.
_CRASH_PULL_CHUNK_BYTES = 64 * 1024


async def crash_reports_pull(udid: str, name: str) -> AsyncIterator[bytes]:
    """Pull a single crash report's content, streamed as octet-stream chunks.

    Synchronous setup (lockdown, pmd3 fetch-to-tempfile) happens before
    returning the iterator so failures surface as BridgeError → 4xx rather
    than as a 200 stream that ends mid-body. The returned iterator yields
    raw bytes in ``_CRASH_PULL_CHUNK_BYTES`` chunks.
    """
    if "/" in name or name.startswith("."):
        raise BridgeError(
            "pmd3_error",
            f"Invalid crash report name: {name!r}",
        )
    started = time.monotonic()

    # Pull the file to a tempdir synchronously so any device-side failure
    # raises BridgeError before we return an iterator. The tempdir lives
    # inside the iterator's closure so it is cleaned up when the iterator
    # completes (or is garbage-collected).
    tmp = tempfile.mkdtemp(prefix="pmd3-crash-")
    try:
        async with _lockdown_ctx(udid) as lc:
            from pymobiledevice3.services.crash_reports import CrashReportsManager
            mgr = CrashReportsManager(lockdown=lc)
            await mgr.pull(out=tmp, entry=name, progress_bar=False)
            path = os.path.join(tmp, name)
            if not os.path.exists(path):
                files = os.listdir(tmp)
                if not files:
                    raise BridgeError(
                        "pmd3_error",
                        f"Crash report {name} not found on {udid}",
                    )
                path = os.path.join(tmp, files[0])
    except BridgeError:
        _rmtree_quiet(tmp)
        raise
    except Exception as exc:
        _rmtree_quiet(tmp)
        raise BridgeError(
            "pmd3_error",
            f"Failed to pull crash report {name} from {udid}: {exc}",
        ) from exc

    return _crash_reports_pull_stream(udid, name, tmp, path, started)


async def _crash_reports_pull_stream(
    udid: str, name: str, tmp: str, path: str, started: float,
) -> AsyncIterator[bytes]:
    total = 0
    try:
        with open(path, "rb") as fh:
            while True:
                chunk = fh.read(_CRASH_PULL_CHUNK_BYTES)
                if not chunk:
                    break
                total += len(chunk)
                yield chunk
                await asyncio.sleep(0)
        log.info("crash_reports pulled udid=%s name=%s bytes=%d elapsed_ms=%d",
                 udid, name, total, int((time.monotonic() - started) * 1000))
    finally:
        _rmtree_quiet(tmp)


def _rmtree_quiet(path: str) -> None:
    """Best-effort removal of a directory tree; swallow errors."""
    import shutil
    try:
        shutil.rmtree(path, ignore_errors=True)
    except Exception:  # pragma: no cover
        pass
