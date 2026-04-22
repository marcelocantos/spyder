# pmd3-bridge

Daemon-private HTTP bridge that exposes pymobiledevice3 operations to
spyder's Go daemon over an ephemeral loopback TCP port secured by a
per-process bearer token (🎯T26.1).

## Requirements

- Python ≥ 3.11
- `uv` (sole package manager)
- macOS arm64 (Darwin only — pmd3 has no Linux/Windows support for CoreDevice)

## Development setup

```bash
cd bridge
uv sync --extra dev
```

## Running

```bash
uv run python -m pmd3_bridge
```

The process binds `127.0.0.1:<ephemeral>`, generates a random bearer
token, and writes a single structured line to stdout:

```
ready port=NNNNN token=BASE64URL\n
```

The spyder daemon reads that line, constructs an HTTP client at
`http://127.0.0.1:NNNNN` with `Authorization: Bearer <token>` on every
request, and treats any unauthenticated request as a 401.  Send SIGTERM
to shut down gracefully.

## Testing

```bash
cd bridge
uv run pytest
```

Tests use a fake services layer — no real device is required.  The auth
middleware is a no-op when no token is installed, so the ASGITransport
test client works without a handshake.

## Building a self-contained bundle (PyInstaller)

```bash
cd bridge
uv run pyinstaller pmd3-bridge.spec
# Output: dist/pmd3-bridge/pmd3-bridge  (onedir)
```

Verify the bundle (bearer-token header is required):

```bash
# Start the bridge and capture the ready line.
dist/pmd3-bridge/pmd3-bridge 2>/dev/null | read -r _ port_kv token_kv
PORT="${port_kv#port=}"
TOKEN="${token_kv#token=}"
curl "http://127.0.0.1:$PORT/v1/list_devices" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{}'
```

## API

All endpoints accept and return JSON.  See `src/pmd3_bridge/schemas.py`
for the full request/response model.

| Path | Purpose |
|---|---|
| `POST /v1/list_devices` | Enumerate connected devices |
| `POST /v1/list_apps` | List installed apps on a device |
| `POST /v1/launch_app` | Launch an app, return PID |
| `POST /v1/kill_app` | Kill a running app |
| `POST /v1/pid_for_bundle` | Find PID of a running bundle |
| `POST /v1/battery` | Battery level and charging state |
| `POST /v1/screenshot` | Base64-encoded PNG screenshot |
| `POST /v1/crash_reports_list` | List crash reports |
| `POST /v1/crash_reports_pull` | Pull crash report content |
| `POST /v1/acquire_power_assertion` | Start a power assertion |
| `POST /v1/refresh_power_assertion` | Extend a power assertion |
| `POST /v1/release_power_assertion` | Release a power assertion |

Every request must carry `Authorization: Bearer <token>` matching the
token printed in the ready line; otherwise the bridge returns HTTP 401.

Error responses: `{"error": "<code>", "message": "<human>"}` with HTTP 4xx/5xx.

Error codes: `device_not_paired`, `bundle_not_installed`, `tunneld_unavailable`, `pmd3_error`, `unauthorized`.

## Architecture notes

### Power assertion lifecycle

pmd3's `PowerAssertionService.create_power_assertion()` is an async context
manager — the assertion lives only while inside the `async with` block.

To hold assertions across HTTP requests, `PowerAssertionManager` spawns an
asyncio Task per assertion.  Each task enters the context manager, sets an
`active_event`, then blocks on a `stop_event`.

**Refresh** avoids an idle gap by acquiring the replacement assertion first
(waiting until it is active), then stopping the old task.  The ordering is:

1. Start new task, wait for `active_event`.
2. Set `stop_event` on old task, await completion.
3. Remap the caller's `handle_id` to the new task.

This guarantees the device is never without an assertion during refresh.
