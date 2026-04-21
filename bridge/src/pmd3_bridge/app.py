# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""FastAPI application and route handlers."""
from __future__ import annotations

import logging
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI, Request, Response
from fastapi.responses import JSONResponse

from . import services
from .assertions import PowerAssertionManager
from .schemas import (
    AcquirePowerAssertionRequest,
    AcquirePowerAssertionResponse,
    BatteryRequest,
    BatteryResponse,
    CrashReportsListRequest,
    CrashReportsListResponse,
    CrashReportsPullRequest,
    CrashReportsPullResponse,
    KillAppRequest,
    KillAppResponse,
    LaunchAppRequest,
    LaunchAppResponse,
    ListAppsRequest,
    ListAppsResponse,
    ListDevicesResponse,
    PidForBundleRequest,
    PidForBundleResponse,
    RefreshPowerAssertionRequest,
    RefreshPowerAssertionResponse,
    ReleasePowerAssertionRequest,
    ReleasePowerAssertionResponse,
    ScreenshotRequest,
    ScreenshotResponse,
)
from .services import BridgeError

log = logging.getLogger(__name__)

_assertion_manager: PowerAssertionManager = PowerAssertionManager()


def _err(code: str, message: str, status: int) -> JSONResponse:
    return JSONResponse({"error": code, "message": message}, status_code=status)


def _classify(exc: BridgeError) -> JSONResponse:
    status = {
        "device_not_paired": 409,
        "bundle_not_installed": 422,
        "tunneld_unavailable": 503,
        "pmd3_error": 500,
    }.get(exc.code, 500)
    return _err(exc.code, exc.message, status)


@asynccontextmanager
async def _lifespan(app: FastAPI):  # type: ignore[type-arg]
    yield
    log.info("Shutdown: releasing all power assertions")
    await _assertion_manager.release_all()


app = FastAPI(title="pmd3-bridge", lifespan=_lifespan)

# Allow the test suite to inject a fake services module.
_services = services


def _set_services(svc: Any) -> None:  # noqa: ANN401  (intentional escape hatch)
    """Replace the services implementation.  For testing only."""
    global _services
    _services = svc


# ── Routes ─────────────────────────────────────────────────────────────────────

@app.post("/v1/list_devices", response_model=ListDevicesResponse)
async def list_devices(_req: Request) -> Any:
    try:
        devices = await _services.list_devices()
        return ListDevicesResponse(devices=devices)
    except BridgeError as exc:
        return _classify(exc)


@app.post("/v1/list_apps", response_model=ListAppsResponse)
async def list_apps(body: ListAppsRequest) -> Any:
    try:
        apps = await _services.list_apps(body.udid)
        return ListAppsResponse(apps=apps)
    except BridgeError as exc:
        return _classify(exc)


@app.post("/v1/launch_app", response_model=LaunchAppResponse)
async def launch_app(body: LaunchAppRequest) -> Any:
    try:
        pid = await _services.launch_app(body.udid, body.bundle_id)
        return LaunchAppResponse(pid=pid)
    except BridgeError as exc:
        return _classify(exc)


@app.post("/v1/kill_app", response_model=KillAppResponse)
async def kill_app(body: KillAppRequest) -> Any:
    try:
        await _services.kill_app(body.udid, body.bundle_id)
        return KillAppResponse()
    except BridgeError as exc:
        return _classify(exc)


@app.post("/v1/pid_for_bundle", response_model=PidForBundleResponse)
async def pid_for_bundle(body: PidForBundleRequest) -> Any:
    try:
        pid = await _services.pid_for_bundle(body.udid, body.bundle_id)
        return PidForBundleResponse(pid=pid)
    except BridgeError as exc:
        return _classify(exc)


@app.post("/v1/battery", response_model=BatteryResponse)
async def battery(body: BatteryRequest) -> Any:
    try:
        return await _services.battery(body.udid)
    except BridgeError as exc:
        return _classify(exc)


@app.post("/v1/screenshot", response_model=ScreenshotResponse)
async def screenshot(body: ScreenshotRequest) -> Any:
    try:
        png_b64 = await _services.screenshot(body.udid)
        return ScreenshotResponse(png_b64=png_b64)
    except BridgeError as exc:
        return _classify(exc)


@app.post("/v1/crash_reports_list", response_model=CrashReportsListResponse)
async def crash_reports_list(body: CrashReportsListRequest) -> Any:
    try:
        reports = await _services.crash_reports_list(body.udid, body.since_iso8601, body.process)
        return CrashReportsListResponse(reports=reports)
    except BridgeError as exc:
        return _classify(exc)


@app.post("/v1/crash_reports_pull", response_model=CrashReportsPullResponse)
async def crash_reports_pull(body: CrashReportsPullRequest) -> Any:
    try:
        content = await _services.crash_reports_pull(body.udid, body.name)
        return CrashReportsPullResponse(content=content)
    except BridgeError as exc:
        return _classify(exc)


@app.post("/v1/acquire_power_assertion", response_model=AcquirePowerAssertionResponse)
async def acquire_power_assertion(body: AcquirePowerAssertionRequest) -> Any:
    try:
        handle_id = await _assertion_manager.acquire(
            body.udid, body.type, body.name, body.timeout_sec, body.details
        )
        return AcquirePowerAssertionResponse(handle_id=handle_id)
    except BridgeError as exc:
        return _classify(exc)
    except Exception as exc:
        return _err("pmd3_error", str(exc), 500)


@app.post("/v1/refresh_power_assertion", response_model=RefreshPowerAssertionResponse)
async def refresh_power_assertion(body: RefreshPowerAssertionRequest) -> Any:
    try:
        await _assertion_manager.refresh(body.handle_id, body.timeout_sec)
        return RefreshPowerAssertionResponse()
    except KeyError:
        return _err("not_found", f"Unknown handle_id: {body.handle_id}", 404)
    except BridgeError as exc:
        return _classify(exc)
    except Exception as exc:
        return _err("pmd3_error", str(exc), 500)


@app.post("/v1/release_power_assertion", response_model=ReleasePowerAssertionResponse)
async def release_power_assertion(body: ReleasePowerAssertionRequest) -> Any:
    # release is a no-op for unknown handles (idempotent).
    await _assertion_manager.release(body.handle_id)
    return ReleasePowerAssertionResponse()
