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
