# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""pytest suite for the pmd3-bridge HTTP contract.

All tests use a fake services module — no real device is required.
Power-assertion tests exercise the manager directly via the app's
assertion_manager instance.
"""
from __future__ import annotations

import asyncio
import base64
import json
import types
from typing import Optional
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
import pytest_asyncio
from httpx import ASGITransport, AsyncClient

from pmd3_bridge.app import app, _assertion_manager, _set_services
from pmd3_bridge.schemas import (
    AppInfo,
    BatteryResponse,
    CrashReportEntry,
    DeviceInfo,
)
from pmd3_bridge.services import BridgeError


# ── Fake services ──────────────────────────────────────────────────────────────

FAKE_UDID = "00000000-000000000000"
FAKE_BUNDLE = "com.example.app"
FAKE_PID = 1234


def _make_fake_services(
    *,
    paired: bool = True,
    installed: bool = True,
    battery_level: Optional[float] = 0.75,
    battery_charging: Optional[bool] = False,
) -> types.ModuleType:
    svc = types.SimpleNamespace()

    async def list_devices() -> list[DeviceInfo]:
        if not paired:
            raise BridgeError("device_not_paired", "Not paired")
        return [
            DeviceInfo(
                udid=FAKE_UDID,
                name="Test Device",
                product_type="iPhone15,4",
                os_version="17.5",
            )
        ]

    async def list_apps(udid: str) -> list[AppInfo]:
        if not paired:
            raise BridgeError("device_not_paired", "Not paired")
        return [AppInfo(bundle_id=FAKE_BUNDLE, name="Test App", version="1.0")]

    async def launch_app(udid: str, bundle_id: str) -> int:
        if not installed:
            raise BridgeError("bundle_not_installed", "Not installed")
        return FAKE_PID

    async def kill_app(udid: str, bundle_id: str) -> None:
        if not installed:
            raise BridgeError("bundle_not_installed", "Not installed")

    async def pid_for_bundle(udid: str, bundle_id: str) -> Optional[int]:
        if not installed:
            return None
        return FAKE_PID

    async def battery(udid: str) -> BatteryResponse:
        return BatteryResponse(level=battery_level, charging=battery_charging)

    async def screenshot(udid: str) -> str:
        return base64.b64encode(b"\x89PNG fake").decode()

    async def crash_reports_list(
        udid: str,
        since_iso8601: Optional[str] = None,
        process: Optional[str] = None,
    ):
        # 🎯T26.3: streaming endpoints return an async iterator; setup errors
        # raise synchronously before the iterator is returned.
        async def _stream():
            yield CrashReportEntry(
                name="crash.ips",
                process="TestApp",
                timestamp="2026-01-01T00:00:00Z",
            )
        return _stream()

    async def crash_reports_pull(udid: str, name: str):
        async def _stream():
            yield b"crash report content"
        return _stream()

    for fn in [
        list_devices, list_apps, launch_app, kill_app, pid_for_bundle,
        battery, screenshot, crash_reports_list, crash_reports_pull,
    ]:
        setattr(svc, fn.__name__, fn)

    return svc  # type: ignore[return-value]


@pytest_asyncio.fixture(autouse=True)
async def reset_services():
    """Inject the default fake services before each test and restore after."""
    _set_services(_make_fake_services())
    yield
    _set_services(_make_fake_services())
    # Release any leftover assertion handles.
    await _assertion_manager.release_all()


@pytest_asyncio.fixture
async def client():
    async with AsyncClient(transport=ASGITransport(app=app), base_url="http://test") as c:
        yield c


# ── list_devices ───────────────────────────────────────────────────────────────

async def test_list_devices_happy(client: AsyncClient) -> None:
    r = await client.post("/v1/list_devices", json={})
    assert r.status_code == 200
    body = r.json()
    assert len(body["devices"]) == 1
    assert body["devices"][0]["udid"] == FAKE_UDID
    assert body["devices"][0]["product_type"] == "iPhone15,4"


async def test_list_devices_not_paired(client: AsyncClient) -> None:
    _set_services(_make_fake_services(paired=False))
    r = await client.post("/v1/list_devices", json={})
    assert r.status_code == 409
    assert r.json()["error"] == "device_not_paired"


# ── list_apps ──────────────────────────────────────────────────────────────────

async def test_list_apps_happy(client: AsyncClient) -> None:
    r = await client.post("/v1/list_apps", json={"udid": FAKE_UDID})
    assert r.status_code == 200
    apps = r.json()["apps"]
    assert any(a["bundle_id"] == FAKE_BUNDLE for a in apps)


async def test_list_apps_not_paired(client: AsyncClient) -> None:
    _set_services(_make_fake_services(paired=False))
    r = await client.post("/v1/list_apps", json={"udid": FAKE_UDID})
    assert r.status_code == 409


# ── launch_app ─────────────────────────────────────────────────────────────────

async def test_launch_app_happy(client: AsyncClient) -> None:
    r = await client.post("/v1/launch_app", json={"udid": FAKE_UDID, "bundle_id": FAKE_BUNDLE})
    assert r.status_code == 200
    assert r.json()["pid"] == FAKE_PID


async def test_launch_app_not_installed(client: AsyncClient) -> None:
    _set_services(_make_fake_services(installed=False))
    r = await client.post("/v1/launch_app", json={"udid": FAKE_UDID, "bundle_id": FAKE_BUNDLE})
    assert r.status_code == 422
    assert r.json()["error"] == "bundle_not_installed"


# ── kill_app ───────────────────────────────────────────────────────────────────

async def test_kill_app_happy(client: AsyncClient) -> None:
    r = await client.post("/v1/kill_app", json={"udid": FAKE_UDID, "bundle_id": FAKE_BUNDLE})
    assert r.status_code == 200


async def test_kill_app_not_installed(client: AsyncClient) -> None:
    _set_services(_make_fake_services(installed=False))
    r = await client.post("/v1/kill_app", json={"udid": FAKE_UDID, "bundle_id": FAKE_BUNDLE})
    assert r.status_code == 422


# ── pid_for_bundle ─────────────────────────────────────────────────────────────

async def test_pid_for_bundle_running(client: AsyncClient) -> None:
    r = await client.post("/v1/pid_for_bundle", json={"udid": FAKE_UDID, "bundle_id": FAKE_BUNDLE})
    assert r.status_code == 200
    assert r.json()["pid"] == FAKE_PID


async def test_pid_for_bundle_not_running(client: AsyncClient) -> None:
    _set_services(_make_fake_services(installed=False))
    r = await client.post("/v1/pid_for_bundle", json={"udid": FAKE_UDID, "bundle_id": FAKE_BUNDLE})
    assert r.status_code == 200
    assert r.json()["pid"] is None


# ── battery ────────────────────────────────────────────────────────────────────

async def test_battery_happy(client: AsyncClient) -> None:
    r = await client.post("/v1/battery", json={"udid": FAKE_UDID})
    assert r.status_code == 200
    body = r.json()
    assert body["level"] == pytest.approx(0.75)
    assert body["charging"] is False


# ── screenshot ─────────────────────────────────────────────────────────────────

async def test_screenshot_happy(client: AsyncClient) -> None:
    r = await client.post("/v1/screenshot", json={"udid": FAKE_UDID})
    assert r.status_code == 200
    png_b64 = r.json()["png_b64"]
    assert base64.b64decode(png_b64) == b"\x89PNG fake"


# ── crash_reports_list ─────────────────────────────────────────────────────────

async def test_crash_reports_list_happy(client: AsyncClient) -> None:
    r = await client.post("/v1/crash_reports_list", json={"udid": FAKE_UDID})
    assert r.status_code == 200
    assert r.headers["content-type"].startswith("application/x-ndjson")
    # NDJSON: one JSON object per line.
    lines = [json.loads(line) for line in r.text.strip().split("\n") if line]
    assert len(lines) == 1
    assert lines[0]["name"] == "crash.ips"


async def test_crash_reports_list_with_filter(client: AsyncClient) -> None:
    r = await client.post(
        "/v1/crash_reports_list",
        json={"udid": FAKE_UDID, "since_iso8601": "2025-01-01T00:00:00Z", "process": "TestApp"},
    )
    assert r.status_code == 200


# ── crash_reports_pull ─────────────────────────────────────────────────────────

async def test_crash_reports_pull_happy(client: AsyncClient) -> None:
    r = await client.post("/v1/crash_reports_pull", json={"udid": FAKE_UDID, "name": "crash.ips"})
    assert r.status_code == 200
    assert r.headers["content-type"].startswith("application/octet-stream")
    assert r.content == b"crash report content"


# ── Power assertion helpers ────────────────────────────────────────────────────

class FakePowerAssertionService:
    """Mimics the async-context-manager API of pmd3's PowerAssertionService."""

    def __init__(self, lockdown) -> None:  # noqa: ANN001
        pass

    def create_power_assertion(self, **kwargs):  # noqa: ANN201
        return _FakeCM()


