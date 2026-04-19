# STABILITY.md

Spyder is pre-1.0. This document catalogues the public interaction surface and
tracks the state of each piece relative to a future 1.0 lock-in.

## Stability commitment

At 1.0, spyder commits to backwards compatibility for:

- The **MCP tool surface** (names, input schemas, output shapes).
- The **CLI subcommand surface** (`spyder serve`, `spyder run`, `spyder
  version`, `spyder help-agent`, flag names, exit codes).
- The **inventory file format** (`~/.spyder/inventory.json`).
- The **HTTP MCP endpoint** (`/mcp`, port default, streamable-HTTP transport).

Breaking changes to any of these after 1.0 require a major version bump (or,
per the project's policy, a fork into a new product). The pre-1.0 period
exists to get these right.

Snapshot as of `v0.4.0`.

## Interaction surface catalogue

### MCP tools

| Tool | Input schema | Output | Stability |
|---|---|---|---|
| `devices` | `{platform?: "ios"\|"android"\|"all"}` (default `all`). | JSON array of `device.Info` (`uuid`, `name`, `platform`, `model`, `os`, `alias`). When `platform=all` and an adapter errors, wraps as `{devices: [...], errors: [...]}`. | Stable |
| `resolve` | `{name: string}` (required). | JSON-encoded `inventory.Entry` (`alias`, `platform`, `ios_uuid`, `ios_coredevice`, `android_serial`, `notes`). | Needs review — passthrough shape for unknown IDs may evolve |
| `device_state` | `{device: string}` (required; alias or raw UUID/serial). | JSON-encoded `device.State` (`battery_level?`, `charging?`, `thermal_state?`, `foreground_app?`, `storage_free_mb?`, `notes?`). | Needs review — pointer-typed optionals, field additions expected |
| `screenshot` | `{device: string, owner?: string}` (device required; owner for reservation auth). | MCP image content block (base64 PNG, `image/png`). | Stable |
| `keepawake` | `{device: string, owner?: string}` (device required; owner for reservation auth). | Text result; platform-specific wording for Android no-op. | Stable |
| `list_apps` | `{device: string}` (required). | JSON array of `device.AppInfo` (`bundle_id`, `name?`, `version?`). | Needs review — Android currently returns bundle_id only; name/version parity pending |
| `launch_app` | `{device: string, bundle_id: string, owner?: string}` (device and bundle_id required; owner for reservation auth). | Text confirmation. | Stable |
| `terminate_app` | `{device: string, bundle_id: string, owner?: string}` (device and bundle_id required; owner for reservation auth). | Text confirmation. | Stable |
| `reserve` | `{device: string, owner: string, ttl_seconds?: number, note?: string}` (device and owner required). | JSON-encoded `reservations.Reservation` (device, owner, expires_at, note, created_at). | Stable |
| `release` | `{device: string, owner: string}`. | Text confirmation. | Stable |
| `renew` | `{device: string, owner: string, ttl_seconds?: number}`. | JSON-encoded `reservations.Reservation` with refreshed expires_at. | Stable |
| `reservations` | (no args). | JSON array of active `Reservation` records. | Stable |

Error classification is part of the contract: `device not connected`, `app
not installed`, `app not running`, `'Locked'`, `'Security'` (trust), and
`tunneld unavailable` are all surfaced as distinct tool-error text. Callers
can match on these phrases.

### CLI subcommands

| Invocation | Behaviour | Stability |
|---|---|---|
| `spyder` (no args) | Prints usage to stdout. | Stable |
| `spyder serve [--addr :PORT] [--tunneld-addr HOST:PORT]` | HTTP MCP server + auto-awake supervisor. Blocks until SIGINT/SIGTERM. | Stable |
| `spyder run [--device ALIAS\|-d ALIAS] [--as OWNER] -- <cmd> [args...]` | Runs command under an auto-acquired reservation (owner defaults to `filepath.Base(cwd)`); foregrounds KeepAwake and releases reservation on exit; opportunistically renews during long runs. Forwards exit code. | Stable |
| `spyder version` / `--version` / `-version` | Prints `spyder <tag>`. | Stable |
| `spyder help` / `--help` / `-help` | Prints usage. | Stable |
| `spyder help-agent` / `--help-agent` / `-help-agent` | Usage + embedded agents-guide.md. | Stable |

Exit codes: `0` success; `1` server startup / unclassified child-process error;
`2` argument parsing error; `3` reservation conflict at `spyder run`
startup (device held by another owner); the child command's own exit
code for `spyder run` when the command itself exits non-zero.

### HTTP MCP endpoint

- Address: `127.0.0.1:3030` (default, loopback only; overridable via `--addr`).
- Path: `/mcp`.
- Transport: mcp-go's streamable HTTP (JSON-RPC over POST; `Mcp-Session-Id`
  header for session continuity).
