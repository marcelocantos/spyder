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

Snapshot as of `v0.18.0`.

## Interaction surface catalogue

### MCP tools

| Tool | Input schema | Output | Stability |
|---|---|---|---|
| `devices` | `{platform?: "ios"\|"android"\|"all"}` (default `all`). | JSON array of `device.Info` (`uuid`, `name`, `platform`, `model`, `os`, `alias`). When `platform=all` and an adapter errors, wraps as `{devices: [...], errors: [...]}`. | Stable |
| `resolve` | `{name?: string, selector?: string}`. Exactly one of name (alias / raw UUID) or selector (JSON predicate, same grammar as `reserve`'s selector) required. | JSON-encoded `inventory.Entry` (`alias`, `platform`, `ios_uuid`, `ios_coredevice`, `android_serial`, `notes`). With selector, returns the entry of the first matching live device. | Needs review — passthrough shape for unknown IDs may evolve |
| `device_state` | `{device: string}` (required; alias or raw UUID/serial). | JSON-encoded `device.State` (`battery_level?`, `charging?`, `thermal_state?`, `foreground_app?`, `storage_free_mb?`, `notes?`). | Needs review — pointer-typed optionals, field additions expected |
| `screenshot` | `{device: string, owner?: string}` (device required; owner for reservation auth). | MCP image content block (base64 PNG, `image/png`). iOS 17+ devices (iOS 17, 18, 26+) route through pmd3's DVT Screenshot instrument over a tunneld-mediated RSD connection; requires an externally-managed `pymobiledevice3 remote tunneld` on the host (typically a launchd service). Pre-iOS-17 devices are not currently supported (legacy `com.apple.mobile.screenshotr` fallback not implemented). Surfaces `tunneld_unavailable` (HTTP 503) when tunneld is down or the device is not in the tunnel registry, and `developer_mode_disabled` (HTTP 412) when the device's Developer Mode toggle is off. | Stable |
| `list_apps` | `{device: string}` (required). | JSON array of `device.AppInfo` (`bundle_id`, `name?`, `version?`). | Needs review — Android currently returns bundle_id only; name/version parity pending |
| `launch_app` | `{device: string, bundle_id: string, owner?: string}` (device and bundle_id required; owner for reservation auth). | Text confirmation. | Stable |
| `is_running` | `{device: string, bundle_id: string}` (both required). | JSON `{state: "running"\|"not_running"\|"not_installed", pid?: number}`. Read-only; not subject to reservations. iOS uses pmd3 dvt process-id-for-bundle-id (requires tunneld); Android uses `adb shell pidof`. ListApps cross-check distinguishes not_running from not_installed. | Stable (🎯T38.1) |
| `terminate_app` | `{device: string, bundle_id: string, owner?: string}` (device and bundle_id required; owner for reservation auth). | Text confirmation. | Stable |
| `install_app` | `{device: string, path: string, owner?: string}` (device and path required). Path must not contain `..` and must exist. | Text confirmation. | Stable |
| `uninstall_app` | `{device: string, bundle_id: string, owner?: string}` (device and bundle_id required). | Text confirmation. | Stable |
| `deploy_app` | `{device: string, path: string, bundle_id?: string, owner?: string}` (device and path required). `bundle_id` derived from Info.plist (iOS) or `aapt dump badging` (Android) if omitted. | JSON `{bundle_id: string, pid: number}`. | Stable |
| `reserve` | `{device?: string, selector?: string, owner: string, ttl_seconds?: number, note?: string}`. Exactly one of device (literal pin) or selector (JSON predicate: platform, model_family?, os_min?, os_max?, orientation_capable?, tags?, attrs?) required. owner is always required. | JSON-encoded `reservations.Reservation` (device, owner, expires_at, note, created_at). | Needs review — selector grammar may evolve |
| `release` | `{device: string, owner: string}`. | Text confirmation. Applied network profiles cleared automatically. | Stable |
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
| `pool_list` | (no args). | JSON array of `pool.TemplateStatus` (`template`, `platform`, `available`, `running`, `reserved`, `instances[]`). Returns "pool not configured" error when `~/.spyder/pool.yaml` is absent. | Needs review |
| `pool_warm` | `{template: string, count: number}`. | Text confirmation. | Needs review |
| `pool_drain` | `{template: string}`. | Text confirmation. | Needs review |
| `record_start` | `{device: string, owner?: string}` (device required; owner for reservation auth). | Text confirmation with subprocess PID and output path. | Needs review — iOS simulator UDID must be passed directly; iOS physical devices return an immediate error. |
| `record_stop` | `{device: string, owner?: string}` (device required; owner for reservation auth). | Text confirmation with the local mp4 path. | Needs review |
| `network` | `{device: string, owner: string, profile?: string}` or `{device: string, owner: string, clear: true}`. Exactly one of profile or clear required. | Text confirmation. | Beta — Android emulator only; iOS and physical Android return clear errors. |
| `logs` | `{device: string, since?: RFC3339, until?: RFC3339, process?: string, subsystem?: string, tag?: string, regex?: string}` (device required). | JSON array of `device.LogLine` (`timestamp`, `process?`, `level?`, `tag?`, `message`). Empty array when no lines match. | Needs review — iOS range is live-window based (not true archived-log query); see *iOS log live-window contract* below. Field set and timestamp precision may evolve |

Error classification is part of the contract: `device not connected`, `app
not installed`, `app not running`, `'Locked'`, `'Security'` (trust),
`tunneld is not running on the host`, and `Developer Mode is not
enabled` are all surfaced as distinct tool-error text. Callers
can match on these phrases.

#### iOS log live-window contract (🎯T38.2)

`logs` and `log_stream` on iOS physical devices are **live-window only**:
they shell out to `pymobiledevice3 syslog live`, which streams lines as
they arrive on the device. There is no archived-log query mode — pmd3
does not expose `OsTraceService` or equivalent through a stable CLI
surface that spyder can consume reliably.

The hard rule callers can rely on:

- A `since` timestamp **older than the moment the live tail subscribes
  to the device** will silently miss lines that occurred before the
  subscription started.
- The live-tail subscription begins when the spyder daemon receives the
  `logs` request and pmd3's syslog reader connects (typically <1s).
- The collector caps the wait at 30s (or `until - now`, whichever is
  smaller) to bound query latency.

Practical consequence for callers:
- "Did this process log anything in the **last 5 minutes**?" cannot be
  answered against archived logs. The window starts now.
- For crash detection, prefer the `crashes` tool (which reads
  `~/Library/Logs/CrashReporter/MobileDevice/<device>/`-style aggregate
  storage on the host) over `logs`.
- For continuous monitoring, use `log --follow` (live SSE stream) and
  inspect lines as they arrive rather than retrospectively.

iOS simulators and Android devices do not share this constraint —
simulators read from the host's unified-log store via `xcrun simctl
spawn ... log`, and Android's `adb logcat` has its own ring buffer.

This contract may relax post-1.0 if pmd3 stabilises an OsTraceService
binding that spyder can wrap.

### CLI subcommands

| Invocation | Behaviour | Stability |
|---|---|---|
| `spyder` (no args) | Prints usage to stdout. | Stable |
| `spyder serve [--addr :PORT]` | HTTP MCP server + auto-awake supervisor + bundled pmd3 bridge. Blocks until SIGINT/SIGTERM. | Stable |
| `spyder run [--device ALIAS\|-d ALIAS\|--on PREDICATE] [--as OWNER] [--timeout DURATION] -- <cmd> [args...]` | Runs command under an auto-acquired reservation (owner defaults to `filepath.Base(cwd)`); releases reservation on exit; opportunistically renews during long runs. Forwards exit code. `--on PREDICATE` resolves+reserves atomically via the daemon (closing the resolve→release→re-acquire race window). `--timeout DURATION` (e.g. `5m`) bounds the wrapped child invocation; on deadline, exits 30 (`ExitTimeout`) instead of forwarding the child's signal-induced exit. `--device` and `--on` are mutually exclusive. | Stable (🎯T38.4 + 🎯T38.5) |
| `spyder version` / `--version` / `-version` | Prints `spyder <tag>`. | Stable |
| `spyder help` / `--help` / `-help` | Prints usage. | Stable |
| `spyder help-agent` / `--help-agent` / `-help-agent` | Usage + embedded agents-guide.md. | Stable |
| `spyder devices [--platform ios\|android\|all] [--json]` | REST proxy to `devices` tool. | Stable |
| `spyder resolve (<name>\|--on PREDICATE) [--json]` | REST proxy to `resolve` tool. Positional `<name>` is treated as an alias / raw UUID. `--on PREDICATE` (or a positional that contains `=`) is parsed as a selector predicate, resolved against live devices via the daemon, and the matched inventory entry is returned. Inputs that are neither a known alias nor a parseable predicate exit 15 (`ExitSelectorNotSupported`) — distinct from exit 1 for alias-known-but-resolution-failed. | Stable (🎯T38.3) |
| `spyder is-running <device> <bundle-id> [--json]` | REST proxy to `is_running`. Exits 0 (running, prints `running pid=<n>`), 20 (not installed), or 22 (installed but not running). `--json` emits the raw `{state, pid?}` body in addition to the exit code. | Stable (🎯T38.1) |
| `spyder device-state <device> [--json]` | REST proxy to `device_state` tool. | Stable |
| `spyder screenshot <device> [--output FILE] [--as OWNER]` | REST proxy to `screenshot`; writes PNG to `--output` (default `<device>-<ts>.png`). | Stable |
| `spyder list-apps <device> [--json]` | REST proxy to `list_apps`. | Stable |
| `spyder launch-app <device> <bundle-id> [--as OWNER]` | REST proxy to `launch_app`. | Stable |
| `spyder terminate-app <device> <bundle-id> [--as OWNER]` | REST proxy to `terminate_app`. | Stable |
| `spyder install <device> <path> [--as OWNER]` | REST proxy to `install_app`. | Stable |
| `spyder uninstall <device> <bundle-id> [--as OWNER]` | REST proxy to `uninstall_app`. | Stable |
| `spyder deploy <device> <path> [--bundle-id ID] [--as OWNER]` | REST proxy to `deploy_app`. Derives bundle id from Info.plist (iOS) or `aapt` (Android) when `--bundle-id` is omitted. | Stable |
| `spyder reserve (<device>\|--on PREDICATE\|--selector JSON\|--platform PLATFORM [--model FAMILY] [--tag TAG]...) [--as OWNER] [--ttl SECONDS] [--note TEXT]` | REST proxy to `reserve`. Positional device = literal pin. `--on PREDICATE` = comma-separated key=value selector grammar (see "CLI selector grammar" below). `--selector` = JSON predicate. Shorthand `--platform`/`--model`/`--tag` flags build the selector inline. | Stable (positional, --on, --selector); Needs review (shorthand --platform/--model/--tag — may consolidate around --on) |
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
| `spyder net <device> [--profile NAME\|--clear] [--as OWNER]` | REST proxy to `network`. Requires exactly one of `--profile` or `--clear`. | Beta — Android emulator only. |
| `spyder log <device> [--process P] [--subsystem S] [--tag T] [--regex R] [--since TS] [--until TS] [--follow] [--json]` | Without `--follow`: REST proxy to `logs` MCP tool (bounded JSON array). With `--follow`: SSE live stream via `POST /api/v1/log_stream`. | Needs review — iOS range quirks; live streaming is REST-only |
| `spyder pool list [--json]` | REST proxy to `pool_list`. | Needs review |
| `spyder pool warm <template> [--count N]` | REST proxy to `pool_warm`. `--count` defaults to 1. | Needs review |
| `spyder pool drain <template>` | REST proxy to `pool_drain`. | Needs review |

All device-tool subcommands POST to `$SPYDER_DAEMON_URL` (default
`http://127.0.0.1:3030`) and print the first text content block
(text tools) or write the first image content block to disk
(`screenshot`). `--as OWNER` defaults to `filepath.Base(cwd)`.

#### Universal flags (every device-tool subcommand)

These flags are auto-registered by `setupCommand` (cli.go) so the
surface is uniform across the CLI:

| Flag | Default | Behaviour |
|---|---|---|
| `--timeout DURATION` | per-command (see below) | Bounds the daemon HTTP call. Go-style duration string (`30s`, `5m`, `2h`). `0` disables the timeout. Exceeded → exit `30` (`timeout`). |
| `--verbose` / `-v` | off | For mutating commands (silent on success by default), restores the daemon's confirmation text on stdout. For read commands, no behavioural change today (reserved for future per-tool diagnostic chatter to stderr). |
| `--json` | off | On read-ish commands (devices, resolve, device-state, list-apps, reservations, runs, crashes, sim list, emu list, pool list, log, diff), emits the daemon's JSON response verbatim for piping to `jq`. Mutating commands do not accept `--json` (their value is the exit code). |

Per-command `--timeout` defaults: read commands `10s`; launch / terminate
/ rotate / sim/emu/net / pool ops `60s`; install / uninstall `5m`;
deploy `10m`; screenshot `30s`; reserve / release / renew `30s`;
record `60s`; `log --follow` and `spyder run -- <cmd>` no timeout.

#### CLI selector grammar (`--on PREDICATE`)

`--on` parses a comma-separated string into the same
`internal/selector.Selector` struct used by the MCP `reserve` tool.
Recognised keys:

| Key | Value | Meaning |
|---|---|---|
| `platform` | `ios` / `android` / `ios-sim` / `android-emu` | Required for selector dispatch. Matches `device.Info.Platform`. |
| `model` | family name (case-insensitive) | Matches `device.Info.Model` and inventory `Tags`. Examples: `ipad`, `iphone`, `phone`, `tablet`. |
| `os>=VERSION` | semver string | Lower bound (inclusive) on `device.Info.OS`. |
| `os<=VERSION` | semver string | Upper bound (inclusive). |
| `os_min`, `os_max` | semver string | Alternate spellings for the above. |
| `orientation_capable` | `true` / `false` / `1` / `0` | Match only sims/emus (rotation is a software feature). |
| `tags` | `tag1+tag2+…` | Plus-separated set; all must be present on the inventory entry's `Tags`. |
| `attr.<name>` | string | Per-key exact match against the inventory entry's `Attrs[name]`. |

Example: `spyder reserve --on platform=ios,os>=17,tags=phone+test --as ci`.

Empty input, unknown keys, duplicate keys, and malformed bool/version
values are reported as `selector parse: …` errors with exit code `2`.

#### Exit codes

Standardised across every CLI subcommand. Defined in
`internal/cliexit/cliexit.go`; mappable from the daemon's REST error
shape via `cliexit.MapDaemonError(statusCode, errorCode, message)`.

| Code | Constant | Meaning |
|---|---|---|
| 0 | `ExitOK` | Success. |
| 1 | `ExitGeneric` | Unclassified failure. Reserved for paths that genuinely can't be attributed to a more specific cause. |
| 2 | `ExitUsage` | Argument parsing error (unknown flag, missing positional, bad format). |
| 10 | `ExitDaemonUnreachable` | Daemon not reachable at `$SPYDER_DAEMON_URL` (and auto-start failed for the default loopback target). |
| 11 | `ExitDeviceNotFound` | Alias / UDID does not resolve to a known device. |
| 12 | `ExitDeviceNotConnected` | Device known but not currently connected, paired, or reachable. |
| 13 | `ExitReservationConflict` | Device held by another owner; `spyder reserve` cannot acquire. Also returned by `spyder run` when the auto-acquire fails for this reason. |
| 14 | `ExitNotReservedByYou` | Operation requires reservation by the supplied owner, and you don't hold it. |
| 15 | `ExitSelectorNotSupported` | `spyder resolve` input is neither a known alias nor a parseable selector predicate. Distinct from exit 1 so scripts can fall through to platform-specific tooling rather than retrying. (🎯T38.3) |
| 20 | `ExitAppNotInstalled` | Bundle id not installed on the device. Also surfaced by `is-running`. |
| 21 | `ExitInstallFailed` | `install_app` / `deploy_app` failed (signing, profile, transport). |
| 22 | `ExitLaunchFailed` / `ExitAppNotRunning` | `launch_app` / `deploy_app` failed at the launch step **or** `is-running` reports installed-but-not-running. The two share a code because semantically both mean "the app is not currently running". (🎯T38.1) |
| 23 | `ExitTerminateFailed` | `terminate_app` could not stop the running process. |
| 24 | `ExitPIDVerificationFailed` | `deploy_app` succeeded at install+launch but PID-verification (post-launch liveness check) failed. |
| 30 | `ExitTimeout` | `--timeout DURATION` exceeded (or implicit per-command default exceeded). |
| 40 | `ExitTrustNotGranted` | iOS device pair-record missing or trust dialog not accepted. |
| 41 | `ExitDeveloperModeDisabled` | iOS Developer Mode toggle is off. |
| 42 | `ExitDeviceLocked` | Device is locked (passcode prompt active). |

The `1.0` commitment is on the codes above and their *meaning*. Adding
new codes for previously-unclassified causes is non-breaking. Repurposing
or removing a code is breaking and forbidden after 1.0.

#### Hermeticity

Each `spyder` proxy CLI invocation is independent: no sticky-state
files under `~/.spyder/`, no implicit "current device". The only
filesystem touches the proxy CLI ever does are:

- `~/.spyder/daemon.log` when auto-spawning a daemon for the default
  loopback target (CLI logs the spawn for diagnostics).
- `<screenshot --output FILE>` — the user-supplied path.

`spyder run` is the one daemonless wrapper and does manage
`~/.spyder/reservations.json` + `~/.spyder/runs/` directly; this is
documented and locked into the contract. The hermeticity rule is
enforced by `TestCLIHermeticity` and `TestCLINoStickyStateOutsideAllowList`.

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
- **SSE log stream:** `POST /api/v1/log_stream` accepts `{device, process?,
  subsystem?, tag?, regex?}` and returns `Content-Type: text/event-stream`.
  Each event is `data: <JSON LogLine>\n\n`. The stream runs until the client
  disconnects. This is the only endpoint that returns a streaming response.
  Stability: **Needs review** — shape may evolve before 1.0.

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
  "notes": "string (optional)",
  "tags": ["string", ...],           // optional; labels for selector matching
  "attrs": {"key": "value", ...}     // optional; exact-match key/value predicates
}
```

Missing file is treated as empty, not an error. Alias lookup is
case-insensitive. `tags` and `attrs` are backwards-compatible: absent
fields load as nil/empty and old clients ignore the new fields. **Stable
(core fields); Needs review (tags/attrs — grammar may evolve with selector).**

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
- **Shell-out path coverage.** Pure logic (inventory, parsers, classifiers,
  MCP dispatch, reservations, daemon HTTP roundtrip, bridge protocol) is
  unit-covered. The developer-run test-report workflow (🎯T26.4) adds real-
  bridge integration and real-device device tiers, but the `internal/device`
  adapter wrappers around `pymobiledevice3`/`adb`/`devicectl` and the
  `internal/notify` OS-specific helpers still lack the same rigour. Before
  1.0 these should be brought under the tiered test runner with their own
  device-present coverage.
- **`pmd3-bridge` internal dependency.** The `internal/pmd3bridge` package
  wraps a FastAPI bridge subprocess as a daemon-private dependency (ephemeral
  loopback port + bearer token, 🎯T26.1; fail-fast on any unresponsiveness,
  🎯T26.2; NDJSON/octet-stream streaming with inter-packet deadline,
  🎯T26.3). The bridge binary ships at `libexec/pmd3-bridge/pmd3-bridge` in
  the Homebrew formula. Battery, list_apps, launch/kill_app, crash-report,
  screenshot (legacy path) surfaces flow through the bridge. Internal to
  the daemon and not part of the 1.0 stability contract.
- **iOS keep-awake via on-device companion app.** The `ios/KeepAwake/`
  SwiftUI app sets `UIApplication.isIdleTimerDisabled = true` while
  foregrounded and exits on `batteryState == .unplugged` so iOS reclaims
  the slot when the cable is pulled. Autoawake runs a convergence loop
  (🎯T32): every 15 s it observes whether KeepAwake is running on each
  connected device, installs (xcodebuild + devicectl) when absent,
  launches when not running, and re-tests human-gate states (locked,
  needs-trust, needs-developer-mode) each tick so user-side resolutions
  are detected without re-plugging. The "connected" filter is wired-only
  (`devicectl transportType=wired`); Wi-Fi-reachable devices are
  excluded so they don't fight KeepAwake's unplugged self-exit in a
  relaunch loop. Per-developer signing identity required (free-tier
  Apple ID suffices). pmd3's `PowerAssertionService` was attempted as a
  drop-in replacement in v0.6.0–v0.8.0 but is a no-op for display sleep
  on iOS; reverted in v0.9.0 (🎯T31).
- **Tunneld lifecycle.** iOS 17+ screenshot and any future
  DVT-instrument operations need an active RSD tunnel for the device.
  spyder's bridge expects an externally-managed `pymobiledevice3 remote
  tunneld` process on `127.0.0.1:49151` (typically a launchd service
  installed by the user) and surfaces `tunneld_unavailable` (bridge error
  code, HTTP 503) when tunneld is unreachable or has no registered devices.
  This is the accepted design for v0.x — the bridge does not start or
  supervise tunneld itself. Pre-iOS-17 devices that are not in the tunneld
  registry will fail screenshot with `device_not_paired`; a legacy
  `com.apple.mobile.screenshotr` fallback for pre-17 devices is deferred
  (no test target on currently-paired hardware). The bridge transparently
  retries transient `EHOSTUNREACH` from the tunneld probe (3 attempts, 0.5
  s backoff) so a brewservices-supervised daemon survives loopback hiccups
  during tunneld restart (🎯T36, v0.17.0).
- **macOS-only host enforcement.** Spyder runs on Linux but iOS operations
  will fail noisily there. Either restrict the binary to Darwin or
  gracefully degrade iOS-related tools with a clear "host does not support
  iOS" error.
- **Rate-limiting / retry policy.** Auto-awake retries lock failures every
  10 s for up to 5 minutes. Not user-configurable. Fine for v0.x but should
  surface a knob before 1.0.
- **Network shaping for iOS simulator.** `network` returns an error on iOS
  simulator and physical devices. Apple's Link Conditioner is host-level (not
  per-simulator); driving it via a CLI requires private CoreSimulator APIs.
  Contributions that implement per-simulator shaping via future `simctl`
  flags or the private framework are welcome.
- **Packet-loss emulation on Android.** The `lossy-<pct>` profile partially
  applies (speed/delay are set) but the adb emulator console has no loss knob.
  Host-level traffic shapers (`tc`, `dummynet`) or Android Studio's network
  profiler are alternatives.
- **Network profile persistence across daemon restarts.** Applied profiles are
  tracked in-memory only. If the daemon crashes before `release` fires the
  cleanup, the emulator retains its last profile until an explicit `clear` or
  emulator restart. A future version may persist the active profile to disk
  (alongside `reservations.json`) to enable cleanup on daemon restart.
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
- Auto-install of a companion app on Android — Android handles stay-awake
  natively via Developer Settings; no companion app is needed.
