# spyder

Cross-platform mobile development workflow assistant. HTTP-based MCP
server (Go) that sits above mobile-mcp and XcodeBuildMCP as the
session-state layer — device inventory, wake-state, and prep/run/restore
orchestration around real-device tests. iOS physical devices are a
first-class citizen (via the bundled
[go-ios](https://github.com/danielpaulus/go-ios) Go library — usbmux,
lockdown, DTX, RSD, all in-process), where mobile-mcp's WDA path
often fails; Android is supported via `adb`.

## What it owns

- Device inventory (symbolic names → platform UUIDs)
- Device state snapshots (battery, charging, thermal, foreground app)
- Session-aware test-run orchestration (`spyder run --` wraps the
  test command under an auto-acquired device reservation)
- A bundled `ios` tunnel daemon (the go-ios CLI, spawned as a child
  process in `--userspace` mode) — provides the iOS-17+ RSD endpoint
  registry that the in-process iOS adapter queries
- Stream relay, pairing, dashboard, and the **spyder player**
  (`player/` → `bin/player`) — stream glass for headless game servers.
  **Transport note (2026-07-19): H.264 is deprecated in place.** The
  command-stream transport (ge SP2S, ge 🎯T128) is the default on both
  sides — the player replays sokol ops on its local GPU. The H.264
  decode path stays working but frozen (players are dev tools only,
  never store-packaged, so removal buys little): do not extend it; new
  stream/glass work targets SP2S replay (see 🎯T106 for the browser
  glass). The relay is transport-opaque either way.

## What it does NOT own

- UI automation (tap/swipe/type/UI tree) — that's mobile-mcp
- xcodebuild invocations — that's XcodeBuildMCP
- iOS protocol implementations — that's go-ios (vendored as a Go
  module dependency); spyder is just its consumer
- Simulator control on macOS — that's `xcrun simctl` (Apple)
- Android protocol — that's `adb` (Google)
- Game engine / direct-mode rendering — that's ge (squz/ge). **ge and
  spyder couple only through protocols** (app channel + stream wire),
  not by linking each other's libraries.

## Build & Run

```bash
make build
make player                           # stream glass → bin/player (self-contained)
bin/spyder run -- xcodebuild test ... # wrapper: runs cmd under device reservation
bin/player --host localhost --port 3030 --name tiltbuggy

# Register with Claude Code (points at the brew daemon):
claude mcp add --scope user --transport http spyder http://localhost:3030/mcp
```

### One daemon only — do not double-serve

On this Mac, day-to-day MCP is **supervisord** running the Homebrew
Cellar binary (not `brew services`). Launchd and supervisord must not
both own `:3030`.

```bash
./supervisor/install.sh       # once: stop brew services, start under supervisord
supervisorctl status spyder
spyder version                # e.g. v0.84.0 — MCP initialize must match
```

**Never** run `bin/spyder serve` (or any tree-built binary) on `:3030`
while the supervised daemon is running. Two processes can both bind
(IPv4 vs IPv6/`*`), clients flip between them, and you get mismatched
versions (`v0.x` vs `dev`).

| Goal | Do this |
|------|---------|
| Normal agent / MCP work | `supervisorctl start spyder` — leave it alone. `brew services stop spyder` must stay stopped |
| After `brew upgrade spyder` | `brew services stop spyder` (stay unloaded), then `supervisorctl restart spyder` — **not** `brew services restart` |
| Test unreleased tree code | keep supervisor on `:3030`; `bin/spyder serve --addr :3131` |
| Sanity check | `lsof -nP -iTCP:3030 -sTCP:LISTEN` — exactly **one** spyder, Cellar path; `supervisorctl status spyder` is RUNNING |

After any local serve experiment, stop the tree binary. Do not `brew
services start spyder` — that reloads launchd and fights supervisord.

## Architecture

- **main.go** — single entrypoint. Subcommands: `serve` (HTTP MCP + REST
  server), `run` (test-wrapper), `doctor`, `status`, `version`,
  `help-agent`, plus device-tool CLI proxies that POST to the daemon
  (`devices`, `screenshot`, `launch-player`, … — see the usage string).
- **internal/daemon** — wires `github.com/mark3labs/mcp-go`'s
  `MCPServer` and `StreamableHTTPServer` with spyder's tool handlers.
  Spawns the bundled `ios` tunnel as a child process at startup;
  reaps it on shutdown.
- **internal/iostunnel** — supervisor for the `ios tunnel start
  --userspace` subprocess.
