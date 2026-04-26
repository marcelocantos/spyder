# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for the _service_provider_ctx RSD-first logic (🎯T42).

These tests exercise the transport-selection logic in services.py without
touching a real device.  The tunneld / USBMux layers are replaced by
lightweight async stubs injected via patch().

Scenarios covered:
  1. RSD available     → provider is the RSD (tunneld path, happy path).
  2. tunneld_unavailable → USBMux fallback succeeds.
  3. device_not_paired  → USBMux fallback succeeds.
  4. tunneld_unavailable + USBMux failure → pmd3_error raised.
  5. device_not_paired  + USBMux failure → device_not_paired re-raised
     (tunneld error is more authoritative).
  6. USBMux pair/trust error → device_not_paired raised.
"""
from __future__ import annotations

import asyncio
from contextlib import asynccontextmanager
from unittest.mock import AsyncMock, patch

import pytest

from pmd3_bridge.services import BridgeError, _service_provider_ctx

FAKE_UDID = "00000000-000000000000"


# ── Fake RSD ──────────────────────────────────────────────────────────────────

class _FakeRSD:
    """Minimal stand-in for RemoteServiceDiscoveryService."""

    def __init__(self, udid: str = FAKE_UDID):
        self.udid = udid
        self.closed = False

    async def close(self) -> None:
        self.closed = True


# ── Fake lockdown client ──────────────────────────────────────────────────────

class _FakeLockdown:
    def __init__(self):
        self.closed = False

    async def close(self) -> None:
        self.closed = True


# ── Helpers ───────────────────────────────────────────────────────────────────

def _tunneld_ok(rsd: _FakeRSD):
    """Patch _tunneld_rsd_for to return *rsd* successfully."""
    async def _impl(udid):
        return rsd
    return patch("pmd3_bridge.services._tunneld_rsd_for", side_effect=_impl)


def _tunneld_fail(code: str, message: str = "boom"):
    """Patch _tunneld_rsd_for to raise BridgeError with *code*."""
    async def _impl(udid):
        raise BridgeError(code, message)
    return patch("pmd3_bridge.services._tunneld_rsd_for", side_effect=_impl)


def _usbmux_ok(lc: _FakeLockdown | None = None):
    """Patch create_using_usbmux to return a fake lockdown client."""
    if lc is None:
        lc = _FakeLockdown()
    async def _impl(serial):
        return lc
    return patch(
        "pymobiledevice3.lockdown.create_using_usbmux",
        side_effect=_impl,
    )


def _usbmux_fail(message: str = "usbmux connection failed"):
    """Patch create_using_usbmux to raise a generic exception."""
    async def _impl(serial):
        raise Exception(message)
    return patch(
        "pymobiledevice3.lockdown.create_using_usbmux",
        side_effect=_impl,
    )


def _usbmux_pair_fail():
    """Patch create_using_usbmux to raise a pair/trust exception."""
    async def _impl(serial):
        raise Exception("pair record missing or trust rejected")
    return patch(
        "pymobiledevice3.lockdown.create_using_usbmux",
        side_effect=_impl,
    )


# ── Scenario 1: RSD available ─────────────────────────────────────────────────

async def test_service_provider_uses_rsd_when_tunneld_available() -> None:
    """When tunneld is reachable, the provider is the RSD itself."""
    rsd = _FakeRSD()
    with _tunneld_ok(rsd):
        async with _service_provider_ctx(FAKE_UDID) as provider:
            assert provider is rsd
    # RSD must be closed after the context exits.
    assert rsd.closed


# ── Scenario 2: tunneld_unavailable → USBMux fallback ────────────────────────

async def test_service_provider_falls_back_on_tunneld_unavailable() -> None:
    """When tunneld is not running, USBMux lockdown is returned instead."""
    lc = _FakeLockdown()
    with _tunneld_fail("tunneld_unavailable"), _usbmux_ok(lc):
        async with _service_provider_ctx(FAKE_UDID) as provider:
            assert provider is lc
    assert lc.closed


# ── Scenario 3: device_not_paired (tunneld) → USBMux fallback ────────────────

async def test_service_provider_falls_back_on_tunneld_device_not_paired() -> None:
    """When the device isn't in tunneld's registry, USBMux is tried next."""
    lc = _FakeLockdown()
    with _tunneld_fail("device_not_paired"), _usbmux_ok(lc):
        async with _service_provider_ctx(FAKE_UDID) as provider:
            assert provider is lc
    assert lc.closed


# ── Scenario 4: tunneld_unavailable + USBMux failure → pmd3_error ────────────

async def test_service_provider_raises_pmd3_error_when_both_fail() -> None:
    """When tunneld is unavailable AND USBMux fails, pmd3_error is raised."""
    with _tunneld_fail("tunneld_unavailable"), _usbmux_fail("usbmux blew up"):
        with pytest.raises(BridgeError) as exc_info:
            async with _service_provider_ctx(FAKE_UDID) as _:
                pass
    assert exc_info.value.code == "pmd3_error"


# ── Scenario 5: device_not_paired (tunneld) + USBMux failure ─────────────────

async def test_service_provider_reraises_tunneld_not_paired_when_usbmux_also_fails() -> None:
    """When tunneld says 'not paired' and USBMux also fails, the tunneld
    device_not_paired error is re-raised — tunneld is the authoritative source."""
    with _tunneld_fail("device_not_paired", "UDID not in registry"), _usbmux_fail():
        with pytest.raises(BridgeError) as exc_info:
            async with _service_provider_ctx(FAKE_UDID) as _:
                pass
    assert exc_info.value.code == "device_not_paired"
    assert "UDID not in registry" in exc_info.value.message


# ── Scenario 6: USBMux pair/trust error → device_not_paired ──────────────────

async def test_service_provider_maps_usbmux_pair_error_to_device_not_paired() -> None:
    """USBMux pair/trust exceptions are mapped to device_not_paired."""
    with _tunneld_fail("tunneld_unavailable"), _usbmux_pair_fail():
        with pytest.raises(BridgeError) as exc_info:
            async with _service_provider_ctx(FAKE_UDID) as _:
                pass
    assert exc_info.value.code == "device_not_paired"
