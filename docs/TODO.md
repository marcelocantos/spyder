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
