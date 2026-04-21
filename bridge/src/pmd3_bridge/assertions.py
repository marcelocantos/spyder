# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""PowerAssertionManager — hold pmd3 power assertions across HTTP calls.

pymobiledevice3's PowerAssertionService.create_power_assertion() is an
async context manager: the assertion lives only while inside the block.
We model long-lived assertions as asyncio Tasks that enter the context
manager and then block on a stop Event.

Refresh strategy (no idle gap):
  1. Acquire a NEW assertion task (new_handle → new_task).
  2. Only after the new task signals it is ACTIVE (holding the assertion),
     set the stop event on the OLD task and await it.
  3. Remap the caller's original handle_id to the new_task.

This guarantees the device is never left without an assertion during refresh.
"""
from __future__ import annotations

import asyncio
import logging
import uuid
from dataclasses import dataclass, field
from typing import Optional

log = logging.getLogger(__name__)


@dataclass
class _AssertionHandle:
    task: asyncio.Task
    stop_event: asyncio.Event
    active_event: asyncio.Event  # set when the assertion is live


class PowerAssertionManager:
    def __init__(self) -> None:
        self._handles: dict[str, _AssertionHandle] = {}

    async def acquire(
        self,
        udid: str,
        type_: str,
        name: str,
        timeout_sec: int,
        details: Optional[str] = None,
    ) -> str:
        """Acquire a new power assertion.  Returns an opaque handle_id."""
        handle_id = str(uuid.uuid4())
        handle = await self._start_assertion(udid, type_, name, timeout_sec, details)
        self._handles[handle_id] = handle
        return handle_id

    async def refresh(self, handle_id: str, timeout_sec: int) -> None:
        """Extend the assertion.

        Acquires a new assertion first (gap-free), then tears down the old one.
        """
        old = self._handles.get(handle_id)
        if old is None:
            raise KeyError(handle_id)

        # Retrieve the original assertion parameters from the old task's name.
        # We encode them there at task-creation time.
        params = old.task.get_name()  # "<udid>|<type_>|<name>|<details>"
        udid, type_, name, details_raw = params.split("|", 3)
        details: Optional[str] = details_raw if details_raw else None

        # Step 1: start the replacement assertion and wait until it is live.
        new_handle = await self._start_assertion(udid, type_, name, timeout_sec, details)

        # Step 2: now that the new assertion is live, tear down the old one.
        old.stop_event.set()
        try:
            await asyncio.wait_for(old.task, timeout=10)
        except (asyncio.TimeoutError, asyncio.CancelledError):
            log.warning("Old assertion task did not stop cleanly for %s", handle_id)

        # Step 3: remap the caller's handle to the new task.
        self._handles[handle_id] = new_handle

    async def release(self, handle_id: str) -> None:
        """Release an assertion.  Silent no-op if handle_id is unknown."""
        handle = self._handles.pop(handle_id, None)
        if handle is None:
            return
        await self._stop(handle)

    async def release_all(self) -> None:
        """Release all outstanding assertions (called on graceful shutdown)."""
        handles = list(self._handles.items())
        self._handles.clear()
        await asyncio.gather(*(self._stop(h) for _, h in handles), return_exceptions=True)

    # ── private ────────────────────────────────────────────────────────────────

    async def _start_assertion(
        self,
        udid: str,
        type_: str,
        name: str,
        timeout_sec: int,
        details: Optional[str],
    ) -> _AssertionHandle:
        stop_event = asyncio.Event()
        active_event = asyncio.Event()
        task_name = f"{udid}|{type_}|{name}|{details or ''}"

        task = asyncio.create_task(
            self._hold_assertion(udid, type_, name, timeout_sec, details, stop_event, active_event),
            name=task_name,
        )

        # Wait until the assertion context is entered (or the task failed).
        done, _ = await asyncio.wait(
            [task, asyncio.create_task(active_event.wait())],
            return_when=asyncio.FIRST_COMPLETED,
        )

        if task in done and task.done():
            exc = task.exception() if not task.cancelled() else None
            raise RuntimeError(f"Assertion task failed immediately: {exc}")

        return _AssertionHandle(task=task, stop_event=stop_event, active_event=active_event)

    async def _hold_assertion(
        self,
        udid: str,
        type_: str,
        name: str,
        timeout_sec: int,
        details: Optional[str],
        stop_event: asyncio.Event,
        active_event: asyncio.Event,
    ) -> None:
        try:
            from pymobiledevice3.lockdown import create_using_usbmux
            from pymobiledevice3.services.power_assertion import PowerAssertionService

            lc = create_using_usbmux(serial=udid)
            svc = PowerAssertionService(lockdown=lc)
            kwargs: dict = {"type_": type_, "name": name, "timeout": timeout_sec}
            if details is not None:
                kwargs["details"] = details

            async with svc.create_power_assertion(**kwargs):
                active_event.set()
                await stop_event.wait()
        except Exception:
            log.exception("Power assertion task exiting with error (udid=%s)", udid)
            active_event.set()  # unblock _start_assertion so it can detect the failure
            raise

    @staticmethod
    async def _stop(handle: _AssertionHandle) -> None:
        handle.stop_event.set()
        try:
            await asyncio.wait_for(handle.task, timeout=10)
        except (asyncio.TimeoutError, asyncio.CancelledError):
            log.warning("Assertion task did not stop cleanly")
        except Exception:
            log.exception("Assertion task raised an error during stop")
