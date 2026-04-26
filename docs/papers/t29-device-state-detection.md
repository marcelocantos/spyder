# T29: Device-State Detection — Investigation Notes

**Target:** 🎯T29 — Automated device-state detection: mechanical signal
for "device is currently asleep/awake" queryable from pmd3-bridge.

**Date:** 2026-04-26
**Status:** Prototype implemented (ScreenshotService path); HIL verification pending.

---

## Problem Statement

The stay-awake regression test (🎯T27) is an fd-count guard, not a
behavioural assertion. We need a mechanical signal that can answer
"is the device screen on/off?" without itself resetting the idle timer
— so that `TestDevice_StaysAwake_Mechanical` can verify that spyder's
power assertion actually prevents display-off/sleep.

The previous attempt (DiagnosticsService.ioregistry IOPMrootDomain
queries for `IOPMUserIsActive` / `SleepWakeUUID` / `System Idle Seconds`)
**failed**: the lockdown query itself counts as user activity and reports
`IOPMUserIsActive: true` even against a confirmed-asleep device. That
path is ruled out.

---

## Candidate Paths

### 1. ScreenshotService behaviour (RECOMMENDED — implemented)

**Hypothesis (user suggestion 2026-04-22):** A screenshot succeeds when
the device is awake and fails when the display is off or the device is
asleep.

**Code path:** `bridge/src/pmd3_bridge/services.py` → `screenshot(udid)`
→ pmd3's DVT `Screenshot` instrument over a tunneld RSD connection.

**Mechanism:** The DVT `Screenshot` service requires an active framebuffer.
When the display is off (device locked/sleeping), the framebuffer is
either powered down or in a low-power state that DVT does not expose.
pmd3's `Screenshot.get_screenshot()` call either times out or raises an
exception when the display is off.

**Non-observation criterion:** Screenshot does **not** write to IOPMrootDomain
user-activity registers — it reads from the framebuffer, which is a GPU/
display driver operation, not a PM user-activity signal. This means it
should satisfy the non-observation requirement.

**Implementation:** `/v1/device_power_state` calls `screenshot()` and
classifies the outcome:
- Success (PNG returned) → `"awake"` (display on, framebuffer readable)
- BridgeError `"pmd3_error"` with display-off heuristic → `"display_off"` or `"asleep"`
- BridgeError `"tunneld_unavailable"` → `"unknown"` (cannot determine)
- BridgeError `"developer_mode_disabled"` → `"unknown"` (prerequisite missing)

**Pixel heuristic fallback:** If screenshot succeeds but all pixels are
black or near-black, this is a secondary "display off" signal (candidate #5
from the task description). Implemented as an optional refinement inside
the same endpoint.

**Testing:** Requires SPYDER_DEVICES=1 (real device) to verify awake vs
asleep transitions. HIL verification steps:
1. Device awake, screen on → `/v1/device_power_state` → `"awake"`
2. Lock device (press sleep button) → wait 5 s → query → `"display_off"` or `"asleep"`
3. Confirm the first query did not re-wake the device (check screen
   physically; optionally check System Idle Seconds via DiagnosticsService
   before and after — the spike value should not reset)

**Risk:** Some pmd3 exception shapes are not yet catalogued for the
"display off" case. The endpoint currently returns `"unknown"` for
unrecognised pmd3_error messages, which is safe. As HIL testing populates
the real exception strings, the heuristic matchers in `services.py` can
be tightened.

---

### 2. OsTraceService / syslog for IOPMrootDomain sleep transitions (NOT IMPLEMENTED)

**Idea:** Subscribe to the device syslog stream via pmd3's `OsTraceService`
and filter for `IOPMrootDomain` messages that indicate `"sleep"` /
`"wake"` transitions.

**Problem:** This is a streaming/subscription model — the endpoint would
need to maintain a long-lived connection and accumulate state. That's
architecturally heavier than a point-in-time query. It also requires the
process to be running *before* the sleep event to catch it — it can't
answer "is the device currently asleep?" retroactively without a prior
subscription.

**Non-observation:** OS-trace subscribe does not touch PM user-activity.
Satisfies the criterion.

