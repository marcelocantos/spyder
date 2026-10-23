# Resident game-input agent (🎯T175)

Test automation on a physical iPhone or iPad needs an XCTest UI-automation
session. iOS asks for approval (Settings › Developer › Enable UI Automation,
or a Touch ID / passcode prompt) each time a new session starts after a
reboot or runner install. If every regression run starts its own runner,
someone has to approve at the device on every run. Instead the main Spyder
daemon (`spyder serve`, :3030) keeps one runner alive per tethered device,
and test harnesses attach to it.

## Design

The 🎯T163 agent supervisor already owns one XCTest runner per tethered
iOS device. It checks the runner on every tick, relaunches it within an
attempt cap, reports consent, lock and disconnect, and gates control on the
device's reservation holder. 🎯T175 adds a second **backend** to that
supervisor for chosen devices:

| Backend | Runner bundle | Purpose |
|---|---|---|
| `alertrunner` (default) | `com.marcelocantos.spyder.AlertRunner.xctrunner` | `system_alert` / `system_alert_tap` (🎯T154). No game input. |
| `devicekit` | `com.mobilenext.devicekit-iosUITests.xctrunner` | Game input over DeviceKit JSON-RPC on device port 12004. |

Why DeviceKit runs as a supervisor backend instead of being added to
AlertRunner:

- Stock Cars' harness and `ios runwda` reinstantiation (🎯T167) already
  use DeviceKit's JSON-RPC on :12004, so there is no new wire protocol.
- AlertRunner has no input protocol. Adding taps, swipes and text to it
  would be a large Swift project and a new contract.
- Each device runs exactly one runner. Running AlertRunner and DeviceKit
  on the same device would mean two XCTest sessions and two consent
  prompts. On a DeviceKit device, AlertRunner taps are refused and T167
  stays away from that device.

There is no separate `--agents-only` daemon. The resident runner belongs
to the daemon that owns reservations.

## Configuration

Set these in the daemon's environment. For :3030 that is the
`environment=` line in the installed
`/opt/homebrew/etc/supervisor.d/spyder.ini`. The repo template carries
placeholders, and `SPYDER_IOS_AGENT_DEVICES=<udid,…> supervisor/install.sh`
renders `devicekit` with those UDIDs. Without it, the installer renders
`alertrunner`.

| Variable | Values |
|---|---|
| `SPYDER_IOS_AGENT_BACKEND` | unset or `alertrunner`: every device uses AlertRunner. `devicekit`: devices listed in `SPYDER_IOS_AGENT_DEVICES` use DeviceKit. |
| `SPYDER_IOS_AGENT_DEVICES` | Comma-separated hardware UDIDs, or `*` for every device. Required when the backend is `devicekit`. |
| `SPYDER_IOS_AGENT_CONSENT_WINDOW` | How long a live DeviceKit launch waits for the owner's Enable UI Automation approval (Go duration, 1m–2h). Default `10m`. |

Any other value (an unknown backend, `devicekit` with no devices, a
malformed UDID, or a bundled `ios` binary that is missing or not
executable) makes supervision **fail closed** for every device. The daemon
logs `resident agent backend misconfigured` and never silently falls back.

The runner is started by the daemon's own bundled go-ios
(`libexec/spyder/ios` in Homebrew, `bin/ios` in a tree build) like this:
`ios runwda --udid=<udid> --bundleid=com.mobilenext.devicekit-iosUITests.xctrunner
--testrunnerbundleid=… --xctestconfig=devicekit-iosUITests.xctest`. There is
no separate pinned CLI. DeviceKit itself must already be installed on the
device; the supervisor never installs or reinstalls it.

## Behaviour

- **No lifetime limit.** The runner runs until the daemon stops it, the
  device disconnects or reboots, or the runner exits. The AlertRunner hold
  test likewise has no hourly limit after `make alert-runner` reinstalls it.
- **Readiness is proved, not assumed.** A runner is ready only when it
  answers a `device.apps.state` JSON-RPC request on :12004 over that
  device's USB with an explicit state. A running process alone is not
  enough.
- **Unattended relaunch.** Every 30 s tick restarts a dead runner, up to
  the attempt cap (3). Each launch counts as one attempt, however long it
  waits for consent; observing the same state again on a tick does not
  count. A daemon restart, a device reboot (new boot id), or
  `agent_ensure` with `retry=true` from the reservation holder gives a
  fresh budget. A plain `agent_ensure` does not reset the cap.
