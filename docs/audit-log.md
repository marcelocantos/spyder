# Audit Log

Chronological record of audits, releases, documentation passes, and other
maintenance activities. Append-only — newest entries at the bottom.

## 2026-04-18 — /release v0.1.0

- **Commit**: pending
- **Outcome**: First release. Published v0.1.0 for darwin-arm64,
  linux-amd64, and linux-arm64 via GitHub Actions; homebrew-releaser
  updated `marcelocantos/homebrew-tap` with a formula that runs
  `spyder serve` as a Homebrew service. Added STABILITY.md (pre-1.0
  surface catalogue), agents-guide.md (multi-step install + gotchas),
  and `--help-agent` flag. Rewrote README for the HTTP architecture
  that replaced the original bimodal mcpbridge scaffold mid-cycle.
  Auto-awake + persistent locked-device alerts via `alerter` verified
  end-to-end across Pippa (iPad Air 5) and a Samsung S23 Ultra.
