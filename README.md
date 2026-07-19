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
app_screenshot()          # final expression → returned as an image block
```

See [agents-guide.md](agents-guide.md#the-app_exec-entry-point) for the full
model (emit/result semantics, frame-stepping, durable handles, caps). The
verbs available as builtins:

| Verb | Purpose |
|---|---|
| `devices` | List connected iOS + Android devices, annotated with inventory alias. |
| `resolve` | Symbolic name → structured entry with all known UUIDs. |
| `device_state` | Battery, charging, thermal, foreground app. 2 s TTL cache. |
| `screenshot` | PNG of the current screen. iOS via DVT; Android via `adb screencap`. |
| `list_apps` | Installed third-party apps. |
| `launch_app` | Foreground an arbitrary app by bundle id. |
| `terminate_app` | Stop an app by bundle id. |
| `rotate` | Rotate an iOS simulator or Android emulator to portrait, landscape-left, landscape-right, or portrait-upside-down. Physical devices return an error. |
| `install_app` | Install a .app/.ipa (iOS) or .apk (Android) on a device. |
| `uninstall_app` | Remove an app by bundle id / package name. |
| `deploy_app` | Atomic deploy: terminate → install → launch → verify pid. Returns `{bundle_id, pid}`. |
| `reserve` / `release` / `renew` / `reservations` | Exclusive device holds for parallel dev sessions. Mutating tools are strict; read tools are unaffected. `reserve` accepts a literal `device` pin or a `selector` JSON predicate for fuzzy matching — see [agents-guide.md](agents-guide.md#fuzzy-reservation-selector). |
| `runs_list` / `runs_show` | Inspect per-reservation artefact bundles under `~/.spyder/runs/`. |
| `baseline_update` | Store a reference screenshot (and optional UI manifest) as a visual baseline. |
| `diff` | Compare a candidate screenshot against the stored baseline. Returns pixel RMS error, manifest structural diff (added/removed/moved elements with bounding boxes), and a pass/fail verdict. |
| `baselines_list` | List all stored baselines for a suite. |
| `record_start` / `record_stop` | Start and stop a screen recording (mp4). iOS simulators via `xcrun simctl io recordVideo`; Android via `adb shell screenrecord`. **iOS physical devices are not supported** — use a simulator. |
| `network` | Apply or clear network condition shaping. Android emulators only — see STABILITY.md for platform limits. |
| `logs` | Fetch log lines between two timestamps. `since` / `until` accept either an RFC3339 absolute or a Go duration relative to now — `since=-2m`, `until=now`. `since=launch` is shorthand for "everything since spyder last called `launch_app` for `bundle_id`". Filter by app with `bundle_id` (resolved server-side to the iOS `CFBundleExecutable` / Android package name) or by raw `process` name; the two are mutually exclusive. Also: `subsystem` (iOS), `tag` (Android), `regex`. Read-only. |
| `crashes` | Fetch crash reports from a device. iOS pulls .ips via go-ios `crashreport`; Android via `adb` tombstones + `logcat -b crash`. `since` accepts RFC3339 or a Go duration (`-15m`, `-1h`). Filter by app with `bundle_id` or by raw `process` name (mutually exclusive). Read-only. |
| `pool_list` / `pool_warm` / `pool_drain` | Sim/emu pool management. Inspect tier counts, pre-boot instances, or drain idle instances. Requires `~/.spyder/pool.yaml` — see [agents-guide.md](agents-guide.md#simemu-pool). |

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
Image-bearing tools (`screenshot`) yield `type:"image"` with base64
`data` + `mimeType`, identical to MCP.

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
spyder devices --platform ios --json
spyder screenshot iPad --output /tmp/ipad.png
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
headless game servers. It attaches to spyder's stream relay, presents
the server's command stream (with H.264 fallback), and forwards touch,
key, and accelerometer input back over the wire. iOS and Android app
variants live under `player/ios/` and `player/android/`.

```bash
make player                     # bin/player (self-contained)
bin/player --host localhost --port 3030 --name tiltbuggy
bin/player --headless --script gestures.txt --trace out.trace  # oracle mode
```

The player vendors C/C++ third-party libraries; see
[player/NOTICES.md](player/NOTICES.md) for attribution.

## Licence

Apache 2.0 — see [LICENSE](LICENSE).
