# Spyder pairing host (🎯T93 / 🎯T93.1)

## Decision (2026-07-11)

For the **dev streaming modality** (player on device ↔ ge tiltbuggy / game
server), **spyder owns QR / out-of-band credential delivery to the player**.

Pigeon's pairing ceremony and session protocol require a **rendezvous
token** (the auth material the player must hold before `pairing.Initiate`)
to reach the player out-of-band. The pigeon repo documents QR code exchange
as the primary delivery channel. In *this* product shape that channel is
**not** ge's job and **not** a free-standing `pigeon pair` CLI as UX — it is
spyder's:

| Role | Owner |
|------|--------|
| Mint / drive pairing ceremony (acceptor) | **spyder** (`internal/pairinghost`) |
| Surface token as QR (dashboard / `GET …/qr`) | **spyder** |
| Optional adb / xcrun inject of the same token | **spyder** (planned) |
| Hold server-side `PairingRecord` after ceremony | **spyder** (`~/.spyder/pairing/`) |
| H.264 encode + game sim | **ge** (activation-mode `Register` with injected record) |
| Scan QR / consume token, run initiator | **player** |
| Encrypted data path after pair | **pigeon relay / LAN** — spyder OFF the path |

## HTTP surface

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/stream/pair` | Begin ceremony → `{id, token, status, …}` |
| `GET` | `/stream/pair/{id}` | Status (+ 6-digit SAS code when ready) |
| `GET` | `/stream/pair/{id}/qr` | Unicode QR of the rendezvous **token** |
| `POST` | `/stream/pair/{id}/confirm` | Operator confirms SAS match |
| `POST` | `/stream/pair/{id}/abort` | Cancel |
| `GET` | `/stream/pairings` | Known peer instance IDs |

Env: `SPYDER_PIGEON_RELAY` (default `https://carrier-pigeon.fly.dev`).

## Build / dependency note

Default spyder must keep the **go-ios quic-go pin** (`v0.40.1-…`) for the
userspace tunnel. Pigeon ≥0.30's `pairing` package pulls cwire + a newer
quic-go and a vendored libsodium static lib — those conflict with go-ios
under a single module graph.

So this package ships in two layers:

1. **Always on:** identity, `PairingRecord` store, HTTP routes, QR of a
   token (`pigeon/crypto` + `pigeon/qr` only).
2. **Live ceremony** (`pairing.Pairer.Accept` → token): not linked yet.
   `POST /stream/pair` returns a clear error until T93 resolves the dep
   graph (upgrade go-ios quic-go, or move live ceremony to a nested
   module / helper binary). The API shape and spyder-owns-QR decision
   are locked either way.

## State on disk

```
~/.spyder/pairing/
  identity.json   # long-term acceptor identity (inject into ge)
  pairings.json   # peer_instance_id → PairingRecord
```

## Relation to live WS path

T92.2 WebSocket `streamrelay` remains the live player↔server pipe until
T93 cutover. Pairing-host routes can land first; they do not move video
off WebSocket by themselves.
