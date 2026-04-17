# spyder

Cross-platform mobile development workflow assistant — a bimodal MCP server
that owns device inventory, tracks session state, and orchestrates prep →
run → restore cycles around tests on real devices.

Not a replacement for
[mobile-mcp](https://github.com/mobile-next/mobile-mcp) (UI automation) or
[XcodeBuildMCP](https://github.com/getsentry/XcodeBuildMCP) (xcodebuild).
Spyder sits above them as the session-state layer — the cross-tool context
those single-shot surfaces don't carry.

## Status

iOS-first MVP. Android adapter is stubbed so the cross-platform seam exists
from day one, but the implementation is deferred.

## Build & Run

```bash
make build
bin/spyder serve                        # daemon on ~/.spyder/spyder.sock
bin/spyder                              # stdio MCP proxy (auto-starts daemon)
```

## Install as MCP server

```bash
claude mcp add --scope user spyder -- spyder
```

## MCP Tools

| Tool | What it does |
|---|---|
| `devices` | List connected iOS and Android devices with alias, platform, model. |
| `resolve` | Resolve a symbolic name (e.g. `Pippa`) to platform-specific UUIDs. |
| `keepawake` | Foreground the KeepAwake companion app on a device. |
| `device_state` | Report battery, thermal, charging, foreground app. |

## Device Inventory

JSON file at `~/.spyder/inventory.json`:

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

## Code Structure

```
spyder/
├── main.go                    bimodal entrypoint (stdio proxy ↔ daemon)
├── internal/
│   ├── paths/                 ~/.spyder/ path conventions
│   ├── daemon/                mcpbridge daemon wiring
│   ├── mcp/                   ToolHandler + tool definitions
│   ├── device/                cross-platform Adapter interface + iOS/Android
│   └── inventory/             symbolic name ↔ UUID resolution
├── ios/
│   └── KeepAwake/             companion Swift app (TBD)
└── docs/
    └── TODO.md
```

## Dependencies

- [`mcpbridge`](https://github.com/marcelocantos/mcpbridge) — daemon/proxy pattern
- [`mcp-go`](https://github.com/mark3labs/mcp-go) — MCP SDK
- `pymobiledevice3` (in PATH) — iOS introspection
- Xcode `devicectl` — iOS 17+ process launching

## Testing

```bash
go test ./...
```

## Delivery

Merged to master via squash PR.

## Licence

Apache 2.0 — see [LICENSE](LICENSE).
