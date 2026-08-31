# STABILITY.md

Spyder is pre-1.0. This document catalogues the public interaction surface and
tracks the state of each piece relative to a future 1.0 lock-in.

## Stability commitment

At 1.0, spyder commits to backwards compatibility for:

- The **MCP tool surface** — the single advertised tool `app_exec`
  (name, input schema, output shape). Per-verb names are **not** MCP
  tools (🎯T88); they are REST paths and Starlark builtins.
- The **CLI subcommand surface** (`spyder serve`, `spyder run`, `spyder
  doctor`, `spyder status`, `spyder version`, `spyder help-agent`, plus
  the device-tool subcommands listed below, flag names, exit codes).
- The **inventory file format** (`~/.spyder/inventory.json`).
- The **HTTP MCP endpoint** (`/mcp`, port default, streamable-HTTP transport).
- The **REST endpoint** (`/api/v1/<tool>` POST + JSON, same listener).

Breaking changes to any of these after 1.0 require a major version bump (or,
per the project's policy, a fork into a new product). The pre-1.0 period
exists to get these right.

## Supported platforms

**macOS arm64 only.** Spyder's iOS device orchestration relies on macOS-only
tooling (`xcrun simctl` for simulators, the bundled go-ios CLI / userspace
tunnel for real devices). The Android adapter via `adb` is cross-platform
in principle, but `adb` itself is cross-platform; spyder doesn't add value
to adb-only workflows on Linux. Release artefacts are darwin-arm64 only;
Homebrew tap formula targets darwin-arm64 only. (🎯T45)

Snapshot as of `v0.82.0` — the iOS path runs entirely on the in-process
[go-ios](https://github.com/danielpaulus/go-ios) Go library, plus a
bundled `ios` userspace tunnel daemon spawned as a spyder child process
(🎯T56). The data path crosses no subprocess boundary: every iOS
operation (list, lockdown queries, DTX, syslog, crash reports,
installation) runs in-process.

## Interaction surface catalogue

### MCP tools

`Definitions()` advertises **only** `app_exec`. Callers that look for
MCP tools named `devices`, `screenshot`, etc. will not find them on
the wire.

| Tool | Input schema | Output | Stability |
|---|---|---|---|
| `app_exec` | `{script?: string, script_path?: string, params?: object, max_duration_ms?: number}`. Exactly one of `script` or `script_path`. Default wall-clock 30 s; `max_duration_ms` capped at **600000** (10 min). | Ordered MCP content blocks from `emit()` and/or the final top-level expression. Screenshot verbs default to a filesystem-path dict (🎯T114); `inline=true` yields an image block. | Stable (🎯T88) |

Starlark-only helpers (not REST, not in `toolHandlers`): `sleep`, `emit`,
`health()`, `help()`, plus 🎯T108/T109 `assert_*` / `find_*`.

### REST / Starlark verbs

The runtime source of truth is `Handler.toolHandlers`. Every name below
is `POST /api/v1/<name>` and a Starlark builtin inside `app_exec`
(`app_exec` itself is a REST verb too, but is **not** nested as a
builtin). `log_stream` is REST SSE only and is not in the verb table.

| Verb | Input schema | Output | Stability |
|---|---|---|---|
| `devices` | `{platform?: "ios"\|"android"\|"all"}` (default `all`). | JSON array of `device.Info` (`uuid`, `name`, `platform`, `model`, `os`, `alias`, optional `tunnel_pending`, optional `usb_speed` / `usb_ceiling` / `usb_anomaly`). USB fields are omitempty (🎯T131 / 🎯T131.1): `usb_speed` is the IOKit Device Speed of a USB-attached physical phone/tablet (`"480 Mb/s"`, `"5 Gb/s"`, `"10 Gb/s"`); `usb_ceiling` is the highest speed observed for that serial; `usb_anomaly` is true only when live < ceiling. Wireless ADB, iOS Wi-Fi, simulators, emulators, and desktop omit them. When `platform=all` and an adapter errors, wraps as `{devices: [...], errors: [...]}`. | Stable |
| `resolve` | `{name?: string, selector?: string}`. Exactly one of name (alias / raw UUID) or selector (JSON predicate, same grammar as `reserve`'s selector) required. | JSON-encoded `inventory.Entry` (`alias`, `platform`, `ios_uuid`, `ios_coredevice`, `android_serial`, `notes`). With selector, returns the entry of the first matching live device. | Needs review — passthrough shape for unknown IDs may evolve |
| `device_state` | `{device: string}` (required; alias or raw UUID/serial). | JSON-encoded `device.State` (`battery_level?`, `charging?`, `thermal_state?`, `foreground_app?`, `storage_free_mb?`, `notes?`). | Needs review — pointer-typed optionals, field additions expected |
| `screenshot` | `{device: string, owner?: string, path?: string, inline?: bool}` (device required; owner archives into the active run). | Default (🎯T114): JSON `{device, path, width, height, bytes}` — PNG written under `~/.spyder/screenshots` (or `path`). `inline=true` returns an MCP image content block (base64 PNG, `image/png`). iOS uses go-ios DVT `ScreenshotService` (iOS 17+ needs the bundled tunnel; iOS ≤16 uses lockdown and needs the Developer Disk Image mounted). Android uses `adb shell screencap`. Read-only; not reservation-gated. | Stable |
| `list_apps` | `{device: string}` (required). | JSON array of `device.AppInfo` (`bundle_id`, `name?`, `version?`). | Needs review — Android currently returns bundle_id only; name/version parity pending |
| `launch_app` | `{device: string, bundle_id: string, owner?: string, env?: object}` (device and bundle_id required; owner for reservation auth). | JSON `{device, bundle_id, session_id?, channel_port?}` — session fields appear when the app completes the app-channel handshake (🎯T119). | Stable |
| `is_running` | `{device: string, bundle_id: string}` (both required). | JSON `{state: "running"\|"not_running"\|"not_installed", pid?: number}`. Read-only; not subject to reservations. iOS uses go-ios's `appservice.ListProcesses` cross-referenced with `installation_proxy.BrowseAllApps` to map bundle id → .app folder → running pid; Android uses `adb shell pidof`. | Stable (🎯T38.1) |
| `terminate_app` | `{device: string, bundle_id: string, owner?: string}` (device and bundle_id required; owner for reservation auth). | Text confirmation. | Stable |
| `install_app` | `{device: string, path: string, owner?: string}` (device and path required). Path must not contain `..` and must exist. | Text confirmation. | Stable |
| `uninstall_app` | `{device: string, bundle_id: string, owner?: string}` (device and bundle_id required). | Text confirmation. | Stable |
| `launch_player` | `{device: string, server?: string, path?: string, owner?: string}` (device required). | JSON `{device, platform, server, stream_addr, bundle_id, pid?, path?, variant?}`. Injects `STREAM_ADDR` / server name. Picks the sole registered stream server when `server` is omitted; errors if zero or multiple. | Stable (🎯T100.3) |
| `deploy_app` | `{device: string, path: string, bundle_id?: string, owner?: string, env?: object}` (device and path required). `bundle_id` derived from Info.plist (iOS) or `aapt dump badging` (Android) if omitted. Refuses the spyder stream player — use `launch_player`. | JSON `{bundle_id: string, pid: number, replaced: bool, session_id?: string, channel_port?: number}` (🎯T121). | Stable |
| `reserve` | `{device?: string, selector?: string, owner: string, ttl_seconds?: number, note?: string}`. Exactly one of device (literal pin) or selector (JSON predicate: platform, model_family?, os_min?, os_max?, orientation_capable?, tags?, attrs?) required. owner is always required. | JSON-encoded `reservations.Reservation` (device, owner, expires_at, note, created_at). | Needs review — selector grammar may evolve |
| `release` | `{device: string, owner: string}`. | Text confirmation. Applied network profiles cleared automatically. | Stable |
| `renew` | `{device: string, owner: string, ttl_seconds?: number}`. | JSON-encoded `reservations.Reservation` with refreshed expires_at. | Stable |
| `reservations` | (no args). | JSON array of active `Reservation` records. | Stable |
| `reservation_status` | `{device: string, owner?: string}` (device required). | JSON `{reserved, holder?, expires_at?, caller_holds, would_gate, gated_verbs, policy}`. Read-only; never gated itself. | Stable (🎯T116) |
| `runs_list` | (no args). | JSON array of `runs.Run` records (id, device, owner, note, created_at, closed_at?, artefacts?), newest first. | Needs review — field additions expected as more artefact-producing tools land |
| `runs_show` | `{run_id: string}`. | JSON-encoded `runs.Run` with full artefact list. | Needs review — same caveat as `runs_list` |
| `rotate` | `{device: string, orientation: string, owner?: string}` (device and orientation required). Orientation: `portrait`, `landscape-left`, `landscape-right`, `portrait-upside-down`. | Text confirmation. | Needs review — simulator/emulator-only; physical device error wording may evolve |
| `crashes` | `{device: string, since?: RFC3339\|duration\|"launch", process?: string, bundle_id?: string, owner?: string}` (device required). `bundle_id` and `process` are mutually exclusive; `since=launch` requires `bundle_id`. | JSON array of crash reports. iOS: go-ios `crashreport`; Android: tombstones + `logcat -b crash`. Read-only. | Needs review |
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
| `pool_gc` | (no args). | JSON list of deleted and skipped orphaned `spyder-pool-*` simulators/AVDs. Booted orphans are skipped. | Needs review |
| `record_start` | `{device: string, owner?: string}` (device required; owner for reservation auth). | Text confirmation with subprocess PID and output path. | Needs review — iOS simulator UDID must be passed directly; iOS physical devices return an immediate error. |
| `record_stop` | `{device: string, owner?: string}` (device required; owner for reservation auth). | Text confirmation with the local mp4 path. | Needs review |
| `network` | `{device: string, owner: string, profile?: string}` or `{device: string, owner: string, clear: true}`. Exactly one of profile or clear required. | Text confirmation. | Beta — Android emulator only; iOS and physical Android return clear errors. |
| `perf_fps` | `{device, package\|bundle_id, window_sec?, owner?}`. | Android: JSON result with fps/total_frames (gfxinfo). iOS: clear error → app_perf_get/metrics. | Stable (🎯T111/T112) — platform-honest |
| `port_forward_start` | `{device, device_port, local_port?, owner?}`. | JSON `{device, platform, local_port, device_port, spec, host_url}`. Android adb; iOS usbmux. | Stable (🎯T111/T112) |
| `port_forward_stop` | `{device, local_port, owner?}`. | JSON `{device, platform, local_port, removed}`. | Stable (🎯T111/T112) |
| `port_forward_list` | `{device, owner?}`. | JSON `{device, platform, forwards:[…]}`. | Stable (🎯T111/T112) |
| `input_tap` | `{device, x, y, owner?}`. | Android: injected. iOS: clear error → app_input/mobile-mcp. | Stable (🎯T111/T112) |
| `input_swipe` | `{device, x1, y1, x2, y2, duration_ms?, owner?}`. | Same Android/iOS split as tap. | Stable (🎯T111/T112) |
| `device_setting` | `{device, key, value?, restore?, get?, owner?}`. Allowlist: `refresh_rate` (Android peak+min). | Android: JSON `{device, platform, result}` with action set/restore/get and current values. Restore **deletes** the pin. Unknown keys rejected (no adb shell). iOS: `"not supported"`. | Stable (🎯T130 / 🎯T112) — platform-honest |
| `wait_state` | `{session_id? \| device+bundle_id?, slice, select?, timeout_ms?, poll_ms?}`. | JSON `{slice, select, value, attempts, elapsed_ms}` where `value` is the jq result that became truthy. Timeout error includes **last observed** value. | Stable (🎯T129) |
| `logs` | `{device: string, since?: RFC3339\|duration\|"launch"\|"now", until?: RFC3339\|duration\|"now", bundle_id?: string, process?: string, subsystem?: string, tag?: string, regex?: string}` (device required). `bundle_id` and `process` are mutually exclusive. | JSON array of `device.LogLine` (`timestamp`, `process?`, `level?`, `tag?`, `message`). Empty array when no lines match. | Needs review — iOS range is live-window based (not true archived-log query); see *iOS log live-window contract* below. Field set and timestamp precision may evolve |
| `log_capture_start` | `{device: string, owner?, bundle_id?, process?, subsystem?, tag?, regex?, ttl_sec?, max_bytes?, max_lines?}`. | JSON `{session_id, started_at, expires_at}`. | Needs review (🎯T60) |
| `log_capture_get` | `{session_id: string}`. | Buffered lines since start or last get; capture continues. | Needs review |
| `log_capture_stop` | `{session_id: string}`. | Remaining lines; session is gone. | Needs review |
| `log_capture_list` | (no args). | Metadata for every live capture session. | Needs review |
| `app_channel_stop` | `{listener_id: string}`. | Text confirmation. Tears down the listener and its sessions. | Needs review |
| `app_channel_list` | (no args). | JSON listeners + sessions (listener_id, device_id, bundle_id, port, owner, idle_since, sessions[]). | Needs review |
| `app_ping` | `{session_id? \| device+bundle_id?}`. | App-seen timestamp. | Needs review |
| `app_quit` | `{session_id? \| device+bundle_id?, timeout_ms?}`. | Clean-exit ack; falls back to `terminate_app` on timeout. | Needs review |
| `app_flush` | `{session_id? \| device+bundle_id?}`. | Ack after draining pending app output. | Needs review |
| `app_background` / `app_foreground` / `app_low_memory` | `{session_id? \| device+bundle_id?}`. | Text confirmation. | Needs review |
| `app_pause` / `app_resume` | `{session_id? \| device+bundle_id?}`. | Text confirmation. | Needs review |
| `app_step` | `{session_id? \| device+bundle_id?, frames?}`. | Advances N frames while paused (default 1). | Needs review |
| `app_speed` | `{session_id? \| device+bundle_id?, multiplier: number}`. | Text confirmation. | Needs review |
| `app_input` | `{session_id? \| device+bundle_id?, type: string, …}`. `type`: `finger_down` / `finger_up` / `finger_motion` / `key_down` / `key_up` / `accel`. | Text confirmation. | Needs review |
| `app_sensor_suppress` / `app_sensor_set` / `app_sensor_unsuppress` / `app_sensor_status` | `{session_id? \| device+bundle_id?, sensor?: "accel", value?: [x,y,z]}`. `value` required for set. | Status or confirmation. Only `accel` today. | Needs review |
| `ensure_session` | `{device: string, bundle_id?, path?, owner?, env?, timeout_ms?}`. | JSON `{session_id, channel_port, pid, deployed, launched}`. Idempotent if a healthy session exists. | Stable (🎯T118) |
| `state_query` | `{session_id? \| device+bundle_id?, slice?, select?}`. | App session-state summary (or named slice); observational; not reservation-gated. | Stable (🎯T122) |
| `app_state` | `{session_id? \| device+bundle_id?, slice: string, select?}`. | Named state slice (optionally jq-filtered). | Needs review |
| `app_tweak_list` | `{session_id? \| device+bundle_id?}`. | Tweak catalogue (name, value, default, metadata). | Needs review (🎯T91.2) |
| `app_tweak_get` | `{session_id? \| device+bundle_id?, name: string}`. | One tweak. | Needs review |
| `app_tweak_set` | `{session_id? \| device+bundle_id?, name: string, value: any}`. | Confirmation after apply/persist. | Needs review |
| `app_tweak_reset` | `{session_id? \| device+bundle_id?, name?}`. Omit `name` to reset all. | Confirmation. | Needs review |
| `app_spawn` | `{session_id? \| device+bundle_id?, game?, owner?, env?, instance_id?}`. Factory path needs `game`; device path launches an installed bundle. | Ready session (`already_running?` on the device path). | Needs review (🎯T92.1 / 🎯T117) |
| `app_acquire` | `{session_id? \| device+bundle_id?, game: string, owner?}`. | Reserved factory instance session. | Needs review (🎯T92.1) |
| `app_release` | `{session_id: string}`. | Confirmation. | Needs review |
| `games` | `{device?}`. | Catalog: desktop targets, factories, and (with `device`) installed mobile bundles. | Needs review (🎯T92 / 🎯T117) |
| `app_save_state` | `{session_id? \| device+bundle_id?}`. | JSON `{state_b64, size}`. | Needs review |
| `app_restore_state` | `{session_id? \| device+bundle_id?, state_b64: string}`. | Confirmation. | Needs review |
| `app_screenshot` | `{session_id? \| device+bundle_id?, path?, inline?}`. | Default (🎯T114): JSON `{path, width, height, format, bytes}` under `~/.spyder/screenshots`. `inline=true` → MCP image block. | Stable |
| `app_state_slices` | `{session_id? \| device+bundle_id?}`. | Slice catalogue from hello. | Needs review |
| `app_state_describe` | `{session_id? \| device+bundle_id?, slice: string}`. | Types-only structural sketch of the slice. | Needs review |
| `app_state_capture_start` | `{session_id? \| device+bundle_id?, slice: string, interval_ms?, select?}`. | `{capture_id}`. Default interval 100 ms, min 10. | Needs review |
| `app_state_capture_get` | `{capture_id: string, select?}`. | Drained samples; capture continues. | Needs review |
| `app_state_capture_stop` | `{capture_id: string}`. | Remaining samples; capture gone. | Needs review |
| `app_state_capture_list` | `{session_id? \| device+bundle_id?}`. | Active captures with metadata. | Needs review |
| `app_log_get` | `{session_id? \| device+bundle_id?, select?}`. | Structured log lines since last call. | Needs review |
| `app_perf_get` | `{session_id? \| device+bundle_id?, select?}`. | Latest-only / push gauges (not the metrics ring). | Needs review |
| `app_metrics_list` | `{session_id? \| device+bundle_id?, instance?}`. | JSON `{session_id, result}` where `result` is the app's metrics catalogue (ge: `{instance, series:[{name,kind},…]}` or multi-instance wrapper). | Stable (🎯T110) — requires appchannel session advertising `metrics_list` |
| `app_metrics_arm` | `{session_id?…, series: string[] (required), capacity?: number, instance?}`. | JSON `{session_id, result}` with arm status (`armed`, `capacity`, `count`, `series`, `instance`). | Stable (🎯T110) |
| `app_metrics_disarm` | `{session_id?…, instance?}`. | JSON `{session_id, result}` status after clear. | Stable (🎯T110) |
| `app_metrics_status` | `{session_id?…, instance?}`. | JSON `{session_id, result}` capture status. | Stable (🎯T110) |
| `app_metrics_dump` | `{session_id?…, instance?}`. | JSON `{session_id, result}` full retained-frame history (`frames`, `count`, `series`, …) — not latest-only gauges. | Stable (🎯T110) |
| `app_methods` | `{session_id? \| device+bundle_id?, scope?: "all"\|"app"\|"engine"}`. | JSON `{session_id, app_name, app_version, scope, methods:[{name, kind, example_params?, doc?}]}` from hello. | Stable — discovery surface for engine + app-registered RPCs |
| `app_call` | `{session_id?…, method: string (required), params?: object, timeout_ms?}`. | JSON `{session_id, method, result}` from the app's handler. | Stable — generic pass-through; method must appear in hello; not a per-game MCP tool |
| `list_scripts` | (no args). | JSON list of durable host Starlark recipes (bundled + `~/.spyder/scripts`). | Stable (🎯T108) |
| `run_script` | `{path?: string, name?: string, params?: object, max_duration_ms?: number}`. Same engine as `app_exec`. | Same content-block model as `app_exec`. `max_duration_ms` default 30000, max 600000. | Stable (🎯T108) |
| `app_exec` | Same schema as the MCP tool. REST `POST /api/v1/app_exec`. Not a nested Starlark builtin. | Same as MCP `app_exec`. | Stable (🎯T88) |

Error classification is part of the contract: `device not connected`, `app
not installed`, `app not running`, the `ErrLocked` sentinel, and
`ErrTrustNotGranted` are all surfaced as distinct tool-error text.
Callers can match on these phrases.

#### iOS log live-window contract (🎯T38.2)

`logs` and `log_stream` on iOS physical devices are **live-window only**:
they subscribe to go-ios's `syslog_relay` (com.apple.syslog_relay over
the userspace tunnel's RSD shim on iOS-17+) and collect entries during
the live window. There is no archived-log query mode.

The hard rule callers can rely on:

- A `since` timestamp **older than the moment the live tail subscribes
  to the device** will silently miss lines that occurred before the
  subscription started. The window starts now.
- The collector caps the wait at 30s (or `until - now`, whichever is
  smaller) to bound query latency.

Additionally on the go-ios path:

- The `subsystem` filter is unsupported. go-ios's syslog parser
  surfaces only the classic BSD-syslog fields (timestamp, process,
  pid, level, message) — there is no structured subsystem metadata
  to filter on. A non-empty `subsystem` filter rejects all entries.
- The per-entry since/until *drop* filter is suppressed. go-ios's
  parser timestamps entries in UTC even though iOS emits them in the
  device's local timezone; comparing those across timezones would
  reject the whole stream. The deadline still bounds collection;
  what's lost is the ability to discard old entries that happen to
  land in the drained burst.

Practical consequence for callers:
- "Did this process log anything in the **last 5 minutes**?" cannot be
  answered against archived logs. The window starts now.
- For crash detection, prefer the `crashes` tool (which reads from
  `~/Library/Logs/CrashReporter/MobileDevice/<device>/`-style storage
  via go-ios's afc-mediated `crashreport.DownloadReports`).
- For continuous monitoring, use `log --follow` (live SSE stream) and
  inspect lines as they arrive rather than retrospectively.

iOS simulators and Android devices do not share these constraints —
simulators read from the host's unified-log store via `xcrun simctl
spawn ... log`, and Android's `adb logcat` has its own ring buffer.

### CLI subcommands

| Invocation | Behaviour | Stability |
|---|---|---|
| `spyder` (no args) | Prints usage to stdout. | Stable |
| `spyder serve [--addr :PORT]` | HTTP MCP server + bundled `ios tunnel start --userspace` subprocess. Blocks until SIGINT/SIGTERM. | Stable |
| `spyder run [--device ALIAS\|-d ALIAS\|--on PREDICATE] [--as OWNER] [--timeout DURATION] -- <cmd> [args...]` | Runs command under an auto-acquired reservation (owner defaults to `filepath.Base(cwd)`); releases reservation on exit; opportunistically renews during long runs. Forwards exit code. `--on PREDICATE` resolves+reserves atomically via the daemon (closing the resolve→release→re-acquire race window). `--timeout DURATION` (e.g. `5m`) bounds the wrapped child invocation; on deadline, exits 30 (`ExitTimeout`) instead of forwarding the child's signal-induced exit. `--device` and `--on` are mutually exclusive. | Stable (🎯T38.4 + 🎯T38.5) |
| `spyder version` / `--version` / `-version` | Prints `spyder <tag>`. | Stable |
| `spyder help` / `--help` / `-help` | Prints usage. | Stable |
| `spyder help-agent` / `--help-agent` / `-help-agent` | Usage + embedded agents-guide.md. | Stable |
| `spyder doctor [--fix] [--install-sudoers]` | Cross-check iOS device-stack diagnosis. `--fix` restarts usbmuxd via `spyder-killusbmuxd`. Can run without the daemon. | Stable (🎯T90 / 🎯T99) |
| `spyder status [--json]` | HTTP client of a running daemon; prints the live health model. Does not start the daemon. | Stable (🎯T90) |
| `spyder devices [--platform ios\|android\|all] [--json]` | REST proxy to `devices` tool. | Stable |
| `spyder resolve (<name>\|--on PREDICATE) [--json]` | REST proxy to `resolve` tool. Positional `<name>` is treated as an alias / raw UUID. `--on PREDICATE` (or a positional that contains `=`) is parsed as a selector predicate, resolved against live devices via the daemon, and the matched inventory entry is returned. Inputs that are neither a known alias (per local inventory) nor a parseable predicate exit 15 (`ExitSelectorNotSupported`) — distinct from exit 1 for alias-known-but-resolution-failed. The CLI does the alias/predicate triage locally before round-tripping to the daemon, so unknown strings no longer get the synthetic Android-serial classification the underlying MCP `resolve` tool falls back to for legacy callers. | Stable (🎯T38.3) |
| `spyder is-running <device> <bundle-id> [--json]` | REST proxy to `is_running`. Exits 0 (running, prints `running pid=<n>`), 20 (not installed), or 22 (installed but not running). `--json` emits the raw `{state, pid?}` body in addition to the exit code. | Stable (🎯T38.1) |
| `spyder device-state <device> [--json]` | REST proxy to `device_state` tool. | Stable |
| `spyder screenshot <device> [--output FILE] [--as OWNER]` | REST proxy to `screenshot`; writes PNG to `--output` (default `<device>-<ts>.png`). | Stable |
| `spyder list-apps <device> [--json]` | REST proxy to `list_apps`. | Stable |
| `spyder launch-app <device> <bundle-id> [--as OWNER]` | REST proxy to `launch_app`. | Stable |
| `spyder terminate-app <device> <bundle-id> [--as OWNER]` | REST proxy to `terminate_app`. | Stable |
| `spyder install <device> <path> [--as OWNER]` | REST proxy to `install_app`. | Stable |
| `spyder uninstall <device> <bundle-id> [--as OWNER]` | REST proxy to `uninstall_app`. | Stable |
| `spyder deploy <device> <path> [--bundle-id ID] [--as OWNER]` | REST proxy to `deploy_app`. Derives bundle id from Info.plist (iOS) or `aapt` (Android) when `--bundle-id` is omitted. Refuses the stream player — use `launch-player`. | Stable |
| `spyder launch-player <device> [--server NAME] [--path PATH] [--as OWNER]` | REST proxy to `launch_player`. | Stable (🎯T100.3) |
| `spyder reserve (<device>\|--on PREDICATE\|--selector JSON\|--platform PLATFORM [--model FAMILY] [--tag TAG]...) [--as OWNER] [--ttl SECONDS] [--note TEXT]` | REST proxy to `reserve`. Positional device = literal pin. `--on PREDICATE` = comma-separated key=value selector grammar (see "CLI selector grammar" below). `--selector` = JSON predicate. Shorthand `--platform`/`--model`/`--tag` flags build the selector inline. | Stable (positional, --on, --selector); Needs review (shorthand --platform/--model/--tag — may consolidate around --on) |
| `spyder release <device> [--as OWNER]` | REST proxy to `release`. | Stable |
| `spyder renew <device> [--as OWNER] [--ttl SECONDS]` | REST proxy to `renew`. | Stable |
| `spyder reservations [--json]` | REST proxy to `reservations`. | Stable |
| `spyder runs list [--json]` | REST proxy to `runs_list`. | Needs review |
| `spyder runs show <run-id> [--json]` | REST proxy to `runs_show`. | Needs review |
| `spyder runs artefacts <run-id> [--json]` | REST proxy to `runs_show`; prints just the artefacts table. | Needs review |
| `spyder rotate <device> --to <orientation> [--as OWNER]` | REST proxy to `rotate`. Orientation: `portrait`, `landscape-left`, `landscape-right`, `portrait-upside-down`. | Needs review |
| `spyder crashes <device> [--since RFC3339\|-15m\|launch] [--bundle-id ID \| --process NAME] [--as OWNER] [--json]` | REST proxy to `crashes`. | Needs review |
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
| `spyder log <device> [--bundle-id ID \| --process P] [--subsystem S] [--tag T] [--regex R] [--since TS] [--until TS] [--follow \| --capture …] [--json]` | Without `--follow`: REST proxy to `logs` (bounded JSON array). With `--follow`: SSE live stream via `POST /api/v1/log_stream`. `--capture` / `--capture-get` / `--capture-stop` / `--capture-list` proxy `log_capture_*`. | Needs review — iOS range quirks; live streaming is REST-only |
| `spyder perf-fps <device> --package PKG [--window-sec N] [--as OWNER] [--json]` | REST proxy to `perf_fps`. | Stable (🎯T111/T112) |
| `spyder port-forward <device> start\|stop\|list …` | REST proxy to `port_forward_*`. | Stable (🎯T111/T112) |
| `spyder input-tap <device> --x N --y N [--as OWNER]` | REST proxy to `input_tap`. Android only; iOS fails closed. | Stable (🎯T111/T112) |
| `spyder input-swipe <device> --x1 N --y1 N --x2 N --y2 N [--duration-ms N] [--as OWNER]` | REST proxy to `input_swipe`. | Stable (🎯T111/T112) |
| `spyder app-perf-get [--session-id ID] [--json]` | REST proxy to `app_perf_get`. | Stable (🎯T110) |
| `spyder wait-state [--session-id ID \| --device D --bundle-id B] --slice S [--select JQ] [--timeout-ms N] [--poll-ms N] [--json]` | REST proxy to `wait_state`. | Stable (🎯T129) |
| `spyder device-setting <device> set\|restore\|get --key refresh_rate [--value N] [--as OWNER] [--json]` | REST proxy to `device_setting`. | Stable (🎯T130) |
| `spyder pool list [--json]` | REST proxy to `pool_list`. | Needs review |
| `spyder pool warm <template> [--count N]` | REST proxy to `pool_warm`. `--count` defaults to 1. | Needs review |
| `spyder pool drain <template>` | REST proxy to `pool_drain`. | Needs review |
| `spyder list-scripts [--json]` | REST proxy to `list_scripts` — durable host Starlark library (bundled + `~/.spyder/scripts`). 🎯T108. | Stable (🎯T108) |
| `spyder run-script <name\|path> [k=v]... [--param k=v] [--max-duration-ms N] [--json]` | REST proxy to `run_script` / `app_exec(script_path=…)`. 🎯T108. | Stable (🎯T108) |
| `spyder secret status --studio squz\|minicades` | Codesign principal + per-studio envelope status (no secret material). macOS only; unsigned binaries refuse unless `SPYDER_ALLOW_UNSIGNED_SECRETS=1` (test-only). | Beta (🎯T133) |
| `spyder secret import --studio …` | Clipboard absorb into keychain; optional live ASC/Play verify. | Beta (🎯T133) |
| `spyder secret mint --studio … --kind play-upload` | Mint Play upload PKCS12 into envelope. | Beta (🎯T133) |
| `spyder secret missing --studio … --for match\|pilot\|deliver\|supply\|firebase` | Preflight missing secret kinds for a lane class. | Beta (🎯T133) |
| `spyder fastlane [--studio …] [--confirm] [--dry-run] -- <action> [args…]` | Wraps `bundle exec fastlane` with secrets only in child env; lane-class gates + audit JSONL. | Beta (🎯T133) |
| `spyder ship-audit` | Lists recent ship audit entries and unfilled reflection stubs under `~/.spyder/ship-audit/`. | Beta (🎯T133) |

All device-tool subcommands POST to `$SPYDER_DAEMON_URL` (default
`http://127.0.0.1:3030`) and print the first text content block
(text tools) or write a PNG to `--output` (`screenshot`; default
`<device>-<ts>.png`). `--as OWNER` defaults to `filepath.Base(cwd)`.
The CLI is a convenience subset of the verb table, not 1:1 with it.

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
  `screenshot` / `app_screenshot` default to a text JSON path dict
  (🎯T114); `inline=true` is the image-block path.
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
string, e.g. `ipad-landscape`. The store is opaque to the key's content.
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

- **iOS foreground-app detection.** Not currently surfaced — the
  BackBoard `applicationStateNotification:` subscription was the source
  before the autoawake removal but pinned the underlying DTX connection
  via a go-ios `Close()` upstream bug (see 🎯T64). `device_state.foreground_app`
  is iOS-empty until a leak-free path lands.
- **iOS thermal state.** `thermal_state` is always empty on iOS 17.4+
  because MobileGestalt was deprecated. Alternative source (`dumpsys
  thermalservice` analog? `sysmon`?) is open research.
- **Android app metadata.** `list_apps` on Android returns bundle IDs only.
  Name/version parity via per-package `dumpsys` is feasible but deferred.
- **Android thermal state.** Not yet wired — `dumpsys thermalservice` is
  available, just not parsed.
- **iOS subprocess dependency.** iOS device operations run in-process
  via the [go-ios](https://github.com/danielpaulus/go-ios) Go module
  (`installationproxy`, `instruments`, `appservice`, `screenshotr`,
  `crashreport`, `syslog_relay`, `zipconduit`, plus the lockdown /
  DTX / RSD foundation). The only iOS child process is the bundled
  `ios tunnel start --userspace` daemon (also go-ios), spawned by
  spyder at startup and reaped on shutdown — no privileged
  LaunchDaemon required.
- **iOS keep-awake.** The daemon-side autoawake convergence supervisor
  was removed in v0.40.0 because go-ios's
  `instruments.ListenAppStateNotifications` `Close()` doesn't actually
  close the underlying DTX connection, leaking a TCP connection per
  convergence cycle and eventually wedging the daemon. A standalone
  `ios/KeepAwake` sidecar is available for manual Xcode install on
  devices without an Auto-Lock=Never option; spyder does not auto-install
  or supervise it. See 🎯T64 for the investigation-and-reinstate target.
- **Tunnel daemon lifecycle.** iOS 17+ DVT operations
  (screenshot, app_state, foreground_app, ProcessControl, etc.)
  need an active RSD tunnel per device. spyder spawns the bundled
  `ios tunnel start --userspace` as a child process at startup and
  reaps it on shutdown — no system LaunchDaemon, no sudo, no
  privileged TUN device. The tunnel registry exposes a loopback
  HTTP API on `127.0.0.1:60105`; goios.Resolver queries it per
  device to obtain the RSD address+port and complete the
  handshake. If the bundled `ios` binary isn't found at startup
  spyder logs a warning and proceeds without a tunnel —
  iOS-17+ DVT-dependent tools then fail per-call with a clear
  error rather than crashing the daemon. Pre-iOS-17 devices that
  don't need RSD continue to work over usbmux.
- **macOS-only host enforcement.** Spyder runs on Linux but iOS operations
  will fail noisily there. Either restrict the binary to Darwin or
  gracefully degrade iOS-related tools with a clear "host does not support
  iOS" error.
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
- Screen-recording on **iOS physical devices** — go-ios and
  `devicectl` do not expose a clean path for video capture on real
  devices at this time. `record_start` on a physical iOS device returns
  an immediate error: `"screen recording is not supported on iOS
  physical devices; use a simulator"`. Use `xcrun simctl list devices`
  to pick a simulator UDID instead.
- Wireless-ADB pairing / discovery — assumed set up externally (spyder
  inherits `adb devices`).
- Auto-install of a companion app on Android — Android handles stay-awake
  natively via Developer Settings; no companion app is needed.

## Health & diagnostics surfaces (🎯T90 / 🎯T99)

| Surface | Contract | Stability |
|---|---|---|
| `spyder status` / `spyder status --json` | Live health model snapshot (daemon-self, subprocesses, devices). | **Stable** |
| `GET /api/v1/health` | Same model over REST (JSON). | **Stable** |
| `health()` app_exec builtin | Same model as a Starlark dict. | **Stable** |
| `spyder doctor` / `spyder doctor --fix` | Cross-check diagnosis; findings should align with the health model for wedge state. | **Stable** |
| `spyder doctor --install-sudoers` | Installs sudoers for `spyder-killusbmuxd` (usbmuxd wedge recovery). Documented agent path. | **Stable** |
| SIGQUIT → `~/.spyder/goroutine-*.txt` | Full goroutine dump without process exit (🎯T99.5). | **Stable** |
| Tool-class dispatch deadlines | Fast read ≤15s, device op ≤60s, install ≤5m (tunable); structured timeout + session invalidate (🎯T99.1). | **Stable** |

In-flight tool calls are tracked for diagnosis (`Handler.InFlightOps`); wedge snapshots continue under `~/.spyder/wedge-snapshots/`.
