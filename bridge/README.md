# pmd3-bridge

HTTP bridge over a Unix domain socket that exposes pymobiledevice3
operations to spyder's Go daemon.

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
uv run python -m pmd3_bridge --socket /tmp/pmd3-bridge.sock
```

The process writes `ready\n` to stdout once the socket is listening, then
blocks.  Send SIGTERM to shut down gracefully.

## Testing

```bash
cd bridge
uv run pytest
```

Tests use a fake services layer — no real device is required.

## Building a self-contained bundle (PyInstaller)

```bash
cd bridge
uv run pyinstaller pmd3-bridge.spec
# Output: dist/pmd3-bridge/pmd3-bridge  (onedir)
```

Verify the bundle:

```bash
dist/pmd3-bridge/pmd3-bridge --socket /tmp/pmd3-bridge.sock &
curl --unix-socket /tmp/pmd3-bridge.sock -X POST http://localhost/v1/list_devices -H 'Content-Type: application/json' -d '{}'
kill %1
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

Error responses: `{"error": "<code>", "message": "<human>"}` with HTTP 4xx/5xx.

Error codes: `device_not_paired`, `bundle_not_installed`, `tunneld_unavailable`, `pmd3_error`.

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
