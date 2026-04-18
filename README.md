# spyder

HTTP-based MCP server for cross-platform mobile development workflow
orchestration. Spyder owns device inventory, live device facts (battery,
charging, foreground app), screenshots, app lifecycle, and a plug-in-driven
auto-launcher for the KeepAwake companion app on iOS.

Not a replacement for
[mobile-mcp](https://github.com/mobile-next/mobile-mcp) (UI automation via
WebDriverAgent) or [XcodeBuildMCP](https://github.com/getsentry/XcodeBuildMCP)
(xcodebuild). Spyder sits above them: it remembers what the device *is* and
wraps the workflow around it, using `pymobiledevice3` + CoreDevice to talk
directly to iOS physical devices where mobile-mcp's WDA path often fails.

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

If you use an agentic coding tool, include
[`agents-guide.md`](agents-guide.md) in your project context — it has
everything below plus gotchas, device-inventory format, and the full
`spyder run` wrapper semantics.

## MCP tools

| Tool | Purpose |
|---|---|
| `devices` | List connected iOS + Android devices, annotated with inventory alias. |
| `resolve` | Symbolic name → structured entry with all known UUIDs. |
| `device_state` | Battery, charging, thermal, foreground app. 2 s TTL cache. |
| `screenshot` | PNG of the current screen. iOS via DVT; Android via `adb screencap`. |
| `keepawake` | Foreground the KeepAwake companion app (iOS). No-op on Android. |
| `list_apps` | Installed third-party apps. |
| `launch_app` | Foreground an arbitrary app by bundle id. |
| `terminate_app` | Stop an app by bundle id. |

## Test-run wrapper

```bash
spyder run -- xcodebuild -project MyApp.xcodeproj \
  -scheme MyApp -destination 'id=00008103-000D39301A6A201E' test
```

Runs the command, waits for it to exit, then foregrounds KeepAwake on the
device regardless of success/failure. Forwards the command's exit code.

## Auto-awake supervisor

`spyder serve` polls `pymobiledevice3 remote tunneld` for paired iOS
devices. For each newly-seen device:

1. Checks whether KeepAwake is installed.
2. If not, auto-deploys via `xcodegen` + `xcodebuild` + `devicectl install`.
3. Launches KeepAwake via DVT.
4. If the device is locked, fires a **persistent macOS alert** via
   [`alerter`](https://github.com/vjeantet/alerter) asking the user to
   unlock. The alert auto-dismisses on successful launch.

## Device inventory

Spyder reads `~/.spyder/inventory.json` — a JSON array mapping symbolic
aliases to platform-specific UUIDs. Alias lookup is case-insensitive;
unknown raw identifiers are classified by format and passed through. See
the [agent guide](agents-guide.md#device-inventory) for the format.

## Build from source

```bash
make build          # bin/spyder
make test
make bullseye       # full invariants
```

Dependencies:

- Go 1.26+
- `pymobiledevice3` ≥ 8.2 in PATH (iOS operations)
- `adb` (Android operations)
- `xcodegen` + Xcode (auto-deploy of KeepAwake on iOS)
- `alerter` (persistent macOS notifications for the locked-device prompt;
  falls back to `terminal-notifier` → `osascript`)

## Licence

Apache 2.0 — see [LICENSE](LICENSE).
