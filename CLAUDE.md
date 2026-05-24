# spyder

Cross-platform mobile development workflow assistant. HTTP-based MCP
server (Go) that sits above mobile-mcp and XcodeBuildMCP as the
session-state layer — device inventory, wake-state, and prep/run/restore
orchestration around real-device tests. iOS physical devices are a
first-class citizen: app lifecycle and enumeration go through Apple's
CoreDevice (`xcrun devicectl`), which bypasses usbmuxd; the DTX-only
surface (screenshots, oslog stream, crashes, battery) uses the bundled
[go-ios](https://github.com/danielpaulus/go-ios) Go library (usbmux,
lockdown, DTX, RSD, in-process) and degrades gracefully when usbmuxd is
wedged. This is where mobile-mcp's WDA path often fails. Android is
supported via `adb`.

## What it owns

- Device inventory (symbolic names → platform UUIDs)
- Device state snapshots (battery, charging, thermal, foreground app)
- Session-aware test-run orchestration (`spyder run --` wraps the
  test command under an auto-acquired device reservation)
- A bundled `ios` tunnel daemon (the go-ios CLI, spawned as a child
  process in `--userspace` mode) — provides the iOS-17+ RSD endpoint
  registry that the in-process iOS adapter queries

## What it does NOT own

- UI automation (tap/swipe/type/UI tree) — that's mobile-mcp
- xcodebuild invocations — that's XcodeBuildMCP
- iOS protocol implementations — that's go-ios (vendored as a Go
  module dependency); spyder is just its consumer
- Simulator control on macOS — that's `xcrun simctl` (Apple)
- Android protocol — that's `adb` (Google)

## Build & Run

```bash
make build
bin/spyder serve                      # HTTP MCP server on :3030, endpoint /mcp
bin/spyder serve --addr :3131         # custom addr
bin/spyder run -- xcodebuild test ... # wrapper: runs cmd under device reservation

# Register with Claude Code:
claude mcp add --scope user --transport http spyder http://localhost:3030/mcp
```

## Architecture

- **main.go** — single entrypoint. Subcommands: `serve` (HTTP MCP
  server), `run` (test-wrapper), `version`.
- **internal/daemon** — wires `github.com/mark3labs/mcp-go`'s
  `MCPServer` and `StreamableHTTPServer` with spyder's tool handlers.
  Spawns the bundled `ios` tunnel as a child process at startup;
  reaps it on shutdown.
- **internal/iostunnel** — supervisor for the `ios tunnel start
  --userspace` subprocess.
- **internal/devicectl** — typed Go wrappers over `xcrun devicectl`
  (CoreDevice). The iOS adapter's primary path for app lifecycle
  (install/uninstall/launch/terminate/pid), app listing, executable
  resolution, device enumeration and device details. CoreDevice
  bypasses usbmuxd entirely, so these keep working when usbmuxd is
  wedged (🎯T72). Imports nothing from go-ios.
- **internal/goios** — per-UDID session helper around go-ios:
  walks tunnel-info → RSD-handshake → enriched DeviceEntry once,
  caches the result (resolution bounded by a 15s timeout so a wedged
  tunnel degrades rather than hangs), hands callers a populated
  DeviceEntry that go-ios's instruments / syslog / crashreport
  packages expect. Now used only for the DTX-only surface.
- **internal/mcp** — `Handler` + `Definitions()`. Dispatches tool calls.
- **internal/device** — `Adapter` interface; `ios.go` and `android.go`
  implementations. iOS talks to devices through two channels:
  **CoreDevice via internal/devicectl** for app lifecycle and
  enumeration (usbmuxd-free), and **in-process go-ios** for the
  DTX-only surface CoreDevice has no equivalent for on iOS 17+ —
  screenshots, the os_trace log stream, crash-report retrieval, and
  battery state. When usbmuxd is wedged the devicectl tools keep
  working and the DTX tools return a structured `ErrUSBMuxdUnavailable`
  rather than hanging. Android shells out to `adb`.
- **internal/inventory** — symbolic name resolution, JSON-backed.
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
- Tool names are unprefixed (`devices`, not `spyder_devices`); MCP clients
  add the server-name prefix at their end.

## TODO

See [docs/TODO.md](docs/TODO.md).

## Testing

```bash
go test ./...
```

**Tests run on the laptop, not in CI.** spyder's value surface (real
iOS/Android devices via go-ios + `adb`, the bundled tunnel daemon's
RSD path, on-device DTX) can't be reproduced in
any hosted CI runner. The only GitHub Actions workflow is
`release.yml`, which builds + packages on tag push; there is no
per-PR CI.

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
  freshness check (the previous SHA-based one was removed because it
  fought squash-merge; a better mechanism is TBD).
- HIL tiers (`integration`, `device`) skip routinely; `overall:
  partial` is acceptable.

When evaluating a PR's mergeability: `TEST-REPORT.json` should
reflect a recent run with `overall ∈ {pass, partial}`. If it
doesn't, the engineer hasn't done their job; reject on that basis.

## Delivery

Merged to master via squash PR. Squash-only merges configured on the repo.

## Gates

Default (base) gates apply.
