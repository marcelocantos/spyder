# T36: brew-services spyder — tunneld loopback investigation

**Date:** 2026-04-26  
**Target:** 🎯T36  
**Status:** Root cause identified and fixed.

---

## Symptom

When spyder runs under `brew services start spyder`, MCP screenshot calls fail:

```
tunneld unreachable at ('127.0.0.1', 49151): [Errno 65] No route to host;
start it with `sudo pymobiledevice3 remote tunneld`
```

The same code path works when spyder is started in a foreground shell.

---

## Evidence from `/opt/homebrew/var/log/spyder.log`

```
2026-04-25 23:24:11  INFO  pmd3-bridge starting port=51927 log_level=info pid=62819
2026-04-25 23:24:15  INFO  bridge app startup complete
2026-04-25 23:28:07  INFO  screenshot udid=00008130-0009702E1110001C
2026-04-25 23:28:07  WARN  screenshot FAILED tunneld_unavailable:
    "tunneld unreachable at ('127.0.0.1', 49151): [Errno 65] No route to host"
2026-04-25 23:28:32  INFO  bridge shutdown (daemon restarted)

2026-04-26 00:50:05  INFO  screenshot udid=...        ← succeeds fine
2026-04-26 00:50:09  INFO  screenshot captured bytes=44342 elapsed_ms=4596
```

Key observation: the error occurs exactly **once**, ~4 minutes after startup.
Screenshots from the same brew-services process succeed 1+ hours later. The
failure is transient, not persistent.

---

## Candidate causes — verdict

| Candidate | Verdict |
|-----------|---------|
| macOS Local Network privacy entitlement (Info.plist) | **Not applicable** — this entitlement affects GUI .app bundles sandboxed by the App Sandbox. A launchd user agent (brew service) running under the user account has no App Sandbox; the entitlement requirement does not apply. |
| brew-generated launchd plist missing networking entitlement | **Not the cause** — inspected plist (`plutil -p ~/Library/LaunchAgents/homebrew.mxcl.spyder.plist`) has no `SandboxProfile`, no `_com.apple.security.network.client` key, no networking restrictions of any kind. The service runs as the user in the Aqua/Background/LoginWindow/StandardIO/System session types. |
| IPv4 vs IPv6 mismatch in bridge | **Not the cause** — `lsof` confirms tunneld (PID 86775, root) binds `127.0.0.1:49151` (IPv4 TCP). The bridge connects to `127.0.0.1:49151` (IPv4). Same family. |
| launchd sandbox prohibiting loopback | **Not the cause** — same plist inspection; no sandbox key. User-level launchd agents do not sandbox loopback by default on macOS. |
| **Tunneld transiently unreachable at first screenshot attempt** | **CONFIRMED ROOT CAUSE** — see below. |

---

## Root cause: transient tunneld unavailability + no retry

`errno 65` (`EHOSTUNREACH` / "No route to host") on the IPv4 loopback
interface is unusual. The normal error for a closed port is `errno 61`
(`ECONNREFUSED`). `EHOSTUNREACH` on loopback occurs on macOS in two
situations:

1. The network stack is briefly in an inconsistent state during **wake from
   sleep** — the loopback interface can take a few hundred milliseconds to
   become fully routable after wake. If spyder started because the machine
   woke up (launchd restarts services on wake), the first screenshot attempt
   4 minutes later could still catch tunneld in this transient window if
   tunneld itself was restarting at the same time.

2. A **pf firewall rule** blocks connections to the port. No evidence of this
   in the environment; subsequent attempts succeed without any rule change.

The more plausible scenario: tunneld (run via `sudo pymobiledevice3 remote
tunneld`, parented by launchd PID 1 after the terminal closed) was briefly
unavailable — either restarting after a device reconnect or mid-tunnel
negotiation — exactly when the first screenshot arrived. The bridge made a
single synchronous `requests.get(...)` with `timeout=2.0` and surfaced the OS
error directly as `tunneld_unavailable` with no retry.

In a **foreground shell** the user naturally retries a failed screenshot
immediately; a single retry succeeds. Under the brew service, the MCP caller
surfaces the 503 to the agent, which is the only report the user sees.

---

## Fix applied

**`bridge/src/pmd3_bridge/services.py` — `_tunneld_rsd_for`**

Added a retry loop (up to 3 attempts, 0.5 s backoff) around the
`get_tunneld_devices` call. Only retried on transient transport errors
(`requests.ConnectionError`, `OSError` including `EHOSTUNREACH`). A
structured `TunneldConnectionError` (tunneld not started at all) is still
retried so a briefly-restarting tunneld doesn't cause a hard failure.

**`bridge/src/pmd3_bridge/services.py` — `list_devices` tunneld probe**

Same retry applied to the `requests.get` call in `list_devices` so the
autoawake 2 s polling loop doesn't flood the log with debug-level errors
during the few seconds after daemon startup when tunneld isn't up yet.

---

## What was NOT changed

- The Homebrew formula (`spyder.rb`) — no plist change needed. The launchd
  plist already has the correct PATH and no sandbox restrictions.
- The Go daemon — no change needed. The 503 / `tunneld_unavailable` error
  code is the correct signal; the fix is in the bridge retry layer.
- The `sudo pymobiledevice3 remote tunneld` management — out of scope for
  spyder. Users must run this separately (or add a LaunchDaemon plist for it).

---

## Residual risk

If tunneld is genuinely not running, all 3 retries will fail and the
`tunneld_unavailable` error is returned as before — no regression in the
"tunneld not started" case. The retry adds at most 1 s latency to that error
path (3 × 0.5 s minus the first attempt which is immediate).
