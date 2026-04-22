# Copyright 2026 Marcelo Cantos
# SPDX-License-Identifier: Apache-2.0
"""FastAPI application and route handlers.

Logging (🎯T26.5): every handler emits an entry log (endpoint + udid if
present), and the dispatcher middleware logs exit with duration and
outcome. The structured trail complements Uvicorn's own access log,
adding pmd3-level context (which device, which bundle, which assertion
type) that is not visible at the HTTP layer alone.
"""
from __future__ import annotations

import logging
import time
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

log = logging.getLogger("pmd3_bridge.app")

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
    log.info("bridge app startup complete")
    yield
    log.info("bridge app shutdown: releasing all power assertions")
    await _assertion_manager.release_all()
    log.info("bridge app shutdown complete")


app = FastAPI(title="pmd3-bridge", lifespan=_lifespan)


# Middleware that logs every request at pmd3 level with duration + outcome,
# complementing Uvicorn's access log with the per-handler view.
@app.middleware("http")
async def _log_requests(request: Request, call_next: Any) -> Response:
    started = time.monotonic()
    path = request.url.path
    try:
        response = await call_next(request)
    except Exception as exc:
        elapsed_ms = int((time.monotonic() - started) * 1000)
        log.exception("handler raised %s elapsed_ms=%d path=%s",
                      type(exc).__name__, elapsed_ms, path)
        raise
    elapsed_ms = int((time.monotonic() - started) * 1000)
    log.info("handler %s %s %d elapsed_ms=%d",
             request.method, path, response.status_code, elapsed_ms)
    return response


# Allow the test suite to inject a fake services module.
_services = services


def _set_services(svc: Any) -> None:  # noqa: ANN401  (intentional escape hatch)
    """Replace the services implementation.  For testing only."""
    global _services
    _services = svc


# ── Routes ─────────────────────────────────────────────────────────────────────

@app.post("/v1/list_devices", response_model=ListDevicesResponse)
async def list_devices(_req: Request) -> Any:
    log.debug("list_devices")
    try:
        devices = await _services.list_devices()
        log.debug("list_devices returned %d device(s)", len(devices))
        return ListDevicesResponse(devices=devices)
    except BridgeError as exc:
        log.warning("list_devices failed code=%s message=%s", exc.code, exc.message)
        return _classify(exc)


@app.post("/v1/list_apps", response_model=ListAppsResponse)
async def list_apps(body: ListAppsRequest) -> Any:
    log.debug("list_apps udid=%s", body.udid)
    try:
        apps = await _services.list_apps(body.udid)
        log.debug("list_apps udid=%s returned %d app(s)", body.udid, len(apps))
        return ListAppsResponse(apps=apps)
    except BridgeError as exc:
        log.warning("list_apps udid=%s failed code=%s message=%s",
                    body.udid, exc.code, exc.message)
        return _classify(exc)


@app.post("/v1/launch_app", response_model=LaunchAppResponse)
async def launch_app(body: LaunchAppRequest) -> Any:
    log.info("launch_app udid=%s bundle=%s", body.udid, body.bundle_id)
    try:
        pid = await _services.launch_app(body.udid, body.bundle_id)
        log.info("launch_app udid=%s bundle=%s pid=%d", body.udid, body.bundle_id, pid)
        return LaunchAppResponse(pid=pid)
    except BridgeError as exc:
        log.warning("launch_app udid=%s bundle=%s failed code=%s message=%s",
                    body.udid, body.bundle_id, exc.code, exc.message)
        return _classify(exc)


@app.post("/v1/kill_app", response_model=KillAppResponse)
async def kill_app(body: KillAppRequest) -> Any:
    log.info("kill_app udid=%s bundle=%s", body.udid, body.bundle_id)
    try:
        await _services.kill_app(body.udid, body.bundle_id)
        return KillAppResponse()
    except BridgeError as exc:
        log.warning("kill_app udid=%s bundle=%s failed code=%s message=%s",
                    body.udid, body.bundle_id, exc.code, exc.message)
        return _classify(exc)


@app.post("/v1/pid_for_bundle", response_model=PidForBundleResponse)
async def pid_for_bundle(body: PidForBundleRequest) -> Any:
    log.debug("pid_for_bundle udid=%s bundle=%s", body.udid, body.bundle_id)
    try:
        pid = await _services.pid_for_bundle(body.udid, body.bundle_id)
        return PidForBundleResponse(pid=pid)
    except BridgeError as exc:
        log.warning("pid_for_bundle udid=%s bundle=%s failed code=%s message=%s",
                    body.udid, body.bundle_id, exc.code, exc.message)
        return _classify(exc)