- Server info: `{name: "spyder", version: "<tag>"}`.

### Reservation file

Path: `~/.spyder/reservations.json`. Schema: JSON array of
`reservations.Reservation` records with atomic-rename writes so
concurrent writers (the daemon and `spyder run`) don't corrupt state.

```json
{
  "device": "string (canonical, alias if known)",
  "owner": "string",
  "expires_at": "RFC3339 timestamp",
  "note": "string (optional)",
  "created_at": "RFC3339 timestamp"
}
```

Expired entries pruned on load and on any access. Default TTL 3600 s;
max TTL 86400 s. **Stable.**

### Inventory file

Path: `~/.spyder/inventory.json`. Schema: JSON array of `inventory.Entry`
records:

```json
{
  "alias": "string",
  "platform": "ios|android",
  "ios_uuid": "string (optional)",
  "ios_coredevice": "string (optional)",
  "android_serial": "string (optional)",
  "notes": "string (optional)"
}
```

Missing file is treated as empty, not an error. Alias lookup is
case-insensitive. **Stable.**

### Version macro

Package-level variable `version` in `main.go`, injected at build time via
`-ldflags "-X main.version=<tag>"`. Defaults to `"dev"` for non-release
builds. **Stable.**

## Gaps and prerequisites for 1.0

- **iOS foreground-app detection.** Currently a note on `device_state`
  outputs ("foreground app detection pending"). pymobiledevice3 DVT surface
  doesn't expose this cleanly; needs investigation. Blocks full
  `device_state` parity.
- **iOS thermal state.** `thermal_state` is always empty on iOS 17.4+
  because MobileGestalt was deprecated. Alternative source (`dumpsys
  thermalservice` analog? `sysmon`?) is open research.
- **Android app metadata.** `list_apps` on Android returns bundle IDs only.
  Name/version parity via per-package `dumpsys` is feasible but deferred.
- **Android thermal state.** Not yet wired — `dumpsys thermalservice` is
  available, just not parsed.
- **Tunneld child-process supervision.** `--supervise-tunneld` was specified
  in 🎯T7 but deferred. Currently spyder assumes an externally-managed
  tunneld. Blocks turnkey installs where no external manager exists.
- **KeepAwake auto-deploy portability.** Project discovery currently walks
  up from CWD. For a Homebrew-installed binary with no source tree nearby,
  auto-deploy silently disables. Long-term fix is `go:embed` of the Xcode
  project + extraction on first use.
- **Tests for shell-out paths.** 105 test functions cover all pure
  logic (inventory, parsers, classifiers, MCP dispatch, reservations,
  daemon HTTP roundtrip). Shell-out orchestration in `internal/device`
  (adapter methods wrapping `pymobiledevice3`/`adb`/`devicectl`),
  `internal/notify` (osascript/terminal-notifier/alerter),
  `main.restoreKeepAwake`, and the `internal/autoawake` supervisor
  loop is still ~20-30% covered. Before 1.0 these should gain
  env-gated live tests (e.g. `SPYDER_LIVE_TESTS=1`) against real
  devices so regressions in the real-world path get caught.
- **`pymobiledevice3` library embedding.** All iOS operations shell out,
  paying ~1 s of Python startup per call. Long-lived helper subprocess (JSON
  protocol over stdin/stdout) would eliminate this.
- **macOS-only host enforcement.** Spyder runs on Linux but iOS operations
  will fail noisily there. Either restrict the binary to Darwin or
  gracefully degrade iOS-related tools with a clear "host does not support
  iOS" error.
- **Rate-limiting / retry policy.** Auto-awake retries lock failures every
  10 s for up to 5 minutes. Not user-configurable. Fine for v0.x but should
  surface a knob before 1.0.
- **`HOMEBREW_TAP_TOKEN` per-repo setup.** Not a stability issue but worth
  noting: each new repo needs the token set, documented in the release
  skill.

## Out of scope for 1.0

- Windows host support.
- Full UI automation (tap/swipe/type) — that's deliberately mobile-mcp's
  territory.
- Screen-recording / video capture (no clean pymobiledevice3 primitive).
- Wireless-ADB pairing / discovery — assumed set up externally (spyder
  inherits `adb devices`).
- Auto-install of an Android KeepAwake app — Android handles stay-awake
  natively via Developer Settings; the MCP tool is a no-op by design.
