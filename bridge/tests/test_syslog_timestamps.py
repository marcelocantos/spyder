# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""Regression test for 🎯T49: bridge syslog endpoint must emit
timezone-aware timestamps (RFC3339Nano with offset).

pmd3's OsTraceService produces naive datetimes (no tzinfo) via
``datetime.fromtimestamp``. A bare ``.isoformat()`` then emits a
timezone-less string like ``2026-04-28T17:30:00.123456`` which Go's
``time.Parse(time.RFC3339Nano, …)`` rejects — the resulting zero-time
fails the LogRange since/until filter on the Go side and the user sees
``logs returns []`` for any window.

The bridge fix is to call ``.astimezone()`` (no argument) on naive
timestamps before ``.isoformat()``: that promotes to the system local
timezone while preserving the absolute instant, and the resulting ISO
string carries an explicit offset that RFC3339Nano accepts.
"""
from __future__ import annotations

import asyncio
import re
from contextlib import asynccontextmanager
from dataclasses import dataclass
from datetime import datetime
from types import SimpleNamespace
from typing import AsyncIterator, Optional
from unittest.mock import patch

import pytest

from pmd3_bridge import services


# RFC3339Nano with explicit offset (no Z form here: pmd3 datetimes are
# local-tz naive, astimezone() promotes to local tz, isoformat emits the
# offset).
_RFC3339_NANO_OFFSET = re.compile(
    r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?[+-]\d{2}:\d{2}$"
)
_RFC3339_NANO_Z = re.compile(
    r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$"
)


@dataclass
class _Label:
    subsystem: str = "com.example"
    category: str = "ui"


@dataclass
class _PmdSyslogEntry:
    """Stand-in for pmd3's SyslogEntry. Fields cover what services.syslog
    reads: pid, timestamp, level (with .name), image_name, label, message."""

    pid: int
    timestamp: datetime
    level: SimpleNamespace
    image_name: str
    message: str
    label: Optional[_Label]


async def _make_naive_entry() -> AsyncIterator[_PmdSyslogEntry]:
    # datetime.fromtimestamp() yields a naive datetime — exactly the shape
    # pmd3's OsTraceService produces.
    naive = datetime.fromtimestamp(1714290000.5)
    assert naive.tzinfo is None, "fixture must reproduce the naive shape"
    yield _PmdSyslogEntry(
        pid=42,
        timestamp=naive,
        level=SimpleNamespace(name="INFO"),
        image_name="MyApp",
        message="hello",
        label=_Label(),
    )


@asynccontextmanager
async def _fake_provider_ctx(udid: str):
    yield object()


async def test_syslog_emits_timezone_aware_iso_timestamp() -> None:
    """🎯T49: every emitted SyslogEntry.timestamp must include a timezone
    offset so the Go side's RFC3339Nano parser accepts it.

    Without the bridge's astimezone() promotion, isoformat() produces
    "2026-04-28T17:30:00.123456" (no offset) and the regex below fails.
    """
    # Patch services._service_provider_ctx so syslog() doesn't try to
    # talk to a real device.
    with patch.object(services, "_service_provider_ctx", _fake_provider_ctx):
        # Patch OsTraceService inside the service function so its
        # `svc.syslog(pid=...)` returns our naive-timestamp generator.
        class _FakeOsTraceService:
            def __init__(self, lockdown):
                self.lockdown = lockdown

            def syslog(self, pid: int):
                return _make_naive_entry()

            async def close(self):
                pass

        with patch(
            "pymobiledevice3.services.os_trace.OsTraceService",
            _FakeOsTraceService,
        ):
            stream = await services.syslog("00000000-000000000000")
            entries = []
            async for e in stream:
                entries.append(e)

    assert entries, "expected at least one entry from the fixture"
    ts = entries[0].timestamp
    assert (
        _RFC3339_NANO_OFFSET.match(ts) or _RFC3339_NANO_Z.match(ts)
    ), (
        f"timestamp {ts!r} is missing a timezone offset — Go's "
        "RFC3339Nano parser will reject it and LogRange will return []"
    )
