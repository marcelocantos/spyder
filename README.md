# spyder

HTTP-based MCP server for cross-platform mobile development workflow
orchestration. Spyder owns device inventory, live device facts (battery,
charging, foreground app), screenshots, app lifecycle, and reservations
that serialise concurrent agent sessions on the same physical device.

Not a replacement for
[mobile-mcp](https://github.com/mobile-next/mobile-mcp) (UI automation via
WebDriverAgent) or [XcodeBuildMCP](https://github.com/getsentry/XcodeBuildMCP)
(xcodebuild). Spyder sits above them: it remembers what the device *is* and
wraps the workflow around it, using the bundled [go-ios](https://github.com/danielpaulus/go-ios)
library + CLI to talk directly to iOS physical devices where mobile-mcp's
WDA path often fails. The whole stack is one Go binary plus a small `ios`
helper binary (also Go) — no Python runtime, no system LaunchDaemon required.

## Quick start (for agents)

Paste this into your agent:

```
Install spyder from https://github.com/marcelocantos/spyder — brew install
the binary from the marcelocantos/tap, start the brew service, register it
as an HTTP MCP server with Claude Code, then restart this session. Follow
agents-guide.md in the repo for the full instructions (it's a multi-step
install — all steps are required).
```

## Install

```bash
# 1. Binary
brew install marcelocantos/tap/spyder

# 2. Persistent server
brew services start spyder

# 3. Register with Claude Code (HTTP transport)
claude mcp add --scope user --transport http spyder http://localhost:3030/mcp

# 4. Restart your agent session
```

Verify with `lsof -iTCP:3030 -sTCP:LISTEN` (the MCP endpoint only answers
JSON-RPC POSTs, so `curl` is not a useful probe).

### Troubleshooting

If spyder MCP tools aren't available in your agent (or vanish mid-session),
the daemon likely isn't running. The launchd service is `KeepAlive`, but it
won't start by itself if `brew services start spyder` was never run, and a
crash before the agent's MCP bridge has retried can leave the bridge in a
closed state.

```bash
brew services list | grep spyder    # want: "started"; "none" means step 2 was skipped
brew services start spyder          # first time, or after `brew services stop`
brew services restart spyder        # if it's "started" but :3030 isn't listening
lsof -iTCP:3030 -sTCP:LISTEN        # confirm spyder is actually listening
```

After the daemon is back, reload the spyder MCP server in your agent
(in Claude Code: `/mcp`, then reconnect) — bridges that exited while the
daemon was down don't auto-revive, but live ones reconnect on next call.

If you use an agentic coding tool, include
[`agents-guide.md`](agents-guide.md) in your project context — it has
everything below plus gotchas, device-inventory format, and the full
`spyder run` wrapper semantics.

## MCP interface: `app_exec`

Spyder exposes a **single** MCP tool, `app_exec`, which runs a Starlark
script with every spyder verb available as a builtin. This lets an agent
drive ordered, timed, looping device action in one call — no per-action
round-trips, so a transient UI state can't vanish between a tap and its
screenshot:

```starlark
deploy_app(device="iPad", path="/path/to/MyApp.app")
sleep(500)
app_screenshot()          # default (🎯T114): {path, width, height, bytes} under ~/.spyder/screenshots
# app_screenshot(inline=True)  # opt in to an inline MCP image block
```

**Durable scripts (🎯T108):** store versionable recipes under
`scripts/lib/` (repo) or `~/.spyder/scripts/`, list with
`spyder list-scripts`, run with `spyder run-script <name>` or
`app_exec(script_path="…", params={…})`. Explore / collect / dynamic
regress recipes and assert helpers are documented in
[agents-guide.md](agents-guide.md#durable-host-starlark-library-t108).

See [agents-guide.md](agents-guide.md#the-app_exec-entry-point) for the full
model (emit/result semantics, frame-stepping, durable handles, caps).
`max_duration_ms` defaults to 30 s and is capped at 600 000 ms (10 min).
`app_exec` is not itself a Starlark builtin (no nested scripting).

The runtime verb table is `Handler.toolHandlers` — every name below is a
Starlark builtin and a REST `POST /api/v1/<verb>` path. Call `help()` inside
a script for the live list; [agents-guide.md](agents-guide.md#builtin-reference)
has signatures.

| Group | Verbs |
|---|---|
| Device | `devices`, `resolve`, `device_state`, `screenshot`, `list_apps`, `launch_app`, `terminate_app`, `install_app`, `uninstall_app`, `deploy_app`, `launch_player`, `is_running` |
| Reservations / runs | `reserve`, `release`, `renew`, `reservations`, `reservation_status`, `runs_list`, `runs_show` |
| Observe | `rotate`, `crashes`, `logs`, `log_capture_start`, `log_capture_get`, `log_capture_stop`, `log_capture_list` |
| Sim / emu | `sim_list`, `sim_create`, `sim_boot`, `sim_shutdown`, `sim_delete`, `emu_list`, `emu_create`, `emu_boot`, `emu_shutdown`, `emu_delete` |
| Visual / record / net | `baseline_update`, `diff`, `baselines_list`, `record_start`, `record_stop`, `network` |
| OS control | `perf_fps`, `port_forward_start`, `port_forward_stop`, `port_forward_list`, `input_tap`, `input_swipe`, `device_setting` |
| App channel | `app_channel_stop`, `app_channel_list`, `app_ping`, `app_quit`, `app_flush`, `app_background`, `app_foreground`, `app_low_memory`, `app_pause`, `app_resume`, `app_step`, `app_speed`, `app_input`, `app_sensor_suppress`, `app_sensor_set`, `app_sensor_unsuppress`, `app_sensor_status`, `ensure_session`, `state_query`, `app_state`, `wait_state`, `app_tweak_list`, `app_tweak_get`, `app_tweak_set`, `app_tweak_reset`, `app_spawn`, `app_acquire`, `app_release`, `games`, `app_save_state`, `app_restore_state`, `app_screenshot`, `app_state_slices`, `app_state_describe`, `app_state_capture_start`, `app_state_capture_get`, `app_state_capture_stop`, `app_state_capture_list`, `app_log_get`, `app_perf_get`, `app_metrics_list`, `app_metrics_arm`, `app_metrics_disarm`, `app_metrics_status`, `app_metrics_dump`, `app_methods`, `app_call` |
| Pool / scripts | `pool_list`, `pool_warm`, `pool_drain`, `pool_gc`, `list_scripts`, `run_script` |

Starlark also adds non-verb helpers: `sleep`, `emit`, `health()`, `help()`,
and the 🎯T108/T109 assert and hit-target helpers.

## REST API and live log streaming

Every spyder verb is also exposed directly as plain HTTP+JSON on the same
listener — the REST transport keeps per-verb access by URL path (MCP is
`app_exec`-only):

```bash
# Human-or-script friendly: shares state with the MCP endpoint.
curl -s -X POST http://127.0.0.1:3030/api/v1/devices \
  -H 'Content-Type: application/json' -d '{"platform":"android"}'

# Zero-arg tools accept an empty body.
curl -s -X POST http://127.0.0.1:3030/api/v1/reservations
```

Responses are JSON-encoded `mcp.CallToolResult` objects
(`{"content":[{"type":"text","text":"…"}], "isError":false}`).
`screenshot` / `app_screenshot` default to a JSON path dict (🎯T114);
pass `inline: true` for a `type:"image"` base64 block.

For live log tailing, use the SSE endpoint:

```bash
# Stream filtered log lines until Ctrl-C.
curl -N -X POST http://127.0.0.1:3030/api/v1/log_stream \
  -H 'Content-Type: application/json' \
  -d '{"device":"iPad","process":"MyApp","regex":"error"}'
```

Each SSE event is `data: <JSON LogLine>` on a single line, followed by a
blank line. The stream runs until the client disconnects.

Reservation state is shared between transports — an agent holding a
reservation via MCP blocks a shell script hitting REST and vice versa.

## CLI device tools

The same surface is available as subcommands of the `spyder` binary.
These POST to the local daemon; set `SPYDER_DAEMON_URL` to override
the default `http://127.0.0.1:3030`.

```bash
spyder doctor                 # iOS device-stack diagnosis (no daemon required for --fix)
spyder status --json          # live health model (daemon must be up)
spyder devices --platform ios --json
spyder screenshot iPad --output /tmp/ipad.png
spyder launch-player iPad --server tiltbuggy
spyder reserve iPad --ttl 600 --note "UI sweep"
spyder reservations --json
spyder release iPad
spyder rotate C6F6FA50-30B5-4E4C-B7A1-8E0F5D1E1FA8 --to landscape-left
spyder log iPad --bundle-id com.example.app --since -2m   # filter by bundle id
spyder log iPad --bundle-id com.example.app --since launch # everything since the last launch_app
spyder log iPad --process MyApp --since -2m               # filter by executable name
spyder log iPad --follow --bundle-id com.example.app      # live SSE tail
spyder crashes iPad --bundle-id com.example.app --since -1h --json
spyder runs list
spyder runs show 20260419-143022-a3f1b2
spyder runs artefacts 20260419-143022-a3f1b2
```

`--as OWNER` flags default to `filepath.Base(cwd)` so project-rooted
shells get a sensible reservation identity without ceremony.

## Test-run wrapper

```bash
spyder run -- xcodebuild -project MyApp.xcodeproj \
  -scheme MyApp -destination 'id=00008103-001122334455667A' test
```

Runs the command, waits for it to exit, then releases the device reservation
regardless of success/failure. Forwards the command's exit code.

Spyder auto-acquires an exclusive reservation on the device for the
command's lifetime (owner defaults to `filepath.Base(cwd)` — pass
`--as <owner>` to override). Other parallel sessions that try to
mutate the same device via MCP will get a clean conflict error
naming the current holder. Opportunistic renewal keeps long runs
alive; release on exit is guaranteed.

## Device inventory

Spyder reads `~/.spyder/inventory.json` — a JSON array mapping symbolic
aliases to platform-specific UUIDs. Alias lookup is case-insensitive;
unknown raw identifiers are classified by format and passed through. See
the [agent guide](agents-guide.md#device-inventory) for the format.

## Ship front door

Spyder is the laptop front door for Squz / Minicades store shipping:
studio secrets live in a macOS keychain envelope (never MCP/REST verbs),
and `spyder fastlane` wraps fastlane with lane-class gates and a redacted
audit trail. After `make build`, run `make sign` so keychain ACLs bind to
a stable code signature.

```bash
spyder secret status --studio squz
spyder secret import --studio squz          # clipboard absorb
spyder secret missing --studio squz --for match
spyder fastlane --studio squz -- pilot
```

See [docs/ship-front-door.md](docs/ship-front-door.md) and
[agents-guide.md](agents-guide.md#ship-front-door) for the full contract.

## Build from source

```bash
make build          # bin/spyder + bin/ios (bundled tunnel daemon)
make test
make bullseye       # full invariants
```

Dependencies:

- Go 1.26+
- `xcrun` (macOS, simulator support — Apple)
- `adb` (Android operations)
- `alerter` (persistent macOS notifications for the locked-device prompt;
  falls back to `terminal-notifier` → `osascript`)

iOS device support is in-process via the bundled
[go-ios](https://github.com/danielpaulus/go-ios) Go library; the
`bin/ios` binary that `make build` produces is the same project's CLI
and is spawned by spyder as a userspace tunnel daemon at runtime.
No Python, no system LaunchDaemon.

## Stream player

`player/` builds `bin/player`, the **spyder player** — stream glass for
headless game servers. The default transport is the command stream (SP2S);
the player replays sokol ops on its local GPU. H.264 decode still works
but is frozen in place — do not extend it. Touch, key, and accelerometer
input go back over the wire. iOS and Android app variants live under
`player/ios/` and `player/android/`. Use `launch_player` (not `deploy_app`)
to put the player on a device.

```bash
make player                     # bin/player (self-contained)
bin/player --host localhost --port 3030 --name tiltbuggy
bin/player --headless --script gestures.txt --trace out.trace  # oracle mode
```

The player vendors C/C++ third-party libraries; see
[player/NOTICES.md](player/NOTICES.md) for attribution.

## Licence

Apache 2.0 — see [LICENSE](LICENSE).
