# Audit Log

Chronological record of audits, releases, documentation passes, and other
maintenance activities. Append-only — newest entries at the bottom.

## 2026-04-18 — /release v0.1.0

- **Commit**: `424ca56`
- **Outcome**: First release. Published v0.1.0 for darwin-arm64,
  linux-amd64, and linux-arm64 via GitHub Actions; homebrew-releaser
  updated `marcelocantos/homebrew-tap` with a formula that runs
  `spyder serve` as a Homebrew service. Added STABILITY.md (pre-1.0
  surface catalogue), agents-guide.md (multi-step install + gotchas),
  and `--help-agent` flag. Rewrote README for the HTTP architecture
  that replaced the original bimodal mcpbridge scaffold mid-cycle.
  Auto-awake + persistent locked-device alerts via `alerter` verified
  end-to-end across Pippa (iPad Air 5) and a Samsung S23 Ultra.

## 2026-04-19 — /release v0.2.0

- **Commit**: `0d64971`
- **Outcome**: Feature release. Shipped the reservation system (🎯T11:
  `reserve`/`release`/`renew`/`reservations` MCP tools; strict
  enforcement on every mutating tool; `spyder run` auto-reserves with
  owner defaulting to `filepath.Base(cwd)`; auto-awake cooperates via
  owner id `autoawake`). Added `daemon.Run` and `daemon.Build`
  embedder entry points plus graceful HTTP shutdown. Fixed the
  `parseIOSPID` / `autoawake.isKeepAwakeRunning` bundle-id-dash bug in
  both call sites. Test suite grew 27 → 105 functions across 10
  packages. STABILITY.md updated with the new surface and an honest
  assessment of shell-out coverage gaps. Published for darwin-arm64,
  linux-amd64, linux-arm64.

## 2026-04-19 — /release v0.3.0

- **Commit**: `79e28fc`
- **Outcome**: Service-polish release. Fixed the v0.2.0 Homebrew
  formula so the launchd service inherits a usable PATH (covers
  /opt/homebrew/bin and system dirs) — v0.2.0's install was silently
  broken when spyder ran as `brew services` because pymobiledevice3 /
  alerter / xcodegen / adb weren't resolvable. Added a persistent
  macOS alert when auto-awake hits pymobiledevice3's `'Security'`
  DvtException (developer profile not trusted on device) so users
  aren't hunting through logs to diagnose the Trust-on-device step.
  Documented brew-services env-var requirements in agents-guide.md,
  with a launchctl setenv snippet for non-Homebrew pymobiledevice3
  installs and a note that SPYDER_KEEPAWAKE_PROJECT is slated for
  removal once the KeepAwake Xcode project is go:embedded into the
  binary. Published for darwin-arm64, linux-amd64, linux-arm64.

## 2026-04-22 — /release v0.6.0

- **PR**: #21 (T23/T24), #22 (T25)
- **Outcome**: Architectural release. Shipped 🎯T23 (fuzzy reservation via
  selector predicates), 🎯T24 (sim/emu pool with two readiness tiers and
  server-owned linger-on-release), and 🎯T25 (bundled pmd3 bridge —
  Python FastAPI over Unix socket + Go supervisor + typed client;
  replaces every `exec.Command("pymobiledevice3", ...)` in the daemon;
  keep-awake is held via pmd3's `PowerAssertionService`, retiring the
  on-device KeepAwake companion app + its xcodegen/xcodebuild/devicectl
  deploy pipeline). Deleted `internal/tunneld/` (supervision absorbed
  into the bridge); `--tunneld-addr` flag removed from `spyder serve`.
  Release tarballs now ship `bin/spyder` + `libexec/pmd3-bridge/` so
  `brew services start spyder` works on a fresh machine without any
  `launchctl setenv PATH` surgery. Published for darwin-arm64,
  linux-amd64, linux-arm64.

## 2026-04-19 — /release v0.5.0

- **PR**: #20 (parallel fan-out), plus direct retire/prep commits
- **Outcome**: Large feature release — 9 targets shipped. Run-artefact
  store (🎯T20) landed first; then 8 parallel Sonnet subagents shipped
  🎯T13 (rotate), 🎯T14 (record), 🎯T15 (crashes), 🎯T16
  (install/uninstall/deploy), 🎯T17 (network), 🎯T18 (sim/emu
  lifecycle), 🎯T19 (logs + SSE live tail), and 🎯T21 (visual
  regression MVP: pixel RMS + manifest diff; SSIM and VLM stubbed).
  Added two design targets: 🎯T23 (fuzzy reservation via selector
  predicates) and 🎯T24 (sim/emu pool with two readiness tiers and
  server-owned linger-on-release). arm64-only host matrix; iOS
  physical devices out of scope for rotate/record/network (errored
  cleanly with structured messages). Published for darwin-arm64,
  linux-amd64, linux-arm64.

## 2026-04-19 — /release v0.4.0

