# Agent guide: spyder

Spyder is an HTTP-based MCP server that owns session state for real-device mobile
development: symbolic device aliases, live device facts (battery, charging,
foreground app), screenshots, app lifecycle, and power-assertion management via
the bundled pmd3 bridge to prevent device auto-lock.

Spyder sits *above* [mobile-mcp](https://github.com/mobile-next/mobile-mcp) and
[XcodeBuildMCP](https://github.com/getsentry/XcodeBuildMCP): those tools drive
the device; spyder remembers what the device *is* and wraps the workflow around
it. In particular, spyder handles iOS physical devices cleanly via
`pymobiledevice3` where mobile-mcp's WebDriverAgent path often fails.

## Installation (not a one-liner — do **all** the steps)

Installing spyder is a **multi-step process**. Do not stop after `brew install` —
the MCP server won't be wired up until every step below has run and your agent
session has been restarted.

```bash
# 1. Install the binary.
brew install marcelocantos/tap/spyder

# 2. Start the persistent server.
brew services start spyder

# 3. Register with Claude Code (user-scope; HTTP transport).
claude mcp add --scope user --transport http spyder http://localhost:3030/mcp

# 4. Restart the agent session — MCP servers are loaded at session start.
```

### Verifying the install

**Do not `curl` the MCP endpoint.** MCP uses JSON-RPC over HTTP; a plain `GET` or
empty `POST` returns nothing useful, and agents routinely misread that as "server
not ready" and enter a diagnostic loop. Use these checks instead:

```bash
# Pre-restart: is the process listening on :3030?
lsof -iTCP:3030 -sTCP:LISTEN

# Post-restart, from inside the agent session: call any spyder tool.
# (devices with platform=all is the lightest ping.)
```

If `lsof` returns nothing, the service isn't running — check
`brew services list | grep spyder` and `brew services restart spyder`.

### Generic MCP client config

For agents that configure MCP servers via a JSON file rather than
`claude mcp add`:

```json
{
  "mcpServers": {
    "spyder": {
      "type": "http",
      "url": "http://localhost:3030/mcp"
    }
  }
}
```

## Device inventory

Spyder reads `~/.spyder/inventory.json` — a JSON array of `Entry` records that
map symbolic aliases to platform-specific identifiers:

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

- `ios_uuid` — hardware UDID (from `pymobiledevice3 usbmux list` or
  `xcrun xctrace list devices`).
- `ios_coredevice` — CoreDevice UUID from `devicectl list devices`.
- `android_serial` — adb serial from `adb devices`.
- A missing inventory file is treated as empty, not an error.

Alias lookup is case-insensitive. Raw identifiers that aren't in the inventory
are classified by format (iOS UDID vs. Android serial) and passed through.

## Tool reference

All tools accept a `device` parameter that resolves against the inventory
(alias, raw UUID, or raw serial). The exception is `devices`, which lists
everything it can see.

| Tool | Purpose | Notes |
|---|---|---|
| `devices` | List connected iOS + Android devices, annotated with inventory alias. | `platform` filter: `ios`, `android`, or `all` (default). |
| `resolve` | Symbolic name → structured `Entry` with all known IDs. | Unknown raw inputs are echoed back classified. |
| `device_state` | Battery level, charging, thermal state, foreground app. | 2-second TTL cache. Thermal is currently a note on iOS 17.4+ (MobileGestalt deprecated). |
| `screenshot` | PNG of the current screen, returned inline as an image content block. | iOS uses `pymobiledevice3 developer dvt screenshot` (needs tunneld); Android uses `adb shell screencap`. |
| `list_apps` | Installed third-party apps. iOS returns bundle ID + name + version; Android returns bundle ID only. | |
| `launch_app` | Foreground an arbitrary app by bundle id. | iOS uses DVT launch (needs tunneld); Android uses `adb monkey -c LAUNCHER`. |
| `terminate_app` | Stop an app by bundle id. | iOS: resolve PID via DVT, then kill. Android: `adb am force-stop`. |
| `rotate` | Rotate an iOS simulator or Android emulator to a named orientation. Physical iOS/Android devices return a clear error. | Orientations: `portrait`, `landscape-left`, `landscape-right`, `portrait-upside-down`. iOS uses `xcrun simctl io <udid> rotate`; Android uses `adb emu rotate` (driven N times to reach the target). |
| `install_app` | Install a .app/.ipa (iOS) or .apk (Android). Path must not contain `..` and must exist. | iOS: `xcrun devicectl device install app`; Android: `adb install -r`. |
| `uninstall_app` | Remove an app by bundle id / package name. | iOS: `xcrun devicectl device uninstall app --bundle-identifier`; Android: `adb uninstall`. |
| `deploy_app` | Atomic deploy: terminate → install → launch → verify pid. Returns `{bundle_id, pid}`. | `bundle_id` is derived from Info.plist (iOS) or `aapt dump badging` (Android) if not supplied. iOS needs tunneld for launch + pid-verify. Fail-fast on install error; "not running" from terminate is ignored. |
| `reserve` | Acquire an exclusive device hold. | Supply `device` (literal pin) **or** `selector` (fuzzy JSON predicate) — not both. `owner` is always required. Default TTL 3600 s, max 86400 s. Same-owner re-acquires renew in place. See "Fuzzy reservation" section for selector schema and worked examples. |
| `release` | Free a reservation. | `{device, owner}`. Non-owner releases conflict. Also stops any active recording owned by the releaser. |
| `release` | Free a reservation. | `{device, owner}`. Non-owner releases conflict. Any applied network profile is cleared automatically. |
| `renew` | Extend a reservation's TTL. | `{device, owner, ttl_seconds?}`. |
| `reservations` | List active reservations. | Read-only. |
| `runs_list` | List run-artefact bundles under `~/.spyder/runs/`, newest first. | Read-only. |
| `runs_show` | Return a single run's full manifest (device, owner, timestamps, artefacts). | Read-only. `{run_id}`. |
| `baseline_update` | Store a new visual baseline for `suite/case/variant`. | Supply PNG via `screenshot_path` or `screenshot_base64`. Optional `manifest` JSON enables structural diffing. |
| `diff` | Compare a candidate screenshot against the stored baseline. | Returns a structured JSON report with RMS pixel error, manifest structural diff (added/removed/moved elements with bounding boxes), and a `pass` verdict. Supply PNG via `screenshot_path` or `screenshot_base64`. |
| `baselines_list` | List all baselines stored for a suite. | Read-only. Returns `[{case, variant, has_png, has_manifest}]`. |
| `sim_list` | List all iOS simulators (UDID, name, state, runtime). Booted sims appear in `devices`. | `state` filter: `Booted`, `Shutdown`, etc. |
| `sim_create` | Create a new iOS simulator; returns its UDID. | `{name, device_type_id, runtime_id}`. IDs from `xcrun simctl list devicetypes/runtimes --json`. |
| `sim_boot` | Boot an iOS simulator by UDID. | `{udid}`. Sim appears in `devices` once booted. |
| `sim_shutdown` | Shut down an iOS simulator by UDID. | `{udid}`. |
| `sim_delete` | Delete an iOS simulator by UDID (must be shut down first). | `{udid}`. Irreversible. |
| `emu_list` | List all Android Virtual Devices (name, path, target, ABI). Booted emus appear in `devices`. | Read-only. |
| `emu_create` | Create a new Android AVD. | `{name, system_image, device_profile}`. System image must be pre-installed. |
| `emu_boot` | Start an Android emulator (headless). Appears in `devices` once fully booted (~30–90 s). | `{name}`. Returns the serial once the process is launched. |
| `emu_shutdown` | Shut down an Android emulator by serial (e.g. `emulator-5554`). | `{serial}`. Sends `adb emu kill`. |
| `emu_delete` | Delete an AVD by name. | `{name}`. Irreversible. |
| `pool_list` | Current pool state for all templates (available/running/reserved counts per template). | Read-only. Pool must be configured via `~/.spyder/pool.yaml`. |
| `pool_warm` | Force pre-boot N additional instances for a template. | `{template, count}`. Moves instances from available to running tier. |
| `pool_drain` | Shut down and delete all idle instances for a template. | `{template}`. Reserved instances are terminated first. |

## Sim/emu pool

The pool manages a collection of pre-created and optionally pre-booted
sim/emu instances so that test runs get a clean device in milliseconds
rather than seconds.

**Client API is minimal by design.** Agents only call `reserve`/`release`.
The pool handles all lifecycle decisions: linger timing, warm-pool sizing,
mint vs. reuse, and shutdown scheduling. No per-reservation knobs.

### Configuring the pool

Create `~/.spyder/pool.yaml`:

```yaml
templates:
  - name: iphone16
    platform: ios
    device_type: com.apple.CoreSimulator.SimDeviceType.iPhone-16
    runtime_or_system_image: com.apple.CoreSimulator.SimRuntime.iOS-18-3
    tags: [ci, ios, iphone]
    available_min: 2      # always keep ≥ 2 created on disk
    available_max: 4      # never keep > 4 shutdown instances
    running_warm: 1       # keep 1 pre-booted and idle
    linger_seconds: 120   # keep running for 2 min after release

  - name: pixel9
    platform: android
    # device_type is the AVD template name for Android (used as clone source)
    device_type: Pixel9_API35_template
    runtime_or_system_image: "system-images;android-35;google_apis;arm64-v8a"
    tags: [ci, android, phone]
    available_min: 1
    available_max: 3
    running_warm: 0
    linger_seconds: 60
```

Restart the daemon after creating or modifying `pool.yaml`. The daemon
reconciles on startup (background goroutine; startup is non-blocking).

**Global linger override**: set `SPYDER_POOL_LINGER_SECONDS` in the
environment to override the default (120 s) for all templates that don't
have a per-template `linger_seconds` value.

### Readiness tiers

| Tier | State | Acquisition latency |
|---|---|---|
| `running` | Booted, idle | ~milliseconds (OS already warm) |
| `available` | Created on disk, not booted | ~5–30 s (simctl/emulator boot) |
| `reserved` | Handed off to a caller | — |

On `Acquire`, the pool prefers `running` → boots an `available` → mints a
new one. On `Release`, the instance stays in `running` for the linger period
so the next `Acquire` in the window gets near-instant handoff. After linger
expires, the instance transitions to `available` (shutdown, disk kept) unless
the `available` tier is at cap — in which case it is deleted.

### Pool tools

```bash
# Inspect the current pool state.
spyder pool list

# Pre-boot 2 extra instances for a template.
spyder pool warm iphone16 --count 2

# Drain all idle instances for a template (reclaim disk/memory).
spyder pool drain iphone16
```

Via MCP:

```json
{"name": "pool_list", "arguments": {}}
{"name": "pool_warm", "arguments": {"template": "iphone16", "count": 2}}
{"name": "pool_drain", "arguments": {"template": "pixel9"}}
```

### Android AVD cloning

Each Android pool instance is cloned from the `device_type` AVD template:

1. `~/.android/avd/<template>.avd/` → `~/.android/avd/<clone>.avd/`
2. `~/.android/avd/<template>.ini` → `~/.android/avd/<clone>.ini` (path= rewritten)
3. `config.ini` AvdId / displayname rewritten to the clone name.

The template AVD must be created manually with `avdmanager create avd`. The
pool never modifies the template; it only reads from it.

### iOS simulator cloning

Each iOS pool instance is a fresh `xcrun simctl create` from the same
`device_type` + `runtime_or_system_image`. This gives a clean, independent
UDID each time. There is no template AVD to pre-create for iOS.

## Simulator and emulator lifecycle

iOS simulators and Android emulators are managed through dedicated tool groups (`sim_*` and `emu_*`). Booted simulators and running emulators appear automatically in `spyder devices` output — the existing iOS adapter calls `xcrun simctl` which includes both physical and simulator devices, and the Android adapter calls `adb devices` which lists running emulators alongside physical devices.

### iOS simulators

```bash
# List all simulators (state: Booted, Shutdown, etc.)
# {"name":"sim_list","arguments":{}}

# Create a new simulator
# {"name":"sim_create","arguments":{"name":"MyTestPhone",
#   "device_type_id":"com.apple.CoreSimulator.SimDeviceType.iPhone-15",
#   "runtime_id":"com.apple.CoreSimulator.SimRuntime.iOS-17-5"}}

# Boot and shut down
# {"name":"sim_boot","arguments":{"udid":"ABCD-1234-..."}}
# {"name":"sim_shutdown","arguments":{"udid":"ABCD-1234-..."}}
```

CLI:

```bash
spyder sim list
spyder sim list --state Booted --json
spyder sim create MyPhone \
  --type com.apple.CoreSimulator.SimDeviceType.iPhone-15 \
  --runtime com.apple.CoreSimulator.SimRuntime.iOS-17-5
spyder sim boot <udid>
spyder sim shutdown <udid>
spyder sim delete <udid>
```

### Android emulators

```bash
# List AVDs
# {"name":"emu_list","arguments":{}}

# Boot an emulator (headless; takes 30–90 s to fully start)
# {"name":"emu_boot","arguments":{"name":"Pixel6_API34"}}

# Shut down by adb serial
# {"name":"emu_shutdown","arguments":{"serial":"emulator-5554"}}
```

CLI:

```bash
spyder emu list --json
spyder emu create Pixel6_API34 \
  --image 'system-images;android-34;google_apis;arm64-v8a' --device pixel_6
spyder emu boot Pixel6_API34
spyder emu shutdown emulator-5554
spyder emu delete Pixel6_API34
```

### Boot-on-demand / reservation policy

You can reserve a device identifier (alias, UDID, or AVD name) before it is booted — the reservation is just a named hold on a string key; spyder does not enforce that the device is currently connected or running. This allows workflows to pre-claim a simulator slot and boot on demand:

1. `reserve` the sim/emu name.
2. `sim_boot` or `emu_boot` to start it.
3. Wait for it to appear in `devices` (simulators: immediate; emulators: poll `spyder devices --platform android` until the serial appears).
4. Use the booted device's UDID or serial for operations.
5. `release` when done (optionally shut it down first).

**Operations that target a device must use the live UDID/serial**, not the AVD name. The `devices` tool returns the identifier once the sim/emu is booted.

## Visual regression

Spyder ships a baseline store (`~/.spyder/baselines/`) and a two-tier
comparison pipeline:

1. **Manifest diff (structural)** — when both the baseline and the candidate
   carry a UI-element manifest, spyder reports added / removed / moved elements
   with their bounding boxes. This tier catches layout regressions that pixel
   RMS misses (e.g. an element moved by 2 px in a noisy background).
2. **Pixel diff (RMS)** — root-mean-square error across all channels, in [0, 1].
   Configurable tolerance (default 0.01). SSIM is stubbed in v1 (returns NaN).

The `diff` tool runs both tiers and returns a unified report. If both sides have
a manifest, structural changes cause `pass=false` regardless of RMS. The VLM
natural-language summary interface is defined but not implemented in v1.

### Manifest schema

Manifests are JSON objects with this shape:

```json
{
  "schema_version": 1,
  "elements": [
    {
      "id":    "com.example.app/MainScreen/loginButton",
      "kind":  "button",
      "bbox":  [x, y, width, height],
      "attrs": { "label": "Log In", "enabled": true }
    }
  ]
}
```

- `id`: stable unique key within a screen (convention: `<bundle>/<screen>/<name>`).
- `kind`: semantic type — `button`, `label`, `image`, `textfield`, `container`, etc.
- `bbox`: `[x, y, width, height]` in logical pixels, top-left origin.
- `attrs`: free-form attribute bag (text, enabled state, accessibility label, …).

### Typical workflow

```bash
# 1. Capture a reference screenshot and store as baseline.
spyder screenshot Pippa --output login.png
spyder baseline update login-flow/main-screen login.png

# 2. Later, compare a new screenshot against the baseline.
spyder diff login-flow/main-screen new-screenshot.png
# exit 0 → pass; exit 1 → fail (structural or pixel regression)

# 3. With a manifest for richer structural diffing:
spyder baseline update login-flow/main-screen login.png manifest.json
spyder diff login-flow/main-screen new-screenshot.png new-manifest.json

# 4. Use --variant for per-device or per-orientation separation:
spyder baseline update login-flow/main-screen login.png --variant pippa-landscape
spyder diff login-flow/main-screen new-screenshot.png --variant pippa-landscape
```

Via MCP, the same operations are:

```json
{"name": "baseline_update", "arguments": {"suite": "login-flow", "case": "main-screen", "screenshot_path": "/tmp/login.png"}}
{"name": "diff", "arguments": {"suite": "login-flow", "case": "main-screen", "screenshot_path": "/tmp/new.png"}}
```
| `record_start` | Begin a screen recording (mp4). Returns immediately; recording runs in background. | `{device, owner?}`. iOS simulators only — physical devices return an immediate error. Only one recording per device at a time. Reservation-gated. |
| `record_stop` | Stop the active recording and return the local mp4 path. | `{device, owner?}`. Waits for the recorder to flush. On Android, pulls the file from the device. |
| `network` | Apply or clear network condition shaping. | `{device, owner, profile?}` or `{device, owner, clear:true}`. Android emulators only — see gotchas below. |
| `logs` | Fetch log lines between two timestamps. | Read-only. iOS uses `pymobiledevice3 syslog live`; Android uses `adb logcat`. For live tailing use REST SSE (see below). |

### Log queries — range vs. live

The `logs` MCP tool returns a bounded JSON array of `LogLine` records. It
accepts:

- `device` (required) — alias or UUID.
- `since` / `until` — RFC3339 timestamps (e.g. `2026-04-19T14:00:00Z`). Both
  optional. When `since` is omitted, iOS collects from a short live window
  (≤5 s); Android uses `-d` (dump buffer then exit).
- `process` — filter by process name (iOS: `--procname`; Android: tag match).
- `subsystem` — iOS only (`--subsystem com.apple.foo`). Ignored on Android.
- `tag` — Android logcat tag (e.g. `MyApp`). Ignored on iOS.
- `regex` — regex applied to the message body (both platforms, client-side).

**MCP transport does not support streaming.** For live log tailing, use the
REST SSE endpoint instead:

```bash
# Live tail — server-sent events, each line is a JSON LogLine.
curl -N -X POST http://127.0.0.1:3030/api/v1/log_stream \
  -H 'Content-Type: application/json' \
  -d '{"device":"Pippa","process":"MyApp","regex":"error"}'
```

Or via the CLI:

```bash
spyder log Pippa --follow                          # live tail
spyder log Pippa --follow --process MyApp          # process filter
spyder log Pippa --since 2026-04-19T00:00:00Z     # bounded range
spyder log Pippa --regex "crash|panic"             # regex on message
```

**Platform quirks:**

- **iOS range queries** run `pymobiledevice3 syslog live` for up to 5 seconds
  and collect lines in the window. This is adequate for post-hoc debugging but
  is not a true archived-log query. For long-span queries run multiple short
  windows or use `--follow` and let the stream run while reproducing.
- **iOS timestamp precision** — pymobiledevice3's syslog output omits the year;
  spyder assumes the current year. Lines near midnight on New Year's may be
  misclassified by one year.
- **Android tag filter** — logcat `-s <tag>:V *:S` suppresses all other tags.
  Combining tag + regex is the most targeted approach.
- **Android process filter** — there is no direct process-name filter in logcat;
  spyder does a case-insensitive substring match on the tag column as a proxy.

## Reservations

For parallel dev sessions (e.g. one agent working on TiltBuggy while another
works on another game), acquire an exclusive hold on a device with `reserve`
before mutating operations. Mutating tools (`screenshot`,
`launch_app`, `terminate_app`) reject with a structured conflict error
naming the holder if someone else is holding the device. Read tools
(`devices`, `resolve`, `device_state`, `reservations`) are unaffected.

### Literal device reservation

Pin a specific device by alias or UUID:

```json
{"name": "reserve", "arguments": {"device": "Pippa", "owner": "tiltbuggy", "ttl_seconds": 3600, "note": "UI regression run"}}
```

### Fuzzy reservation (selector)

When you don't need a specific device — just *any* iOS iPad, or *any* Android
phone with API ≥ 33 — pass a `selector` instead of `device`. The server
resolves the selector against the live device set and inventory, picks the
best available candidate, and returns a reservation bound to a concrete UUID.
**The caller never has to know which device was picked.**

#### Selector schema

```json
{
  "platform":            "ios | android",          // required
  "model_family":        "ipad | iphone | phone | tablet | ...",  // optional
  "os_min":              "17.3",                   // optional, inclusive lower bound
  "os_max":              "17.9",                   // optional, inclusive upper bound
  "orientation_capable": true,                     // optional; requires sim/emu
  "tags":                ["arm64", "ci"],           // optional; all must be present
  "attrs":               {"env": "staging"}        // optional; exact key/value match
}
```

`model_family` is matched case-insensitively against the `model` field returned
by `spyder devices` and against the `tags` array on the inventory entry. This
means you can add `"ipad"` to the `tags` of a physical iPad entry to make it
participate in `model_family: ipad` selection.

`orientation_capable` requires that the candidate supports programmatic
rotation (i.e. is a simulator or emulator). Physical devices are excluded
because rotation on physical hardware is a sensor, not a software-controlled
feature.

`tags` and `attrs` are matched against the inventory entry. Inventory entries
can now carry these optional fields:

```json
{
  "alias": "Pippa",
  "platform": "ios",
  "ios_uuid": "00008103-000D39301A6A201E",
  "tags": ["ipad", "arm64"],
  "attrs": {"env": "ci", "zone": "lab-a"}
}
```

#### Resolution preference order

1. **Idle physical device** matching all predicate fields.
2. **Idle sim/emu** from the pool (🎯T24, not yet wired — skipped when pool unavailable).
3. **Error** with structured near-miss detail (up to 3 near-misses, each naming the one predicate that failed).

#### Worked examples

iOS iPad (any):

```json
{"name": "reserve", "arguments": {
  "selector": "{\"platform\":\"ios\",\"model_family\":\"ipad\"}",
  "owner": "tiltbuggy"
}}
```

Android phone with API ≥ 33:

```json
{"name": "reserve", "arguments": {
  "selector": "{\"platform\":\"android\",\"model_family\":\"phone\",\"os_min\":\"33\"}",
  "owner": "tiltbuggy"
}}
```

iOS simulator only (for rotation tests):

```json
{"name": "reserve", "arguments": {
  "selector": "{\"platform\":\"ios\",\"orientation_capable\":true}",
  "owner": "tiltbuggy"
}}
```

Device with CI-environment tag:

```json
{"name": "reserve", "arguments": {
  "selector": "{\"platform\":\"ios\",\"tags\":[\"ci\"]}",
  "owner": "tiltbuggy"
}}
```

#### CLI

```bash
# Selector JSON
spyder reserve --selector '{"platform":"ios","model_family":"ipad"}' --as tiltbuggy

# Shorthand flags (equivalent)
spyder reserve --platform ios --model ipad --as tiltbuggy

# With tags
spyder reserve --platform android --tag arm64 --tag ci --as tiltbuggy

# Literal device (unchanged)
spyder reserve Pippa --as tiltbuggy
```

Agents don't *have* to reserve: if the device is free, mutating calls just
work. Reservations are only necessary for long-running sequences where
another session could race you. `spyder run` auto-reserves for the wrapped
command's lifetime (owner defaults to `filepath.Base(cwd)`), opportunistically
renews, and releases on exit — no explicit reserve/release needed for the
common test-run pattern.

To pass owner-authentication on a mutating call while someone else holds
the device, pass `"owner": "<your-owner>"` in the arguments map. The
server resolves canonical identity via the inventory, so reserving
"Pippa" also blocks operations on her raw UDID and vice versa.

## Run-artefact store

Every successful `reserve` opens a **run** — a directory under
`~/.spyder/runs/<run-id>/` that collects screenshots, recordings, logs,
and crash reports captured during that reservation. `release` (and the
equivalent `spyder run` exit path) closes the run. The run's
`manifest.json` enumerates every artefact with its source tool,
timestamp, mime type, and size.

Currently the `screenshot` tool writes its PNG into the active run's
directory in addition to returning it inline. Future tools (recording,
log capture, crash collection) will follow the same convention.

```bash
# List all runs, newest first.
spyder runs list

# Inspect one run's full manifest.
spyder runs show 20260419-143022-a3f1b2

# Just the artefacts table for a run.
spyder runs artefacts 20260419-143022-a3f1b2
```

Retention is enforced when the daemon starts up. Two bounds, either
optional, configured via environment:

- `SPYDER_RUNS_MAX_AGE_DAYS` — default 30. Closed runs older than this
  are deleted. Open runs are never pruned by age.
- `SPYDER_RUNS_MAX_SIZE_GB` — default 20. If total artefact bytes
  exceed this, oldest closed runs are deleted until the store is under
  the cap.

Set either to `0` to disable that bound. When spyder is run as a
Homebrew service, use `launchctl setenv` to inject env vars into the
service (see **1. PATH** above for the pattern).

## Network condition shaping

The `network` tool applies named network profiles to emulators for
streaming-protocol testing (adaptive bitrate, reconnection, loss recovery).

### Supported platforms

| Platform | Support | Notes |
|---|---|---|
| Android emulator (avd) | Full (speed + delay) | Via `adb emu network speed/delay`. |
| Android physical device | Not supported | adb console commands are emulator-only. |
| iOS simulator | Not supported | No public CLI for Link Conditioner. Contributions welcome. |
| iOS physical device | Not supported | No remote interface to Developer Settings. |

### Named profiles

| Profile | Up kbps | Down kbps | Delay ms | Notes |
|---|---|---|---|---|
| `wifi` | unlimited | unlimited | 0 | Full speed — removes any applied throttle. |
| `4g` | 5760 | 14400 | 20 | HSPA+ class. adb keyword: `hsdpa`. |
| `3g` | 384 | 2000 | 100 | UMTS class. adb keyword: `umts`. |
| `edge` | 128 | 384 | 400 | EDGE/2.75G. adb keyword: `edge`. |
| `gsm` | 40 | 114 | 600 | GPRS. adb keyword: `gprs`. |
| `offline` | 0 | 0 | — | No connectivity (speed=0). |
| `lossy-<pct>` | unlimited | unlimited | 0 | **Partial** — speed/delay only; loss not implemented by adb console. Error returned. |
| `delay-<ms>` | unlimited | unlimited | `<ms>` | Extra one-way latency only. |

### Usage

```json
// Apply a profile
{"name": "network", "arguments": {"device": "Pixel8", "owner": "myagent", "profile": "3g"}}

// Clear — restore full speed
{"name": "network", "arguments": {"device": "Pixel8", "owner": "myagent", "clear": true}}
```

CLI equivalent:

```bash
spyder net Pixel8 --profile 3g --as myagent
spyder net Pixel8 --clear --as myagent
```

### Automatic cleanup on release

When a reservation is released (via `release` or `spyder run` exit), spyder
attempts to clear any network profile applied by the same owner. This is
best-effort: if the daemon exits abnormally before the release call, the
emulator retains the last applied profile until the next explicit `clear`,
the next `spyder serve` session that clears it, or the emulator is restarted.
Always prefer a `clear` or `release` call to clean up rather than relying on
daemon restart.

### Common gotchas

- **Android physical device gets "KO: not supported"** → `adb emu network`
  commands go through the emulator's control socket. Physical devices don't
  expose one; use Android Studio's network profiler or a host-level traffic
  shaper (`tc`, `dummynet`, `Charles Proxy`) for real hardware.
- **iOS simulator returns "not yet implemented"** → correct and intentional.
  Link Conditioner is macOS-host-level (affects all traffic), not per-simulator.
  A per-simulator shaping solution would need private CoreSimulator APIs.
  PRs welcome.
- **`lossy-<pct>` profile returns an error** → partially applied (speed/delay
  set correctly) but the adb emulator console has no packet-loss knob. Use
  a host-level traffic shaper for loss simulation on Android.
- **Profile persists after daemon crash** → the emulator is stateful; clear
  manually via `spyder net <device> --clear` after restarting the daemon.

## REST API and CLI subcommands

Every MCP tool is also exposed over plain HTTP+JSON on the same
listener, at `POST /api/v1/<tool>`. The request body is the tool's
arguments (same as MCP); the response is a JSON-encoded
`mcp.CallToolResult`:

```bash
# Shell scripts: call a tool directly.
curl -s -X POST http://127.0.0.1:3030/api/v1/devices \
  -H 'Content-Type: application/json' -d '{"platform":"android"}'

# Zero-arg tools accept an empty body.
curl -s -X POST http://127.0.0.1:3030/api/v1/reservations
```

The same surface is available as `spyder` subcommands, which POST to
the local daemon and render the result:

```bash
spyder devices --platform ios --json
spyder screenshot Pippa --output /tmp/pippa.png
spyder reserve Pippa --ttl 600 --note "UI sweep"
spyder list-apps Pippa --json
spyder release Pippa
spyder runs list
spyder runs show 20260419-143022-a3f1b2
spyder sim list --json
spyder sim list --state Booted
spyder sim boot <udid>
spyder sim shutdown <udid>
spyder sim create MyPhone --type com.apple.CoreSimulator.SimDeviceType.iPhone-15 \
  --runtime com.apple.CoreSimulator.SimRuntime.iOS-17-5
spyder sim delete <udid>
spyder emu list --json
spyder emu boot Pixel6_API34
spyder emu shutdown emulator-5554
spyder emu create Pixel6_API34 \
  --image 'system-images;android-34;google_apis;arm64-v8a' --device pixel_6
spyder emu delete Pixel6_API34
spyder log Pippa --since 2026-04-19T00:00:00Z --json
spyder log Pippa --follow --process MyApp         # live SSE tail
```

`--as OWNER` defaults to `filepath.Base(cwd)` (same convention as
`spyder run`). Set `SPYDER_DAEMON_URL` to point the CLI at a
non-default daemon (e.g. `http://127.0.0.1:13030` during development).

Reservation state is shared between transports: an agent holding a
lock via MCP blocks a shell script hitting REST and vice versa.

## The `spyder run` test wrapper

Beyond the MCP surface, spyder exposes a CLI wrapper that runs a command
under an auto-acquired device reservation and releases it on exit (success
or failure):

```bash
spyder run -- xcodebuild -project MyApp.xcodeproj \
  -scheme MyApp -destination 'id=00008103-000D39301A6A201E' test
```

- Default device is `Pippa`. Override with `--device <alias-or-uuid>`.
- The wrapper forwards stdin/stdout/stderr and the command's exit code.
- Release failures are logged but do not mask the test's exit code.

## Auto-awake supervisor

`spyder serve` runs an always-on supervisor via the bundled pmd3 bridge.
Whenever a new paired iOS device appears, spyder acquires a
`PreventUserIdleSystemSleep` IOKit power assertion on the host. The assertion
is refreshed before it would expire and released when the device disconnects or
the daemon shuts down. No companion app to install, no on-device trust prompt,
no lock-state detection — the power assertion prevents auto-lock in the first
place.

## Environment and dependencies

- **macOS host.** Tested on macOS 15+ / Apple Silicon. Linux builds exist for
  spyder's non-iOS-specific surface (devices list, Android, the MCP server
  itself) but iOS operations will fail there.
- **`pymobiledevice3` ≥ 8.2** — iOS operations. The `pmd3-bridge` FastAPI
  subprocess (bundled at `libexec/pmd3-bridge/pmd3-bridge`) provides a
  persistent Unix-socket API over pmd3; spyder's Go daemon supervises it
  automatically and falls back to shell-out paths when the binary is absent.
- **`pymobiledevice3 remote tunneld`** — required for DVT operations on iOS 17+.
  Run as root (TUN/TAP interface). Spyder detects and uses an externally-managed
  instance by default; integrated supervision via `--supervise-tunneld` is
  planned but not yet wired.
- **`adb`** — Android operations.
- **`alerter`** — persistent macOS notifications for the locked-device prompt
  (fallbacks: `terminal-notifier`, `osascript`).

## Configuration

```bash
spyder serve                                  # default: 127.0.0.1:3030, HTTP MCP on /mcp (loopback only)
spyder serve --addr :3030                     # expose on all interfaces (caution: no auth; only on trusted networks)
spyder serve --tunneld-addr 127.0.0.1:49151   # non-default tunneld location
```

**Security note.** Spyder's MCP endpoint has no authentication; anyone
who can hit `http://<addr>:3030/mcp` can take screenshots, launch /
terminate apps, and hold reservations on your devices. The default
loopback bind is deliberate — external exposure is opt-in via
`--addr` and should only be used on trusted networks.

### Brew-services environment (launchd)

When spyder runs as a Homebrew service, launchd doesn't inherit your
shell env. The v0.3+ formula sets a default `PATH` that covers
`/opt/homebrew/bin` and the usual system paths. One thing may still
need manual setup:

**1. Non-Homebrew `pymobiledevice3`.** If your install lives outside
`/opt/homebrew/bin` (e.g. `pipx` in `~/.local/bin`, or a `uv`-managed
venv in `~/.py/bin`), either add a Homebrew-blessed install (preferred)
or override `PATH` for the spyder service:

```bash
launchctl setenv PATH "/opt/homebrew/bin:/Users/you/.py/bin:/usr/bin:/bin"
brew services restart spyder
```

`launchctl setenv` affects every user-level launchd job; an alternative
is editing `~/Library/LaunchAgents/homebrew.mxcl.spyder.plist` directly
with an `EnvironmentVariables` block (but Homebrew rewrites that plist
on reinstall, so it's transient).

## Screen recording

`record_start` / `record_stop` capture a video of the device's screen. Single
screenshots miss multi-frame visual bugs (rotation flashes, animation glitches,
transition artifacts). Use recording to capture a short mp4 around a dynamic
event.

```json
{"name": "record_start", "arguments": {"device": "iphone-16-sim", "owner": "tiltbuggy"}}
// … trigger the event …
{"name": "record_stop", "arguments": {"device": "iphone-16-sim", "owner": "tiltbuggy"}}
```

**Platform notes:**

- **iOS simulator**: Pass the simulator UDID directly (from `xcrun simctl list
  devices`). The alias inventory doesn't currently have a simulator type, so
  pass the raw UDID. Recording uses `xcrun simctl io <udid> recordVideo
  <dest.mp4>`.
- **iOS physical device**: Not supported. `record_start` returns an immediate
  error: `"screen recording is not supported on iOS physical devices; use a
  simulator"`. This is a platform limitation — `pymobiledevice3` and
  `devicectl` do not expose a recording API at this time.
- **Android device / emulator**: Uses `adb shell screenrecord --bit-rate
  4000000 /sdcard/spyder-recording.mp4`. The file is pulled to a local temp
  path on `record_stop`. Maximum native recording duration is 180 s per
  Android's `screenrecord` limit.

**Conflict detection**: Only one recording session per device at a time. A
second `record_start` on the same device returns a Conflict error naming the
current recorder's owner. The session is also stopped automatically when the
owner's reservation is released.

## Common gotchas

- **"tunneld unavailable"** in a tool error → start
  `sudo pymobiledevice3 remote tunneld` (or the systemd/launchd service that
  wraps it) and retry.
- **Device listed but operations fail with "device not connected"** → the
  device appears in the paired list (USB/WiFi pairing record exists) but isn't
  currently reachable. Plug it back in, unlock, or re-enable "Connect via
  network" in Xcode → Window → Devices and Simulators.
- **`launch_app` / `terminate_app` return `'Security'` DvtException** → the
  app's developer profile isn't trusted on this device. Go to Settings →
  General → VPN & Device Management → tap the developer name → Trust. Only
  applies to side-loaded / developer-signed apps. Auto-awake fires a
  persistent macOS alert for this case so you're not hunting through
  logs. Note: iOS discards the developer entry when the *only* app from
  that developer is uninstalled — reinstalling will require another
  Trust tap.
- **`launch_app` returns `'Locked'` DvtException on iOS** → unlock the device.
  Auto-awake fires a persistent macOS alert in this case.
- **`deploy_app` bundle_id auto-derivation (iOS)** → requires `plutil` (ships
  with macOS). Fails if the .app bundle has no `Info.plist` or
  `CFBundleIdentifier` is empty. Pass `bundle_id` explicitly to skip.
- **`deploy_app` bundle_id auto-derivation (Android)** → requires `aapt` from
  Android SDK build-tools in PATH. If absent, the error says to install it. Pass
  `bundle_id` explicitly to skip. The CLI equivalent is `--bundle-id`.
- **`install_app` path validation** → paths containing `..` are rejected at the
  handler layer before reaching the device. Use absolute paths or relative paths
  without traversal for reliability.
- **Already terminated race in `deploy_app`** → if the app is not running when
  deploy starts, the terminate step returns "not running" which is treated as
  success and the deploy continues. There is no race window: terminate and install
  are sequential within the same handler lock.
- **`brew services start spyder` + registration but no tools visible** → the
  agent session needs a restart. MCP servers are loaded at session start;
  mid-session registration doesn't take effect.

## Further reading

- `README.md` — human-facing install and feature overview.
- `STABILITY.md` — pre-1.0 interaction-surface catalogue and gaps.
- `docs/audit-log.md` — release audit trail.
