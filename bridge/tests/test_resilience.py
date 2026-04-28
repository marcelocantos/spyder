# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""Regression tests for 🎯T50 (bridge resilience policy).

Pins the high-value bulkhead invariants:

  1. Outer exception handler — any uncaught Exception in a route handler
     becomes a structured pmd3_error 500 with a correlation_id; never
     a transport-level crash.
  2. ``/v1/health`` — returns immediately with {ok, uptime_s} and touches
     no device state. Liveness checks never depend on a device path.
  3. ``get_tunneld_devices`` is bounded by an asyncio.timeout — a
     tunneld that accepts but never responds becomes
     BridgeError("tunneld_unavailable") rather than a hang.
"""
from __future__ import annotations

import asyncio
import time
import types
from unittest.mock import patch

import pytest
import pytest_asyncio
from httpx import ASGITransport, AsyncClient

from pmd3_bridge import services
from pmd3_bridge.app import _set_services, app
from pmd3_bridge.services import BridgeError


@pytest_asyncio.fixture
async def client():
    async with AsyncClient(transport=ASGITransport(app=app), base_url="http://test") as c:
        yield c


# ── /v1/health ────────────────────────────────────────────────────────────────


async def test_health_returns_200_immediately(client: AsyncClient) -> None:
    """🎯T50: /v1/health is a no-device-state liveness probe."""
    started = time.monotonic()
    r = await client.post("/v1/health", json={})
    elapsed_ms = int((time.monotonic() - started) * 1000)
    assert r.status_code == 200
    body = r.json()
    assert body.get("ok") is True
    assert "uptime_s" in body
    assert isinstance(body["uptime_s"], (int, float))
    # Generous bound — under load on CI, asgi+pydantic shouldn't take
    # more than ~500ms for a no-op endpoint.
    assert elapsed_ms < 500, f"/v1/health took {elapsed_ms}ms — should be < 500ms"


async def test_health_get_also_works(client: AsyncClient) -> None:
    """GET on /v1/health works too — ergonomics for `curl -s host/v1/health`
    smoke checks."""
    r = await client.get("/v1/health")
    assert r.status_code == 200
    assert r.json().get("ok") is True


async def test_health_is_independent_of_device_services(client: AsyncClient) -> None:
    """🎯T50: /v1/health must not call into the services module. Replace
    services with a fail-on-anything stub and verify /v1/health still
    works while /v1/list_devices fails as expected."""
    failing_svc = types.SimpleNamespace()

    async def _explode(*_args, **_kwargs):
        raise BridgeError("pmd3_error", "device path is broken")

    for name in (
        "list_devices", "list_apps", "launch_app", "kill_app", "pid_for_bundle",
        "battery", "screenshot", "crash_reports_list", "crash_reports_pull",
        "device_power_state", "syslog", "app_state",
    ):
        setattr(failing_svc, name, _explode)
    _set_services(failing_svc)
    try:
        # Health stays green even though every device endpoint is broken.
        h = await client.post("/v1/health", json={})
        assert h.status_code == 200
        assert h.json().get("ok") is True
        # Device endpoint surfaces the structured error as designed.
        d = await client.post("/v1/list_devices", json={})
        assert d.status_code == 500
        assert d.json().get("error") == "pmd3_error"
    finally:
        # Restore default fakes so other tests aren't affected.
        from pmd3_bridge.app import _services as _svc_mod  # type: ignore
        _set_services(_svc_mod)


# ── Outer exception handler ──────────────────────────────────────────────────


async def test_unhandled_exception_returns_structured_500(client: AsyncClient) -> None:
    """🎯T50: any uncaught Exception in a handler becomes a structured
    pmd3_error 500 with a correlation_id — never a transport-level crash
    that uvicorn would translate into an unstructured 500.
    """
    blowup_svc = types.SimpleNamespace()

    async def _list_devices():
        raise RuntimeError("synthetic unhandled exception")

    blowup_svc.list_devices = _list_devices
    # Stub out everything else with no-ops so the test fixture doesn't
    # misfire on unrelated routes.
    async def _noop(*_args, **_kwargs):
        return []
    for name in (
        "list_apps", "launch_app", "kill_app", "pid_for_bundle",
        "battery", "screenshot", "crash_reports_list", "crash_reports_pull",
        "device_power_state", "syslog", "app_state",
    ):
        setattr(blowup_svc, name, _noop)
    _set_services(blowup_svc)
    try:
        r = await client.post("/v1/list_devices", json={})
        assert r.status_code == 500
        body = r.json()
        assert body.get("error") == "pmd3_error"
        assert "RuntimeError" in body.get("message", "")
        assert "synthetic unhandled exception" in body.get("message", "")
        assert "correlation_id" in body
        assert len(body["correlation_id"]) >= 8
    finally:
        from pmd3_bridge.app import _services as _svc_mod  # type: ignore
        _set_services(_svc_mod)


# ── tunneld timeout ──────────────────────────────────────────────────────────


async def test_tunneld_hang_becomes_tunneld_unavailable() -> None:
    """🎯T50: get_tunneld_devices() is bounded by asyncio.timeout. A
    tunneld that accepts but never responds becomes a structured
    tunneld_unavailable error — the bridge handler does not hang.
    """
    async def _hang(*_args, **_kwargs):
        # Simulate a wedged tunneld: accept the call, never return.
        await asyncio.sleep(60)

    # Reduce the timeout to keep the test fast.
    with patch.object(services, "_TUNNELD_ATTEMPT_TIMEOUT_S", 0.1), \
         patch.object(services, "_TUNNELD_RETRY_DELAY_S", 0.01), \
         patch(
             "pymobiledevice3.tunneld.api.get_tunneld_devices",
             side_effect=_hang,
         ):
        started = time.monotonic()
        with pytest.raises(BridgeError) as exc_info:
            await services._tunneld_rsd_for("00000000-000000000000")
        elapsed = time.monotonic() - started

    assert exc_info.value.code == "tunneld_unavailable"
    # 3 attempts × (0.1s timeout + 0.01s retry delay) ≈ 0.33s; bound
    # generously to absorb scheduler jitter on CI.
    assert elapsed < 5.0, f"tunneld_rsd_for hung for {elapsed:.2f}s — timeout did not fire"