- **Commit**: `d64b00e`
- **Outcome**: Small feature + security + correctness release.
  🎯T22 fixed the long-standing `devices({platform:"ios"})` stub
  (shipped broken across v0.1.0–v0.3.0, masked by the partial-
  results wrapper); iOS enumeration now fuses pymobiledevice3
  usbmux list and xcrun devicectl list devices by hardware UDID.
  Default HTTP bind changed to 127.0.0.1:3030 (loopback only) —
  the MCP endpoint is unauthenticated and a wildcard bind on
  shared Wi-Fi would let LAN peers drive devices; external
  exposure is opt-in via --addr. Go 1.24 idiom sweep across five
  sites (SplitSeq, range-over-int, CutPrefix). Filed and scored
  🎯T12–T21 for post-v0.4.0 work (REST API, run-artefact store,
  orientation, recording, crash reports, install/uninstall, net
  shaping, sim/emu lifecycle, log tailing, visual regression).
  Published for darwin-arm64, linux-amd64, linux-arm64.

## 2026-04-22 — /release v0.7.0

- **PR**: #25 (T26 umbrella: T26.1–T26.5)
- **Outcome**: Hardening release on the daemon↔pmd3-bridge pairing.
  Bridge transport moved to ephemeral loopback TCP + bearer token
  over stdio (🎯T26.1) — no more filesystem socket. Fail-fast model
  replaced retry/restart machinery; bridge unresponsiveness (transport
  error, deadline, unexpected exit) panics the daemon and launchd
  restarts. Liveness probe catches wedged-but-alive bridges — the
  exact class of bug that stranded a device on v0.6.0. Streaming for
  crash-report list (NDJSON) and pull (octet-stream) with a typed
  inter-packet stall error. Comprehensive logging across Go + Python:
  every MCP dispatch, shell-out, state transition, and pmd3 operation
  leaves a breadcrumb. Tiered developer test suite with committed
  `TEST-REPORT.json`; pre-push hook and `make bullseye` invariant
  reject stale reports. Retired fault-injection mocks (the bridge is
  paired, not hostile); detection logic tested at primitive layers
  (parseReadyLine pure, stallReader on io.Pipe, watchdog on plain
  subprocess). Published for darwin-arm64, linux-amd64, linux-arm64.

## 2026-04-22 — /release v0.8.0

- **PR**: #27 (fd-leak hotfix + 🎯T27)
- **Outcome**: Stability hotfix after v0.7.0 stranded attached devices
  ~4 min into a session. Root cause: services._lockdown callers
  never closed the pymobiledevice3 lockdown client, leaking a
  usbmux socket per call. autoawake polls list_devices every 2 s,
  so with 2 devices attached the bridge hit EMFILE within minutes;
  list_devices then returned pmd3_error (correctly classified as a
  structured BridgeError by the T26.2 fail-fast model, which
  logged+skipped rather than panicked), refreshes stopped, and
  device-side assertions timed out. Fixed by introducing
  _lockdown_ctx async context manager and routing every service
  caller through it; assertions.py similarly closes its lockdown
  client when the assertion task exits. Added T27 regression
  guard: fd-count delta assertions around real bridge subprocess
  against real attached device (validated: delta=+100 on unfixed
  code, delta=0 on fixed code, over 50 ListDevices calls).
  Discovered two follow-ups during release prep: 🎯T29 (automated
  awake/asleep signal — IOPMrootDomain queries count as user
  activity and don't distinguish states) and 🎯T30 (iOS 17+
  screenshot needs tunneld+RSD; regression from T25's "tunneld
  supervision absorbed into the bridge" where it was silently not
  re-implemented). Published for darwin-arm64, linux-amd64,
  linux-arm64.

## 2026-04-24 — /release v0.9.0

- **PR**: #29 (T31 KeepAwake restore)
- **Outcome**: Keep-awake-actually-works release. T25 (v0.6.0) deleted
  the KeepAwake companion app on the assumption that pmd3's
  PowerAssertionService was a drop-in replacement; this release cycle
  empirically confirmed it wasn't. Every assertion type accessible via
  iOS's com.apple.mobile.assertion_agent (PreventUserIdleSystemSleep,
  PreventUserIdleDisplaySleep, PreventSystemSleep) turned out to be a
  no-op for display auto-lock on iOS. v0.6.0 / v0.7.0 / v0.8.0 all
  shipped with autoawake claiming to keep devices awake while not
  actually doing so. T31 restored the companion app (ios/KeepAwake,
  SwiftUI, UIApplication.isIdleTimerDisabled=true when foregrounded)
  and rewrote internal/autoawake to launch it via xcrun devicectl on
  device arrival and periodically re-foreground. Per-developer signing
  identity required; documented in ios/README.md. Bridge PowerAssertion
  endpoints kept for potential future use but unwired from autoawake.
  Also raised 🎯T29 (automated awake/asleep signal investigation —
  IOPMrootDomain queries count as user activity) and 🎯T30 (iOS 17+
  screenshot broken — legacy screenshotr deprecated, needs tunneld+RSD
  restoration regressed at T25). Published for darwin-arm64,
  linux-amd64, linux-arm64.

