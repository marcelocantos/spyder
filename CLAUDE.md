# spyder

Cross-platform mobile development workflow assistant. Bimodal MCP server
(Go) that sits above mobile-mcp and XcodeBuildMCP as the session-state
layer — device inventory, wake-state, and prep/run/restore orchestration
around real-device tests. iOS-first; Android stubbed.

## What it owns

- Device inventory (symbolic names → platform UUIDs)
- Device state snapshots (battery, thermal, charging, foreground app)
- KeepAwake companion app lifecycle (keeps devices awake while plugged in)
- Session-aware test-run orchestration (deferred — see 🎯 targets)

## What it does NOT own

- UI automation (that's mobile-mcp)
- xcodebuild invocations (that's XcodeBuildMCP)
- Raw device ops — spyder wraps `pymobiledevice3` / `devicectl` / `adb`,
  it doesn't reimplement them

## Build & Run

```bash
make build
bin/spyder serve                        # daemon on ~/.spyder/spyder.sock
bin/spyder                              # stdio MCP proxy (auto-starts daemon)
claude mcp add --scope user spyder -- spyder
```

## Architecture

- **main.go** — bimodal entrypoint. No args → stdio proxy (auto-starts
  daemon). `serve` → persistent daemon. Pattern follows sawmill.
- **internal/daemon** — wires `mcpbridge.NewServer` with spyder's
  `ToolHandler` and tool definitions.
- **internal/mcp** — `Handler` + `Definitions()`. Dispatches tool calls.
- **internal/device** — `Adapter` interface; `ios.go` and `android.go`
  implementations. iOS shells out to `pymobiledevice3` + `devicectl`;
  Android is a stub.
- **internal/inventory** — symbolic name resolution, JSON-backed.
- **internal/paths** — `~/.spyder/` path conventions.
- **ios/KeepAwake** — minimal SwiftUI app that sets
  `UIApplication.isIdleTimerDisabled = true`. Pending.

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
- `~/.spyder/` holds all runtime state (socket, inventory).
- Tool names are unprefixed (`devices`, not `spyder_devices`); MCP clients
  add the server-name prefix at their end.

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
