# STABILITY.md

Spyder is pre-1.0. This document catalogues the public interaction surface and
tracks the state of each piece relative to a future 1.0 lock-in.

## Stability commitment

At 1.0, spyder commits to backwards compatibility for:

- The **MCP tool surface** (names, input schemas, output shapes).
- The **CLI subcommand surface** (`spyder serve`, `spyder run`, `spyder
  version`, `spyder help-agent`, plus the device-tool subcommands
  listed below, flag names, exit codes).
- The **inventory file format** (`~/.spyder/inventory.json`).
- The **HTTP MCP endpoint** (`/mcp`, port default, streamable-HTTP transport).
- The **REST endpoint** (`/api/v1/<tool>` POST + JSON, same listener).

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
| `runs_list` | (no args). | JSON array of `runs.Run` records (id, device, owner, note, created_at, closed_at?, artefacts?), newest first. | Needs review — field additions expected as more artefact-producing tools land |
| `runs_show` | `{run_id: string}`. | JSON-encoded `runs.Run` with full artefact list. | Needs review — same caveat as `runs_list` |
| `rotate` | `{device: string, orientation: string, owner?: string}` (device and orientation required). Orientation: `portrait`, `landscape-left`, `landscape-right`, `portrait-upside-down`. | Text confirmation. | Needs review — simulator/emulator-only; physical device error wording may evolve |
| `baseline_update` | `{suite, case, variant?, screenshot_path?, screenshot_base64?, manifest?}`. One of screenshot_path/base64 required. | Text confirmation. | Needs review — variant convention and manifest schema v1 may gain fields |
| `diff` | `{suite, case, variant?, screenshot_path?, screenshot_base64?, manifest?, pixel_tolerance?, owner?, device?}`. | JSON-encoded `visualdiff.Report`. | Needs review — SSIM stubbed (NaN); VLM interface unimplemented; report shape expected to gain fields |
| `baselines_list` | `{suite: string}`. | JSON array of `{case, variant, has_png, has_manifest}`. | Needs review |
| `sim_list` | `{state?: string}`. | JSON array of `simemu.SimDevice` (`udid`, `name`, `state`, `runtimeID`). | Needs review |
| `sim_create` | `{name: string, device_type_id: string, runtime_id: string}`. | JSON `{udid, name}`. | Needs review |
| `sim_boot` | `{udid: string}`. | Text confirmation. | Needs review |
| `sim_shutdown` | `{udid: string}`. | Text confirmation. | Needs review |
| `sim_delete` | `{udid: string}`. | Text confirmation. | Needs review |
| `emu_list` | (no args). | JSON array of `simemu.AVD` (`name`, `path?`, `target?`, `abi?`). | Needs review |
| `emu_create` | `{name: string, system_image: string, device_profile: string}`. | Text confirmation. | Needs review |
| `emu_boot` | `{name: string}`. | Text (serial visible in `adb devices` once booted). | Needs review |
| `emu_shutdown` | `{serial: string}`. | Text confirmation. | Needs review |
| `emu_delete` | `{name: string}`. | Text confirmation. | Needs review |
| `record_start` | `{device: string, owner?: string}` (device required; owner for reservation auth). | Text confirmation with subprocess PID and output path. | Needs review — iOS simulator UDID must be passed directly; iOS physical devices return an immediate error. |
| `record_stop` | `{device: string, owner?: string}` (device required; owner for reservation auth). | Text confirmation with the local mp4 path. | Needs review |

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
| `spyder devices [--platform ios\|android\|all] [--json]` | REST proxy to `devices` tool. | Stable |
| `spyder resolve <name> [--json]` | REST proxy to `resolve` tool. | Stable |
| `spyder device-state <device> [--json]` | REST proxy to `device_state` tool. | Stable |
| `spyder screenshot <device> [--output FILE] [--as OWNER]` | REST proxy to `screenshot`; writes PNG to `--output` (default `<device>-<ts>.png`). | Stable |
| `spyder keepawake <device> [--as OWNER]` | REST proxy to `keepawake`. | Stable |
| `spyder list-apps <device> [--json]` | REST proxy to `list_apps`. | Stable |
| `spyder launch-app <device> <bundle-id> [--as OWNER]` | REST proxy to `launch_app`. | Stable |
| `spyder terminate-app <device> <bundle-id> [--as OWNER]` | REST proxy to `terminate_app`. | Stable |
| `spyder reserve <device> [--as OWNER] [--ttl SECONDS] [--note TEXT]` | REST proxy to `reserve`. | Stable |
| `spyder release <device> [--as OWNER]` | REST proxy to `release`. | Stable |
| `spyder renew <device> [--as OWNER] [--ttl SECONDS]` | REST proxy to `renew`. | Stable |
| `spyder reservations [--json]` | REST proxy to `reservations`. | Stable |
| `spyder runs list [--json]` | REST proxy to `runs_list`. | Needs review |
| `spyder runs show <run-id> [--json]` | REST proxy to `runs_show`. | Needs review |
| `spyder runs artefacts <run-id> [--json]` | REST proxy to `runs_show`; prints just the artefacts table. | Needs review |
| `spyder rotate <device> --to <orientation> [--as OWNER]` | REST proxy to `rotate`. Orientation: `portrait`, `landscape-left`, `landscape-right`, `portrait-upside-down`. | Needs review |
| `spyder diff <suite>/<case> <screenshot> [<manifest>] [--variant V] [--tolerance F] [--json]` | REST proxy to `diff`. Exits 0 on pass, 1 on fail. | Needs review |
| `spyder baseline update <suite>/<case> <screenshot> [<manifest>] [--variant V]` | REST proxy to `baseline_update`. | Needs review |
| `spyder sim list [--state STATE] [--json]` | REST proxy to `sim_list`. | Needs review |
| `spyder sim create <name> --type <id> --runtime <id>` | REST proxy to `sim_create`. | Needs review |
| `spyder sim boot <udid>` | REST proxy to `sim_boot`. | Needs review |
| `spyder sim shutdown <udid>` | REST proxy to `sim_shutdown`. | Needs review |
| `spyder sim delete <udid>` | REST proxy to `sim_delete`. | Needs review |
| `spyder emu list [--json]` | REST proxy to `emu_list`. | Needs review |
| `spyder emu create <name> --image <pkg> --device <profile>` | REST proxy to `emu_create`. | Needs review |
| `spyder emu boot <name>` | REST proxy to `emu_boot`. | Needs review |
| `spyder emu shutdown <serial>` | REST proxy to `emu_shutdown`. | Needs review |
| `spyder emu delete <name>` | REST proxy to `emu_delete`. | Needs review |
| `spyder record <device> --start \| --stop [--as OWNER]` | REST proxy to `record_start` / `record_stop`. Starts or stops a screen recording on an iOS simulator or Android device. | Needs review |