## 2026-04-25 — /release v0.10.0

- **Commit**: `497b436`
- **Outcome**: T30 (iOS 17+ screenshot) and T32 (transparent autoawake)
  brought to working state via two structural changes. Screenshot
  rewired to use pmd3's DVT instrument over a tunneld-mediated RSD
  connection, replacing the legacy `com.apple.mobile.screenshotr`
  lockdown service that Apple deprecated in iOS 17 (the bridge had been
  returning `InvalidServiceError` on every modern device for months).
  HIL-verified against Pippa (iPad, iOS 26.3.1), Jevons (iPhone, iOS
  26.4.1), and Minicades (iPhone, iOS 26.2) — all three return real
  PNGs. Bridge `list_devices` now reads tunneld's HTTP registry as
  canonical so iOS 17+ enumeration is no longer subject to `pmd3 usbmux
  list`'s random drops. Autoawake redesigned from a one-shot trigger-
  based model into a convergence loop — every 15 s it observes each
  connected device's KeepAwake-running and KeepAwake-installed state
  and drives toward foregrounded; the user resolving a human gate
  (trusting a developer cert, unlocking the device, toggling Developer
  Mode) is detected on the next tick rather than requiring a re-plug.
  HIL-verified end-to-end. Daemon shutdown race fixed (clean exit 0
  under SIGTERM instead of panic). Ghost-UDID filter added (paired-but-
  unavailable devices no longer surface as autoawake targets). New
  typed BridgeError codes `tunneld_unavailable` and
  `developer_mode_disabled` plumbed Go-side with actionable error text.
  Stale auto-awake docs in agents-guide.md and README.md rewritten to
  match post-v0.9.0 / post-T32 behaviour. Targets 🎯T31 (KeepAwake
  restored — gap closed by convergence) and 🎯T32 (transparent install)
  retired; 🎯T33 (Swift sidecar) abandoned without work after the
  tunneld+DVT diagnosis; 🎯T34 (recover from stale KeepAwake install)
  raised as post-v0.10.0 follow-up. Published for darwin-arm64,
  linux-amd64, linux-arm64.

## 2026-04-25 — /release v0.11.0

- **Commit**: `d0d2cf8`
- **Outcome**: Re-release of v0.10.0 after that tag's release.yml run
  failed in the Test step. The autoawake-supervisor convergence
  rewrite (T32) removed the old `if s.bridge == nil { return }`
  early-exit at the top of Run(); the existing
  TestSupervisorNilBridge_RunExitsImmediately test relied on that
  exit happening within 2s, and on a fresh darwin-arm64 CI runner
  the convergence loop's initial `s.poll()` triggers
  `xcrun devicectl list devices` which takes >2s before devicectl
  has been warmed. Result: v0.10.0 was tagged on GitHub but no
  binaries were uploaded (Test → Build → Upload all skipped on
  failure). v0.10.0 left in place as a dead release for history;
  v0.11.0 bumps minor per the project's "always bump MINOR" rule
  and ships the same content as v0.10.0 was meant to ship plus the
  one-line fix in autoawake.Run() to short-circuit on a pre-
  cancelled context. PR #37 fixed the test; this release-prep PR
  bumps STABILITY.md snapshot to v0.11.0 and records the audit-log
  entry. Published for darwin-arm64, linux-amd64, linux-arm64.

## 2026-04-26 — /release v0.12.0

- **Commit**: `pending`
- **Outcome**: Hotfix release for 🎯T35. Post-v0.11.0 MCP testing surfaced
  that every `brew install marcelocantos/tap/spyder` ran with the
  bundled pmd3-bridge undiscoverable: the daemon's `resolveBridgeBinary`
  computed the bridge path relative to `os.Executable()`, which on
  macOS / Linux Homebrew installs returns the symlink path (e.g.
  `/opt/homebrew/bin/spyder`) rather than the resolved Cellar path.
  The libexec sibling lives next to the real binary inside the Cellar,
  not next to the symlink under `/opt/homebrew/libexec`. Fix (PR #40):
  add an `exePathReal()` helper that calls `filepath.EvalSymlinks` on
  `os.Executable()` before computing the relative path. Verified
  against a simulated Homebrew layout: bridge resolves through the
  symlink correctly. v0.12.0 ships the fix; brew-services-supervised
  daemons still hit 🎯T36 ("No route to host" on loopback tunneld) so
  `brew services start spyder` users will see iOS screenshot fail
  until that follow-up lands. Foreground `spyder serve` works fully
  on a Homebrew install for the first time. Published for darwin-arm64,
  linux-amd64, linux-arm64.
