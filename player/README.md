# Spyder player

Native stream glass for headless ge game servers. Connects to **spyder**'s
stream relay (`spyder serve`, default `:3030`), speaks the GE wire
(H.264 / SP2I / SP2S / SP2T), and renders + forwards input.

## Coupling rule

**ge and spyder interact only through protocols** (stream wire + app
channel). This tree does **not** link `libge.a`, does **not** take a
`GE_ROOT`, and does **not** include ge headers from a live checkout.

Wire layout lives under `include/wire/Protocol.h` (byte-compatible with
ge's server encoder). Client implementation is owned here under
`include/player/` and `src/` (`spyder::` namespace).

## Build (desktop)

```bash
make player          # from spyder root → bin/player
make -C player check-wire   # magics + sizeof oracle
```

```bash
bin/player --host localhost --port 3030 --name tiltbuggy
```

## Mobile

Packaging shells under `ios/` and `android/` build against **this**
tree only (`PLAYER_ROOT` + `vendor/`).

```bash
# iOS — needs vendor/sdl3/lib/ios-arm64{,-simulator}
cd player/ios && cmake -B build/xcode -G Xcode
cmake --build build/xcode --config Debug --target Player

# Android — needs vendor/github.com/libsdl-org/SDL* + vendor/ffmpeg android-arm64
cd player/android && ./gradlew :app:assembleDebug
```

## Vendor

`vendor/` is a one-time seed of third-party code and platform prebuilts
(SDL sources, SDL static libs per platform, FFmpeg android-arm64, spdlog,
asio, lunasvg, …). It is **not** a live dependency on a ge checkout.

## Wire protocol

When the stream wire changes, update **both**:

| Repo | File |
|------|------|
| spyder | `player/include/wire/Protocol.h` |
| ge | `include/ge/Protocol.h` |

`make -C player check-wire` is the player's layout oracle. Magics are
ASCII `SP2x` and must stay identical on both sides. From the spyder
repo root, `make check-wire` also runs `./scripts/check-wire-ge.sh`, which
compares magics and `kProtocolVersion` against `../ge/include/ge/Protocol.h`
when that checkout exists (`SPYDER_GE_ROOT` overrides the path). If ge is
absent, the cross-repo step skips closed; the local sizeof oracle still
runs. Regression: `./scripts/check-wire-ge-test.sh`.

### Submodule object-store independence

The four `vendor/github.com/` source submodules must not borrow Git objects
from ge or another checkout. After `git submodule update --init` (a fresh
clone has no alternates by default), audit the module stores with
`./scripts/check-player-submodules.sh` from the spyder root. This checks the
shared Git module stores even when invoked from a worktree that has not
checked out the submodules. It fails on missing stores, any alternate, or
`git fsck` errors.

For a checkout that was seeded with `git clone --shared` / `--reference` or
borrowed ge's object store, run `./scripts/check-player-submodules.sh --repair`
**before** removing the ge checkout. Repair repacks reachable objects while
the borrowed store is still present, removes the alternate, and verifies
with `git fsck`; it restores the pointer if verification fails. A missing
borrowed store needs recovery from the submodule's upstream before repair.
Do not just delete `objects/info/alternates`: objects reachable only through
that file would disappear. The fixture regression is
`./scripts/check-player-submodules-test.sh`.

### Zero-install browser link (development bundle)

Build the optional wasm glass with `make player-web` (Emscripten required) and
serve it from the daemon at `/player/`. Use a daemon address reachable from the
phone on the same LAN; `localhost` on a phone refers to the phone, not your Mac.
The relay's WebSocket origin check requires the link's HTTP host and port to be
the same as the stream relay. This path is HTTP/WS only; HTTPS/WSS needs a
separate transport change. For example:

```sh
python3 scripts/player-pair.py --host 192.168.1.23 --port 3030 --name tiltbuggy --qr /tmp/tiltbuggy.png
# prints http://192.168.1.23:3030/player/?host=192.168.1.23&port=3030&name=tiltbuggy
```

Open the printed URL directly or scan the PNG with the phone camera. The page
auto-connects, with a phone device class by default. `qrencode` is optional
unless `--qr` is requested; the QR is just a link and does not authenticate or
create a server. Start the named ge streaming server and ensure the daemon is
reachable from the phone first. The native player and its Keychain state are
not used or modified by this browser path. Generating a QR is not proof of a
successful physical phone scan/play session.
