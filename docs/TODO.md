# TODO

Tracked as bullseye targets in `bullseye.yaml`. This file is for ad-hoc
notes that haven't yet earned a target.

## Unscoped

- Investigate whether `devicectl` on iOS 17+ exposes a cleaner
  foreground-launch API than `process launch`.

- **streamrelay session GC** (2026-07-18): `GET /stream/sessions` retains
  zombie entries for dead servers/players (observed: sessions hours old with
  0 fps after `kill -9`). Cull on sideband/wire death or age them out. Related
  unexplained anomaly: with ~10 orphaned servers alive, a fresh player attach
  hung at the SessionConfig handshake even though the catalogue pointed at a
  live new-code server; never root-caused — suspect relay bookkeeping under
  many replaced-but-alive registrations.

- **Self-restart without a supervisor kills the daemon** (2026-07-18): the
  T99.3 stuck-dispatch watchdog exits "for supervised relaunch", but a bare
  `spyder serve` in a terminal has no supervisor — the daemon just dies
  (observed live: emulator launch_player exceeded the dispatch deadline →
  watchdog exit → stack down). Either self-exec relaunch when unsupervised,
  or gate the exit on detecting a supervisor.
- **launch_player dispatch deadline too tight for emulator installs**
  (2026-07-18): a cold-emulator `adb install` legitimately exceeds the
  deadline (stuck goroutine was deployApp mid-install, tools.go:1564).
  Give install-bearing dispatches an emulator-scaled deadline, or split
  install progress from the stuck heuristic.

- **Daemon restart footguns compound** (2026-07-19): after the watchdog
  exit, a bare `bin/spyder serve` relaunch bound loopback-only (the
  default) — every LAN glass then failed to attach and exited fail-closed,
  which looked like a device/app problem (an hour of misdiagnosis:
  extras, Samsung Freecess, macOS firewall). Any supervised/self relaunch
  must preserve the original listen addr; consider logging a prominent
  warning when serving loopback-only while LAN devices are in inventory.