**Verdict:** Viable as a supplementary signal but wrong shape for a
synchronous `/v1/device_power_state` query. Could be added later as a
push notification path if the ScreenshotService approach proves too slow
(screenshot takes ~1-2 s on device).

---

### 3. SpringBoardService lock-state queries (NOT TESTED)

**Idea:** pmd3 has a `SpringBoardServicesService` that can query lock
state. Possible keys: `SBGetScreenLockStatus`, `SBGetActivationState`.

**Code location:** `pymobiledevice3.services.springboard` (if it exists in
the installed version).

**Problem:** SpringBoard queries go through lockdown. The concern from
the original spike is that lockdown queries count as user activity. The
`IOPMUserIsActive` failure mode suggests lockdown touch = activity event.
SpringBoard queries use the same lockdown transport — same risk.

**Non-observation:** Unverified. High risk of same observation-resets-timer
problem as DiagnosticsService.

**Verdict:** Not pursued. Same transport problem as the original failed
attempt. Would need careful HIL testing before trusting.

---

### 4. DiagnosticsService power/display keys (RULED OUT)

**Idea:** Query IOPMrootDomain via `DiagnosticsService.ioregistry()` for
display-specific keys (`AppleDisplaySleepKey`, `TimeToSleep`, etc.).

**Problem:** This is exactly the approach that was tried and failed.
The lockdown query itself counts as user activity and resets the idle
timer / reports `IOPMUserIsActive: true`. All variants of DiagnosticsService
that go through lockdown share this problem.

**Verdict:** Ruled out. The task description explicitly bans this path.

---

### 5. Screenshot pixel-heuristic (IMPLEMENTED as supplement to #1)

**Idea:** If the screenshot succeeds but returns an image where all (or
nearly all) pixels are black, the display may be in a content-blank state
(e.g., screen saver, or display just coming on). Not a primary signal but
useful as a refinement.

**Implementation:** The Python `device_power_state()` function checks: if
the screenshot is a valid PNG but is entirely or nearly entirely black
(first 2 KB of decoded pixel data), it maps to `"display_off"` rather
than `"awake"`. This handles the edge case where DVT can read a black
framebuffer but the device isn't meaningfully "awake".

**Non-observation:** Same as #1 — no PM user-activity writes.

**Verdict:** Implemented as a refinement inside candidate #1. Threshold
is conservative (>99% black pixels) to avoid false positives on dark
wallpapers.

---

## Recommendation

**Implement candidate #1 (ScreenshotService)** for the initial prototype.

Rationale:
- Simplest implementation — reuses existing `screenshot()` service function.
- Non-observation criterion is met (DVT reads framebuffer, not PM activity).
- Already tested infrastructure — the DVT Screenshot path was validated
  in 🎯T30.
- Degrades gracefully: tunneld_unavailable → `"unknown"` rather than
  error; developer_mode_disabled → `"unknown"` with actionable message.

**What remains for HIL verification:**
- Test against Pippa (00008103-000D39301A6A201E) with screen on/off.
- Document the exact pmd3 exception string that surfaces when display is off.
- Tighten the exception heuristic matchers from `"unknown"` to `"asleep"` or
  `"display_off"` once real exception shapes are known.
- Run `TestDevice_StaysAwake_Mechanical` with SPYDER_DEVICES=1 against a
  device where: (a) spyder holds power assertion — should report `"awake"`;
  (b) no assertion held — after idle timeout should report `"display_off"`.

---

## IOKit Properties Investigated

| Property | Surface | Outcome |
|---|---|---|
| `IOPMUserIsActive` | DiagnosticsService.ioregistry | FAILED — lockdown query resets idle timer |
| `SleepWakeUUID` | DiagnosticsService.ioregistry | FAILED — same lockdown transport |
| `System Idle Seconds` | DiagnosticsService.ioregistry | FAILED — same lockdown transport |
| DVT Screenshot | tunneld RSD / Screenshot instrument | PROTOTYPE — see §1 above |
| OsTraceService syslog | tunneld RSD | Not tested — wrong shape for sync query |
| SpringBoard lock state | lockdown | Not tested — same transport risk as DiagnosticsService |