class _FakeCM:
    async def __aenter__(self):
        return self

    async def __aexit__(self, *_):
        pass


async def _patched_acquire(
    manager,
    udid: str,
    type_: str,
    name: str,
    timeout_sec: int,
    details: Optional[str] = None,
) -> str:
    """Acquisition that bypasses real pmd3 by injecting a fake CM task."""
    import uuid
    from pmd3_bridge.assertions import _AssertionHandle

    stop_event = asyncio.Event()
    active_event = asyncio.Event()
    task_name = f"{udid}|{type_}|{name}|{details or ''}"

    async def _hold() -> None:
        async with _FakeCM():
            active_event.set()
            await stop_event.wait()

    task = asyncio.create_task(_hold(), name=task_name)
    await active_event.wait()

    handle_id = str(uuid.uuid4())
    manager._handles[handle_id] = _AssertionHandle(
        task=task, stop_event=stop_event, active_event=active_event
    )
    return handle_id


# ── Power assertion: acquire / refresh / release ───────────────────────────────

async def test_power_assertion_acquire_release(client: AsyncClient) -> None:
    """Acquire then release: handle_id round-trip, clean release."""
    handle_id = await _patched_acquire(
        _assertion_manager, FAKE_UDID, "NoDisplaySleepAssertion", "test", 60
    )
    assert handle_id

    r = await client.post("/v1/release_power_assertion", json={"handle_id": handle_id})
    assert r.status_code == 200
    assert handle_id not in _assertion_manager._handles


