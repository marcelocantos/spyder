# Agent guide: spyder

Spyder is an HTTP-based MCP server that owns session state for real-device mobile
development: symbolic device aliases, live device facts (battery, charging,
foreground app), screenshots, app lifecycle, and reservations that serialise
parallel agent sessions on the same physical device.

Spyder sits *above* [mobile-mcp](https://github.com/mobile-next/mobile-mcp) and
[XcodeBuildMCP](https://github.com/getsentry/XcodeBuildMCP): those tools drive
the device; spyder remembers what the device *is* and wraps the workflow around
it. iOS physical-device support is in-process via the bundled
[go-ios](https://github.com/danielpaulus/go-ios) Go library — usbmux, lockdown,
DTX, and RSD all run inside spyder rather than fronting a Python subprocess.

## Reading this guide

Every device action goes through the single MCP tool `app_exec`. The Starlark
snippets shown throughout this guide are the value of the `script` argument —
e.g. a snippet `devices(platform="ios")` means:

```json
{"name": "app_exec", "arguments": {"script": "devices(platform=\"ios\")"}}
```

The REST transport (`POST /api/v1/<verb>`) is the one place individual verb
names still appear directly in URLs. All REST sections in this guide are
preserved as-is and are accurate for that transport.

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

# Post-restart, from inside the agent session: call devices via app_exec.
```

```json
{"name": "app_exec", "arguments": {"script": "devices(platform=\"all\")"}}
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
    "alias": "iPad",
    "platform": "ios",
    "ios_uuid": "00008103-001122334455667A",
    "ios_coredevice": "00000000-0000-0000-0000-000000000001",
    "notes": "Preferred iPad test device"
  }
]
```

- `ios_uuid` — hardware UDID (from `ios list` or
  `xcrun xctrace list devices`).
- `ios_coredevice` — CoreDevice UUID from `devicectl list devices`.
- `android_serial` — adb serial from `adb devices`.
- `executable_path` — for `"platform": "desktop"` entries, the binary spyder
  launches. It doubles as the desktop "device id". `working_dir` optionally
  overrides the launched process's cwd (default: the binary's own directory).
- A missing inventory file is treated as empty, not an error.

Alias lookup is case-insensitive. Raw identifiers that aren't in the inventory
are classified by format (iOS UDID vs. Android serial) and passed through.

### Desktop targets (🎯T85)

Spyder starts and monitors games on any medium — device, sim/emu, or the
**desktop host** — through the same tools. A desktop game is just a local
process: add a `"platform": "desktop"` entry and spyder runs and drives it
exactly like an iOS/Android app.

```json
{
  "alias": "tiltbuggy-desktop",
  "platform": "desktop",
  "executable_path": "/Users/me/ge/sample/tiltbuggy/bin/tiltbuggy"
}
```

Then launch and monitor it with the ordinary flow — spyder auto-injects
`SPYDER_APP_CHANNEL` (a `127.0.0.1` listener) so the app dials back:

```jsonc
// Start it. spyder execs the binary in its own process group with the
// app-channel env injected; the app connects and app_channel_list shows it.
devices(platform="desktop")                          // lists desktop entries
launch_app(device="tiltbuggy-desktop", bundle_id="com.squz.tiltbuggy")
// → app_state / app_log_get / app_screenshot / app_tweak_* / the dashboard
//   all work against it, no device required.
terminate_app(device="tiltbuggy-desktop", bundle_id="com.squz.tiltbuggy")
```

Desktop targets support the launch/monitor surface (List, launch_app,
terminate_app, is_running, app_pid, logs from captured stdout/stderr) plus the
full app-channel. Device-only operations (screenshot via DTX, rotate, crashes,
screen recording, install/uninstall, network shaping) return a clear
"not supported on desktop" — use the app-channel equivalents (e.g.
`app_screenshot`) instead.

## The `app_exec` entry point

`app_exec` is spyder's only MCP tool. It runs a Starlark script with every
spyder verb available as a builtin.

**Arguments:**

| Argument | Type | Description |
|---|---|---|
| `script` | string | Inline Starlark source. Provide this **or** `script_path`. |
| `script_path` | string | Durable library name (e.g. `skeleton`), `bundled:name`, or filesystem path to a `.star` file (🎯T108). |
| `params` | object | Optional string map injected as the Starlark global `params`. |
| `max_duration_ms` | number (optional) | Wall-clock cap in ms. Default 30 000; max 120 000. |

**Result model:** an ordered list of emitted values returned as MCP content
blocks. A value is emitted by `emit(x)` OR by being the **final top-level
expression** in the script (REPL-style "last value"). Intermediate bare
expressions are discarded — a bare `app_input(...)` in the middle of a script
produces no output; its acknowledgement is dropped.

- Image-returning verbs (`screenshot`, `app_screenshot`) emit image blocks.
- JSON-returning verbs return dicts/lists you can index directly:
  `d = app_state(slice="hud"); emit(d["score"])`.
- Text-returning verbs return strings.

**Controls:**

- `sleep(ms)` — server-side wall-clock delay, ms-accurate. The way to time a
  sequence; does not burn the Starlark step budget.
- `help()` — returns the verb list.
- `emit(x)` — emit a value explicitly. Required for all but the final value.
- `params` — dict of parameters for durable scripts (always present; may be empty).
- 🎯T108 asserts/L1: `assert_trajectory`, `assert_drag_follow`, `assert_settle`,
  `resolve_target`, `find_by_label` (fail closed with clear diagnostics).
- `list_scripts()` / `run_script(path=...)` — durable library (also CLI
  `spyder list-scripts` / `spyder run-script`).

**Control flow:** bounded `for i in range(N):` and `if` are allowed at top
level; variables, dict/list indexing, comparisons, and arithmetic work. No
`while` and no recursion — by design.

**Caps (liveness, to protect the agent's tool slot):** default 30 s
wall-clock, max 120 s via `max_duration_ms`; plus a Starlark step budget. On
breach, whatever was emitted so far is returned with an error note — never a
hang.

**Errors:** a verb that fails aborts the script, reported as an error result
citing the script position.

**Determinism:** all calls in one script run in script order with zero agent
round-trips. For frame-perfect capture: `app_pause(session_id=s)`,
`app_input(...)`, `app_step(session_id=s, frames=1)`,
`emit(app_screenshot(session_id=s))`, `app_resume(session_id=s)`.

**Durable handles:** a returned id is a plain string backed by a server-side
registry. Capture it in a variable and reuse it in the same script, OR pass it
back in a later `app_exec` call.

## Durable host Starlark library (🎯T108)

Host-only scripting: no in-app VM. Recipes live as plain `.star` files.

**Locations**

| Source | Path |
|--------|------|
| Bundled recipes | embedded in the spyder binary (`list_scripts` source=`bundled`) |
| Repo mirror (versionable) | `scripts/lib/*.star` in this repo |
| User overrides / new scripts | `~/.spyder/scripts/<name>.star` (wins over bundled same name) |

**CLI**

```bash
spyder list-scripts --json
spyder run-script skeleton
spyder run-script explore_tilt_probe session_id=s1
# or via app_exec:
# app_exec(script_path="skeleton", params={"session_id":"s1"})
```

### Explore → collect → regress promotion

1. **Explore** — poke dynamic behaviour; emit observations; no golden required.
   Recipes: `explore_tilt_probe` (E1 accel), `explore_drag` (E2 finger path),
   `explore_tap` (E3).
2. **Collect** — wrap the same stimulus with `app_state_capture_*` so you keep
   a stimulus+timeseries artifact (`dropped_samples` / `errors` preserved).
   Recipe: `collect_capture_while_drive` (C1).
3. **Regress** — add fail-closed asserts on series/scalars (primary oracles),
   not only click→screen. Recipes: `regress_trajectory` (R1 corridor),
   `regress_drag_follow` (R2 p95 tracking error). Optional visual baseline
   remains secondary (`diff` / baselines).

**Frame-accurate template** (games-quality timing):
`frame_deterministic_template` — `restore` → `pause` → inject → `step` →
observe → `resume`. Wall-clock `sleep` is fine for explore.

**L1 semantic (thin):** `l1_tap_body` uses `find_by_label` + `resolve_target`
when a physics/UI slice publishes `screen`/`bbox`. Missing geometry errors
clearly (slice contract gap) — no pixel guessing. L0 coordinates still work
everywhere.

### Worked examples (one per mode)

**Explore — tilt probe (needs live app-channel + physics slice):**

```starlark
# spyder run-script explore_tilt_probe session_id=<id>
# or:
run_script(path="explore_tilt_probe", params={"session_id": sid})
```

**Collect — capture while driving:**

```starlark
run_script(path="collect_capture_while_drive", params={"session_id": sid})
# → emit {stimulus, samples, dropped_samples, errors, ...}
```

**Regress — trajectory corridor (frame-stepped):**

```starlark
# tighten corridor via params once you have goldens
run_script(path="regress_trajectory", params={
    "session_id": sid,
    "min_x": "-2", "max_x": "2", "min_y": "-2", "max_y": "2",
})
```

**Offline / no device — skeleton + asserts:**

```starlark
run_script(path="skeleton")
# pure helpers (no device):
pts = [{"x":0.0,"y":0.0},{"x":1.0,"y":0.5}]
assert_trajectory(points=pts, min_x=-1.0, max_x=2.0, min_y=-1.0, max_y=2.0)
```

### Worked examples

**One-liner — list all devices (full MCP envelope):**

```json
{"name": "app_exec", "arguments": {"script": "devices(platform=\"all\")"}}
```

**Timed tap then screenshot** — tap the screen, wait 500 ms, capture:

```starlark
app_input(type="finger_down", x=0.5, y=0.5)
app_input(type="finger_up", x=0.5, y=0.5)
sleep(500)
app_screenshot()
```

**Frame-deterministic capture** — pause, inject input, advance exactly one
frame, capture, resume:

```starlark
s = "my-session-id"
app_pause(session_id=s)
app_input(session_id=s, type="finger_down", x=0.5, y=0.5)
app_step(session_id=s, frames=1)
emit(app_screenshot(session_id=s))
app_resume(session_id=s)
```

**Bounded poll loop** — check app state every 200 ms for up to 10 samples:

```starlark
for i in range(10):
    emit(app_state(slice="hud"))
    sleep(200)
```

**Handle round-trip across two calls** — first call starts a recording and
emits the handle:

```starlark
h = record_start(device="iPad", owner="me")
emit(h)
```

A later `app_exec` call uses the saved handle to stop it:

```starlark
record_stop(id="<that id>", owner="me")
```

## Builtin reference

All builtins accept a `device` parameter that resolves against the inventory
(alias, raw UUID, or raw serial). The exception is `devices`, which lists
everything it can see.

Arguments below are shown in keyword-call form. A `?` suffix means optional.

| Builtin | Purpose | Notes |
|---|---|---|
| `devices(platform?)` | List connected iOS + Android devices, annotated with inventory alias. iOS-17+ devices visible to USBMux but whose RSD tunnel hasn't settled yet appear with `tunnel_pending: true` (🎯T84) — they show up rather than disappearing during the settling window, but DTX-backed tools (screenshot, launch_app, …) may fail with a clear "tunnel not ready" error until the flag clears. | `platform` filter: `ios`, `android`, or `all` (default). |
| `resolve(device)` | Symbolic name → structured `Entry` with all known IDs. | Unknown raw inputs are echoed back classified. |
| `device_state(device)` | Battery level, charging, thermal state, foreground app. | 2-second TTL cache. Thermal is currently a note on iOS 17.4+ (MobileGestalt deprecated). |
| `screenshot(device, owner?)` | PNG of the current screen, returned inline as an image content block. | iOS uses go-ios's DVT `ScreenshotService`. iOS-17+ needs the bundled tunnel; iOS ≤16 uses lockdown directly and needs the Developer Disk Image mounted (`ios image auto <udid>` or open the device in Xcode once). Android uses `adb shell screencap`. Read-only; not gated by reservations — any session may screenshot any device. Pass `owner` to archive the PNG into the active run. |
| `list_apps(device)` | Installed third-party apps. iOS returns bundle ID + name + version; Android returns bundle ID only. | |
| `launch_app(device, bundle_id, env?)` | Foreground an arbitrary app by bundle id. Optional `env` dict injects environment variables into the launched process — see "Launching with env" below. | iOS-17+ uses go-ios's `appservice.LaunchApp` (CoreDevice/RemoteXPC, needs tunnel); iOS ≤16 uses `instruments.ProcessControl` (DTX-over-lockdown, no tunnel, needs DDI mounted). Path selection automatic per device. Android uses `adb monkey -c LAUNCHER` (no env) or `am start --es KEY VALUE` (with env). |
| `terminate_app(device, bundle_id)` | Stop an app by bundle id. | iOS: resolve PID via DVT, then kill. Android: `adb am force-stop`. |
| `rotate(device, orientation)` | Rotate an iOS simulator or Android emulator to a named orientation. Physical iOS/Android devices return a clear error. | Orientations: `portrait`, `landscape-left`, `landscape-right`, `portrait-upside-down`. iOS uses `xcrun simctl io <udid> rotate`; Android uses `adb emu rotate` (driven N times to reach the target). |
| `install_app(device, path)` | Install a .app/.ipa (iOS) or .apk (Android). Path must not contain `..` and must exist. | **Agents must use this tool (or `spyder install`), not shell out to `devicectl`/`adb`.** iOS: in-process go-ios `zipconduit` (works on iOS 16 lockdown and iOS 17+ RSD; CoreDevice's installapp capability is incomplete on some devices). Android: `adb install -r`, after best-effort settings that suppress Play Protect "Send app for a security check?" prompts on sideload (`verifier_verify_adb_installs=0`, `package_verifier_user_consent=-1`, `upload_apk_enable=0`). |
| `uninstall_app(device, bundle_id)` | Remove an app by bundle id / package name. | iOS: go-ios `installation_proxy`; Android: `adb uninstall`. |
| `deploy_app(device, path, bundle_id?, env?)` | Atomic deploy: terminate → install → launch → verify pid. Returns `{bundle_id, pid}`. Optional `env` dict forwarded to the launch step — see "Launching with env" below. | `bundle_id` is derived from Info.plist (iOS) or `aapt dump badging` (Android) if not supplied. iOS install uses go-ios's `zipconduit`; launch + pid-verify use `appservice` + DVT and need the bundled tunnel. Fail-fast on install error; "not running" from terminate is ignored. |
| `reserve(device?, selector?, owner, ttl_seconds?, note?)` | Acquire an exclusive device hold. | Supply `device` (literal pin) **or** `selector` (fuzzy JSON predicate) — not both. `owner` is always required. Default TTL 3600 s, max 86400 s. Same-owner re-acquires renew in place. See "Fuzzy reservation" section for selector schema and worked examples. |
| `release(device, owner)` | Free a reservation. | Non-owner releases conflict. Also stops any active recording owned by the releaser. Any applied network profile is cleared automatically. |
| `renew(device, owner, ttl_seconds?)` | Extend a reservation's TTL. | |
| `reservations()` | List active reservations. | Read-only. |
| `runs_list()` | List run-artefact bundles under `~/.spyder/runs/`, newest first. | Read-only. |
| `runs_show(run_id)` | Return a single run's full manifest (device, owner, timestamps, artefacts). | Read-only. |
| `baseline_update(suite, case, variant?, screenshot_path?, screenshot_base64?, manifest?)` | Store a new visual baseline for `suite/case/variant`. | Supply PNG via `screenshot_path` or `screenshot_base64`. Optional `manifest` JSON enables structural diffing. |
| `diff(suite, case, variant?, screenshot_path?, screenshot_base64?)` | Compare a candidate screenshot against the stored baseline. | Returns a structured JSON report with RMS pixel error, manifest structural diff (added/removed/moved elements with bounding boxes), and a `pass` verdict. Supply PNG via `screenshot_path` or `screenshot_base64`. |
| `baselines_list(suite)` | List all baselines stored for a suite. | Read-only. Returns `[{case, variant, has_png, has_manifest}]`. |
| `sim_list(state?)` | List all iOS simulators (UDID, name, state, runtime). Booted sims appear in `devices`. | `state` filter: `Booted`, `Shutdown`, etc. |
| `sim_create(name, device_type_id, runtime_id)` | Create a new iOS simulator; returns its UDID. | IDs from `xcrun simctl list devicetypes/runtimes --json`. |
| `sim_boot(udid)` | Boot an iOS simulator by UDID. | Sim appears in `devices` once booted. |
| `sim_shutdown(udid)` | Shut down an iOS simulator by UDID. | |
| `sim_delete(udid)` | Delete an iOS simulator by UDID (must be shut down first). | Irreversible. |
| `emu_list()` | List all Android Virtual Devices (name, path, target, ABI). Booted emus appear in `devices`. | Read-only. |
| `emu_create(name, system_image, device_profile)` | Create a new Android AVD. | System image must be pre-installed. |
| `emu_boot(name)` | Start an Android emulator (headless). Appears in `devices` once fully booted (~30–90 s). | Returns the serial once the process is launched. |
| `emu_shutdown(serial)` | Shut down an Android emulator by serial (e.g. `emulator-5554`). | Sends `adb emu kill`. |
| `emu_delete(name)` | Delete an AVD by name. | Irreversible. |
| `pool_list()` | Current pool state for all templates (available/running/reserved counts per template). | Read-only. Pool must be configured via `~/.spyder/pool.yaml`. |
| `pool_warm(template, count)` | Force pre-boot N additional instances for a template. | Moves instances from available to running tier. |
| `pool_drain(template)` | Shut down and delete all idle instances for a template. | Reserved instances are terminated first. |
| `pool_gc()` | Garbage-collect pool instances that are no longer tracked. | |
| `app_channel_stop(listener_id)` | Manually stop a per-(device, bundle_id) listener and tear down its sessions. Routine cleanup is automatic (24h idle TTL). | |
| `app_channel_list()` | List active per-(device, bundle_id) listeners and their sessions (listener_id, device_id, bundle_id, port, owner, idle_since, sessions[]). | Read-only. Listeners are created automatically by `launch_app`/`deploy_app`. |
| `app_ping(session_id?)` | Liveness check. | `session_id` is optional when exactly one session is connected. |
| `app_quit(session_id?)` | Clean-exit primitive (no macOS crash notification). | |
| `app_flush(session_id?)` | Flush pending app→spyder messages. | |
| `app_background(session_id?)` | Send the app to background. | |
| `app_foreground(session_id?)` | Bring the app to foreground. | |
| `app_low_memory(session_id?)` | Simulate a low-memory warning. | |
| `app_pause(session_id?)` | Pause the app's main loop. | |
| `app_resume(session_id?)` | Resume the app's main loop. | |
| `app_step(session_id?, frames?)` | Advance N frames while paused. | |
| `app_speed(session_id?, multiplier?)` | Set a dt multiplier for the app's main loop. | |
| `app_input(session_id?, type, ...)` | Inject a synthetic SDL event (`finger_down`, `finger_up`, `finger_motion`, `key_down`, `key_up`, `accel`). | For accel prefer `value=[x,y,z]`. One-shot accel is overwritten by real CoreMotion unless override is enabled. |
| `app_sensor_override_enable(session_id?, sensor?, value?)` | **Sticky** claim of accel stream: real samples dropped until `app_sensor_disable`. Optional `value=[x,y,z]` latches a sample (re-asserted each frame). | Touch/keys unchanged. |
| `app_sensor_override_set(session_id?, sensor?, value)` | Update latch while override is enabled. `value=[x,y,z]` required. | Errors if override not enabled. |
| `app_sensor_mute_enable(session_id?, sensor?)` | **Sticky** mute: drop real samples, emit neutral `[0,0,0]` until disable. | |
| `app_sensor_disable(session_id?, sensor?)` | Restore real device stream (passthrough). Ends override or mute. | Always call when done. |
| `app_sensor_status(session_id?, sensor?)` | Query `{enabled, kind, value?}`. | |
| `app_state(session_id?, slice, select?)` | Query a named state slice (`scene`, `physics`, `hud`, …). Slices the app advertises in `hello` are valid. | `select` is an optional jq expression evaluated server-side. |
| `app_save_state(session_id?)` | Serialize app state. Returns a base64-encoded blob; the app picks the schema. | |
| `app_restore_state(session_id?, state_b64)` | Deserialize app state from a base64 blob. | |
| `app_screenshot(session_id?)` | Request a PNG from the app's own framebuffer (sibling to the OS-path `screenshot`). | |
| `app_state_slices(session_id?)` | Return the list of named state slices the app has registered. | |
| `app_state_describe(session_id?, slice)` | Return a types-only structural sketch of a slice without the full payload. | |
| `app_state_capture_start(session_id?, slice, interval_ms?, select?)` | Start a polling capture of a state slice. Returns a `capture_id`. | Default 100 ms interval. Minimum 10 ms. |
| `app_state_capture_get(capture_id)` | Drain accumulated samples; capture continues. | Clears the buffer. |
| `app_state_capture_stop(capture_id)` | Stop the capture and drain remaining samples. | |
| `app_state_capture_list()` | List all active captures with metadata. | Read-only. |
| `app_log_get(session_id?, select?)` | Drain structured logs the app has pushed since the last call. | |
| `app_perf_get(session_id?, select?)` | Drain perf-counter samples the app has pushed since the last call. | |
| `is_running(device, bundle_id)` | Check whether an app is currently running. | |
| `health()` | Live daemon / subprocess / device health snapshot as a dict. | Same source of truth as `spyder status` and `GET /api/v1/health`. Read-only; takes no args. |
| `record_start(device, owner?)` | Begin a screen recording (mp4). Returns immediately; recording runs in background. | iOS simulators only — physical devices return an immediate error. Observational; not gated by device reservation. Only one recording per device at a time. The `owner` you pass here is the one that must stop the recording. |
| `record_stop(device, owner?)` | Stop the active recording and return the local mp4 path. | Owner must match the one that started the recording (not the device reservation). Waits for the recorder to flush. On Android, pulls the file from the device. |
| `network(device, owner, profile?, clear?)` | Apply or clear network condition shaping. | Android emulators only — see gotchas below. |
| `logs(device, since?, until?, bundle_id?, process?, subsystem?, tag?, regex?)` | Fetch log lines between two timestamps. | Read-only. iOS routes through go-ios's `syslog_relay` shim; Android uses `adb logcat`. For live tailing use REST SSE (see below). |
| `log_capture_start(device, owner?, bundle_id?, process?, subsystem?, regex?, ttl_sec?, max_bytes?, max_lines?)` | Start a managed log-capture session. | Returns `{session_id, started_at, expires_at}`. |
| `log_capture_get(session_id)` | Peek at buffered lines; clears the buffer. Capture continues. | |
| `log_capture_stop(session_id)` | Stop and drain remaining lines. | |
| `log_capture_list()` | List all active capture sessions. | Read-only. |
| `crashes(device)` | Fetch recent crash reports from the device. | |

## Launching with env

Both `launch_app` and `deploy_app` accept an optional `env` dict that
forwards environment variables to the launched app process. Useful for
runtime configuration that shouldn't be baked into the binary —
debug-feature flags, sandbox markers, and (the motivating use case)
network log targets.

```starlark
deploy_app(
    device="Jevons",
    path="/path/to/MyApp.app",
    env={"SPYDER_APP_CHANNEL": "192.168.1.42:9999", "FEATURE_FLAG_X": "on"},
)
```

The `env` parameter is a generic mechanism — spyder doesn't know what
the keys mean. Apps decide which env vars they read. The one
convention spyder documents is `SPYDER_APP_CHANNEL=host:port`:
apps that opt in dial that address, perform the appchannel `hello`
handshake, and then ride the bidirectional MessagePack RPC channel
(see [Bidirectional app channel](#bidirectional-app-channel-t75) below)
for logs, perf, screenshots, input injection, state queries, and
everything else. Apps that pre-date the appchannel protocol — or that
read the older `LOG_TARGET` name — won't connect and you'll see no
sessions in `app_channel_list`; upgrade the app or rename its env-var
lookup. Everything else (other env keys) is between you and your app.

### Per-platform delivery

| Platform | Delivery | Pickup |
|---|---|---|
| **iOS device** | go-ios `appservice.LaunchApp` passes the map as the launched process environment. | Standard `getenv("KEY")` in the app. No app-side shim required. |
| **iOS simulator** | `xcrun simctl launch` reads `SIMCTL_CHILD_<KEY>=<VALUE>` entries from its own environment and exposes them as `<KEY>=<VALUE>` to the simulated app. spyder builds these from the `env` dict. | Standard `getenv("KEY")`. |
| **Android (device or emulator)** | `adb shell am start --es KEY VALUE ...` passes the entries as Intent string-extras. Spyder switches from `monkey` to `am start` whenever `env` is non-empty. | The app's Java/Kotlin shim must extract the extras in `onCreate()` and call `setenv()` via JNI before native code runs — see the shim pattern below. |

### Android shim pattern

Android Intent extras aren't environment variables; the app has to
transcribe them. The standard pattern in `MainActivity.java` /
`MainActivity.kt` plus a small JNI helper:

```java
// MainActivity.java
@Override
protected void onCreate(Bundle savedInstanceState) {
    super.onCreate(savedInstanceState);

    Intent intent = getIntent();
    Bundle extras = intent.getExtras();
    if (extras != null) {
        for (String key : extras.keySet()) {
            Object value = extras.get(key);
            if (value instanceof String) {
                nativeSetenv(key, (String) value);
            }
        }
    }
    // SDLActivity.super.onCreate() or equivalent comes after.
}

private static native void nativeSetenv(String key, String value);
```

```c
// jni/setenv.c
#include <jni.h>
#include <stdlib.h>

JNIEXPORT void JNICALL
Java_com_example_app_MainActivity_nativeSetenv(
    JNIEnv* env, jclass cls, jstring key, jstring value) {
    const char* k = (*env)->GetStringUTFChars(env, key, NULL);
    const char* v = (*env)->GetStringUTFChars(env, value, NULL);
    setenv(k, v, 1);
    (*env)->ReleaseStringUTFChars(env, key, k);
    (*env)->ReleaseStringUTFChars(env, value, v);
}
```

Now `getenv("SPYDER_APP_CHANNEL")` from native code returns the expected value
on Android the same way it does on iOS and the desktop. The shim is
~20 lines and lives in the app, not in spyder or ge — spyder doesn't
have an Android side of itself to inject the shim into.

### REST equivalent

REST is the direct-verb transport; MCP is app_exec-only.

```sh
curl -s -X POST http://127.0.0.1:3030/api/v1/deploy_app \
  -H 'Content-Type: application/json' \
  -d '{"device":"Jevons","path":"/path/to/MyApp.app","env":{"SPYDER_APP_CHANNEL":"192.168.1.42:9999"}}'
```

## Collecting logs

App→spyder logging goes through the **bidirectional app channel**
(see [Bidirectional app channel](#bidirectional-app-channel-t75) below) —
apps send structured `log` push messages over the MessagePack RPC
channel, and the agent drains them with `app_log_get`. The previous
raw-text `log_collect_*` family was removed in v0.58.0.

As of v0.60.0 (🎯T83) the agent no longer manages the listener
lifecycle: `launch_app` / `deploy_app` automatically open (or reuse)
the per-(device, bundle_id) listener and inject
`SPYDER_APP_CHANNEL=host:port` into the launched process's env.
Listeners survive crashes and relaunches (same port, fresh
`session_id` each connect), and are reaped after 24h of no live
session and no `app_*` activity.

```starlark
# 1. Deploy (or launch). spyder auto-wires SPYDER_APP_CHANNEL.
deploy_app(device="Jevons", path="/path/to/MyApp.app")

# 2. App dials, sends `hello`, starts pushing structured `log` messages.
#    Drain them whenever you want — capture continues.
#    (In a second app_exec call, after the app has connected:)
app_log_get(device="Jevons", bundle_id="com.example.app")
# → {"lines": [{"timestamp": ..., "level": "info", "format": "...", ...}], ...}
```

You can still pass an explicit `SPYDER_APP_CHANNEL` in `env` to
override the auto-injected value (e.g. for an app on one device
dialing a listener on another host).

Compared to the old raw-text path: structured fields (level,
subsystem, format, args), no per-line regex parsing, server-side jq
`select` filtering, automatic session lifecycle (no manual stop), and
the same channel carries everything else the agent needs (`app_input`,
`app_state`, `app_quit`, etc.). The app-side cost is implementing
the appchannel protocol — for ge consumers, that's the work behind
ge T83's NetworkLogSink upgrade.

### Caveat: env vars are per-launch

The `SPYDER_APP_CHANNEL` env arrives on one specific launch. A user-tap
relaunch from SpringBoard / the Android launcher loses it — the app
restarts without the env, can't dial home, and the agent gets no
session in `app_channel_list`. The keyed listener is still there
on the same port (it survives for 24h of activity), so the easiest
recovery is to re-run `launch_app` — that re-injects the same
host:port the previous launch saw. Alternatively, have the app
cache the value across launches (NSUserDefaults / SharedPreferences
/ a config file).

## Bidirectional app channel (🎯T75)

The unified app→spyder channel. Apps speak length-prefixed MessagePack with a
JSON-RPC-shaped envelope (`{id, method, params}` requests, `{id,
result|error}` responses, `{method, params}` async pushes). Spyder
can request things (quit, pause, screenshot, query state); the app
can push things (logs, perf counters). Same agent, same loop, far
more leverage.

### Wire format

- **Framing**: `[4-byte LE length] [MessagePack body]`. Max body 16 MB.
- **Envelope**:
  - Request (either direction): `{id, method, params}` — `id` is a
    uint64 monotonically assigned by the sender.
  - Response: `{id, result}` or `{id, error: {code, message, data?}}`.
  - Push (no response expected): `{method, params}` with `id` omitted.
- **Handshake**: first frame app→spyder must be a `hello` request:
  `{id, method: "hello", params: {app_name, app_version, methods: [...]}}`.
  Spyder responds with `{id, result: {spyder_version, accepted_methods}}`
  (intersection of the app's advertised methods with spyder's known set).

### Worked example

```starlark
# 1. Deploy. spyder auto-creates the per-(device, bundle_id)
#    listener and injects SPYDER_APP_CHANNEL into the launch env.
deploy_app(device="Jevons", path="/path/to/MyApp.app")

# 2. App connects, sends hello → app_channel_list shows the session.
emit(app_channel_list())
# → {"listeners": [{
#      "listener_id": "...", "device_id": "...", "bundle_id": "com.example.app",
#      "port": 54321, "sessions": [{"session_id": "...", "app_name": "MultiMaze", ...}]
#    }]}

# 3. Drive the app. Either pass session_id, or address by (device, bundle_id);
#    or, when only one session is connected, omit all three.
emit(app_state(device="Jevons", bundle_id="com.example.app", slice="scene"))
app_input(type="finger_down", x=0.5, y=0.5)
app_pause()
app_step(frames=3)
emit(app_screenshot())
app_quit()  # clean exit, no macOS crash notification
```

### Method catalogue

The full v1 method set (🎯T75): `ping`, `quit`, `flush`,
`backgrounded`, `foregrounded`, `low_memory_warning`, `pause`,
`resume`, `step`, `speed`, `input_inject`, `state_query`,
`save_state`, `restore_state`, `screenshot_app`. Push messages from
the app: `log` (structured), `perf` (key/value counter batches).

Apps need only implement the subset they care about — advertise the
list in `hello.methods` and spyder's per-method builtins will
gracefully refuse calls to anything the app didn't claim.

### Session addressing

Every `app_*` builtin resolves its target session in this order:

1. **`session_id`** — explicit, fastest, unambiguous.
2. **`device` + `bundle_id`** — looks up the keyed listener and picks
   the unique session connected to it. Use when you want to address
   a specific app without juggling session ids.
3. **Single-session fallback** — when exactly one session is connected
   across all listeners, omit all three.

`app_channel_list()` shows the listener + session topology.

### Discovering state slices (🎯T80, T81)

`app_state_slices()` lists what slices a connected app exposes. `app_state_describe(slice=...)` returns a types-only sketch:

```starlark
emit(app_state_slices())
# → {"session_id": "...",
#    "slices": [
#      {"name": "scene"},
#      {"name": "physics",
#       "example": {"marble": {"position": {"x": 0.0, "y": 0.0, "z": 0.0}}}}
#    ]}
```

Each entry has a `name` (required) and an optional `example` payload —
apps that volunteer one give the agent an immediate template for
filter-writing; apps that don't, omit it. Bare-string entries from
pre-T81 builds (`"scene"` rather than `{"name": "scene"}`) decode
cleanly as name-only descriptors.

### Knowing what to query for (🎯T81)

When a slice doesn't have an inline example, ask the server for a
types-only sketch — one call walked into a structure-only view, much
smaller than the full payload:

```starlark
app_state_describe(slice="physics")
# → {"slice": "physics",
#    "shape": {"marble": {"position": {"x": "float", "y": "float", "z": "float"}},
#              "doors": [{"id": "int", "open": "bool"}]}}
```

The agent reads the shape, writes a jq filter for the part it cares
about, and queries the slice with that filter — never paying the full-
payload cost. Works for any app supporting `state_query` (no app-side
changes required).

### Filtering server-side with jq (🎯T81)

Every state/log/perf readout builtin accepts an optional `select` jq
expression. Spyder evaluates it server-side and returns only the
filtered result, keeping agent context budgets manageable when slices
are large:

```starlark
app_state(slice="physics", select=".marble.position")
# → {"x": 1.2, "y": 0.3, "z": 0.0}
```

Works on:
- `app_state` — single-shot, applied to the response.
- `app_state_capture_start` — applied at *insert time*; samples whose
  filter result is empty are skipped (saves buffer memory).
- `app_state_capture_get` — applied at *drain time*; different filter
  per call against the same capture's buffer.
- `app_log_get` / `app_perf_get` — applied to the drained arrays.

Bad expressions surface as `{"select_error": {"stage": "parse",
"detail": "..."}}` rather than a generic tool error, so the agent
can distinguish a busted filter from a busted device.

### Watching state evolve under inputs

For "tilt the device and read the ball position each frame" workflows,
fire the input then poll state in a single `app_exec` call — no
agent round-trips, no parallel tool calls needed (this supersedes the
old "parallel tool calls" framing):

```starlark
# Sustained tilt: enable is sticky until disable (does not revert next step).
app_sensor_override_enable(sensor="accel", value=[0.3, 0.0, 0.0])
for i in range(60):
    emit(app_state(slice="geometry"))  # tiltbuggy uses "geometry"; others vary
    sleep(16)
app_sensor_override_set(value=[-0.3, 0.0, 0.0])  # still override
sleep(500)
app_sensor_disable(sensor="accel")  # restore CoreMotion
```

One-shot inject without override is still valid for emulators / no-sensor
hosts, but on a physical device CoreMotion will overwrite it almost immediately:

```starlark
# Best-effort one-shot (passthrough) — not for sustained tilt.
app_input(type="accel", value=[0.3, 0.0, 0.0])
```

For a lossless capture with server-side buffering, use the
state-capture session pattern:

```starlark
# 1. Start a poller at 60 Hz.
cap = app_state_capture_start(slice="physics", interval_ms=16)

# 2. Drive the app while the poller accumulates samples.
app_input(type="accel", x=0.3, y=0.0, z=0.0)
sleep(1000)

# 3. Stop and drain.
emit(app_state_capture_stop(capture_id=cap["capture_id"]))
```

Samples are bounded (100k per capture by default, FIFO eviction); the
response carries `dropped_samples` and `errors` so the agent can tell
when its capture wasn't lossless. The poller calls `state_query` via
the standard RPC path with a 2 s per-call timeout — a stalled app
shows up as `errors > 0` and the `last_error` string, not a hung
capture. Active captures stop automatically when the session closes.

### Caveats

- The channel is dev-only by design. Apps should compile the receiver
  out of release builds (debug-build macro guard — ge does this).
- No authentication / encryption. LAN-only. (🎯T76.4 covers the
  upgrade path if cloud-CI or untrusted-network use ever surfaces.)
- One spyder connection per app session for v1; multi-client fan-out
  is deferred to 🎯T76.5.
- **iOS local-network permission**: the first time an app dials a
  spyder app-channel listener on the same LAN, iOS shows the system
  "*App* would like to find and connect to devices on your local
  network" prompt. The channel doesn't connect until you tap *Allow*
  once per (device, app) pair. If the agent times out on the
  handshake and `app_channel_list()` shows no session, check the
  device — the prompt is likely sitting on screen.

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
    available_max: 4      # cap on idle (Available) sims; LRU evict beyond it
    linger_seconds: 120   # keep running for 2 min after release

  - name: pixel9
    platform: android
    # device_type is the AVD template name for Android (used as clone source)
    device_type: Pixel9_API35_template
    runtime_or_system_image: "system-images;android-35;google_apis;arm64-v8a"
    tags: [ci, android, phone]
    available_max: 3
    linger_seconds: 60
```

The pool is **purely demand-driven**: sims are only created in
response to `Acquire`. There is no startup pre-warming, no
`available_min` floor, no `running_warm` pre-boot. After `Release`,
linger keeps the sim booted briefly for instant re-acquire; on linger
expiry it transitions to Available (shut down on disk). When a release
would push Available over `available_max`, the oldest Available sim is
evicted first (LRU). `available_max: 0` means no cap (sims accumulate
up to whatever has actually been used).

Restart the daemon after creating or modifying `pool.yaml`. The daemon
adopts existing `spyder-pool-*` sims on startup (background goroutine;
startup is non-blocking).

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

Via `app_exec`:

```starlark
pool_list()
```

```starlark
pool_warm(template="iphone16", count=2)
```

```starlark
pool_drain(template="pixel9")
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

iOS simulators and Android emulators are managed through dedicated builtin groups (`sim_*` and `emu_*`). Booted simulators and running emulators appear automatically in `devices()` output — the existing iOS adapter calls `xcrun simctl` which includes both physical and simulator devices, and the Android adapter calls `adb devices` which lists running emulators alongside physical devices.

### iOS simulators

```bash
# List all simulators (state: Booted, Shutdown, etc.)
spyder sim list
spyder sim list --state Booted --json

# Create a new simulator
spyder sim create MyPhone \
  --type com.apple.CoreSimulator.SimDeviceType.iPhone-15 \
  --runtime com.apple.CoreSimulator.SimRuntime.iOS-17-5

# Boot and shut down
spyder sim boot <udid>
spyder sim shutdown <udid>
spyder sim delete <udid>
```

Via `app_exec`:

```starlark
sim_list()
```

```starlark
sim_create(
    name="MyTestPhone",
    device_type_id="com.apple.CoreSimulator.SimDeviceType.iPhone-15",
    runtime_id="com.apple.CoreSimulator.SimRuntime.iOS-17-5",
)
```

```starlark
sim_boot(udid="ABCD-1234-...")
```

```starlark
sim_shutdown(udid="ABCD-1234-...")
```

### Android emulators

```bash
# List AVDs
spyder emu list --json

# Boot an emulator (headless; takes 30–90 s to fully start)
spyder emu boot Pixel6_API34

# Shut down by adb serial
spyder emu shutdown emulator-5554
```

CLI:

```bash
spyder emu create Pixel6_API34 \
  --image 'system-images;android-34;google_apis;arm64-v8a' --device pixel_6
spyder emu delete Pixel6_API34
```

Via `app_exec`:

```starlark
emu_list()
```

```starlark
emu_boot(name="Pixel6_API34")
```

```starlark
emu_shutdown(serial="emulator-5554")
```

### Boot-on-demand / reservation policy

You can reserve a device identifier (alias, UDID, or AVD name) before it is booted — the reservation is just a named hold on a string key; spyder does not enforce that the device is currently connected or running. This allows workflows to pre-claim a simulator slot and boot on demand:

1. `reserve` the sim/emu name.
2. `sim_boot` or `emu_boot` to start it.
3. Wait for it to appear in `devices` (simulators: immediate; emulators: poll `devices(platform="android")` until the serial appears).
4. Use the booted device's UDID or serial for operations.
5. `release` when done (optionally shut it down first).

**Operations that target a device must use the live UDID/serial**, not the AVD name. The `devices` builtin returns the identifier once the sim/emu is booted.

## Visual regression

Spyder ships a baseline store (`~/.spyder/baselines/`) and a two-tier
comparison pipeline:

1. **Manifest diff (structural)** — when both the baseline and the candidate
   carry a UI-element manifest, spyder reports added / removed / moved elements
   with their bounding boxes. This tier catches layout regressions that pixel
   RMS misses (e.g. an element moved by 2 px in a noisy background).
2. **Pixel diff (RMS)** — root-mean-square error across all channels, in [0, 1].
   Configurable tolerance (default 0.01). SSIM is stubbed in v1 (returns NaN).

The `diff` builtin runs both tiers and returns a unified report. If both sides have
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
spyder screenshot iPad --output login.png
spyder baseline update login-flow/main-screen login.png

# 2. Later, compare a new screenshot against the baseline.
spyder diff login-flow/main-screen new-screenshot.png
# exit 0 → pass; exit 1 → fail (structural or pixel regression)

# 3. With a manifest for richer structural diffing:
spyder baseline update login-flow/main-screen login.png manifest.json
spyder diff login-flow/main-screen new-screenshot.png new-manifest.json

# 4. Use --variant for per-device or per-orientation separation:
spyder baseline update login-flow/main-screen login.png --variant ipad-landscape
spyder diff login-flow/main-screen new-screenshot.png --variant ipad-landscape
```

Via `app_exec`:

```starlark
baseline_update(suite="login-flow", case="main-screen", screenshot_path="/tmp/login.png")
```

```starlark
diff(suite="login-flow", case="main-screen", screenshot_path="/tmp/new.png")
```

### Log queries — range vs. live

The `logs` builtin returns a bounded list of `LogLine` records. It
accepts:

- `device` (required) — alias or UUID.
- `since` / `until` — window bounds. Each accepts either an RFC3339 absolute
  (e.g. `2026-04-19T14:00:00Z`) or a Go duration relative to now (e.g.
  `since="-2m"` for "the last two minutes", `since="-1h"`, `until="+30s"`,
  `until="now"`). Additionally, `since="launch"` is shorthand for "everything
  since spyder last called `launch_app` for `bundle_id` on this device" — it
  requires `bundle_id` and errors if no such call was recorded in this
  daemon's lifetime (so it's not a substitute for absolute times if the
  daemon has restarted or the app was foregrounded via SpringBoard). Both
  optional. When `since` is omitted, iOS collects from a short live window
  (≤5 s); Android uses `-d` (dump buffer then exit).
- `bundle_id` — filter by app bundle id (e.g. `com.example.app`). The server resolves this to the iOS `CFBundleExecutable` (via `installation_proxy`) or the Android package name before applying the process filter, so callers don't need to know the executable name. Mutually exclusive with `process`.
- `process` — filter by raw process name (iOS: matched against `image_name` server-side; Android: tag/process match). Use when you already know the image name and don't want to pay the resolution round-trip. Mutually exclusive with `bundle_id`.
- `subsystem` — iOS only (matched against `SyslogLabel.subsystem` server-side, e.g. `com.apple.network`). Ignored on Android.
- `tag` — Android logcat tag (e.g. `MyApp`). Ignored on iOS.
- `regex` — regex applied to the message body (both platforms, client-side).

**MCP transport does not support streaming.** For live log tailing, use the
REST SSE endpoint instead:

```bash
# Live tail — server-sent events, each line is a JSON LogLine.
curl -N -X POST http://127.0.0.1:3030/api/v1/log_stream \
  -H 'Content-Type: application/json' \
  -d '{"device":"iPad","process":"MyApp","regex":"error"}'
```

Or via the CLI:

```bash
spyder log iPad --follow                                   # live tail
spyder log iPad --follow --bundle-id com.example.app       # filter by app
spyder log iPad --follow --process MyApp                   # filter by executable name
spyder log iPad --since -2m                                # the last two minutes
spyder log iPad --bundle-id com.example.app --since -2m    # last 2 min, just this app
spyder log iPad --bundle-id com.example.app --since launch # everything since the last launch_app
spyder log iPad --since 2026-04-19T00:00:00Z              # bounded range, absolute
spyder log iPad --regex "crash|panic"                      # regex on message
```

**Platform quirks:**

- **iOS range queries** subscribe to the live syslog stream via go-ios's
  `syslog_relay` shim for up to 5 seconds and collect lines in the
  window. This is adequate for post-hoc debugging but is not a true
  archived-log query. For long-span queries run multiple short windows
  or use `--follow` and let the stream run while reproducing.
- **iOS log capture** routes through the DTX `activitytracetap`
  channel — the same path Xcode's Console.app uses for live device
  logs. Each entry arrives with structured fields (image name,
  message-type / level, OSLog subsystem and category, the format-
  string-expanded message body, PID, thread ID), so `subsystem`
  filtering works server-side and third-party-app emissions
  surface (an app's own `os_log` / `syslog(3)` / SPDLOG calls
  flow through alongside system frameworks). Falls back to the
  legacy `os_trace_relay` lockdown service on devices where the
  DTX channel can't be opened (DDI not mounted, iOS <17) — that
  fallback gets system-process coverage only.
- **Android tag filter** — logcat `-s <tag>:V *:S` suppresses all other tags.
  Combining tag + regex is the most targeted approach.
- **Android process filter** — there is no direct process-name filter in logcat;
  spyder does a case-insensitive substring match on the tag column as a proxy.


## Daemon health (🎯T90)

Spyder maintains a live **health model** of the daemon, its supervised
subprocesses (notably the bundled `ios` tunnel), and connected devices.
Faults are classified resiliently: recoverable stale tunnels self-heal
quietly; only un-self-healable conditions surface as a macOS notification.

Three pull surfaces share one in-process model:

| Surface | How |
|---|---|
| CLI | `spyder status` (table) or `spyder status --json` |
| REST | `GET /api/v1/health` |
| Starlark | `health()` inside `app_exec` |

`spyder status` is an HTTP **client** of a running `spyder serve` — it does
not start the daemon. On connection failure it tells you to check that
serve is running.

Entity kinds: `daemon`, `subprocess`, `device`. States include `healthy`,
`degraded`, `recovering`, `needs_attention`, `absent_expected`,
`absent_unexpected`. Everything below `needs_attention` stays pull-only;
the notifier fires **once** per entity that reaches `needs_attention`,
deduped and auto-cleared when it recovers.

## Reservations

For parallel dev sessions (e.g. one agent working on TiltBuggy while another
works on another game), acquire an exclusive hold on a device with `reserve`
before mutating operations. Mutating tools (`launch_app`, `terminate_app`,
`install_app`, `uninstall_app`, `deploy_app`, `network`, `rotate`) reject
with a structured conflict error naming the holder if someone else is
holding the device. Read and observational tools (`devices`, `resolve`,
`device_state`, `is_running`, `list_apps`, `crashes`, `logs`, `screenshot`,
`record_start`, `record_stop`, `reservations`) are unaffected — any session
can screenshot or record any device, even one held by someone else.
`record_stop` authenticates against the owner that started the recording,
not the device reservation.

### Literal device reservation

Pin a specific device by alias or UUID:

```starlark
reserve(device="iPad", owner="tiltbuggy", ttl_seconds=3600, note="UI regression run")
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
by `devices()` and against the `tags` array on the inventory entry. This
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
  "alias": "iPad",
  "platform": "ios",
  "ios_uuid": "00008103-001122334455667A",
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

```starlark
reserve(selector="{\"platform\":\"ios\",\"model_family\":\"ipad\"}", owner="tiltbuggy")
```

Android phone with API ≥ 33:

```starlark
reserve(selector="{\"platform\":\"android\",\"model_family\":\"phone\",\"os_min\":\"33\"}", owner="tiltbuggy")
```

iOS simulator only (for rotation tests):

```starlark
reserve(selector="{\"platform\":\"ios\",\"orientation_capable\":true}", owner="tiltbuggy")
```

Device with CI-environment tag:

```starlark
reserve(selector="{\"platform\":\"ios\",\"tags\":[\"ci\"]}", owner="tiltbuggy")
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
spyder reserve iPad --as tiltbuggy
```

Agents don't *have* to reserve: if the device is free, mutating calls just
work. Reservations are only necessary for long-running sequences where
another session could race you. `spyder run` auto-reserves for the wrapped
command's lifetime (owner defaults to `filepath.Base(cwd)`), opportunistically
renews, and releases on exit — no explicit reserve/release needed for the
common test-run pattern.

To pass owner-authentication on a mutating call while someone else holds
the device, pass `owner="<your-owner>"` in the builtin call. The
server resolves canonical identity via the inventory, so reserving
"iPad" also blocks operations on her raw UDID and vice versa.

## Run-artefact store

Every successful `reserve` opens a **run** — a directory under
`~/.spyder/runs/<run-id>/` that collects screenshots, recordings, logs,
and crash reports captured during that reservation. `release` (and the
equivalent `spyder run` exit path) closes the run. The run's
`manifest.json` enumerates every artefact with its source tool,
timestamp, mime type, and size.

Currently the `screenshot` builtin writes its PNG into the active run's
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
service (e.g. `launchctl setenv SPYDER_RUNS_MAX_AGE_DAYS 60`).

## Network condition shaping

The `network` builtin applies named network profiles to emulators for
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

```starlark
# Apply a profile
network(device="Pixel8", owner="myagent", profile="3g")
```

```starlark
# Clear — restore full speed
network(device="Pixel8", owner="myagent", clear=True)
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

Every spyder verb is also exposed over plain HTTP+JSON on the same
listener, at `POST /api/v1/<verb>`. REST is the direct-verb transport —
MCP is app_exec-only. The request body is the verb's arguments; the response
is a JSON-encoded `mcp.CallToolResult`:

```bash
# Shell scripts: call a verb directly.
curl -s -X POST http://127.0.0.1:3030/api/v1/devices \
  -H 'Content-Type: application/json' -d '{"platform":"android"}'

# Zero-arg verbs accept an empty body.
curl -s -X POST http://127.0.0.1:3030/api/v1/reservations
```

The same surface is available as `spyder` subcommands, which POST to
the local daemon and render the result:

```bash
spyder devices --platform ios --json
spyder screenshot iPad --output /tmp/ipad.png
spyder reserve iPad --ttl 600 --note "UI sweep"
spyder list-apps iPad --json
spyder is-running iPad com.example.app   # exit 0 / 20 / 22
spyder release iPad
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
spyder log iPad --since -2m --json               # last two minutes
spyder log iPad --since 2026-04-19T00:00:00Z --json
spyder log iPad --follow --process MyApp         # live SSE tail
```

`--as OWNER` defaults to `filepath.Base(cwd)` (same convention as
`spyder run`). Set `SPYDER_DAEMON_URL` to point the CLI at a
non-default daemon (e.g. `http://127.0.0.1:13030` during development).

Reservation state is shared between transports: an agent holding a
lock via MCP blocks a shell script hitting REST and vice versa.

### Universal flags

Every device-tool subcommand auto-registers three flags. The defaults
match the Make-driven test infrastructure use case (silent on success,
machine-parseable when asked, bounded latency).

- `--timeout DURATION` — Bounds the daemon HTTP call. Go duration
  (`30s`, `5m`, `2h`); `0` disables. Per-command defaults: `10s` for
  reads; `60s` for launch/terminate/rotate/sim/emu/net/pool ops; `5m`
  for install/uninstall; `10m` for deploy; `30s` for screenshot; `30s`
  for reserve/release/renew; `60s` for record; no timeout for
  `log --follow` and `spyder run -- <cmd>`. Exceeded → exit `30`.
- `--json` — On read-ish commands, emits the daemon's JSON response
  verbatim. Pipe to `jq` from shell scripts.
- `-v` / `--verbose` — On mutating commands (silent on success by
  default), restores the daemon's confirmation text on stdout.

### Selector grammar (`--on PREDICATE`)

`spyder reserve --on PREDICATE`, `spyder run --on PREDICATE`, and
`spyder resolve --on PREDICATE` all parse a comma-separated key=value
selector into the same struct the `reserve` builtin consumes. Useful
for Make targets that can't hard-code a device alias. `spyder resolve`
also auto-detects predicates in the positional argument when it
contains `=` (so `spyder resolve platform=ios` works without an
explicit `--on`); inputs that are neither alias nor parseable predicate
exit 15 (`ExitSelectorNotSupported`).

`spyder run --on PREDICATE` resolves+reserves+runs **atomically** via
the daemon — no resolve→release→re-acquire dance, no race window.
Combine with `--timeout DURATION` to declare a cell budget:

```bash
spyder run --on platform=ios,model=ipad --timeout 5m -- \
  ./tools/matrix-cell.sh ui_smoke
```

```bash
spyder reserve --on platform=ios,os>=17,tags=phone+test --as ci
spyder reserve --on platform=android,model=pixel
spyder reserve --on platform=ios,attr.serial=ABC123
```

Recognised keys: `platform`, `model`, `os>=`/`os<=`/`os_min`/`os_max`,
`orientation_capable`, `tags=tag1+tag2`, `attr.<name>`. See
`STABILITY.md` for the full schema.

### Exit codes (machine-readable failure classification)

The CLI returns distinct exit codes per failure mode so Make targets
can branch on them:

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Generic / unclassified failure. |
| 2 | Argument parsing error. |
| 10 | Daemon not reachable (`$SPYDER_DAEMON_URL`). |
| 11 | Device not found. |
| 12 | Device not connected. |
| 13 | Reservation conflict (held by another owner). |
| 14 | Not reserved by you. |
| 15 | Selector grammar not supported (resolve input is neither alias nor parseable predicate). |
| 20 | App not installed (also: `is-running` reports not installed). |
| 21 | Install / deploy failed. |
| 22 | Launch failed (also: `is-running` reports installed but not running). |
| 23 | Terminate failed. |
| 24 | PID-verification failed (deploy). |
| 30 | `--timeout` exceeded. |
| 40 | Trust not granted (iOS pair-record). |
| 41 | Developer Mode disabled. |
| 42 | Device locked. |

Defined in `internal/cliexit/cliexit.go`. The mapping from daemon
REST errors to exit codes lives in `cliexit.MapDaemonError`. Exit-code
*meaning* is part of the 1.0 stability commitment (see STABILITY.md);
adding new codes for previously-unclassified causes is non-breaking.

### Hermeticity

Each proxy CLI invocation is independent — no `~/.spyder/` state is
read or written by the CLI itself. The two exceptions are documented:
auto-spawning a daemon writes `~/.spyder/daemon.log`, and `spyder run`
manages its own reservation+runs store directly (it's the daemonless
wrapper). Tests in the main package (`TestCLIHermeticity`,
`TestCLINoStickyStateOutsideAllowList`) lock this contract.

## The `spyder run` test wrapper

Beyond the MCP surface, spyder exposes a CLI wrapper that runs a command
under an auto-acquired device reservation and releases it on exit (success
or failure):

```bash
spyder run -- xcodebuild -project MyApp.xcodeproj \
  -scheme MyApp -destination 'id=00008103-001122334455667A' test
```

- Default device is `iPad`. Override with `--device <alias-or-uuid>`.
- The wrapper forwards stdin/stdout/stderr and the command's exit code.
- Release failures are logged but do not mask the test's exit code.

## Managed log-capture sessions (🎯T60)

Agent-driven log capture used to require shell glue: `spyder log --follow > /tmp/cap &`, save the pid, ask the user to reproduce, `kill <pid>`, grep the file. Fragile, requires shell tooling that some MCP hosts don't have, drops trailing lines on disconnect, no peek-during-capture, no recovery if the daemon restarts mid-capture.

The managed-session API replaces all of that. The lifecycle:

1. **Start a session.** Pick whichever filter set the existing `logs` builtin would have used (bundle_id, process, subsystem, regex). Returns a `session_id` plus `expires_at`.

   ```starlark
   log_capture_start(device="iPhone", bundle_id="com.example.app", owner="calibration-loop")
   # → {"session_id": "a1b2c3d4e5f6...", "started_at": "...", "expires_at": "..."}
   ```

2. **Reproduce the scenario.** The server buffers lines into a per-session ring (default 50 MB / 100k lines, FIFO eviction). The agent does whatever else it needs to in the meantime.

3. **(Optional) Peek incrementally.** `log_capture_get` returns whatever is currently buffered and **clears the buffer** so subsequent calls see only new lines. Capture continues. Useful for "wait for marker X, then keep waiting for marker Y."

   ```starlark
   log_capture_get(session_id="a1b2c3d4e5f6...")
   # → {"lines": [...], "dropped_lines": 0}
   ```

4. **Stop and drain.** `log_capture_stop` returns the remaining buffer and tears the session down. Subsequent get/stop calls on the same session_id return an error.

   ```starlark
   log_capture_stop(session_id="a1b2c3d4e5f6...")
   # → {"lines": [...], "stopped_at": "...", "dropped_lines": 0}
   ```

**Eviction policy.** When the buffer hits either bound (configurable via `max_bytes` / `max_lines`), the oldest entry is dropped and `dropped_lines` is incremented. Non-zero `dropped_lines` in a get/stop response means the capture wasn't lossless across that interval — increase the bound or peek more often. The counter resets on each get/stop drain so it tracks "drops since last drain," not cumulative drops.

**TTL.** Sessions auto-expire after `ttl_sec` of no get/stop activity (default 5 min, max 24 h). The sweeper runs every 30 s; an idle session is torn down silently and a subsequent get/stop call reports "no such session." This guards against forgotten captures pinning device IO indefinitely.

**Per-session isolation.** Each session opens its own underlying tap against the device — there's no shared device-wide capture today (filed as future work in 🎯T65 once it exists). Two agents capturing the same device for two different bundle ids each pay one tap of device load. For 1–2 concurrent sessions this is fine; if you find yourself running many, profile first.

**Inspection.** `log_capture_list()` returns metadata for every live session (id, device, owner, started_at, expires_at, buffer_lines, buffer_bytes, dropped_lines, filter) without disturbing any of them — useful when an agent loses track of a session it started earlier in the conversation.

**CLI mirrors.**

```bash
spyder log <device> --capture [--bundle-id ID | --process P] [--subsystem S] [--regex R] \
                              [--ttl-sec N] [--max-bytes N] [--max-lines N] [--as OWNER]
spyder log --capture-get <session-id>
spyder log --capture-stop <session-id>
spyder log --capture-list
```

**Persistence.** Sessions are in-memory only. A daemon restart (graceful or crashed) drops every active session; `log_capture_get` / `log_capture_stop` on a session_id from before the restart returns "no such session." For captures that need to survive a `brew services restart spyder`, use the `--follow` SSE stream piped to a file instead.

**When to prefer `--capture` over `--follow`.** Use `--capture` whenever the agent needs to read the data across more than one turn (most agent workflows), needs to peek without stopping, or runs in a host without easy access to background shell + temp files (most MCP clients). Use `--follow` for human-driven tailing on the terminal or for very long captures where you want SSE-style streaming straight into a file.

## Diagnosing iOS device-stack wedges (`spyder doctor`)

macOS's `usbmuxd` daemon can desync from CoreDevice's view of paired iOS devices under operational churn — heavy `spyder` test runs, repeated DTX channel opens, multiple service-connection cycles per second. The symptom: `xcrun devicectl list devices` shows the device as `connected` but `bin/ios list` returns an empty or short list, and every go-ios RPC against the missing UDID fails with `Device 'UDID' not found. Is it attached to the machine?`.

Recovery is `killall usbmuxd` — launchd respawns it within ~1 s and the device list re-enumerates from current hardware state. `spyder doctor` automates the detect+fix loop:

```bash
spyder doctor          # diagnose only; exits 2 if wedged
spyder doctor --fix    # diagnose; if wedged, run the bundled killusbmuxd helper via sudo
spyder doctor --json   # machine-readable report
```

`--fix` shells out to the bundled `spyder-killusbmuxd` binary (installed alongside `spyder`). The helper does literally one thing — `killall usbmuxd` — and exits. It's separated from `spyder` itself so the operator can grant it `NOPASSWD` sudo without giving the main binary any privilege.

For auth-free recovery, add a sudoers entry (one-time setup):

```bash
# Pick the install path for your environment:
#   Homebrew:   /opt/homebrew/bin/spyder-killusbmuxd
#   Source dev: /path/to/spyder/bin/spyder-killusbmuxd
HELPER=/opt/homebrew/bin/spyder-killusbmuxd
echo "$USER ALL=(root) NOPASSWD: $HELPER" | sudo tee /etc/sudoers.d/spyder-killusbmuxd
sudo chmod 0440 /etc/sudoers.d/spyder-killusbmuxd
```

After that, `spyder doctor --fix` runs without any password prompt. With PAM touchid configured for sudo, you don't even need the sudoers entry — touchid handles the auth interactively.

**When `--fix` doesn't recover everything**: occasionally a device-side state (lockdown / RemotePairing) also gets stuck and the device stays missing from usbmux even after the restart. Unplug+replug the device and re-pair if the dialog appears.

## Keeping iOS devices awake

There is no in-spyder keep-awake supervisor. The previous KeepAwake
companion-app + autoawake convergence loop was removed in v0.40.0 —
the underlying go-ios `instruments.ListenAppStateNotifications.Close()`
doesn't actually close the DTX connection, which leaked a TCP
connection per convergence cycle and eventually wedged the daemon.
The leak is upstream and there's no spyder-side workaround.

Until the upstream is fixed (🎯T64 tracks the investigation +
reinstate work), use the device's OS-level **never-lock** setting:
**Settings → Display & Brightness → Auto-Lock → Never**.

## Environment and dependencies

- **macOS host.** macOS 15+ / Apple Silicon only. Spyder's value is iOS device
  orchestration via macOS-specific tooling (`xcrun simctl` for simulators,
  the bundled go-ios CLI / tunnel for real devices); Linux is not a release
  target. (🎯T45)
- **bundled `ios` binary** — the [go-ios](https://github.com/danielpaulus/go-ios)
  CLI, installed at `$(brew --prefix)/libexec/spyder/ios`. spyder spawns
  it as `ios tunnel start --userspace` at daemon startup; the tunnel
  registry on `127.0.0.1:60105` is what the in-process iOS adapter
  queries to do RSD lookups for iOS-17+ devices. Userspace mode means
  no sudo is required.
- **`adb`** — Android operations.
- **`alerter`** — persistent macOS notifications for the locked-device prompt
  (fallbacks: `terminal-notifier`, `osascript`).

## Configuration

```bash
spyder serve                                  # default: 127.0.0.1:3030, HTTP MCP on /mcp (loopback only)
spyder serve --addr :3030                     # expose on all interfaces (caution: no auth; only on trusted networks)
```

**Security note.** Spyder's MCP endpoint has no authentication; anyone
who can hit `http://<addr>:3030/mcp` can take screenshots, launch /
terminate apps, and hold reservations on your devices. The default
loopback bind is deliberate — external exposure is opt-in via
`--addr` and should only be used on trusted networks.

### Brew-services environment (launchd)

When spyder runs as a Homebrew service, launchd doesn't inherit your
shell env. The formula sets a default `PATH` that covers
`/opt/homebrew/bin` and the usual system paths. The bundled `ios`
tunnel binary lives at `$(brew --prefix)/libexec/spyder/ios` and is
resolved relative to the spyder executable automatically. No
`launchctl setenv PATH` surgery required on a fresh machine.

## Screen recording

`record_start` / `record_stop` capture a video of the device's screen. Single
screenshots miss multi-frame visual bugs (rotation flashes, animation glitches,
transition artifacts). Use recording to capture a short mp4 around a dynamic
event.

```starlark
# Start recording, trigger the event, stop.
record_start(device="iphone-16-sim", owner="tiltbuggy")
# … trigger the event in a subsequent app_exec call …
```

```starlark
record_stop(device="iphone-16-sim", owner="tiltbuggy")
```

Or as a handle round-trip if you want to keep the id for later:

```starlark
h = record_start(device="iphone-16-sim", owner="tiltbuggy")
emit(h)
```

**Platform notes:**

- **iOS simulator**: Pass the simulator UDID directly (from `xcrun simctl list
  devices`). The alias inventory doesn't currently have a simulator type, so
  pass the raw UDID. Recording uses `xcrun simctl io <udid> recordVideo
  <dest.mp4>`.
- **iOS physical device**: Not supported. `record_start` returns an immediate
  error: `"screen recording is not supported on iOS physical devices; use a
  simulator"`. This is a platform limitation — go-ios and `devicectl` do
  not expose a recording API at this time.
- **Android device / emulator**: Uses `adb shell screenrecord --bit-rate
  4000000 /sdcard/spyder-recording.mp4`. The file is pulled to a local temp
  path on `record_stop`. Maximum native recording duration is 180 s per
  Android's `screenrecord` limit.

**Conflict detection**: Only one recording session per device at a time. A
second `record_start` on the same device returns a Conflict error naming the
current recorder's owner. A recording is also stopped automatically when its
own owner releases their reservation (matched on owner identity, not on device
holder).

## Common gotchas

- **"tunneld unavailable"** in a tool error → the bundled `ios tunnel
  start --userspace` child process is meant to be running. If spyder
  started it but it crashed, `brew services restart spyder` brings it
  back up. Required on iOS 17+ for `screenshot` and for stable device
  enumeration; iOS <17 devices keep working over USBMux even without
  the tunnel.
- **"Developer Mode is not enabled"** in a tool error (iOS 17+) → on
  the device, **Settings → Privacy & Security → Developer Mode**, toggle
  on. The device reboots; trust the developer cert again afterwards if
  prompted.
- **Device listed but operations fail with "device not connected"** → the
  device appears in the paired list (USB/WiFi pairing record exists) but isn't
  currently reachable. Plug it back in, unlock, or re-enable "Connect via
  network" in Xcode → Window → Devices and Simulators.
- **`launch_app` / `terminate_app` return `'Security'` DvtException** → the
  app's developer profile isn't trusted on this device. Go to Settings →
  General → VPN & Device Management → tap the developer name → Trust. Only
  applies to side-loaded / developer-signed apps. Note: iOS discards the
  developer entry when the *only* app from that developer is uninstalled —
  reinstalling will require another Trust tap.
- **`launch_app` returns `'Locked'` DvtException on iOS** → unlock the device.
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
