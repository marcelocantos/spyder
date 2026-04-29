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
import traceback
import uuid
from contextlib import asynccontextmanager
from typing import Any

import json as _json

from fastapi import FastAPI, Request, Response
from fastapi.responses import JSONResponse, StreamingResponse

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
    DevicePowerStateRequest,
    DevicePowerStateResponse,
    KillAppRequest,
    KillAppResponse,
    LaunchAppRequest,
    LaunchAppResponse,
    ListAppsRequest,
    ListAppsResponse,
    ListDevicesResponse,
    AppStateRequest,
    AppStateResponse,
    PidForBundleRequest,
    PidForBundleResponse,
    RefreshPowerAssertionRequest,
    RefreshPowerAssertionResponse,
    ReleasePowerAssertionRequest,
    ReleasePowerAssertionResponse,
    ScreenshotRequest,
    ScreenshotResponse,
    SyslogRequest,
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
        "developer_mode_disabled": 412,
        "tunneld_unavailable": 503,
        "pmd3_busy": 503,      # 🎯T50 AC5: concurrency limit reached — retry later
        "pmd3_timeout": 504,   # 🎯T50 AC2: external await timed out
        "pmd3_error": 500,
    }.get(exc.code, 500)
    return _err(exc.code, exc.message, status)


# Process startup time, used by /v1/health to report uptime.
_started_at: float = time.monotonic()


@asynccontextmanager
async def _lifespan(app: FastAPI):  # type: ignore[type-arg]
    log.info("bridge app startup complete")
    yield
    log.info("bridge app shutdown: releasing all power assertions")
    await _assertion_manager.release_all()
    log.info("bridge app shutdown complete")


app = FastAPI(title="pmd3-bridge", lifespan=_lifespan)


def _make_unhandled_exception_response(request: Request, exc: Exception) -> JSONResponse:
    """Convert an uncaught handler exception into a structured 500 response.
    Used by both the FastAPI exception_handler hook and the request-logging
    middleware (BaseHTTPMiddleware-raised exceptions don't always reach the
    exception_handler chain in older Starlette/FastAPI combinations, so we
    cover both paths)."""
    correlation_id = uuid.uuid4().hex[:12]
    log.error(
        "unhandled exception in handler path=%s correlation_id=%s exc_type=%s message=%s\n%s",
        request.url.path,
        correlation_id,
        type(exc).__name__,
        str(exc),
        traceback.format_exc(),
    )
    return JSONResponse(
        {
            "error": "pmd3_error",
            "message": f"unhandled {type(exc).__name__}: {exc}",
            "correlation_id": correlation_id,
        },
        status_code=500,
    )


# Outer catch-all (🎯T50): no exception is ever allowed to escape a route
# handler. Any uncaught Exception becomes a structured pmd3_error 500
# response with a correlation ID; the full traceback is logged so the
# Go-side post-mortem has it. This is the bulkhead that keeps the bridge
# alive when a handler hits a code path no-one anticipated.
@app.exception_handler(Exception)
async def _unhandled_exception(request: Request, exc: Exception) -> JSONResponse:
    return _make_unhandled_exception_response(request, exc)


# Auth token installed by __main__.py at startup. None → no auth required
# (for tests that use ASGITransport directly and bypass the loopback listener).
_auth_token: str | None = None


def set_auth_token(token: str) -> None:
    """Install the bearer token the bridge accepts. Called from __main__.py
    after generating a fresh random token per process lifetime."""
    global _auth_token
    _auth_token = token


def get_auth_token() -> str | None:
    """Exposed for tests that need to mint requests with the current token."""
    return _auth_token


