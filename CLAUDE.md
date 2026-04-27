# spyder

Cross-platform mobile development workflow assistant. HTTP-based MCP
server (Go) that sits above mobile-mcp and XcodeBuildMCP as the
session-state layer — device inventory, wake-state, and prep/run/restore
orchestration around real-device tests. iOS physical devices are a
first-class citizen (via `pymobiledevice3` + CoreDevice, where
mobile-mcp struggles); Android is supported via `adb`.

## What it owns

- Device inventory (symbolic names → platform UUIDs)
- Device state snapshots (battery, charging, thermal, foreground app)
- Power-assertion management via the bundled pmd3 bridge (prevents auto-lock)
- Session-aware test-run orchestration (`spyder run --` wraps the
  test command under an auto-acquired device reservation)

## What it does NOT own

- UI automation (tap/swipe/type/UI tree) — that's mobile-mcp
- xcodebuild invocations — that's XcodeBuildMCP
- Raw device protocols — spyder shells out to `pymobiledevice3` /
  `devicectl` / `adb`, it doesn't reimplement them

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
- **internal/mcp** — `Handler` + `Definitions()`. Dispatches tool calls.
- **internal/device** — `Adapter` interface; `ios.go` and `android.go`
  implementations. iOS shells out to `pymobiledevice3` + `devicectl`;
  Android shells out to `adb`.
- **internal/inventory** — symbolic name resolution, JSON-backed.
- **internal/paths** — `~/.spyder/` path conventions.

## Device Inventory Format

JSON array at `~/.spyder/inventory.json`:

```json
[
  {
    "alias": "Pippa",
    "platform": "ios",
    "ios_uuid": "00008103-000D39301A6A201E",
    "ios_coredevice": "E1A01EA6-8D77-556C-B18D-D470B2909E87",
    "notes": "Preferred iPad test device"
  }
]
```

`ios_uuid` — from `pymobiledevice3 usbmux list` or `xcrun xctrace list devices`.
`ios_coredevice` — from `devicectl list devices` (iOS 17+).

## Convention Notes

- Apache 2.0, short-form SPDX headers on every .go file.
- Go 1.26.1, `go.mod` at repo root (flat layout — no nested `go/` subdir).
- `~/.spyder/` holds runtime state (inventory).
- Tool names are unprefixed (`devices`, not `spyder_devices`); MCP clients
  add the server-name prefix at their end.

## KeepAwake versioning

**Whenever `ios/KeepAwake/Sources/` changes, bump
`MARKETING_VERSION` in `ios/KeepAwake/KeepAwake.xcodeproj/project.pbxproj`
in BOTH the Debug and Release `buildSettings` blocks.** This is
the only signal autoawake has to detect that the on-device build
is stale and trigger a redeploy (uninstall → rebuild → reinstall →
relaunch on the next convergence tick). Without a bump, source
changes sit in the repo with no path out to existing devices.

- PATCH bump for behaviour-preserving tweaks (drift speed, colours).
- MINOR bump for behavioural changes (new lifecycle hook, new
  exit condition).
- Independent of spyder's release version — the iOS app's version
  is its own.
- The string is opaque to spyder: semver, semver-with-suffix
  (`0.2.0-rc1`), date-based (`2026.04.27`) all work.

## TODO

See [docs/TODO.md](docs/TODO.md).

## Testing

```bash
go test ./...
```

## Delivery

Merged to master via squash PR. Squash-only merges configured on the repo.

## Gates

Default (base) gates apply.