- **Ownership is the launch.** The daemon owns a runner while the
  `ios runwda` host process it spawned is alive, whatever pid the device
  reports for the runner (approval can restart it under a new pid).
- **No adoption.** A DeviceKit runner driven by a host this daemon did not
  start (a harness, other tools, another live spyder daemon) is reported
  and left alone. The error names the host `runwda` pid, binary and parent.
  Stop that process, then call `agent_ensure`.
- **Stale and orphaned runners are replaced.** A runner left on the device
  with no `runwda` host on this Mac is killed on the device (process kill,
  no reinstall) before a fresh launch. A `runwda` left by a previous spyder
  daemon (spyder's own bundled `libexec/spyder/ios`, DeviceKit bundle,
  parent gone) is sent SIGTERM (SIGKILL after 5 s) and the runner is
  relaunched.
- **Exits are logged.** Every launch exit is logged
  (`resident agent launch exited`) with host pid, exit status and the last
  20 lines of output, and the latest one is `last_exit` in `agent_status`.
- **Owner checks.** `agent_ensure` and `agent_idle` need the device's
  reservation holder. The holder is checked again after the probe, so a
  reservation lost during the probe cannot start or stop anything.
- **Idle keeps the runner.** `agent_idle` stops the app under test and
  drops leases and host sessions, but leaves a healthy runner running.
- **Clean shutdown.** When the daemon stops, all runners are stopped in
  parallel within supervisord's `stopwaitsecs=10`. If the daemon is killed
  with SIGKILL, an orphaned `runwda` can keep the old runner alive. The new
  daemon terminates it and relaunches (see above).
- **Go-ios startup serialization.** DeviceKit launch holds the per-device
  go-ios initializer lock for a 5 s settle window, so the launch never
  overlaps another RSD/XPC handshake on the same device (🎯T161).

## Consent

Apple's approval cannot be bypassed and is never inferred. When a launch
is alive but its runner does not answer within 45 s, the condition becomes
`consent_pending` (`consent_pending=true`, `host_pid` set, `consent.message`
says what to do). The launch stays owned and tracked: the daemon keeps
polling readiness on that same launch every 3 s until it answers, exits,
or `SPYDER_IOS_AGENT_CONSENT_WINDOW` (default 10 min from launch) passes.
Ticks and `agent_ensure` (even with `retry=true`) neither relaunch it nor
count it again. Once the owner approves and the runner answers, the
condition becomes `ready` on the next tick or `agent_ensure`, and the
consent message is cleared.

If the window passes, the launch is stopped and the condition becomes
`consent_required`. A launch that exits with Apple's consent refusal in its
output also records `consent_required`. Neither is relaunched unattended:
with someone at the device, call `agent_ensure(device, owner, retry=true)`.
A launch that exits for any other reason is relaunched on the next tick
within the attempt cap; see `last_exit` for why it ended.

Expect one approval per device after each reboot or new DeviceKit install,
and none after that.

## Harness use

A harness should not start or stop DeviceKit itself. Each run:

1. Reserve the device on :3030.
2. `agent_ensure(device, owner)`, then check that `agent_status` shows
   `ready=true`, `runner_healthy=true`, `runner.bundle_id` is the DeviceKit
   bundle and `owner` matches.
3. Open its own USB forward to device port 12004 and speak JSON-RPC.
4. When done, close the forward, stop the game, call `agent_idle`, and
   release the reservation. The runner stays up for the next run.

## Live check

This check requires access to the device and needs a daemon running this
build with the backend enabled. While :3030 or another daemon supervises
the same device, use a scratch daemon only if no other daemon is running
an XCTest runner on that device. Two supervisors on one device fight over
the XCTest session.

```sh
SPYDER_DATA_DIR=$(mktemp -d) SPYDER_IOS_AGENT_BACKEND=devicekit \
  SPYDER_IOS_AGENT_DEVICES=<udid> bin/spyder serve --addr 127.0.0.1:<port>
# reserve <udid> as <owner> on that daemon, then:
curl -s -XPOST 127.0.0.1:<port>/api/v1/agent_ensure -d '{"device":"<udid>","owner":"<owner>"}'
curl -s -XPOST 127.0.0.1:<port>/api/v1/agent_status -d '{"device":"<udid>"}'
```

Pass criteria: `ready=true` with the DeviceKit runner pid. A second
`agent_ensure` reuses the same pid. Killing the runner leads to a relaunch
within one tick. After a daemon restart, a new runner comes up with no
second approval while the device has not rebooted, and no `STREAM_CLOSED`
appears in the log.
