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

# Maximum attempts and inter-attempt pause for tunneld probes. These values
# are tuned for the startup-race window: the brew-service daemon may attempt
# its first screenshot a few seconds after tunneld finishes negotiating a
# new device tunnel. Three attempts at 0.5 s cover the observed ~1 s window
# without adding noticeable latency to the steady-state success path.
_TUNNELD_MAX_ATTEMPTS = 3
_TUNNELD_RETRY_DELAY_S = 0.5

from .schemas import (
    AppInfo,
    BatteryResponse,
    CrashReportEntry,
    DeviceInfo,
    DevicePowerStateResponse,
    SyslogEntry,
)

log = logging.getLogger("pmd3_bridge.services")


class BridgeError(Exception):
    """Raised when a pmd3 operation fails in a classifiable way."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code
        self.message = message


# ── Service-provider context manager (🎯T42) ─────────────────────────────────
#
# ``_service_provider_ctx`` replaces the old USBMux-only ``_lockdown_ctx``.
# It returns whatever pmd3 calls a ``LockdownServiceProvider`` — either an
# RSD from tunneld (iOS 17+ path) or a classic usbmux lockdown client
# (iOS <17 / tunneld-unavailable fallback).
#
# Why can we pass the RSD directly to every pmd3 service?
# ``RemoteServiceDiscoveryService`` is a subclass of
# ``LockdownServiceProvider``, which is the declared parameter type for all
# service constructors (DiagnosticsService, InstallationProxyService,
# CrashReportsManager, DvtProvider, etc.).  Service implementations that
# care about the connection type use ``isinstance(provider,
# RemoteServiceDiscoveryService)`` internally to select the right service
# name (e.g. ``RSD_SERVICE_NAME`` vs ``SERVICE_NAME``).  So passing an RSD
# directly is both type-safe and functionally correct.
#
# Error taxonomy:
#   tunneld_unavailable — tunneld process not running at all; we fall
#                         through to USBMux because this device may be iOS <17.
#   device_not_paired   — tunneld is running but doesn't know this UDID;
#                         we still try USBMux in case it's a freshly-attached
#                         iOS 17+ device that tunneld hasn't picked up yet.
#                         If USBMux also fails, we re-raise the original
#                         tunneld-side error (device_not_paired), because
#                         tunneld is the more authoritative source for
#                         iOS 17+ devices.

@asynccontextmanager
async def _service_provider_ctx(udid: str):
    """Yield a pmd3 LockdownServiceProvider for *udid*, then close it.

    Priority:
    1. tunneld RSD (iOS 17+ reliable path).
    2. USBMux lockdown (iOS <17 / tunneld-unavailable fallback).

    The caller should treat the yielded object as an opaque
    LockdownServiceProvider — it may be a RemoteServiceDiscoveryService
    (tunneld path) or a RemoteLockdownClient/LockdownClient (USBMux path).
    """
    rsd_exc: Optional[BridgeError] = None
    provider = None

    # 1) Try tunneld RSD.
    try:
        provider = await _tunneld_rsd_for(udid)
    except BridgeError as exc:
        if exc.code not in ("tunneld_unavailable", "device_not_paired"):
            raise  # unexpected error code — propagate immediately
        rsd_exc = exc
        log.debug(
            "service_provider RSD unavailable (code=%s), trying USBMux: %s",
            exc.code, exc.message,
        )

    if provider is not None:
        try:
            yield provider
        finally:
            try:
                await provider.close()
            except Exception:
                log.warning("rsd close failed udid=%s", udid)
        return

    # 2) Tunneld unavailable or device not yet in registry — fall through to USBMux.
    try:
        from pymobiledevice3.lockdown import create_using_usbmux
        started = time.monotonic()
        lc = await create_using_usbmux(serial=udid)
        log.debug(
            "service_provider USBMux fallback udid=%s elapsed_ms=%d",
            udid, int((time.monotonic() - started) * 1000),
        )
    except Exception as exc:
        elapsed_ms = int((time.monotonic() - started) * 1000)  # type: ignore[possibly-undefined]
        msg = str(exc).lower()
        if "pair" in msg or "trust" in msg:
            log.warning("lockdown unpaired udid=%s elapsed_ms=%d err=%r",
                        udid, elapsed_ms, exc)
            raise BridgeError(
                "device_not_paired",
                f"Device {udid} is not paired: {exc}",
            ) from exc
        log.warning("lockdown failed udid=%s elapsed_ms=%d err=%r",
                    udid, elapsed_ms, exc)
        # If tunneld was running but didn't know the device, that error is
        # more authoritative than a USBMux failure — re-raise it.
        if rsd_exc is not None and rsd_exc.code == "device_not_paired":
            raise rsd_exc
        raise BridgeError(
            "pmd3_error",
            f"Failed to connect to device {udid}: {exc}",
        ) from exc

    try:
        yield lc
    finally:
        try:
            await lc.close()
        except Exception:
            log.warning("lockdown close failed udid=%s", udid)


# Legacy aliases used in list_devices() for the USBMux-only enrichment path.
# list_devices() calls the old _lockdown / _lockdown_ctx directly because it
# has its own multi-source logic that predates the unified provider abstraction.

@asynccontextmanager
async def _lockdown_ctx(udid: str):
    """Open a USBMux lockdown client scoped to an ``async with`` block.

    Used internally by ``list_devices`` for the USBMux-only enrichment pass.
    All other callers should use ``_service_provider_ctx`` instead.
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
    """Open a USBMux lockdown client for the given UDID.

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
            log.warning("lockdown unpaired udid=%s elapsed_ms=%d err=%r",
                        udid, elapsed_ms, exc)
            raise BridgeError(
                "device_not_paired",
                f"Device {udid} is not paired: {exc}",
            ) from exc
        log.warning("lockdown failed udid=%s elapsed_ms=%d err=%r",
                    udid, elapsed_ms, exc)
        raise BridgeError(
            "pmd3_error",
            f"Failed to connect to device {udid}: {exc}",
        ) from exc


async def list_devices() -> list[DeviceInfo]:
    """Return every iOS device currently reachable from this host.

    Two sources are unioned, in this order:

      1. **Tunneld registry** (`pymobiledevice3.tunneld.api.get_tunneld_devices`).
         Authoritative for iOS 17+ devices — `pmd3 usbmux list` drops them
         randomly across runs (observed: Jevons missing one tick, Minicades
         missing the next), but tunneld's RSD registry has them stably as
         long as the user-managed `pymobiledevice3 remote tunneld` has
         negotiated a tunnel. Each RSD carries udid, product_type, and
         product_version directly — no extra lockdown round-trip needed.
      2. **USBMux** for any device tunneld didn't list. Covers iOS <17
         devices and any device tunneld hasn't tunneled yet. Per-device
         lockdown read enriches metadata; explicit close prevents the
         v0.7.0 EMFILE leak.

    The Go-side adapter further enriches with `xcrun devicectl list devices`
    to backfill the user-visible name (e.g. "Pippa", "Jevons") that neither
    tunneld nor lockdown surfaces directly.

    No tunneld is not an error — when the registry is unreachable we fall
    back to USBMux only (matching pre-T30 behaviour).
    """
    seen: dict[str, DeviceInfo] = {}

    # 1) tunneld registry — HTTP probe rather than full RSD-connect.
    # `get_tunneld_devices()` silently drops devices whose RSD-connect
    # times out (observed: Pippa intermittently missing from the RSD-
    # connect path despite being in the registry). The HTTP endpoint at
    # http://127.0.0.1:49151/ is the canonical "what's tunneled" view —
    # we read the UDID list from it directly and let the Go-side
    # `xcrun devicectl list devices` enrichment fill in metadata.
    #
    # Retry up to _TUNNELD_MAX_ATTEMPTS times on transient transport errors
    # (errno 65 EHOSTUNREACH on macOS loopback during wake-from-sleep or
    # tunneld restart). Falls back to USBMux-only on exhaustion. See
    # docs/papers/t36-tunneld-launchd-investigation.md.
    try:
        import requests
        from pymobiledevice3.tunneld.api import TUNNELD_DEFAULT_ADDRESS
        host, port = TUNNELD_DEFAULT_ADDRESS
        tunnels: dict = {}
        _last_probe_exc: Exception | None = None
        for _attempt in range(_TUNNELD_MAX_ATTEMPTS):
            if _attempt > 0:
                time.sleep(_TUNNELD_RETRY_DELAY_S)
            try:
                resp = requests.get(f"http://{host}:{port}", timeout=2.0)
                tunnels = resp.json()
                _last_probe_exc = None
                break
            except Exception as exc:
                _last_probe_exc = exc
                log.debug(
                    "tunneld HTTP probe attempt=%d failed: %s", _attempt, exc
                )
        if _last_probe_exc is not None:
            raise _last_probe_exc
        for udid in tunnels.keys():
            seen[udid] = DeviceInfo(
                udid=udid,
                # Metadata not on the HTTP shape; Go side backfills via
                # devicectl. Keep fields populated with placeholders so
                # downstream JSON consumers don't choke on missing keys.
                name="",
                product_type="",
                os_version="",
            )
    except Exception as exc:
        log.debug("tunneld registry unreachable; falling back to USBMux: %s", exc)

    # 2) USBMux fallback for devices tunneld didn't surface.
    try:
        from pymobiledevice3.usbmux import select_devices_by_connection_type
        muxes = await select_devices_by_connection_type(connection_type="USB")
    except Exception as exc:
        if not seen:
            raise BridgeError(
                "pmd3_error",
                f"Failed to list devices via tunneld and USBMux: {exc}",
            ) from exc
        log.debug("USBMux enumeration failed but tunneld populated; continuing: %s", exc)
        muxes = []

    for dev in muxes:
        if dev.serial in seen:
            continue
        try:
            async with _lockdown_ctx(dev.serial) as lc:
                seen[dev.serial] = DeviceInfo(
                    udid=dev.serial,
                    name=lc.display_name,
                    product_type=lc.product_type,
                    os_version=lc.product_version,
                )
        except BridgeError:
            seen[dev.serial] = DeviceInfo(
                udid=dev.serial,
                name="unknown",
                product_type="unknown",
                os_version="unknown",
            )

    # Enrich tunneld-only entries (those still with empty fields) via
    # lockdown. iOS 17+ devices that legacy lockdown can't read stay
    # empty — Go side backfills via devicectl.
    for udid, info in list(seen.items()):
        if info.name or info.product_type or info.os_version:
            continue
        try:
            async with _lockdown_ctx(udid) as lc:
                seen[udid] = DeviceInfo(
                    udid=udid,
                    name=lc.display_name or "",
                    product_type=lc.product_type or "",
                    os_version=lc.product_version or "",
                )
        except BridgeError:
            # Leave the empty-field entry; downstream enrichment fills it.
            pass

    return list(seen.values())


async def list_apps(udid: str) -> list[AppInfo]:
    """Return installed apps for the device."""
    async with _service_provider_ctx(udid) as provider:
        try:
            from pymobiledevice3.services.installation_proxy import (
                InstallationProxyService,
            )
            # "Any" covers User + System in pmd3's installation-proxy API.
            svc = InstallationProxyService(lockdown=provider)
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
    async with _service_provider_ctx(udid) as provider:
        try:
            from pymobiledevice3.services.dvt.instruments.dvt_provider import (
                DvtProvider,
            )
            from pymobiledevice3.services.dvt.instruments.process_control import (
                ProcessControl,
            )
            async with DvtProvider(provider) as dvt:
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
    async with _service_provider_ctx(udid) as provider:
        try:
            from pymobiledevice3.services.dvt.instruments.dvt_provider import (
                DvtProvider,
            )
            from pymobiledevice3.services.dvt.instruments.device_info import (
                DeviceInfo as DvtDeviceInfo,
            )
            from pymobiledevice3.services.dvt.instruments.process_control import (
                ProcessControl,
            )
            async with DvtProvider(provider) as dvt:
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
    async with _service_provider_ctx(udid) as provider:
        try:
            from pymobiledevice3.services.dvt.instruments.dvt_provider import (
                DvtProvider,
            )
            from pymobiledevice3.services.dvt.instruments.device_info import (
                DeviceInfo as DvtDeviceInfo,
            )
            async with DvtProvider(provider) as dvt:
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


async def app_state(udid: str, bundle_id: str) -> tuple[str, str]:
    """Query the lifecycle state of one app on a device.

    Subscribes to the DVT mobile-notifications service for ~0.5 s,
    drains the initial state-enumeration burst BackBoard emits on
    connection, finds the entry for ``bundle_id`` (matched against
    ``execName``'s bundle path), and disconnects.

    Returns ``(state, description)`` where state ∈ {"running",
    "backgrounded", "terminated"}.

      * BKS state_description == "Running"        → "running"
      * other recognised state_descriptions       → "backgrounded"
      * app not present in the enumeration burst  → "terminated"

    Note: BKS reports SpringBoard specially (not in the enumeration);
    a query for SpringBoard always returns "terminated". Also note
    that "Running" is the BKS ForegroundRunning state — it does not
    distinguish a foregrounded app from a background-running one
    (e.g. an audio-mode app), but for autoawake's purposes that's
    fine: KeepAwake never uses background modes, so its "Running"
    state is unambiguously foreground.
    """
    async with _service_provider_ctx(udid) as provider:
        try:
            from pymobiledevice3.services.dvt.instruments.dvt_provider import (
                DvtProvider,
            )
            from pymobiledevice3.services.dvt.instruments.notifications import (
                Notifications,
            )
            async with DvtProvider(provider) as dvt:
                async with Notifications(dvt) as notif:
                    # Silence the pmd3 channel close-callback that calls
                    # shutdown_queue on a stdlib asyncio.Queue, which lacks
                    # .shutdown() prior to Python 3.13. Our bridge runs on
                    # 3.11; without this monkey-patch every state probe
                    # logs a (cosmetic) ERROR with a TypeError traceback,
                    # which would flood the daemon log at the autoawake
                    # convergence cadence. We've already drained what we
                    # need by the time on_closed fires, so skipping it
                    # outright is safe.
                    notif.service.on_closed = lambda *_args, **_kwargs: None
                    # BackBoard dumps current state of every "managed" app
                    # on connect, then streams transitions. We drain the
                    # dump (events stop arriving when the burst ends) and
                    # ignore everything after.
                    found_state = ""
                    while True:
                        try:
                            ev = await asyncio.wait_for(
                                notif.service.events.get(), timeout=0.5
                            )
                        except asyncio.TimeoutError:
                            break
                        sel, args = ev
                        if sel != "applicationStateNotification:":
                            continue
                        payload = args[0] if args and isinstance(args[0], list) else [args[0]] if args else []
                        for entry in payload:
                            exec_name = entry.get("execName", "") or ""
                            # execName looks like
                            # "/private/var/containers/Bundle/Application/<UUID>/<App>.app"
                            # or "/Applications/<App>.app". Match by the
                            # ".app" path segment against the App-installed
                            # bundle's known structure.
                            # Match the .app directory name against the
                            # bundle id's last segment. iOS bundle ids
                            # produced from Xcode templates use the target
                            # name as the last segment AND the .app folder
                            # name, so this is reliable for any third-party
                            # app — including KeepAwake. Case-insensitive
                            # to tolerate Apple system apps where casing
                            # diverges (e.g. com.apple.camera → Camera.app).
                            wanted = f"/{bundle_id.split('.')[-1]}.app"
                            if wanted.lower() in exec_name.lower():
                                found_state = entry.get("state_description", "") or ""
        except BridgeError:
            raise
        except Exception as exc:
            raise BridgeError(
                "pmd3_error",
                f"Failed to query app state on {udid}: {exc}",
            ) from exc

    if not found_state:
        return ("terminated", "")
    if found_state.lower() == "running":
        return ("running", found_state)
    return ("backgrounded", found_state)


async def battery(udid: str) -> BatteryResponse:
    """Return battery level and charging state."""
    async with _service_provider_ctx(udid) as provider:
        try:
            from pymobiledevice3.services.diagnostics import DiagnosticsService
            svc = DiagnosticsService(lockdown=provider)
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
        from pymobiledevice3.exceptions import (
            DeveloperModeError,
            DeveloperModeIsNotEnabledError,
        )
        from pymobiledevice3.services.dvt.instruments.dvt_provider import DvtProvider
        from pymobiledevice3.services.dvt.instruments.screenshot import Screenshot
        try:
            async with DvtProvider(rsd) as dvt, Screenshot(dvt) as shot:
                png_bytes = await shot.get_screenshot()
        except BridgeError:
            raise
        except (DeveloperModeIsNotEnabledError, DeveloperModeError) as exc:
            raise BridgeError(
                "developer_mode_disabled",
                f"Developer Mode is not enabled on {udid}: enable at "
                f"Settings → Privacy & Security → Developer Mode (device will reboot)",
            ) from exc
        except Exception as exc:
            # Pattern-match on the message too — pmd3 sometimes raises a
            # generic Exception when DDI services need DeveloperMode but
            # the failure surfaces from a deeper layer.
            if "developer mode" in str(exc).lower():
                raise BridgeError(
                    "developer_mode_disabled",
                    f"Developer Mode is not enabled on {udid}: enable at "
                    f"Settings → Privacy & Security → Developer Mode "
                    f"(device will reboot). Underlying error: {exc}",
                ) from exc
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

    Retries up to _TUNNELD_MAX_ATTEMPTS times on transient transport errors
    (errno 65 EHOSTUNREACH, ConnectionError, OSError). This covers the
    startup-race window where the brew-service daemon launches and attempts
    a screenshot a few seconds before tunneld finishes negotiating a tunnel
    or recovers from a brief restart. See docs/papers/t36-tunneld-launchd-
    investigation.md for the full root-cause analysis.
    """
    from pymobiledevice3.tunneld.api import (
        TUNNELD_DEFAULT_ADDRESS,
        get_tunneld_devices,
    )
    last_exc: Exception | None = None
    for attempt in range(_TUNNELD_MAX_ATTEMPTS):
        if attempt > 0:
            await asyncio.sleep(_TUNNELD_RETRY_DELAY_S)
            log.debug("tunneld_rsd_for: retry attempt=%d udid=%s", attempt, udid)
        try:
            rsds = await get_tunneld_devices(TUNNELD_DEFAULT_ADDRESS)
            break  # success — fall through to UDID search
        except Exception as exc:
            last_exc = exc
            log.debug(
                "tunneld_rsd_for: attempt=%d failed: %s", attempt, exc
            )
    else:
        # All attempts exhausted.
        raise BridgeError(
            "tunneld_unavailable",
            f"tunneld unreachable at {TUNNELD_DEFAULT_ADDRESS}: {last_exc}; "
            "start it with `sudo pymobiledevice3 remote tunneld`",
        ) from last_exc
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


# ── Device power state (🎯T29) ────────────────────────────────────────────────

# Pixel-black threshold for the framebuffer-blank heuristic (candidate #5
# in the T29 design doc). If the first _PIXEL_SAMPLE_BYTES of the raw PNG
# data are entirely zero after stripping the PNG header, we classify as
# "display_off". In practice a real-content screenshot will have varied
# pixel values; an all-black framebuffer will be near-zero throughout.
#
# We sample the raw PNG bytes (not decoded pixels) so we don't need an
# image decoding dependency. PNG DEFLATE-compressed all-black data is
# highly repetitive and produces byte sequences that are almost all
# zeros after decompression — but even at the compressed level the
# sequence has very low entropy. The heuristic is conservative: we
# require > 95% zero-valued bytes in the first 4 KB of the PNG body
# (after the 8-byte PNG magic) to call it "display_off".
_PIXEL_SAMPLE_BYTES = 4096
_BLACK_THRESHOLD = 0.95

# Exception message fragments that indicate the device display/framebuffer
# is off or the device is asleep. Derived from observed pmd3 behaviour on
# locked/sleeping devices. Updated as HIL testing reveals new shapes.
_ASLEEP_PATTERNS = (
    "display off",
    "device is sleeping",
    "screen is off",
    "lockdown failed",
    "unable to retrieve screenshot",
    "device not connected",
    "remotexpc connection interrupted",
    "connection closed",
)


async def device_power_state(udid: str) -> DevicePowerStateResponse:
    """Query the power/display state of a device without resetting its idle timer.

    Implementation (🎯T29, candidate #1 — ScreenshotService behaviour):
    attempts a DVT screenshot via tunneld RSD. The framebuffer read is a
    GPU/display-driver operation that does NOT write to IOPMrootDomain
    user-activity registers, satisfying the non-observation requirement.

    State mapping:
      "awake"       — screenshot succeeded and pixel content is non-trivial.
      "display_off" — screenshot succeeded but image is all-black (framebuffer blank).
      "asleep"      — screenshot failed with a pattern indicating display/device off.
      "unknown"     — prerequisite missing (tunneld down, developer mode off)
                      or unrecognised error; cannot determine state.

    See docs/papers/t29-device-state-detection.md for the full candidate
    analysis and the HIL verification protocol.
    """
    started = time.monotonic()
    try:
        png_b64 = await screenshot(udid)
    except BridgeError as exc:
        elapsed_ms = int((time.monotonic() - started) * 1000)
        detail = exc.message

        # Prerequisites missing — cannot determine state.
        if exc.code in ("tunneld_unavailable",):
            log.info("device_power_state udid=%s state=unknown (tunneld) elapsed_ms=%d",
                     udid, elapsed_ms)
            return DevicePowerStateResponse(state="unknown", detail=detail)

        if exc.code == "developer_mode_disabled":
            log.info("device_power_state udid=%s state=unknown (devmode) elapsed_ms=%d",
                     udid, elapsed_ms)
            return DevicePowerStateResponse(state="unknown", detail=detail)

        # pmd3_error with a pattern matching sleep/display-off messages.
        if exc.code == "pmd3_error":
            lower = exc.message.lower()
            for pattern in _ASLEEP_PATTERNS:
                if pattern in lower:
                    log.info("device_power_state udid=%s state=asleep (pmd3 pattern %r) elapsed_ms=%d",
                             udid, pattern, elapsed_ms)
                    return DevicePowerStateResponse(state="asleep", detail=detail)
            # pmd3_error with unrecognised message — conservative fallback.
            log.info("device_power_state udid=%s state=unknown (pmd3_error unrecognised) elapsed_ms=%d",
                     udid, elapsed_ms)
            return DevicePowerStateResponse(state="unknown", detail=detail)

        # device_not_paired or other known bridge codes.
        log.info("device_power_state udid=%s state=unknown (bridge code=%s) elapsed_ms=%d",
                 udid, exc.code, elapsed_ms)
        return DevicePowerStateResponse(state="unknown", detail=detail)

    # Screenshot succeeded — check for all-black framebuffer.
    import base64 as _base64
    elapsed_ms = int((time.monotonic() - started) * 1000)
    try:
        raw = _base64.b64decode(png_b64)
    except Exception:
        # Corrupt base64 — treat as unknown rather than crashing.
        log.warning("device_power_state udid=%s state=unknown (b64 decode failed) elapsed_ms=%d",
                    udid, elapsed_ms)
        return DevicePowerStateResponse(state="unknown",
                                        detail="base64 decode of screenshot failed")

    # Skip the 8-byte PNG magic and sample the first _PIXEL_SAMPLE_BYTES.
    sample = raw[8:8 + _PIXEL_SAMPLE_BYTES]
    if sample:
        zero_fraction = sample.count(0) / len(sample)
        if zero_fraction >= _BLACK_THRESHOLD:
            log.info("device_power_state udid=%s state=display_off (%.0f%% zeros) elapsed_ms=%d",
                     udid, zero_fraction * 100, elapsed_ms)
            return DevicePowerStateResponse(
                state="display_off",
                detail=f"screenshot all-black ({zero_fraction * 100:.0f}% zero bytes in first {len(sample)} bytes)",
            )

    log.info("device_power_state udid=%s state=awake elapsed_ms=%d bytes=%d",
             udid, elapsed_ms, len(raw))
    return DevicePowerStateResponse(state="awake")


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
    async with _service_provider_ctx(udid) as provider:
        try:
            from pymobiledevice3.services.crash_reports import CrashReportsManager
            mgr = CrashReportsManager(lockdown=provider)
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
        async with _service_provider_ctx(udid) as provider:
            from pymobiledevice3.services.crash_reports import CrashReportsManager
            mgr = CrashReportsManager(lockdown=provider)
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


# ── Syslog (🎯T46) ────────────────────────────────────────────────────────────
#
# Wraps pmd3's OsTraceService.syslog generator, applying optional process_name
# and subsystem filters server-side and emitting structured SyslogEntry records.
# The endpoint streams forever until the client closes the connection (or
# context cancellation propagates from the Uvicorn worker).
#
# pid/process_name semantics: pmd3 supports server-side pid filtering only
# (via the StartActivity request). process_name is matched client-side here
# against image_name; subsystem is matched against label.subsystem.

async def syslog(
    udid: str,
    pid: int = -1,
    process_name: Optional[str] = None,
    subsystem: Optional[str] = None,
) -> AsyncIterator[SyslogEntry]:
    """Stream syslog entries from the device, optionally filtered.

    Synchronous setup (provider open, OsTraceService connect, StartActivity
    request) happens before the async generator yields its first entry, so
    failures surface as BridgeError → 4xx rather than as a 200 stream that
    ends mid-body.
    """
    # Eagerly open the provider + service so connect failures raise before we
    # commit to a streaming response.
    provider_cm = _service_provider_ctx(udid)
    provider = await provider_cm.__aenter__()
    try:
        from pymobiledevice3.services.os_trace import OsTraceService
        svc = OsTraceService(lockdown=provider)
        # syslog() awaits connect() internally before yielding.
        gen = svc.syslog(pid=pid)
    except BridgeError:
        await provider_cm.__aexit__(None, None, None)
        raise
    except Exception as exc:
        await provider_cm.__aexit__(None, None, None)
        raise BridgeError(
            "pmd3_error",
            f"Failed to open syslog stream on {udid}: {exc}",
        ) from exc

    return _syslog_stream(provider_cm, svc, gen, process_name, subsystem)


async def _syslog_stream(
    provider_cm,
    svc,
    gen,
    process_name: Optional[str],
    subsystem: Optional[str],
) -> AsyncIterator[SyslogEntry]:
    started = time.monotonic()
    emitted = 0
    try:
        async for entry in gen:
            if process_name and entry.image_name != process_name:
                continue
            if subsystem:
                if entry.label is None or entry.label.subsystem != subsystem:
                    continue
            yield SyslogEntry(
                pid=entry.pid,
                timestamp=entry.timestamp.isoformat(),
                level=entry.level.name,
                process=entry.image_name,
                subsystem=(entry.label.subsystem if entry.label else ""),
                category=(entry.label.category if entry.label else ""),
                message=entry.message,
            )
            emitted += 1
    finally:
        try:
            await svc.close()
        except Exception:
            log.debug("syslog svc close failed", exc_info=True)
        try:
            await provider_cm.__aexit__(None, None, None)
        except Exception:
            log.debug("syslog provider close failed", exc_info=True)
        log.info(
            "syslog stream closed emitted=%d elapsed_ms=%d",
            emitted, int((time.monotonic() - started) * 1000),
        )