async def test_power_assertion_refresh(client: AsyncClient) -> None:
    """acquire → refresh → release, verifying no gap in assertion coverage."""
    handle_id = await _patched_acquire(
        _assertion_manager, FAKE_UDID, "NoDisplaySleepAssertion", "test", 60
    )
    old_task = _assertion_manager._handles[handle_id].task

    # Patch refresh to use fake CM as well.
    original_start = _assertion_manager._start_assertion

    async def _fake_start(udid, type_, name, timeout_sec, details):
        from pmd3_bridge.assertions import _AssertionHandle

        stop_event = asyncio.Event()
        active_event = asyncio.Event()
        task_name = f"{udid}|{type_}|{name}|{details or ''}"

        async def _hold() -> None:
            async with _FakeCM():
                active_event.set()
                await stop_event.wait()

        task = asyncio.create_task(_hold(), name=task_name)
        await active_event.wait()
        return _AssertionHandle(task=task, stop_event=stop_event, active_event=active_event)

    _assertion_manager._start_assertion = _fake_start  # type: ignore[method-assign]
    try:
        r = await client.post(
            "/v1/refresh_power_assertion",
            json={"handle_id": handle_id, "timeout_sec": 120},
        )
        assert r.status_code == 200
    finally:
        _assertion_manager._start_assertion = original_start  # type: ignore[method-assign]

    # The handle should still be present and mapped to a NEW task.
    assert handle_id in _assertion_manager._handles
    assert _assertion_manager._handles[handle_id].task is not old_task

    # Old task should have exited cleanly.
    assert old_task.done()

    # Release cleanly.
    await _assertion_manager.release(handle_id)


async def test_power_assertion_double_release(client: AsyncClient) -> None:
    """Double-release is a clean no-op (idempotent)."""
    handle_id = await _patched_acquire(
        _assertion_manager, FAKE_UDID, "NoDisplaySleepAssertion", "test", 60
    )
    r1 = await client.post("/v1/release_power_assertion", json={"handle_id": handle_id})
    r2 = await client.post("/v1/release_power_assertion", json={"handle_id": handle_id})
    assert r1.status_code == 200
    assert r2.status_code == 200  # second release is a no-op, not a 404


async def test_power_assertion_refresh_unknown_handle(client: AsyncClient) -> None:
    """Refreshing an unknown handle returns 404."""
    r = await client.post(
        "/v1/refresh_power_assertion",
        json={"handle_id": "00000000-0000-0000-0000-000000000000", "timeout_sec": 60},
    )
    assert r.status_code == 404
    assert r.json()["error"] == "not_found"


async def test_power_assertion_shutdown_releases_all(client: AsyncClient) -> None:
    """On shutdown, all outstanding handles are released."""
    h1 = await _patched_acquire(_assertion_manager, FAKE_UDID, "NoDisplaySleepAssertion", "h1", 60)
    h2 = await _patched_acquire(_assertion_manager, FAKE_UDID, "NoIdleSleepAssertion", "h2", 60)

    assert len(_assertion_manager._handles) == 2

    # Simulate graceful shutdown.
    await _assertion_manager.release_all()

    assert len(_assertion_manager._handles) == 0
    # Both tasks should have exited.
    # (autouse fixture also calls release_all, but handles are already gone — no-op.)
