# Agent guide: spyder

Spyder is an HTTP-based MCP server that owns session state for real-device mobile
development: symbolic device aliases, live device facts (battery, charging,
foreground app), screenshots, app lifecycle, and a plug-in-driven auto-launcher
for the KeepAwake companion app on iOS.

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
| `keepawake` | Foreground the KeepAwake companion app on iOS. No-op on Android (OS handles stay-awake natively). | |
| `list_apps` | Installed third-party apps. iOS returns bundle ID + name + version; Android returns bundle ID only. | |
| `launch_app` | Foreground an arbitrary app by bundle id. | iOS uses DVT launch (needs tunneld); Android uses `adb monkey -c LAUNCHER`. |
| `terminate_app` | Stop an app by bundle id. | iOS: resolve PID via DVT, then kill. Android: `adb am force-stop`. |
| `reserve` | Acquire an exclusive device hold. | `{device, owner, ttl_seconds?, note?}`. Default TTL 3600 s, max 86400 s. Same-owner re-acquires renew in place. |
| `release` | Free a reservation. | `{device, owner}`. Non-owner releases conflict. |
| `renew` | Extend a reservation's TTL. | `{device, owner, ttl_seconds?}`. |
| `reservations` | List active reservations. | Read-only. |

## Reservations

For parallel dev sessions (e.g. one agent working on TiltBuggy while another
works on another game), acquire an exclusive hold on a device with `reserve`
before mutating operations. Mutating tools (`keepawake`, `screenshot`,
`launch_app`, `terminate_app`) reject with a structured conflict error
naming the holder if someone else is holding the device. Read tools
(`devices`, `resolve`, `device_state`, `reservations`) are unaffected.

```json
{"name": "reserve", "arguments": {"device": "Pippa", "owner": "tiltbuggy", "ttl_seconds": 3600, "note": "UI regression run"}}
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

## The `spyder run` test wrapper

Beyond the MCP surface, spyder exposes a CLI wrapper that runs a command and
then foregrounds KeepAwake on exit (success or failure):

```bash
spyder run -- xcodebuild -project MyApp.xcodeproj \
  -scheme MyApp -destination 'id=00008103-000D39301A6A201E' test
```

- Default device is `Pippa`. Override with `--device <alias-or-uuid>`.
- The wrapper forwards stdin/stdout/stderr and the command's exit code.
- KeepAwake restore failures are logged but do not mask the test's exit code.

## Auto-awake supervisor

`spyder serve` runs an always-on supervisor that polls
`pymobiledevice3 remote tunneld` (default `127.0.0.1:49151`). Whenever a new
paired iOS device appears:

1. Checks whether KeepAwake is installed (`pymobiledevice3 apps list`).
2. If not installed, auto-deploys: runs `xcodegen` + `xcodebuild` targeting the
   UDID, then `xcrun devicectl device install app`.
3. Checks whether KeepAwake is already running (DVT `process-id-for-bundle-id`).
4. If not, launches it via DVT.

If the launch fails because the device is locked, spyder fires a **persistent
macOS notification** via `alerter` (or `terminal-notifier` / `osascript` as
fallbacks) asking the user to unlock. The alert stays up until:

- The user clicks **Dismiss**, or
- Spyder's retry loop succeeds (within 5 minutes), at which point the alert is
  dismissed programmatically via `alerter --remove`.

Auto-deploy requires Xcode Accounts signed in with an Apple ID and a one-time
device-trust tap (Settings → General → VPN & Device Management → Trust). That
step is Apple-imposed and cannot be automated.

## Environment and dependencies

- **macOS host.** Tested on macOS 15+ / Apple Silicon. Linux builds exist for
  spyder's non-iOS-specific surface (devices list, Android, the MCP server
  itself) but iOS operations will fail there.
- **`pymobiledevice3` ≥ 8.2** — iOS operations. Shelling-out is the default;
  long-lived library embedding is a future refinement.
- **`pymobiledevice3 remote tunneld`** — required for DVT operations on iOS 17+.
  Run as root (TUN/TAP interface). Spyder detects and uses an externally-managed
  instance by default; integrated supervision via `--supervise-tunneld` is
  planned but not yet wired.
- **`adb`** — Android operations.
- **`xcodebuild`, `xcodegen`, `xcrun devicectl`** — iOS auto-deploy of KeepAwake.
- **`alerter`** — persistent macOS notifications for the locked-device prompt
  (fallbacks: `terminal-notifier`, `osascript`).

## Configuration

```bash
spyder serve --addr :3030                     # default; HTTP MCP on /mcp
spyder serve --tunneld-addr 127.0.0.1:49151   # non-default tunneld location
```

Environment variables:

- `SPYDER_KEEPAWAKE_PROJECT` — directory containing KeepAwake's `project.yml`.
  Defaults to searching upward from the working directory. If not found,
  auto-deploy is disabled but auto-launch still works for pre-installed
  KeepAwake. **(Planned for removal once the project is `go:embed`ded
  into the binary.)**

### Brew-services environment (launchd)

When spyder runs as a Homebrew service, launchd doesn't inherit your
shell env. The v0.3+ formula sets a default `PATH` that covers
`/opt/homebrew/bin` and the usual system paths. Two things may still
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

**2. KeepAwake project location.** `SPYDER_KEEPAWAKE_PROJECT` must
point at your local spyder clone's `ios/KeepAwake/` so auto-deploy can
run `xcodegen` + `xcodebuild`. Set it for the service the same way:

```bash
launchctl setenv SPYDER_KEEPAWAKE_PROJECT /path/to/spyder/ios/KeepAwake
brew services restart spyder
```

Without it, auto-launch still works for devices that already have
KeepAwake installed; auto-deploy is silently skipped for first-install
devices. Logs show `autoawake: ready project_dir=…` (good) or
`autoawake: KeepAwake project not found — auto-deploy disabled`.

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
  The `keepawake` auto-launcher fires a persistent macOS alert in this case.
- **Stale screenshot after auto-awake's launch** → DVT launches to foreground
  but screen content can lag a beat. If you need a settled screenshot, wait
  500 ms - 1 s before capturing.
- **`brew services start spyder` + registration but no tools visible** → the
  agent session needs a restart. MCP servers are loaded at session start;
  mid-session registration doesn't take effect.

## Further reading

- `README.md` — human-facing install and feature overview.
- `STABILITY.md` — pre-1.0 interaction-surface catalogue and gaps.
- `ios/README.md` — KeepAwake companion app build notes.
- `docs/audit-log.md` — release audit trail.