# Auth middleware (🎯T26.1): the bridge is a daemon-private subprocess. Any
# request that arrives without the matching bearer token is an outside-the-
# daemon connection attempt and is rejected with 401.
@app.middleware("http")
async def _check_auth(request: Request, call_next: Any) -> Response:
    token = _auth_token
    if token is None:
        # No token configured (test mode); allow.
        return await call_next(request)
    header = request.headers.get("authorization", "")
    expected = f"Bearer {token}"
    if header != expected:
        log.warning("auth rejected path=%s remote=%s", request.url.path,
                    request.client.host if request.client else "?")
        return JSONResponse({"error": "unauthorized", "message": "bearer token missing or mismatched"},
                            status_code=401)
    return await call_next(request)


# Middleware that logs every request at pmd3 level with duration + outcome,
# complementing Uvicorn's access log with the per-handler view.
#
# Doubles as the bulkhead path for unhandled exceptions (🎯T50): if a route
# raises something not caught by route-level `except BridgeError`, this
# middleware converts it into the structured pmd3_error 500 response. The
# FastAPI @app.exception_handler(Exception) sibling above also handles
# these, but Starlette's BaseHTTPMiddleware sometimes intercepts the
# exception before the handler chain can — covering both paths means a
# bug never escapes as a transport-level crash.
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
        return _make_unhandled_exception_response(request, exc)
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

@app.get("/v1/health")
@app.post("/v1/health")
async def health() -> Any:
    """Liveness probe (🎯T50). Returns immediately, touches no device state.

    The Go LivenessProbe routes through this endpoint so liveness checks
    are decoupled from device-state correctness — wedged device paths
    surface as structured BridgeError on the relevant endpoint, not as
    a transport timeout on a probe.
    """
    return {
        "ok": True,
        "uptime_s": round(time.monotonic() - _started_at, 3),
    }


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