@app.post("/v1/battery", response_model=BatteryResponse)
async def battery(body: BatteryRequest) -> Any:
    log.debug("battery udid=%s", body.udid)
    try:
        return await _services.battery(body.udid)
    except BridgeError as exc:
        log.warning("battery udid=%s failed code=%s message=%s",
                    body.udid, exc.code, exc.message)
        return _classify(exc)


@app.post("/v1/screenshot", response_model=ScreenshotResponse)
async def screenshot(body: ScreenshotRequest) -> Any:
    log.info("screenshot udid=%s", body.udid)
    try:
        png_b64 = await _services.screenshot(body.udid)
        log.info("screenshot udid=%s bytes=%d", body.udid, len(png_b64))
        return ScreenshotResponse(png_b64=png_b64)
    except BridgeError as exc:
        log.warning("screenshot udid=%s failed code=%s message=%s",
                    body.udid, exc.code, exc.message)
        return _classify(exc)


@app.post("/v1/crash_reports_list", response_model=CrashReportsListResponse)
async def crash_reports_list(body: CrashReportsListRequest) -> Any:
    log.info("crash_reports_list udid=%s since=%s process=%s",
             body.udid, body.since_iso8601, body.process)
    try:
        reports = await _services.crash_reports_list(body.udid, body.since_iso8601, body.process)
        log.info("crash_reports_list udid=%s returned %d report(s)",
                 body.udid, len(reports))
        return CrashReportsListResponse(reports=reports)
    except BridgeError as exc:
        log.warning("crash_reports_list udid=%s failed code=%s message=%s",
                    body.udid, exc.code, exc.message)
        return _classify(exc)


@app.post("/v1/crash_reports_pull", response_model=CrashReportsPullResponse)
async def crash_reports_pull(body: CrashReportsPullRequest) -> Any:
    log.info("crash_reports_pull udid=%s name=%s", body.udid, body.name)
    try:
        content = await _services.crash_reports_pull(body.udid, body.name)
        log.info("crash_reports_pull udid=%s name=%s bytes=%d",
                 body.udid, body.name, len(content))
        return CrashReportsPullResponse(content=content)
    except BridgeError as exc:
        log.warning("crash_reports_pull udid=%s name=%s failed code=%s message=%s",
                    body.udid, body.name, exc.code, exc.message)
        return _classify(exc)


@app.post("/v1/acquire_power_assertion", response_model=AcquirePowerAssertionResponse)
async def acquire_power_assertion(body: AcquirePowerAssertionRequest) -> Any:
    log.info("acquire_power_assertion udid=%s type=%s name=%s timeout=%ds",
             body.udid, body.type, body.name, body.timeout_sec)
    try:
        handle_id = await _assertion_manager.acquire(
            body.udid, body.type, body.name, body.timeout_sec, body.details
        )
        log.info("acquire_power_assertion udid=%s handle=%s",
                 body.udid, handle_id)
        return AcquirePowerAssertionResponse(handle_id=handle_id)
    except BridgeError as exc:
        log.warning("acquire_power_assertion udid=%s failed code=%s message=%s",
                    body.udid, exc.code, exc.message)
        return _classify(exc)
    except Exception as exc:
        log.exception("acquire_power_assertion udid=%s unexpected", body.udid)
        return _err("pmd3_error", str(exc), 500)


@app.post("/v1/refresh_power_assertion", response_model=RefreshPowerAssertionResponse)
async def refresh_power_assertion(body: RefreshPowerAssertionRequest) -> Any:
    log.debug("refresh_power_assertion handle=%s timeout=%ds",
              body.handle_id, body.timeout_sec)
    try:
        await _assertion_manager.refresh(body.handle_id, body.timeout_sec)
        return RefreshPowerAssertionResponse()
    except KeyError:
        log.warning("refresh_power_assertion unknown handle=%s", body.handle_id)
        return _err("not_found", f"Unknown handle_id: {body.handle_id}", 404)
    except BridgeError as exc:
        log.warning("refresh_power_assertion handle=%s failed code=%s message=%s",
                    body.handle_id, exc.code, exc.message)
        return _classify(exc)
    except Exception as exc:
        log.exception("refresh_power_assertion handle=%s unexpected", body.handle_id)
        return _err("pmd3_error", str(exc), 500)


@app.post("/v1/release_power_assertion", response_model=ReleasePowerAssertionResponse)
async def release_power_assertion(body: ReleasePowerAssertionRequest) -> Any:
    log.info("release_power_assertion handle=%s", body.handle_id)
    # release is a no-op for unknown handles (idempotent).
    await _assertion_manager.release(body.handle_id)
    return ReleasePowerAssertionResponse()
