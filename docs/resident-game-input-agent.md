# Resident iOS game-input automation

`agent_ensure` and `agent_idle` can supervise the installed DeviceKit game-input
runner as well as the existing AlertRunner. Select one backend per daemon.
An existing AlertRunner cannot serve DeviceKit's JSON-RPC input protocol.

For a separate local service, run `spyder serve --agents-only --addr
127.0.0.1:3140` with a separate `SPYDER_DATA_DIR` containing the selected device
inventory. Set these environment variables before starting the service:

- `SPYDER_IOS_AGENT_BACKEND=devicekit`
- `SPYDER_IOS_AGENT_CLI`: absolute path to the installed go-ios launcher.
- `SPYDER_IOS_AGENT_CLI_SHA256`: SHA256 of that exact executable.
- `SPYDER_IOS_AGENT_DEVICES`: comma-separated hardware UDIDs, not CoreDevice IDs.

The service reuses the existing tunnel. It does not start another tunnel or
the lab-wide battery, development-policy, Wi-Fi or wedge monitors. The first
agent start requires an explicit reserved client; background checks do not
open new consent prompts. Missing DeviceKit installation requires an explicit
install; supervision does not reinstall a healthy runner.

An authorized client reserves its device, calls `agent_ensure`, and verifies
the returned hardware, runner bundle and PID. Readiness requires a real
DeviceKit app-state response over that exact USB device, not a process alone.
The client owns its temporary forwarding connection. On completion it stops
its game, calls `agent_idle`, closes its forwarding connection and releases
its reservation. The daemon keeps the runner's XCTest channel open, with no
client or hourly lifetime. Idle retains no client build/Unity lease.

Stopping the service ends its owned native launcher. A dead channel, a foreign
pre-existing runner, a locked device or missing consent cannot be adopted as
ready. Apple can still invalidate authorization; the service cannot provide
the owner's passcode or biometric approval. A failed unattended session is
reported rather than repeatedly opening approval prompts.

The earlier AlertRunner-only `testHoldAgent` still has its historical one-hour
limit. It is not the runner used by this DeviceKit backend. A long-duration
device observation is separate from the two-client reuse check.