@app.post("/v1/app_state", response_model=AppStateResponse)
async def app_state(body: AppStateRequest) -> Any:
    log.debug("app_state udid=%s bundle=%s", body.udid, body.bundle_id)
    try:
        state, desc = await _services.app_state(body.udid, body.bundle_id)
        return AppStateResponse(state=state, description=desc)
    except BridgeError as exc:
        log.warning("app_state udid=%s bundle=%s failed code=%s message=%s",
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


@app.post("/v1/crash_reports_list")
async def crash_reports_list(body: CrashReportsListRequest) -> Any:
    """Stream the crash-report index as NDJSON (🎯T26.3).

    One JSON object per line. The Go client reads line-by-line with an
    inter-packet deadline; a gap > 10 s between lines panics the daemon.
    """
    log.info("crash_reports_list udid=%s since=%s process=%s",
             body.udid, body.since_iso8601, body.process)
    # Probe the generator for synchronous errors (notably _lockdown failing
    # with device_not_paired); those should surface as HTTP 4xx, not as a
    # stream that opens 200 and then errors mid-body.
    try:
        gen = await _services.crash_reports_list(body.udid, body.since_iso8601, body.process)
    except BridgeError as exc:
        log.warning("crash_reports_list udid=%s failed code=%s message=%s",
                    body.udid, exc.code, exc.message)
        return _classify(exc)

    async def _iter() -> Any:
        count = 0
        try:
            async for entry in gen:
                count += 1
                yield (_json.dumps(entry.model_dump()) + "\n").encode("utf-8")
        except BridgeError as exc:
            # Late error: we've already committed to 200. Emit a terminating
            # NDJSON line carrying the error. The Go client treats the
            # trailing `{"error":...}` shape as a structured BridgeError.
            log.warning("crash_reports_list udid=%s mid-stream error code=%s message=%s",
                        body.udid, exc.code, exc.message)
            yield (_json.dumps({"error": exc.code, "message": exc.message}) + "\n").encode("utf-8")
        log.info("crash_reports_list udid=%s streamed %d entries", body.udid, count)

    return StreamingResponse(_iter(), media_type="application/x-ndjson")


@app.post("/v1/crash_reports_pull")
async def crash_reports_pull(body: CrashReportsPullRequest) -> Any:
    """Stream a crash report as raw octet-stream bytes (🎯T26.3).

    Transfer-Encoding: chunked; Content-Type: application/octet-stream.
    The Go client drives io.Copy with an inter-packet read deadline.
    """
    log.info("crash_reports_pull udid=%s name=%s", body.udid, body.name)
    try:
        gen = await _services.crash_reports_pull(body.udid, body.name)
    except BridgeError as exc:
        log.warning("crash_reports_pull udid=%s name=%s failed code=%s message=%s",
                    body.udid, body.name, exc.code, exc.message)
        return _classify(exc)

    async def _iter() -> Any:
        total = 0
        try:
            async for chunk in gen:
                total += len(chunk)
                yield chunk
        except BridgeError as exc:
            # Mid-stream failure on an octet-stream response: we cannot
            # surface a structured error at this point, so just close the
            # stream. The Go client's inter-packet deadline will catch
            # prolonged stalls; a clean close with no prior bytes reads
            # as a pmd3_error at that layer.
            log.warning("crash_reports_pull udid=%s name=%s mid-stream error code=%s message=%s",
                        body.udid, body.name, exc.code, exc.message)
        log.info("crash_reports_pull udid=%s name=%s streamed %d bytes",
                 body.udid, body.name, total)

    return StreamingResponse(_iter(), media_type="application/octet-stream")


@app.post("/v1/syslog")
async def syslog(body: SyslogRequest) -> Any:
    """Stream syslog entries as NDJSON (🎯T46).

    One JSON object per line. The Go client reads line-by-line with an
    inter-packet deadline matching crash_reports_list. The stream stays
    open until the client closes the connection.
    """
    log.info(
        "syslog udid=%s pid=%d process_name=%s subsystem=%s",
        body.udid, body.pid, body.process_name, body.subsystem,
    )
    try:
        gen = await _services.syslog(
            body.udid, body.pid, body.process_name, body.subsystem,
        )
    except BridgeError as exc:
        log.warning("syslog udid=%s failed code=%s message=%s",
                    body.udid, exc.code, exc.message)
        return _classify(exc)

    async def _iter() -> Any:
        count = 0
        try:
            async for entry in gen:
                count += 1
                yield (_json.dumps(entry.model_dump()) + "\n").encode("utf-8")
        except BridgeError as exc:
            log.warning("syslog udid=%s mid-stream error code=%s message=%s",
                        body.udid, exc.code, exc.message)
            yield (_json.dumps({"error": exc.code, "message": exc.message}) + "\n").encode("utf-8")
        log.info("syslog udid=%s streamed %d entries", body.udid, count)

    return StreamingResponse(_iter(), media_type="application/x-ndjson")


@app.post("/v1/device_power_state", response_model=DevicePowerStateResponse)
async def device_power_state(body: DevicePowerStateRequest) -> Any:
    """Query the power/display state of a device (🎯T29).

    Uses the DVT Screenshot instrument via tunneld RSD. Reading the
    framebuffer does NOT reset the device's idle timer (non-observation
    requirement). See docs/papers/t29-device-state-detection.md.

    BridgeErrors that indicate a missing prerequisite (tunneld_unavailable,
    developer_mode_disabled, device_not_paired) map to state="unknown" so
    the caller always gets a structured DevicePowerStateResponse rather than
    an HTTP error. Hard errors (pmd3_error with unrecognised cause) are
    handled by the service function; any unexpected BridgeError that escapes
    is mapped to unknown here as a safety net.
    """
    log.debug("device_power_state udid=%s", body.udid)
    try:
        result = await _services.device_power_state(body.udid)
        log.debug("device_power_state udid=%s state=%s", body.udid, result.state)
        return result
    except BridgeError as exc:
        # Safety net: any BridgeError that escapes device_power_state becomes
        # state="unknown" rather than an HTTP error. The service function
        # should handle all known codes; this path should not normally fire.
        log.warning("device_power_state udid=%s escaped BridgeError code=%s message=%s",
                    body.udid, exc.code, exc.message)
        return DevicePowerStateResponse(state="unknown", detail=exc.message)


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
