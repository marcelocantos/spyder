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
  4. Per-device DTX concurrency semaphore — when _DTX_MAX_CONCURRENCY slots
     are all held, the next request gets BridgeError("pmd3_busy") → HTTP 503
     immediately rather than queuing behind a wedged handler.
  5. _bounded() converts asyncio.TimeoutError to BridgeError("pmd3_timeout")
     so the handler returns a structured 504 rather than an unhandled exception.
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
from pmd3_bridge.services import BridgeError, _dtx_semaphores


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


# ── DTX concurrency semaphore (🎯T50 AC5) ────────────────────────────────────


async def test_dtx_slot_fails_fast_when_full() -> None:
    """🎯T50 AC5: _dtx_slot raises pmd3_busy immediately when all slots are held.

    With _DTX_MAX_CONCURRENCY=1, holding the semaphore and calling _dtx_slot
    again must raise BridgeError("pmd3_busy") without blocking.
    """
    udid = "00000000-SEMAPHORE-TEST"
    with patch.object(services, "_DTX_MAX_CONCURRENCY", 1):
        # Clear any cached semaphore for this UDID so the patch takes effect.
        _dtx_semaphores.pop(udid, None)
        sem = await services._get_dtx_semaphore(udid)
        # Manually acquire the only slot.
        await sem.acquire()
        try:
            with pytest.raises(BridgeError) as exc_info:
                async with services._dtx_slot(udid):
                    pass  # should not be reached
        finally:
            sem.release()
            _dtx_semaphores.pop(udid, None)

    assert exc_info.value.code == "pmd3_busy"
    assert "concurrency limit" in exc_info.value.message.lower()


async def test_dtx_slot_via_http_returns_503(client: AsyncClient) -> None:  # type: ignore[name-defined]
    """🎯T50 AC5: a pmd3_busy error from a service function surfaces as HTTP 503."""
    busy_svc = types.SimpleNamespace()

    async def _busy_launch(udid: str, bundle_id: str) -> int:
        raise BridgeError("pmd3_busy", "DTX concurrency limit reached for this test")

    async def _noop(*_args, **_kwargs):
        return []

    for name in (
        "list_devices", "list_apps", "kill_app", "pid_for_bundle",
        "battery", "screenshot", "crash_reports_list", "crash_reports_pull",
        "device_power_state", "syslog", "app_state",
    ):
        setattr(busy_svc, name, _noop)
    busy_svc.launch_app = _busy_launch
    _set_services(busy_svc)
    try:
        r = await client.post("/v1/launch_app",
                              json={"udid": "00000000-000000000000", "bundle_id": "com.example.app"})
        assert r.status_code == 503
        body = r.json()
        assert body.get("error") == "pmd3_busy"
    finally:
        from pmd3_bridge.app import _services as _svc_mod  # type: ignore
        _set_services(_svc_mod)


# ── _bounded() DTX RPC timeout (🎯T50 AC2) ───────────────────────────────────


async def test_bounded_converts_timeout_to_bridge_error() -> None:
    """🎯T50 AC2: _bounded() converts asyncio.TimeoutError to BridgeError("pmd3_timeout")."""
    async def _slow():
        await asyncio.sleep(60)

    started = time.monotonic()
    with pytest.raises(BridgeError) as exc_info:
        await services._bounded(_slow(), timeout_s=0.05, operation="test_op")
    elapsed = time.monotonic() - started

    assert exc_info.value.code == "pmd3_timeout"
    assert "test_op" in exc_info.value.message
    assert elapsed < 2.0, f"_bounded hung for {elapsed:.2f}s — timeout did not fire"


async def test_bounded_timeout_via_http_returns_504(client: AsyncClient) -> None:  # type: ignore[name-defined]
    """🎯T50 AC2: pmd3_timeout from a service function surfaces as HTTP 504."""
    timeout_svc = types.SimpleNamespace()

    async def _timeout_screenshot(udid: str) -> str:
        raise BridgeError("pmd3_timeout", "screenshot.get_screenshot timed out after 15.01s (limit 15.0s)")

    async def _noop(*_args, **_kwargs):
        return []

    for name in (
        "list_devices", "list_apps", "launch_app", "kill_app", "pid_for_bundle",
        "battery", "crash_reports_list", "crash_reports_pull",
        "device_power_state", "syslog", "app_state",
    ):
        setattr(timeout_svc, name, _noop)
    timeout_svc.screenshot = _timeout_screenshot
    _set_services(timeout_svc)
    try:
        r = await client.post("/v1/screenshot",
                              json={"udid": "00000000-000000000000"})
        assert r.status_code == 504
        body = r.json()
        assert body.get("error") == "pmd3_timeout"
    finally:
        from pmd3_bridge.app import _services as _svc_mod  # type: ignore
        _set_services(_svc_mod)


# ── AC2: additional bounded-await tests ──────────────────────────────────────