All device-tool subcommands POST to `$SPYDER_DAEMON_URL` (default
`http://127.0.0.1:3030`) and print the first text content block
(text tools) or write the first image content block to disk
(`screenshot`). `--as OWNER` defaults to `filepath.Base(cwd)`.

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

### REST endpoint

- Address: same listener as `/mcp`.
- Path: `POST /api/v1/<tool>`.
- Request: JSON object of the tool's arguments (same as MCP). Empty body
  allowed for zero-arg tools.
- Response: JSON-encoded `mcp.CallToolResult`
  (`{"content":[{"type":"text","text":"…"} | {"type":"image","data":"…","mimeType":"…"}], "isError":bool}`).
- Errors: `404` unknown tool; `405` non-POST; `400` bad JSON body.
  Tool-level errors (missing args, conflicts, etc.) return `200` with
  `isError:true` in the body — transport success, tool failure.
- Reservation state is shared with the MCP transport: a lock taken via
  one channel is honoured on the other.

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

### Run-artefact store

Path: `~/.spyder/runs/<run-id>/`. Each reservation opens one run
directory; artefact-producing tools (currently `screenshot`) write into
it, and `manifest.json` enumerates every file. `release` stamps
`closed_at` on the manifest.

```
~/.spyder/runs/
  20260419-143022-a3f1b2/
    manifest.json
    screenshot-20260419-143025.png
```

Retention is enforced on daemon startup. Configurable via environment:

- `SPYDER_RUNS_MAX_AGE_DAYS` (default `30`; `0` disables). Closed runs
  older than this are deleted.
- `SPYDER_RUNS_MAX_SIZE_GB` (default `20`; `0` disables). Measures the
  sum of `artefacts[].size` from each run's manifest; deletes oldest
  closed runs until total ≤ cap.

Open runs are never pruned — they represent in-flight reservations.
**Needs review** — schema may gain fields as more artefact tools land.

### Baseline store

Path: `~/.spyder/baselines/<suite>/<variant>/<case>.{png,manifest.json}`.

A variant key encodes per-device / per-orientation context as a URL-safe
string, e.g. `pippa-landscape`. The store is opaque to the key's content.
Writes are atomic (write-to-temp then rename).

Diff report shape (`visualdiff.Report`):

```json
{
  "tier": "manifest+pixel | manifest | pixel",
  "pixel": {
    "rms_error": 0.003,
    "ssim_score": "NaN (stubbed in v1)",
    "ssim_note": "not implemented in v1",
    "size_mismatch": false,
    "width": 390,
    "height": 844
  },
  "manifest_diff": {
    "added": ["id/of/new/element"],
    "removed": [],
    "moved": [{"id": "…", "from": [x,y,w,h], "to": [x,y,w,h]}],
    "attr_changed": [{"id": "…", "from": {}, "to": {}}],
    "kind_changed": [],
    "unchanged": 12
  },
  "regions": [{"label": "added:…", "bbox": [x,y,w,h]}],
  "vlm_summary": "",
  "pass": true,
  "pixel_tolerance": 0.01
}
```

**Needs review** — SSIM is stubbed (NaN); VLM interface defined but unimplemented;
manifest schema v1 may gain fields in later tiers.

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
- Screen-recording on **iOS physical devices** — `pymobiledevice3` and
  `devicectl` do not expose a clean CLI path for video capture on real
  devices at this time. `record_start` on a physical iOS device returns an
  immediate error: `"screen recording is not supported on iOS physical
  devices; use a simulator"`. Use `xcrun simctl list devices` to pick a
  simulator UDID instead.
- Wireless-ADB pairing / discovery — assumed set up externally (spyder
  inherits `adb devices`).
- Auto-install of an Android KeepAwake app — Android handles stay-awake
  natively via Developer Settings; the MCP tool is a no-op by design.
