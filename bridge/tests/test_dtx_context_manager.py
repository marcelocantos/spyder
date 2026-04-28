# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""Regression tests for 🎯T48: DTX-based pmd3 services
(``ProcessControl``, ``DvtDeviceInfo``) must be entered as async context
managers before use.

Both classes are themselves ``DtxService`` subclasses — they each need a
``connect()`` (or equivalent ``async with``) before their ``.service``
property is reachable. Constructing them as plain objects and calling a
method directly raises::

    RuntimeError: not connected — use `async with` or await connect()

That regression shipped in spyder 0.24.0 for ``launch_app`` /
``kill_app`` / ``pid_for_bundle`` and broke every iOS 17+ launch and
kill on Wi-Fi-only devices. The Screenshot/Notifications handlers
already wrapped their DTX services in ``async with``; these tests pin
the same contract for the rest.
"""
from __future__ import annotations

from contextlib import asynccontextmanager
from unittest.mock import patch

import pytest

from pmd3_bridge import services

FAKE_UDID = "00000000-000000000000"
FAKE_BUNDLE = "com.example.app"
FAKE_PID = 4242


class _FakeRSD:
    async def close(self) -> None:
        pass


@asynccontextmanager
async def _fake_provider_ctx(udid: str):
    yield _FakeRSD()


class _FakeDvtProvider:
    """Stand-in for pmd3's DvtProvider — async-context-manager only."""

    def __init__(self, lockdown):
        self.lockdown = lockdown

    async def __aenter__(self):
        return self

    async def __aexit__(self, *_):
        return None


class _RequiresConnect:
    """Mimics pmd3's DtxService base: ``self.service`` is gated behind
    ``__aenter__`` (or an explicit ``connect()``). Constructing without
    entering and calling a method raises the same RuntimeError pmd3 emits.
    """

    def __init__(self, dvt):
        self._dvt = dvt
        self._connected = False

    async def __aenter__(self):
        self._connected = True
        return self

    async def __aexit__(self, *_):
        self._connected = False
        return None

    def _check(self) -> None:
        if not self._connected:
            raise RuntimeError("not connected — use `async with` or await connect()")


class _FakeProcessControl(_RequiresConnect):
    async def launch(self, bundle_id: str, **_kwargs) -> int:
        self._check()
        return FAKE_PID

    async def kill(self, pid: int) -> None:
        self._check()


class _FakeDvtDeviceInfo(_RequiresConnect):
    def __init__(self, dvt, processes=None):
        super().__init__(dvt)
        self._processes = processes or []

    async def proclist(self):
        self._check()
        return self._processes


def _patch_dvt(*, processes=None):
    """Stack of patches that replaces the lazily-imported pmd3 DTX classes
    with the ``_RequiresConnect`` fakes."""
    proc_list = processes or []

    def _make_proclist(dvt):
        return _FakeDvtDeviceInfo(dvt, processes=proc_list)

    return [
        patch.object(services, "_service_provider_ctx", _fake_provider_ctx),
        patch(
            "pymobiledevice3.services.dvt.instruments.dvt_provider.DvtProvider",
            _FakeDvtProvider,
        ),
        patch(
            "pymobiledevice3.services.dvt.instruments.process_control.ProcessControl",
            _FakeProcessControl,
        ),
        patch(
            "pymobiledevice3.services.dvt.instruments.device_info.DeviceInfo",
            _make_proclist,
        ),
    ]


# ── launch_app ────────────────────────────────────────────────────────────────


async def test_launch_app_enters_process_control_context() -> None:
    """🎯T48: launch_app must enter ProcessControl as a context manager.

    Without the fix, ProcessControl(dvt).launch() raises RuntimeError
    ("not connected") because ProcessControl is itself a DtxService that
    needs its own connect()/__aenter__.
    """
    patches = _patch_dvt()
    for p in patches:
        p.start()
    try:
        pid = await services.launch_app(FAKE_UDID, FAKE_BUNDLE)
    finally:
        for p in patches:
            p.stop()
    assert pid == FAKE_PID


# ── kill_app ──────────────────────────────────────────────────────────────────


async def test_kill_app_enters_dvt_device_info_and_process_control_contexts() -> None:
    """🎯T48: kill_app must enter both DvtDeviceInfo and ProcessControl.

    DvtDeviceInfo.proclist() is required to find the target PID, and
    ProcessControl.kill() then needs its own connected DTX. Both fail
    with "not connected" if constructed without an `async with`.
    """
    target_pid = 9999
    procs = [{"bundleIdentifier": FAKE_BUNDLE, "pid": target_pid}]
    patches = _patch_dvt(processes=procs)
    for p in patches:
        p.start()
    try:
        result = await services.kill_app(FAKE_UDID, FAKE_BUNDLE)
    finally:
        for p in patches:
            p.stop()
    assert result is None  # kill_app returns None on success


async def test_kill_app_skips_process_control_when_target_not_running() -> None:
    """When the target bundle is not in the proclist, kill_app must not
    attempt to kill anything — DvtDeviceInfo entry is sufficient."""
    procs = [{"bundleIdentifier": "com.unrelated.app", "pid": 1}]
    patches = _patch_dvt(processes=procs)
    for p in patches:
        p.start()
    try:
        result = await services.kill_app(FAKE_UDID, FAKE_BUNDLE)
    finally:
        for p in patches:
            p.stop()
    assert result is None


# ── pid_for_bundle ────────────────────────────────────────────────────────────


async def test_pid_for_bundle_enters_dvt_device_info_context() -> None:
    """🎯T48: pid_for_bundle must enter DvtDeviceInfo as a context manager."""
    target_pid = 1357
    procs = [{"bundleIdentifier": FAKE_BUNDLE, "pid": target_pid}]
    patches = _patch_dvt(processes=procs)
    for p in patches:
        p.start()
    try:
        pid = await services.pid_for_bundle(FAKE_UDID, FAKE_BUNDLE)
    finally:
        for p in patches:
            p.stop()
    assert pid == target_pid


async def test_pid_for_bundle_returns_none_when_bundle_not_running() -> None:
    procs = [{"bundleIdentifier": "com.unrelated.app", "pid": 1}]
    patches = _patch_dvt(processes=procs)
    for p in patches:
        p.start()
    try:
        pid = await services.pid_for_bundle(FAKE_UDID, FAKE_BUNDLE)
    finally:
        for p in patches:
            p.stop()
    assert pid is None
