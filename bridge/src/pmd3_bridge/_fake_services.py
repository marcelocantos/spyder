# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""Runtime-loadable fake services for no-device developer machines.

Activated by running the bridge with SPYDER_BRIDGE_FAKE_SERVICES=1. The
real ``services`` module is replaced at startup with this one; the rest
of the FastAPI app (auth middleware, streaming wrappers, HTTP framing)
runs unmodified. This lets Go integration tests exercise the real
``pmd3-bridge`` subprocess end-to-end on machines without a paired iOS
device — the only surviving fake under 🎯T26.4 and it exists because
pmd3 requires a device, not because we simulate a broken bridge.
"""
from __future__ import annotations

import asyncio
import base64
from typing import AsyncIterator, Optional

from .schemas import AppInfo, BatteryResponse, CrashReportEntry, DeviceInfo
from .services import BridgeError  # re-export for app classifier

FAKE_UDID = "00000000-FAKEFAKE0000"
FAKE_BUNDLE = "com.example.fake"


async def list_devices() -> list[DeviceInfo]:
    return [
        DeviceInfo(
            udid=FAKE_UDID,
            name="Fake Device",
            product_type="iPhone14,2",
            os_version="18.0",
        ),
    ]


async def list_apps(udid: str) -> list[AppInfo]:
    _check_udid(udid)
    return [AppInfo(bundle_id=FAKE_BUNDLE, name="Fake App", version="1.0")]


async def launch_app(udid: str, bundle_id: str) -> int:
    _check_udid(udid)
    if bundle_id != FAKE_BUNDLE:
        raise BridgeError("bundle_not_installed", f"{bundle_id} not installed")
    return 4242


async def kill_app(udid: str, bundle_id: str) -> None:
    _check_udid(udid)


async def pid_for_bundle(udid: str, bundle_id: str) -> Optional[int]:
    _check_udid(udid)
    if bundle_id == FAKE_BUNDLE:
        return 4242
    return None


async def battery(udid: str) -> BatteryResponse:
    _check_udid(udid)
    return BatteryResponse(level=0.77, charging=True)


# Fixed 12-byte PNG magic header — not a valid PNG but good enough for
# tests that only care about "bytes round-trip through base64".
_FAKE_PNG = b"\x89PNG\r\n\x1a\nfake"


async def screenshot(udid: str) -> str:
    _check_udid(udid)
    return base64.b64encode(_FAKE_PNG).decode()


async def crash_reports_list(
    udid: str,
    since_iso8601: Optional[str] = None,
    process: Optional[str] = None,
) -> AsyncIterator[CrashReportEntry]:
    _check_udid(udid)
    return _crash_list_stream()


async def _crash_list_stream() -> AsyncIterator[CrashReportEntry]:
    for i in (1, 2, 3):
        yield CrashReportEntry(
            name=f"FakeApp-2026-01-{i:02d}-120000.ips",
            process="FakeApp",
            timestamp=f"2026-01-{i:02d}T12:00:00Z",
        )
        await asyncio.sleep(0)


async def crash_reports_pull(udid: str, name: str) -> AsyncIterator[bytes]:
    _check_udid(udid)
    if "/" in name or name.startswith("."):
        raise BridgeError("pmd3_error", f"invalid name: {name!r}")
    return _crash_pull_stream(name)


async def _crash_pull_stream(name: str) -> AsyncIterator[bytes]:
    # Yield three chunks so streaming tests see real framing.
    for chunk in (b"chunk1:", f"name={name};".encode(), b"end"):
        yield chunk
        await asyncio.sleep(0)


def _check_udid(udid: str) -> None:
    if udid != FAKE_UDID:
        raise BridgeError("device_not_paired", f"Unknown udid {udid}")
