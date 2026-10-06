# 🎯T105 spike: `devicectl device process launch` as a replacement for the current iOS-17+ launch path

## Current implementation (as of this spike)

`IOSAdapter.LaunchApp` (internal/device/ios.go) already branches by iOS major
version:

- **iOS ≤16**: `instruments.ProcessControl.LaunchAppWithArgs` over go-ios's
  DTX-over-lockdown transport (no CoreDevice tunnel required).
- **iOS 17+**: `appservice.Connection.LaunchApp` — go-ios's own
  CoreDevice/RemoteXPC implementation of
  `com.apple.coredevice.feature.launchapplication`, the same XPC feature
  `xcrun devicectl device process launch` uses under the hood. The
  connection comes from `a.asPool`, a per-UDID pooled appservice connection
  reused across launch/pid-verify/terminate calls so the tunnel/XPC
  handshake cost is paid once per device, not once per call.

So the iOS-17+ path is **already CoreDevice-native** — it does not shell out
to `xcrun devicectl`. The open question this spike answers is narrower than
"should we adopt devicectl at all" (we already use its underlying protocol):
it's "should `LaunchApp`'s iOS-17+ branch be replaced by shelling out to the
`devicectl` CLI binary itself, specifically its `--activate` foreground
flag, instead of calling go-ios's in-process appservice client?"

`devicectl` the CLI is still used elsewhere in this codebase for install /
uninstall (`runDevicectl`, with a 30s timeout wrapper) because devicectl's
signing/provisioning handling is hard to replace, and for `TerminateApp`'s
iOS-17+ "graceful termination" path (`devicectl device process terminate
--pid`), which was deliberately chosen over go-ios's `KillProcess` because
CoreDevice's own termination avoids an immediate app relaunch that was
observed with go-ios's SIGKILL path.

## `devicectl device process launch` CLI surface (Xcode's bundled devicectl)

Relevant flags from `xcrun devicectl device process launch --help`:

- `--activate` / `--no-activate` (default `--activate`) — foregrounds the
  launched app. This is the "foreground-launch" flag named in the target.
- `--terminate-existing` — kill any running instance before relaunch.
- `--environment-variables <json>` — set child env (also: `DEVICECTL_CHILD_*`
  prefixed vars from the calling shell).
- `--console` — bridge the app's stdout to devicectl's own stdout (useful
  for debugging, not relevant to spyder's launch path).
- `--json-output <path>` — structured result, including the launched pid.
- No direct argv-passing flag analog beyond trailing
  `<command-line-arguments>` positional args (comparable to go-ios's launch
  args).

## Comparison

| Dimension | Current: go-ios appservice (in-process) | Candidate: `devicectl` CLI shell-out |
|---|---|---|
| Session/connection cost | Pooled per-UDID connection (`asPool`), amortized across launch/pid-verify/terminate | New process + new CoreDevice/RemoteXPC handshake every invocation (devicectl doesn't expose a persistent-connection mode to callers); this is the same reason the install/terminate call sites wrap it in a 30s timeout — devicectl invocations are known to be slow/occasionally wedge |
| Reliability | In-process error returned directly; typed errors already mapped to spyder's "app not installed" convention | Must parse devicectl's stdout/stderr/exit code (or `--json-output <path>` + read a temp file) to detect the same failure classes; more string-matching surface, another place for a devicectl hang to need its own timeout+kill plumbing (duplicating what `runDevicectlCtx`/`devicectlTimeout` already does for install/terminate) |
| pid-verify support | `AppPID` already does a *separate* appservice `ListProcesses()` call after launch, independent of which launch path was used — this isn't entangled with the launch call itself | Same: `AppPID` would be unchanged; `devicectl`'s own `--json-output` *does* return the launched pid directly in principle, which would let launch+pid-verify collapse into one call — a real minor win, but not one that outweighs the connection-cost regression below |
| Foreground guarantee | go-ios's `appservice.LaunchApp(..., activate bool)` call already takes an `activate`-equivalent bool (currently passed `false` in the call at ios.go:994, i.e. launched *not* activated/foregrounded — worth separately flagging, see below) | `--activate` is the devicectl default |
| Process cost per device roundtrip | None beyond the pooled XPC call | New `xcrun devicectl` subprocess fork/exec, argv/env marshalling, JSON output parse — measurably slower than an in-process pooled XPC call, and this cost repeats on every single launch (spyder launches apps frequently during session churn, pool recycling, and test loops) |

## Side finding (not in scope for this spike's acceptance, filed separately)

`launchAppAppservice` calls `conn.LaunchApp(bundleID, args, envArg, nil,
false)` — the final bool (go-ios's `activate`/foreground flag) is
hard-coded `false`. If spyder intends every `LaunchApp` call to foreground
the app (matching devicectl's own `--activate` default, and matching what a
human workflow expects — "launch" implies "bring to front"), this is a
plausibly-real pre-existing bug independent of the devicectl question. Filed
as 🎯T158 rather than fixed in this spike: fixing launch-path
foreground behavior is implementation, not evaluation, and risks conflating
"does devicectl's CLI replace our launch path" with "is our current
behavior correct" in one commit.

## Decision: **reject** (do not replace the in-process appservice launch
path with a `devicectl` CLI shell-out)

Rationale:

1. The underlying protocol devicectl uses for `process launch` is the same
   CoreDevice/RemoteXPC `launchapplication` feature go-ios's `appservice`
   already speaks in-process via a pooled connection. Shelling out to the
   CLI binary buys no new capability spyder doesn't already have through
   go-ios, and loses the connection pooling that amortizes tunnel/XPC setup
   cost across launch → pid-verify → terminate.
2. CLI shell-outs are the *costlier* path in this codebase today — that's
   precisely why install/uninstall and the terminate "graceful kill" call
   sites wrap `runDevicectl` in an explicit timeout-and-kill harness
   (`devicectlTimeout`, 30s + 2s grace). Routing the hot launch path through
   the same mechanism would import that latency/timeout-plumbing burden
   into the single most frequently-called device operation in the adapter.
3. devicectl is retained (correctly) for the two things it's uniquely good
   at in this codebase: install/uninstall signing handling, and the
   terminate-without-relaunch behavior observed empirically with Stock Cars.
   `process launch --activate` doesn't offer an analogous capability
   advantage over the existing appservice path to justify paying the
   shell-out cost on every launch.

No code change lands from this spike (per T105's acceptance criteria — a
spike records a decision, not an implementation). The one adjacent
correctness question surfaced (launch not foregrounding by default) is
filed as a follow-up target rather than folded into this commit.
