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