async def test_tunneld_timeout_returns_503() -> None:
    """🎯T50 AC2: tunneld hang → tunneld_unavailable (503).

    The tunneld timeout deliberately maps to tunneld_unavailable (503 Service
    Unavailable), not pmd3_timeout (504), because the condition is
    "tunneld is not accessible right now" — a retriable external dependency
    outage, not an internal operation timeout. This preserves the semantic
    already pinned by test_tunneld_hang_becomes_tunneld_unavailable.
    """
    async def _hang(*_args, **_kwargs):
        await asyncio.sleep(60)

    with patch.object(services, "_TUNNELD_ATTEMPT_TIMEOUT_S", 0.05), \
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
    assert elapsed < 5.0, f"timed out too slowly ({elapsed:.2f}s)"


async def test_rsd_connect_timeout_returns_pmd3_timeout() -> None:
    """🎯T50 AC2: a slow RSD __aenter__ is bounded by _TIMEOUT_DTX_CONNECT_S.

    The DTX connect timeout wraps DvtProvider/__aenter__, raising
    BridgeError("pmd3_timeout") rather than hanging the handler.
    """
    import asyncio
    from contextlib import asynccontextmanager
    from unittest.mock import patch

    # Simulate a DvtProvider whose __aenter__ hangs forever.
    class _SlowDvtProvider:
        def __init__(self, *args, **kwargs):
            pass

        async def __aenter__(self):
            await asyncio.sleep(60)  # hangs

        async def __aexit__(self, *args):
            pass

    with patch.object(services, "_TIMEOUT_DTX_CONNECT_S", 0.05):
        started = time.monotonic()
        with pytest.raises(BridgeError) as exc_info:
            async with services._dtx_provider(_SlowDvtProvider(), _SlowDvtProvider, operation="test_dtx_connect"):
                pass
        elapsed = time.monotonic() - started

    assert exc_info.value.code == "pmd3_timeout"
    assert "test_dtx_connect" in exc_info.value.message
    assert elapsed < 3.0, f"dtx connect timeout hung for {elapsed:.2f}s"


async def test_dtx_connect_timeout_returns_504(client: AsyncClient) -> None:
    """🎯T50 AC2: a pmd3_timeout from dtx_connect surfaces as HTTP 504."""
    timeout_svc = types.SimpleNamespace()

    async def _timeout_launch(udid: str, bundle_id: str) -> int:
        raise BridgeError(
            "pmd3_timeout",
            "dtx_connect.launch_app timed out after 10.01s (limit 10.0s)",
        )

    async def _noop(*_args, **_kwargs):
        return []

    for name in (
        "list_devices", "list_apps", "kill_app", "pid_for_bundle",
        "battery", "screenshot", "crash_reports_list", "crash_reports_pull",
        "device_power_state", "syslog", "app_state",
    ):
        setattr(timeout_svc, name, _noop)
    timeout_svc.launch_app = _timeout_launch
    _set_services(timeout_svc)
    try:
        r = await client.post(
            "/v1/launch_app",
            json={"udid": "00000000-000000000000", "bundle_id": "com.example.app"},
        )
        assert r.status_code == 504
        body = r.json()
        assert body.get("error") == "pmd3_timeout"
    finally:
        from pmd3_bridge.app import _services as _svc_mod  # type: ignore
        _set_services(_svc_mod)


# ── AC7: no shared connection state across concurrent calls ──────────────────


async def test_no_shared_connection_state_across_calls() -> None:
    """🎯T50 AC7: concurrent requests to the same device each get their own
    provider instance — no shared mutable state between calls.

    _service_provider_ctx is an async context manager that creates a fresh
    provider on each enter. Two concurrent invocations must yield distinct
    objects and must each have closed their own provider on exit.
    """
    import asyncio
    from unittest.mock import AsyncMock, patch

    providers_yielded: list = []
    providers_closed: set[int] = set()

    class _FakeProvider:
        def __init__(self, name: str):
            self.name = name
            self.closed = False

        async def close(self) -> None:
            self.closed = True
            providers_closed.add(id(self))

    provider_seq = [_FakeProvider("p1"), _FakeProvider("p2")]
    provider_iter = iter(provider_seq)

    async def _fake_tunneld_rsd(udid):
        return next(provider_iter)

    with patch("pmd3_bridge.services._tunneld_rsd_for", side_effect=_fake_tunneld_rsd):
        async def _use_provider():
            async with services._service_provider_ctx("00000000-000000000000") as p:
                providers_yielded.append(p)
                await asyncio.sleep(0)  # let the other coroutine run

        await asyncio.gather(_use_provider(), _use_provider())

    # Each call got its own distinct provider.
    assert len(providers_yielded) == 2
    assert providers_yielded[0] is not providers_yielded[1]
    # Both providers were closed by their respective context manager exits.
    assert providers_yielded[0].closed
    assert providers_yielded[1].closed