- **internal/goios** — per-UDID session helper around go-ios:
  walks tunnel-info → RSD-handshake → enriched DeviceEntry once,
  caches the result, hands callers a populated DeviceEntry that
  go-ios's instruments / installationproxy / appservice / syslog
  packages expect.
- **internal/mcp** — `Handler` + `Definitions()`. `Definitions()`
  advertises a single MCP tool, `app_exec` (🎯T88). `toolHandlers()`
  is the verb table (REST `POST /api/v1/<verb>` and Starlark builtins).
- **internal/device** — `Adapter` interface; `ios.go`, `android.go`,
  and `desktop.go` implementations. iOS uses go-ios as a Go module
  dependency (`installationproxy`, `instruments`, `appservice`,
  `syslog`, `crashreport`, `zipconduit`); Android shells out to `adb`;
  desktop execs a local binary.
- **internal/inventory** — symbolic name resolution, JSON-backed
  (iOS / Android / desktop entries).
- **internal/paths** — `~/.spyder/` path conventions.

## Device Inventory Format

JSON array at `~/.spyder/inventory.json`:

```json
[
  {
    "alias": "iPad",
    "platform": "ios",
    "ios_uuid": "00008103-001122334455667A",
    "ios_coredevice": "00000000-0000-0000-0000-000000000001",
    "notes": "Preferred iPad test device"
  }
]
```

`ios_uuid` — from `ios list` (go-ios) or `xcrun xctrace list devices`.
`ios_coredevice` — from `devicectl list devices` (iOS 17+).

## Convention Notes

- Apache 2.0, short-form SPDX headers on every .go file.
- Go 1.26.1, `go.mod` at repo root (flat layout — no nested `go/` subdir).
- `~/.spyder/` holds runtime state (inventory).
- MCP advertises `app_exec` only (clients see a server-prefixed name
  such as `mcp__spyder__app_exec`). REST and Starlark use unprefixed
  verb names from `toolHandlers()` (`devices`, not `spyder_devices`).

## Testing

```bash
go test ./...
```

**Tests run on the laptop, not in CI.** spyder's value surface (real
iOS/Android devices via go-ios + `adb`, the bundled tunnel daemon's
RSD path, on-device DTX) can't be reproduced in any hosted CI runner.
There is no hosted CI.

Instead, the laptop is the test runner and `TEST-REPORT.json` at the
repo root is the attestation:

- `scripts/test-report.sh` (invoked via `make test-report`) runs every
  tier on a clean tree and writes `TEST-REPORT.json`. Tiers:
  1. `go-unit` — `go test ./...`
  2. `live` — `go test -run '_Live$' ./internal/device/...` (gated on
     `SPYDER_LIVE_UDID=<udid>`; requires a paired iOS device and the
     bundled `ios tunnel start --userspace` running, which spyder
     spawns automatically)
- The report is an attestation — *the engineer ran these tests, here
  are the per-tier outcomes*. Keeping it up to date relative to the
  code is the engineer's responsibility. There is no automated
  freshness check (the previous SHA-based one was removed; a better
  mechanism is TBD).
- HIL tiers (`integration`, `device`) skip routinely; `overall:
  partial` is acceptable.

Before pushing to `master` or cutting a release: `TEST-REPORT.json`
should reflect a recent run with `overall ∈ {pass, partial}`.
Run `make bullseye` during development and `make pre-release` before
tagging.

## Delivery

Land on `master` by direct push — no PR ceremony, no feature branch
requirement. Long-lived topic branches are fine for WIP, but shipping is
`git push origin master` (or `/push`, which skips PR creation here).

Releases are local (vellum/tapper shape): `VERSION=0.N.0 make release`
runs `pre-release`, packages the darwin-arm64 tarball (codesigned
spyder + bundled ios + spyder-killusbmuxd), `gh release create`s it,
and `tapper push`es the Homebrew formula. After `brew upgrade spyder`,
restart with `supervisorctl restart spyder` — not `brew services
restart`. Day-to-day MCP is the Homebrew Cellar binary under
supervisord (see **One daemon only** above).

## Gates

profile: base
override:
  - pr-workflow: skip

Direct push to `master` is the default delivery path. `/push` must not
open a PR for this repo unless the user explicitly asks for one on a
non-default branch.

## Plateau

Current product plateau is **Plateau P** — see [docs/plateau-p.md](docs/plateau-p.md).
ge is the engine; spyder is the sole control plane. `ged` is gone.
